package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealDatabasePipelineTrace runs the trace end to end: real SDK client, real
// Database, relational permission filter, stored request log. The result of the
// query is the same whichever Database runs it; what differs is whether
// statistics could be attached. Set GRAPHQL_TEST_DATABASE_STATISTICS=reported
// for a Database that must report them, and =unavailable for one that refuses.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabasePipelineTrace -v
func TestRealDatabasePipelineTrace(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-pipeline-trace")
	relationalFixture(t, r)
	var rows map[string][]map[string]any
	if err := json.Unmarshal([]byte(relationalPolicyRows), &rows); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"calls", "sales", "offers", "centres"} {
		r.insert(name, rows[name]...)
	}
	const reader = "42"
	r.permissions[reader] = []string{"calls:read"}
	r.insert("profiles", map[string]any{"id": "p" + reader, "user_id": reader, "centre_id": "centre-a", "active": "1"})
	r.insert("grants", map[string]any{"id": "1", "user_id": reader, "enabled": "1", "offer_ids": []any{"offer-a"}, "centre_ids": []any{"centre-a"}})
	if failure := publishRelational(r, relationalPolicyPlan(t)); failure != "" {
		t.Fatal(failure)
	}

	request := func(variables map[string]any) (string, string, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": `query($first: Int, $after: String) { calls(first: $first, after: $after) { rows { id } total next } }`, "variables": variables})
		req, _ := http.NewRequest("POST", r.graph.URL()+"/public/graphql/default", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+pipelineToken(reader))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		for _, leak := range []string{"diagnostics", "filter_hash", "pipelines", "executionMs"} {
			if strings.Contains(string(raw), leak) || strings.Contains(resp.Header.Get("Server-Timing"), leak) {
				t.Errorf("client saw %s: %s", leak, raw)
			}
		}
		var out struct {
			Data struct {
				Calls struct {
					Rows []struct {
						ID any `json:"id"`
					} `json:"rows"`
					Total any `json:"total"`
					Next  any `json:"next"`
				} `json:"calls"`
			} `json:"data"`
			Errors []map[string]any `json:"errors"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || len(out.Errors) > 0 {
			t.Fatalf("query: %s (%v)", raw, err)
		}
		ids := []string{}
		for _, row := range out.Data.Calls.Rows {
			ids = append(ids, fmt.Sprint(row.ID))
		}
		next, _ := out.Data.Calls.Next.(string)
		// The filter is what it was before there was a trace.
		if total := fmt.Sprint(out.Data.Calls.Total); total != "6" {
			t.Errorf("total: %s", total)
		}
		// Logs are written asynchronously.
		requestID := resp.Header.Get("X-Request-ID")
		deadline := time.Now().Add(10 * time.Second)
		for {
			logs, _ := r.graph.MCP("graphql_logs", map[string]any{"limit": 50})["logs"].([]any)
			for _, raw := range logs {
				if entry, _ := raw.(map[string]any); entry["request_id"] == requestID {
					return strings.Join(ids, ","), next, entry
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("request %s was not logged: %v", requestID, logs)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	ids, next, entry := request(map[string]any{"first": 2})
	if ids != "12,10" || next == "" {
		t.Fatalf("first page: %s next %q", ids, next)
	}
	timings, _ := entry["timings"].(map[string]any)
	if _, ok := timings["serialise"]; !ok {
		t.Errorf("no serialise stage: %v", timings)
	}
	if _, ok := timings["write"]; !ok {
		t.Errorf("no write stage: %v", timings)
	}
	runtime, _ := entry["runtime"].(map[string]any)
	pipelines, _ := runtime["pipelines"].([]any)
	if len(pipelines) != 1 {
		t.Fatalf("pipelines: %v", runtime)
	}
	trace := pipelines[0].(map[string]any)
	dump, _ := json.MarshalIndent(trace, "", "  ")
	t.Logf("trace:\n%s", dump)
	if trace["field"] != "Query.calls" || trace["outcome"] != "ok" || trace["filter_hash"] == "" {
		t.Errorf("trace: %s", dump)
	}
	access := trace["access"].(map[string]any)
	if access["batches"].(float64) < 1 || access["reads"].(float64) < 1 || access["wire"].(map[string]any)["server_wait_ms"].(float64) <= 0 {
		t.Errorf("access: %v", access)
	}
	batch := trace["database_batch"].(map[string]any)
	wire := batch["wire"].(map[string]any)
	for _, stage := range []string{"server_wait_ms", "decode_ms", "response_bytes"} {
		if wire[stage].(float64) <= 0 {
			t.Errorf("%s was not measured through the real client: %v", stage, wire)
		}
	}
	operations := batch["operations"].([]any)
	if len(operations) != 2 {
		t.Fatalf("the page and one logical count: %v", operations)
	}
	page, total := operations[0].(map[string]any), operations[1].(map[string]any)
	if page["op"] != "find" || page["rows"] != float64(2) || page["has_more"] != true {
		t.Errorf("page: %v", page)
	}
	if total["op"] != "count" || total["count_of"] != "rows" || total["count"] != "6" {
		t.Errorf("count: %v", total)
	}
	if page["filter_hash"] == nil || page["filter_hash"] != total["filter_hash"] || page["filter_hash"] != trace["filter_hash"] {
		t.Errorf("the count must carry the page's filter: %v / %v / %v", page["filter_hash"], total["filter_hash"], trace["filter_hash"])
	}

	switch state, want := batch["statistics"], os.Getenv("GRAPHQL_TEST_DATABASE_STATISTICS"); {
	case want != "" && state != want:
		t.Fatalf("statistics %v, want %s: %s", state, want, dump)
	case state == pipelineStatisticsReported:
		statistics := total["statistics"].(map[string]any)
		if statistics["executed"] != true || total["execution_ms"].(float64) <= 0 || batch["database_reported_ms"].(float64) <= 0 {
			t.Errorf("statistics: %v", statistics)
		}
		if page["execution_ms"] != nil || page["statistics"] != nil {
			t.Errorf("a find reports nothing today: %v", page)
		}
		if unattributed := batch["unattributed_server_wait_ms"].(float64); unattributed < 0 || unattributed > wire["server_wait_ms"].(float64) {
			t.Errorf("unattributed: %v", batch)
		}
	case state == pipelineStatisticsUnavailable:
		if batch["retried_without_statistics"] != true || total["execution_ms"] != nil {
			t.Errorf("first request on a refusing Database: %v", batch)
		}
	default:
		t.Fatalf("statistics state: %v", state)
	}
	firstState := batch["statistics"]

	// The cursor still belongs to the resolved query, statistics or not.
	ids, _, entry = request(map[string]any{"first": 2, "after": next})
	if ids != "4,3" {
		t.Errorf("second page: %s", ids)
	}
	second := entry["runtime"].(map[string]any)["pipelines"].([]any)[0].(map[string]any)
	if second["filter_hash"] != trace["filter_hash"] {
		t.Errorf("the same permissions on the next page changed the filter: %v vs %v", second["filter_hash"], trace["filter_hash"])
	}
	batch = second["database_batch"].(map[string]any)
	if batch["statistics"] != firstState || batch["retried_without_statistics"] != nil {
		t.Errorf("second request: %v", batch)
	}

	// A permission change is visible as a different filter, and in the rows.
	r.operator("db_update", map[string]any{"collection": "grants", "where": map[string]any{"field": "user_id", "op": "eq", "value": reader},
		"set": map[string]any{"offer_ids": []any{"offer-unknown"}}, "maxAffected": 1})
	body, _ := json.Marshal(map[string]any{"query": `{ calls { rows { id } total } }`})
	req, _ := http.NewRequest("POST", r.graph.URL()+"/public/graphql/default", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pipelineToken(reader))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), `"total":6`) {
		t.Errorf("a removed grant kept its rows: %s", raw)
	}
}
