package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"

	"github.com/jordanhubbard/tokenhub/config"
	"github.com/jordanhubbard/tokenhub/models"
	"github.com/jordanhubbard/tokenhub/orchestrator"
	"github.com/jordanhubbard/tokenhub/providers"
	"github.com/jordanhubbard/tokenhub/router"
	"github.com/jordanhubbard/tokenhub/server"
	"github.com/jordanhubbard/tokenhub/vault"
)

func main() {
	configPath := flag.String("config", "", "Path to configuration file")
	generateKey := flag.Bool("generate-key", false, "Generate a new encryption key")
	flag.Parse()

	if *generateKey {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			log.Fatalf("Failed to generate key: %v", err)
		}
		fmt.Printf("Generated encryption key: %s\n", hex.EncodeToString(key))
		fmt.Println("Set this as TOKENHUB_ENCRYPTION_KEY environment variable")
		return
	}

	// Load configuration
	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.LoadConfig(*configPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
	} else {
		cfg = config.DefaultConfig()
		log.Println("Using default configuration")
	}

	// Initialize vault
	var v *vault.Vault
	if cfg.EncryptionKey != "" {
		keyBytes, err := hex.DecodeString(cfg.EncryptionKey)
		if err != nil || len(keyBytes) != 32 {
			log.Println("Warning: Invalid encryption key format, generating temporary key")
			keyBytes = make([]byte, 32)
			rand.Read(keyBytes)
		}
		v, err = vault.NewVault(keyBytes)
		if err != nil {
			log.Fatalf("Failed to create vault: %v", err)
		}
		log.Println("Vault initialized with AES-256 encryption")
	}

	// Initialize provider registry
	providerRegistry := providers.NewRegistry()
	for _, providerCfg := range cfg.Providers {
		var provider providers.Provider
		
		apiKey := providerCfg.APIKey
		if apiKey == "" && v != nil {
			// Try to get from vault
			storedKey, err := v.Get(providerCfg.Name + "_api_key")
			if err == nil {
				apiKey = storedKey
			}
		}

		switch providerCfg.Type {
		case "openai":
			if apiKey == "" {
				log.Printf("Warning: No API key for OpenAI provider")
				continue
			}
			provider = providers.NewOpenAIProvider(apiKey)
		case "anthropic":
			if apiKey == "" {
				log.Printf("Warning: No API key for Anthropic provider")
				continue
			}
			provider = providers.NewAnthropicProvider(apiKey)
		case "vllm":
			if providerCfg.BaseURL == "" {
				log.Printf("Warning: No base URL for vLLM provider %s", providerCfg.Name)
				continue
			}
			provider = providers.NewVLLMProvider(providerCfg.Name, providerCfg.BaseURL)
		default:
			log.Printf("Warning: Unknown provider type: %s", providerCfg.Type)
			continue
		}

		providerRegistry.Register(provider)
		log.Printf("Registered provider: %s", provider.Name())
	}

	// Initialize model registry
	modelRegistry := models.NewRegistry()
	for _, modelCfg := range cfg.Models {
		model := &models.Model{
			ID:           modelCfg.ID,
			Provider:     modelCfg.Provider,
			Name:         modelCfg.Name,
			Weight:       modelCfg.Weight,
			CostPer1K:    modelCfg.CostPer1K,
			ContextSize:  modelCfg.ContextSize,
			Capabilities: modelCfg.Capabilities,
		}
		modelRegistry.Register(model)
		log.Printf("Registered model: %s (provider: %s, context: %d)", model.Name, model.Provider, model.ContextSize)
	}

	// Initialize router
	r := router.NewRouter(providerRegistry, modelRegistry)

	// Initialize orchestrator
	o := orchestrator.NewOrchestrator(r)

	// Start server
	srv := server.NewServer(r, o, cfg.Server.Host, cfg.Server.Port)
	log.Fatal(srv.Start())
}
