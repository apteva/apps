package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestExamplesEndpoint: GET /examples returns the embedded handler
// files for both runtimes, with parsed descriptions. The panel's
// "Load" picker fails silently if this regresses, so guard it here.
func TestExamplesEndpoint(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/examples", nil)
	w := httptest.NewRecorder()
	app.handleHTTPExamples(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Examples []struct {
			Name        string `json:"name"`
			Runtime     string `json:"runtime"`
			Source      string `json:"source"`
			Description string `json:"description"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var foundNodeHello, foundGoHello, foundTablesInsert bool
	for _, e := range resp.Examples {
		if e.Source == "" {
			t.Errorf("example %q (%s) has empty source", e.Name, e.Runtime)
		}
		if e.Name == "hello" && e.Runtime == "node" {
			foundNodeHello = true
		}
		if e.Name == "hello" && e.Runtime == "go" {
			foundGoHello = true
		}
		if e.Name == "tables-insert" {
			foundTablesInsert = true
			if e.Description == "" {
				t.Errorf("tables-insert has no parsed description")
			}
		}
	}
	if !foundNodeHello || !foundGoHello {
		t.Errorf("expected hello in both runtimes; got node=%v go=%v", foundNodeHello, foundGoHello)
	}
	if !foundTablesInsert {
		t.Error("tables-insert example missing from /examples response")
	}
}

// TestExamplesEndpointRuntimeFilter: ?runtime=go returns only go.
func TestExamplesEndpointRuntimeFilter(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/examples?runtime=go", nil)
	w := httptest.NewRecorder()
	app.handleHTTPExamples(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Examples []struct {
			Runtime string `json:"runtime"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Examples) == 0 {
		t.Fatal("filtered response is empty")
	}
	for _, e := range resp.Examples {
		if e.Runtime != "go" {
			t.Errorf("filter leaked a %q example", e.Runtime)
		}
	}
}
