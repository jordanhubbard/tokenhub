package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr string
	LogLevel   string

	DBDSN string

	VaultEnabled bool

	DefaultMode         string
	DefaultMaxBudget    float64
	DefaultMaxLatencyMs int

	ProviderTimeoutSecs int

	// Security & hardening.
	AdminToken     string   // required for /admin/v1 access in production
	CORSOrigins    []string // allowed CORS origins; empty = ["*"]
	RateLimitRPS   int      // requests per second per IP
	RateLimitBurst int      // burst capacity per IP

	// Temporal workflow engine.
	TemporalEnabled   bool
	TemporalHostPort  string
	TemporalNamespace string
	TemporalTaskQueue string
}

func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr: getEnv("TOKENHUB_LISTEN_ADDR", ":8080"),
		LogLevel:   getEnv("TOKENHUB_LOG_LEVEL", "info"),
		DBDSN:      getEnv("TOKENHUB_DB_DSN", "file:/data/tokenhub.sqlite"),
		VaultEnabled: getEnvBool("TOKENHUB_VAULT_ENABLED", true),

		DefaultMode: getEnv("TOKENHUB_DEFAULT_MODE", "normal"),
		DefaultMaxBudget: getEnvFloat("TOKENHUB_DEFAULT_MAX_BUDGET_USD", 0.05),
		DefaultMaxLatencyMs: getEnvInt("TOKENHUB_DEFAULT_MAX_LATENCY_MS", 20000),

		ProviderTimeoutSecs: getEnvInt("TOKENHUB_PROVIDER_TIMEOUT_SECS", 30),

		AdminToken:     getEnv("TOKENHUB_ADMIN_TOKEN", ""),
		CORSOrigins:    getEnvStringSlice("TOKENHUB_CORS_ORIGINS", nil),
		RateLimitRPS:   getEnvInt("TOKENHUB_RATE_LIMIT_RPS", 60),
		RateLimitBurst: getEnvInt("TOKENHUB_RATE_LIMIT_BURST", 120),

		TemporalEnabled:   getEnvBool("TOKENHUB_TEMPORAL_ENABLED", false),
		TemporalHostPort:  getEnv("TOKENHUB_TEMPORAL_HOST", "localhost:7233"),
		TemporalNamespace: getEnv("TOKENHUB_TEMPORAL_NAMESPACE", "tokenhub"),
		TemporalTaskQueue: getEnv("TOKENHUB_TEMPORAL_TASK_QUEUE", "tokenhub-tasks"),
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks config values for obviously invalid settings.
func (c Config) Validate() error {
	if c.RateLimitRPS <= 0 {
		return fmt.Errorf("TOKENHUB_RATE_LIMIT_RPS must be > 0, got %d", c.RateLimitRPS)
	}
	if c.RateLimitBurst <= 0 {
		return fmt.Errorf("TOKENHUB_RATE_LIMIT_BURST must be > 0, got %d", c.RateLimitBurst)
	}
	if c.ProviderTimeoutSecs <= 0 {
		return fmt.Errorf("TOKENHUB_PROVIDER_TIMEOUT_SECS must be > 0, got %d", c.ProviderTimeoutSecs)
	}
	if c.DefaultMaxBudget < 0 {
		return fmt.Errorf("TOKENHUB_DEFAULT_MAX_BUDGET_USD must be >= 0, got %f", c.DefaultMaxBudget)
	}
	if c.DefaultMaxLatencyMs <= 0 {
		return fmt.Errorf("TOKENHUB_DEFAULT_MAX_LATENCY_MS must be > 0, got %d", c.DefaultMaxLatencyMs)
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		i, err := strconv.Atoi(v)
		if err == nil {
			return i
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return def
}

func getEnvStringSlice(key string, def []string) []string {
	if v := os.Getenv(key); v != "" {
		var result []string
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				result = append(result, s)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return def
}
