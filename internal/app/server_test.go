package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

// discardLogger returns a logger that discards all output, suitable for tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLoadConfigDefaults(t *testing.T) {
	// Unset all TOKENHUB_ env vars to ensure defaults are used.
	envVars := []string{
		"TOKENHUB_LISTEN_ADDR",
		"TOKENHUB_LOG_LEVEL",
		"TOKENHUB_DB_DSN",
		"TOKENHUB_VAULT_ENABLED",
		"TOKENHUB_DEFAULT_MODE",
		"TOKENHUB_DEFAULT_MAX_BUDGET_USD",
		"TOKENHUB_DEFAULT_MAX_LATENCY_MS",
		"TOKENHUB_PROVIDER_TIMEOUT_SECS",
		"TOKENHUB_NEMO_RELAY_PRICING_FILES",
	}
	for _, key := range envVars {
		t.Setenv(key, "")
		_ = os.Unsetenv(key)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddr != ":8090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8090")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.DBDSN != "file:/data/tokenhub.sqlite" {
		t.Errorf("DBDSN = %q, want %q", cfg.DBDSN, "file:/data/tokenhub.sqlite")
	}
	if cfg.VaultEnabled != true {
		t.Errorf("VaultEnabled = %v, want true", cfg.VaultEnabled)
	}
	if cfg.DefaultMode != "normal" {
		t.Errorf("DefaultMode = %q, want %q", cfg.DefaultMode, "normal")
	}
	if cfg.DefaultMaxBudget != 0.05 {
		t.Errorf("DefaultMaxBudget = %f, want 0.05", cfg.DefaultMaxBudget)
	}
	if cfg.DefaultMaxLatencyMs != 120000 {
		t.Errorf("DefaultMaxLatencyMs = %d, want 120000", cfg.DefaultMaxLatencyMs)
	}
	if cfg.ProviderTimeoutSecs != 30 {
		t.Errorf("ProviderTimeoutSecs = %d, want 30", cfg.ProviderTimeoutSecs)
	}
	if len(cfg.NemoRelayPricingFiles) != 0 {
		t.Errorf("NemoRelayPricingFiles = %v, want empty", cfg.NemoRelayPricingFiles)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("TOKENHUB_LISTEN_ADDR", ":9090")
	t.Setenv("TOKENHUB_LOG_LEVEL", "debug")
	t.Setenv("TOKENHUB_DB_DSN", "file::memory:")
	t.Setenv("TOKENHUB_VAULT_ENABLED", "false")
	t.Setenv("TOKENHUB_DEFAULT_MODE", "budget")
	t.Setenv("TOKENHUB_DEFAULT_MAX_BUDGET_USD", "1.5")
	t.Setenv("TOKENHUB_DEFAULT_MAX_LATENCY_MS", "5000")
	t.Setenv("TOKENHUB_PROVIDER_TIMEOUT_SECS", "60")
	t.Setenv("TOKENHUB_NEMO_RELAY_PRICING_FILES", "/etc/nemo/primary.json,/etc/nemo/fallback.json")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.DBDSN != "file::memory:" {
		t.Errorf("DBDSN = %q, want %q", cfg.DBDSN, "file::memory:")
	}
	if cfg.VaultEnabled != false {
		t.Errorf("VaultEnabled = %v, want false", cfg.VaultEnabled)
	}
	if cfg.DefaultMode != "budget" {
		t.Errorf("DefaultMode = %q, want %q", cfg.DefaultMode, "budget")
	}
	if cfg.DefaultMaxBudget != 1.5 {
		t.Errorf("DefaultMaxBudget = %f, want 1.5", cfg.DefaultMaxBudget)
	}
	if cfg.DefaultMaxLatencyMs != 5000 {
		t.Errorf("DefaultMaxLatencyMs = %d, want 5000", cfg.DefaultMaxLatencyMs)
	}
	if cfg.ProviderTimeoutSecs != 60 {
		t.Errorf("ProviderTimeoutSecs = %d, want 60", cfg.ProviderTimeoutSecs)
	}
	if got, want := cfg.NemoRelayPricingFiles, []string{"/etc/nemo/primary.json", "/etc/nemo/fallback.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NemoRelayPricingFiles = %v, want %v", got, want)
	}
}

func TestLoadConfigInvalidEnvFallsBackToDefaults(t *testing.T) {
	t.Setenv("TOKENHUB_VAULT_ENABLED", "notabool")
	t.Setenv("TOKENHUB_DEFAULT_MAX_LATENCY_MS", "notanint")
	t.Setenv("TOKENHUB_DEFAULT_MAX_BUDGET_USD", "notafloat")
	t.Setenv("TOKENHUB_PROVIDER_TIMEOUT_SECS", "notanint")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.VaultEnabled != true {
		t.Errorf("VaultEnabled = %v, want true (default on invalid input)", cfg.VaultEnabled)
	}
	if cfg.DefaultMaxLatencyMs != 120000 {
		t.Errorf("DefaultMaxLatencyMs = %d, want 120000 (default on invalid input)", cfg.DefaultMaxLatencyMs)
	}
	if cfg.DefaultMaxBudget != 0.05 {
		t.Errorf("DefaultMaxBudget = %f, want 0.05 (default on invalid input)", cfg.DefaultMaxBudget)
	}
	if cfg.ProviderTimeoutSecs != 30 {
		t.Errorf("ProviderTimeoutSecs = %d, want 30 (default on invalid input)", cfg.ProviderTimeoutSecs)
	}
}

func newTestConfig() Config {
	return Config{
		ListenAddr:          ":0",
		LogLevel:            "error",
		DBDSN:               ":memory:",
		VaultEnabled:        false,
		DefaultMode:         "normal",
		DefaultMaxBudget:    0.05,
		DefaultMaxLatencyMs: 120000,
		ProviderTimeoutSecs: 30,
		RateLimitRPS:        60,
		RateLimitBurst:      120,
	}
}

func TestNewServer(t *testing.T) {
	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer func() { _ = srv.Close() }()

	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestNewServerHasRouter(t *testing.T) {
	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer func() { _ = srv.Close() }()

	if srv.Router() == nil {
		t.Fatal("expected non-nil Router()")
	}
}

func TestCORSExposesCostAccountingHeaders(t *testing.T) {
	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer func() { _ = srv.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://client.example")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	exposed := w.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(exposed, "X-Tokenhub-Cost-Usd") || !strings.Contains(exposed, "X-Tokenhub-Cost-Source") {
		t.Fatalf("cost accounting headers are not CORS-exposed: %q", exposed)
	}
}

func TestNewServerLoadsNeMoRelayPricing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	err := os.WriteFile(path, []byte(`{
  "version": 1,
  "entries": [{
    "provider": "openai", "model_id": "priced-model",
    "rates": {"input_per_million": 1, "output_per_million": 2},
    "prompt_cache": {"read_accounting": "included_in_prompt_tokens"},
    "pricing_as_of": "2026-08-01", "pricing_source": "test"
  }]
}`), 0o600)
	if err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	cfg := newTestConfig()
	cfg.NemoRelayPricingFiles = []string{path}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer func() { _ = srv.Close() }()

	status := srv.engine.CostEstimatorStatus()
	if !status.Enabled || status.Type != "nemo_relay" || status.Entries != 1 {
		t.Fatalf("unexpected pricing status: %+v", status)
	}
}

func TestNewServerRejectsInvalidNeMoRelayPricing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"entries":[]}`), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	cfg := newTestConfig()
	cfg.NemoRelayPricingFiles = []string{path}
	srv, err := NewServer(cfg)
	if srv != nil {
		_ = srv.Close()
	}
	if err == nil {
		t.Fatal("NewServer() accepted an invalid NeMo Relay pricing catalog")
	}
}

func TestServerReloadRetainsLastKnownGoodNeMoRelayPricing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	valid := []byte(`{
  "version": 1,
  "entries": [{
    "provider": "openai", "model_id": "priced-model",
    "rates": {"input_per_million": 1, "output_per_million": 2},
    "prompt_cache": {"read_accounting": "included_in_prompt_tokens"},
    "pricing_as_of": "2026-08-01", "pricing_source": "test"
  }]
}`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	cfg := newTestConfig()
	cfg.NemoRelayPricingFiles = []string{path}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer func() { _ = srv.Close() }()

	if err := os.WriteFile(path, []byte(`{"version":2,"entries":[]}`), 0o600); err != nil {
		t.Fatalf("replace catalog: %v", err)
	}
	srv.Reload(cfg)
	status := srv.engine.CostEstimatorStatus()
	if !status.Enabled || status.Entries != 1 {
		t.Fatalf("reload did not retain last known-good pricing: %+v", status)
	}

	disabled := cfg
	disabled.NemoRelayPricingFiles = nil
	srv.Reload(disabled)
	if status := srv.engine.CostEstimatorStatus(); status.Enabled {
		t.Fatalf("empty reload did not disable pricing: %+v", status)
	}
}

func TestServerClose(t *testing.T) {
	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	err = srv.Close()
	if err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestServerReload(t *testing.T) {
	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// Verify initial config.
	if srv.cfg.RateLimitRPS != 60 {
		t.Fatalf("initial RateLimitRPS = %d, want 60", srv.cfg.RateLimitRPS)
	}
	if srv.cfg.DefaultMode != "normal" {
		t.Fatalf("initial DefaultMode = %q, want %q", srv.cfg.DefaultMode, "normal")
	}

	// Reload with updated configuration.
	newCfg := cfg
	newCfg.RateLimitRPS = 100
	newCfg.RateLimitBurst = 200
	newCfg.DefaultMode = "budget"
	newCfg.DefaultMaxBudget = 1.0
	newCfg.DefaultMaxLatencyMs = 5000
	newCfg.LogLevel = "debug"

	srv.Reload(newCfg)

	// Verify stored config was updated.
	if srv.cfg.RateLimitRPS != 100 {
		t.Errorf("after Reload RateLimitRPS = %d, want 100", srv.cfg.RateLimitRPS)
	}
	if srv.cfg.RateLimitBurst != 200 {
		t.Errorf("after Reload RateLimitBurst = %d, want 200", srv.cfg.RateLimitBurst)
	}
	if srv.cfg.DefaultMode != "budget" {
		t.Errorf("after Reload DefaultMode = %q, want %q", srv.cfg.DefaultMode, "budget")
	}
	if srv.cfg.DefaultMaxBudget != 1.0 {
		t.Errorf("after Reload DefaultMaxBudget = %f, want 1.0", srv.cfg.DefaultMaxBudget)
	}
	if srv.cfg.DefaultMaxLatencyMs != 5000 {
		t.Errorf("after Reload DefaultMaxLatencyMs = %d, want 5000", srv.cfg.DefaultMaxLatencyMs)
	}
	if srv.cfg.LogLevel != "debug" {
		t.Errorf("after Reload LogLevel = %q, want %q", srv.cfg.LogLevel, "debug")
	}
}

// newTestEngine creates a minimal router.Engine suitable for testing.
func newTestEngine() *router.Engine {
	return router.NewEngine(router.EngineConfig{})
}

func TestAutoloadModelsForProvider_Basic(t *testing.T) {
	// Spin up a mock HTTP server that returns an OpenAI-compatible model list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		type modelEntry struct {
			ID string `json:"id"`
		}
		type resp struct {
			Data []modelEntry `json:"data"`
		}
		_ = json.NewEncoder(w).Encode(resp{Data: []modelEntry{
			{ID: "model-a"},
			{ID: "model-b"},
		}})
	}))
	defer srv.Close()

	eng := newTestEngine()
	autoloadModelsForProvider(context.Background(), "test-provider", "", srv.URL, nil, eng, nil, discardLogger())

	models := eng.ListModels()
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
		if m.ProviderID != "test-provider" {
			t.Errorf("model %q has ProviderID %q, want %q", m.ID, m.ProviderID, "test-provider")
		}
		if !m.Enabled {
			t.Errorf("model %q should be enabled", m.ID)
		}
		if m.Weight != 5 {
			t.Errorf("model %q has Weight %d, want 5", m.ID, m.Weight)
		}
	}
	if !ids["model-a"] || !ids["model-b"] {
		t.Errorf("unexpected model IDs: %v", ids)
	}
}

func TestAutoloadModelsForProvider_SkipsExplicitModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type modelEntry struct {
			ID string `json:"id"`
		}
		type resp struct {
			Data []modelEntry `json:"data"`
		}
		_ = json.NewEncoder(w).Encode(resp{Data: []modelEntry{
			{ID: "model-a"},
			{ID: "model-b"},
		}})
	}))
	defer srv.Close()

	eng := newTestEngine()
	explicit := map[string]bool{"model-a": true}
	autoloadModelsForProvider(context.Background(), "p", "", srv.URL, explicit, eng, nil, discardLogger())

	models := eng.ListModels()
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d: %v", len(models), models)
	}
	if models[0].ID != "model-b" {
		t.Errorf("expected model-b, got %q", models[0].ID)
	}
}

func TestAutoloadModelsForProvider_PlainArrayResponse(t *testing.T) {
	// Some providers return a plain JSON array instead of {data: [...]}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type modelEntry struct {
			ID string `json:"id"`
		}
		_ = json.NewEncoder(w).Encode([]modelEntry{{ID: "m1"}, {ID: "m2"}})
	}))
	defer srv.Close()

	eng := newTestEngine()
	autoloadModelsForProvider(context.Background(), "p", "", srv.URL, nil, eng, nil, discardLogger())

	models := eng.ListModels()
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
}

func TestAutoloadModelsForProvider_ProviderError(t *testing.T) {
	// Provider returns an HTTP error – should log and not register anything.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	eng := newTestEngine()
	autoloadModelsForProvider(context.Background(), "p", "", srv.URL, nil, eng, nil, discardLogger())

	if models := eng.ListModels(); len(models) != 0 {
		t.Errorf("expected 0 models on provider error, got %d", len(models))
	}
}

func TestLoadCredentialsFile_AutoloadModels(t *testing.T) {
	// Spin up a mock provider that serves /v1/models.
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		type modelEntry struct {
			ID string `json:"id"`
		}
		type resp struct {
			Data []modelEntry `json:"data"`
		}
		_ = json.NewEncoder(w).Encode(resp{Data: []modelEntry{
			{ID: "auto-model-1"},
			{ID: "auto-model-2"},
		}})
	}))
	defer mockProvider.Close()

	// Write a credentials file with autoload_models=true.
	creds := map[string]any{
		"providers": []map[string]any{
			{
				"id":              "auto-provider",
				"type":            "openai",
				"base_url":        mockProvider.URL,
				"autoload_models": true,
			},
		},
		"models": []map[string]any{},
	}
	data, _ := json.Marshal(creds)

	f, err := os.CreateTemp(t.TempDir(), "creds*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chmod(f.Name(), 0600); err != nil {
		t.Fatal(err)
	}

	eng := newTestEngine()
	loadCredentialsFile(f.Name(), eng, nil, nil, 30*1000*1000*1000, discardLogger())

	models := eng.ListModels()
	if len(models) != 2 {
		t.Fatalf("expected 2 autoloaded models, got %d", len(models))
	}
}
