package router

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"sync"
)

// Sticky-by policies for alias routing. These control which request attribute
// is hashed to pick a variant, i.e. what "the same X" means for "the same X
// always lands on the same variant".
const (
	// StickyByRequest hashes on the request ID. Different HTTP requests (even
	// from the same caller) can land on different variants; each request is
	// stable within itself (retries, hedging, fallback paths). This is the
	// right default for stateless workloads and cleanly randomized A/B.
	StickyByRequest = "request"
	// StickyByAPIKey hashes on the API key ID so every request from the same
	// credential lands on the same variant for the life of the experiment.
	// Use this when variants differ enough that mid-session flipping would
	// confuse the caller (tone, output format, tool-use behaviour).
	StickyByAPIKey = "api_key"
	// StickyByRoundRobin ignores request attributes and cycles through alias
	// variants in weighted order. Use this for wildcard model diversity when
	// consecutive agents should intentionally land on different backends.
	StickyByRoundRobin = "round_robin"
)

// MetaAPIKeyID is the Request.Meta key under which handlers stash the API
// key ID so that StickyByAPIKey aliases can route on it. Kept as a string
// constant here so the router and httpapi packages can't drift apart.
const MetaAPIKeyID = "api_key_id"

// AliasVariant is one target in a weighted alias split. A variant routes some
// fraction of the alias's traffic to ModelID, where the fraction is Weight
// relative to the sum of all variant weights on the alias.
type AliasVariant struct {
	ModelID string `json:"model_id"`
	Weight  int    `json:"weight"`
}

// Alias maps a client-facing model name to a weighted set of real models.
// When a client request hints "gpt-4o" and an alias named "gpt-4o" exists,
// the engine transparently rewrites ModelHint to one of the variants chosen
// by deterministic hash of the request ID. This enables blind A/B tests:
// the caller never sees which variant served the response, but request logs
// record both the alias and the resolved target for side-by-side analysis.
//
// Disabled aliases are a no-op: the original ModelHint is preserved.
type Alias struct {
	Name     string         `json:"name"`
	Variants []AliasVariant `json:"variants"`
	Enabled  bool           `json:"enabled"`
	// StickyBy chooses the mechanism used to pick a variant. Accepted values:
	// "" / "request" (hash on Request.ID — default, independent per request)
	// or "api_key" (hash on the caller's API key ID so a given key always
	// lands on the same variant), or "round_robin" (cycle through variants).
	// If "api_key" is selected but the request carries no API key ID, the
	// resolver falls back to the request ID so traffic is never black-holed on
	// a missing attribute.
	StickyBy string `json:"sticky_by,omitempty"`
}

// Validate returns an error when the alias is malformed. An alias must have a
// non-empty name, at least one variant, all variants must have a non-empty
// ModelID and strictly positive Weight, and the name itself must not appear
// as a ModelID in its own variants (which would be a routing loop).
func (a Alias) Validate() error {
	if a.Name == "" {
		return errors.New("alias name must not be empty")
	}
	if len(a.Variants) == 0 {
		return errors.New("alias must have at least one variant")
	}
	switch a.StickyBy {
	case "", StickyByRequest, StickyByAPIKey, StickyByRoundRobin:
		// ok
	default:
		return fmt.Errorf("sticky_by %q: must be one of %q, %q, %q (or empty)",
			a.StickyBy, StickyByRequest, StickyByAPIKey, StickyByRoundRobin)
	}
	seen := make(map[string]bool, len(a.Variants))
	for i, v := range a.Variants {
		if v.ModelID == "" {
			return fmt.Errorf("variant[%d].model_id must not be empty", i)
		}
		if v.Weight <= 0 {
			return fmt.Errorf("variant[%d].weight must be > 0, got %d", i, v.Weight)
		}
		if v.ModelID == a.Name {
			return fmt.Errorf("variant[%d] references the alias name %q — would loop", i, a.Name)
		}
		if seen[v.ModelID] {
			return fmt.Errorf("variant[%d] duplicates model_id %q", i, v.ModelID)
		}
		seen[v.ModelID] = true
	}
	return nil
}

// keyForRequest returns the hash key to use when routing req through this
// alias, honoring StickyBy. Falls back to the request ID when the sticky
// attribute is absent — never returning empty unless the request itself has
// no ID, in which case the resolver's random-selection path takes over.
func (a Alias) keyForRequest(req *Request) string {
	switch a.StickyBy {
	case StickyByAPIKey:
		if req != nil && req.Meta != nil {
			if v, ok := req.Meta[MetaAPIKeyID]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		// Fall through to request ID when no API key is available. Missing
		// API key should not disable the experiment — better to roll the dice
		// per request than to black-hole the traffic.
		fallthrough
	case "", StickyByRequest:
		if req != nil {
			return req.ID
		}
	}
	return ""
}

// AliasResolver holds a registry of aliases and resolves a client-supplied
// ModelHint to a concrete ModelID using a deterministic hash of a routing key
// (usually the request ID). The same key always picks the same variant so
// idempotent replays and retries land on the same backend.
type AliasResolver struct {
	mu         sync.RWMutex
	aliases    map[string]Alias
	roundRobin map[string]int64
}

// NewAliasResolver returns an empty resolver.
func NewAliasResolver() *AliasResolver {
	return &AliasResolver{
		aliases:    make(map[string]Alias),
		roundRobin: make(map[string]int64),
	}
}

// Set inserts or replaces an alias. The alias must pass Validate; invalid
// aliases are rejected so the live registry never contains broken entries
// that would silently drop traffic or route in circles.
func (r *AliasResolver) Set(a Alias) error {
	if err := a.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.roundRobin == nil {
		r.roundRobin = make(map[string]int64)
	}
	r.aliases[a.Name] = a
	r.mu.Unlock()
	return nil
}

// Delete removes an alias by name. Missing names are a silent no-op so
// callers don't have to check existence first.
func (r *AliasResolver) Delete(name string) {
	r.mu.Lock()
	delete(r.aliases, name)
	delete(r.roundRobin, name)
	r.mu.Unlock()
}

// Get returns the alias for name, or ok=false when absent.
func (r *AliasResolver) Get(name string) (Alias, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.aliases[name]
	return a, ok
}

// List returns all registered aliases sorted by name for stable output.
func (r *AliasResolver) List() []Alias {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Alias, 0, len(r.aliases))
	for _, a := range r.aliases {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve returns the target ModelID chosen for the given name and key.
//
//   - If name is not registered (or the alias is disabled), returns (name, false).
//   - Otherwise the variant is chosen by stable FNV-1a hash of key, modulo the
//     sum of weights. An empty key triggers random selection (used when the
//     request carries no stable identifier yet).
//
// The boolean return is true whenever an alias was actually applied — callers
// use it to record the original alias name in request logs so A/B analysis
// can join on it later.
func (r *AliasResolver) Resolve(name, key string) (string, bool) {
	r.mu.RLock()
	a, ok := r.aliases[name]
	r.mu.RUnlock()
	if !ok || !a.Enabled || len(a.Variants) == 0 {
		return name, false
	}

	total := aliasTotalWeight(a)
	if total <= 0 {
		// Validate() prevents this in practice; fall back to the first variant
		// as a defensive default so a malformed entry never black-holes traffic.
		return a.Variants[0].ModelID, true
	}

	var bucket int64
	if key == "" {
		bucket = rand.Int63n(total)
	} else {
		h := fnv.New32a()
		_, _ = h.Write([]byte(key))
		bucket = int64(h.Sum32()) % total
	}

	return aliasVariantForBucket(a, bucket), true
}

// ResolveForRequest is Resolve's request-aware sibling. It selects the variant
// based on the alias's StickyBy policy: hash request ID, hash API key ID, or
// advance the alias's round-robin counter. Callers in the engine use this to
// transparently honor assignments without caring about the mechanism.
//
// When the alias is absent or disabled, returns (name, false) unchanged.
func (r *AliasResolver) ResolveForRequest(name string, req *Request) (string, bool) {
	r.mu.Lock()
	a, ok := r.aliases[name]
	if ok && a.Enabled && a.StickyBy == StickyByRoundRobin {
		total := aliasTotalWeight(a)
		if total <= 0 {
			r.mu.Unlock()
			return a.Variants[0].ModelID, true
		}
		if r.roundRobin == nil {
			r.roundRobin = make(map[string]int64)
		}
		bucket := r.roundRobin[name] % total
		r.roundRobin[name] = bucket + 1
		target := aliasVariantForBucket(a, bucket)
		r.mu.Unlock()
		return target, true
	}
	r.mu.Unlock()
	if !ok || !a.Enabled {
		return name, false
	}
	return r.Resolve(name, a.keyForRequest(req))
}

// ReplaceAll atomically swaps the alias set. Used on startup to hydrate the
// resolver from the persistent store without emitting intermediate states.
// Invalid aliases are skipped with the error accumulated into the return.
func (r *AliasResolver) ReplaceAll(aliases []Alias) error {
	next := make(map[string]Alias, len(aliases))
	var firstErr error
	for _, a := range aliases {
		if err := a.Validate(); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("alias %q: %w", a.Name, err)
			}
			continue
		}
		next[a.Name] = a
	}
	r.mu.Lock()
	r.aliases = next
	r.roundRobin = make(map[string]int64)
	r.mu.Unlock()
	return firstErr
}

func aliasTotalWeight(a Alias) int64 {
	var total int64
	for _, v := range a.Variants {
		total += int64(v.Weight)
	}
	return total
}

func aliasVariantForBucket(a Alias, bucket int64) string {
	var cum int64
	for _, v := range a.Variants {
		cum += int64(v.Weight)
		if bucket < cum {
			return v.ModelID
		}
	}
	return a.Variants[len(a.Variants)-1].ModelID
}
