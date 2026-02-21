package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/circuitbreaker"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/httpapi"
	"github.com/jordanhubbard/tokenhub/internal/idempotency"
	"github.com/jordanhubbard/tokenhub/internal/logging"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/providers/anthropic"
	"github.com/jordanhubbard/tokenhub/internal/providers/openai"
	"github.com/jordanhubbard/tokenhub/internal/providers/vllm"
	"github.com/jordanhubbard/tokenhub/internal/ratelimit"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/store"
	temporalpkg "github.com/jordanhubbard/tokenhub/internal/temporal"
	"github.com/jordanhubbard/tokenhub/internal/tracing"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

type Server struct {
	cfg Config

	r *chi.Mux

	vault            *vault.Vault
	engine           *router.Engine
	store            store.Store
	logger           *slog.Logger
	temporal         *temporalpkg.Manager // nil when Temporal disabled
	prober           *health.Prober       // nil when no probeable adapters
	rateLimiter      *ratelimit.Limiter
	idempotencyCache *idempotency.Cache          // nil when idempotency disabled
	otelShutdown     func(context.Context) error // nil when OTel disabled
	stopBandit       func()                      // nil when Thompson Sampling disabled
	tsdb             *tsdb.Store                 // nil when TSDB failed to init

	stopPrune    chan struct{} // signals TSDB prune goroutine to stop
	stopLogPrune chan struct{} // signals log prune goroutine to stop
	stopRotation chan struct{} // signals key rotation enforcement goroutine to stop
	apiKeyMgr    *apikey.Manager
	eventBus     *events.Bus
}

func NewServer(cfg Config) (*Server, error) {
	logger := logging.Setup(cfg.LogLevel)

	// Initialize OpenTelemetry tracing (opt-in).
	otelShutdown, err := tracing.Setup(tracing.Config{
		Enabled:     cfg.OTelEnabled,
		Endpoint:    cfg.OTelEndpoint,
		ServiceName: cfg.OTelServiceName,
	})
	if err != nil {
		return nil, fmt.Errorf("otel setup: %w", err)
	}
	if cfg.OTelEnabled {
		logger.Info("opentelemetry tracing enabled",
			slog.String("endpoint", cfg.OTelEndpoint),
			slog.String("service", cfg.OTelServiceName),
		)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(logging.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	if cfg.OTelEnabled {
		r.Use(tracing.Middleware())
	}
	corsOrigins := cfg.CORSOrigins
	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	m := metrics.New()

	// Per-IP rate limiting (applied only to /v1 routes, not healthz/metrics/admin).
	rl := ratelimit.New(cfg.RateLimitRPS, cfg.RateLimitBurst, time.Second,
		ratelimit.WithCounter(m.RateLimitedTotal))

	v, err := vault.New(cfg.VaultEnabled)
	if err != nil {
		return nil, err
	}

	eng := router.NewEngine(router.EngineConfig{
		DefaultMode:         cfg.DefaultMode,
		DefaultMaxBudgetUSD: cfg.DefaultMaxBudget,
		DefaultMaxLatencyMs: cfg.DefaultMaxLatencyMs,
	})

	// Open store.
	db, err := store.NewSQLite(cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	logger.Info("database initialized", slog.String("dsn", cfg.DBDSN))

	// Restore vault salt from DB (for credential persistence across restarts).
	if salt, data, err := db.LoadVaultBlob(context.Background()); err == nil && salt != nil {
		v.SetSalt(salt)
		logger.Info("restored vault salt from database")
		if data != nil {
			_ = v.Import(data)
			logger.Info("restored vault credentials", slog.Int("keys", len(data)))
		}
	}

	// Set up health tracking.
	ht := health.NewTracker(health.DefaultConfig())
	eng.SetHealthChecker(ht)

	// Register provider adapters from environment.
	timeout := time.Duration(cfg.ProviderTimeoutSecs) * time.Second
	registerProviders(eng, timeout, logger)

	// Load additional providers from credentials file (~/.tokenhub/credentials).
	loadCredentialsFile(cfg.CredentialsFile, eng, timeout, logger)

	// Load persisted providers from DB and register adapters so they're
	// included in health probing and routing from the moment we start.
	loadPersistedProviders(eng, v, db, timeout, logger)

	// Start health check prober for registered adapters (disable with TOKENHUB_HEALTH_PROBE_DISABLED=true).
	var prober *health.Prober
	if os.Getenv("TOKENHUB_HEALTH_PROBE_DISABLED") != "true" {
		var probeTargets []health.Probeable
		for _, id := range eng.ListAdapterIDs() {
			if p, ok := eng.GetAdapter(id).(health.Probeable); ok {
				probeTargets = append(probeTargets, p)
			}
		}
		if len(probeTargets) > 0 {
			prober = health.NewProber(health.DefaultProberConfig(), ht, probeTargets, logger)
			prober.Start()
			logger.Info("health prober started", slog.Int("targets", len(probeTargets)))
		}
	} else {
		logger.Info("health probing disabled via TOKENHUB_HEALTH_PROBE_DISABLED")
	}

	// Register default models, then load any persisted models from DB.
	registerDefaultModels(eng)
	loadPersistedModels(eng, db, logger)
	loadRoutingConfig(eng, db, logger)

	// Startup validation: warn if system cannot route requests.
	adapterIDs := eng.ListAdapterIDs()
	modelList := eng.ListModels()
	if len(adapterIDs) == 0 {
		logger.Warn("NO PROVIDERS REGISTERED — set TOKENHUB_OPENAI_API_KEY, TOKENHUB_ANTHROPIC_API_KEY, or TOKENHUB_VLLM_ENDPOINTS")
	}
	if len(modelList) == 0 {
		logger.Warn("NO MODELS REGISTERED — requests will fail until models are configured")
	} else {
		enabledCount := 0
		for _, m := range modelList {
			if m.Enabled {
				enabledCount++
			}
		}
		if enabledCount == 0 {
			logger.Warn("ALL MODELS DISABLED — requests will fail until models are enabled")
		} else {
			logger.Info("startup ready", slog.Int("providers", len(adapterIDs)), slog.Int("models", enabledCount))
		}
	}

	// Initialize Thompson Sampling bandit policy.
	sampler := router.NewThompsonSampler()
	eng.SetBanditPolicy(sampler)
	fetchRewards := func() ([]router.RewardSummaryRow, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		summaries, err := db.GetRewardSummary(ctx)
		if err != nil {
			return nil, err
		}
		rows := make([]router.RewardSummaryRow, len(summaries))
		for i, s := range summaries {
			rows[i] = router.RewardSummaryRow{
				ModelID:     s.ModelID,
				TokenBucket: s.TokenBucket,
				Count:       s.Count,
				Successes:   s.Successes,
				SumReward:   s.SumReward,
			}
		}
		return rows, nil
	}
	stopBandit := router.StartRefreshLoop(router.DefaultRefreshConfig(), sampler, fetchRewards, logger)
	logger.Info("thompson sampling bandit policy initialized")

	// Initialize API key manager and budget checker.
	keyMgr := apikey.NewManager(db)
	budgetChecker := apikey.NewBudgetChecker(db)

	bus := events.NewBus()
	sc := stats.NewCollector()

	// Initialize embedded TSDB.
	ts, err := tsdb.New(db.DB())
	if err != nil {
		logger.Warn("failed to initialize TSDB", slog.String("error", err.Error()))
	}

	// Initialize idempotency cache (5-minute TTL, 10k max entries).
	idemCache := idempotency.New(5*time.Minute, 10000)
	logger.Info("idempotency cache initialized", slog.Duration("ttl", 5*time.Minute), slog.Int("max_entries", 10000))

	s := &Server{
		cfg:              cfg,
		r:                r,
		vault:            v,
		engine:           eng,
		store:            db,
		logger:           logger,
		prober:           prober,
		rateLimiter:      rl,
		idempotencyCache: idemCache,
		otelShutdown:     otelShutdown,
		stopBandit:       stopBandit,
		tsdb:             ts,
		stopPrune:        make(chan struct{}),
		stopLogPrune:     make(chan struct{}),
		stopRotation:     make(chan struct{}),
		apiKeyMgr:        keyMgr,
		eventBus:         bus,
	}

	// Start TSDB auto-prune goroutine.
	if ts != nil {
		go s.tsdbPruneLoop(ts)
	}

	// Start log retention prune goroutine (every 6h, 90-day retention).
	go s.logPruneLoop()

	// Start API key rotation enforcement goroutine.
	go s.rotationEnforceLoop()

	// Log security warnings.
	if cfg.AdminToken == "" {
		logger.Warn("TOKENHUB_ADMIN_TOKEN not set — admin endpoints are UNPROTECTED")
	}
	if len(cfg.CORSOrigins) == 0 {
		logger.Warn("TOKENHUB_CORS_ORIGINS not set — CORS allows all origins")
	}

	// Initialize Temporal circuit breaker.
	cb := circuitbreaker.New(
		circuitbreaker.WithThreshold(3),
		circuitbreaker.WithCooldown(30*time.Second),
		circuitbreaker.WithOnStateChange(func(from, to circuitbreaker.State) {
			logger.Warn("temporal circuit breaker state change",
				slog.String("from", from.String()),
				slog.String("to", to.String()),
			)
			m.TemporalCircuitState.Set(float64(to))
		}),
	)

	deps := httpapi.Dependencies{
		Engine:           eng,
		Vault:            v,
		Metrics:          m,
		Store:            db,
		Health:           ht,
		EventBus:         bus,
		Stats:            sc,
		TSDB:             ts,
		APIKeyMgr:        keyMgr,
		BudgetChecker:    budgetChecker,
		AdminToken:       cfg.AdminToken,
		IdempotencyCache: idemCache,
		CircuitBreaker:   cb,
		RateLimiter:      rl,
		ProviderTimeout:  time.Duration(cfg.ProviderTimeoutSecs) * time.Second,
	}

	// Initialize Temporal workflow engine if enabled.
	if cfg.TemporalEnabled {
		acts := &temporalpkg.Activities{
			Engine:   eng,
			Store:    db,
			Health:   ht,
			Metrics:  m,
			EventBus: bus,
			Stats:    sc,
			TSDB:     ts,
		}
		tmgr, err := temporalpkg.New(temporalpkg.Config{
			HostPort:  cfg.TemporalHostPort,
			Namespace: cfg.TemporalNamespace,
			TaskQueue: cfg.TemporalTaskQueue,
		}, acts)
		if err != nil {
			logger.Error("failed to initialize Temporal", slog.String("error", err.Error()))
			// Non-fatal: fall back to direct engine calls.
		} else {
			if err := tmgr.Start(); err != nil {
				logger.Error("failed to start Temporal worker", slog.String("error", err.Error()))
				tmgr.Stop()
			} else {
				s.temporal = tmgr
				deps.TemporalClient = tmgr.Client()
				deps.TemporalTaskQueue = cfg.TemporalTaskQueue
				m.TemporalUp.Set(1)
				logger.Info("temporal workflow engine started",
					slog.String("host", cfg.TemporalHostPort),
					slog.String("namespace", cfg.TemporalNamespace),
					slog.String("task_queue", cfg.TemporalTaskQueue),
				)
			}
		}
	}

	httpapi.MountRoutes(r, deps)

	return s, nil
}

func (s *Server) Router() http.Handler { return s.r }

// Reload applies hot-reloadable configuration parameters at runtime without
// restarting the server. It updates rate limiter settings, routing policy
// defaults, and the log level.
func (s *Server) Reload(cfg Config) {
	s.rateLimiter.UpdateLimits(cfg.RateLimitRPS, cfg.RateLimitBurst)
	s.engine.UpdateDefaults(cfg.DefaultMode, cfg.DefaultMaxBudget, cfg.DefaultMaxLatencyMs)
	logging.SetLevel(cfg.LogLevel)
	s.cfg = cfg
	s.logger.Info("configuration reloaded",
		slog.Int("rate_limit_rps", cfg.RateLimitRPS),
		slog.Int("rate_limit_burst", cfg.RateLimitBurst),
		slog.String("default_mode", cfg.DefaultMode),
		slog.Float64("default_max_budget", cfg.DefaultMaxBudget),
		slog.Int("default_max_latency_ms", cfg.DefaultMaxLatencyMs),
		slog.String("log_level", cfg.LogLevel),
	)
}

func (s *Server) Close() error {
	close(s.stopPrune)
	close(s.stopLogPrune)
	close(s.stopRotation)
	if s.stopBandit != nil {
		s.stopBandit()
	}
	if s.prober != nil {
		s.prober.Stop()
	}
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
	if s.idempotencyCache != nil {
		s.idempotencyCache.Stop()
	}
	if s.temporal != nil {
		s.temporal.Stop()
	}
	if s.otelShutdown != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.otelShutdown(ctx); err != nil {
			s.logger.Warn("otel shutdown error", slog.String("error", err.Error()))
		}
	}
	if s.tsdb != nil {
		s.tsdb.Stop()
	}
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

func (s *Server) tsdbPruneLoop(ts *tsdb.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			deleted, err := ts.Prune(ctx)
			cancel()
			if err != nil {
				s.logger.Warn("TSDB prune failed", slog.String("error", err.Error()))
			} else if deleted > 0 {
				s.logger.Info("TSDB pruned", slog.Int64("deleted", deleted))
			}
		case <-s.stopPrune:
			return
		}
	}
}

// logPruneLoop periodically deletes old rows from request_logs, audit_logs,
// and reward_logs. Runs every 6 hours with a 90-day retention window.
func (s *Server) logPruneLoop() {
	const retention = 90 * 24 * time.Hour
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			deleted, err := s.store.PruneOldLogs(ctx, retention)
			cancel()
			if err != nil {
				s.logger.Warn("log prune failed", slog.String("error", err.Error()))
			} else if deleted > 0 {
				s.logger.Info("old logs pruned", slog.Int64("deleted", deleted))
			}
		case <-s.stopLogPrune:
			return
		}
	}
}

// rotationEnforceLoop periodically checks for API keys that have exceeded
// their rotation period and disables them.
func (s *Server) rotationEnforceLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			count, err := s.apiKeyMgr.EnforceRotation(ctx, s.eventBus, s.logger)
			cancel()
			if err != nil {
				s.logger.Warn("key rotation enforcement failed", slog.String("error", err.Error()))
			} else if count > 0 {
				s.logger.Info("key rotation enforcement completed", slog.Int("disabled", count))
			}
		case <-s.stopRotation:
			return
		}
	}
}

func registerProviders(eng *router.Engine, timeout time.Duration, logger *slog.Logger) {
	if key := os.Getenv("TOKENHUB_OPENAI_API_KEY"); key != "" {
		eng.RegisterAdapter(openai.New("openai", key, "https://api.openai.com", openai.WithTimeout(timeout)))
		logger.Info("registered provider", slog.String("provider", "openai"))
	}

	if key := os.Getenv("TOKENHUB_ANTHROPIC_API_KEY"); key != "" {
		eng.RegisterAdapter(anthropic.New("anthropic", key, "https://api.anthropic.com", anthropic.WithTimeout(timeout)))
		logger.Info("registered provider", slog.String("provider", "anthropic"))
	}

	if endpoints := os.Getenv("TOKENHUB_VLLM_ENDPOINTS"); endpoints != "" {
		var eps []string
		for _, ep := range strings.Split(endpoints, ",") {
			ep = strings.TrimSpace(ep)
			if ep != "" {
				eps = append(eps, ep)
			}
		}
		if len(eps) > 0 {
			opts := []vllm.Option{vllm.WithTimeout(timeout)}
			if len(eps) > 1 {
				opts = append(opts, vllm.WithEndpoints(eps[1:]...))
			}
			eng.RegisterAdapter(vllm.New("vllm", eps[0], opts...))
			logger.Info("registered provider", slog.String("provider", "vllm"), slog.Int("endpoints", len(eps)))
		}
	}

	// TOKENHUB_EXTRA_PROVIDERS: JSON array of additional OpenAI-compatible
	// providers. Each entry: {"id":"...","endpoint":"...","api_key":"..."}.
	// This allows registering multiple providers with custom endpoints
	// (e.g. NVIDIA NIM, Azure OpenAI, etc.) without code changes.
	if extra := os.Getenv("TOKENHUB_EXTRA_PROVIDERS"); extra != "" {
		type extraProvider struct {
			ID       string `json:"id"`
			Endpoint string `json:"endpoint"`
			APIKey   string `json:"api_key"`
		}
		var providers []extraProvider
		if err := json.Unmarshal([]byte(extra), &providers); err != nil {
			logger.Warn("failed to parse TOKENHUB_EXTRA_PROVIDERS", slog.String("error", err.Error()))
		} else {
			for _, p := range providers {
				if p.ID == "" || p.Endpoint == "" || p.APIKey == "" {
					logger.Warn("skipping extra provider: id, endpoint, and api_key required", slog.String("id", p.ID))
					continue
				}
				eng.RegisterAdapter(openai.New(p.ID, p.APIKey, p.Endpoint, openai.WithTimeout(timeout)))
				logger.Info("registered extra provider", slog.String("provider", p.ID), slog.String("endpoint", p.Endpoint))
			}
		}
	}
}

// loadCredentialsFile reads a JSON credentials file (similar to ~/.netrc) and
// registers any providers and models it contains. The file is optional; if it
// does not exist the function silently returns. The file must be owner-readable
// only (mode 0600 or 0400) to prevent accidental secret exposure.
func loadCredentialsFile(path string, eng *router.Engine, timeout time.Duration, logger *slog.Logger) {
	if path == "" {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		logger.Warn("credentials file stat error", slog.String("path", path), slog.String("error", err.Error()))
		return
	}

	// Enforce restrictive permissions (owner-only read/write).
	if mode := info.Mode().Perm(); mode&0077 != 0 {
		logger.Warn("credentials file has insecure permissions, skipping",
			slog.String("path", path),
			slog.String("mode", fmt.Sprintf("%04o", mode)),
			slog.String("required", "0600 or stricter"),
		)
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		logger.Warn("failed to read credentials file", slog.String("path", path), slog.String("error", err.Error()))
		return
	}

	type credProvider struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Endpoint string `json:"endpoint"`
		APIKey   string `json:"api_key"`
	}
	type credModel struct {
		ID               string  `json:"id"`
		ProviderID       string  `json:"provider_id"`
		Weight           int     `json:"weight"`
		MaxContextTokens int     `json:"max_context_tokens"`
		InputPer1K       float64 `json:"input_per_1k"`
		OutputPer1K      float64 `json:"output_per_1k"`
		Enabled          bool    `json:"enabled"`
	}
	type credFile struct {
		Providers []credProvider `json:"providers"`
		Models    []credModel    `json:"models"`
	}

	var creds credFile
	if err := json.Unmarshal(data, &creds); err != nil {
		logger.Warn("failed to parse credentials file", slog.String("path", path), slog.String("error", err.Error()))
		return
	}

	for _, p := range creds.Providers {
		if p.ID == "" || p.Endpoint == "" || p.APIKey == "" {
			logger.Warn("skipping credentials provider: id, endpoint, and api_key required", slog.String("id", p.ID))
			continue
		}
		switch p.Type {
		case "anthropic":
			eng.RegisterAdapter(anthropic.New(p.ID, p.APIKey, p.Endpoint, anthropic.WithTimeout(timeout)))
		default:
			eng.RegisterAdapter(openai.New(p.ID, p.APIKey, p.Endpoint, openai.WithTimeout(timeout)))
		}
		logger.Info("registered provider from credentials file", slog.String("provider", p.ID), slog.String("endpoint", p.Endpoint))
	}

	for _, m := range creds.Models {
		if m.ID == "" || m.ProviderID == "" {
			logger.Warn("skipping credentials model: id and provider_id required", slog.String("id", m.ID))
			continue
		}
		eng.RegisterModel(router.Model{
			ID:               m.ID,
			ProviderID:       m.ProviderID,
			Weight:           m.Weight,
			MaxContextTokens: m.MaxContextTokens,
			InputPer1K:       m.InputPer1K,
			OutputPer1K:      m.OutputPer1K,
			Enabled:          m.Enabled,
		})
		logger.Info("registered model from credentials file", slog.String("model", m.ID), slog.String("provider", m.ProviderID))
	}

	logger.Info("loaded credentials file",
		slog.String("path", path),
		slog.Int("providers", len(creds.Providers)),
		slog.Int("models", len(creds.Models)),
	)
}

// loadPersistedProviders reads provider records from the database and creates
// runtime adapters for any that don't already have one registered (e.g. from
// env vars). This ensures providers added via the admin API or bootstrap.local
// survive restarts without re-running the bootstrap script.
func loadPersistedProviders(eng *router.Engine, v *vault.Vault, db store.Store, timeout time.Duration, logger *slog.Logger) {
	providers, err := db.ListProviders(context.Background())
	if err != nil {
		logger.Warn("failed to load persisted providers", slog.String("error", err.Error()))
		return
	}
	existingAdapters := make(map[string]bool)
	for _, id := range eng.ListAdapterIDs() {
		existingAdapters[id] = true
	}
	logger.Info("loadPersistedProviders", slog.Int("db_providers", len(providers)), slog.Int("existing_adapters", len(existingAdapters)))
	registered := 0
	for _, p := range providers {
		logger.Info("checking provider", slog.String("id", p.ID), slog.Bool("enabled", p.Enabled), slog.String("base_url", p.BaseURL), slog.Bool("already_registered", existingAdapters[p.ID]))
		if !p.Enabled || p.BaseURL == "" {
			continue
		}
		if existingAdapters[p.ID] {
			continue
		}
		apiKey := ""
		if p.CredStore == "vault" && v != nil && !v.IsLocked() {
			apiKey, _ = v.Get("provider:" + p.ID + ":api_key")
		}
		switch p.Type {
		case "anthropic":
			eng.RegisterAdapter(anthropic.New(p.ID, apiKey, p.BaseURL, anthropic.WithTimeout(timeout)))
		case "vllm":
			eng.RegisterAdapter(vllm.New(p.ID, p.BaseURL, vllm.WithTimeout(timeout)))
		default:
			eng.RegisterAdapter(openai.New(p.ID, apiKey, p.BaseURL, openai.WithTimeout(timeout)))
		}
		registered++
		logger.Info("registered persisted provider", slog.String("provider", p.ID), slog.String("type", p.Type), slog.String("base_url", p.BaseURL))
	}
	if registered > 0 {
		logger.Info("loaded persisted providers", slog.Int("count", registered))
	}
}

func loadPersistedModels(eng *router.Engine, db store.Store, logger *slog.Logger) {
	models, err := db.ListModels(context.Background())
	if err != nil {
		logger.Warn("failed to load persisted models", slog.String("error", err.Error()))
		return
	}
	for _, m := range models {
		eng.RegisterModel(router.Model{
			ID:               m.ID,
			ProviderID:       m.ProviderID,
			Weight:           m.Weight,
			MaxContextTokens: m.MaxContextTokens,
			InputPer1K:       m.InputPer1K,
			OutputPer1K:      m.OutputPer1K,
			Enabled:          m.Enabled,
		})
	}
	if len(models) > 0 {
		logger.Info("loaded persisted models", slog.Int("count", len(models)))
	}
}

func loadRoutingConfig(eng *router.Engine, db store.Store, logger *slog.Logger) {
	cfg, err := db.LoadRoutingConfig(context.Background())
	if err != nil {
		logger.Warn("failed to load routing config", slog.String("error", err.Error()))
		return
	}
	if cfg.DefaultMode != "" {
		eng.UpdateDefaults(cfg.DefaultMode, cfg.DefaultMaxBudgetUSD, cfg.DefaultMaxLatencyMs)
		logger.Info("loaded routing config from DB",
			slog.String("mode", cfg.DefaultMode),
			slog.Float64("budget", cfg.DefaultMaxBudgetUSD),
			slog.Int("latency_ms", cfg.DefaultMaxLatencyMs),
		)
	}
}

func registerDefaultModels(eng *router.Engine) {
	defaults := []router.Model{
		{ID: "gpt-4o", ProviderID: "openai", Weight: 8, MaxContextTokens: 128000, InputPer1K: 0.0025, OutputPer1K: 0.01, Enabled: true},
		{ID: "gpt-4o-mini", ProviderID: "openai", Weight: 3, MaxContextTokens: 128000, InputPer1K: 0.00015, OutputPer1K: 0.0006, Enabled: true},
		{ID: "claude-opus-4-0520", ProviderID: "anthropic", Weight: 10, MaxContextTokens: 200000, InputPer1K: 0.015, OutputPer1K: 0.075, Enabled: true},
		{ID: "claude-sonnet-4-5-20250929", ProviderID: "anthropic", Weight: 7, MaxContextTokens: 200000, InputPer1K: 0.003, OutputPer1K: 0.015, Enabled: true},
	}
	for _, m := range defaults {
		eng.RegisterModel(m)
	}
}
