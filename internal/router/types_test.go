package router

import (
	"encoding/json"
	"testing"
)

func TestMessageUnmarshal_StringContent(t *testing.T) {
	raw := `{"role":"user","content":"hello world"}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Role != "user" {
		t.Errorf("role: got %q, want %q", m.Role, "user")
	}
	if m.Content != "hello world" {
		t.Errorf("content: got %q, want %q", m.Content, "hello world")
	}
}

func TestMessageUnmarshal_ArrayContent(t *testing.T) {
	raw := `{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Content != "hello world" {
		t.Errorf("content: got %q, want %q", m.Content, "hello world")
	}
}

func TestMessageUnmarshal_ArrayContentSkipsNonText(t *testing.T) {
	raw := `{"role":"user","content":[{"type":"image_url","url":"https://example.com/img.png"},{"type":"text","text":"describe this"}]}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Content != "describe this" {
		t.Errorf("content: got %q, want %q", m.Content, "describe this")
	}
}

func TestMessageUnmarshal_NullContent(t *testing.T) {
	raw := `{"role":"assistant","content":null}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Content != "" {
		t.Errorf("content: got %q, want empty string", m.Content)
	}
}
