package main

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSessionDetailInputSchemaRequiresOnlySessionID(t *testing.T) {
	schema, err := jsonschema.For[SessionDetailArgs](nil)
	if err != nil {
		t.Fatalf("jsonschema.For[SessionDetailArgs]() error = %v", err)
	}
	if len(schema.Properties) != 1 {
		t.Fatalf("session_detail should expose only session_id, got %+v", schema.Properties)
	}
	if _, ok := schema.Properties["session_id"]; !ok {
		t.Fatalf("session_id property missing from schema: %+v", schema.Properties)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "session_id" {
		t.Fatalf("session_id should be the only required field: %+v", schema.Required)
	}
}
