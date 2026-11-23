package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// publicFixture serves fast-b (as "m" in the response, like a real
// provider's own id) behind the public name text-fast.
func publicFixture(t *testing.T, upstream http.HandlerFunc) *gatewayFixture {
	t.Helper()
	f := newGatewayFixture(t, upstream, nil)
	if _, err := dbPublicModelUpsert(f.ctx.AppDB(), "proj-test", map[string]any{"name": "text-fast",
		"routes": []any{map[string]any{"provider": "openai", "model": "fast-b"}}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func sseChunks(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, part := range []string{"hel", "lo"} {
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-up\",\"model\":\"accounts/fireworks/fast-b\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
	}
	io.WriteString(w, "data: {\"id\":\"chatcmpl-up\",\"model\":\"accounts/fireworks/fast-b\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	io.WriteString(w, "data: {\"id\":\"chatcmpl-up\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n")
	io.WriteString(w, "data: [DONE]\n\n")
}

func TestPublicModelNameIsReturnedInsteadOfProviderModel(t *testing.T) {
	f := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			sseChunks(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-up","object":"chat.completion","model":"accounts/fireworks/fast-b","choices":[{"index":0,"message":{"role":"assistant","content":"<ok> & done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	})

	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`)
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != 200 || resp.Model != "text-fast" || resp.Choices[0].Message.Content != "<ok> & done" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fireworks") {
		t.Fatalf("provider model leaked: %s", rec.Body.String())
	}
	if ev := f.lastEvent(); ev.Model != "openai/fast-b" || ev.RequestedModel != "text-fast" {
		t.Fatalf("usage must keep the provider route: %+v", ev)
	}

	stream := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	body := stream.Body.String()
	if stream.Code != 200 || strings.Contains(body, "fireworks") || strings.Count(body, `"model":"text-fast"`) != 4 {
		t.Fatalf("stream should name text-fast in every chunk and the usage chunk: %s", body)
	}

	responses := f.do(http.MethodPost, "/v1/responses", `{"model":"text-fast","input":"hi"}`)
	if !strings.Contains(responses.Body.String(), `"model":"text-fast"`) || strings.Contains(responses.Body.String(), "fireworks") {
		t.Fatalf("responses = %s", responses.Body.String())
	}

	direct := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/fast-b","messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(direct.Body.String(), `"model":"accounts/fireworks/fast-b"`) {
		t.Fatalf("provider-prefixed requests keep the provider's model: %s", direct.Body.String())
	}
}

func TestPublicModelHidesProviderErrors(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusNotFound)
	f := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		code := int(status.Load())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"id":"chatcmpl-abc123","error":{"message":"Model not found, inaccessible, and/or not deployed: accounts/fireworks/fast-b (%d)"}}`, code)
	})
	leaks := []string{"chatcmpl-", "fireworks", "fast-b", "deployed"}
	check := func(label, raw string, wantStatus, gotStatus int) {
		t.Helper()
		if gotStatus != wantStatus {
			t.Fatalf("%s: status=%d want %d body=%s", label, gotStatus, wantStatus, raw)
		}
		for _, leak := range leaks {
			if strings.Contains(raw, leak) {
				t.Fatalf("%s leaked %q: %s", label, leak, raw)
			}
		}
	}

	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`)
	check("404", rec.Body.String(), http.StatusBadGateway, rec.Code)
	if !strings.Contains(rec.Body.String(), publicModelUnavailableMsg) {
		t.Fatalf("generic message missing: %s", rec.Body.String())
	}
	failed := f.events.EventsByTopic("llm.request.failed")
	if len(failed) == 0 || !strings.Contains(fmt.Sprint(failed[len(failed)-1].Data), "deployed") {
		t.Fatalf("llm.request.failed should keep the raw error: %v", failed)
	}
	if ev := f.lastEvent(); ev.Status != "failed" || ev.Model != "openai/fast-b" {
		t.Fatalf("usage = %+v", ev)
	}

	stream := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	check("stream 404", stream.Body.String(), http.StatusBadGateway, stream.Code)

	for code, want := range map[int]int{http.StatusTooManyRequests: 429, http.StatusBadRequest: 400, http.StatusUnprocessableEntity: 400,
		http.StatusGatewayTimeout: 504, http.StatusInternalServerError: 502, http.StatusUnauthorized: 502} {
		status.Store(int64(code))
		rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`)
		check(fmt.Sprint(code), rec.Body.String(), want, rec.Code)
	}

	status.Store(http.StatusNotFound)
	direct := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/fast-b","messages":[{"role":"user","content":"hi"}]}`)
	if direct.Code != http.StatusNotFound || !strings.Contains(direct.Body.String(), "deployed") {
		t.Fatalf("provider-prefixed requests keep the raw error: %d %s", direct.Code, direct.Body.String())
	}
}

func TestInputEstimateCoversOverheadAndMargin(t *testing.T) {
	body := map[string]any{"model": "openai/m", "messages": []any{map[string]any{"role": "user", "content": "Say hi in three words."}}}
	if got := applyInputMargin(estimateChatTokens(body), nil); got < 22 {
		t.Fatalf("estimate %d is below the real count of 22", got)
	}
	if got := applyInputMargin(estimateTokens("Say hi in three words."), nil); got < 8 {
		t.Fatalf("embedding estimate %d", got)
	}
	if a, b := applyInputMargin(100, nil), applyInputMargin(100, &AdmissionConfig{InputEstimateMargin: 2}); a != 125 || b != 200 {
		t.Fatalf("margins = %d, %d", a, b)
	}
	for _, bad := range []float64{0.5, 6} {
		hook := map[string]any{"app": "billing", "authorize_tool": "a", "settle_tool": "s", "input_estimate_margin": bad}
		if _, err := admissionFromAny(hook); err == nil {
			t.Fatalf("margin %v should be rejected", bad)
		}
	}

	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatJSON(w, "ok", 22, 4) }), nil)
	hooks := installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		return map[string]any{"allow": true}, nil
	})
	hook := map[string]any{"app": "billing", "authorize_tool": "llm_authorize", "settle_tool": "llm_settle", "input_estimate_margin": 2.0}
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": hook})
	f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"Say hi in three words."}]}`)
	auth := hooks.byTool("llm_authorize")
	if len(auth) != 1 {
		t.Fatalf("authorize calls = %d", len(auth))
	}
	plain := map[string]any{"model": "openai/m", "messages": []any{map[string]any{"role": "user", "content": "Say hi in three words."}}}
	if got, want := fmt.Sprint(auth[0].Input["estimated_input_tokens"]), fmt.Sprint(estimateChatTokens(plain)*2); got != want {
		t.Fatalf("authorize estimate = %s, want %s", got, want)
	}
}

func TestChatToolDoesNotSendInternalFieldsToProvider(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		keys = keys[:0]
		for k := range body {
			keys = append(keys, k)
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"model":"e","data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":2}}`)
			return
		}
		chatJSON(w, "ok", 3, 1)
	}), nil)
	internal := map[string]bool{"_project_id": true, "_caller": true, "project_id": true, "subject_type": true, "subject_id": true, "request_id": true, "_llm_request_id": true}
	assertClean := func(label string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(keys) == 0 {
			t.Fatalf("%s: provider not called", label)
		}
		for _, k := range keys {
			if internal[k] || strings.HasPrefix(k, "_") {
				t.Fatalf("%s: provider body has %q (%v)", label, k, keys)
			}
		}
	}
	out, err := f.app.toolChatComplete(f.ctx, map[string]any{
		"model": "openai/m", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"_project_id": "proj-test", "_caller": "x", "project_id": "proj-test", "subject_type": "tenant", "subject_id": "acme", "request_id": "tool-req-1",
	})
	if err != nil || out == nil {
		t.Fatalf("chat tool: %v", err)
	}
	assertClean("chat")
	if ev := f.lastEvent(); ev.RequestID != "tool-req-1" || ev.SubjectID != "acme" || ev.ProjectID != "proj-test" {
		t.Fatalf("tool identity lost: %+v", ev)
	}
	if _, err := f.app.toolEmbeddingsCtx(t.Context(), f.ctx, map[string]any{
		"model": "openai/e", "input": "hi", "_project_id": "proj-test", "project_id": "proj-test", "request_id": "tool-req-2",
	}); err != nil {
		t.Fatalf("embeddings tool: %v", err)
	}
	assertClean("embeddings")
}

func TestStuckSettlementAlertsOnceAndKeepsRetrying(t *testing.T) {
	if (&AdmissionConfig{}).stuckAfter() != 10 || (*AdmissionConfig)(nil).stuckAfter() != 10 {
		t.Fatal("default stuck threshold should be 10")
	}
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatJSON(w, "ok", 1, 1) }), nil)
	var failSettle atomic.Bool
	failSettle.Store(true)
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		if tool == "llm_settle" && failSettle.Load() {
			return nil, errors.New("billing down")
		}
		return map[string]any{"allow": true, "admission_ref": "adm-42"}, nil
	})
	hook := map[string]any{"app": "billing", "authorize_tool": "llm_authorize", "settle_tool": "llm_settle", "stuck_after_attempts": 3}
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": hook})
	f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`)
	db := f.ctx.AppDB()
	retry := func() {
		t.Helper()
		_, _ = db.Exec(`UPDATE admission_outbox SET next_attempt_at=datetime('now','-1 second')`)
		if n, _ := deliverOutbox(f.ctx, 0); n != 0 {
			t.Fatal("delivered while billing was down")
		}
	}
	for i := 1; i <= 5; i++ {
		retry()
		stuck := f.events.EventsByTopic("llm.settlement.stuck")
		wantEvents := 0
		if i >= 3 {
			wantEvents = 1
		}
		if len(stuck) != wantEvents {
			t.Fatalf("attempt %d: stuck events = %d", i, len(stuck))
		}
		var st string
		var minutes float64
		_ = db.QueryRow(`SELECT status, (julianday(next_attempt_at)-julianday('now'))*1440 FROM admission_outbox`).Scan(&st, &minutes)
		if i < 3 && st != "pending" {
			t.Fatalf("attempt %d: status %s", i, st)
		}
		if i >= 3 && (st != "stuck" || minutes < 59) {
			t.Fatalf("attempt %d: status=%s next in %.1f min", i, st, minutes)
		}
	}
	data, _ := f.events.EventsByTopic("llm.settlement.stuck")[0].Data.(map[string]any)
	if data["admission_ref"] != "adm-42" || data["app"] != "billing" || !strings.Contains(fmt.Sprint(data["last_error"]), "billing down") || fmt.Sprint(data["attempts"]) != "3" {
		t.Fatalf("stuck event = %v", data)
	}
	rows, _ := dbOutboxList(db, "proj-test", "stuck", 10)
	if len(rows) != 1 {
		t.Fatalf("stuck filter = %v", rows)
	}
	failSettle.Store(false)
	_, _ = db.Exec(`UPDATE admission_outbox SET next_attempt_at=datetime('now','-1 second')`)
	if n, err := deliverOutbox(f.ctx, 0); n != 1 || err != nil {
		t.Fatalf("stuck settlement should still deliver: n=%d err=%v", n, err)
	}
	if bad, err := admissionFromAny(map[string]any{"app": "b", "authorize_tool": "a", "settle_tool": "s", "stuck_after_attempts": 5000}); err == nil {
		t.Fatalf("threshold above limit accepted: %+v", bad)
	}
}
