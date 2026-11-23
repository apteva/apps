package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestMain(m *testing.M) {
	asyncSettlement = false
	os.Exit(m.Run())
}

type gatewayFixture struct {
	t      *testing.T
	ctx    *sdk.AppCtx
	app    *App
	token  string
	tokID  int64
	events *tk.EmitRecorder
}

func newGatewayFixture(t *testing.T, upstream http.Handler, tokenArgs map[string]any) *gatewayFixture {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	events := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithEmitter(events), tk.WithConfig(map[string]string{
		"openai_api_key": "sk-test", "anthropic_api_key": "sk-ant",
	}))
	app := &App{httpClient: srv.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	for _, provider := range []string{"openai", "anthropic"} {
		if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": provider, "base_url": srv.URL}); err != nil {
			t.Fatalf("provider %s: %v", provider, err)
		}
	}
	args := map[string]any{"project_id": "proj-test", "subject_type": "tenant", "subject_id": "acme", "scopes": []any{"*"}}
	for k, v := range tokenArgs {
		args[k] = v
	}
	tok, err := createToken(ctx.AppDB(), args)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return &gatewayFixture{t: t, ctx: ctx, app: app, token: tok["token"].(string), tokID: tok["id"].(int64), events: events}
}

func (f *gatewayFixture) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.app.handleV1(rec, req)
	return rec
}

func (f *gatewayFixture) lastEvent() *UsageEvent {
	f.t.Helper()
	ev, err := scanUsageEvent(f.ctx.AppDB().QueryRow(`SELECT ` + usageEventColumns + ` FROM usage_events ORDER BY id DESC LIMIT 1`))
	if err != nil {
		f.t.Fatalf("last usage event: %v", err)
	}
	return ev
}

func chatJSON(w http.ResponseWriter, content string, in, out int) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, content, in, out)
}

// stubHooks replaces the platform call used for admission hooks.
type stubHooks struct {
	mu       sync.Mutex
	calls    []hookCall
	respond  func(app, tool string, input map[string]any) (any, error)
	previous func(*sdk.AppCtx, string, string, string, map[string]any, any) error
}

type hookCall struct {
	App, Tool string
	Input     map[string]any
}

func installHooks(t *testing.T, respond func(app, tool string, input map[string]any) (any, error)) *stubHooks {
	h := &stubHooks{respond: respond, previous: hookCaller}
	hookCaller = func(_ *sdk.AppCtx, _ string, app, tool string, input map[string]any, out any) error {
		h.mu.Lock()
		h.calls = append(h.calls, hookCall{App: app, Tool: tool, Input: input})
		h.mu.Unlock()
		resp, err := h.respond(app, tool, input)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(resp)
		return json.Unmarshal(b, out)
	}
	t.Cleanup(func() { hookCaller = h.previous })
	return h
}

func (h *stubHooks) byTool(tool string) []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []hookCall
	for _, c := range h.calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

var billingHook = map[string]any{"app": "billing", "authorize_tool": "llm_authorize", "settle_tool": "llm_settle"}

// ---- Streaming ----

func TestChatStreamingRelaysProviderChunksAndRecordsProviderUsage(t *testing.T) {
	var upstreamBody map[string]any
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range []string{"hel", "lo ", "world"} {
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}), nil)
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream should be asked to stream, got %v", upstreamBody["stream"])
	}
	if opts, _ := upstreamBody["stream_options"].(map[string]any); opts["include_usage"] != true {
		t.Fatalf("upstream stream_options = %v", upstreamBody["stream_options"])
	}
	body := rec.Body.String()
	if strings.Count(body, `"content":"`) != 3 || !strings.Contains(body, `"content":"lo "`) {
		t.Fatalf("chunks not relayed with whitespace intact: %s", body)
	}
	if !strings.Contains(body, `"prompt_tokens":5`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("missing final usage chunk or DONE: %s", body)
	}
	ev := f.lastEvent()
	if ev.Status != "completed" || ev.RequestTokens != 5 || ev.ResponseTokens != 2 || ev.Endpoint != "chat.completions" {
		t.Fatalf("usage = %+v", ev)
	}
	if strings.Contains(string(ev.UsageDetails), "estimated") {
		t.Fatalf("provider usage should not be marked estimated: %s", ev.UsageDetails)
	}
}

func TestChatStreamWithoutProviderUsageEstimatesFromSentText(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"abcdefghij\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}), nil)
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"usage"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	ev := f.lastEvent()
	if ev.ResponseTokens != 3 || !strings.Contains(string(ev.UsageDetails), `"usage_source":"estimated"`) {
		t.Fatalf("expected 10 chars -> 3 estimated tokens, got %+v details=%s", ev, ev.UsageDetails)
	}
}

func TestChatStreamFailsOverBeforeFirstChunkOnly(t *testing.T) {
	var calls atomic.Int64
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		if body["model"] == "primary" {
			http.Error(w, `{"error":"overloaded"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}],\"usage\":null}\n\ndata: [DONE]\n\n")
	}), nil)
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"fallback_policy": map[string]any{"routes": []any{map[string]any{"model": "openai/backup"}}}})
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/primary","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"content":"ok"`) || calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
	}
	if ev := f.lastEvent(); ev.Model != "openai/backup" {
		t.Fatalf("served model = %s", ev.Model)
	}
}

func TestChatStreamClientDisconnectSettlesForGeneratedOutput(t *testing.T) {
	release := make(chan struct{})
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial output text\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	f := newGatewayFixture(t, upstream, nil)
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		return map[string]any{"allow": true, "admission_ref": "adm-1"}, nil
	})
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook})
	gateway := httptest.NewServer(http.HandlerFunc(f.app.handleV1))
	defer gateway.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"openai/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.Contains(line, "partial output") {
		t.Fatalf("first line = %q", line)
	}
	cancel()
	resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	var ev *UsageEvent
	for time.Now().Before(deadline) {
		ev = f.lastEvent()
		if ev.Status != "reserved" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ev.Status != "completed" || ev.ResponseTokens == 0 || !strings.Contains(string(ev.UsageDetails), `"client_disconnected":true`) {
		t.Fatalf("expected a partial completed record, got %+v details=%s", ev, ev.UsageDetails)
	}
	rows, _ := dbOutboxList(f.ctx.AppDB(), "proj-test", "", 10)
	if len(rows) != 1 {
		t.Fatalf("settlement not queued: %v", rows)
	}
	payload := rows[0]["payload"].(map[string]any)
	if payload["partial"] != true || payload["status"] != "completed" || payload["admission_ref"] != "adm-1" {
		t.Fatalf("settlement payload = %v", payload)
	}
}

func TestAnthropicStreamTranslatesTextToolsAndUsage(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("anthropic request should stream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":11,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"lookup"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		} {
			var typ struct{ Type string }
			_ = json.Unmarshal([]byte(ev), &typ)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, ev)
		}
	}), nil)
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"anthropic/claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := rec.Body.String()
	for _, want := range []string{`"content":"Hi "`, `"name":"lookup"`, `"id":"tu_1"`, `"arguments":"{\"q\":"`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if ev := f.lastEvent(); ev.RequestTokens != 11 || ev.ResponseTokens != 9 {
		t.Fatalf("usage = %+v", ev)
	}
}

func TestResponsesStreamEmitsLiveDeltas(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" two\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}), nil)
	rec := f.do(http.MethodPost, "/v1/responses", `{"model":"openai/m","stream":true,"input":"hi"}`)
	body := rec.Body.String()
	if strings.Count(body, "event: response.output_text.delta") != 2 || !strings.Contains(body, `"text":"one two"`) || !strings.Contains(body, "response.completed") {
		t.Fatalf("body = %s", body)
	}
	if ev := f.lastEvent(); ev.Endpoint != "responses" || ev.ResponseTokens != 2 {
		t.Fatalf("usage = %+v", ev)
	}
}

// ---- Admission hooks ----

func TestAdmissionHookAuthorizesCapsOutputAndSettles(t *testing.T) {
	var upstreamMax any
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		upstreamMax = body["max_tokens"]
		chatJSON(w, "hello", 7, 3)
	}), map[string]any{"metadata": map[string]any{"account": "acct_42"}})
	hooks := installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		if tool == "llm_authorize" {
			return map[string]any{"allow": true, "admission_ref": "hold-9", "max_output_tokens": 50}, nil
		}
		return map[string]any{"ok": true}, nil
	})
	if _, err := dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook}); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`, "Idempotency-Key", "req-1")
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fmt.Sprint(upstreamMax) != "50" {
		t.Fatalf("hook max_output_tokens should cap the provider call, got %v", upstreamMax)
	}
	auth := hooks.byTool("llm_authorize")
	if len(auth) != 1 {
		t.Fatalf("authorize calls = %d", len(auth))
	}
	in := auth[0].Input
	md, _ := in["token_metadata"].(map[string]any)
	if in["request_id"] != "req-1" || md["account"] != "acct_42" || fmt.Sprint(in["max_output_tokens"]) != "200" || in["endpoint"] != "chat.completions" || in["model"] != "openai/m" {
		t.Fatalf("authorize input = %v", in)
	}
	if _, ok := in["estimated_input_tokens"]; !ok {
		t.Fatalf("authorize input lacks estimated_input_tokens: %v", in)
	}
	for _, money := range []string{"price", "amount", "charge"} {
		if _, ok := in[money]; ok {
			t.Fatalf("authorize input must not carry money: %v", in)
		}
	}
	if rec.Header().Get("X-Apteva-Admission-Ref") != "hold-9" {
		t.Fatalf("admission ref header = %q", rec.Header().Get("X-Apteva-Admission-Ref"))
	}
	if n, err := deliverOutbox(f.ctx, 0); err != nil || n != 1 {
		t.Fatalf("deliver: n=%d err=%v", n, err)
	}
	settle := hooks.byTool("llm_settle")
	if len(settle) != 1 {
		t.Fatalf("settle calls = %d", len(settle))
	}
	s := settle[0].Input
	if s["admission_ref"] != "hold-9" || s["status"] != "completed" || fmt.Sprint(s["input_tokens"]) != "7" || fmt.Sprint(s["output_tokens"]) != "3" ||
		s["provider"] != "openai" || s["model"] != "openai/m" || s["request_id"] != "req-1" {
		t.Fatalf("settle input = %v", s)
	}
	if _, ok := s["provider_cost"]; !ok {
		t.Fatalf("settle must include provider_cost: %v", s)
	}
	rows, _ := dbOutboxList(f.ctx.AppDB(), "proj-test", "delivered", 10)
	if len(rows) != 1 {
		t.Fatalf("outbox not marked delivered: %v", rows)
	}
}

func TestAdmissionDenialReturnsHookErrorAndFreesRequestID(t *testing.T) {
	var providerCalls atomic.Int64
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		chatJSON(w, "ok", 1, 1)
	}), nil)
	allow := atomic.Bool{}
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		if tool == "llm_authorize" && !allow.Load() {
			return map[string]any{"allow": false, "deny_reason": "out of credit", "http_status": 429, "error_type": "insufficient_quota"}, nil
		}
		return map[string]any{"allow": true}, nil
	})
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook, "limits": map[string]any{"monthly_request_limit": 1}})
	body := `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`
	rec := f.do(http.MethodPost, "/v1/chat/completions", body, "Idempotency-Key", "same")
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "insufficient_quota") || !strings.Contains(rec.Body.String(), "out of credit") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatal("denied request reached the provider")
	}
	usage, _ := dbUsageGet(f.ctx.AppDB(), usageFilter{ProjectID: "proj-test"})
	if usage.Requests != 0 {
		t.Fatalf("denied requests must not count toward usage: %+v", usage)
	}
	allow.Store(true)
	rec = f.do(http.MethodPost, "/v1/chat/completions", body, "Idempotency-Key", "same")
	if rec.Code != 200 {
		t.Fatalf("retry with the same idempotency key after denial should succeed (and the monthly limit of 1 should not be used up): %d %s", rec.Code, rec.Body.String())
	}
	if rows, _ := dbOutboxList(f.ctx.AppDB(), "proj-test", "", 10); len(rows) != 1 {
		t.Fatalf("only the allowed request should settle: %v", rows)
	}
}

func TestAdmissionUnavailableFailsClosedByDefaultAndOpenWhenConfigured(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatJSON(w, "ok", 2, 1) }), nil)
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		if tool == "llm_authorize" {
			return nil, errors.New("connection refused")
		}
		return map[string]any{}, nil
	})
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook})
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "admission_unavailable") {
		t.Fatalf("fail closed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	open := map[string]any{"app": "billing", "authorize_tool": "llm_authorize", "settle_tool": "llm_settle", "fail_mode": "open"}
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": open})
	rec = f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("fail open: status=%d body=%s", rec.Code, rec.Body.String())
	}
	ev := f.lastEvent()
	if !strings.Contains(string(ev.UsageDetails), `"admission":"bypassed"`) {
		t.Fatalf("bypass not recorded: %s", ev.UsageDetails)
	}
	if rows, _ := dbOutboxList(f.ctx.AppDB(), "proj-test", "pending", 10); len(rows) != 1 {
		t.Fatalf("bypassed request must still settle so billing can catch up: %v", rows)
	}
}

func TestTokenAdmissionOverridesPolicyAdmission(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatJSON(w, "ok", 1, 1) }),
		map[string]any{"admission": map[string]any{"app": "reseller", "authorize_tool": "auth", "settle_tool": "settle"}})
	hooks := installHooks(t, func(app, tool string, input map[string]any) (any, error) { return map[string]any{"allow": true}, nil })
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook})
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	if calls := hooks.byTool("auth"); len(calls) != 1 || calls[0].App != "reseller" || len(hooks.byTool("llm_authorize")) != 0 {
		t.Fatalf("token hook should win: %+v", hooks.calls)
	}
}

func TestEmbeddingsUseAdmissionHooks(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":4}}`)
	}), nil)
	hooks := installHooks(t, func(app, tool string, input map[string]any) (any, error) { return map[string]any{"allow": true}, nil })
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook})
	if rec := f.do(http.MethodPost, "/v1/embeddings", `{"model":"openai/e","input":"hi"}`); rec.Code != 200 {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	if calls := hooks.byTool("llm_authorize"); len(calls) != 1 || calls[0].Input["endpoint"] != "embeddings" {
		t.Fatalf("authorize = %+v", calls)
	}
	if ev := f.lastEvent(); ev.Endpoint != "embeddings" || ev.RequestTokens != 4 {
		t.Fatalf("usage = %+v", ev)
	}
}

func TestSettlementRetriesWithBackoffUntilDelivered(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatJSON(w, "ok", 1, 1) }), nil)
	failSettle := atomic.Bool{}
	failSettle.Store(true)
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		if tool == "llm_settle" && failSettle.Load() {
			return nil, errors.New("billing down")
		}
		return map[string]any{"allow": true}, nil
	})
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"admission": billingHook})
	f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`)
	if n, _ := deliverOutbox(f.ctx, 0); n != 0 {
		t.Fatalf("delivered while billing was down")
	}
	rows, _ := dbOutboxList(f.ctx.AppDB(), "proj-test", "pending", 10)
	if len(rows) != 1 || fmt.Sprint(rows[0]["attempts"]) != "1" || !strings.Contains(fmt.Sprint(rows[0]["last_error"]), "billing down") {
		t.Fatalf("outbox after failure = %v", rows)
	}
	if n, _ := deliverOutbox(f.ctx, 0); n != 0 {
		t.Fatal("retry should wait for its backoff")
	}
	failSettle.Store(false)
	_, _ = f.ctx.AppDB().Exec(`UPDATE admission_outbox SET next_attempt_at=datetime('now','-1 second')`)
	if n, err := deliverOutbox(f.ctx, 0); n != 1 || err != nil {
		t.Fatalf("redelivery n=%d err=%v", n, err)
	}
}

func TestStaleReservationSweepFailsAndQueuesSettlement(t *testing.T) {
	f := newGatewayFixture(t, http.NotFoundHandler(), nil)
	db := f.ctx.AppDB()
	ident := &TokenIdentity{ID: f.tokID, ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "acme"}
	res, _, err := reserveUsageMeta(db, ident, "openai", "openai/m", 10, 0, "crashed-1", []*Policy{{}}, false, reservationMeta{Endpoint: endpointChat})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE usage_events SET admission_ref='hold-x', admission_json=?, created_at=datetime('now','-2 hours') WHERE id=?`, admissionJSON(&AdmissionConfig{App: "billing", AuthorizeTool: "a", SettleTool: "s"}), res.ID)
	if n, err := sweepStaleReservations(db); n != 1 || err != nil {
		t.Fatalf("sweep n=%d err=%v", n, err)
	}
	ev, _ := dbUsageEventByID(db, res.ID)
	if ev.Status != "failed" || !strings.Contains(string(ev.UsageDetails), `"abandoned":true`) {
		t.Fatalf("event = %+v %s", ev, ev.UsageDetails)
	}
	rows, _ := dbOutboxList(db, "proj-test", "pending", 10)
	if len(rows) != 1 {
		t.Fatalf("abandoned reservation must queue a settlement: %v", rows)
	}
	payload := rows[0]["payload"].(map[string]any)
	if payload["status"] != "failed" || payload["admission_ref"] != "hold-x" {
		t.Fatalf("payload = %v", payload)
	}
}

// ---- Tokens ----

func TestTokenLabelLast4MetadataFiltersAndUpdate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	db := ctx.AppDB()
	a, err := createToken(db, map[string]any{"project_id": "proj-test", "label": "Production server", "prefix": "rl_live_",
		"metadata": map[string]any{"account": "acct_42", "env": "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	tokA := a["token"].(string)
	if !strings.HasPrefix(tokA, "rl_live_") || a["last4"] != tokA[len(tokA)-4:] || a["display"] != "rl_live_…"+tokA[len(tokA)-4:] {
		t.Fatalf("created = %v", a)
	}
	if _, err := createToken(db, map[string]any{"project_id": "proj-test", "label": "Staging", "metadata": map[string]any{"account": "acct_7"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := createToken(db, map[string]any{"project_id": "proj-test", "prefix": "BAD"}); err == nil {
		t.Fatal("invalid prefix accepted")
	}
	f, _ := tokenFilterFromArgs(map[string]any{"project_id": "proj-test", "metadata": map[string]any{"account": "acct_42"}})
	rows, err := dbTokensQuery(db, f)
	if err != nil || len(rows) != 1 || rows[0].Label != "Production server" || rows[0].LastUsedAt != "" {
		t.Fatalf("metadata filter rows=%+v err=%v", rows, err)
	}
	f, _ = tokenFilterFromArgs(map[string]any{"project_id": "proj-test", "label": "stag"})
	if rows, _ := dbTokensQuery(db, f); len(rows) != 1 || rows[0].Label != "Staging" {
		t.Fatalf("label filter = %+v", rows)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tokA)
	ident, err := authenticateLLMToken(db, req)
	if err != nil || !strings.Contains(string(ident.Metadata), "acct_42") {
		t.Fatalf("auth ident=%+v err=%v", ident, err)
	}
	var first string
	_ = db.QueryRow(`SELECT COALESCE(last_used_at,'') FROM api_tokens WHERE id=?`, a["id"]).Scan(&first)
	if first == "" {
		t.Fatal("last_used_at not set on first use")
	}
	_, _ = db.Exec(`UPDATE api_tokens SET last_used_at=datetime('now','-10 seconds') WHERE id=?`, a["id"])
	var before string
	_ = db.QueryRow(`SELECT last_used_at FROM api_tokens WHERE id=?`, a["id"]).Scan(&before)
	_, _ = authenticateLLMToken(db, req)
	var after string
	_ = db.QueryRow(`SELECT last_used_at FROM api_tokens WHERE id=?`, a["id"]).Scan(&after)
	if before != after {
		t.Fatalf("last_used_at should be throttled: %s -> %s", before, after)
	}

	rec, err := updateToken(db, map[string]any{"project_id": "proj-test", "token_id": a["id"], "label": "Renamed", "admission": billingHook})
	if err != nil || rec.Label != "Renamed" || rec.Admission == nil || rec.Admission.App != "billing" || !strings.Contains(string(rec.Metadata), "acct_42") {
		t.Fatalf("update rec=%+v err=%v", rec, err)
	}
	rec, err = updateToken(db, map[string]any{"project_id": "proj-test", "token_id": a["id"], "admission": nil})
	if err != nil || rec.Admission != nil {
		t.Fatalf("admission should clear: %+v err=%v", rec, err)
	}
	if _, err := updateToken(db, map[string]any{"project_id": "proj-test", "token_id": a["id"], "admission": map[string]any{"app": "billing"}}); err == nil {
		t.Fatal("incomplete admission accepted")
	}
}

func TestUsageRecordsCarryRequestLogFields(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		chatJSON(w, "ok", 3, 1)
	}), map[string]any{"metadata": map[string]any{"account": "acct_42"}})
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	ev := f.lastEvent()
	if ev.LatencyMS < 5 || ev.TTFTMS < 5 || ev.Endpoint != "chat.completions" || ev.RequestedModel != "openai/m" || !strings.Contains(string(ev.TokenMetadata), "acct_42") || ev.TokenID != f.tokID {
		t.Fatalf("event = %+v", ev)
	}
	rec := f.do(http.MethodGet, "/v1/usage/events", "")
	var out struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Data) != 1 {
		t.Fatalf("usage events status=%d body=%s", rec.Code, rec.Body.String())
	}
	row := out.Data[0]
	md, _ := row["token_metadata"].(map[string]any)
	if row["endpoint"] != "chat.completions" || md["account"] != "acct_42" || row["schema_version"] != "2026-11-llm-usage-v4" {
		t.Fatalf("row = %v", row)
	}
	if _, ok := row["latency_ms"]; !ok {
		t.Fatalf("row lacks latency_ms: %v", row)
	}
}

// ---- Rate limits ----

func TestPerTokenRequestRateLimitReturns429WithHeaders(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatJSON(w, "ok", 1, 1) }), nil)
	_, _ = dbPolicySet(f.ctx.AppDB(), "proj-test", map[string]any{"limits": map[string]any{"token_requests_per_minute": 2}})
	body := `{"model":"openai/m","messages":[{"role":"user","content":"hi"}]}`
	first := f.do(http.MethodPost, "/v1/chat/completions", body)
	if first.Code != 200 || first.Header().Get("X-Ratelimit-Limit-Requests") != "2" || first.Header().Get("X-Ratelimit-Remaining-Requests") != "1" {
		t.Fatalf("first status=%d headers=%v", first.Code, first.Header())
	}
	f.do(http.MethodPost, "/v1/chat/completions", body)
	third := f.do(http.MethodPost, "/v1/chat/completions", body)
	if third.Code != 429 || !strings.Contains(third.Body.String(), "rate_limit_exceeded") || third.Header().Get("Retry-After") == "" || third.Header().Get("X-Ratelimit-Remaining-Requests") != "0" {
		t.Fatalf("third status=%d headers=%v body=%s", third.Code, third.Header(), third.Body.String())
	}
	usage, _ := dbUsageGet(f.ctx.AppDB(), usageFilter{ProjectID: "proj-test"})
	if usage.Requests != 2 {
		t.Fatalf("rate-limited request must not reserve usage: %+v", usage)
	}
}

func TestRateLimiterSlidingWindowAndTokenSettlement(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newRateLimiter()
	l.now = func() time.Time { return now }
	rules := []rateRule{{Key: "k", Scope: "subject", TPM: 100}}
	c1, _, err := l.Charge(rules, 80)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Charge(rules, 30); err == nil {
		t.Fatal("over the token budget")
	}
	c1.Settle(20) // the request actually used 20 tokens
	if _, _, err := l.Charge(rules, 30); err != nil {
		t.Fatalf("settled tokens should free capacity: %v", err)
	}
	if _, _, err := l.Charge(rules, 101); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("single request above the limit: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, _, err := l.Charge(rules, 100); err != nil {
		t.Fatalf("window should have slid: %v", err)
	}
	c, _, _ := l.Charge([]rateRule{{Key: "r", Scope: "token", RPM: 1}}, 0)
	c.Release()
	if _, _, err := l.Charge([]rateRule{{Key: "r", Scope: "token", RPM: 1}}, 0); err != nil {
		t.Fatalf("released request should not count: %v", err)
	}
}

// ---- Public models ----

func TestPublicModelRoutesWithFallbackAndPolicy(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, fmt.Sprint(body["model"]))
		mu.Unlock()
		if body["model"] == "fast-a" {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		chatJSON(w, "ok", 2, 1)
	}), nil)
	db := f.ctx.AppDB()
	if _, err := dbPublicModelUpsert(db, "proj-test", map[string]any{"name": "openai/bad", "routes": []any{map[string]any{"model": "openai/x"}}}); err == nil {
		t.Fatal("public names must not contain '/'")
	}
	if _, err := dbPublicModelUpsert(db, "proj-test", map[string]any{"name": "text-fast", "description": "Fast text",
		"routes": []any{map[string]any{"model": "openai/fast-a"}, map[string]any{"provider": "openai", "model": "fast-b"}}}); err != nil {
		t.Fatal(err)
	}
	_, _ = dbPolicySet(db, "proj-test", map[string]any{"allowed_models": []any{"text-fast"}})
	rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || strings.Join(seen, ",") != "fast-a,fast-b" {
		t.Fatalf("status=%d seen=%v body=%s", rec.Code, seen, rec.Body.String())
	}
	ev := f.lastEvent()
	if ev.RequestedModel != "text-fast" || ev.Model != "openai/fast-b" {
		t.Fatalf("event = %+v", ev)
	}
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/fast-a","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 403 {
		t.Fatalf("direct provider model should stay blocked by allowed_models: %d", rec.Code)
	}
	models := f.do(http.MethodGet, "/v1/models", "")
	if !strings.Contains(models.Body.String(), `"id":"text-fast"`) || strings.Contains(models.Body.String(), `"id":"openai/fast-a"`) {
		t.Fatalf("models = %s", models.Body.String())
	}
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 400 {
		t.Fatalf("unknown public name: %d", rec.Code)
	}
	_, _ = dbPublicModelUpsert(db, "proj-test", map[string]any{"name": "text-fast", "enabled": false, "routes": []any{map[string]any{"model": "openai/fast-b"}}})
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 400 {
		t.Fatalf("disabled public model should not resolve: %d", rec.Code)
	}
}

// ---- Audio and images ----

func TestAudioAndImageEndpointsMeterUnits(t *testing.T) {
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio/speech":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "tts" {
				t.Errorf("speech model = %v", body["model"])
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("ID3fakeaudio"))
		case "/audio/transcriptions":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			file, _, err := r.FormFile("file")
			if err != nil || r.FormValue("model") != "voxtral" || r.FormValue("language") != "fr" {
				t.Errorf("transcription form model=%q lang=%q err=%v", r.FormValue("model"), r.FormValue("language"), err)
			} else {
				b, _ := io.ReadAll(file)
				if string(b) != "RIFFwav" {
					t.Errorf("file = %q", b)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"text":"bonjour","usage":{"type":"duration","seconds":3}}`)
		case "/images/generations":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"b64_json":"a"},{"b64_json":"b"}],"usage":{"input_tokens":9,"output_tokens":100}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}), nil)

	rec := f.do(http.MethodPost, "/v1/audio/speech", `{"model":"openai/tts","input":"hello there","voice":"alloy"}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/mpeg" || rec.Body.String() != "ID3fakeaudio" {
		t.Fatalf("speech status=%d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	if ev := f.lastEvent(); ev.Endpoint != "audio.speech" || !strings.Contains(string(ev.UsageDetails), `"characters":11`) {
		t.Fatalf("speech usage = %+v %s", ev, ev.UsageDetails)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "openai/voxtral")
	_ = mw.WriteField("language", "fr")
	part, _ := mw.CreateFormFile("file", "a.wav")
	part.Write([]byte("RIFFwav"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	trec := httptest.NewRecorder()
	f.app.handleV1(trec, req)
	if trec.Code != 200 || !strings.Contains(trec.Body.String(), "bonjour") {
		t.Fatalf("transcription status=%d body=%s", trec.Code, trec.Body.String())
	}
	if ev := f.lastEvent(); ev.Endpoint != "audio.transcriptions" || !strings.Contains(string(ev.UsageDetails), `"audio_seconds":3`) {
		t.Fatalf("transcription usage = %+v %s", ev, ev.UsageDetails)
	}

	rec = f.do(http.MethodPost, "/v1/images/generations", `{"model":"openai/img","prompt":"a cat","n":2,"size":"1024x1024"}`)
	if rec.Code != 200 {
		t.Fatalf("images status=%d body=%s", rec.Code, rec.Body.String())
	}
	ev := f.lastEvent()
	if ev.Endpoint != "images.generations" || ev.RequestTokens != 9 || ev.ResponseTokens != 100 || !strings.Contains(string(ev.UsageDetails), `"images":2`) {
		t.Fatalf("images usage = %+v %s", ev, ev.UsageDetails)
	}

	limited := newGatewayFixture(t, http.NotFoundHandler(), map[string]any{"scopes": []any{"chat"}})
	if rec := limited.do(http.MethodPost, "/v1/images/generations", `{"model":"openai/img","prompt":"x"}`); rec.Code != 403 {
		t.Fatalf("images scope should be required: %d", rec.Code)
	}
}

func TestAdmissionConfigValidation(t *testing.T) {
	if _, err := admissionFromAny(map[string]any{"app": "b", "authorize_tool": "a", "settle_tool": "s", "fail_mode": "maybe"}); err == nil {
		t.Fatal("bad fail_mode accepted")
	}
	if _, err := admissionFromAny(map[string]any{"app": "b", "authorize_tool": "a", "settle_tool": "s", "timeout_ms": 60000}); err == nil {
		t.Fatal("timeout above the cap accepted")
	}
	cfg, err := admissionFromAny(map[string]any{"app": "b", "authorize_tool": "a", "settle_tool": "s"})
	if err != nil || cfg.FailMode != "closed" || cfg.timeout() != defaultAdmissionTimeout {
		t.Fatalf("defaults cfg=%+v err=%v", cfg, err)
	}
	if cfg, err := admissionFromAny(nil); cfg != nil || err != nil {
		t.Fatal("nil should clear")
	}
}

func TestPolicyWriteWithoutAdmissionKeepsExistingHook(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	db := ctx.AppDB()
	if _, err := dbPolicySet(db, "proj-test", map[string]any{"admission": billingHook}); err != nil {
		t.Fatal(err)
	}
	pol, err := dbPolicySet(db, "proj-test", map[string]any{"limits": map[string]any{"requests_per_minute": 10}})
	if err != nil || pol.Admission == nil || pol.Admission.App != "billing" || pol.Limits.RequestsPerMinute != 10 {
		t.Fatalf("omitting admission must keep the hook: %+v err=%v", pol, err)
	}
	pol, err = dbPolicySet(db, "proj-test", map[string]any{"admission": nil})
	if err != nil || pol.Admission != nil {
		t.Fatalf("explicit null should clear the hook: %+v err=%v", pol, err)
	}
	if _, err := dbPolicySet(db, "proj-test", map[string]any{"admission": billingHook}); err != nil {
		t.Fatal(err)
	}
	pol, _ = dbPolicySetDisabled(db, "proj-test", "tenant", "acme", true)
	project, _ := dbPolicyGet(db, "proj-test", "", "")
	if !pol.Disabled || project.Admission == nil {
		t.Fatalf("suspending a subject must not touch the project hook: subject=%+v project=%+v", pol, project)
	}
}
