package router

import (
	"encoding/json"
	"regexp"
	"strings"
)

var thinkBlockRe = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// ShapeOutput applies OutputFormat transformations to a provider response.
// Returns the modified response.
func ShapeOutput(resp ProviderResponse, fmt OutputFormat) ProviderResponse {
	if fmt.Type == "" && !fmt.StripThink && fmt.MaxTokens == 0 {
		return resp // no shaping requested
	}

	content := ExtractContent(resp)
	if content == "" {
		return resp
	}

	// Strip <think> blocks if requested.
	if fmt.StripThink {
		content = thinkBlockRe.ReplaceAllString(content, "")
		content = strings.TrimSpace(content)
	}

	// Truncate by approximate token count (chars/4).
	if fmt.MaxTokens > 0 {
		maxChars := fmt.MaxTokens * 4
		if len(content) > maxChars {
			content = content[:maxChars] + "..."
		}
	}

	// Format type shaping.
	switch fmt.Type {
	case "json":
		content = extractJSON(content)
	case "markdown":
		// Already likely markdown; just ensure clean output.
		content = strings.TrimSpace(content)
	case "text":
		// Strip markdown formatting.
		content = stripMarkdown(content)
	}

	// Wrap back into OpenAI-compatible response format.
	shaped := map[string]any{
		"choices": []map[string]any{
			{
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
			},
		},
	}
	result, _ := json.Marshal(shaped)
	return result
}

// extractJSON attempts to find a JSON block within the content.
func extractJSON(content string) string {
	// Try to find ```json ... ``` blocks first.
	if idx := strings.Index(content, "```json"); idx >= 0 {
		start := idx + 7
		if end := strings.Index(content[start:], "```"); end >= 0 {
			return strings.TrimSpace(content[start : start+end])
		}
	}
	// Try to find raw JSON (starts with { or [).
	content = strings.TrimSpace(content)
	if len(content) > 0 && (content[0] == '{' || content[0] == '[') {
		return content
	}
	return content
}

// stripMarkdown removes common markdown formatting.
func stripMarkdown(content string) string {
	// Remove headers.
	lines := strings.Split(content, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		// Remove bold/italic.
		line = strings.ReplaceAll(line, "**", "")
		line = strings.ReplaceAll(line, "*", "")
		line = strings.ReplaceAll(line, "`", "")
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
