package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestToolVisualsDelegatedReadPermission(t *testing.T) {
	a, _, _ := newTestEnv(t)
	for _, tc := range []struct {
		name    string
		actions []string
		status  int
	}{
		{"explicit permission", []string{"tool_visuals.read"}, http.StatusOK},
		{"chat permission only", []string{"chat.read"}, http.StatusForbidden},
		{"message permission only", []string{"message.read"}, http.StatusForbidden},
		{"no permission", nil, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise the registered route, including its delegated middleware.
			rec := visitorRequest(a, "visuals-visitor", "GET", "/tool-visuals", nil, tc.actions)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.status == http.StatusOK {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if _, ok := body["integrations"]; !ok {
					t.Fatalf("missing visual metadata response: %s", rec.Body)
				}
			}
		})
	}
	// Reading visual metadata must not grant access to conversation content.
	if rec := visitorRequest(a, "visuals-visitor", "GET", "/chats", nil, []string{"tool_visuals.read"}); rec.Code != http.StatusForbidden {
		t.Fatalf("visual metadata permission granted chat access: %d %s", rec.Code, rec.Body)
	}
}
