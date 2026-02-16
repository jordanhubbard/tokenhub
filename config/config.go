package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config holds the application configuration
type Config struct {
	Server       ServerConfig       `json:"server"`
	Vault        VaultConfig        `json:"vault"`
	Providers    []ProviderConfig   `json:"providers"`
	Models       []ModelConfig      `json:"models"`
	EncryptionKey string            `json:"encryption_key,omitempty"`
}

// ServerConfig holds server configuration
type ServerConfig struct {
	Port int    `json:"port"`
	Host string `json:"host"`
}

// VaultConfig holds vault configuration
type VaultConfig struct {
	EncryptionKeyEnv string            `json:"encryption_key_env"`
	StoredKeys       map[string]string `json:"stored_keys,omitempty"`
}

// ProviderConfig holds provider configuration
type ProviderConfig struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // "openai", "anthropic", "vllm"
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	APIKey     string `json:"api_key,omitempty"`
	BaseURL    string `json:"base_url,omitempty"` // for vLLM
}

// ModelConfig holds model configuration
type ModelConfig struct {
	ID          string   `json:"id"`
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	Weight      int      `json:"weight"`
	CostPer1K   float64  `json:"cost_per_1k"`
	ContextSize int      `json:"context_size"`
	Capabilities []string `json:"capabilities"`
}

// LoadConfig loads configuration from a JSON file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Load environment variables
	for i := range config.Providers {
		if config.Providers[i].APIKeyEnv != "" {
			envKey := os.Getenv(config.Providers[i].APIKeyEnv)
			if envKey != "" {
				config.Providers[i].APIKey = envKey
			}
		}
	}

	if config.Vault.EncryptionKeyEnv != "" {
		config.EncryptionKey = os.Getenv(config.Vault.EncryptionKeyEnv)
	}

	return &config, nil
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
			Host: "0.0.0.0",
		},
		Vault: VaultConfig{
			EncryptionKeyEnv: "TOKENHUB_ENCRYPTION_KEY",
		},
		Providers: []ProviderConfig{
			{
				Name:      "openai",
				Type:      "openai",
				APIKeyEnv: "OPENAI_API_KEY",
			},
			{
				Name:      "anthropic",
				Type:      "anthropic",
				APIKeyEnv: "ANTHROPIC_API_KEY",
			},
		},
		Models: []ModelConfig{
			{
				ID:           "gpt-4",
				Provider:     "openai",
				Name:         "gpt-4",
				Weight:       90,
				CostPer1K:    0.03,
				ContextSize:  8192,
				Capabilities: []string{"chat", "completion"},
			},
			{
				ID:           "gpt-3.5-turbo",
				Provider:     "openai",
				Name:         "gpt-3.5-turbo",
				Weight:       80,
				CostPer1K:    0.002,
				ContextSize:  4096,
				Capabilities: []string{"chat", "completion"},
			},
			{
				ID:           "claude-2",
				Provider:     "anthropic",
				Name:         "claude-2",
				Weight:       85,
				CostPer1K:    0.01,
				ContextSize:  100000,
				Capabilities: []string{"chat", "completion"},
			},
		},
	}
}
