package router

import "testing"

func TestParseDirectivesBasic(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub mode=cheap budget=0.01 latency=5000\nHello world"},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from directive")
	}
	if p.Mode != "cheap" {
		t.Errorf("expected mode=cheap, got %s", p.Mode)
	}
	if p.MaxBudgetUSD != 0.01 {
		t.Errorf("expected budget=0.01, got %f", p.MaxBudgetUSD)
	}
	if p.MaxLatencyMs != 5000 {
		t.Errorf("expected latency=5000, got %d", p.MaxLatencyMs)
	}
}

func TestParseDirectivesNoDirective(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "Just a normal message"},
	}
	p := ParseDirectives(msgs)
	if p != nil {
		t.Error("expected nil policy for message without directive")
	}
}

func TestParseDirectivesSystemMessageIgnored(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "@@tokenhub mode=cheap"},
		{Role: "user", Content: "Hi"},
	}
	p := ParseDirectives(msgs)
	if p != nil {
		t.Error("expected nil - directives in system messages should be ignored")
	}
}

func TestParseDirectivesMinWeight(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub min_weight=7"},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy")
	}
	if p.MinWeight != 7 {
		t.Errorf("expected min_weight=7, got %d", p.MinWeight)
	}
}

func TestStripDirectives(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub mode=cheap\nHello world"},
	}
	stripped := StripDirectives(msgs)
	if stripped[0].Content != "Hello world" {
		t.Errorf("expected stripped content to be 'Hello world', got %q", stripped[0].Content)
	}
}

func TestStripDirectivesNoNewline(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub mode=cheap"},
	}
	stripped := StripDirectives(msgs)
	if stripped[0].Content != "" {
		t.Errorf("expected empty content after stripping, got %q", stripped[0].Content)
	}
}

func TestStripDirectivesPreservesOtherMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are helpful"},
		{Role: "user", Content: "prefix @@tokenhub mode=cheap\nactual question"},
	}
	stripped := StripDirectives(msgs)
	if stripped[0].Content != "You are helpful" {
		t.Error("system message should be unchanged")
	}
	if stripped[1].Content != "prefix actual question" {
		t.Errorf("expected 'prefix actual question', got %q", stripped[1].Content)
	}
}
