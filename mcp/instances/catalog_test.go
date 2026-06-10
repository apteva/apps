package main

import (
	"encoding/json"
	"testing"
)

func TestActiveServerTypes_FiltersDeprecated(t *testing.T) {
	types, err := parseHetznerServerTypes(json.RawMessage(`{
		"server_types": [
			{"name":"old","cores":2,"memory":4,"disk":40,"deprecated":true,"prices":[]},
			{"name":"current","cores":4,"memory":8,"disk":80,"deprecated":false,"prices":[]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	active := activeServerTypes(types)
	if len(active) != 1 || active[0].Name != "current" {
		t.Fatalf("active server types = %#v, want only current", active)
	}
}
