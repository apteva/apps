package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

var customerHook = map[string]any{"app": "saas", "authorize_tool": "saas_usage_authorize", "settle_tool": "saas_usage_settle"}

// customerFixture: the fixture token is a customer key (its own hook); the
// provider answers as fast-b behind the public name text-fast.
func customerFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	f := newGatewayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			io.WriteString(w, `{"model":"fast-e","data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":2}}`)
			return
		}
		chatJSON(w, "ok", 5, 2)
	}), map[string]any{"admission": customerHook})
	db := f.ctx.AppDB()
	if _, err := dbPublicModelUpsert(db, "proj-test", map[string]any{"name": "text-fast", "routes": []any{map[string]any{"model": "openai/fast-b"}}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *gatewayFixture) internalToken() string {
	f.t.Helper()
	tok, err := createToken(f.ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "app", "subject_id": "ops", "scopes": []any{"*"}})
	if err != nil {
		f.t.Fatal(err)
	}
	return tok["token"].(string)
}

func TestCustomerKeysOnlySeeAndCallPublicModels(t *testing.T) {
	f := customerFixture(t)
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		return map[string]any{"allow": true, "admission_ref": "hold-1"}, nil
	})
	db := f.ctx.AppDB()
	if err := dbProviderModelsReplace(db, "proj-test", "openai", []ProviderModel{{ModelID: "fast-b"}}); err != nil {
		t.Fatalf("seed provider model: %v", err)
	}

	models := f.do(http.MethodGet, "/v1/models", "").Body.String()
	if !strings.Contains(models, `"id":"text-fast"`) || strings.Contains(models, "openai/") {
		t.Fatalf("customer /v1/models = %s", models)
	}
	for _, req := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"openai/fast-b","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"openai/fast-b","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"openai/fast-b","input":"hi"}`},
		{"/v1/embeddings", `{"model":"openai/fast-e","input":"hi"}`},
	} {
		rec := f.do(http.MethodPost, req.path, req.body)
		var body struct {
			Error struct{ Type, Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 404 || body.Error.Code != "model_not_found" || body.Error.Type != "invalid_request_error" {
			t.Fatalf("%s %s: status=%d body=%s", req.path, req.body, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("public model: %d %s", rec.Code, rec.Body.String())
	}

	// A token without its own hook keeps full access, even with a policy hook.
	_, _ = dbPolicySet(db, "proj-test", map[string]any{"admission": billingHook})
	f.token = f.internalToken()
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"openai/fast-b","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("internal token provider model: %d %s", rec.Code, rec.Body.String())
	}
	if models := f.do(http.MethodGet, "/v1/models", "").Body.String(); !strings.Contains(models, `"id":"openai/fast-b"`) {
		t.Fatalf("internal /v1/models = %s", models)
	}
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 400 {
		t.Fatalf("internal unknown name keeps 400: %d", rec.Code)
	}
}

func TestCustomerUsageHidesProviderAndShowsCharge(t *testing.T) {
	f := customerFixture(t)
	installHooks(t, func(app, tool string, input map[string]any) (any, error) {
		if tool == "saas_usage_settle" {
			return map[string]any{"settled": true, "amount": 1200, "committed": 1234}, nil
		}
		return map[string]any{"allow": true, "admission_ref": "hold-1"}, nil
	})
	if rec := f.do(http.MethodPost, "/v1/chat/completions", `{"model":"text-fast","messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	hidden := []string{`"provider`, "openai", "fast-b", "token_metadata", "raw_usage", "attempts"}
	readEvents := func() map[string]any {
		t.Helper()
		raw := f.do(http.MethodGet, "/v1/usage/events", "").Body.String()
		for _, h := range hidden {
			if strings.Contains(raw, h) {
				t.Fatalf("usage events leak %q: %s", h, raw)
			}
		}
		var list struct{ Data []map[string]any }
		_ = json.Unmarshal([]byte(raw), &list)
		if len(list.Data) != 1 {
			t.Fatalf("events = %s", raw)
		}
		return list.Data[0]
	}
	ev := readEvents()
	if ev["model"] != "text-fast" || ev["cost_status"] != "pending" || ev["cost_microunits"] != nil {
		t.Fatalf("before settlement: %v", ev)
	}
	if n, err := deliverOutbox(f.ctx, 0); n != 1 || err != nil {
		t.Fatalf("deliver n=%d err=%v", n, err)
	}
	ev = readEvents()
	if ev["cost_status"] != "settled" || ev["cost_microunits"] != float64(1234) {
		t.Fatalf("after settlement: %v", ev)
	}
	totalsRaw := f.do(http.MethodGet, "/v1/usage", "").Body.String()
	var totals map[string]any
	_ = json.Unmarshal([]byte(totalsRaw), &totals)
	if strings.Contains(totalsRaw, "provider") || totals["cost_microunits"] != float64(1234) || totals["requests"] != float64(1) || totals["settled_requests"] != float64(1) {
		t.Fatalf("totals = %s", totalsRaw)
	}
	if raw := f.do(http.MethodGet, "/v1/usage/events?model=openai/fast-b", "").Body.String(); strings.Contains(raw, "text-fast") {
		t.Fatalf("customer must not match provider model ids: %s", raw)
	}
	if raw := f.do(http.MethodGet, "/v1/usage?model=text-fast", "").Body.String(); !strings.Contains(raw, `"requests":1`) {
		t.Fatalf("filter by public name: %s", raw)
	}
	// Internal views keep the provider and also show the charge.
	internal := usageEventPayload(f.lastEvent(), nil)
	if internal["provider"] != "openai" || internal["charged_microunits"] != int64(1234) || internal["charge_status"] != "settled" {
		t.Fatalf("internal payload = %v", internal)
	}
}

func TestSettledChargeReadsSettleReplies(t *testing.T) {
	cases := []struct {
		reply  any
		amount int64
		ok     bool
	}{
		{map[string]any{"committed": float64(50), "amount": float64(40)}, 50, true},
		{map[string]any{"charged_microunits": float64(7)}, 7, true},
		{map[string]any{"settled": true, "released": true}, 0, true},
		{map[string]any{"settled": false, "skipped": "no_admission_ref"}, 0, true},
		{map[string]any{"settled": true}, 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		if amount, ok := settledCharge(c.reply); amount != c.amount || ok != c.ok {
			t.Fatalf("%v: got %d %v", c.reply, amount, ok)
		}
	}
}
