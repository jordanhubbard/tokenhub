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

// --- Block directive tests ---

func TestParseDirectivesBlock(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\nmode=adversarial\nbudget=0.05\nlatency=10000\nmin_weight=7\n@@end\nWhat is the meaning of life?"},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from block directive")
	}
	if p.Mode != "adversarial" {
		t.Errorf("expected mode=adversarial, got %s", p.Mode)
	}
	if p.MaxBudgetUSD != 0.05 {
		t.Errorf("expected budget=0.05, got %f", p.MaxBudgetUSD)
	}
	if p.MaxLatencyMs != 10000 {
		t.Errorf("expected latency=10000, got %d", p.MaxLatencyMs)
	}
	if p.MinWeight != 7 {
		t.Errorf("expected min_weight=7, got %d", p.MinWeight)
	}
}

func TestParseDirectivesBlockWithTrailingWhitespace(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub  \nmode=cheap\nbudget=0.01\n@@end\nHello"},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from block directive with trailing whitespace")
	}
	if p.Mode != "cheap" {
		t.Errorf("expected mode=cheap, got %s", p.Mode)
	}
	if p.MaxBudgetUSD != 0.01 {
		t.Errorf("expected budget=0.01, got %f", p.MaxBudgetUSD)
	}
}

func TestParseDirectivesBlockBlankLines(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\n\nmode=cheap\n\nbudget=0.02\n\n@@end\nHello"},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from block directive with blank lines")
	}
	if p.Mode != "cheap" {
		t.Errorf("expected mode=cheap, got %s", p.Mode)
	}
	if p.MaxBudgetUSD != 0.02 {
		t.Errorf("expected budget=0.02, got %f", p.MaxBudgetUSD)
	}
}

func TestParseDirectivesBlockMalformedNoEnd(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\nmode=cheap\nbudget=0.01\nHello world"},
	}
	p := ParseDirectives(msgs)
	if p != nil {
		t.Error("expected nil policy for malformed block directive (no @@end)")
	}
}

func TestStripDirectivesBlock(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\nmode=adversarial\nbudget=0.05\n@@end\nWhat is the meaning of life?"},
	}
	stripped := StripDirectives(msgs)
	if stripped[0].Content != "What is the meaning of life?" {
		t.Errorf("expected 'What is the meaning of life?', got %q", stripped[0].Content)
	}
}

func TestStripDirectivesBlockNoTrailingContent(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\nmode=cheap\n@@end"},
	}
	stripped := StripDirectives(msgs)
	if stripped[0].Content != "" {
		t.Errorf("expected empty content after stripping block, got %q", stripped[0].Content)
	}
}

func TestStripDirectivesBlockMixed(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are helpful"},
		{Role: "user", Content: "Some preamble\n@@tokenhub\nmode=cheap\nbudget=0.01\n@@end\nActual question here"},
	}
	stripped := StripDirectives(msgs)
	if stripped[0].Content != "You are helpful" {
		t.Error("system message should be unchanged")
	}
	expected := "Some preamble\nActual question here"
	if stripped[1].Content != expected {
		t.Errorf("expected %q, got %q", expected, stripped[1].Content)
	}
}

func TestStripDirectivesBlockMalformedNoEnd(t *testing.T) {
	// When @@end is missing, strip only the @@tokenhub line (single-line fallback).
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\nmode=cheap\nActual question"},
	}
	stripped := StripDirectives(msgs)
	expected := "mode=cheap\nActual question"
	if stripped[0].Content != expected {
		t.Errorf("expected %q, got %q", expected, stripped[0].Content)
	}
}

func TestParseDirectivesOutputSchema(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: `@@tokenhub output_schema={"type":"object","required":["name","age"]}` + "\nDescribe a person."},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from directive with output_schema")
	}
	expected := `{"type":"object","required":["name","age"]}`
	if p.OutputSchema != expected {
		t.Errorf("expected output_schema=%s, got %s", expected, p.OutputSchema)
	}
}

func TestParseDirectivesBlockOutputSchema(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub\nmode=planning\noutput_schema={\"type\":\"object\",\"required\":[\"summary\"]}\n@@end\nSummarize this."},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from block directive with output_schema")
	}
	if p.Mode != "planning" {
		t.Errorf("expected mode=planning, got %s", p.Mode)
	}
	expected := `{"type":"object","required":["summary"]}`
	if p.OutputSchema != expected {
		t.Errorf("expected output_schema=%s, got %s", expected, p.OutputSchema)
	}
}

func TestSingleLineStillWorksAfterBlockSupport(t *testing.T) {
	// Verify that existing single-line behavior is fully preserved.
	msgs := []Message{
		{Role: "user", Content: "@@tokenhub mode=cheap budget=0.01 latency=5000\nHello world"},
	}
	p := ParseDirectives(msgs)
	if p == nil {
		t.Fatal("expected policy from single-line directive")
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

	stripped := StripDirectives(msgs)
	if stripped[0].Content != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", stripped[0].Content)
	}
}
