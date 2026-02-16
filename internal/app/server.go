package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jordanhubbard/tokenhub/internal/events"
	"github.com/jordanhubbard/tokenhub/internal/health"
	"github.com/jordanhubbard/tokenhub/internal/httpapi"
	"github.com/jordanhubbard/tokenhub/internal/logging"
	"github.com/jordanhubbard/tokenhub/internal/metrics"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/jordanhubbard/tokenhub/internal/providers/anthropic"
	"github.com/jordanhubbard/tokenhub/internal/providers/openai"
	"github.com/jordanhubbard/tokenhub/internal/providers/vllm"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/store"
	"github.com/jordanhubbard/tokenhub/internal/tsdb"
	"github.com/jordanhubbard/tokenhub/internal/vault"
)

type Server struct {
	cfg Config

	r *chi.Mux

	vault  *vault.Vault
	engine *router.Engine
	store  store.Store
	logger *slog.Logger
}

func NewServer(cfg Config) (*Server, error) {
	logger := logging.Setup(cfg.LogLevel)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(logging.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

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
		db.Close()
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

	// Register default models, then load any persisted models from DB.
	registerDefaultModels(eng)
	loadPersistedModels(eng, db, logger)
	loadRoutingConfig(eng, db, logger)

	m := metrics.New()
	bus := events.NewBus()
	sc := stats.NewCollector()

	// Initialize embedded TSDB.
	ts, err := tsdb.New(db.DB())
	if err != nil {
		logger.Warn("failed to initialize TSDB", slog.String("error", err.Error()))
	}

	s := &Server{
		cfg:    cfg,
		r:      r,
		vault:  v,
		engine: eng,
		store:  db,
		logger: logger,
	}

	httpapi.MountRoutes(r, httpapi.Dependencies{
		Engine:   eng,
		Vault:    v,
		Metrics:  m,
		Store:    db,
		Health:   ht,
		EventBus: bus,
		Stats:    sc,
		TSDB:     ts,
	})

	return s, nil
}

func (s *Server) Router() http.Handler { return s.r }

func (s *Server) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	return nil
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
		{ID: "gpt-4", ProviderID: "openai", Weight: 8, MaxContextTokens: 128000, InputPer1K: 0.01, OutputPer1K: 0.03, Enabled: true},
		{ID: "gpt-3.5-turbo", ProviderID: "openai", Weight: 3, MaxContextTokens: 16385, InputPer1K: 0.0005, OutputPer1K: 0.0015, Enabled: true},
		{ID: "claude-opus", ProviderID: "anthropic", Weight: 10, MaxContextTokens: 200000, InputPer1K: 0.015, OutputPer1K: 0.075, Enabled: true},
		{ID: "claude-sonnet", ProviderID: "anthropic", Weight: 7, MaxContextTokens: 200000, InputPer1K: 0.003, OutputPer1K: 0.015, Enabled: true},
	}
	for _, m := range defaults {
		eng.RegisterModel(m)
	}
}
