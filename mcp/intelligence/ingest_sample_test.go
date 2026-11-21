package main

// Reproducible ingestion sample.
//
// The real connectors (prediction markets, GDELT news, BLS) run against
// upstream responses recorded once from the public APIs and stored under
// testdata/ingest-sample/. Replaying them with the recorded clock yields the
// same evidence, questions, prices and forecasts on every machine, and the
// test compares a digest of that output with manifest.json.
//
//	GOWORK=off go test -run TestIngestionSample -v            # replay
//
//	INTEL_SAMPLE_RECORD=1 APTEVA_GATEWAY_URL=http://localhost:5280 \
//	APTEVA_APP_TOKEN=<an install token with connections.execute> \
//	GOWORK=off go test -run TestIngestionSample -v            # re-record
//
// Recording runs each call the way production does: through the
// platform's keyless integration route and the catalog tool, so it
// needs a running apteva-server. It stores successful responses only. A
// failed upstream call (e.g. a GDELT 429) is replayed as a failure,
// which is itself part of the sample.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleDir = "testdata/ingest-sample"

type sampleCall struct {
	Slug  string         `json:"slug"`
	Tool  string         `json:"tool"`
	Args  map[string]any `json:"args"`
	File  string         `json:"file,omitempty"`
	OK    bool           `json:"ok"`
	Error string         `json:"error,omitempty"`
}

// recordingClient records public responses (record mode) or serves them
// back by (slug, tool, args) key (replay mode). No integration is bound.
type recordingClient struct {
	record bool
	calls  []sampleCall
}

func sampleKey(slug, tool string, args map[string]any) string {
	b, _ := json.Marshal(args) // map keys marshal sorted: stable
	h := sha256.Sum256([]byte(slug + "\x00" + tool + "\x00" + string(b)))
	return slug + "__" + tool + "__" + hex.EncodeToString(h[:6]) + ".json.gz"
}

func (c *recordingClient) call(slug, tool string, args map[string]any) (json.RawMessage, bool) {
	file := sampleKey(slug, tool, args)
	path := filepath.Join(sampleDir, "responses", file)
	if c.record {
		raw, reason := recordPublicCall(slug, tool, args)
		ok := reason == ""
		call := sampleCall{Slug: slug, Tool: tool, Args: args, File: map[bool]string{true: file}[ok], OK: ok, Error: reason}
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if !ok {
			_ = os.WriteFile(strings.TrimSuffix(path, ".json.gz")+".error", []byte(reason), 0o644)
			setSourceError(slug, reason)
		} else {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(raw)
			_ = zw.Close()
			_ = os.WriteFile(path, buf.Bytes(), 0o644)
			setSourceError(slug, "")
		}
		c.calls = append(c.calls, call)
		return raw, ok
	}
	f, err := os.Open(path)
	if err != nil {
		// A recorded upstream failure replays as the same failure.
		reason := "not recorded"
		if b, err := os.ReadFile(strings.TrimSuffix(path, ".json.gz") + ".error"); err == nil {
			reason = string(b)
		}
		setSourceError(slug, reason)
		c.calls = append(c.calls, sampleCall{Slug: slug, Tool: tool, Args: args, Error: reason})
		return nil, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, false
	}
	setSourceError(slug, "")
	c.calls = append(c.calls, sampleCall{Slug: slug, Tool: tool, Args: args, File: file, OK: true})
	return json.RawMessage(raw), true
}

// recordPublicCall runs one call through a live platform's keyless
// integration route, exactly as platformSourceClient does in production.
func recordPublicCall(slug, tool string, args map[string]any) (json.RawMessage, string) {
	base, token := os.Getenv("APTEVA_GATEWAY_URL"), os.Getenv("APTEVA_APP_TOKEN")
	if base == "" || token == "" {
		return nil, "recording needs APTEVA_GATEWAY_URL and APTEVA_APP_TOKEN"
	}
	ctool, cargs := catalogCall(slug, tool, args)
	body, _ := json.Marshal(map[string]any{"tool": ctool, "input": cargs})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/api/apps/callback/integrations/public/"+url.PathEscape(slug)+"/execute", bytes.NewReader(body))
	if err != nil {
		return nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("platform HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 160))
	}
	var res struct {
		Success bool            `json:"success"`
		Status  int             `json:"status"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err.Error()
	}
	if !res.Success || res.Status >= 400 {
		return nil, failureReason(res.Status, res.Data, nil)
	}
	return res.Data, ""
}

func (c *recordingClient) boundSlugs(cands []string) map[string]bool { return map[string]bool{} }

func TestIngestionSample(t *testing.T) {
	record := os.Getenv("INTEL_SAMPLE_RECORD") == "1"
	clockPath := filepath.Join(sampleDir, "clock.txt")
	var clock time.Time
	if record {
		clock = time.Now().UTC().Truncate(time.Second)
		_ = os.RemoveAll(filepath.Join(sampleDir, "responses"))
		if err := os.MkdirAll(sampleDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(clockPath, []byte(ts(clock)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		b, err := os.ReadFile(clockPath)
		if err != nil {
			t.Skip("no recorded sample; run with INTEL_SAMPLE_RECORD=1")
		}
		p := parseTS(strings.TrimSpace(string(b)))
		if p == nil {
			t.Fatalf("bad clock %q", b)
		}
		clock = *p
	}
	prev := nowUTC
	nowUTC = func() time.Time { return clock }
	defer func() { nowUTC = prev }()

	db := openTestDB(t)
	rc := &recordingClient{record: record}
	env := &ingestEnv{db: db, pid: "sample", sc: rc, now: clock}
	for _, s := range defaultSources() {
		if err := ensureSource(db, env.pid, s); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := runDueSources(env, true, "")
	if err != nil {
		t.Fatal(err)
	}
	forecasts, err := updateForecasts(env, 0)
	if err != nil {
		t.Fatal(err)
	}

	manifest := sampleManifest(t, db, env.pid, clock, runs, forecasts)
	manifest["calls"] = rc.calls
	got, _ := json.MarshalIndent(manifest, "", "  ")
	manifestPath := filepath.Join(sampleDir, "manifest.json")
	if record {
		if err := os.WriteFile(manifestPath, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		writeSampleRows(t, db, env.pid)
		t.Logf("recorded sample at %s: %s", ts(clock), manifest["digest"])
		return
	}
	want, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(got)) {
		_ = os.WriteFile(filepath.Join(t.TempDir(), "manifest.got.json"), got, 0o644)
		t.Fatalf("replayed ingestion differs from manifest.json\n--- got ---\n%s", got)
	}
}

// sampleManifest summarises what the run produced, per layer, plus a digest
// over the deterministic fields of every row.
func sampleManifest(t *testing.T, db *sql.DB, pid string, clock time.Time, runs []map[string]any, forecasts map[string]int) map[string]any {
	t.Helper()
	h := sha256.New()
	count := func(q string, args ...any) int {
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	digestRows := func(q string, args ...any) {
		rows, err := db.Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(h, vals...)
		}
	}
	digestRows(`SELECT content_hash, kind, source, COALESCE(published_time,''), COALESCE(event_time,''), COALESCE(ingested_at,'') FROM intelligence_evidence WHERE project_id = ? ORDER BY id`, pid)
	digestRows(`SELECT venue, external_id, text, deadline, event_key FROM intel_questions WHERE project_id = ? ORDER BY id`, pid)
	digestRows(`SELECT q.external_id, p.price, p.observed_at FROM intel_question_prices p JOIN intel_questions q ON q.id = p.question_id WHERE q.project_id = ? ORDER BY p.id`, pid)
	digestRows(`SELECT q.external_id, f.probability, f.snapshot_hash FROM intel_forecasts f JOIN intel_questions q ON q.id = f.question_id WHERE q.project_id = ? ORDER BY f.id`, pid)

	runSummary := []map[string]any{}
	for _, r := range runs {
		row := map[string]any{"source": r["source"], "fetched": r["fetched"], "inserted": r["inserted"], "duplicates": r["duplicates"], "calls": r["calls"]}
		if e, ok := r["error"]; ok && e != nil {
			row["error"] = e
		}
		runSummary = append(runSummary, row)
	}
	byVenue := map[string]int{}
	rows, err := db.Query(`SELECT venue, COUNT(*) FROM intel_questions WHERE project_id = ? GROUP BY venue`, pid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		var n int
		_ = rows.Scan(&v, &n)
		byVenue[v] = n
	}
	rows.Close()
	return map[string]any{
		"clock": ts(clock),
		"runs":  runSummary,
		"layers": map[string]any{
			"observed": map[string]int{
				"evidence":       count(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ?`, pid),
				"news":           count(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ? AND kind = 'news'`, pid),
				"measurements":   count(`SELECT COUNT(*) FROM intelligence_evidence WHERE project_id = ? AND kind = 'measurement'`, pid),
				"market_prices":  count(`SELECT COUNT(*) FROM intel_question_prices p JOIN intel_questions q ON q.id = p.question_id WHERE q.project_id = ?`, pid),
				"question_links": count(`SELECT COUNT(*) FROM intel_question_evidence l JOIN intel_questions q ON q.id = l.question_id WHERE q.project_id = ?`, pid),
			},
			"estimated": map[string]int{
				"entities":      count(`SELECT COUNT(*) FROM entities WHERE project_id = ?`, pid),
				"state_values":  count(`SELECT COUNT(*) FROM intel_state_values WHERE project_id = ?`, pid),
				"relationships": count(`SELECT COUNT(*) FROM intel_relationships WHERE project_id = ?`, pid),
				"claims":        count(`SELECT COUNT(*) FROM intel_claims WHERE project_id = ?`, pid),
			},
			"futures": map[string]any{
				"questions_by_venue": byVenue,
				"forecasts":          count(`SELECT COUNT(*) FROM intel_forecasts f JOIN intel_questions q ON q.id = f.question_id WHERE q.project_id = ?`, pid),
				"forecast_triggers":  forecasts,
			},
			"synthetic": 0,
		},
		"digest": hex.EncodeToString(h.Sum(nil)),
	}
}

// writeSampleRows exports a few rows per layer with their provenance and
// timestamps, for reviewers who want to see the stored shape.
func writeSampleRows(t *testing.T, db *sql.DB, pid string) {
	t.Helper()
	out := map[string]any{}
	news, _ := searchEvidence(db, EvidenceQuery{ProjectID: pid, Kind: "news", Limit: 3})
	meas, _ := searchEvidence(db, EvidenceQuery{ProjectID: pid, Kind: "measurement", Limit: 2})
	out["evidence_news"], out["evidence_measurement"] = news, meas
	qs, _ := listQuestions(db, pid, "open", 3)
	type qrow struct {
		Question  Question         `json:"question"`
		Prices    []map[string]any `json:"prices"`
		Forecasts []Forecast       `json:"forecasts"`
	}
	rows := []qrow{}
	for _, q := range qs {
		p, _ := priceSeries(db, q.ID)
		f, _ := forecastsFor(db, q.ID)
		rows = append(rows, qrow{q, p, f})
	}
	out["questions"] = rows
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(filepath.Join(sampleDir, "sample_rows.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
