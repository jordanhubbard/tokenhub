package httpapi

import (
	"encoding/json"
	"strings"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

// validateOutputSchema validates model output against an optional JSON Schema.
//
// The schema is expected to be a JSON object that minimally includes a `type`
// property, mirroring router.ValidateJSONSchema / ValidateAgainstSchema behavior.
func validateOutputSchema(resp router.ProviderResponse, schema json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}

	if err := router.ValidateJSONSchema(schema); err != nil {
		return err
	}

	content := strings.TrimSpace(router.ExtractContent(resp))
	// Empty responses can't be type-checked; defer enforcement to model behavior.
	if content == "" {
		return nil
	}

	candidate := candidateJSON(content)
	if candidate == "" {
		// Reuse extractor behavior on the content itself.
		candidate = content
	}

	return router.ValidateAgainstSchema(json.RawMessage(candidate), schema)
}

func candidateJSON(content string) string {
	if strings.HasPrefix(content, "```json") {
		candidate := strings.TrimSpace(content[len("```json"):])
		if end := strings.Index(candidate, "```"); end >= 0 {
			return strings.TrimSpace(candidate[:end])
		}
	}
	if content != "" && (content[0] == '{' || content[0] == '[') {
		return content
	}
	return ""
}
