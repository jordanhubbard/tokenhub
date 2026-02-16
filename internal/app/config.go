package app

import (
	"os"
	"strconv"
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

		TemporalEnabled:   getEnvBool("TOKENHUB_TEMPORAL_ENABLED", false),
		TemporalHostPort:  getEnv("TOKENHUB_TEMPORAL_HOST", "localhost:7233"),
		TemporalNamespace: getEnv("TOKENHUB_TEMPORAL_NAMESPACE", "tokenhub"),
		TemporalTaskQueue: getEnv("TOKENHUB_TEMPORAL_TASK_QUEUE", "tokenhub-tasks"),
	}
	return cfg, nil
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
