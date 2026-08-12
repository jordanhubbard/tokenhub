// Package nemorelay implements NeMo Relay pricing catalog v1 compatibility.
// It intentionally uses the published JSON contract instead of the
// experimental NeMo Relay Go/FFI binding, keeping TokenHub's binaries pure Go.
package nemorelay

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

const catalogVersion = 1

const (
	cacheReadIncluded = "included_in_prompt_tokens"
	cacheReadSeparate = "separate"
	unitPerToken      = "per_token"
	unitPerRequest    = "per_request"
	unitPerSecond     = "per_second"
	unitGPUHour       = "gpu_hour"
)

// Catalog is the NeMo Relay model-pricing catalog v1 document.
type Catalog struct {
	Version int            `json:"version"`
	Entries []ModelPricing `json:"entries"`
}

// ModelPricing is one provider/model entry in a NeMo Relay pricing catalog.
type ModelPricing struct {
	Provider      string             `json:"provider"`
	ModelID       string             `json:"model_id"`
	Aliases       []string           `json:"aliases,omitempty"`
	Currency      string             `json:"currency,omitempty"`
	Unit          string             `json:"unit,omitempty"`
	Rates         *TokenPricingRates `json:"rates,omitempty"`
	RateSchedule  *TokenRateSchedule `json:"rate_schedule,omitempty"`
	PromptCache   PromptCachePricing `json:"prompt_cache"`
	PricingAsOf   string             `json:"pricing_as_of"`
	PricingSource string             `json:"pricing_source"`
}

// TokenPricingRates are expressed in currency units per one million tokens.
type TokenPricingRates struct {
	InputPerMillion      float64  `json:"input_per_million"`
	OutputPerMillion     float64  `json:"output_per_million"`
	CacheReadPerMillion  *float64 `json:"cache_read_per_million,omitempty"`
	CacheWritePerMillion *float64 `json:"cache_write_per_million,omitempty"`
}

// UnmarshalJSON preserves NeMo Relay's required input/output rate fields.
// Plain float fields would otherwise turn a missing price into an implicit
// zero rate and silently undercount cost.
func (r *TokenPricingRates) UnmarshalJSON(raw []byte) error {
	var wire struct {
		InputPerMillion      *float64 `json:"input_per_million"`
		OutputPerMillion     *float64 `json:"output_per_million"`
		CacheReadPerMillion  *float64 `json:"cache_read_per_million,omitempty"`
		CacheWritePerMillion *float64 `json:"cache_write_per_million,omitempty"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if wire.InputPerMillion == nil {
		return fmt.Errorf("missing input_per_million")
	}
	if wire.OutputPerMillion == nil {
		return fmt.Errorf("missing output_per_million")
	}
	r.InputPerMillion = *wire.InputPerMillion
	r.OutputPerMillion = *wire.OutputPerMillion
	r.CacheReadPerMillion = wire.CacheReadPerMillion
	r.CacheWritePerMillion = wire.CacheWritePerMillion
	return nil
}

// TokenRateSchedule selects one set of rates for the full request.
type TokenRateSchedule struct {
	Type      string          `json:"type"`
	AppliesTo string          `json:"applies_to,omitempty"`
	Tiers     []TokenRateTier `json:"tiers"`
}

// TokenRateTier uses inclusive prompt-token bounds.
type TokenRateTier struct {
	MinPromptTokens *int              `json:"min_prompt_tokens,omitempty"`
	MaxPromptTokens *int              `json:"max_prompt_tokens,omitempty"`
	Rates           TokenPricingRates `json:"rates"`
}

// PromptCachePricing declares whether cache-read tokens are already included
// in the provider's prompt-token count.
type PromptCachePricing struct {
	ReadAccounting string `json:"read_accounting"`
}

// Resolver searches catalogs in source order, matching NeMo Relay's
// first-source-wins behavior.
type Resolver struct {
	catalogs []Catalog
	sources  []string
	entries  int
}

// LoadFiles loads and validates NeMo Relay JSON catalogs in precedence order.
func LoadFiles(paths []string) (*Resolver, error) {
	resolver := &Resolver{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read NeMo Relay pricing catalog %q: %w", path, err)
		}
		catalog, err := ParseCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("load NeMo Relay pricing catalog %q: %w", path, err)
		}
		resolver.catalogs = append(resolver.catalogs, catalog)
		resolver.sources = append(resolver.sources, path)
		resolver.entries += len(catalog.Entries)
	}
	if len(resolver.catalogs) == 0 {
		return nil, fmt.Errorf("no NeMo Relay pricing catalogs configured")
	}
	return resolver, nil
}

// ParseCatalog decodes and validates one NeMo Relay pricing catalog v1.
func ParseCatalog(raw []byte) (Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("invalid catalog JSON: %w", err)
	}
	if err := catalog.validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

// NewResolver builds a resolver from validated in-memory catalogs. It is
// primarily useful to API consumers and tests that already own catalog bytes.
func NewResolver(catalogs ...Catalog) (*Resolver, error) {
	resolver := &Resolver{}
	for i := range catalogs {
		if err := catalogs[i].validate(); err != nil {
			return nil, fmt.Errorf("catalog %d: %w", i, err)
		}
		resolver.catalogs = append(resolver.catalogs, catalogs[i])
		resolver.sources = append(resolver.sources, fmt.Sprintf("inline:%d", i))
		resolver.entries += len(catalogs[i].Entries)
	}
	if len(resolver.catalogs) == 0 {
		return nil, fmt.Errorf("no NeMo Relay pricing catalogs configured")
	}
	return resolver, nil
}

// CostEstimatorStatus implements router.CostEstimatorInspector.
func (r *Resolver) CostEstimatorStatus() router.CostEstimatorStatus {
	if r == nil {
		return router.CostEstimatorStatus{}
	}
	return router.CostEstimatorStatus{
		Enabled: true,
		Type:    "nemo_relay",
		Sources: append([]string(nil), r.sources...),
		Entries: r.entries,
	}
}

// EstimateCost implements router.CostEstimator.
func (r *Resolver) EstimateCost(providerID, modelID string, usage router.TokenUsage) (router.CostEstimate, bool) {
	if r == nil {
		return router.CostEstimate{}, false
	}
	for i := range r.catalogs {
		if entry := r.catalogs[i].pricingFor(providerID, modelID); entry != nil {
			return entry.estimateCost(usage)
		}
	}
	return router.CostEstimate{}, false
}

func (c *Catalog) validate() error {
	if c.Version != catalogVersion {
		return fmt.Errorf("unsupported catalog version %d", c.Version)
	}
	seen := make(map[string]struct{})
	for i := range c.Entries {
		entry := &c.Entries[i]
		if err := entry.validate(i); err != nil {
			return err
		}
		provider := normalizeProvider(entry.Provider)
		for _, model := range append([]string{entry.ModelID}, entry.Aliases...) {
			key := provider + "/" + normalizeModel(model)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate model pricing alias %q", key)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func (m *ModelPricing) validate(index int) error {
	for name, value := range map[string]string{
		"provider":       m.Provider,
		"model_id":       m.ModelID,
		"pricing_as_of":  m.PricingAsOf,
		"pricing_source": m.PricingSource,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("model pricing entry %d has empty %s", index, name)
		}
	}
	if m.Currency == "" {
		m.Currency = "USD"
	}
	if m.Unit == "" {
		m.Unit = unitPerToken
	}
	switch m.Unit {
	case unitPerToken, unitPerRequest, unitPerSecond, unitGPUHour:
	default:
		return fmt.Errorf("model pricing entry %d has unsupported unit %q", index, m.Unit)
	}
	if m.Unit == unitPerToken && m.Rates == nil && m.RateSchedule == nil {
		return fmt.Errorf("model pricing entry %d has empty rates or rate_schedule", index)
	}
	if m.Rates != nil {
		if err := validateRates(index, "rates", *m.Rates); err != nil {
			return err
		}
	}
	if m.RateSchedule != nil {
		if m.RateSchedule.Type != "prompt_token_threshold" {
			return fmt.Errorf("model pricing entry %d has unsupported rate_schedule type %q", index, m.RateSchedule.Type)
		}
		if m.RateSchedule.AppliesTo == "" {
			m.RateSchedule.AppliesTo = "full_request"
		}
		if m.RateSchedule.AppliesTo != "full_request" {
			return fmt.Errorf("model pricing entry %d has unsupported rate_schedule applies_to %q", index, m.RateSchedule.AppliesTo)
		}
		if len(m.RateSchedule.Tiers) == 0 {
			return fmt.Errorf("model pricing entry %d has empty rate_schedule.tiers", index)
		}
		for tierIndex, tier := range m.RateSchedule.Tiers {
			if tier.MinPromptTokens != nil && *tier.MinPromptTokens < 0 || tier.MaxPromptTokens != nil && *tier.MaxPromptTokens < 0 {
				return fmt.Errorf("model pricing entry %d tier %d has negative prompt-token bound", index, tierIndex)
			}
			if tier.MinPromptTokens != nil && tier.MaxPromptTokens != nil && *tier.MinPromptTokens > *tier.MaxPromptTokens {
				return fmt.Errorf("model pricing entry %d tier %d has min_prompt_tokens greater than max_prompt_tokens", index, tierIndex)
			}
			if err := validateRates(index, fmt.Sprintf("rate_schedule.tiers[%d].rates", tierIndex), tier.Rates); err != nil {
				return err
			}
		}
	}
	if m.PromptCache.ReadAccounting != cacheReadIncluded && m.PromptCache.ReadAccounting != cacheReadSeparate {
		return fmt.Errorf("model pricing entry %d has invalid prompt_cache.read_accounting %q", index, m.PromptCache.ReadAccounting)
	}
	return nil
}

func validateRates(index int, prefix string, rates TokenPricingRates) error {
	values := []struct {
		name  string
		value *float64
	}{
		{name: "input_per_million", value: &rates.InputPerMillion},
		{name: "output_per_million", value: &rates.OutputPerMillion},
		{name: "cache_read_per_million", value: rates.CacheReadPerMillion},
		{name: "cache_write_per_million", value: rates.CacheWritePerMillion},
	}
	for _, value := range values {
		if value.value != nil && (math.IsNaN(*value.value) || math.IsInf(*value.value, 0) || *value.value < 0) {
			return fmt.Errorf("model pricing entry %d has invalid %s.%s: %v", index, prefix, value.name, *value.value)
		}
	}
	return nil
}

type lookupKey struct {
	provider string
	model    string
}

func (c Catalog) pricingFor(provider, model string) *ModelPricing {
	for _, key := range lookupKeys(provider, model) {
		for i := range c.Entries {
			if c.Entries[i].matches(key) {
				return &c.Entries[i]
			}
		}
	}
	return nil
}

func (m ModelPricing) matches(key lookupKey) bool {
	if key.provider != "" && normalizeProvider(m.Provider) != key.provider {
		return false
	}
	if normalizeModel(m.ModelID) == key.model {
		return true
	}
	for _, alias := range m.Aliases {
		if normalizeModel(alias) == key.model {
			return true
		}
	}
	return false
}

func lookupKeys(provider, model string) []lookupKey {
	normalized := normalizeModel(model)
	if normalized == "" {
		return nil
	}
	parts := splitModel(normalized)
	if len(parts) == 0 {
		return nil
	}
	terminal := parts[len(parts)-1]
	var keys []lookupKey
	add := func(key lookupKey) {
		for _, existing := range keys {
			if existing == key {
				return
			}
		}
		keys = append(keys, key)
	}
	if explicit := normalizeProvider(provider); explicit != "" {
		add(lookupKey{provider: explicit, model: normalized})
		add(lookupKey{provider: explicit, model: terminal})
	} else if len(parts) > 1 {
		add(lookupKey{provider: strings.Join(parts[:len(parts)-1], "/"), model: terminal})
	}
	for i := range parts {
		add(lookupKey{model: strings.Join(parts[i:], "/")})
	}
	return keys
}

func splitModel(model string) []string {
	parts := strings.Split(model, "/")
	filtered := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func normalizeModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(provider), "/"))
}

func (m ModelPricing) estimateCost(usage router.TokenUsage) (router.CostEstimate, bool) {
	if m.Unit != unitPerToken || !strings.EqualFold(m.Currency, "USD") {
		return router.CostEstimate{}, false
	}
	rates, ok := m.ratesForUsage(usage)
	if !ok {
		return router.CostEstimate{}, false
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 {
		return router.CostEstimate{}, false
	}

	billablePrompt := usage.PromptTokens
	if m.PromptCache.ReadAccounting == cacheReadIncluded {
		billablePrompt = max(0, billablePrompt-usage.CacheReadTokens)
	}
	input := pricedComponent(billablePrompt, rates.InputPerMillion)
	output := pricedComponent(usage.CompletionTokens, rates.OutputPerMillion)
	cacheRead := optionalPricedComponent(usage.CacheReadTokens, rates.CacheReadPerMillion)
	cacheWrite := optionalPricedComponent(usage.CacheWriteTokens, rates.CacheWritePerMillion)
	total := 0.0
	for _, component := range []*float64{input, output, cacheRead, cacheWrite} {
		if component != nil {
			total += *component
		}
	}
	total = roundCost(total)
	return router.CostEstimate{
		Total:           total,
		Currency:        "USD",
		Input:           input,
		Output:          output,
		CacheRead:       cacheRead,
		CacheWrite:      cacheWrite,
		Source:          "model_pricing",
		PricingProvider: m.Provider,
		PricingModel:    m.ModelID,
		PricingAsOf:     m.PricingAsOf,
		PricingSource:   m.PricingSource,
	}, true
}

func (m ModelPricing) ratesForUsage(usage router.TokenUsage) (TokenPricingRates, bool) {
	if m.RateSchedule == nil {
		if m.Rates == nil {
			return TokenPricingRates{}, false
		}
		return *m.Rates, true
	}
	for _, tier := range m.RateSchedule.Tiers {
		if tier.MinPromptTokens != nil && usage.PromptTokens < *tier.MinPromptTokens {
			continue
		}
		if tier.MaxPromptTokens != nil && usage.PromptTokens > *tier.MaxPromptTokens {
			continue
		}
		return tier.Rates, true
	}
	return TokenPricingRates{}, false
}

func pricedComponent(tokens int, rate float64) *float64 {
	if tokens <= 0 {
		return nil
	}
	cost := roundCost(float64(tokens) * rate / 1_000_000)
	return &cost
}

func optionalPricedComponent(tokens int, rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	return pricedComponent(tokens, *rate)
}

func roundCost(cost float64) float64 {
	const scale = 1_000_000_000_000.0
	return math.Round(cost*scale) / scale
}
