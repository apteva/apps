//go:build integration

package main

// Tier 2 — full render pipeline against a real ffmpeg child AND a
// real storage sidecar. The testkit's WithDependency option spawns
// storage as a companion sidecar and stands up a gateway proxy so
// media's storageclient.go hits production HTTP paths.
//
// What this catches that a mock can't:
//   - storage HTTP shape changes that drift away from the client
//   - multipart upload encoding differences (real storage does
//     server-side validation we'd otherwise miss)
//   - storage schema migrations / id allocation behaviour
//   - the gateway prefix-strip + token-swap actually working
//
// The fixture we use for the source video is the same checked-in
// 5-second sample-5s.mp4 the Tier 3 scenario uses, so a regression
// here usually shows up identically there.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// uploadFixtureToStorage hits the real storage sidecar's /files
// endpoint via the gateway URL the parent Sidecar now exposes. We
// can't use sc.POST directly because that targets the parent (media)
// sidecar — cross-app calls go through the gateway.
//
// Returns the assigned storage file_id.
func uploadFixtureToStorage(t *testing.T, sc *tk.Sidecar, projectID, name, contentType, folder string, payload []byte) int64 {
	t.Helper()
	gw := sc.GatewayURL()
	if gw == "" {
		t.Fatal("sc.GatewayURL() empty — WithDependency wiring is broken")
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("folder", folder)
	// Private (the storage default) — readable by anyone holding a
	// platform bearer (the gateway swaps in storage's own token).
	// Don't use "signed" here: signed-visibility downloads require
	// ?sig=…&exp=… on the URL, and the renderpool's storageclient
	// only sends a bearer.
	mw.WriteField("visibility", "private")
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	url := gw + "/api/apps/storage/files?project_id=" + projectID
	req, _ := http.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Any bearer is fine — the gateway's token-swap layer replaces
	// it with storage's own token before forwarding.
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("upload to storage: %d: %s", resp.StatusCode, body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("parse upload response: %v: %s", err, body)
	}
	if out.ID == 0 {
		t.Fatalf("storage returned id=0: %s", body)
	}
	return out.ID
}

// downloadFromStorage fetches bytes by storage file_id via the
// gateway. Render outputs are uploaded with visibility=signed, so
// bearer-only downloads are 403'd by storage; we mint a signed URL
// via files_get_url first and then GET that. Mirrors what an agent
// (or panel) would do to share + render the output.
func downloadFromStorage(t *testing.T, sc *tk.Sidecar, projectID string, fileID int64) []byte {
	t.Helper()
	signedURL := mintSignedURL(t, sc, projectID, fileID)
	resp, err := http.Get(signedURL) // signed URLs need no Authorization header
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("download id=%d via signed URL: %d: %s", fileID, resp.StatusCode, body)
	}
	out, _ := io.ReadAll(resp.Body)
	return out
}

// mintSignedURL hits storage's files_get_url MCP tool through the
// gateway. Returns the signed URL the call produces.
func mintSignedURL(t *testing.T, sc *tk.Sidecar, projectID string, fileID int64) string {
	t.Helper()
	gw := sc.GatewayURL()
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "files_get_url",
			"arguments": map[string]any{
				"_project_id": projectID,
				"id":          fileID,
				"ttl_seconds": 60,
			},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, gw+"/api/apps/storage/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("files_get_url: %d: %s", resp.StatusCode, rawResp)
	}
	// Unwrap the JSON-RPC + content[].text envelope to get the
	// inner tool-result map.
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("decode mcp envelope: %v: %s", err, rawResp)
	}
	if len(env.Result.Content) == 0 {
		t.Fatalf("files_get_url empty content: %s", rawResp)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &inner); err != nil {
		t.Fatalf("decode inner: %v: %s", err, env.Result.Content[0].Text)
	}
	rawURL, _ := inner["url"].(string)
	if rawURL == "" {
		t.Fatalf("files_get_url returned no url: %v", inner)
	}
	// Storage returns a relative URL (e.g. "/files/2/content?sig=…")
	// expecting the platform to mount it. Rewrite it to go through
	// our test gateway, which also strips the /api/apps/storage prefix
	// before forwarding.
	if strings.HasPrefix(rawURL, "/") {
		return gw + "/api/apps/storage" + rawURL
	}
	return rawURL
}

// listStorageFolder counts files in a storage folder via the gateway.
func listStorageFolder(t *testing.T, sc *tk.Sidecar, projectID, folder string) []map[string]any {
	t.Helper()
	gw := sc.GatewayURL()
	url := fmt.Sprintf("%s/api/apps/storage/files?project_id=%s&folder=%s", gw, projectID, folder)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list folder %q: %d: %s", folder, resp.StatusCode, body)
	}
	var out struct {
		Files []map[string]any `json:"files"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("parse list: %v: %s", err, body)
	}
	return out.Files
}

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("scenarios", "fixtures", "sample-5s.mp4"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// pollUntilOk repeatedly calls media_get_render until the row reaches
// a terminal status or the deadline expires.
func pollUntilOk(t *testing.T, sc *tk.Sidecar, projectID string, renderID int64, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := sc.MCP("media_get_render", map[string]any{
			"_project_id": projectID,
			"render_id":   renderID,
		})
		r, _ := out["render"].(map[string]any)
		st, _ := r["status"].(string)
		if st == "ok" || st == "failed" || st == "cancelled" {
			return r
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("render %d did not reach terminal status within %v", renderID, timeout)
	return nil
}

// spawnMediaWithStorage is the common setup: media + storage as a
// real dependency. Any test that needs the cross-app render pipeline
// uses this rather than re-declaring the topology.
func spawnMediaWithStorage(t *testing.T) *tk.Sidecar {
	t.Helper()
	return tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithDependency("storage", "../storage"),
		tk.WithConfig(map[string]string{
			"render_pool_size":       "1",
			"render_timeout_seconds": "30",
			// Skip the indexer's noisy 30s sweep in tests; renders
			// don't need it. (The indexer would also try to probe
			// the source we upload, which is fine but slow.)
			"poll_interval_seconds": "60",
		}),
	)
}

// ─── Tests ──────────────────────────────────────────────────────────

func TestSidecar_RenderPipeline_Trim(t *testing.T) {
	skipIfNoFFmpeg(t)
	sc := spawnMediaWithStorage(t)

	// Upload the source video into storage via the gateway.
	srcID := uploadFixtureToStorage(t, sc, "test-proj",
		"sample-5s.mp4", "video/mp4", "/tests/", fixtureBytes(t))

	// Submit the trim against the real storage file_id.
	subm := sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj",
		"file_id":     strconv.FormatInt(srcID, 10),
		"start_ms":    1000,
		"end_ms":      3000,
		"output_name": "trimmed.mp4",
	})
	renderID := int64(subm["render_id"].(float64))

	final := pollUntilOk(t, sc, "test-proj", renderID, 25*time.Second)
	if final["status"] != "ok" {
		t.Fatalf("render failed: %v", final)
	}
	outputIDStr, _ := final["output_file_id"].(string)
	outputID, _ := strconv.ParseInt(outputIDStr, 10, 64)
	if outputID == 0 {
		t.Fatal("output_file_id not set")
	}

	// Validate the bytes are a real mp4 by ffprobe-ing them.
	bytes := downloadFromStorage(t, sc, "test-proj", outputID)
	if len(bytes) == 0 {
		t.Fatal("output is empty")
	}

	tmp := filepath.Join(t.TempDir(), "verify.mp4")
	if err := os.WriteFile(tmp, bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffprobe",
		"-v", "error", "-print_format", "json",
		"-show_format", tmp,
	)
	probeOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe uploaded output: %v", err)
	}
	var probed struct {
		Format struct {
			Duration   string `json:"duration"`
			FormatName string `json:"format_name"`
		} `json:"format"`
	}
	if err := json.Unmarshal(probeOut, &probed); err != nil {
		t.Fatalf("parse ffprobe: %v", err)
	}
	d, _ := strconv.ParseFloat(probed.Format.Duration, 64)
	// Stream-copy trim of an x264-ultrafast source: keyframe alignment
	// can stretch the clip; just assert it shrunk vs the 5s source.
	if d <= 0 || d >= 5.0 {
		t.Errorf("trimmed duration=%.2fs (source is 5s); render didn't trim", d)
	}

	// Output must have landed in /renders/ on storage.
	files := listStorageFolder(t, sc, "test-proj", "/renders/")
	if len(files) != 1 {
		t.Errorf("expected 1 file in /renders/, got %d (%v)", len(files), files)
	}
}

func TestSidecar_RenderPipeline_ExtractFrame(t *testing.T) {
	skipIfNoFFmpeg(t)
	sc := spawnMediaWithStorage(t)

	srcID := uploadFixtureToStorage(t, sc, "test-proj",
		"sample-5s.mp4", "video/mp4", "/tests/", fixtureBytes(t))

	subm := sc.MCP("media_extract_frame", map[string]any{
		"_project_id": "test-proj",
		"file_id":     strconv.FormatInt(srcID, 10),
		"at_ms":       2500,
		"width":       160,
	})
	renderID := int64(subm["render_id"].(float64))

	final := pollUntilOk(t, sc, "test-proj", renderID, 25*time.Second)
	if final["status"] != "ok" {
		t.Fatalf("extract_frame failed: %v", final)
	}
	outputIDStr, _ := final["output_file_id"].(string)
	outputID, _ := strconv.ParseInt(outputIDStr, 10, 64)

	bytes := downloadFromStorage(t, sc, "test-proj", outputID)
	// PNG signature: 89 50 4E 47
	if len(bytes) < 8 || bytes[0] != 0x89 || bytes[1] != 'P' || bytes[2] != 'N' || bytes[3] != 'G' {
		t.Errorf("output bytes lack PNG signature: %x", bytes[:min(8, len(bytes))])
	}
}

func TestSidecar_RenderPipeline_FailsGracefullyOnMissingSource(t *testing.T) {
	skipIfNoFFmpeg(t)
	sc := spawnMediaWithStorage(t)

	// Nothing uploaded. file_id 9999 doesn't exist in real storage
	// either — the worker should fail gracefully with a useful error.
	subm := sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj",
		"file_id":     "9999",
		"start_ms":    0,
		"end_ms":      1000,
	})
	renderID := int64(subm["render_id"].(float64))

	final := pollUntilOk(t, sc, "test-proj", renderID, 15*time.Second)
	if final["status"] != "failed" {
		t.Fatalf("expected failed status on missing source, got %v", final)
	}
	errMsg, _ := final["error"].(string)
	if errMsg == "" {
		t.Error("failed render didn't capture an error message")
	}
	// Error should reference download / source — not something
	// generic like "ffmpeg failed". This catches the case where we
	// silently swallow the storage 404.
	if !strings.Contains(strings.ToLower(errMsg), "source") &&
		!strings.Contains(strings.ToLower(errMsg), "404") &&
		!strings.Contains(strings.ToLower(errMsg), "download") &&
		!strings.Contains(strings.ToLower(errMsg), "lookup") {
		t.Errorf("error message doesn't mention source/download/404: %q", errMsg)
	}
}

// TestSidecar_RenderPipeline_FullFlow exercises the agent's full
// happy path described in the docs: search-style listing, extract a
// frame for inspection, then trim. Single test, real storage,
// real ffmpeg, end-to-end.
func TestSidecar_RenderPipeline_FullFlow(t *testing.T) {
	skipIfNoFFmpeg(t)
	sc := spawnMediaWithStorage(t)

	srcID := uploadFixtureToStorage(t, sc, "test-proj",
		"sample-5s.mp4", "video/mp4", "/tests/", fixtureBytes(t))
	srcIDStr := strconv.FormatInt(srcID, 10)

	// 1. extract_frame at 2.5s — agent uses this to confirm the
	// moment it'll trim around.
	frame := sc.MCP("media_extract_frame", map[string]any{
		"_project_id": "test-proj",
		"file_id":     srcIDStr,
		"at_ms":       2500,
		"width":       320,
	})
	frameID := int64(frame["render_id"].(float64))
	frameFinal := pollUntilOk(t, sc, "test-proj", frameID, 25*time.Second)
	if frameFinal["status"] != "ok" {
		t.Fatalf("frame extraction failed: %v", frameFinal)
	}

	// 2. trim 2 seconds out of the middle.
	trim := sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj",
		"file_id":     srcIDStr,
		"start_ms":    1500,
		"end_ms":      3500,
	})
	trimID := int64(trim["render_id"].(float64))
	trimFinal := pollUntilOk(t, sc, "test-proj", trimID, 25*time.Second)
	if trimFinal["status"] != "ok" {
		t.Fatalf("trim failed: %v", trimFinal)
	}

	// /renders/ should now hold both the PNG and the trimmed mp4.
	files := listStorageFolder(t, sc, "test-proj", "/renders/")
	if len(files) != 2 {
		t.Errorf("expected 2 renders in /renders/, got %d: %v", len(files), files)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
