package router

import (
	"encoding/json"
	"fmt"
)

// ValidateJSONSchema validates that the given schema is valid JSON and contains
// at minimum a "type" field. This is a basic sanity check, not a full JSON
// Schema validator.
func ValidateJSONSchema(schema json.RawMessage) error {
	if len(schema) == 0 {
		return fmt.Errorf("schema is empty")
	}

	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return fmt.Errorf("schema is not valid JSON: %w", err)
	}

	if _, ok := parsed["type"]; !ok {
		return fmt.Errorf("schema missing required \"type\" field")
	}

	return nil
}

// ValidateAgainstSchema performs basic validation that the given data conforms
// to the given schema. It checks:
//   - If schema type is "object", data must be a JSON object
//   - If schema type is "array", data must be a JSON array
//   - If schema type is "string", data must be a JSON string
//   - If schema has "required" array, all required fields must be present in the data object
//
// This is intentionally simple -- it does not implement a full JSON Schema validator.
func ValidateAgainstSchema(data, schema json.RawMessage) error {
	var schemaParsed map[string]any
	if err := json.Unmarshal(schema, &schemaParsed); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	schemaType, _ := schemaParsed["type"].(string)

	// Validate the data type matches the schema type.
	switch schemaType {
	case "object":
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return fmt.Errorf("expected JSON object but got something else")
		}

		// Check required fields if specified.
		if reqRaw, ok := schemaParsed["required"]; ok {
			reqSlice, ok := reqRaw.([]any)
			if !ok {
				return fmt.Errorf("schema \"required\" field must be an array")
			}
			for _, r := range reqSlice {
				fieldName, ok := r.(string)
				if !ok {
					continue
				}
				if _, exists := obj[fieldName]; !exists {
					return fmt.Errorf("missing required field %q", fieldName)
				}
			}
		}

	case "array":
		var arr []any
		if err := json.Unmarshal(data, &arr); err != nil {
			return fmt.Errorf("expected JSON array but got something else")
		}

	case "string":
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("expected JSON string but got something else")
		}
	}

	return nil
}
