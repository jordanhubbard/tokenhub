package httpapi

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// reverseMap returns a new map with keys and values swapped.
func reverseMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	rev := make(map[string]string, len(m))
	for k, v := range m {
		rev[v] = k
	}
	return rev
}

// rewriteToolNames rewrites function names in an OpenAI-format tools array.
// tools is a JSON array of tool objects; nameMap maps old names to new names.
// Returns the original slice unchanged if nameMap is empty or parsing fails.
func rewriteToolNames(tools json.RawMessage, nameMap map[string]string) json.RawMessage {
	if len(nameMap) == 0 || len(tools) == 0 {
		return tools
	}
	var arr []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
			Strict      *bool           `json:"strict,omitempty"`
		} `json:"function"`
	}
	if err := json.Unmarshal(tools, &arr); err != nil {
		return tools
	}
	changed := false
	for i, t := range arr {
		if newName, ok := nameMap[t.Function.Name]; ok {
			arr[i].Function.Name = newName
			changed = true
		}
	}
	if !changed {
		return tools
	}
	out, err := json.Marshal(arr)
	if err != nil {
		return tools
	}
	return out
}

// rewriteChoicesToolCalls rewrites tool call function names in an OpenAI choices
// array (non-streaming). nameMap maps old names to new names.
func rewriteChoicesToolCalls(choices json.RawMessage, nameMap map[string]string) json.RawMessage {
	if len(nameMap) == 0 || len(choices) == 0 {
		return choices
	}
	type toolFunction struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}
	type toolCall struct {
		ID       string       `json:"id,omitempty"`
		Type     string       `json:"type,omitempty"`
		Function toolFunction `json:"function"`
	}
	type message struct {
		Role      string          `json:"role,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
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
		for j, tc := range c.Message.ToolCalls {
			if newName, ok := nameMap[tc.Function.Name]; ok {
				arr[i].Message.ToolCalls[j].Function.Name = newName
				changed = true
			}
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

// newSSEToolNameRewriter wraps an SSE stream and rewrites tool call function
// names in streaming chunks on the fly. nameMap maps old names to new names.
// If nameMap is empty, src is returned unchanged.
func newSSEToolNameRewriter(src io.ReadCloser, nameMap map[string]string) io.ReadCloser {
	if len(nameMap) == 0 {
		return src
	}
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		defer pw.Close()
		scanner := bufio.NewScanner(src)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				data := line[6:]
				if data != "[DONE]" {
					line = "data: " + rewriteSSEChunkToolNames(data, nameMap)
				}
			}
			if _, err := pw.Write([]byte(line + "\n")); err != nil {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			pw.CloseWithError(err)
		}
	}()
	return pr
}

// rewriteSSEChunkToolNames rewrites tool call names in a single SSE data payload.
func rewriteSSEChunkToolNames(data string, nameMap map[string]string) string {
	type toolFunction struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}
	type toolCallDelta struct {
		Index    int          `json:"index"`
		ID       string       `json:"id,omitempty"`
		Type     string       `json:"type,omitempty"`
		Function toolFunction `json:"function,omitempty"`
	}
	type delta struct {
		Role      string          `json:"role,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
		ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int             `json:"index"`
		Delta        delta           `json:"delta"`
		FinishReason *string         `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs,omitempty"`
	}
	type chunk struct {
		ID      string          `json:"id,omitempty"`
		Object  string          `json:"object,omitempty"`
		Created int64           `json:"created,omitempty"`
		Model   string          `json:"model,omitempty"`
		Choices []choice        `json:"choices"`
		Usage   json.RawMessage `json:"usage,omitempty"`
	}

	var c chunk
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return data
	}
	changed := false
	for i, ch := range c.Choices {
		for j, tc := range ch.Delta.ToolCalls {
			if newName, ok := nameMap[tc.Function.Name]; ok {
				c.Choices[i].Delta.ToolCalls[j].Function.Name = newName
				changed = true
			}
		}
	}
	if !changed {
		return data
	}
	out, err := json.Marshal(c)
	if err != nil {
		return data
	}
	return string(out)
}
