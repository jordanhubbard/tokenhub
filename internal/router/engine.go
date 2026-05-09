// Package router implements the multi-provider model routing engine.
//
// This file defines the Engine type, the provider-adapter contracts it
// depends on, and the registration / introspection API used by the rest
// of the codebase. Selection, routing, streaming, and orchestration logic
// live in scoring.go, route.go, stream.go, and orchestrate.go respectively.
package router

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"
)

// Sender is the interface that provider adapters must implement for the engine.
// Defined here to avoid an import cycle with the providers package.
type Sender interface {
	ID() string
	Send(ctx context.Context, model string, req Request) (ProviderResponse, error)
	ClassifyError(err error) *ClassifiedError
}

// Describer is an optional interface that adapters can implement to expose
// metadata like base URL and health endpoint for the admin UI.
type Describer interface {
	HealthEndpoint() string
}

// StreamSender is an optional interface for provider adapters that support SSE streaming.
type StreamSender interface {
	Sender
	SendStream(ctx context.Context, model string, req Request) (io.ReadCloser, error)
}

// ErrorClass classifies provider errors for routing decisions.
type ErrorClass string

const (
	ErrContextOverflow ErrorClass = "context_overflow"
	ErrRateLimited     ErrorClass = "rate_limited"
	ErrTransient       ErrorClass = "transient"
	ErrFatal           ErrorClass = "fatal"
	// ErrBudgetExceeded signals that the provider's spending budget is exhausted.
	// The engine will disable the model so it is not selected again until manually
	// re-enabled via the admin API, saving wasted round-trips to a permanently
	// unavailable provider.
	ErrBudgetExceeded ErrorClass = "budget_exceeded"
)

// ClassifiedError wraps an error with routing classification.
type ClassifiedError struct {
	Err        error
	Class      ErrorClass
	RetryAfter int
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

// HealthChecker is an optional interface for provider health tracking.
// Defined here to avoid import cycles with the health package.
type HealthChecker interface {
	IsAvailable(providerID string) bool
	RecordSuccess(providerID string, latencyMs float64)
	RecordError(providerID string, errMsg string)
}

// SkipRecorder receives a notification each time a model/provider is excluded
// from routing. Implemented by the metrics registry to count skip reasons.
type SkipRecorder interface {
	RecordProviderSkip(providerID string, reason string)
}

// StatsProvider optionally extends HealthChecker with scoring data.
type StatsProvider interface {
	GetAvgLatencyMs(providerID string) float64
	GetErrorRate(providerID string) float64
}

type EngineConfig struct {
	DefaultMode         string
	DefaultMaxBudgetUSD float64
	DefaultMaxLatencyMs int
	MaxRetries          int
	// ExplorationTemp controls softmax temperature for load distribution.
	// 0 = always pick top model, 0.5 = moderate exploration, 1.0 = strong.
	ExplorationTemp float64
	// PerProviderTimeoutMs caps how long a single provider attempt may take.
	// When set, each adapter.Send call receives its own context deadline so
	// that one slow or unreachable provider cannot consume the entire routing
	// budget and starve all fallback providers. If 0, per-provider capping is
	// disabled and only the overall DefaultMaxLatencyMs deadline applies.
	PerProviderTimeoutMs int
	// HedgeAfterMs, if > 0, fires a parallel request to the next-best provider
	// when the primary hasn't responded within this interval. Set to 0 to keep
	// purely sequential fallback. A value of 5000 (5 s) is a reasonable starting
	// point: it hedges on slow providers without wasting tokens on fast ones.
	// When 0, all parallelism is disabled and providers are tried sequentially.
	HedgeAfterMs int
	// MaxHedgedProviders caps the number of concurrent in-flight hedged requests.
	// Defaults to 3 (primary + 2 hedges) when HedgeAfterMs > 0 and not set.
	MaxHedgedProviders int
}

type Engine struct {
	cfg          EngineConfig
	health       HealthChecker
	bandit       *ThompsonSampler // nil = disabled
	skipRecorder SkipRecorder
	aliases      *AliasResolver // nil = no alias rewriting
	wildcardRR   uint64

	mu       sync.RWMutex
	models   map[string]Model
	adapters map[string]Sender // provider_id -> adapter
}

func NewEngine(cfg EngineConfig) *Engine {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	return &Engine{
		cfg:      cfg,
		models:   make(map[string]Model),
		adapters: make(map[string]Sender),
	}
}

// SetHealthChecker attaches a health tracker to the engine.
func (e *Engine) SetHealthChecker(h HealthChecker) {
	e.health = h
}

// SetSkipRecorder attaches a recorder that is notified whenever a model or
// provider is excluded from routing (e.g. health_down, budget_exceeded).
func (e *Engine) SetSkipRecorder(sr SkipRecorder) {
	e.skipRecorder = sr
}

// SetBanditPolicy attaches a Thompson Sampling policy for RL-based routing.
// When set and mode is "thompson", model selection uses probabilistic sampling
// instead of the deterministic multi-objective scoring function.
func (e *Engine) SetBanditPolicy(ts *ThompsonSampler) {
	e.bandit = ts
}

// SetAliasResolver attaches an alias resolver for blind A/B model rewriting.
// When set, any ModelHint matching a registered alias is rewritten to one of
// the alias's weighted variants before eligibility/scoring runs. The original
// alias name flows through in Decision.AliasFrom so request logs can attribute
// traffic for experiment analysis.
func (e *Engine) SetAliasResolver(a *AliasResolver) {
	e.aliases = a
}

// AliasResolver returns the currently attached alias resolver, or nil when
// aliasing is disabled. Exposed so the admin API can mutate the registry
// without taking a separate reference.
func (e *Engine) AliasResolver() *AliasResolver {
	return e.aliases
}

// resolveAlias rewrites req.ModelHint through the alias resolver and returns
// the original alias name if a rewrite happened (for recording in Decision).
// Routing callers invoke this once at entry so downstream selection and
// logging both see the post-rewrite model ID.
//
// The hash key used to pick a variant depends on the alias's StickyBy policy:
// by default a given request ID always lands on the same variant; when the
// alias opts into api_key stickiness, the caller's API key ID (pulled from
// req.Meta[MetaAPIKeyID]) is used instead so the same credential is pinned
// to one variant for the life of the experiment.
func (e *Engine) resolveAlias(req *Request) string {
	if req == nil {
		return ""
	}
	req.ModelHint = strings.TrimSpace(req.ModelHint)
	if req.ModelHint == "" {
		return ""
	}

	if e.aliases != nil {
		target, applied := e.aliases.ResolveForRequest(req.ModelHint, req)
		if applied {
			originalAlias := req.ModelHint
			req.ModelHint = target
			return originalAlias
		}
	}

	if IsWildcardModelHint(req.ModelHint) {
		return e.resolveDefaultWildcard(req)
	}
	return ""
}

// ResolveModelHint applies TokenHub's public model-hint semantics to req:
// aliases are rewritten to concrete models, while "*" means "let TokenHub
// choose" unless a "*" alias is configured.
func (e *Engine) ResolveModelHint(req *Request) string {
	return e.resolveAlias(req)
}

func (e *Engine) resolveDefaultWildcard(req *Request) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	defaultModels := DefaultWildcardRoundRobinModelIDs()
	candidates := make([]string, 0, len(defaultModels))
	for _, preferredID := range defaultModels {
		if id, ok := e.availableModelIDLocked(preferredID); ok {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		req.ModelHint = ""
		return ""
	}

	req.ModelHint = candidates[int(e.wildcardRR%uint64(len(candidates)))]
	e.wildcardRR++
	return WildcardModelHint
}

func (e *Engine) availableModelIDLocked(preferredID string) (string, bool) {
	if m, ok := e.models[preferredID]; ok && m.Enabled {
		if _, ok := e.adapters[m.ProviderID]; ok {
			return preferredID, true
		}
	}

	suffix := "/" + preferredID
	var matches []string
	for id, m := range e.models {
		if !m.Enabled || !strings.HasSuffix(id, suffix) {
			continue
		}
		if _, ok := e.adapters[m.ProviderID]; ok {
			matches = append(matches, id)
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	sort.Strings(matches)
	return matches[0], true
}

func (e *Engine) wildcardRoutingPoolLocked(selectedID string) map[string]bool {
	pool := make(map[string]bool)

	if e.aliases != nil {
		if alias, ok := e.aliases.Get(WildcardModelHint); ok && alias.Enabled {
			for _, variant := range alias.Variants {
				if id, ok := e.availableModelIDLocked(variant.ModelID); ok {
					pool[id] = true
				}
			}
		}
	}
	if len(pool) == 0 {
		for _, preferredID := range DefaultWildcardRoundRobinModelIDs() {
			if id, ok := e.availableModelIDLocked(preferredID); ok {
				pool[id] = true
			}
		}
	}
	if selectedID != "" {
		if id, ok := e.availableModelIDLocked(selectedID); ok {
			pool[id] = true
		} else {
			pool[selectedID] = true
		}
	}
	return pool
}

func filterModelsByIDSet(models []Model, allowed map[string]bool) []Model {
	if len(allowed) == 0 {
		return models
	}
	out := models[:0]
	for _, model := range models {
		if allowed[model.ID] {
			out = append(out, model)
		}
	}
	return out
}

// RegisterAdapter registers a provider adapter.
func (e *Engine) RegisterAdapter(a Sender) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adapters[a.ID()] = a
}

// UnregisterAdapter removes a provider adapter by ID. Models that reference
// this provider remain registered but become ineligible for routing until a
// new adapter with the same ID is registered.
func (e *Engine) UnregisterAdapter(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.adapters, id)
}

func (e *Engine) RegisterModel(m Model) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models[m.ID] = m
}

// HasModel returns true if a model with the given ID is registered (enabled or not).
func (e *Engine) HasModel(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.models[id]
	return ok
}

// UnregisterModel removes a model by ID so it is no longer eligible for routing.
func (e *Engine) UnregisterModel(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.models, id)
}

// UpdateDefaults updates the runtime routing policy defaults.
func (e *Engine) UpdateDefaults(mode string, maxBudget float64, maxLatencyMs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mode != "" {
		e.cfg.DefaultMode = mode
	}
	if maxBudget > 0 {
		e.cfg.DefaultMaxBudgetUSD = maxBudget
	}
	if maxLatencyMs > 0 {
		e.cfg.DefaultMaxLatencyMs = maxLatencyMs
	}
}

// ListModels returns all registered models.
func (e *Engine) ListModels() []Model {
	e.mu.RLock()
	defer e.mu.RUnlock()
	models := make([]Model, 0, len(e.models))
	for _, m := range e.models {
		models = append(models, m)
	}
	return models
}

// ListAdapterIDs returns the IDs of all registered adapters.
func (e *Engine) ListAdapterIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]string, 0, len(e.adapters))
	for id := range e.adapters {
		ids = append(ids, id)
	}
	return ids
}

// GetAdapter returns the registered provider adapter for the given provider ID.
func (e *Engine) GetAdapter(providerID string) Sender {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.adapters[providerID]
}

// AdapterInfo holds metadata about a registered adapter for the admin UI.
type AdapterInfo struct {
	ID             string `json:"id"`
	HealthEndpoint string `json:"health_endpoint,omitempty"`
}

// ListAdapterInfo returns metadata for all registered adapters.
func (e *Engine) ListAdapterInfo() []AdapterInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	infos := make([]AdapterInfo, 0, len(e.adapters))
	for id, a := range e.adapters {
		info := AdapterInfo{ID: id}
		if d, ok := a.(Describer); ok {
			info.HealthEndpoint = d.HealthEndpoint()
		}
		infos = append(infos, info)
	}
	return infos
}

// GetModel returns a registered model by ID.
func (e *Engine) GetModel(modelID string) (Model, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	m, ok := e.models[modelID]
	return m, ok
}

// GetAnthropicSender returns an AnthropicRawSender for the model's provider,
// or the first available one if the model isn't found. Returns nil when no
// Anthropic-capable adapter is registered.
func (e *Engine) GetAnthropicSender(modelHint string) AnthropicRawSender {
	s, _ := e.GetAnthropicSenderAndModel(modelHint)
	return s
}

// GetAnthropicSenderAndModel returns the sender and the resolved upstream model
// ID that should be used in the forwarded request body. The resolved ID may
// differ from the hint: e.g. hint "claude-sonnet-4-6" resolves to the
// registered "azure/anthropic/claude-sonnet-4-6" so NVIDIA NIM accepts it.
func (e *Engine) GetAnthropicSenderAndModel(modelHint string) (AnthropicRawSender, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	// Prefer the adapter for the hinted model's provider (exact match).
	if m, ok := e.models[modelHint]; ok {
		if !m.Enabled {
			return nil, ""
		}
		if a, ok := e.adapters[m.ProviderID]; ok {
			if ars, ok := a.(AnthropicRawSender); ok {
				return ars, modelHint
			}
		}
	}
	// Suffix match: find a model whose ID ends with /<hint>.
	// Allows "claude-sonnet-4-6" to resolve to "azure/anthropic/claude-sonnet-4-6".
	suffix := "/" + modelHint
	for id, m := range e.models {
		if !m.Enabled {
			continue
		}
		if !strings.HasSuffix(id, suffix) {
			continue
		}
		if a, ok := e.adapters[m.ProviderID]; ok {
			if ars, ok := a.(AnthropicRawSender); ok {
				return ars, id
			}
		}
	}
	// Fallback: first adapter that implements AnthropicRawSender.
	for _, a := range e.adapters {
		if ars, ok := a.(AnthropicRawSender); ok {
			// Keep fallback scoped to enabled models so disabled providers are
			// never selected through Anthropic passthrough.
			for resolvedID, m := range e.models {
				if m.Enabled && m.ProviderID == a.ID() {
					return ars, resolvedID
				}
			}
		}
	}
	return nil, ""
}
