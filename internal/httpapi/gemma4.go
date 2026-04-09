package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// gemma4StringDelim is the special token Gemma 4 uses to wrap string values
// inside tool call argument lists.
const gemma4StringDelim = "<|\"|>"

var (
	// gemma4ThinkRe matches <|channel>thought\n...<channel|> thinking blocks.
	gemma4ThinkRe = regexp.MustCompile(`(?s)<\|channel>thought\n.*?<channel\|>`)
	// gemma4ToolCallRe matches <|tool_call>call:name{args}<tool_call|>.
	// Group 1 = function name, group 2 = raw args string.
	gemma4ToolCallRe = regexp.MustCompile(`(?s)<\|tool_call>call:(\w+)\{(.*?)\}<tool_call\|>`)
)

// gemma4ToolCallResult holds a parsed Gemma 4 tool call.
type gemma4ToolCallResult struct {
	Name      string
	Arguments string // JSON object string ready for OpenAI tool_calls
}

// gemma4ArgsToJSON converts Gemma 4's key:value argument notation to a JSON
// object string. String values are wrapped with <|"|>...<|"|>; non-string
// values are bare JSON literals (numbers, booleans, null).
func gemma4ArgsToJSON(args string) string {
	result := make(map[string]any)
	i := 0
	for i < len(args) {
		colonIdx := strings.IndexByte(args[i:], ':')
		if colonIdx < 0 {
			break
		}
		key := strings.TrimSpace(args[i : i+colonIdx])
		if key == "" {
			break
		}
		i += colonIdx + 1

		if strings.HasPrefix(args[i:], gemma4StringDelim) {
			// String value: <|"|>value<|"|>
			i += len(gemma4StringDelim)
			end := strings.Index(args[i:], gemma4StringDelim)
			if end < 0 {
				result[key] = args[i:]
				break
			}
			result[key] = args[i : i+end]
			i += end + len(gemma4StringDelim)
		} else {
			// Non-string literal: up to the next comma or end.
			end := strings.IndexByte(args[i:], ',')
			var raw string
			if end < 0 {
				raw = strings.TrimSpace(args[i:])
				i = len(args)
			} else {
				raw = strings.TrimSpace(args[i : i+end])
				i += end + 1
			}
			var v any
			if json.Unmarshal([]byte(raw), &v) == nil {
				result[key] = v
			} else {
				result[key] = raw
			}
		}
		if i < len(args) && args[i] == ',' {
			i++
		}
	}
	out, _ := json.Marshal(result)
	return string(out)
}

// gemma4ParseContent strips Gemma 4 thinking blocks and extracts inline tool
// calls from content. Returns the cleaned text and any parsed tool calls.
func gemma4ParseContent(content string) (string, []gemma4ToolCallResult) {
	content = strings.TrimSpace(gemma4ThinkRe.ReplaceAllString(content, ""))

	matches := gemma4ToolCallRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, nil
	}

	var calls []gemma4ToolCallResult
	var sb strings.Builder
	prev := 0
	for _, m := range matches {
		sb.WriteString(content[prev:m[0]])
		calls = append(calls, gemma4ToolCallResult{
			Name:      content[m[2]:m[3]],
			Arguments: gemma4ArgsToJSON(content[m[4]:m[5]]),
		})
		prev = m[1]
	}
	sb.WriteString(content[prev:])
	return strings.TrimSpace(sb.String()), calls
}

// rewriteGemma4Choices transforms the choices array from a Gemma 4 response
// into standard OpenAI format. Thinking blocks are stripped from content;
// inline tool call tokens are converted to tool_calls entries.
func rewriteGemma4Choices(choices json.RawMessage) json.RawMessage {
	type toolFunction struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type toolCall struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function toolFunction `json:"function"`
	}
	type message struct {
		Role      string          `json:"role,omitempty"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []toolCall      `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int             `json:"index"`
		Message      message         `json:"message"`
		FinishReason *string         `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs,omitempty"`
	}

	var arr []choice
	if err := json.Unmarshal(choices, &arr); err != nil {
		return choices
	}

	changed := false
	for i, c := range arr {
		var contentStr string
		if err := json.Unmarshal(c.Message.Content, &contentStr); err != nil {
			continue // null or non-string content
		}

		cleaned, calls := gemma4ParseContent(contentStr)
		if cleaned == contentStr && len(calls) == 0 {
			continue
		}
		changed = true

		if len(calls) > 0 {
			tcs := make([]toolCall, len(calls))
			for j, tc := range calls {
				tcs[j] = toolCall{
					ID:   fmt.Sprintf("call_%d_%d", i, j),
					Type: "function",
					Function: toolFunction{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				}
			}
			arr[i].Message.ToolCalls = tcs
			reason := "tool_calls"
			arr[i].FinishReason = &reason
		}
		if cleaned == "" {
			arr[i].Message.Content = json.RawMessage("null")
		} else {
			raw, _ := json.Marshal(cleaned)
			arr[i].Message.Content = raw
		}
	}

	if !changed {
		return choices
	}
	out, err := json.Marshal(arr)
	if err != nil {
		return choices
	}
	return out
}

// gemma4ContentAcc accumulates SSE content deltas for one choice index.
type gemma4ContentAcc struct {
	role    string
	content strings.Builder
}

// newSSEGemma4Transformer wraps a Gemma 4 SSE stream. Because Gemma 4's tool
// call tokens span many content deltas, it buffers all content per choice and
// emits properly structured OpenAI SSE chunks once the stream ends.
func newSSEGemma4Transformer(src io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		defer pw.Close()

		type chunkMeta struct {
			id      string
			object  string
			created int64
			model   string
		}

		var meta chunkMeta
		hasMeta := false
		accs := make(map[int]*gemma4ContentAcc)

		emit := func(data string) {
			_, _ = pw.Write([]byte("data: " + data + "\n\n"))
		}

		scanner := bufio.NewScanner(src)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				_, _ = pw.Write([]byte(line + "\n"))
				continue
			}
			data := line[6:]
			if data == "[DONE]" {
				gemma4EmitSSE(emit, meta.id, meta.object, meta.created, meta.model, hasMeta, accs)
				_, _ = pw.Write([]byte("data: [DONE]\n\n"))
				return
			}

			var raw struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				Created int64  `json:"created"`
				Model   string `json:"model"`
				Choices []struct {
					Index int `json:"index"`
					Delta struct {
						Role    string `json:"role,omitempty"`
						Content string `json:"content,omitempty"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				_, _ = pw.Write([]byte(line + "\n\n"))
				continue
			}
			if !hasMeta {
				meta = chunkMeta{id: raw.ID, object: raw.Object, created: raw.Created, model: raw.Model}
				hasMeta = true
			}
			for _, c := range raw.Choices {
				a, ok := accs[c.Index]
				if !ok {
					a = &gemma4ContentAcc{role: c.Delta.Role}
					accs[c.Index] = a
				}
				if c.Delta.Role != "" && a.role == "" {
					a.role = c.Delta.Role
				}
				a.content.WriteString(c.Delta.Content)
			}
		}
		if err := scanner.Err(); err != nil {
			pw.CloseWithError(err)
		}
	}()
	return pr
}

// gemma4EmitSSE emits the buffered Gemma 4 content as OpenAI-format SSE chunks.
func gemma4EmitSSE(emit func(string), id, object string, created int64, model string, hasMeta bool, accs map[int]*gemma4ContentAcc) {
	type toolFunction struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}
	type toolCallDelta struct {
		Index    int          `json:"index"`
		ID       string       `json:"id,omitempty"`
		Type     string       `json:"type,omitempty"`
		Function toolFunction `json:"function"`
	}
	type delta struct {
		Role      string          `json:"role,omitempty"`
		Content   *string         `json:"content,omitempty"`
		ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int     `json:"index"`
		Delta        delta   `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	type chunk struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Created int64    `json:"created"`
		Model   string   `json:"model"`
		Choices []choice `json:"choices"`
	}

	if !hasMeta || len(accs) == 0 {
		return
	}
	if object == "" {
		object = "chat.completion.chunk"
	}

	// Process each choice index in order.
	for idx := 0; idx < len(accs); idx++ {
		a, ok := accs[idx]
		if !ok {
			continue
		}

		cleaned, calls := gemma4ParseContent(a.content.String())

		// Emit role delta.
		role := a.role
		if role == "" {
			role = "assistant"
		}
		roleChunk := chunk{
			ID: id, Object: object, Created: created, Model: model,
			Choices: []choice{{Index: idx, Delta: delta{Role: role}, FinishReason: nil}},
		}
		if b, err := json.Marshal(roleChunk); err == nil {
			emit(string(b))
		}

		// Emit content delta if any text remains.
		if cleaned != "" {
			contentChunk := chunk{
				ID: id, Object: object, Created: created, Model: model,
				Choices: []choice{{Index: idx, Delta: delta{Content: &cleaned}, FinishReason: nil}},
			}
			if b, err := json.Marshal(contentChunk); err == nil {
				emit(string(b))
			}
		}

		if len(calls) > 0 {
			// Emit one chunk per tool call with name + arguments.
			for j, tc := range calls {
				callID := fmt.Sprintf("call_%d_%d", idx, j)
				callChunk := chunk{
					ID: id, Object: object, Created: created, Model: model,
					Choices: []choice{{
						Index: idx,
						Delta: delta{
							ToolCalls: []toolCallDelta{{
								Index:    j,
								ID:       callID,
								Type:     "function",
								Function: toolFunction{Name: tc.Name, Arguments: tc.Arguments},
							}},
						},
						FinishReason: nil,
					}},
				}
				if b, err := json.Marshal(callChunk); err == nil {
					emit(string(b))
				}
			}
			// Final chunk with finish_reason: tool_calls.
			reason := "tool_calls"
			finishChunk := chunk{
				ID: id, Object: object, Created: created, Model: model,
				Choices: []choice{{Index: idx, Delta: delta{}, FinishReason: &reason}},
			}
			if b, err := json.Marshal(finishChunk); err == nil {
				emit(string(b))
			}
		} else {
			// Final chunk with finish_reason: stop.
			reason := "stop"
			finishChunk := chunk{
				ID: id, Object: object, Created: created, Model: model,
				Choices: []choice{{Index: idx, Delta: delta{}, FinishReason: &reason}},
			}
			if b, err := json.Marshal(finishChunk); err == nil {
				emit(string(b))
			}
		}
	}
}
