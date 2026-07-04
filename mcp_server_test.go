package main

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSessionDetailInputSchemaKeepsPagesOptional(t *testing.T) {
	schema, err := jsonschema.For[SessionDetailArgs](nil)
	if err != nil {
		t.Fatalf("jsonschema.For[SessionDetailArgs]() error = %v", err)
	}
	if _, ok := schema.Properties["pages"]; !ok {
		t.Fatalf("pages property missing from schema: %+v", schema.Properties)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal schema JSON: %v", err)
	}
	properties, ok := decoded["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing from JSON: %s", encoded)
	}
	pages, ok := properties["pages"].(map[string]any)
	if !ok {
		t.Fatalf("pages property missing from JSON: %s", encoded)
	}
	if pages["type"] != "integer" {
		t.Fatalf("pages should be integer schema, got %+v", pages)
	}
	for _, required := range schema.Required {
		if required == "pages" {
			t.Fatalf("pages should not be required: %+v", schema.Required)
		}
	}
	foundSessionID := false
	for _, required := range schema.Required {
		if required == "session_id" {
			foundSessionID = true
			break
		}
	}
	if !foundSessionID {
		t.Fatalf("session_id should remain required: %+v", schema.Required)
	}
}
