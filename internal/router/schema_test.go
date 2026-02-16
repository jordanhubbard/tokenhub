package router

import (
	"encoding/json"
	"testing"
)

func TestValidateJSONSchemaValid(t *testing.T) {
	schema := json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string"}}}`)
	if err := ValidateJSONSchema(schema); err != nil {
		t.Errorf("expected valid schema to pass, got error: %v", err)
	}
}

func TestValidateJSONSchemaInvalid(t *testing.T) {
	schema := json.RawMessage(`{not valid json`)
	err := ValidateJSONSchema(schema)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestValidateJSONSchemaMissingType(t *testing.T) {
	schema := json.RawMessage(`{"properties": {"name": {"type": "string"}}}`)
	err := ValidateJSONSchema(schema)
	if err == nil {
		t.Error("expected error for missing type field, got nil")
	}
}

func TestValidateAgainstSchemaObject(t *testing.T) {
	schema := json.RawMessage(`{"type": "object"}`)
	data := json.RawMessage(`{"name": "Alice", "age": 30}`)
	if err := ValidateAgainstSchema(data, schema); err != nil {
		t.Errorf("expected object data to pass object schema, got error: %v", err)
	}
}

func TestValidateAgainstSchemaWrongType(t *testing.T) {
	schema := json.RawMessage(`{"type": "object"}`)
	data := json.RawMessage(`[1, 2, 3]`)
	err := ValidateAgainstSchema(data, schema)
	if err == nil {
		t.Error("expected error for array data against object schema, got nil")
	}
}

func TestValidateAgainstSchemaRequiredFields(t *testing.T) {
	schema := json.RawMessage(`{"type": "object", "required": ["name", "email"]}`)
	data := json.RawMessage(`{"name": "Alice"}`)
	err := ValidateAgainstSchema(data, schema)
	if err == nil {
		t.Error("expected error for missing required field 'email', got nil")
	}
}

func TestValidateAgainstSchemaRequiredFieldsPresent(t *testing.T) {
	schema := json.RawMessage(`{"type": "object", "required": ["name", "email"]}`)
	data := json.RawMessage(`{"name": "Alice", "email": "alice@example.com"}`)
	if err := ValidateAgainstSchema(data, schema); err != nil {
		t.Errorf("expected all required fields present to pass, got error: %v", err)
	}
}

func TestValidateAgainstSchemaArray(t *testing.T) {
	schema := json.RawMessage(`{"type": "array"}`)
	data := json.RawMessage(`[1, 2, 3]`)
	if err := ValidateAgainstSchema(data, schema); err != nil {
		t.Errorf("expected array data to pass array schema, got error: %v", err)
	}
}

func TestValidateAgainstSchemaString(t *testing.T) {
	schema := json.RawMessage(`{"type": "string"}`)
	data := json.RawMessage(`"hello world"`)
	if err := ValidateAgainstSchema(data, schema); err != nil {
		t.Errorf("expected string data to pass string schema, got error: %v", err)
	}
}

func TestValidateAgainstSchemaStringWrongType(t *testing.T) {
	schema := json.RawMessage(`{"type": "string"}`)
	data := json.RawMessage(`42`)
	err := ValidateAgainstSchema(data, schema)
	if err == nil {
		t.Error("expected error for number data against string schema, got nil")
	}
}

func TestValidateJSONSchemaEmpty(t *testing.T) {
	schema := json.RawMessage(``)
	err := ValidateJSONSchema(schema)
	if err == nil {
		t.Error("expected error for empty schema, got nil")
	}
}
