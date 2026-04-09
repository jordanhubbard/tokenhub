package httpapi

import (
	"encoding/json"
	"testing"
)

func TestGemma4ArgsToJSON(t *testing.T) {
	tests := []struct {
		name string
		args string
		want map[string]any
	}{
		{
			name: "single string arg",
			args: `query:<|"|>quest team OR hermes deploy<|"|>`,
			want: map[string]any{"query": "quest team OR hermes deploy"},
		},
		{
			name: "multiple args mixed types",
			args: `name:<|"|>foo<|"|>,count:42,flag:true`,
			want: map[string]any{"name": "foo", "count": float64(42), "flag": true},
		},
		{
			name: "string with commas inside",
			args: `query:<|"|>a OR b, c OR d<|"|>`,
			want: map[string]any{"query": "a OR b, c OR d"},
		},
		{
			name: "empty args",
			args: ``,
			want: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gemma4ArgsToJSON(tt.args)
			var m map[string]any
			if err := json.Unmarshal([]byte(got), &m); err != nil {
				t.Fatalf("gemma4ArgsToJSON returned invalid JSON: %v", err)
			}
			for k, want := range tt.want {
				got, ok := m[k]
				if !ok {
					t.Errorf("missing key %q", k)
					continue
				}
				if got != want {
					t.Errorf("key %q: got %v, want %v", k, got, want)
				}
			}
			if len(m) != len(tt.want) {
				t.Errorf("got %d keys, want %d", len(m), len(tt.want))
			}
		})
	}
}

func TestGemma4ParseContent(t *testing.T) {
	t.Run("strips think block", func(t *testing.T) {
		input := "<|channel>thought\nI should search for this.\n<channel|>\nHere is my answer."
		cleaned, calls := gemma4ParseContent(input)
		if cleaned != "Here is my answer." {
			t.Errorf("got %q, want %q", cleaned, "Here is my answer.")
		}
		if len(calls) != 0 {
			t.Errorf("expected no tool calls, got %d", len(calls))
		}
	})

	t.Run("extracts tool call", func(t *testing.T) {
		input := `<|tool_call>call:session_search{query:<|"|>quest team OR hermes deploy<|"|>}<tool_call|>`
		cleaned, calls := gemma4ParseContent(input)
		if cleaned != "" {
			t.Errorf("expected empty content, got %q", cleaned)
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Name != "session_search" {
			t.Errorf("name: got %q, want %q", calls[0].Name, "session_search")
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(calls[0].Arguments), &args); err != nil {
			t.Fatalf("invalid arguments JSON: %v", err)
		}
		if args["query"] != "quest team OR hermes deploy" {
			t.Errorf("query: got %v", args["query"])
		}
	})

	t.Run("think block then tool call", func(t *testing.T) {
		input := "<|channel>thought\nLet me think.\n<channel|>\n<|tool_call>call:lookup{id:<|\"|>42<|\"|>}<tool_call|>"
		cleaned, calls := gemma4ParseContent(input)
		if cleaned != "" {
			t.Errorf("expected empty content after tool call removal, got %q", cleaned)
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Name != "lookup" {
			t.Errorf("name: got %q", calls[0].Name)
		}
	})

	t.Run("text with no special tokens", func(t *testing.T) {
		input := "Hello, world!"
		cleaned, calls := gemma4ParseContent(input)
		if cleaned != input {
			t.Errorf("got %q, want %q", cleaned, input)
		}
		if len(calls) != 0 {
			t.Errorf("expected no calls, got %d", len(calls))
		}
	})
}

func TestRewriteGemma4Choices(t *testing.T) {
	t.Run("tool call extracted", func(t *testing.T) {
		input := `[{"index":0,"message":{"role":"assistant","content":"<|tool_call>call:my_tool{arg:<|\"|\">hello<|\"|\">}<tool_call|>"},"finish_reason":"stop"}]`
		out := rewriteGemma4Choices(json.RawMessage(input))

		var arr []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}
		if err := json.Unmarshal(out, &arr); err != nil {
			t.Fatalf("invalid output JSON: %v", err)
		}
		if len(arr) == 0 {
			t.Fatal("empty choices")
		}
		c := arr[0]
		if c.Message.Content != nil {
			t.Errorf("expected null content, got %q", *c.Message.Content)
		}
		if len(c.Message.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(c.Message.ToolCalls))
		}
		if c.Message.ToolCalls[0].Function.Name != "my_tool" {
			t.Errorf("name: got %q", c.Message.ToolCalls[0].Function.Name)
		}
		if c.FinishReason != "tool_calls" {
			t.Errorf("finish_reason: got %q, want tool_calls", c.FinishReason)
		}
	})

	t.Run("think block stripped", func(t *testing.T) {
		input := `[{"index":0,"message":{"role":"assistant","content":"<|channel>thought\ninternal\n<channel|>\nActual answer."},"finish_reason":"stop"}]`
		out := rewriteGemma4Choices(json.RawMessage(input))

		var arr []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(out, &arr); err != nil {
			t.Fatalf("invalid output JSON: %v", err)
		}
		if arr[0].Message.Content != "Actual answer." {
			t.Errorf("got %q, want %q", arr[0].Message.Content, "Actual answer.")
		}
	})

	t.Run("no gemma4 tokens passthrough", func(t *testing.T) {
		input := `[{"index":0,"message":{"role":"assistant","content":"plain text"},"finish_reason":"stop"}]`
		out := rewriteGemma4Choices(json.RawMessage(input))
		// Should return the same bytes when nothing changes.
		if string(out) != input {
			// May differ in whitespace from re-marshal; just check it parses the same.
			var a, b []map[string]any
			json.Unmarshal(json.RawMessage(input), &a)
			json.Unmarshal(out, &b)
			if len(a) != len(b) {
				t.Errorf("passthrough altered choices: %s", out)
			}
		}
	})
}
