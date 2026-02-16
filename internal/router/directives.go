package router

import (
	"strconv"
	"strings"
)

// maxDirectiveScan limits how far into a message we scan for directives.
const maxDirectiveScan = 2048

// directivePrefix is the in-band marker that clients embed in message content.
const directivePrefix = "@@tokenhub"

// directiveEnd is the closing marker for block-style directives.
const directiveEnd = "@@end"

// ParseDirectives scans the first user message for @@tokenhub directives
// and returns any policy overrides found. Unrecognized keys are ignored.
//
// Single-line format: @@tokenhub key=value key=value ...
// Block format:
//
//	@@tokenhub
//	key=value
//	key=value
//	@@end
//
// Supported keys: mode, budget, latency, min_weight
//
// Example: @@tokenhub mode=cheap budget=0.01 latency=5000
func ParseDirectives(messages []Message) *Policy {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		content := m.Content
		if len(content) > maxDirectiveScan {
			content = content[:maxDirectiveScan]
		}
		idx := strings.Index(content, directivePrefix)
		if idx < 0 {
			continue
		}

		// Extract the rest of the line after @@tokenhub.
		rest := content[idx+len(directivePrefix):]
		firstLine := rest
		if nl := strings.IndexByte(firstLine, '\n'); nl >= 0 {
			firstLine = firstLine[:nl]
		}

		if strings.TrimSpace(firstLine) == "" && strings.IndexByte(rest, '\n') >= 0 {
			// Block directive: parse lines between @@tokenhub and @@end.
			body := rest[strings.IndexByte(rest, '\n')+1:]
			endIdx := strings.Index(body, directiveEnd)
			if endIdx < 0 {
				// Malformed block (no @@end) - skip this directive.
				continue
			}
			block := body[:endIdx]
			p := &Policy{}
			for _, line := range strings.Split(block, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				applyDirectiveKV(p, line)
			}
			return p
		}

		// Single-line directive.
		line := strings.TrimSpace(firstLine)
		if line == "" {
			continue
		}

		p := &Policy{}
		for _, part := range strings.Fields(line) {
			applyDirectiveKV(p, part)
		}
		return p
	}
	return nil
}

// applyDirectiveKV parses a single key=value token and applies it to the policy.
func applyDirectiveKV(p *Policy, token string) {
	kv := strings.SplitN(token, "=", 2)
	if len(kv) != 2 {
		return
	}
	key, val := kv[0], kv[1]
	switch key {
	case "mode":
		p.Mode = val
	case "budget":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			p.MaxBudgetUSD = f
		}
	case "latency":
		if i, err := strconv.Atoi(val); err == nil {
			p.MaxLatencyMs = i
		}
	case "min_weight":
		if i, err := strconv.Atoi(val); err == nil {
			p.MinWeight = i
		}
	}
}

// StripDirectives returns messages with @@tokenhub directives removed from content.
// This prevents directives from being forwarded to providers.
// Supports both single-line and block (@@tokenhub...@@end) directives.
func StripDirectives(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, m := range messages {
		out[i] = m
		idx := strings.Index(m.Content, directivePrefix)
		if idx < 0 {
			continue
		}

		rest := m.Content[idx+len(directivePrefix):]
		firstLine := rest
		if nl := strings.IndexByte(firstLine, '\n'); nl >= 0 {
			firstLine = firstLine[:nl]
		}

		if strings.TrimSpace(firstLine) == "" && strings.IndexByte(rest, '\n') >= 0 {
			// Block directive: strip from @@tokenhub through @@end (inclusive).
			body := rest[strings.IndexByte(rest, '\n')+1:]
			endIdx := strings.Index(body, directiveEnd)
			if endIdx >= 0 {
				// Calculate end position: idx + prefix + firstLine + \n + endIdx + len(@@end)
				blockEnd := idx + len(directivePrefix) + strings.IndexByte(rest, '\n') + 1 + endIdx + len(directiveEnd)
				// Skip a trailing newline if present.
				if blockEnd < len(m.Content) && m.Content[blockEnd] == '\n' {
					blockEnd++
				}
				out[i].Content = m.Content[:idx] + m.Content[blockEnd:]
			} else {
				// No @@end found - strip just the @@tokenhub line as single-line fallback.
				end := strings.IndexByte(m.Content[idx:], '\n')
				if end >= 0 {
					out[i].Content = m.Content[:idx] + m.Content[idx+end+1:]
				} else {
					out[i].Content = strings.TrimSpace(m.Content[:idx])
				}
			}
		} else {
			// Single-line directive: remove just that line.
			end := strings.IndexByte(m.Content[idx:], '\n')
			if end >= 0 {
				out[i].Content = m.Content[:idx] + m.Content[idx+end+1:]
			} else {
				out[i].Content = strings.TrimSpace(m.Content[:idx])
			}
		}
	}
	return out
}
