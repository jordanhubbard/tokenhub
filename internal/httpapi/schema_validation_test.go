package httpapi

import (
	"testing"
)

func TestValidateOutputSchemaNoSchema(t *testing.T) {
	resp := []byte(`{"choices":[{"message":{"content":"{\"name\":\"Alice\",\"age\":30}"}}]}`)
	if err := validateOutputSchema(resp, nil); err != nil {
		t.Fatalf("expected empty schema to pass, got error: %v", err)
	}
}

func TestValidateOutputSchemaRejectsInvalidSchema(t *testing.T) {
	resp := []byte(`{"choices":[{"message":{"content":"{\"name\":\"Alice\"}"}}]}`)
	err := validateOutputSchema(resp, []byte(`{invalid}`))
	if err == nil {
		t.Fatal("expected invalid schema to error")
	}
}

func TestValidateOutputSchemaValidObject(t *testing.T) {
	resp := []byte(`{"choices":[{"message":{"content":"{\"name\":\"Alice\",\"age\":30}"}}]}`)
	err := validateOutputSchema(resp, []byte(`{"type":"object","required":["name"]}`))
	if err != nil {
		t.Fatalf("expected object response to pass, got error: %v", err)
	}
}

func TestValidateOutputSchemaMissingRequiredField(t *testing.T) {
	resp := []byte(`{"choices":[{"message":{"content":"{\"name\":\"Alice\"}"}}]}`)
	err := validateOutputSchema(resp, []byte(`{"type":"object","required":["name","age"]}`))
	if err == nil {
		t.Fatal("expected missing required field to fail")
	}
}

func TestValidateOutputSchemaCodeBlockJSON(t *testing.T) {
	resp := []byte("{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"name\\\":\\\"Alice\\\",\\\"age\\\":30}\\n```\"}}]}")
	schema := []byte(`{"type":"object","required":["name","age"]}`)
	if err := validateOutputSchema(resp, schema); err != nil {
		t.Fatalf("expected fenced JSON output to pass, got: %v", err)
	}
}

func TestValidateOutputSchemaWrongType(t *testing.T) {
	resp := []byte(`{"choices":[{"message":{"content":"\"just plain text\""}}]}`)
	err := validateOutputSchema(resp, []byte(`{"type":"object"}`))
	if err == nil {
		t.Fatal("expected non-JSON content to fail object schema")
	}
}

