package main

import (
	"encoding/json"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"reflect"
	"testing"
)

func TestWireSchemaMatchesRunFields(t *testing.T) {
	s := schema(reflect.TypeFor[w.Run]())
	props := s["properties"].(map[string]any)
	for _, name := range []string{"id", "workspace_id", "thread_id", "input", "status", "demo"} {
		if _, ok := props[name]; !ok {
			t.Fatal("missing field", name)
		}
	}
	b, err := json.Marshal(s)
	if err != nil || !json.Valid(b) {
		t.Fatal("invalid schema", err)
	}
	if s["additionalProperties"] != false {
		t.Fatal("wire schema permits unknown fields")
	}
}
