package browserengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestCreateSessionDefersInitialNavigationWhenEnvironmentRequested(t *testing.T) {
	var got sessionCreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"session-1","connect_url":"ws://example.invalid/devtools/browser/1"}`))
	}))
	defer server.Close()

	c, err := NewWithOptions("key", computer.DisplaySize{Width: 390, Height: 844}, Options{BaseURL: server.URL, UserAgent: "Native-UA"})
	if err != nil {
		t.Fatal(err)
	}
	mobile := true
	if _, err := c.createSession(computer.OpenOptions{
		URL:         "https://example.com/first",
		Environment: computer.EnvironmentOptions{UserAgent: "Native-UA", Mobile: &mobile},
	}); err != nil {
		t.Fatal(err)
	}
	if got.InitialURL != "about:blank" {
		t.Fatalf("initial_url=%q; environment must be applied before explicit navigation", got.InitialURL)
	}
	if got.UserAgent != "Native-UA" {
		t.Fatalf("native user_agent=%q", got.UserAgent)
	}
}
