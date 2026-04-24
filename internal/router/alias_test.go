package router

import (
	"fmt"
	"math"
	"testing"
)

func TestAlias_Validate(t *testing.T) {
	cases := []struct {
		name    string
		alias   Alias
		wantErr bool
	}{
		{
			name:    "valid two-variant split",
			alias:   Alias{Name: "exp1", Variants: []AliasVariant{{ModelID: "a", Weight: 50}, {ModelID: "b", Weight: 50}}, Enabled: true},
			wantErr: false,
		},
		{
			name:    "empty name",
			alias:   Alias{Variants: []AliasVariant{{ModelID: "a", Weight: 1}}},
			wantErr: true,
		},
		{
			name:    "no variants",
			alias:   Alias{Name: "exp1"},
			wantErr: true,
		},
		{
			name:    "zero weight",
			alias:   Alias{Name: "exp1", Variants: []AliasVariant{{ModelID: "a", Weight: 0}}},
			wantErr: true,
		},
		{
			name:    "negative weight",
			alias:   Alias{Name: "exp1", Variants: []AliasVariant{{ModelID: "a", Weight: -1}}},
			wantErr: true,
		},
		{
			name:    "self-reference loop",
			alias:   Alias{Name: "gpt-4o", Variants: []AliasVariant{{ModelID: "gpt-4o", Weight: 1}}},
			wantErr: true,
		},
		{
			name:    "duplicate target",
			alias:   Alias{Name: "exp1", Variants: []AliasVariant{{ModelID: "a", Weight: 50}, {ModelID: "a", Weight: 50}}},
			wantErr: true,
		},
		{
			name:    "empty model_id",
			alias:   Alias{Name: "exp1", Variants: []AliasVariant{{ModelID: "", Weight: 1}}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.alias.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAliasResolver_Unknown(t *testing.T) {
	r := NewAliasResolver()
	target, applied := r.Resolve("unknown", "req-1")
	if applied {
		t.Fatal("unknown alias should not apply")
	}
	if target != "unknown" {
		t.Fatalf("expected passthrough, got %q", target)
	}
}

func TestAliasResolver_Disabled(t *testing.T) {
	r := NewAliasResolver()
	if err := r.Set(Alias{
		Name:     "exp",
		Variants: []AliasVariant{{ModelID: "a", Weight: 1}},
		Enabled:  false,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	target, applied := r.Resolve("exp", "req-1")
	if applied {
		t.Fatal("disabled alias should not apply")
	}
	if target != "exp" {
		t.Fatalf("expected original name, got %q", target)
	}
}

func TestAliasResolver_Deterministic(t *testing.T) {
	r := NewAliasResolver()
	if err := r.Set(Alias{
		Name: "exp",
		Variants: []AliasVariant{
			{ModelID: "a", Weight: 50},
			{ModelID: "b", Weight: 50},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	key := "stable-req-id"
	target1, applied := r.Resolve("exp", key)
	if !applied {
		t.Fatal("alias should apply")
	}
	// Repeating with the same key must always land on the same variant.
	for i := 0; i < 100; i++ {
		got, _ := r.Resolve("exp", key)
		if got != target1 {
			t.Fatalf("non-deterministic: iteration %d got %q, want %q", i, got, target1)
		}
	}
}

func TestAliasResolver_WeightDistribution(t *testing.T) {
	// 80/20 split: over a large sample of synthetic request IDs, the observed
	// share should approach the configured weight share. This also verifies
	// that the FNV hash distributes bucket assignments roughly uniformly.
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name: "exp",
		Variants: []AliasVariant{
			{ModelID: "a", Weight: 80},
			{ModelID: "b", Weight: 20},
		},
		Enabled: true,
	})

	counts := map[string]int{}
	const n = 10000
	for i := 0; i < n; i++ {
		target, _ := r.Resolve("exp", fmt.Sprintf("req-%d", i))
		counts[target]++
	}
	if counts["a"]+counts["b"] != n {
		t.Fatalf("unexpected target keys: %v", counts)
	}
	shareA := float64(counts["a"]) / float64(n)
	if math.Abs(shareA-0.8) > 0.03 {
		t.Fatalf("share of A = %.3f; expected ~0.80 within ±0.03 (counts=%v)", shareA, counts)
	}
}

func TestAliasResolver_SingleVariantAlwaysPicksIt(t *testing.T) {
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name:     "exp",
		Variants: []AliasVariant{{ModelID: "only", Weight: 7}},
		Enabled:  true,
	})
	for i := 0; i < 50; i++ {
		got, applied := r.Resolve("exp", fmt.Sprintf("k-%d", i))
		if !applied || got != "only" {
			t.Fatalf("single variant must always resolve: got (%q, %v)", got, applied)
		}
	}
}

func TestAliasResolver_Delete(t *testing.T) {
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name:     "exp",
		Variants: []AliasVariant{{ModelID: "a", Weight: 1}},
		Enabled:  true,
	})
	r.Delete("exp")
	if _, applied := r.Resolve("exp", "k"); applied {
		t.Fatal("deleted alias should not resolve")
	}
	// Double-delete is a no-op.
	r.Delete("exp")
}

func TestAliasResolver_ReplaceAll(t *testing.T) {
	r := NewAliasResolver()
	valid := Alias{
		Name:     "ok",
		Variants: []AliasVariant{{ModelID: "a", Weight: 1}},
		Enabled:  true,
	}
	invalid := Alias{Name: "bad"} // no variants
	if err := r.ReplaceAll([]Alias{valid, invalid}); err == nil {
		t.Fatal("expected validation error for bad alias")
	}
	// The valid alias is installed; the invalid one is skipped.
	if _, ok := r.Get("ok"); !ok {
		t.Fatal("valid alias should be installed even when another fails validation")
	}
	if _, ok := r.Get("bad"); ok {
		t.Fatal("invalid alias must NOT be installed")
	}
}

func TestAliasResolver_EmptyKeyUsesRandom(t *testing.T) {
	// Empty key triggers rand.Intn; across many calls we expect to see both
	// variants. This test guards against a regression where an empty key
	// always resolved to the first variant.
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name: "exp",
		Variants: []AliasVariant{
			{ModelID: "a", Weight: 1},
			{ModelID: "b", Weight: 1},
		},
		Enabled: true,
	})
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		got, _ := r.Resolve("exp", "")
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected both variants to appear with empty key; saw %v", seen)
	}
}

func TestEngine_ResolveAliasRewrites(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name:     "gpt-4o",
		Variants: []AliasVariant{{ModelID: "claude-sonnet", Weight: 1}},
		Enabled:  true,
	})
	eng.SetAliasResolver(r)

	req := Request{ModelHint: "gpt-4o", ID: "req-abc"}
	alias := eng.resolveAlias(&req)
	if alias != "gpt-4o" {
		t.Fatalf("expected alias 'gpt-4o' captured, got %q", alias)
	}
	if req.ModelHint != "claude-sonnet" {
		t.Fatalf("expected ModelHint rewritten to variant, got %q", req.ModelHint)
	}
}

func TestEngine_ResolveAliasNoopWhenNilResolver(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	req := Request{ModelHint: "gpt-4o", ID: "req-abc"}
	alias := eng.resolveAlias(&req)
	if alias != "" {
		t.Fatalf("nil resolver should be a no-op, got alias %q", alias)
	}
	if req.ModelHint != "gpt-4o" {
		t.Fatalf("ModelHint should be unchanged, got %q", req.ModelHint)
	}
}

// --- Sticky-by-user assignment ------------------------------------------------

func TestAlias_Validate_StickyBy(t *testing.T) {
	cases := []struct {
		name     string
		stickyBy string
		wantErr  bool
	}{
		{"empty is default", "", false},
		{"explicit request", StickyByRequest, false},
		{"api_key", StickyByAPIKey, false},
		{"bogus value rejected", "user_id", true},
	}
	base := []AliasVariant{{ModelID: "a", Weight: 1}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Alias{Name: "exp", Variants: base, StickyBy: tc.stickyBy}
			err := a.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for sticky_by=%q", tc.stickyBy)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for sticky_by=%q: %v", tc.stickyBy, err)
			}
		})
	}
}

func TestAliasResolver_ResolveForRequest_StickyByRequest(t *testing.T) {
	// Default StickyBy (empty) hashes on request ID — different requests from
	// the same API key can land on different variants.
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name: "exp",
		Variants: []AliasVariant{
			{ModelID: "a", Weight: 1},
			{ModelID: "b", Weight: 1},
		},
		Enabled: true,
	})
	apiKey := "key-1"
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		req := &Request{
			ID:   fmt.Sprintf("req-%d", i),
			Meta: map[string]any{MetaAPIKeyID: apiKey},
		}
		got, ok := r.ResolveForRequest("exp", req)
		if !ok {
			t.Fatal("alias should apply")
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("request-sticky should spread across variants for one api_key; got %v", seen)
	}
}

func TestAliasResolver_ResolveForRequest_StickyByAPIKey(t *testing.T) {
	// With StickyByAPIKey, the same api_key always maps to the same variant
	// regardless of request ID. Across many api_keys we still see both
	// variants (proves the hash is actually running over the api_key value,
	// not pinning everyone to variant 0).
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name: "exp",
		Variants: []AliasVariant{
			{ModelID: "a", Weight: 1},
			{ModelID: "b", Weight: 1},
		},
		Enabled:  true,
		StickyBy: StickyByAPIKey,
	})

	// Step 1: per-key stability — 50 different req IDs, same api_key.
	keyA := "user-alice"
	var firstA string
	for i := 0; i < 50; i++ {
		req := &Request{
			ID:   fmt.Sprintf("req-%d", i),
			Meta: map[string]any{MetaAPIKeyID: keyA},
		}
		got, _ := r.ResolveForRequest("exp", req)
		if i == 0 {
			firstA = got
		}
		if got != firstA {
			t.Fatalf("api_key stickiness broken: iter %d got %q, expected stable %q", i, got, firstA)
		}
	}

	// Step 2: across many api_keys we should see both variants.
	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		req := &Request{
			ID:   "some-req", // constant on purpose — prove ID doesn't matter
			Meta: map[string]any{MetaAPIKeyID: fmt.Sprintf("user-%d", i)},
		}
		got, _ := r.ResolveForRequest("exp", req)
		counts[got]++
	}
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Fatalf("api_key-sticky should route both variants across distinct keys; got %v", counts)
	}
}

func TestAliasResolver_ResolveForRequest_StickyByAPIKeyMissingFallsBack(t *testing.T) {
	// An api_key-sticky alias with no api_key in Meta must not black-hole
	// traffic. It falls back to request ID.
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name:     "exp",
		Variants: []AliasVariant{{ModelID: "a", Weight: 1}, {ModelID: "b", Weight: 1}},
		Enabled:  true,
		StickyBy: StickyByAPIKey,
	})
	req := &Request{ID: "req-1"} // no Meta
	got, applied := r.ResolveForRequest("exp", req)
	if !applied {
		t.Fatal("alias should apply even when api_key is missing")
	}
	if got != "a" && got != "b" {
		t.Fatalf("expected a real variant, got %q", got)
	}
	// Determinism must still hold on the fallback: same req.ID => same variant.
	for i := 0; i < 20; i++ {
		again, _ := r.ResolveForRequest("exp", req)
		if again != got {
			t.Fatalf("fallback should still be request-deterministic: got %q vs %q", again, got)
		}
	}
}

func TestEngine_ResolveAliasHonorsAPIKeyStickiness(t *testing.T) {
	// End-to-end on the Engine wrapper: same api_key, different request IDs
	// -> same variant is pinned.
	eng := NewEngine(EngineConfig{})
	r := NewAliasResolver()
	_ = r.Set(Alias{
		Name: "gpt-4o",
		Variants: []AliasVariant{
			{ModelID: "m-a", Weight: 1},
			{ModelID: "m-b", Weight: 1},
		},
		Enabled:  true,
		StickyBy: StickyByAPIKey,
	})
	eng.SetAliasResolver(r)

	apiKey := "stable-key"
	var pinned string
	for i := 0; i < 30; i++ {
		req := Request{
			ModelHint: "gpt-4o",
			ID:        fmt.Sprintf("req-%d", i),
			Meta:      map[string]any{MetaAPIKeyID: apiKey},
		}
		alias := eng.resolveAlias(&req)
		if alias != "gpt-4o" {
			t.Fatalf("expected alias capture, got %q", alias)
		}
		if i == 0 {
			pinned = req.ModelHint
		}
		if req.ModelHint != pinned {
			t.Fatalf("api_key pinning broken: iter %d => %q, expected %q", i, req.ModelHint, pinned)
		}
	}
}
