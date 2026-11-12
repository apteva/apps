package testbrowser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testValue(value string) *string { return &value }

func TestSelectValuePresence(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		valid       bool
	}{
		{"empty", `,"value":""`, true},
		{"nonempty", `,"value":"bell"`, true},
		{"missing", "", false},
		{"null", `,"value":null`, false},
		{"number", `,"value":1`, false},
		{"boolean", `,"value":false`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var command Command
			err := json.Unmarshal([]byte(`{"action":"select","locator":{"role":"combobox","name":"Ringtone"}`+tc.field+`}`), &command)
			if err == nil {
				err = command.Validate()
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid {
				encoded, _ := json.Marshal(command)
				if !strings.Contains(string(encoded), `"value":`) {
					t.Fatalf("explicit value omitted: %s", encoded)
				}
			} else if tc.name == "missing" || tc.name == "null" {
				// Validation must reject before touching a browser or dispatching input.
				result, err := (&Browser{}).Do(context.Background(), command)
				if err == nil || result.Dispatched {
					t.Fatal("invalid selection dispatched", result, err)
				}
			}
		})
	}
}
