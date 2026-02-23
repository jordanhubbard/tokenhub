package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	stopPricing    chan struct{} // signals pricing refresh goroutine to stop
	stopHeartbeat  chan struct{} // signals heartbeat goroutine to stop
	apiKeyMgr    *apikey.Manager
	eventBus     *events.Bus

	storeWriteQueue chan func()      // buffered channel for async store writes
	storeWriteDone  chan struct{}    // closed by the write worker when it exits

	httpServer *http.Server // set via SetHTTPServer; used by Close() to drain in-flight requests
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
		ExplorationTemp:     cfg.ExplorationTemp,
	})
	eng.SetSkipRecorder(m)

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

	// Auto-unlock vault from environment if TOKENHUB_VAULT_PASSWORD is set.
	// This allows headless/automated deployments to skip interactive unlock.
	if cfg.VaultPassword != "" && cfg.VaultEnabled {
		logger.Warn("TOKENHUB_VAULT_PASSWORD is set: vault password is visible in the process environment — prefer a secrets manager or encrypted secret store in production")
		if err := v.Unlock([]byte(cfg.VaultPassword)); err != nil {
			logger.Error("failed to auto-unlock vault from TOKENHUB_VAULT_PASSWORD", slog.String("error", err.Error()))
		} else {
			logger.Info("vault auto-unlocked from TOKENHUB_VAULT_PASSWORD")
			// Persist the vault blob so first-time setup also works headless.
			if salt := v.Salt(); salt != nil {
				data := v.Export()
				if err := db.SaveVaultBlob(context.Background(), salt, data); err != nil {
					logger.Warn("failed to persist vault blob after auto-unlock", slog.String("error", err.Error()))
				}
			}
		}
	}

	// Set up health tracking.
	ht := health.NewTracker(health.DefaultConfig(), health.WithOnUpdate(func(providerID string, state health.State) {
		var v float64
		switch state {
		case health.StateHealthy:
			v = 2
		case health.StateDegraded:
			v = 1
		default: // StateDown
			v = 0
		}
		m.ProviderHealthState.WithLabelValues(providerID).Set(v)
	}))
	eng.SetHealthChecker(ht)

	timeout := time.Duration(cfg.ProviderTimeoutSecs) * time.Second

	// Load providers from credentials file (~/.tokenhub/credentials).
	loadCredentialsFile(cfg.CredentialsFile, eng, v, db, timeout, logger)

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

	// Load persisted models from DB.
	loadPersistedModels(eng, db, logger)
	loadRoutingConfig(eng, db, logger)

	// Startup validation: warn if system cannot route requests.
	adapterIDs := eng.ListAdapterIDs()
	modelList := eng.ListModels()
	if len(adapterIDs) == 0 {
		logger.Warn("NO PROVIDERS REGISTERED — configure ~/.tokenhub/credentials, or use the admin API, tokenhubctl, or the UI to add providers")
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
	seedStatsFromDB(sc, db, logger)

	// Initialize embedded TSDB.
	ts, err := tsdb.New(db.DB())
	if err != nil {
		logger.Warn("failed to initialize TSDB", slog.String("error", err.Error()))
	}

	// Initialize idempotency cache (5-minute TTL, 10k max entries).
	idemCache := idempotency.New(5*time.Minute, 10000)
	logger.Info("idempotency cache initialized", slog.Duration("ttl", 5*time.Minute), slog.Int("max_entries", 10000))

	// Async store write queue: decouples SQLite writes from handler goroutines.
	// The channel is closed in Close() after HTTP drain to flush remaining writes.
	storeWriteQueue := make(chan func(), 4096)
	storeWriteDone := make(chan struct{})
	go func() {
		defer close(storeWriteDone)
		for fn := range storeWriteQueue {
			fn()
		}
	}()

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
		stopPricing:      make(chan struct{}),
		stopHeartbeat:    make(chan struct{}),
		apiKeyMgr:        keyMgr,
		eventBus:         bus,
		storeWriteQueue:  storeWriteQueue,
		storeWriteDone:   storeWriteDone,
	}

	// Start TSDB auto-prune goroutine.
	if ts != nil {
		go s.tsdbPruneLoop(ts)
	}

	// Start log retention prune goroutine (every 6h, 90-day retention).
	go s.logPruneLoop()

	// Start API key rotation enforcement goroutine.
	go s.rotationEnforceLoop()

	// Start pricing refresh goroutine (polls LiteLLM pricing JSON).
	if cfg.PricingRefreshEnabled {
		go s.pricingRefreshLoop()
	}

	// Start heartbeat goroutine.
	go s.heartbeatLoop(m, bus)

	// Ensure admin endpoints are always protected. Auto-generate a token if
	// the operator didn't set one, and log it so they can use it.
	if cfg.AdminToken == "" {
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			return nil, fmt.Errorf("generate admin token: %w", err)
		}
		cfg.AdminToken = hex.EncodeToString(tokenBytes)
		logger.Warn("TOKENHUB_ADMIN_TOKEN not set — auto-generated token written to data dir (retrieve with: tokenhubctl admin-token)")
	}
	writeStateEnv(cfg.DBDSN, cfg.AdminToken, logger)
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
		RateLimitRPS:     cfg.RateLimitRPS,
		ProviderTimeout:  time.Duration(cfg.ProviderTimeoutSecs) * time.Second,
		Prober:           prober,
		StoreWriteQueue:  storeWriteQueue,
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

// SetHTTPServer registers the HTTP server so that Close() can drain in-flight
// requests via http.Server.Shutdown before releasing other resources.
func (s *Server) SetHTTPServer(srv *http.Server) {
	s.httpServer = srv
}

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
	// Drain in-flight HTTP requests before stopping background workers.
	if s.httpServer != nil {
		drainSecs := s.cfg.ShutdownDrainSecs
		if drainSecs <= 0 {
			drainSecs = 30
		}
		drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Duration(drainSecs)*time.Second)
		defer drainCancel()
		if err := s.httpServer.Shutdown(drainCtx); err != nil {
			s.logger.Warn("HTTP drain error", slog.String("error", err.Error()))
		}
	}

	close(s.stopPrune)
	close(s.stopLogPrune)
	close(s.stopRotation)
	if s.stopPricing != nil {
		close(s.stopPricing)
	}
	close(s.stopHeartbeat)
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
	// Flush all remaining async store writes before closing the store.
	// HTTP is already drained, so no new writes will be enqueued.
	if s.storeWriteQueue != nil {
		close(s.storeWriteQueue)
		<-s.storeWriteDone
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

const litellmPricingURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

type litellmEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	MaxInputTokens     int     `json:"max_input_tokens"`
}

func (s *Server) pricingRefreshLoop() {
	interval := time.Duration(s.cfg.PricingRefreshIntervalSecs) * time.Second
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.refreshPricing() // run immediately on startup
	for {
		select {
		case <-ticker.C:
			s.refreshPricing()
		case <-s.stopPricing:
			return
		}
	}
}

func (s *Server) refreshPricing() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, litellmPricingURL, nil)
	if err != nil {
		s.logger.Warn("pricing refresh: build request failed", slog.String("error", err.Error()))
		return
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		s.logger.Warn("pricing refresh: fetch failed", slog.String("error", err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		s.logger.Warn("pricing refresh: unexpected status", slog.Int("status", resp.StatusCode))
		return
	}

	var pricing map[string]litellmEntry
	if err := json.NewDecoder(resp.Body).Decode(&pricing); err != nil {
		s.logger.Warn("pricing refresh: decode failed", slog.String("error", err.Error()))
		return
	}

	// Build set of local (self-hosted) provider IDs to skip.
	providers, _ := s.store.ListProviders(ctx)
	localProviders := make(map[string]bool)
	for _, p := range providers {
		if p.Type == "vllm" {
			localProviders[p.ID] = true
		}
	}

	models, _ := s.store.ListModels(ctx)
	updated := 0
	for _, m := range models {
		if localProviders[m.ProviderID] {
			continue // skip self-hosted models
		}
		// Don't overwrite manually-set pricing.
		if m.PricingSource == "manual" && (m.InputPer1K != 0 || m.OutputPer1K != 0) {
			continue
		}
		entry, ok := pricing[m.ID]
		if !ok {
			continue
		}
		m.InputPer1K = entry.InputCostPerToken * 1000
		m.OutputPer1K = entry.OutputCostPerToken * 1000
		m.PricingSource = "litellm"
		if entry.MaxInputTokens > 0 && m.MaxContextTokens == 0 {
			m.MaxContextTokens = entry.MaxInputTokens
		}
		if err := s.store.UpsertModel(ctx, m); err == nil {
			s.engine.RegisterModel(router.Model{
				ID:               m.ID,
				ProviderID:       m.ProviderID,
				Weight:           m.Weight,
				MaxContextTokens: m.MaxContextTokens,
				InputPer1K:       m.InputPer1K,
				OutputPer1K:      m.OutputPer1K,
				Enabled:          m.Enabled,
				PricingSource:    m.PricingSource,
			})
			updated++
		}
	}
	s.logger.Info("pricing refresh complete", slog.Int("updated", updated))
}

// seedStatsFromDB loads recent request logs from the database to pre-populate
// the in-memory stats collector so the dashboard isn't blank after a restart.
func seedStatsFromDB(sc *stats.Collector, db store.Store, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logs, err := db.ListRequestLogs(ctx, 5000, 0)
	if err != nil {
		logger.Warn("failed to seed stats from DB", slog.String("error", err.Error()))
		return
	}
	if len(logs) == 0 {
		return
	}
	snapshots := make([]stats.Snapshot, 0, len(logs))
	for _, l := range logs {
		snapshots = append(snapshots, stats.Snapshot{
			Timestamp:    l.Timestamp,
			ModelID:      l.ModelID,
			ProviderID:   l.ProviderID,
			LatencyMs:    float64(l.LatencyMs),
			CostUSD:      l.EstimatedCostUSD,
			Success:      l.StatusCode < 500,
			InputTokens:  l.InputTokens,
			OutputTokens: l.OutputTokens,
		})
	}
	sc.Seed(snapshots)
	logger.Info("seeded stats from DB", slog.Int("snapshots", len(snapshots)))
}

// heartbeatLoop emits a periodic heartbeat event to the event bus and
// increments the Prometheus heartbeat counter. External monitors can alert
// if the counter stops incrementing, which would indicate a hung process.
func (s *Server) heartbeatLoop(m *metrics.Registry, bus *events.Bus) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.HeartbeatTotal.Inc()
			models := s.engine.ListModels()
			adapters := s.engine.ListAdapterIDs()
			enabledCount := 0
			for _, mod := range models {
				if mod.Enabled {
					enabledCount++
				}
			}
			bus.Publish(events.Event{
				Type:   events.EventHeartbeat,
				Reason: fmt.Sprintf("providers=%d models=%d", len(adapters), enabledCount),
			})
		case <-s.stopHeartbeat:
			return
		}
	}
}

// loadCredentialsFile reads a JSON credentials file (default ~/.tokenhub/credentials)
// and registers providers and models. Providers are persisted to the database and
// API keys are stored in the vault (when unlocked). This is the primary mechanism
// for bootstrapping a fresh TokenHub instance — it is declarative, lives outside
// the source tree, and requires no running service or CLI tools.
//
// The file must be owner-readable only (mode 0600 or 0400). It is idempotent:
// providers and models are upserted, so the file can remain in place across restarts.
func loadCredentialsFile(path string, eng *router.Engine, v *vault.Vault, db store.Store, timeout time.Duration, logger *slog.Logger) {
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
		ID      string `json:"id"`
		Type    string `json:"type"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Enabled *bool  `json:"enabled"` // nil = true
	}
	type credModel struct {
		ID               string  `json:"id"`
		ProviderID       string  `json:"provider_id"`
		Weight           int     `json:"weight"`
		MaxContextTokens int     `json:"max_context_tokens"`
		InputPer1K       float64 `json:"input_per_1k"`
		OutputPer1K      float64 `json:"output_per_1k"`
		Enabled          *bool   `json:"enabled"` // nil = true
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

	ctx := context.Background()

	for _, p := range creds.Providers {
		if p.ID == "" || p.BaseURL == "" {
			logger.Warn("skipping credentials provider: id and base_url required", slog.String("id", p.ID))
			continue
		}

		enabled := p.Enabled == nil || *p.Enabled

		// Store API key in vault if provided and vault is unlocked.
		credStore := "none"
		if p.APIKey != "" && v != nil && !v.IsLocked() {
			if err := v.Set("provider:"+p.ID+":api_key", p.APIKey); err != nil {
				logger.Warn("failed to store API key in vault", slog.String("provider", p.ID), slog.String("error", err.Error()))
			} else {
				credStore = "vault"
			}
		}

		// Persist to database.
		if db != nil {
			rec := store.ProviderRecord{
				ID:        p.ID,
				Type:      p.Type,
				Enabled:   enabled,
				BaseURL:   p.BaseURL,
				CredStore: credStore,
			}
			if err := db.UpsertProvider(ctx, rec); err != nil {
				logger.Warn("failed to persist credentials provider", slog.String("provider", p.ID), slog.String("error", err.Error()))
			}
		}

		// Register runtime adapter.
		adapter, err := newProviderAdapter(p.Type, p.ID, p.APIKey, p.BaseURL, timeout)
		if err != nil {
			logger.Warn("skipping credentials provider: unknown type", slog.String("provider", p.ID), slog.String("type", p.Type))
			continue
		}
		eng.RegisterAdapter(adapter)
		logger.Info("registered provider from credentials file", slog.String("provider", p.ID), slog.String("base_url", p.BaseURL), slog.String("cred_store", credStore))
	}

	// Persist vault blob after storing all API keys.
	if v != nil && !v.IsLocked() && db != nil {
		if salt := v.Salt(); salt != nil {
			data := v.Export()
			if err := db.SaveVaultBlob(ctx, salt, data); err != nil {
				logger.Warn("failed to persist vault after credentials load", slog.String("error", err.Error()))
			}
		}
	}

	for _, m := range creds.Models {
		if m.ID == "" || m.ProviderID == "" {
			logger.Warn("skipping credentials model: id and provider_id required", slog.String("id", m.ID))
			continue
		}
		enabled := m.Enabled == nil || *m.Enabled
		model := router.Model{
			ID:               m.ID,
			ProviderID:       m.ProviderID,
			Weight:           m.Weight,
			MaxContextTokens: m.MaxContextTokens,
			InputPer1K:       m.InputPer1K,
			OutputPer1K:      m.OutputPer1K,
			Enabled:          enabled,
		}
		eng.RegisterModel(model)

		// Persist to database.
		if db != nil {
			if err := db.UpsertModel(ctx, store.ModelRecord{
				ID: m.ID, ProviderID: m.ProviderID, Weight: m.Weight,
				MaxContextTokens: m.MaxContextTokens, InputPer1K: m.InputPer1K,
				OutputPer1K: m.OutputPer1K, Enabled: enabled,
			}); err != nil {
				logger.Warn("failed to persist credentials model", slog.String("model", m.ID), slog.String("error", err.Error()))
			}
		}
		logger.Info("registered model from credentials file", slog.String("model", m.ID), slog.String("provider", m.ProviderID))
	}

	logger.Info("loaded credentials file",
		slog.String("path", path),
		slog.Int("providers", len(creds.Providers)),
		slog.Int("models", len(creds.Models)),
	)
}

// newProviderAdapter constructs a runtime adapter for the given provider type,
// credentials, and base URL. Returns an error for unknown provider types.
func newProviderAdapter(provType, id, apiKey, baseURL string, timeout time.Duration) (router.Sender, error) {
	switch provType {
	case "anthropic":
		return anthropic.New(id, apiKey, baseURL, anthropic.WithTimeout(timeout)), nil
	case "vllm":
		opts := []vllm.Option{vllm.WithTimeout(timeout)}
		if apiKey != "" {
			opts = append(opts, vllm.WithAPIKey(apiKey))
		}
		return vllm.New(id, baseURL, opts...), nil
	case "openai", "":
		return openai.New(id, apiKey, baseURL, openai.WithTimeout(timeout)), nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", provType)
	}
}

// loadPersistedProviders reads provider records from the database and creates
// runtime adapters for any that don't already have one registered (e.g. from
// the credentials file). This ensures providers added via the admin API
// survive restarts.
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
	registered := 0
	for _, p := range providers {
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
		adapter, err := newProviderAdapter(p.Type, p.ID, apiKey, p.BaseURL, timeout)
		if err != nil {
			logger.Warn("skipping persisted provider: unknown type", slog.String("provider", p.ID), slog.String("type", p.Type))
			continue
		}
		eng.RegisterAdapter(adapter)
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
			PricingSource:    m.PricingSource,
		})
	}
	if len(models) > 0 {
		logger.Info("loaded persisted models", slog.Int("count", len(models)))
	}
}

// writeStateEnv writes startup state as key=value pairs next to the database.
// For Docker deployments, make start reads /data/env from the container via
// docker compose exec and writes ~/.tokenhub/env on the host. The server never
// writes to the host filesystem directly — that is always handled by the
// Makefile, which is guaranteed to run in the host context.
func writeStateEnv(dbDSN, token string, logger *slog.Logger) {
	dsn := strings.TrimPrefix(dbDSN, "file:")
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		dsn = dsn[:i]
	}
	if dsn == "" || dsn == ":memory:" {
		return
	}
	dir := filepath.Dir(dsn)
	envContent := []byte("TOKENHUB_ADMIN_TOKEN=" + token + "\n")
	if err := os.WriteFile(filepath.Join(dir, "env"), envContent, 0600); err != nil {
		logger.Warn("failed to write state env file", slog.String("error", err.Error()))
	}
	// Legacy: keep .admin-token for older tokenhubctl versions.
	tokenContent := []byte(token + "\n")
	if err := os.WriteFile(filepath.Join(dir, ".admin-token"), tokenContent, 0600); err != nil {
		logger.Warn("failed to write admin token file", slog.String("error", err.Error()))
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

