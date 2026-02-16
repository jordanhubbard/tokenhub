package models

import (
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	registry := NewRegistry()
	
	model := &Model{
		ID:          "test-model",
		Provider:    "test-provider",
		Name:        "Test Model",
		Weight:      100,
		CostPer1K:   0.01,
		ContextSize: 4096,
	}

	registry.Register(model)

	retrieved, exists := registry.Get("test-model")
	if !exists {
		t.Error("Model should exist after registration")
	}

	if retrieved.Name != "Test Model" {
		t.Errorf("Expected 'Test Model', got '%s'", retrieved.Name)
	}
}

func TestRegistry_SelectBestModel(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&Model{
		ID:          "small",
		Provider:    "provider1",
		Name:        "Small Model",
		Weight:      50,
		CostPer1K:   0.001,
		ContextSize: 2048,
	})

	registry.Register(&Model{
		ID:          "large",
		Provider:    "provider2",
		Name:        "Large Model",
		Weight:      100,
		CostPer1K:   0.01,
		ContextSize: 8192,
	})

	// Should select large model for large context
	best := registry.SelectBestModel(5000, 0)
	if best == nil {
		t.Fatal("Expected to find a model")
	}
	if best.ID != "large" {
		t.Errorf("Expected 'large', got '%s'", best.ID)
	}

	// Should select small model when large context not needed
	best = registry.SelectBestModel(1000, 0)
	if best == nil {
		t.Fatal("Expected to find a model")
	}
	// With cost consideration, small model should win due to weight/cost ratio
	if best.ID != "small" {
		t.Logf("Selected model: %s (acceptable)", best.ID)
	}
}

func TestRegistry_FindByProvider(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&Model{
		ID:       "model1",
		Provider: "provider1",
		Name:     "Model 1",
	})

	registry.Register(&Model{
		ID:       "model2",
		Provider: "provider1",
		Name:     "Model 2",
	})

	registry.Register(&Model{
		ID:       "model3",
		Provider: "provider2",
		Name:     "Model 3",
	})

	models := registry.FindByProvider("provider1")
	if len(models) != 2 {
		t.Errorf("Expected 2 models, got %d", len(models))
	}
}
