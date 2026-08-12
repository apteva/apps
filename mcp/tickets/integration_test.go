//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecarTicketFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-project"))
	var health map[string]any
	if response := sc.GET("/health", &health); response.Status != 200 || health["ok"] != true {
		t.Fatalf("health=%d %#v", response.Status, health)
	}
	created := sc.MCP("tickets_create", map[string]any{"title": "Review the backend", "area": "backend", "type": "feedback"})
	ticket := created["ticket"].(map[string]any)
	id := int64(ticket["id"].(float64))
	sc.MCP("tickets_comment", map[string]any{"id": id, "body": "Acknowledged."})
	detail := sc.MCP("tickets_get", map[string]any{"id": id})
	if len(detail["comments"].([]any)) != 1 {
		t.Fatalf("detail=%#v", detail)
	}
}

func TestPublicPortalWorksWithoutAppTokenAndHidesInternalNotes(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("public-project"))
	portalOut := sc.MCP("tickets_portal_get", map[string]any{})
	portal := portalOut["portal"].(map[string]any)
	intakeURL, err := url.Parse(portal["intake_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	portalToken := intakeURL.Path[strings.LastIndex(intakeURL.Path, "/")+1:]

	response, err := http.Get(sc.URL() + "/p/" + portalToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Client feedback")) {
		t.Fatalf("intake status=%d body=%s", response.StatusCode, body)
	}

	payload, _ := json.Marshal(map[string]any{"title": "Public ticket", "description": "Submitted without an app token", "requester_email": "client@example.com", "area": "frontend"})
	response, err = http.Post(sc.URL()+"/p/"+portalToken+"/tickets", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%#v", response.StatusCode, created)
	}
	ticket := created["ticket"].(map[string]any)
	id := int64(ticket["id"].(float64))
	sc.MCP("tickets_add_internal_note", map[string]any{"id": id, "body": "Secret implementation detail"})
	ticketURL, _ := url.Parse(ticket["portal_url"].(string))
	ticketToken := ticketURL.Path[strings.LastIndex(ticketURL.Path, "/")+1:]
	response, err = http.Get(sc.URL() + "/p/ticket/" + ticketToken + "?format=json")
	if err != nil {
		t.Fatal(err)
	}
	var publicDetail map[string]any
	if err := json.NewDecoder(response.Body).Decode(&publicDetail); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public detail status=%d", response.StatusCode)
	}
	if comments := publicDetail["comments"].([]any); len(comments) != 0 {
		t.Fatalf("internal note leaked publicly: %#v", comments)
	}
}
