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
	expectedProps := []string{"session_id", "max_items", "cursor", "click_more_replies", "reply_limit", "scroll_speed"}
	for _, prop := range expectedProps {
		if _, ok := schema.Properties[prop]; !ok {
			t.Fatalf("property %q missing from schema: %+v", prop, schema.Properties)
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "session_id" {
		t.Fatalf("session_id should be the only required field: %+v", schema.Required)
	}
}

func TestListFeedsSchemaIncludesCursorAndMaxItems(t *testing.T) {
	schema, err := jsonschema.For[ListFeedsArgs](nil)
	if err != nil {
		t.Fatalf("jsonschema.For[ListFeedsArgs]() error = %v", err)
	}
	if _, ok := schema.Properties["session_id"]; !ok {
		t.Fatal("session_id property missing from list_feeds schema")
	}
	if _, ok := schema.Properties["cursor"]; !ok {
		t.Fatal("cursor property missing from list_feeds schema")
	}
	if _, ok := schema.Properties["max_items"]; !ok {
		t.Fatal("max_items property missing from list_feeds schema")
	}
}

func TestSessionSearchSchemaIncludesCursorAndMaxItems(t *testing.T) {
	schema, err := jsonschema.For[SessionSearchArgs](nil)
	if err != nil {
		t.Fatalf("jsonschema.For[SessionSearchArgs]() error = %v", err)
	}
	if _, ok := schema.Properties["cursor"]; !ok {
		t.Fatal("cursor property missing from session_search schema")
	}
	if _, ok := schema.Properties["max_items"]; !ok {
		t.Fatal("max_items property missing from session_search schema")
	}
}
