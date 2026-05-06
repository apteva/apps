//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Tier 2: real binary, real HTTP. SpawnSidecar compiles and boots the
// app, hands back a *Sidecar with helpers for /mcp + REST calls, and
// tears it down at t.Cleanup.

func TestSidecar_HealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

func TestSidecar_RoundtripCreateInsertSearch(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	out := sc.MCP("tables_create", map[string]any{
		"name": "books",
		"columns": []any{
			map[string]any{"name": "title", "type": "text", "nullable": false},
			map[string]any{"name": "author", "type": "text"},
			map[string]any{"name": "rating", "type": "number"},
			map[string]any{"name": "finished", "type": "bool"},
		},
	})
	if out["name"] != "books" {
		t.Fatalf("create returned %+v", out)
	}

	out = sc.MCP("rows_insert", map[string]any{
		"table": "books",
		"rows": []any{
			map[string]any{"title": "A", "author": "X", "rating": 5.0, "finished": true},
			map[string]any{"title": "B", "author": "X", "rating": 4.0, "finished": false},
			map[string]any{"title": "C", "author": "Y", "rating": 3.0, "finished": true},
		},
	})
	if int(out["inserted"].(float64)) != 3 {
		t.Fatalf("inserted=%v", out["inserted"])
	}

	out = sc.MCP("rows_search", map[string]any{
		"table": "books",
		"where": []any{map[string]any{"col": "finished", "op": "eq", "value": true}},
	})
	if int(out["total"].(float64)) != 2 {
		t.Errorf("finished=true total=%v", out["total"])
	}

	out = sc.MCP("tables_query", map[string]any{
		"sql": "SELECT author, COUNT(*) AS n FROM {books} GROUP BY author ORDER BY n DESC",
	})
	rows := out["rows"].([]any)
	if len(rows) != 2 {
		t.Errorf("expected 2 grouped rows, got %d", len(rows))
	}
}

func TestSidecar_RESTSurface(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// POST /tables — create
	createBody := map[string]any{
		"name": "notes",
		"columns": []any{
			map[string]any{"name": "body", "type": "text", "nullable": false},
		},
	}
	var created map[string]any
	if resp := sc.POST("/tables", createBody, &created); resp.Status != 200 {
		t.Fatalf("POST /tables: %d", resp.Status)
	}

	// POST /tables/notes/rows — insert
	var inserted map[string]any
	insertBody := map[string]any{"row": map[string]any{"body": "hello rest"}}
	if resp := sc.POST("/tables/notes/rows", insertBody, &inserted); resp.Status != 200 {
		t.Fatalf("POST rows: %d", resp.Status)
	}
	ids := inserted["ids"].([]any)
	rowID := int64(ids[0].(float64))

	// GET /tables/notes — describe
	var desc map[string]any
	sc.GET("/tables/notes", &desc)
	if desc["name"] != "notes" {
		t.Errorf("describe: %+v", desc)
	}

	// GET /tables/notes/rows — list
	var listed map[string]any
	sc.GET("/tables/notes/rows", &listed)
	if int(listed["total"].(float64)) != 1 {
		t.Errorf("list total=%v", listed["total"])
	}

	// PATCH /tables/notes/rows/{id}
	var patched map[string]any
	patch := map[string]any{"body": "updated"}
	if resp := sc.PATCH("/tables/notes/rows/"+strconv.FormatInt(rowID, 10), patch, &patched); resp.Status != 200 {
		t.Fatalf("PATCH: %d", resp.Status)
	}
	if patched["row"].(map[string]any)["body"] != "updated" {
		t.Errorf("patched body=%v", patched["row"])
	}

	// DELETE /tables/notes/rows/{id}
	if resp := sc.DELETE("/tables/notes/rows/" + strconv.FormatInt(rowID, 10)); resp.Status != 200 {
		t.Fatalf("DELETE row: %d", resp.Status)
	}

	// DELETE /tables/notes (drop) without confirm — should refuse
	if resp := sc.DELETE("/tables/notes"); resp.Status == 200 {
		t.Errorf("drop without confirm should be refused, got %d", resp.Status)
	}

	// DELETE /tables/notes?confirm=true — succeeds
	req, _ := http.NewRequest("DELETE", sc.URL()+"/tables/notes?confirm=true", nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("drop with confirm: %d", resp.StatusCode)
	}
}

func TestSidecar_AlterTableViaPATCH(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	sc.MCP("tables_create", map[string]any{
		"name": "books",
		"columns": []any{
			map[string]any{"name": "title", "type": "text", "nullable": false},
		},
	})

	// add — PATCH with {add: {...}}
	var out map[string]any
	if resp := sc.PATCH("/tables/books", map[string]any{
		"add": map[string]any{"name": "isbn", "type": "text"},
	}, &out); resp.Status != 200 {
		t.Fatalf("PATCH add: %d", resp.Status)
	}
	cols := out["columns"].([]any)
	if len(cols) != 2 {
		t.Errorf("after add, expected 2 cols, got %d", len(cols))
	}

	// rename — PATCH with {rename: {from, to}}
	if resp := sc.PATCH("/tables/books", map[string]any{
		"rename": map[string]any{"from": "isbn", "to": "isbn13"},
	}, &out); resp.Status != 200 {
		t.Fatalf("PATCH rename: %d", resp.Status)
	}

	// describe to confirm
	var desc map[string]any
	sc.GET("/tables/books", &desc)
	descCols := desc["columns"].([]any)
	hasIsbn13 := false
	for _, c := range descCols {
		if c.(map[string]any)["name"] == "isbn13" {
			hasIsbn13 = true
		}
	}
	if !hasIsbn13 {
		t.Errorf("rename did not stick: %+v", descCols)
	}

	// drop — PATCH with {drop: "col"}
	if resp := sc.PATCH("/tables/books", map[string]any{"drop": "isbn13"}, &out); resp.Status != 200 {
		t.Fatalf("PATCH drop: %d", resp.Status)
	}
	sc.GET("/tables/books", &desc)
	if len(desc["columns"].([]any)) != 1 {
		t.Errorf("drop did not stick: %+v", desc["columns"])
	}

	// Bad PATCH bodies should return 400, not 500
	if resp := sc.PATCH("/tables/books", map[string]any{}, &out); resp.Status == 200 {
		t.Errorf("empty PATCH body should 4xx, got %d", resp.Status)
	}
	if resp := sc.PATCH("/tables/books", map[string]any{
		"add":    map[string]any{"name": "x", "type": "text"},
		"rename": map[string]any{"from": "title", "to": "tname"},
	}, &out); resp.Status == 200 {
		t.Errorf("PATCH with two ops should 4xx, got %d", resp.Status)
	}
}

func TestSidecar_QueryViaRESTRefusesWrites(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	sc.MCP("tables_create", map[string]any{
		"name": "x", "columns": []any{map[string]any{"name": "v", "type": "number"}},
	})
	body, _ := json.Marshal(map[string]any{"sql": "DELETE FROM {x}"})
	req, _ := http.NewRequest("POST", sc.URL()+"/tables/x/query", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatalf("DELETE-disguised-as-query should be refused, got %d", resp.StatusCode)
	}
	bodyBytes := make([]byte, 256)
	n, _ := resp.Body.Read(bodyBytes)
	if !strings.Contains(string(bodyBytes[:n]), "SELECT") {
		t.Errorf("error body should mention SELECT, got %q", string(bodyBytes[:n]))
	}
}
