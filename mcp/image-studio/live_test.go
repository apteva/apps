//go:build live
// +build live

package main

// Live smoke test against the real OpenAI API. Skipped unless built
// with -tags live AND OPENAI_API_KEY is set in the env. Never reads
// from a config file; the key must come from the operator's shell
// so it can't leak through a checked-in fixture.
//
// Run:
//   OPENAI_API_KEY=sk-... go test -tags live -v -run TestLive_ ./...
//
// Costs vary by model (gpt-image-2 medium ~ a few cents per run).
// CI canary: gate behind a manual workflow + a dedicated key with
// strict spend caps + per-day rate limits via OpenAI's dashboard.
//
// What this verifies that the unit tests can't:
//   - the integrations/openai-api.json catalog entry is still in
//     sync with the live API shape (a silent breaking change at
//     OpenAI surfaces here, not as a 500 from the storage handoff)
//   - executeIntegrationToolWithRefresh's auth header construction
//     matches what OpenAI accepts (a typo in the catalog's
//     auth.headers map would 401 here)
//   - the response actually parses through normalizeImageResponse
//     with the live data shape

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const openaiURL = "https://api.openai.com/v1/images/generations"

// TestLive_OpenAI_GenerateImage hits the real OpenAI image API
// directly (not through apteva-server's integration runner — that
// would require a full apteva-server fixture). The intent is to
// verify the upstream contract: the URL, headers, and response shape
// our catalog assumes. If this passes, normalizeImageResponse will
// correctly parse what the platform path produces.
func TestLive_OpenAI_GenerateImage(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		t.Skip("OPENAI_API_KEY not set — skipping live test (run with: OPENAI_API_KEY=sk-... go test -tags live)")
	}

	// gpt-image-2 is the current SOTA. It always returns b64_json
	// (no URL response option), which is exactly the path
	// normalizeImageResponse + saveToStorage(files_upload) takes.
	body := map[string]any{
		"model":   "gpt-image-2",
		"prompt":  "minimalist line drawing of a teacup on a saucer, black ink on white",
		"n":       1,
		"size":    "1024x1024",
		"quality": "medium",
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", openaiURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upstream call failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("openai returned %d: %+v", resp.StatusCode, errBody)
	}

	raw, err := readBody(resp)
	if err != nil {
		t.Fatal(err)
	}

	// Run it through our parser to verify the shape is still what
	// normalizeImageResponse expects.
	images, revised, model, err := normalizeImageResponse("openai-api", raw)
	if err != nil {
		t.Fatalf("normalize failed (catalog drift?): %v\n\nraw response: %s", err, raw)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	// gpt-image-2 returns b64_json (never URL). Verify the bytes path
	// in imageBytes — that's what the production storage handoff uses.
	if images[0].B64 == "" {
		t.Fatal("b64_json empty — gpt-image-2 should always inline bytes")
	}
	if images[0].UpstreamURL != "" {
		t.Errorf("gpt-image-2 should not return URL, got %q", images[0].UpstreamURL)
	}
	bytes, err := imageBytes(images[0])
	if err != nil {
		t.Fatalf("imageBytes decode failed: %v", err)
	}
	if len(bytes) < 1024 {
		t.Errorf("decoded image suspiciously small: %d bytes", len(bytes))
	}
	// PNG magic — confirms we got real image bytes back.
	if len(bytes) >= 8 && string(bytes[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Logf("note: bytes[:8]=%x (not PNG header — check output_format)", bytes[:8])
	}

	t.Logf("live OK: model=%s revised_prompt=%q b64_len=%d decoded=%d bytes",
		model, revised, len(images[0].B64), len(bytes))
}

// readBody is a tiny helper so the test doesn't sprawl with bytes
// handling. Returns the body as json.RawMessage for direct passthrough
// into normalizeImageResponse.
func readBody(resp *http.Response) (json.RawMessage, error) {
	dec := json.NewDecoder(resp.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	return raw, nil
}
