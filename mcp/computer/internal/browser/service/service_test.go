package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestFastModeKeepsOneServiceActionAndNoProtocolField(t *testing.T) {
	var (
		mu      sync.Mutex
		actions []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/screenshot" {
			_, _ = w.Write([]byte("png"))
			return
		}
		var action map[string]any
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		actions = append(actions, action)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(server.URL, computer.DisplaySize{Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Execute(computer.Action{
		Type: "type",
		Text: "abc",
		Presentation: computer.PresentationOptions{
			Mode: "fast",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0]["text"] != "abc" {
		t.Fatalf("fast service actions: %#v", actions)
	}
	if _, leaked := actions[0]["presentation"]; leaked {
		t.Fatalf("presentation metadata changed service protocol: %#v", actions[0])
	}
}

func TestDemoModeSplitsServiceTyping(t *testing.T) {
	var typed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/screenshot" {
			_, _ = w.Write([]byte("png"))
			return
		}
		var action map[string]any
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			t.Fatal(err)
		}
		typed = append(typed, action["text"].(string))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(server.URL, computer.DisplaySize{Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Execute(computer.Action{
		Type: "type",
		Text: "a🙂b",
		Presentation: computer.PresentationOptions{
			Mode:              "demo",
			TypingDelayMS:     1,
			PostActionDelayMS: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := len(typed), 3; got != want {
		t.Fatalf("demo service action count: got %d want %d (%#v)", got, want, typed)
	}
	if typed[0] != "a" || typed[1] != "🙂" || typed[2] != "b" {
		t.Fatalf("demo service typing: %#v", typed)
	}
}
