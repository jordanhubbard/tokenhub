package models

// Model represents an LLM model with its characteristics
type Model struct {
	ID          string
	Provider    string
	Name        string
	Weight      int     // Priority/preference weight (higher = more preferred)
	CostPer1K   float64 // Cost per 1000 tokens
	ContextSize int     // Maximum context window size in tokens
	Capabilities []string // e.g., ["chat", "completion", "embedding"]
}

// Registry manages available models
type Registry struct {
	models map[string]*Model
}

// NewRegistry creates a new model registry
func NewRegistry() *Registry {
	return &Registry{
		models: make(map[string]*Model),
	}
}

// Register adds a model to the registry
func (r *Registry) Register(model *Model) {
	r.models[model.ID] = model
}

// Get retrieves a model by ID
func (r *Registry) Get(id string) (*Model, bool) {
	model, exists := r.models[id]
	return model, exists
}

// List returns all registered models
func (r *Registry) List() []*Model {
	models := make([]*Model, 0, len(r.models))
	for _, model := range r.models {
		models = append(models, model)
	}
	return models
}

// FindByProvider returns all models for a given provider
func (r *Registry) FindByProvider(provider string) []*Model {
	models := make([]*Model, 0)
	for _, model := range r.models {
		if model.Provider == provider {
			models = append(models, model)
		}
	}
	return models
}

// SelectBestModel selects the best model based on criteria
func (r *Registry) SelectBestModel(requiredContextSize int, maxCost float64) *Model {
	var best *Model
	bestScore := -1.0

	for _, model := range r.models {
		if model.ContextSize < requiredContextSize {
			continue
		}
		if maxCost > 0 && model.CostPer1K > maxCost {
			continue
		}

		// Score based on weight and cost (higher weight, lower cost = better)
		score := float64(model.Weight) - (model.CostPer1K * 10)
		if score > bestScore {
			bestScore = score
			best = model
		}
	}

	return best
}
