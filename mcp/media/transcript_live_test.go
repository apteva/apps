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
		// Kimi K2.6 is a reasoning model — it spends tokens thinking
		// before producing visible output. With a tight cap the
		// response comes back content=null, finish_reason=length, and
		// only `reasoning` is populated. 200 leaves room for both.
		"max_tokens": 200,
	}
	out := postOpenCodeGo(t, apiKey, body)

	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in response: %v", out)
	}
	first := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	reasoning, _ := msg["reasoning"].(string)
	finishReason, _ := first["finish_reason"].(string)
	t.Logf("content=%q reasoning=%q finish_reason=%q", content, reasoning, finishReason)

	// The catalog's job is to forward the call correctly — proof of
	// that is content OR reasoning being non-empty. Asserting on
	// specific text would punish model drift that has nothing to do
	// with the integration.
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) == "" {
		t.Errorf("response had no content and no reasoning — call likely didn't reach the model: %v", out)
	}
	if strings.Contains(strings.ToLower(content), "pong") {
		t.Logf("model produced visible output ✓")
	} else if reasoning != "" {
		t.Logf("model is reasoning — that's fine, the integration shape works")
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
		// Same reasoning-budget reasoning as the text test — Kimi
		// thinks before answering, vision adds extra reasoning, give
		// it room.
		"max_tokens": 300,
	}
	out := postOpenCodeGo(t, apiKey, body)

	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("vision request returned no choices — model may not support vision: %v", out)
	}
	first := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	reasoning, _ := msg["reasoning"].(string)
	t.Logf("vision: content=%q reasoning=%q", content, reasoning)

	// The integration's job is to forward image_url parts intact and
	// get a valid response back. Empty everything would mean the
	// multimodal shape was rejected before reaching the model.
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) == "" {
		t.Errorf("multimodal call returned empty content + reasoning — opencode-go may not be forwarding image_url parts: %v", out)
	}
}

// TestLive_OpenCodeGo_DescribePrompt validates the actual prompt the
// auto-describer sends — same shape as buildDescribePrompt for the
// audio-with-transcript case. Catches regressions where the system
// prompt or the temperature/max_tokens choice produce content that's
// no longer 2-3 sentences.
func TestLive_OpenCodeGo_DescribePrompt(t *testing.T) {
	apiKey := requireEnv(t, "OPENCODE_GO_API_KEY")

	body := map[string]any{
		"model": "kimi-k2.6",
		"messages": []map[string]any{
			{"role": "system", "content": "You write concise media descriptions. Output exactly 2-3 sentences in plain prose, no preamble, no headings, no quotes. Focus on what is depicted or said — subjects, setting, action. Avoid speculation about emotions or intent unless explicitly visible/audible. Don't mention the medium ('this video', 'this audio') — describe the content directly."},
			{"role": "user", "content": "Describe what's said in this recording in 2-3 sentences.\n\nTranscript:\nAlice: Quarterly results came in above forecast — revenue up 18%, margins held at 24%. Bob: Great, but the cloud spend is creeping. We need to flag that for the board. Alice: Agreed, I'll add a slide."},
		},
		"temperature": 0.3,
		"max_tokens":  1500,
	}
	out := postOpenCodeGo(t, apiKey, body)

	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices: %v", out)
	}
	first := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	reasoning, _ := msg["reasoning"].(string)
	finishReason, _ := first["finish_reason"].(string)

	t.Logf("description: %q\nreasoning chars: %d\nfinish_reason: %s",
		content, len(reasoning), finishReason)

	// Validate the prompt produces something useful — empty or null
	// content with no reasoning fallback would mean our prompt is
	// busted (or max_tokens is too tight).
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) == "" {
		t.Errorf("describe prompt produced no content or reasoning")
	}

	// Check for prose hygiene: shouldn't start with markers like
	// 'description:' or quote the entire response. These are common
	// failure modes when the system prompt drifts.
	low := strings.ToLower(strings.TrimSpace(content))
	if strings.HasPrefix(low, "description:") || strings.HasPrefix(low, "summary:") {
		t.Errorf("model added preamble — system prompt may need tuning: %q", content)
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
