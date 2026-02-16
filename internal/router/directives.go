package router

import (
	"strconv"
	"strings"
)

// maxDirectiveScan limits how far into a message we scan for directives.
const maxDirectiveScan = 2048

// directivePrefix is the in-band marker that clients embed in message content.
const directivePrefix = "@@tokenhub"

// ParseDirectives scans the first user message for @@tokenhub directives
// and returns any policy overrides found. Unrecognized keys are ignored.
//
// Format: @@tokenhub key=value key=value ...
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

		// Extract the directive line.
		line := content[idx+len(directivePrefix):]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		p := &Policy{}
		for _, part := range strings.Fields(line) {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
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
		return p
	}
	return nil
}

// StripDirectives returns messages with @@tokenhub directives removed from content.
// This prevents directives from being forwarded to providers.
func StripDirectives(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, m := range messages {
		out[i] = m
		if idx := strings.Index(m.Content, directivePrefix); idx >= 0 {
			// Remove the directive line.
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
