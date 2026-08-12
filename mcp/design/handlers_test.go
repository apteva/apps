package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestHTTPDesignLifecycleUsesAPIPrefix(t *testing.T) {
	store := testStore(t)
	app := &App{store: store, engine: &Engine{}, artifactRoot: t.TempDir(), maxOperations: 256}
	manifest := app.Manifest()
	app.ctx = sdk.NewAppCtxForTest(&manifest, store.db, sdk.Config{}, nil, nil).WithProject("project-a")

	body, _ := json.Marshal(map[string]any{
		"name": "HTTP plate", "definition": json.RawMessage(testDefinition()), "parameters": map[string]any{"width": 45},
	})
	createRequest := httptest.NewRequest(http.MethodPost, "/api/designs", bytes.NewReader(body))
	createResponse := httptest.NewRecorder()
	app.handleDesigns(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", createResponse.Code, createResponse.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/designs?status=all", nil)
	listResponse := httptest.NewRecorder()
	app.handleDesigns(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var output struct {
		Designs []Design `json:"designs"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Designs) != 1 || output.Designs[0].Name != "HTTP plate" {
		t.Fatalf("unexpected designs: %#v", output.Designs)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/designs/"+jsonNumber(output.Designs[0].ID), nil)
	getResponse := httptest.NewRecorder()
	app.handleDesign(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status %d: %s", getResponse.Code, getResponse.Body.String())
	}
}

func jsonNumber(value int64) string {
	body, _ := json.Marshal(value)
	return string(body)
}
