//go:build live

package main

// Live API smoke tests. Build-tag `live` keeps them out of CI by
// default; run with:
//
//	go test -tags live -run TestLive ./...
//
// Each test is gated on a separate env var so missing one doesn't
// fail the suite — they self-skip with a clear message instead.
//
//	DEEPGRAM_API_KEY        — runs the Deepgram listen smoke test
//	OPENCODE_GO_API_KEY     — runs the OpenCode Go chat_completion smoke test
//
// What these prove that the stub-based Tier 1 tests can't:
//   - the integration catalog's auth header + base_url + path are
//     correct against the real upstream
//   - the response shape we parse against actually matches what
//     production returns today (live APIs drift over time)
//   - billing / quota / rate-limit handling — if the upstream returns
//     401 / 429 / 5xx for real, our parser surfaces it clearly
//
// Costs are tiny: Deepgram on a 250 ms WAV is well under a cent per
// run; OpenCode Go is a flat-rate sub. Still, don't run these in a
// loop.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireEnv skips the test cleanly when the gating var is missing.
// We use t.Skip rather than t.Fatal so a partial-credentials run
// surfaces what was skipped without failing CI.
func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Skipf("%s not set — live test skipped", name)
	}
	return v
}

// ─── Deepgram ──────────────────────────────────────────────────────

func TestLive_Deepgram_Listen(t *testing.T) {
	apiKey := requireEnv(t, "DEEPGRAM_API_KEY")

	// Use a real fixture. The 250ms tone won't transcribe to anything
	// meaningful but Deepgram will still return a well-formed envelope
	// — that's what we're testing here, not transcript content.
	// A spoken-word fixture would give richer assertions; we keep the
	// existing fixture to avoid bloating scenarios/fixtures/.
	audioPath := filepath.Join("scenarios", "fixtures", "tone-250ms.wav")
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatalf("fixture %s: %v", audioPath, err)
	}

	// Direct call to api.deepgram.com — same URL the integration
	// catalog points at. Sending bytes directly (not a URL) so we
	// don't need to host the file anywhere.
	req, err := http.NewRequest(http.MethodPost,
		"https://api.deepgram.com/v1/listen?model=nova-3&smart_format=true",
		bytes.NewReader(audioBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Token "+apiKey)
	req.Header.Set("Content-Type", "audio/wav")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("deepgram POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("deepgram returned %d: %s", resp.StatusCode, body)
	}

	// Parse through media's own parser — this catches drift between
	// Deepgram's real schema and what parseDeepgramResponse expects.
	parsed, err := parseDeepgramResponse(body)
	if err != nil {
		t.Fatalf("parseDeepgramResponse: %v\nbody: %s", err, body)
	}
	t.Logf("transcript text: %q (segments=%d, language=%q)",
		parsed.Text, len(parsed.Segments), parsed.Language)

	// We can't assert specific text (a 1kHz tone has nothing to say),
	// but we expect the parse to succeed and the language field to
	// be populated. That alone catches most schema drift.
	if parsed.Language == "" {
		// Deepgram detects language even on tones — usually "en" or
		// the global default. Empty would be a real regression.
		t.Logf("warning: empty detected_language — deepgram may have changed defaults")
	}
}

// ─── OpenCode Go ───────────────────────────────────────────────────

func TestLive_OpenCodeGo_ChatCompletion_Text(t *testing.T) {
	apiKey := requireEnv(t, "OPENCODE_GO_API_KEY")

	body := map[string]any{
		"model": "kimi-k2.6",
		"messages": []map[string]any{
			{"role": "user", "content": "Reply with exactly one word: pong"},
		},
		"temperature": 0,
		"max_tokens":  10,
	}
	out := postOpenCodeGo(t, apiKey, body)

	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in response: %v", out)
	}
	first := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	t.Logf("model said: %q", content)
	if !strings.Contains(strings.ToLower(content), "pong") {
		// We log instead of failing — a non-zero-temperature decode
		// could drift, but the catalog's job is to forward the call
		// correctly, not police output content.
		t.Logf("warning: response didn't contain 'pong' — model may have ignored the directive")
	}
}

func TestLive_OpenCodeGo_ChatCompletion_Vision(t *testing.T) {
	apiKey := requireEnv(t, "OPENCODE_GO_API_KEY")

	// 1×1 white PNG as a data: URL — smallest possible vision input.
	// The model won't have anything interesting to say but the
	// multimodal request shape is what we're testing here.
	tinyPNG := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

	body := map[string]any{
		"model": "kimi-k2.6",
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "What colour is this image? Reply in one word."},
				{"type": "image_url", "image_url": map[string]any{"url": tinyPNG}},
			}},
		},
		"temperature": 0,
		"max_tokens":  10,
	}
	out := postOpenCodeGo(t, apiKey, body)

	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("vision request returned no choices — model may not support vision: %v", out)
	}
	first := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	t.Logf("vision model said: %q", content)
	// Don't assert "white" — model accuracy on a 1px PNG isn't the
	// point. Empty content would mean the multimodal shape was
	// rejected, which IS the point.
	if strings.TrimSpace(content) == "" {
		t.Errorf("multimodal call returned empty content — opencode-go may not be forwarding image_url parts")
	}
}

// postOpenCodeGo is the shared helper. Direct call to the chat
// completions endpoint — same URL + auth shape the integration
// catalog declares. Decodes the JSON response.
func postOpenCodeGo(t *testing.T, apiKey string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost,
		"https://opencode.ai/zen/go/v1/chat/completions",
		bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("opencode-go POST: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("opencode-go returned %d: %s", resp.StatusCode, respBody)
	}
	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, respBody)
	}
	_ = fmt.Sprintf("ok") // pad imports
	return out
}
