package router

import (
	"math"
	"math/rand"
	"sync"
)

// armKey identifies a (model, token_bucket) pair for the contextual bandit.
type armKey struct {
	ModelID     string
	TokenBucket string
}

// armParams holds the Beta distribution parameters for one arm.
type armParams struct {
	Alpha float64 // successes (sum of rewards) + 1
	Beta  float64 // failures (count - sum of rewards) + 1
}

// ThompsonSampler implements contextual Thompson Sampling for model selection.
// Each (model_id, token_bucket) pair is an arm with a Beta(alpha, beta) prior.
type ThompsonSampler struct {
	mu   sync.RWMutex
	arms map[armKey]armParams
}

// NewThompsonSampler creates a sampler with uniform priors.
func NewThompsonSampler() *ThompsonSampler {
	return &ThompsonSampler{
		arms: make(map[armKey]armParams),
	}
}

// UpdateArm sets the Beta parameters for a (model, bucket) arm.
func (ts *ThompsonSampler) UpdateArm(modelID, tokenBucket string, alpha, beta float64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.arms[armKey{modelID, tokenBucket}] = armParams{Alpha: alpha, Beta: beta}
}

// Sample draws from each model's Beta distribution for the given token bucket
// and returns model IDs sorted by descending sampled value (best first).
func (ts *ThompsonSampler) Sample(modelIDs []string, tokenBucket string) []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	type scored struct {
		modelID string
		value   float64
	}
	samples := make([]scored, len(modelIDs))
	for i, id := range modelIDs {
		key := armKey{id, tokenBucket}
		p, ok := ts.arms[key]
		if !ok {
			p = armParams{Alpha: 1, Beta: 1} // uniform prior
		}
		samples[i] = scored{modelID: id, value: betaSample(p.Alpha, p.Beta)}
	}

	// Sort descending by sampled value (higher = better).
	for i := 1; i < len(samples); i++ {
		for j := i; j > 0 && samples[j].value > samples[j-1].value; j-- {
			samples[j], samples[j-1] = samples[j-1], samples[j]
		}
	}

	result := make([]string, len(samples))
	for i, s := range samples {
		result[i] = s.modelID
	}
	return result
}

// betaSample draws a sample from Beta(alpha, beta) using the gamma distribution.
func betaSample(alpha, beta float64) float64 {
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}
	x := gammaSample(alpha)
	y := gammaSample(beta)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// gammaSample draws from Gamma(shape, 1) using Marsaglia and Tsang's method.
func gammaSample(shape float64) float64 {
	if shape < 1 {
		// Boost: Gamma(shape) = Gamma(shape+1) * U^(1/shape)
		return gammaSample(shape+1) * math.Pow(rand.Float64(), 1.0/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		var x, v float64
		for {
			x = rand.NormFloat64()
			v = 1.0 + c*x
			if v > 0 {
				break
			}
		}
		v = v * v * v
		u := rand.Float64()
		if u < 1.0-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v
		}
	}
}
