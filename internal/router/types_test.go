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

func TestMessageUnmarshal_PreservesToolCalls(t *testing.T) {
	raw := `{"role":"assistant","content":null,"tool_calls":[{"id":"call_abc123","type":"function","function":{"name":"search","arguments":"{\"q\":\"weather\"}"}}]}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Role != "assistant" {
		t.Errorf("role: got %q, want %q", m.Role, "assistant")
	}
	if len(m.ToolCalls) == 0 {
		t.Fatal("tool_calls should not be empty")
	}
	// ToolCalls is preserved as opaque raw JSON; verify the wire fields are intact.
	var decoded []map[string]any
	if err := json.Unmarshal(m.ToolCalls, &decoded); err != nil {
		t.Fatalf("tool_calls is not a JSON array: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(decoded))
	}
	if decoded[0]["id"] != "call_abc123" {
		t.Errorf("tool_calls[0].id: got %v", decoded[0]["id"])
	}
	if decoded[0]["type"] != "function" {
		t.Errorf("tool_calls[0].type: got %v", decoded[0]["type"])
	}
	fn, _ := decoded[0]["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Errorf("tool_calls[0].function.name: got %v", fn["name"])
	}
}

func TestMessageUnmarshal_PreservesToolCallID(t *testing.T) {
	raw := `{"role":"tool","tool_call_id":"call_abc123","content":"temperature is 72°F"}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Role != "tool" {
		t.Errorf("role: got %q, want %q", m.Role, "tool")
	}
	if m.Content != "temperature is 72°F" {
		t.Errorf("content: got %q, want %q", m.Content, "temperature is 72°F")
	}
	if m.ToolCallID != "call_abc123" {
		t.Errorf("tool_call_id: got %q, want %q", m.ToolCallID, "call_abc123")
	}
}

func TestMessageMarshal_RoundTripsToolCalls(t *testing.T) {
	raw := `{"role":"assistant","tool_calls":[{"id":"call_xyz","type":"function","function":{"name":"search","arguments":"{}"}}],"content":null}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var reconstructed struct {
		Role      string          `json:"role"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(out, &reconstructed); err != nil {
		t.Fatalf("re-unmarshal error: %v", err)
	}
	if reconstructed.Role != "assistant" {
		t.Errorf("role: got %q", reconstructed.Role)
	}
	if len(reconstructed.ToolCalls) == 0 {
		t.Fatal("tool_calls missing after marshal round-trip")
	}
}

func TestMessageMarshal_RoundTripsToolCallID(t *testing.T) {
	raw := `{"role":"tool","tool_call_id":"call_xyz","content":"result data"}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var reconstructed struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal(out, &reconstructed); err != nil {
		t.Fatalf("re-unmarshal error: %v", err)
	}
	if reconstructed.ToolCallID != "call_xyz" {
		t.Errorf("tool_call_id: got %q, want %q", reconstructed.ToolCallID, "call_xyz")
	}
}
