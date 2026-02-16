package app

import (
	"os"
	"testing"
)

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
	}
	for _, key := range envVars {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
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
	if cfg.DefaultMaxLatencyMs != 20000 {
		t.Errorf("DefaultMaxLatencyMs = %d, want 20000", cfg.DefaultMaxLatencyMs)
	}
	if cfg.ProviderTimeoutSecs != 30 {
		t.Errorf("ProviderTimeoutSecs = %d, want 30", cfg.ProviderTimeoutSecs)
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
	if cfg.DefaultMaxLatencyMs != 20000 {
		t.Errorf("DefaultMaxLatencyMs = %d, want 20000 (default on invalid input)", cfg.DefaultMaxLatencyMs)
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
		DefaultMaxLatencyMs: 20000,
		ProviderTimeoutSecs: 30,
	}
}

func TestNewServer(t *testing.T) {
	// Clear provider API key env vars so registerProviders doesn't try real APIs.
	t.Setenv("TOKENHUB_OPENAI_API_KEY", "")
	t.Setenv("TOKENHUB_ANTHROPIC_API_KEY", "")
	t.Setenv("TOKENHUB_VLLM_ENDPOINTS", "")
	os.Unsetenv("TOKENHUB_OPENAI_API_KEY")
	os.Unsetenv("TOKENHUB_ANTHROPIC_API_KEY")
	os.Unsetenv("TOKENHUB_VLLM_ENDPOINTS")

	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer srv.Close()

	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestNewServerHasRouter(t *testing.T) {
	t.Setenv("TOKENHUB_OPENAI_API_KEY", "")
	t.Setenv("TOKENHUB_ANTHROPIC_API_KEY", "")
	t.Setenv("TOKENHUB_VLLM_ENDPOINTS", "")
	os.Unsetenv("TOKENHUB_OPENAI_API_KEY")
	os.Unsetenv("TOKENHUB_ANTHROPIC_API_KEY")
	os.Unsetenv("TOKENHUB_VLLM_ENDPOINTS")

	cfg := newTestConfig()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	defer srv.Close()

	if srv.Router() == nil {
		t.Fatal("expected non-nil Router()")
	}
}

func TestServerClose(t *testing.T) {
	t.Setenv("TOKENHUB_OPENAI_API_KEY", "")
	t.Setenv("TOKENHUB_ANTHROPIC_API_KEY", "")
	t.Setenv("TOKENHUB_VLLM_ENDPOINTS", "")
	os.Unsetenv("TOKENHUB_OPENAI_API_KEY")
	os.Unsetenv("TOKENHUB_ANTHROPIC_API_KEY")
	os.Unsetenv("TOKENHUB_VLLM_ENDPOINTS")

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
