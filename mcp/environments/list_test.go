package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apteva/app-sdk/testkit"
)

func intPointer(n int) *int { return &n }

// Insert without constructing or decoding 1,000 separate spec trees in Go.
func seedListDefinitions(t testing.TB, db *sql.DB, count int, spec, name string) {
	t.Helper()
	_, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM seq WHERE n+1<?)
 INSERT INTO environment_definitions(id,name,desired_state,spec_version,spec_json,created_at,updated_at)
 SELECT printf('env-%04d',n),?,'stopped',1,?,'2026-11-14T08:00:00Z','2026-11-14T08:00:00Z' FROM seq`, count, name, spec)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionListPaginationAndValidation(t *testing.T) {
	s := testStore(t)
	seedListDefinitions(t, s.db, 217, `{"version":1}`, "Example")
	for _, test := range []struct {
		limit *int
		count int
	}{{nil, 50}, {intPointer(200), 200}, {intPointer(1), 1}} {
		page, err := s.listDefinitionSummaries(ListOptions{Limit: test.limit})
		if err != nil || len(page.Items) != test.count || !page.HasMore || page.NextCursor == nil {
			t.Fatalf("page=%+v err=%v", page, err)
		}
	}
	for _, limit := range []int{-1, 0, 201} {
		if _, err := s.listDefinitionSummaries(ListOptions{Limit: &limit}); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	for _, cursor := range []string{"", "!", strings.Repeat("a", 1025), base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"created":"2026-11-14T08:00:00Z","id":"env"}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"created":"bad","id":"env"}`))} {
		if _, err := s.listDefinitionSummaries(ListOptions{Cursor: &cursor}); err == nil {
			t.Fatalf("accepted malformed cursor")
		}
	}
	seen := map[string]bool{}
	options := ListOptions{Limit: intPointer(17)}
	for {
		page, err := s.listDefinitionSummaries(options)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate %s", item.ID)
			}
			seen[item.ID] = true
		}
		// Lifecycle updates change updated_at, but must never change traversal order.
		if _, err := s.db.Exec(`UPDATE environment_definitions SET desired_state='running',updated_at='2026-11-14T09:00:00Z'`); err != nil {
			t.Fatal(err)
		}
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Fatal("terminal cursor")
			}
			break
		}
		options.Cursor = page.NextCursor
	}
	if len(seen) != 217 {
		t.Fatalf("visited %d of 217", len(seen))
	}
	// A cursor remains usable after deletion of its anchor. Newer creations do
	// not enter an in-progress traversal; a fresh traversal sees them.
	first, err := s.listDefinitionSummaries(ListOptions{Limit: intPointer(1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM environment_definitions WHERE id=?`, first.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO environment_definitions(id,name,spec_json,created_at,updated_at) VALUES('new','New','{}','2026-11-14T10:00:00Z','2026-11-14T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	next, err := s.listDefinitionSummaries(ListOptions{Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range next.Items {
		if item.ID == "new" {
			t.Fatal("new creation moved into traversal")
		}
	}
}

func TestDefinitionListDoesNotReadPayloadColumns(t *testing.T) {
	s := testStore(t)
	seedListDefinitions(t, s.db, 3, "invalid JSON decoder sentinel", "Example")
	// Removing the column proves even a discarded spec_json projection fails.
	if _, err := s.db.Exec(`ALTER TABLE environment_definitions RENAME COLUMN spec_json TO forbidden_spec_payload`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE environment_web_fixtures RENAME TO forbidden_web_payloads; ALTER TABLE environment_protocol_fixtures RENAME TO forbidden_protocol_payloads`); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run", EnvironmentID: "env-0001", RuntimeID: "rt", Status: "running", StartedAt: time.Now().UTC()}
	if err := s.createRun(run); err != nil {
		t.Fatal(err)
	}
	// No AppCtx or RuntimeAPI: summary listing must make zero platform calls.
	svc := &service{db: s}
	page, err := svc.listDefinitions(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[1].ActiveRun == nil || page.Items[1].ActiveRun.ID != "run" || page.Items[0].ActiveRun != nil {
		t.Fatalf("page=%+v", page)
	}
	raw, _ := json.Marshal(page)
	for _, field := range []string{`"spec"`, `"error"`, `"runtime"`, `"web_fixtures"`, `"protocol_fixtures"`} {
		if bytes.Contains(raw, []byte(field)) {
			t.Fatalf("payload leaked %s", field)
		}
	}
}

func TestDefinitionListByteBudgetAndOversizedLegacyFields(t *testing.T) {
	s := testStore(t)
	// Control characters and HTML exercise JSON escaping and MCP's text wrapper.
	seedListDefinitions(t, s.db, 60, `{"version":1}`, strings.Repeat("\x00<\"", 1000))
	seen := map[string]bool{}
	options := ListOptions{Limit: intPointer(200)}
	for {
		page, err := s.listDefinitionSummaries(options)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(page)
		envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw)}}}})
		if len(envelope) > maxListResponseBytes || len(raw)+len(envelope) > maxListResponseBytes {
			t.Fatalf("wire bytes=%d direct=%d", len(envelope), len(raw))
		}
		if len(page.Items) == 0 || len(page.Items) >= 60 {
			t.Fatalf("budget did not bound page: %d", len(page.Items))
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate after byte cut")
			}
			seen[item.ID] = true
		}
		if !page.HasMore {
			break
		}
		options.Cursor = page.NextCursor
	}
	if len(seen) != 60 {
		t.Fatalf("visited %d", len(seen))
	}
	if _, err := s.db.Exec(`UPDATE environment_definitions SET name=? WHERE id='env-0059'`, strings.Repeat("x", 2<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.listDefinitionSummaries(ListOptions{}); err == nil || !strings.Contains(err.Error(), "summary field") {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestDefinitionListLargeSpecsBoundedAllocationsAndInterfaces(t *testing.T) {
	s := testStore(t)
	raw, err := rawSpec(largeReconcileSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	seedListDefinitions(t, s.db, 1000, string(raw), "Large fixture")
	// Warm SQLite and JSON metadata before measuring steady-state request cost.
	svc := &service{db: s}
	app := &App{svc: svc}
	if _, err := svc.listDefinitions(ListOptions{}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.getDefinition("env-0000")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := rawSpec(detail.Spec)
	if err != nil || raw != restored {
		t.Fatal("detail specification changed")
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	const requests = 12
	maxBytes := 0
	for i := 0; i < requests; i++ {
		var raw []byte
		if i%2 == 0 {
			value, err := app.toolList(nil, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			w := httptest.NewRecorder()
			app.handleEnvironments(w, httptest.NewRequest("GET", "/api/environments", nil))
			if w.Code != 200 {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body)
			}
			raw = w.Body.Bytes()
		}
		if len(raw) > maxBytes {
			maxBytes = len(raw)
		}
		if bytes.Contains(raw, []byte(`"spec"`)) {
			t.Fatal("spec leaked into list")
		}
	}
	runtime.ReadMemStats(&after)
	allocated := (after.TotalAlloc - before.TotalAlloc) / requests
	t.Logf("1000 specs: stored=%d bytes; default page response<=%d bytes; allocated/request=%d bytes", len(raw)*1000, maxBytes, allocated)
	if allocated > 2<<20 {
		t.Fatalf("listing allocated %d bytes/request for page of 50", allocated)
	}
	// Independent stores model the existing per-install isolation boundary.
	other := testStore(t)
	seedListDefinitions(t, other.db, 1, `{}`, "Other install")
	page, err := other.listDefinitionSummaries(ListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "Other install" {
		t.Fatalf("isolation page=%+v err=%v", page, err)
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=1.5", "limit=1&limit=2", "cursor=", "cursor=!"} {
		w := httptest.NewRecorder()
		app.handleEnvironments(w, httptest.NewRequest("GET", "/api/environments?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("%s: HTTP %d", query, w.Code)
		}
	}
	for _, args := range []map[string]any{{"limit": 0}, {"limit": nil}, {"limit": 1.5}, {"cursor": "!"}, {"limit": "50"}, {"unknown": 1}} {
		if _, err := app.toolList(nil, args); err == nil {
			t.Fatalf("accepted arguments %+v", args)
		}
	}
}

func BenchmarkDefinitionListLargeSpecifications(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := testStore(b)
			raw, err := rawSpec(largeReconcileSpec(b))
			if err != nil {
				b.Fatal(err)
			}
			seedListDefinitions(b, s.db, count, string(raw), "Large fixture")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.listDefinitionSummaries(ListOptions{Limit: intPointer(10)}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Exercise the real pinned SDK transport and concurrent callers against a
// sidecar with a heap target smaller than the stored specification payloads.
func TestDefinitionListSidecarLargeSpecifications(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a sidecar for transport/concurrency coverage")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer gateway.Close()
	path := filepath.Join(t.TempDir(), "app.db")
	appDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sc := testkit.SpawnSidecar(t, appDir, testkit.WithEnv("DB_PATH", path), testkit.WithEnv("GOMEMLIMIT", "32MiB"), testkit.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := rawSpec(largeReconcileSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	seedListDefinitions(t, db, 1000, string(raw), "Large fixture")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 15 * time.Second}
			for i := 0; i < 8; i++ {
				method, path, body := "GET", "/api/environments", io.Reader(nil)
				if i%2 == 0 {
					method, path, body = "POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"environment_list","arguments":{}}}`)
				}
				req, _ := http.NewRequest(method, sc.URL()+path, body)
				req.Header.Set("Authorization", "Bearer "+sc.Token())
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					errs <- err
					return
				}
				response, err := io.ReadAll(io.LimitReader(resp.Body, maxListResponseBytes+1))
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || len(response) > maxListResponseBytes {
					errs <- fmt.Errorf("transport status=%d bytes=%d err=%v", resp.StatusCode, len(response), err)
					return
				}
				if method == "POST" {
					var envelope struct {
						Result struct {
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"result"`
					}
					if err := json.Unmarshal(response, &envelope); err != nil || len(envelope.Result.Content) != 1 {
						errs <- fmt.Errorf("invalid MCP response: %s", response)
						return
					}
					response = []byte(envelope.Result.Content[0].Text)
				}
				var page EnvironmentPage
				if err := json.Unmarshal(response, &page); err != nil || len(page.Items) != 50 || !page.HasMore {
					errs <- fmt.Errorf("invalid page: items=%d err=%v", len(page.Items), err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	response, err := http.Get(sc.URL() + "/api/environments")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated list status=%d", response.StatusCode)
	}
	if _, err := db.Exec(`UPDATE environment_definitions SET name=?`, strings.Repeat("\x00<\"", 1000)); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", sc.URL()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"environment_list","arguments":{"limit":200}}}`))
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	req.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := io.ReadAll(io.LimitReader(response.Body, maxListResponseBytes+1))
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(wire) > maxListResponseBytes {
		t.Fatalf("escaped MCP status=%d bytes=%d err=%v", response.StatusCode, len(wire), err)
	}
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wire, &envelope); err != nil || len(envelope.Result.Content) != 1 {
		t.Fatalf("invalid escaped MCP response: %s", wire)
	}
	var budgetPage EnvironmentPage
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &budgetPage); err != nil || len(budgetPage.Items) == 0 || len(budgetPage.Items) >= 200 || !budgetPage.HasMore || budgetPage.NextCursor == nil {
		t.Fatalf("budget page=%+v err=%v", budgetPage, err)
	}
	t.Logf("real SDK escaped MCP response=%d bytes (ceiling %d), items=%d", len(wire), maxListResponseBytes, len(budgetPage.Items))

	t.Logf("64 requests / 8 concurrent callers; %d stored spec bytes; sidecar GOMEMLIMIT=32MiB", len(raw)*1000)
}
