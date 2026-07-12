//go:build integration

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecarFetchRequestEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"integration"}`))
	}))
	defer upstream.Close()

	sidecar := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("integration-project"),
		tk.WithConfig(map[string]string{"allow_loopback": "true"}),
	)
	result := sidecar.MCP("fetch_request", map[string]any{
		"method": "GET",
		"url":    upstream.URL,
	})
	if result["status"] != float64(http.StatusOK) {
		t.Fatalf("fetch_request result: %#v", result)
	}
	if result["history_id"] == nil {
		t.Fatalf("history id missing: %#v", result)
	}
}
