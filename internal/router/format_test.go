package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShapeOutputNoOp(t *testing.T) {
	resp := ProviderResponse(`{"choices":[{"message":{"content":"Hello"}}]}`)
	result := ShapeOutput(resp, OutputFormat{})
	if string(result) != string(resp) {
		t.Error("expected no change with empty format")
	}
}

func TestShapeOutputStripThink(t *testing.T) {
	resp := ProviderResponse(`{"choices":[{"message":{"content":"<think>internal reasoning</think>\nFinal answer"}}]}`)
	result := ShapeOutput(resp, OutputFormat{StripThink: true})

	content := extractContent(result)
	if strings.Contains(content, "<think>") {
		t.Error("think block should be stripped")
	}
	if !strings.Contains(content, "Final answer") {
		t.Error("expected 'Final answer' to remain")
	}
}

func TestShapeOutputMaxTokens(t *testing.T) {
	long := strings.Repeat("word ", 500) // ~2500 chars = ~625 tokens
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": long}},
		},
	})
	result := ShapeOutput(resp, OutputFormat{MaxTokens: 100}) // ~400 chars

	content := extractContent(result)
	if len(content) > 410 { // 400 chars + "..."
		t.Errorf("expected truncated content, got %d chars", len(content))
	}
	if !strings.HasSuffix(content, "...") {
		t.Error("expected ellipsis at end of truncated content")
	}
}

func TestShapeOutputJSON(t *testing.T) {
	resp := ProviderResponse(`{"choices":[{"message":{"content":"Here is the result:\n` + "```json\n{\"key\":\"value\"}\n```" + `"}}]}`)
	result := ShapeOutput(resp, OutputFormat{Type: "json"})

	content := extractContent(result)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Errorf("expected valid JSON, got error: %v, content: %s", err, content)
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %v", parsed)
	}
}

func TestShapeOutputText(t *testing.T) {
	resp := ProviderResponse(`{"choices":[{"message":{"content":"# Hello\n\n**bold** and *italic*"}}]}`)
	result := ShapeOutput(resp, OutputFormat{Type: "text"})

	content := extractContent(result)
	if strings.Contains(content, "#") {
		t.Error("markdown headers should be stripped")
	}
	if strings.Contains(content, "**") {
		t.Error("bold markers should be stripped")
	}
}

func TestExtractJSONRawObject(t *testing.T) {
	result := extractJSON(`{"key": "value"}`)
	if result != `{"key": "value"}` {
		t.Errorf("expected raw JSON preserved, got %s", result)
	}
}

func TestExtractJSONFromCodeBlock(t *testing.T) {
	result := extractJSON("Some text\n```json\n{\"a\":1}\n```\nMore text")
	if result != `{"a":1}` {
		t.Errorf("expected extracted JSON, got %s", result)
	}
}
