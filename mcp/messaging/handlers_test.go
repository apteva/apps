package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Stub PlatformClient ──────────────────────────────────────────

// stubPlatform records every ExecuteIntegrationTool / CallApp call
// and returns a canned response. Only the methods we actually use in
// tests are non-nil; everything else panics so failures are loud.
type stubPlatform struct {
	tk.BasePlatformClient
	mu               sync.Mutex
	executeCalls     []executeCall
	callAppCalls     []callAppCall
	executeReply     *sdk.ExecuteResult
	replyByTool      map[string]*sdk.ExecuteResult
	executeErr       error
	callAppReply     json.RawMessage
	callAppErr       error
	connectionCreds  map[int64]map[string]string
	bindingsOverride map[string]any       // when non-nil, replaces the default email_provider binding
	whoAmIOverride   *sdk.InstallIdentity // when non-nil, replaces the default identity
	// executeOverride: when non-nil, called per ExecuteIntegrationTool
	// invocation BEFORE replyByTool / defaults. The int is the
	// 0-indexed count of prior calls for that tool — lets a test
	// switch behaviour between calls (e.g., first AlreadyExists,
	// second success). Return nil to fall through to the normal stub
	// behaviour.
	executeOverride func(tool string, priorCalls int) *sdk.ExecuteResult
	toolCallCounts  map[string]int
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func (s *stubPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.mu.Lock()
	s.executeCalls = append(s.executeCalls, executeCall{ConnID: connID, Tool: tool, Input: input})
	priorCalls := 0
	if s.toolCallCounts == nil {
		s.toolCallCounts = map[string]int{}
	}
	priorCalls = s.toolCallCounts[tool]
	s.toolCallCounts[tool] = priorCalls + 1
	s.mu.Unlock()
	if s.executeErr != nil {
		return nil, s.executeErr
	}
	// Per-call override wins (lets a test switch behaviour between
	// successive calls for the same tool, e.g. first AlreadyExists,
	// second success).
	if s.executeOverride != nil {
		if r := s.executeOverride(tool, priorCalls); r != nil {
			return r, nil
		}
	}
	// Per-tool reply override wins; otherwise fall back to a sane default.
	if s.replyByTool != nil {
		if r, ok := s.replyByTool[tool]; ok {
			return r, nil
		}
	}
	if s.executeReply != nil {
		return s.executeReply, nil
	}
	switch tool {
	case "send_email":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"MessageId":"ses-msg-123"}`)}, nil
	case "send_raw_email":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"MessageId":"ses-raw-123"}`)}, nil
	case "list_identities":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"EmailIdentities":[],"NextToken":""}`)}, nil
	case "get_quota":
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"SendQuota":{"Max24HourSend":200,"MaxSendRate":1,"SentLast24Hours":0},"SendingEnabled":true,"ProductionAccessEnabled":false,"EnforcementStatus":"HEALTHY"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
}

func (s *stubPlatform) CallApp(app, tool string, input map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	s.callAppCalls = append(s.callAppCalls, callAppCall{App: app, Tool: tool, Input: input})
	s.mu.Unlock()
	if s.callAppErr != nil {
		return nil, s.callAppErr
	}
	return s.callAppReply, nil
}

func (s *stubPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	raw, err := s.CallApp(app, tool, input)
	if err != nil {
		return err
	}
	if len(raw) == 0 || out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Unused PlatformClient methods — return zero values; tests that hit
// them would panic, which is the intended signal.
func (s *stubPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "aws-ses", Status: "active"}, nil
}
func (s *stubPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	if s.connectionCreds == nil {
		return nil, tk.ErrNotImplemented
	}
	fields, ok := s.connectionCreds[id]
	if !ok {
		return nil, tk.ErrNotImplemented
	}
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "twilio", Fields: fields}, nil
}
func (s *stubPlatform) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (s *stubPlatform) GetInstance(int64) (*sdk.PlatformInstance, error) { return nil, nil }
func (s *stubPlatform) SendEvent(int64, string) error                    { return nil }
func (s *stubPlatform) SendToChannel(string, string, string) error       { return nil }
func (s *stubPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	// Provide a binding for the email_provider role so IntegrationFor returns non-nil.
	bindings := map[string]any{"email_provider": float64(1)}
	if s.bindingsOverride != nil {
		bindings = s.bindingsOverride
	}
	if s.whoAmIOverride != nil {
		// Caller-provided identity wins, but merge bindings if the
		// override didn't set them (most tests only override
		// InstallID + AppName).
		id := *s.whoAmIOverride
		if id.Bindings == nil {
			id.Bindings = bindings
		}
		return &id, nil
	}
	return &sdk.InstallIdentity{
		AppName:   "messaging",
		ProjectID: "test-proj",
		Bindings:  bindings,
	}, nil
}

// PlatformClient methods added in v0.1.3+ (StartOAuth, Disconnect,
// ListOwnedConnections, GetGrants). Stubs return zero values; tests
// don't exercise these paths.
func (s *stubPlatform) StartOAuth(sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return &sdk.OAuthStartResult{}, nil
}
func (s *stubPlatform) DisconnectConnection(int64) error { return nil }
func (s *stubPlatform) ListOwnedConnections() ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (s *stubPlatform) GetGrants(int64) (*sdk.GrantsResponse, error) {
	return &sdk.GrantsResponse{DefaultEffect: "allow"}, nil
}

// ─── Test harness ─────────────────────────────────────────────────

func newTestCtx(t *testing.T, plat *stubPlatform, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{
		tk.WithProjectID("test-proj"),
	}, opts...)
	if plat != nil {
		full = append(full, tk.WithPlatform(plat))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", full...)
	globalCtx = ctx
	return ctx
}

// fromAcme is a stable test sender to keep send_message calls terse.
const fromAcme = "notifications@acme.com"

// ─── send_message ─────────────────────────────────────────────────

func TestSendMessage_PersistsAndCallsProvider(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"from":    fromAcme,
		"to":      "alice@example.com",
		"subject": "hello",
		"body":    "hi there",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["status"] != "sent" {
		t.Fatalf("status=%v, want sent", r["status"])
	}
	if r["channel"] != "email" {
		t.Errorf("channel=%v", r["channel"])
	}
	if r["provider_message_id"] != "ses-msg-123" {
		t.Errorf("provider_message_id=%v", r["provider_message_id"])
	}
	if len(plat.executeCalls) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(plat.executeCalls))
	}
	call := plat.executeCalls[0]
	if call.Tool != "send_email" {
		t.Errorf("tool=%q", call.Tool)
	}
	if call.Input["FromEmailAddress"] != "notifications@acme.com" {
		t.Errorf("FromEmailAddress=%v", call.Input["FromEmailAddress"])
	}
	dest := call.Input["Destination"].(map[string]any)
	to := dest["ToAddresses"].([]string)
	if len(to) != 1 || to[0] != "alice@example.com" {
		t.Errorf("ToAddresses=%v", to)
	}
	content := call.Input["Content"].(map[string]any)
	simple := content["Simple"].(map[string]any)
	if simple["Subject"].(map[string]any)["Data"] != "hello" {
		t.Errorf("Subject.Data=%v", simple["Subject"])
	}
	if simple["Body"].(map[string]any)["Text"].(map[string]any)["Data"] != "hi there" {
		t.Errorf("Body.Text.Data=%v", simple["Body"])
	}
	if simple["Body"].(map[string]any)["Html"].(map[string]any)["Data"] != "<!doctype html><html><body>hi there</body></html>" {
		t.Errorf("Body.Html.Data=%v", simple["Body"])
	}
}

func TestTextBodyToTrackingHTML_EscapesText(t *testing.T) {
	got := textBodyToTrackingHTML("hi <there>\r\nnext & last")
	want := "<!doctype html><html><body>hi &lt;there&gt;<br>\nnext &amp; last</body></html>"
	if got != want {
		t.Fatalf("html=%q, want %q", got, want)
	}
}

func TestSendMessage_EmailReplyUsesRawMIMEHeaders(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendMessage(ctx, map[string]any{
		"channel":     "email",
		"from":        fromAcme,
		"to":          "alice@example.com",
		"subject":     "Re: hello",
		"body":        "reply body",
		"in_reply_to": "<orig@example.com>",
		"references":  []any{"<parent@example.com>", "<orig@example.com>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["provider_message_id"] != "ses-raw-123" {
		t.Fatalf("provider_message_id=%v, want ses-raw-123", r["provider_message_id"])
	}
	if len(plat.executeCalls) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(plat.executeCalls))
	}
	call := plat.executeCalls[0]
	if call.Tool != "send_raw_email" {
		t.Fatalf("tool=%q, want send_raw_email", call.Tool)
	}
	content := call.Input["Content"].(map[string]any)
	raw := content["Raw"].(map[string]any)
	data, err := base64.StdEncoding.DecodeString(raw["Data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	mimeBody := string(data)
	for _, want := range []string{
		"In-Reply-To: <orig@example.com>",
		"References: <parent@example.com> <orig@example.com>",
		"Subject: Re: hello",
	} {
		if !strings.Contains(mimeBody, want) {
			t.Fatalf("raw MIME missing %q:\n%s", want, mimeBody)
		}
	}

	msg, err := dbMessageGet(ctx.AppDB(), "test-proj", r["id"].(int64))
	if err != nil {
		t.Fatal(err)
	}
	if msg.InReplyTo != "<orig@example.com>" {
		t.Errorf("InReplyTo=%q", msg.InReplyTo)
	}
	if len(msg.References) != 2 || msg.References[0] != "<parent@example.com>" || msg.References[1] != "<orig@example.com>" {
		t.Errorf("References=%v", msg.References)
	}
}

func TestSendMessage_RequiresBodyOrTemplate(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"from":    fromAcme,
		"to":      "alice@example.com",
		"subject": "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("expected body-required error, got %v", err)
	}
}

func TestSendMessage_RequiresFrom(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"to":      "alice@example.com",
		"body":    "hi",
	})
	if err == nil || !strings.Contains(err.Error(), "from: required") {
		t.Errorf("expected from-required error, got %v", err)
	}
	if len(plat.executeCalls) != 0 {
		t.Errorf("provider should not have been called")
	}
}

func TestSendMessage_RequiresChannel(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"from": fromAcme,
		"to":   "alice@example.com",
		"body": "hi",
	})
	if err == nil || !strings.Contains(err.Error(), "channel: required") {
		t.Errorf("expected channel-required error, got %v", err)
	}
}

func TestSendMessage_PhoneProviderNotBound(t *testing.T) {
	plat := &stubPlatform{} // default bindings expose only email_provider
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "sms",
		"from":    "+15551112222",
		"to":      "+15553334444",
		"body":    "hi",
	})
	if err != nil {
		t.Fatalf("send_message returned go error %v (expected the failure to surface in the persisted row)", err)
	}
	// Row persisted as failed; no provider call recorded.
	if len(plat.executeCalls) != 0 {
		t.Errorf("expected zero provider calls (no phone_provider bound), got %d", len(plat.executeCalls))
	}
}

func TestSendMessage_Idempotency(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	args := map[string]any{
		"channel":         "email",
		"from":            fromAcme,
		"to":              "bob@example.com",
		"body":            "yo",
		"idempotency_key": "abc-123",
	}
	out1, err := app.toolSendMessage(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := app.toolSendMessage(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out1.(map[string]any)["id"] != out2.(map[string]any)["id"] {
		t.Errorf("idempotent calls returned different ids: %v vs %v", out1, out2)
	}
	if len(plat.executeCalls) != 1 {
		t.Errorf("expected provider called once, got %d", len(plat.executeCalls))
	}
}

func TestSendMessage_RespectsSuppression(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	if err := dbSuppressionUpsert(ctx.AppDB(), "test-proj", "email", "bad@example.com", "hard-bounce", "auto"); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"from":    fromAcme,
		"to":      "bad@example.com",
		"body":    "you'll never see this",
	})
	if err == nil {
		t.Fatal("expected suppression error")
	}
	if !strings.Contains(err.Error(), "suppressed") {
		t.Errorf("error %v should mention suppression", err)
	}
	if len(plat.executeCalls) != 0 {
		t.Errorf("provider should not have been called")
	}
}

func TestSendMessage_ProviderErrorMarksFailed(t *testing.T) {
	plat := &stubPlatform{
		executeReply: &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"error":"boom"}`)},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"from":    fromAcme,
		"to":      "carol@example.com",
		"body":    "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["status"] != "failed" {
		t.Errorf("status=%v, want failed", r["status"])
	}
	if !strings.Contains(r["status_reason"].(string), "non-2xx") {
		t.Errorf("status_reason=%v", r["status_reason"])
	}
}

// ─── templates ────────────────────────────────────────────────────

func TestTemplate_CreateGetUpdateList(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}

	out, err := app.toolTemplateCreate(ctx, map[string]any{
		"name":      "welcome",
		"subject":   "Welcome {{name}}",
		"body_text": "Hi {{name}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := out.(map[string]any)["template"].(*Template)
	if tpl.Name != "welcome" || tpl.Channel != "email" {
		t.Errorf("template=%+v", tpl)
	}

	updated, err := app.toolTemplateUpdate(ctx, map[string]any{
		"id":      tpl.ID,
		"subject": "Welcome back {{name}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.(map[string]any)["template"].(*Template).Subject != "Welcome back {{name}}" {
		t.Error("update did not persist")
	}

	listOut, err := app.toolTemplateList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if listOut.(map[string]any)["count"].(int) != 1 {
		t.Errorf("expected 1 template, got %v", listOut)
	}
}

func TestSendMessage_TemplateRender(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	createOut, err := app.toolTemplateCreate(ctx, map[string]any{
		"name":      "ping",
		"subject":   "Hello {{name}}",
		"body_text": "Hi {{name}}, code = {{code}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	tplID := createOut.(map[string]any)["template"].(*Template).ID

	_, err = app.toolSendMessage(ctx, map[string]any{
		"channel":     "email",
		"from":        fromAcme,
		"to":          "user@example.com",
		"template_id": tplID,
		"vars":        map[string]any{"name": "Alice", "code": "X-42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plat.executeCalls) != 1 {
		t.Fatal("no provider call")
	}
	call := plat.executeCalls[0]
	simple := call.Input["Content"].(map[string]any)["Simple"].(map[string]any)
	if simple["Subject"].(map[string]any)["Data"] != "Hello Alice" {
		t.Errorf("subject=%v", simple["Subject"])
	}
	if simple["Body"].(map[string]any)["Text"].(map[string]any)["Data"] != "Hi Alice, code = X-42" {
		t.Errorf("body=%v", simple["Body"])
	}
}

// ─── inbound routes ───────────────────────────────────────────────

func TestInboundRoute_SetIdempotent(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}
	args := map[string]any{
		"pattern":      "support+*@acme.com",
		"target_app":   "support",
		"target_route": "/inbound",
	}
	out1, err := app.toolInboundRouteSet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := app.toolInboundRouteSet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	id1 := out1.(map[string]any)["route"].(*InboundRoute).ID
	id2 := out2.(map[string]any)["route"].(*InboundRoute).ID
	if id1 != id2 {
		t.Errorf("expected idempotent, got ids %d vs %d", id1, id2)
	}
}

// ─── suppression ──────────────────────────────────────────────────

func TestSuppression_AddRemove(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}
	if _, err := app.toolSuppressionAdd(ctx, map[string]any{
		"address": "blocked@x.com",
		"reason":  "manual",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolSuppressionList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", out)
	}
	if _, err := app.toolSuppressionRemove(ctx, map[string]any{"address": "blocked@x.com"}); err != nil {
		t.Fatal(err)
	}
	out, _ = app.toolSuppressionList(ctx, map[string]any{})
	if out.(map[string]any)["count"].(int) != 0 {
		t.Errorf("expected 0 after remove, got %v", out)
	}
}

// ─── senders ──────────────────────────────────────────────────────

// preseedSender writes a row directly via dbUpsertSender so list/refresh
// tests don't have to round-trip through senders_create (which also
// calls upstream verify_email / verify_domain).
func preseedSender(t *testing.T, ctx *sdk.AppCtx, u senderUpsert) {
	t.Helper()
	u.ProjectID = "test-proj"
	u.MarkSyncedNow = true
	if _, err := dbUpsertSender(ctx.AppDB(), &u); err != nil {
		t.Fatalf("preseed %s: %v", u.Address, err)
	}
}

// preseedIdentity writes an anchor row directly via dbUpsertIdentity.
// Used by tests that need a verified domain (or future WABA) in
// place — these live in identities, not senders, after v0.12.
// Returns the row id so callers can wire FK references.
func preseedIdentity(t *testing.T, ctx *sdk.AppCtx, u identityUpsert) int64 {
	t.Helper()
	u.ProjectID = "test-proj"
	u.MarkSyncedNow = true
	id, err := dbUpsertIdentity(ctx.AppDB(), &u)
	if err != nil {
		t.Fatalf("preseed identity %s: %v", u.Address, err)
	}
	return id
}

func TestSenders_List_NormalisesShape(t *testing.T) {
	// v0.12: senders_list returns sendable rows only. The DKIM
	// anchor (acme.com) lives in identities now, not senders — it's
	// preseeded here to prove it does NOT leak into the senders list.
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "notifications@acme.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "notifications@acme.com",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})
	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "acme.com",
		Provider: "aws-ses", ProviderIdentityID: "acme.com",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "pending@acme.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "pending@acme.com",
		Verified: false, VerificationStatus: "pending", SendingEnabled: false,
	})

	out, err := app.toolSendersList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	// Only mailboxes appear; the domain anchor stays out.
	if r["count"].(int) != 2 {
		t.Errorf("expected 2 mailboxes, got %v (anchor leaking?)", r["count"])
	}
	rows := r["senders"].([]map[string]any)
	addresses := []string{}
	for _, row := range rows {
		addresses = append(addresses, row["address"].(string))
	}
	wantAddrs := []string{"notifications@acme.com", "pending@acme.com"}
	for i, want := range wantAddrs {
		if i >= len(addresses) || addresses[i] != want {
			t.Errorf("row %d: addr=%q, want %q", i, addresses[i], want)
		}
	}
	for _, row := range rows {
		if row["address"] == "pending@acme.com" && row["verified"] != false {
			t.Errorf("pending row should not be verified: %+v", row)
		}
		if row["kind"] == "email_domain" {
			t.Errorf("domain anchor leaked into senders_list: %+v", row)
		}
	}
}

func TestSenders_List_VerifiedOnly(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "good@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "good@x.com",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "pending@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "pending@x.com",
		Verified: false, VerificationStatus: "pending", SendingEnabled: false,
	})

	out, err := app.toolSendersList(ctx, map[string]any{"verified_only": true})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("verified_only filter broken: %+v", r)
	}
	rows := r["senders"].([]map[string]any)
	if len(rows) != 1 || rows[0]["address"] != "good@x.com" {
		t.Errorf("unexpected rows: %+v", rows)
	}
}

// v0.10 guarantee: empty local table stays empty even when SES has
// identities. Operators add senders explicitly via senders_create.
func TestSenders_List_DoesNotImportFromUpstream(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(`{
				"EmailIdentities":[
					{"IdentityName":"leftover@x.com","IdentityType":"EMAIL_ADDRESS","SendingEnabled":true,"VerificationStatus":"SUCCESS"},
					{"IdentityName":"old-test.com","IdentityType":"DOMAIN","SendingEnabled":true,"VerificationStatus":"SUCCESS"}
				]
			}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendersList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["count"].(int) != 0 {
		t.Errorf("empty local table should stay empty; got %v", r)
	}
	// list_identities must not have been called either — with zero
	// known rows, the refresh short-circuits before hitting SES.
	for _, c := range plat.executeCalls {
		if c.Tool == "list_identities" {
			t.Errorf("expected zero list_identities calls on empty-local refresh, got %+v", c)
		}
	}
}

// Refresh updates known rows but never inserts unknowns.
func TestSendersRefresh_UpdatesKnownButIgnoresUnknown(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(`{
				"EmailIdentities":[
					{"IdentityName":"known@x.com","IdentityType":"EMAIL_ADDRESS","SendingEnabled":true,"VerificationStatus":"SUCCESS"},
					{"IdentityName":"unknown@x.com","IdentityType":"EMAIL_ADDRESS","SendingEnabled":true,"VerificationStatus":"SUCCESS"}
				]
			}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Pre-seed the row that's about to flip from pending → verified
	// at SES. Status starts out stale locally.
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "known@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "known@x.com",
		Verified: false, VerificationStatus: "pending", SendingEnabled: false,
	})

	if _, err := app.toolSendersRefresh(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}

	out, _ := app.toolSendersList(ctx, map[string]any{})
	rows := out.(map[string]any)["senders"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("refresh imported unknown row: %+v", rows)
	}
	if rows[0]["address"] != "known@x.com" || rows[0]["verified"] != true {
		t.Errorf("known row should be refreshed to verified=true, got %+v", rows[0])
	}
}

// v0.11.2 regression: mailbox rows inherit DKIM from their parent and
// are deliberately NOT created as SES identities — so list_identities
// won't return them. The refresh's "missing upstream → soft-delete"
// pass must skip these inherited rows; otherwise every panel reload
// silently wipes them.
func TestSendersRefresh_PreservesMailboxesInheritingFromVerifiedParent(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(`{
				"EmailIdentities":[
					{"IdentityName":"socialcast.dev","IdentityType":"DOMAIN","SendingEnabled":true,"VerificationStatus":"SUCCESS"}
				]
			}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Pre-seed: a verified parent identity + an inheritance-mailbox
	// row pointing at it via the FK. Exactly the shape
	// sendersCreateEmailViaParentDomain produces under v0.12.
	parentID := preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "socialcast.dev",
		Provider: "aws-ses", ProviderIdentityID: "socialcast.dev",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "test@socialcast.dev", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "test@socialcast.dev",
		Verified: true, VerificationStatus: "verified",
		SendingEnabled: true, DkimStatus: "SUCCESS",
		ParentIdentityID: parentID,
	})

	if _, err := app.toolSendersRefresh(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}

	out, _ := app.toolSendersList(ctx, map[string]any{})
	r := out.(map[string]any)
	// Only the mailbox shows up in senders_list (anchor lives in
	// identities now). Surviving the refresh is what matters.
	if r["count"].(int) != 1 {
		t.Errorf("expected mailbox to survive in senders_list, got %v", r)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "test@socialcast.dev")
	if row == nil || row.DeletedAt != nil {
		t.Errorf("inherited mailbox row was soft-deleted: %+v", row)
	}
}

// v0.11.3 regression: the parent domain may never get persisted
// locally (e.g., sendersCreateDomain returned early on a midway
// bootstrap failure, before reaching persistSenderRow). The refresh
// must still treat the inheritance mailbox as alive when SES's
// list_identities reports the parent — the prior fix only checked
// LOCAL rows for the parent, which silently wiped mailboxes in that
// scenario.
func TestSendersRefresh_PreservesMailboxWhenParentOnlyExistsUpstream(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(`{
				"EmailIdentities":[
					{"IdentityName":"socialcast.dev","IdentityType":"DOMAIN","SendingEnabled":true,"VerificationStatus":"SUCCESS"}
				]
			}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Mailbox row only — NO local kind=domain row for socialcast.dev.
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "test@socialcast.dev", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "test@socialcast.dev",
		Verified: true, VerificationStatus: "verified",
		SendingEnabled: true, DkimStatus: "SUCCESS",
	})

	if _, err := app.toolSendersRefresh(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "test@socialcast.dev")
	if row == nil || row.DeletedAt != nil {
		t.Errorf("mailbox should survive when parent is in list_identities, got %+v", row)
	}
}

// Companion: mailbox rows whose parent ISN'T verified locally still
// get soft-deleted when missing upstream — they're real SES identities
// that the operator must've removed via the console, and the refresh
// should still reflect that.
func TestSendersRefresh_SoftDeletesStandaloneMailboxesMissingUpstream(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(`{"EmailIdentities":[]}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Pre-seed only the mailbox — no parent-domain row exists locally,
	// so this is a standalone SES identity, not an inheritance row.
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "lonely@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "lonely@x.com",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendersRefresh(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolSendersList(ctx, map[string]any{})
	if out.(map[string]any)["count"].(int) != 0 {
		t.Errorf("standalone mailbox missing upstream should be soft-deleted, got %+v", out)
	}
}

// If the known row vanishes from SES, refresh soft-deletes it locally.
func TestSendersRefresh_SoftDeletesRowsMissingUpstream(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(`{"EmailIdentities":[]}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "gone@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "gone@x.com",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendersRefresh(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolSendersList(ctx, map[string]any{})
	if out.(map[string]any)["count"].(int) != 0 {
		t.Errorf("row missing upstream should be soft-deleted, got %+v", out)
	}
}

func TestSendersCreate_DispatchesByShape(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_email":  {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["aaa","bbb","ccc"],"Status":"PENDING"}}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// email → verify_email. Inbound branch never runs.
	emailOutRaw, err := app.toolSendersCreate(ctx, map[string]any{"address": "new@acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	emailOut := emailOutRaw.(*sendersCreateResp)
	if emailOut.Kind != "email" {
		t.Errorf("email kind=%q", emailOut.Kind)
	}
	if !hasStep(emailOut.Steps, "ses_verify_email", true) {
		t.Errorf("expected ok ses_verify_email step, got %+v", emailOut.Steps)
	}

	// domain → verify_domain + DKIM records. inbound=auto with no
	// aws-s3 / aws-sns bindings should *not* touch SNS or S3.
	domainOutRaw, err := app.toolSendersCreate(ctx, map[string]any{"address": "newdomain.com"})
	if err != nil {
		t.Fatal(err)
	}
	d := domainOutRaw.(*sendersCreateResp)
	if d.Kind != "domain" {
		t.Errorf("domain kind=%q", d.Kind)
	}
	if len(d.DkimTokens) != 3 || d.DkimTokens[0] != "aaa" {
		t.Errorf("dkim_tokens=%v", d.DkimTokens)
	}
	if len(d.DnsRecords) == 0 || d.DnsRecords[0]["name"] != "aaa._domainkey.newdomain.com" || d.DnsRecords[0]["value"] != "aaa.dkim.amazonses.com" {
		t.Errorf("dns_records[0]=%+v", d.DnsRecords)
	}
	if d.Inbound == nil || d.Inbound.Bootstrapped {
		t.Errorf("expected inbound.bootstrapped=false, got %+v", d.Inbound)
	}

	// Confirm dispatch by tool name — only the two SES verify_* calls,
	// no SNS / S3 traffic on the unbound auto path.
	tools := []string{}
	for _, c := range plat.executeCalls {
		tools = append(tools, c.Tool)
	}
	if len(tools) != 2 || tools[0] != "verify_email" || tools[1] != "verify_domain" {
		t.Errorf("tool dispatch=%v", tools)
	}
}

func hasStep(steps []bootstrapStep, name string, wantOK bool) bool {
	for _, s := range steps {
		if s.Step == name && s.OK == wantOK {
			return true
		}
	}
	return false
}

func TestSenders_GetQuota_ReportsSandboxFlag(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{}) // default get_quota stub: ProductionAccessEnabled=false
	app := &App{}
	out, err := app.toolSendersGetQuota(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["sandboxed"] != true {
		t.Errorf("expected sandboxed=true, got %+v", r)
	}
	if r["send_quota_24h"].(float64) != 200 {
		t.Errorf("quota=%v", r["send_quota_24h"])
	}
}

func TestSenders_NoBoundProvider(t *testing.T) {
	// stubPlatform with WhoAmI bindings *empty* — no email_provider.
	// v0.9: senders_list reads from the local table and the empty-
	// table refresh path silently skips unbound providers. So with no
	// provider AND no local rows, we return an empty list — not an
	// error. Errors only surface from senders_create (which actually
	// needs a provider to do its work).
	plat := &stubPlatform{}
	plat.bindingsOverride = map[string]any{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	out, err := app.toolSendersList(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := out.(map[string]any)
	if r["count"].(int) != 0 {
		t.Errorf("expected empty senders list with no provider bound, got %+v", r)
	}
	// senders_create is the right place for the unbound-provider error.
	_, err = app.toolSendersCreate(ctx, map[string]any{"address": "x.com"})
	if err == nil || !strings.Contains(err.Error(), "email_provider") {
		t.Errorf("senders_create with no email_provider should error, got %v", err)
	}
}

// ─── /tools/call HTTP dispatcher ───────────────────────────────────

func TestHandleToolsCall_DispatchesByName(t *testing.T) {
	_ = newTestCtx(t, &stubPlatform{})
	app := &App{}

	body := bytes.NewBufferString(`{"tool":"template_create","args":{"name":"hello"}}`)
	r := httptest.NewRequest("POST", "/tools/call", body)
	w := httptest.NewRecorder()
	app.handleToolsCall(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["template"] == nil {
		t.Errorf("expected template in response, got %v", out)
	}
}

func TestHandleToolsCall_UnknownTool404(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	_ = ctx
	app := &App{}

	r := httptest.NewRequest("POST", "/tools/call", bytes.NewBufferString(`{"tool":"does_not_exist","args":{}}`))
	w := httptest.NewRecorder()
	app.handleToolsCall(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleToolsCall_RejectsGET(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	_ = ctx
	app := &App{}

	r := httptest.NewRequest("GET", "/tools/call", nil)
	w := httptest.NewRecorder()
	app.handleToolsCall(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// v0.11.5 regression: SES verify_domain returns "already exists" when
// the domain identity is in the account from a prior bootstrap (very
// common: the parent was never persisted locally because some earlier
// step failed, so the inheritance flow re-runs sendersCreateDomain).
// We must adopt the existing identity via get_identity_verification
// and continue, not fail the whole bootstrap.
func TestSendersCreate_Domain_AdoptsExistingIdentityWhenAlreadyAtSES(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"domains":        float64(42),
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_domain": {
				Success: false, Status: 409,
				Data: json.RawMessage(`{"message":"Email identity socialcast.dev already exist."}`),
			},
			"get_identity_verification": {Success: true, Status: 200, Data: json.RawMessage(
				`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
		},
		callAppReply: json.RawMessage(`{"action":"updated"}`),
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendersCreate(ctx, map[string]any{"address": "test@socialcast.dev"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	// ses_verify_domain step should be ok with "adopted" annotation.
	var verifyStep *bootstrapStep
	for i := range resp.Steps {
		if resp.Steps[i].Step == "ses_verify_domain" {
			verifyStep = &resp.Steps[i]
		}
	}
	if verifyStep == nil || !verifyStep.OK {
		t.Fatalf("expected ok ses_verify_domain after adoption, got %+v", resp.Steps)
	}
	if !strings.Contains(verifyStep.Detail, "adopted") {
		t.Errorf("expected adoption annotation in detail, got %q", verifyStep.Detail)
	}
	// DKIM tokens from the existing identity should bubble up.
	if len(resp.DkimTokens) != 3 || resp.DkimTokens[0] != "a" {
		t.Errorf("expected adopted DKIM tokens, got %v", resp.DkimTokens)
	}
	// Confirm the SES dispatch: verify_domain then get_identity_verification.
	var saw []string
	for _, c := range plat.executeCalls {
		if c.Tool == "verify_domain" || c.Tool == "get_identity_verification" {
			saw = append(saw, c.Tool)
		}
	}
	if len(saw) != 2 || saw[0] != "verify_domain" || saw[1] != "get_identity_verification" {
		t.Errorf("unexpected adoption dispatch: %v", saw)
	}
}

// ─── Mailbox inherits parent-domain DKIM (no per-mailbox verify) ──

func TestSendersCreate_Mailbox_InheritsFromVerifiedParent(t *testing.T) {
	// Domains app + SES both bound; the parent domain is already a
	// verified-domain row locally → mailbox add must not call any SES
	// verify_* tool, and must persist the row as verified.
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"domains":        float64(42),
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "socialcast.dev",
		Provider: "aws-ses", ProviderIdentityID: "socialcast.dev",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})

	out, err := app.toolSendersCreate(ctx, map[string]any{"address": "test@socialcast.dev"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if resp.Pending {
		t.Errorf("expected resp.Pending=false (inherited), got true: %+v", resp)
	}
	if !hasStep(resp.Steps, "parent_domain_already_verified", true) {
		t.Errorf("expected parent_domain_already_verified step, got %+v", resp.Steps)
	}
	for _, c := range plat.executeCalls {
		if c.Tool == "verify_email" || c.Tool == "verify_domain" {
			t.Errorf("inherited path should not call SES %s, got %+v", c.Tool, c)
		}
	}
	// Mailbox row persisted as verified.
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "test@socialcast.dev")
	if row == nil || !row.Verified {
		t.Errorf("expected verified mailbox row, got %+v", row)
	}
}

func TestSendersCreate_Mailbox_VerifiesParentWhenMissing(t *testing.T) {
	// Parent domain not in local table → mailbox add triggers the full
	// domain verification flow on the parent (verify_domain + DNS
	// publish via Domains app), no per-mailbox verify_email.
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"domains":        float64(42),
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(
				`{"DkimAttributes":{"Tokens":["t1","t2","t3"],"Status":"PENDING"}}`)},
		},
		callAppReply: json.RawMessage(`{"action":"created"}`),
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendersCreate(ctx, map[string]any{"address": "ops@newdomain.com"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	// Confirm the SES dispatch: verify_domain on the parent, NOT
	// verify_email on the mailbox.
	tools := []string{}
	for _, c := range plat.executeCalls {
		tools = append(tools, c.Tool)
	}
	hasVerifyDomain := false
	for _, name := range tools {
		if name == "verify_email" {
			t.Errorf("verify_email should not be called when domains is bound, got %v", tools)
		}
		if name == "verify_domain" {
			hasVerifyDomain = true
		}
	}
	if !hasVerifyDomain {
		t.Errorf("expected verify_domain call on parent, got %v", tools)
	}
	// Honest inheritance: the parent's DKIM is still PENDING (per the
	// stub), so the mailbox must NOT claim verified. Persisting verified=
	// true here while the parent isn't actually verified was the "lie
	// about success" bug (#2) — the mailbox inherits the parent's REAL
	// state, and only flips verified once the parent's DKIM does.
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "ops@newdomain.com")
	if row == nil {
		t.Fatalf("expected mailbox row to exist")
	}
	if row.Verified || row.VerificationStatus != "pending" {
		t.Errorf("mailbox should inherit parent's pending state, got verified=%v status=%q", row.Verified, row.VerificationStatus)
	}
	if !resp.Pending {
		t.Errorf("resp.Pending should be true while parent DKIM is pending")
	}
	// DKIM token surfaced from the parent's domain flow.
	if len(resp.DkimTokens) != 3 {
		t.Errorf("expected parent DKIM tokens to bubble up, got %v", resp.DkimTokens)
	}
}

func TestSendersCreate_MailboxPersistsVerifiedParentDespiteInboundFailure(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"domains":               float64(42),
			"inbound_storage":       float64(3),
			"inbound_notifications": float64(4),
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_topic": {Success: true, Status: 200, Data: json.RawMessage(
				`{"TopicArn":"arn:aws:sns:eu-west-1:123456789012:apteva-test"}`)},
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(
				`{"DkimAttributes":{"Tokens":["t1","t2","t3"],"Status":"SUCCESS"}}`)},
		},
		callAppReply: json.RawMessage(`{"action":"created"}`),
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendersCreate(ctx, map[string]any{"address": "ops@newdomain.com"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if !hasStep(resp.Steps, "sns_subscribe_webhook", false) {
		t.Fatalf("expected inbound webhook failure before final domain persist, got %+v", resp.Steps)
	}
	parent, _ := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "newdomain.com")
	if parent == nil || !parent.Verified || parent.DkimStatus != "SUCCESS" {
		t.Fatalf("verified parent identity should persist despite later inbound failure, got %+v", parent)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "ops@newdomain.com")
	if row == nil || !row.Verified || row.VerificationStatus != "verified" || row.ParentIdentityID == nil {
		t.Fatalf("mailbox should inherit verified parent despite later inbound failure, got %+v", row)
	}
	if resp.Pending {
		t.Errorf("resp.Pending should be false after DKIM SUCCESS inheritance")
	}
}

// Legacy fallback: when Domains is NOT bound, the old per-mailbox
// verify_email flow must still work for mailboxes at uncontrolled
// domains (e.g., me@gmail.com).
func TestSendersCreate_Mailbox_FallsBackToVerifyEmailWithoutDomains(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{"email_provider": float64(1)},
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_email": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "me@gmail.com"}); err != nil {
		t.Fatal(err)
	}
	called := false
	for _, c := range plat.executeCalls {
		if c.Tool == "verify_email" {
			called = true
		}
	}
	if !called {
		t.Errorf("expected verify_email fallback when domains unbound, got %+v", plat.executeCalls)
	}
}

// ─── senders_delete idempotency ───────────────────────────────────

func TestSendersDelete_InheritanceMailboxSkipsUpstream(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Verified parent identity + inheritance mailbox pointing at it.
	parentID := preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "socialcast.dev",
		Provider: "aws-ses", ProviderIdentityID: "socialcast.dev",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "test@socialcast.dev", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "test@socialcast.dev",
		Verified: true, VerificationStatus: "verified",
		SendingEnabled: true, DkimStatus: "SUCCESS",
		ParentIdentityID: parentID,
	})

	if _, err := app.toolSendersDelete(ctx, map[string]any{"address": "test@socialcast.dev"}); err != nil {
		t.Fatalf("inheritance delete should succeed without SES call, got %v", err)
	}
	for _, c := range plat.executeCalls {
		if c.Tool == "delete_identity" {
			t.Errorf("inheritance mailbox should not call SES delete_identity, got %+v", c)
		}
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "test@socialcast.dev")
	if row == nil || row.DeletedAt == nil {
		t.Errorf("local row should be soft-deleted, got %+v", row)
	}
}

func TestSendersDelete_TreatsIdentityNotFoundAsSuccess(t *testing.T) {
	// Standalone mailbox (no local parent) — and SES says it doesn't
	// exist. Should still soft-delete locally; "already gone" is
	// success not failure.
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"delete_identity": {
				Success: false, Status: 404,
				Data: json.RawMessage(`{"message":"Email identity gone@x.com does not exist."}`),
			},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "gone@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "gone@x.com",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendersDelete(ctx, map[string]any{"address": "gone@x.com"}); err != nil {
		t.Fatalf("not-found delete should be idempotent success, got %v", err)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "gone@x.com")
	if row == nil || row.DeletedAt == nil {
		t.Errorf("local row should be soft-deleted, got %+v", row)
	}
}

func TestSendersDelete_PropagatesRealUpstreamError(t *testing.T) {
	// Anything that's NOT a "not found" should still surface as an
	// error so the panel shows the toast and the local row stays.
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"delete_identity": {
				Success: false, Status: 500,
				Data: json.RawMessage(`{"message":"InternalServerError"}`),
			},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "ok@x.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "ok@x.com",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendersDelete(ctx, map[string]any{"address": "ok@x.com"}); err == nil {
		t.Fatal("expected real upstream error to propagate")
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "ok@x.com")
	if row == nil || row.DeletedAt != nil {
		t.Errorf("row should NOT be soft-deleted when SES error is real, got %+v", row)
	}
}

// ─── v0.12.7 inbound body transfer-encoding decode ────────────────

// Reported bug: extractBodies stored the raw transfer-encoded bytes
// (base64 / quoted-printable) instead of decoding. Proton mail
// triggered it every time — both text/plain and text/html parts are
// base64-encoded by default.
func TestExtractBodies_DecodesBase64SinglePart(t *testing.T) {
	// "Hello\n" → base64 → "SGVsbG8K"
	body := []byte("SGVsbG8K")
	text, html := extractBodies("text/plain; charset=utf-8", "base64", body)
	if text != "Hello\n" {
		t.Errorf("base64 single-part text/plain not decoded: %q", text)
	}
	if html != "" {
		t.Errorf("html should be empty for text/plain input: %q", html)
	}
}

func TestExtractBodies_DecodesQuotedPrintableSinglePart(t *testing.T) {
	// "café=200€" → quoted-printable → "caf=C3=A9=3D200=E2=82=AC"
	body := []byte("caf=C3=A9=3D200=E2=82=AC")
	text, _ := extractBodies("text/plain", "quoted-printable", body)
	if text != "café=200€" {
		t.Errorf("quoted-printable not decoded: %q", text)
	}
}

func TestExtractBodies_DecodesPerPartEncodingInMultipart(t *testing.T) {
	// Proton-shaped multipart: text/plain base64 + text/html base64,
	// both with the per-part Content-Transfer-Encoding header. The
	// outer Content-Transfer-Encoding doesn't apply to multipart;
	// each part declares its own.
	body := []byte("--BNDRY\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"SGVsbG8K\r\n" +
		"--BNDRY\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"PHA+SGVsbG88L3A+\r\n" +
		"--BNDRY--")
	text, html := extractBodies("multipart/alternative; boundary=\"BNDRY\"", "", body)
	if text != "Hello\n" {
		t.Errorf("multipart text/plain not decoded: %q", text)
	}
	if html != "<p>Hello</p>" {
		t.Errorf("multipart text/html not decoded: %q", html)
	}
}

func TestExtractBodies_NoEncodingPassesThrough(t *testing.T) {
	// 7bit / unset → raw bytes preserved as-is. Regression guard for
	// Gmail-style plain mail that pre-v0.12.7 worked fine.
	body := []byte("This is plain text.\n")
	text, _ := extractBodies("text/plain", "7bit", body)
	if text != "This is plain text.\n" {
		t.Errorf("plain text mangled: %q", text)
	}
	text2, _ := extractBodies("text/plain", "", body)
	if text2 != "This is plain text.\n" {
		t.Errorf("empty encoding mangled: %q", text2)
	}
}

func TestDecodeTransferEncoding_FallsBackOnDecodeFailure(t *testing.T) {
	// Malformed base64 must not lose the data — return it verbatim so
	// the operator can still inspect.
	garbage := "this is not base64!@#$"
	got := decodeTransferEncoding("base64", garbage)
	if got != garbage {
		t.Errorf("decode failure should fall through to raw; got %q", got)
	}
}

// ─── v0.12.6 inbound webhook project resolution ───────────────────

// Reported bug: SNS subscription URL lacked project_id, so global-
// scope installs (prod) errored with "project_id required in query
// string when install scope=global" on every inbound delivery — SES
// thought it had succeeded; nothing landed locally. v0.12.6 derives
// the project from the recipient domain via the identities table.
func TestResolveProjectFromInboundEmail_UsesIdentityForRecipientDomain(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})

	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "schwartzindustries.com",
		Provider: "aws-ses", Verified: true, VerificationStatus: "verified",
	})

	sesEnv := &sesInboundEnvelope{}
	sesEnv.Receipt.Recipients = []string{"contact@schwartzindustries.com"}

	got := resolveProjectFromInboundEmail(ctx, nil, sesEnv)
	if got != "test-proj" {
		t.Errorf("expected project derived from identity (test-proj), got %q", got)
	}
}

func TestResolveProjectFromInboundEmail_NoMatchReturnsEmpty(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})

	sesEnv := &sesInboundEnvelope{}
	sesEnv.Receipt.Recipients = []string{"someone@notmine.example"}

	got := resolveProjectFromInboundEmail(ctx, nil, sesEnv)
	if got != "" {
		t.Errorf("unknown domain should not derive a project, got %q", got)
	}
}

func TestResolveProjectFromInboundEmail_FallsBackToParsedTo(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "hypnofans.com",
		Provider: "aws-ses", Verified: true, VerificationStatus: "verified",
	})

	// SES envelope without Receipt.Recipients (e.g., legacy/odd
	// shape); parsed.To should still resolve us correctly.
	parsed := &parsedInbound{To: []string{"info@hypnofans.com"}}
	got := resolveProjectFromInboundEmail(ctx, parsed, &sesInboundEnvelope{})
	if got != "test-proj" {
		t.Errorf("expected fallback to parsed.To, got %q", got)
	}
}

func TestResolveProjectFromInboundPhone_LooksUpSender(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	preseedSender(t, ctx, senderUpsert{
		Channel: "sms", Address: "+15551234567", Kind: "phone",
		Provider: "twilio", Verified: true, VerificationStatus: "verified",
		SendingEnabled: true,
	})

	got := resolveProjectFromInboundPhone(ctx, "sms", "+15551234567")
	if got != "test-proj" {
		t.Errorf("expected project from sender row, got %q", got)
	}
}

func TestResolveProjectFromInboundPhone_StripsWhatsAppPrefix(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	preseedSender(t, ctx, senderUpsert{
		Channel: "whatsapp", Address: "+15551234567", Kind: "phone",
		Provider: "twilio", Verified: true, VerificationStatus: "verified",
		SendingEnabled: true,
	})

	got := resolveProjectFromInboundPhone(ctx, "whatsapp", "whatsapp:+15551234567")
	if got != "test-proj" {
		t.Errorf("whatsapp: prefix should be stripped before lookup, got %q", got)
	}
}

// ─── v0.12.5 receipt-rule per-install + merge-on-AlreadyExists ────

// Reported bug: ruleName was the constant "messaging-inbound" across
// every install. First install to bootstrap claimed it; everyone else
// AlreadyExists'd and silently no-op'd while persisting
// inbound_bootstrapped=1 — SES had no rule for their domains, mail
// bounced.
func TestSendersCreate_Domain_InboundRuleNameIsInstallScoped(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"inbound_storage":       float64(2),
			"inbound_notifications": float64(3),
		},
		whoAmIOverride: &sdk.InstallIdentity{
			AppName: "messaging", ProjectID: "test-proj", InstallID: 47,
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_sns_topic":            {Success: true, Status: 200, Data: json.RawMessage(`{"CreateTopicResponse":{"CreateTopicResult":{"TopicArn":"arn:aws:sns:eu-west-1:111:apteva-ses-inbound-47"}}}`)},
			"set_topic_attributes":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_s3_bucket":            {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"put_s3_bucket_policy":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"verify_domain":               {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
			"create_receipt_rule_set":     {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_receipt_rule":         {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"set_active_receipt_rule_set": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"subscribe":                   {Success: true, Status: 200, Data: json.RawMessage(`{"SubscribeResponse":{"SubscribeResult":{"SubscriptionArn":"arn:sub"}}}`)},
		},
	}
	// PublicURL is needed for inbound auto to engage; the bootstrap
	// also reads it for the webhook subscription. Skip subscribe step
	// failures aren't relevant to the rule-naming assertion below.
	t.Setenv("APTEVA_PUBLIC_URL", "https://test.public.example")
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"address": "schwartzindustries.com",
		"inbound": "true",
	}); err != nil {
		t.Fatal(err)
	}

	var createRule *executeCall
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "create_receipt_rule" {
			createRule = &plat.executeCalls[i]
			break
		}
	}
	if createRule == nil {
		t.Fatal("create_receipt_rule not called")
	}
	if createRule.Input["Rule.Name"] != "messaging-inbound-47" {
		t.Errorf("rule name not install-scoped: got %q, want messaging-inbound-47", createRule.Input["Rule.Name"])
	}
	if createRule.Input["Rule.Recipients.member.1"] != "schwartzindustries.com" {
		t.Errorf("recipient not set: %+v", createRule.Input)
	}
}

func TestSendersCreate_Domain_SubscribeWebhookURLsAreProjectScoped(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"inbound_storage":       float64(2),
			"inbound_notifications": float64(3),
		},
		whoAmIOverride: &sdk.InstallIdentity{
			AppName:   "messaging",
			ProjectID: "test-proj",
			InstallID: 47,
			PublicURL: "https://test.public.example",
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_sns_topic":            {Success: true, Status: 200, Data: json.RawMessage(`{"CreateTopicResponse":{"CreateTopicResult":{"TopicArn":"arn:aws:sns:eu-west-1:111:apteva-ses-inbound-47"}}}`)},
			"set_topic_attributes":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_s3_bucket":            {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"put_s3_bucket_policy":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"verify_domain":               {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
			"create_config_set":           {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"add_event_destination":       {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_receipt_rule_set":     {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_receipt_rule":         {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"set_active_receipt_rule_set": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"subscribe":                   {Success: true, Status: 200, Data: json.RawMessage(`{"SubscribeResponse":{"SubscribeResult":{"SubscriptionArn":"arn:sub"}}}`)},
		},
	}
	t.Setenv("APTEVA_APP_TOKEN", "dev-47")
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"address": "schwartzindustries.com",
		"inbound": "true",
	}); err != nil {
		t.Fatal(err)
	}

	var endpoints []string
	for _, call := range plat.executeCalls {
		if call.Tool == "subscribe" {
			endpoints = append(endpoints, fmt.Sprint(call.Input["Endpoint"]))
		}
	}
	if len(endpoints) != 2 {
		t.Fatalf("subscribe calls=%d endpoints=%v, want events + inbound", len(endpoints), endpoints)
	}
	joined := strings.Join(endpoints, "\n")
	for _, want := range []string{
		"/api/apps/messaging/webhooks/ses-bounces?",
		"/api/apps/messaging/webhooks/ses-inbound?",
		"api_key=dev-47",
		"project_id=test-proj",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("subscribe endpoints missing %q:\n%s", want, joined)
		}
	}
}

func TestSendersCreate_Domain_GlobalInstallWebhookURLsOmitProject(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"inbound_storage":       float64(2),
			"inbound_notifications": float64(3),
		},
		whoAmIOverride: &sdk.InstallIdentity{
			AppName:   "messaging",
			ProjectID: "",
			InstallID: 47,
			PublicURL: "https://test.public.example",
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_sns_topic":            {Success: true, Status: 200, Data: json.RawMessage(`{"CreateTopicResponse":{"CreateTopicResult":{"TopicArn":"arn:aws:sns:eu-west-1:111:apteva-ses-inbound-47"}}}`)},
			"set_topic_attributes":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_s3_bucket":            {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"put_s3_bucket_policy":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"verify_domain":               {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
			"create_config_set":           {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"add_event_destination":       {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_receipt_rule_set":     {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_receipt_rule":         {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"set_active_receipt_rule_set": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"subscribe":                   {Success: true, Status: 200, Data: json.RawMessage(`{"SubscribeResponse":{"SubscribeResult":{"SubscriptionArn":"arn:sub"}}}`)},
		},
	}
	t.Setenv("APTEVA_APP_TOKEN", "dev-47")
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"address":     "schwartzindustries.com",
		"inbound":     "true",
		"_project_id": "test-proj",
	}); err != nil {
		t.Fatal(err)
	}

	var endpoints []string
	for _, call := range plat.executeCalls {
		if call.Tool == "subscribe" {
			endpoints = append(endpoints, fmt.Sprint(call.Input["Endpoint"]))
		}
	}
	joined := strings.Join(endpoints, "\n")
	if !strings.Contains(joined, "api_key=dev-47") {
		t.Fatalf("subscribe endpoints missing api key:\n%s", joined)
	}
	if strings.Contains(joined, "project_id=") {
		t.Fatalf("global install webhook endpoints should omit project_id:\n%s", joined)
	}
}

// Same install adding a second domain: AlreadyExists must trigger
// describe → merge → delete → recreate-with-both, not the pre-v0.12.5
// silent no-op.
func TestSendersCreate_Domain_AlreadyExists_MergesRecipients(t *testing.T) {
	const describeReply = `{
		"DescribeActiveReceiptRuleSetResponse": {
			"DescribeActiveReceiptRuleSetResult": {
				"Metadata": {"Name":"apteva-default"},
				"Rules": {"member":[
					{"Name":"messaging-inbound-47","Recipients":{"member":["schwartzindustries.com"]}}
				]}
			}
		}
	}`
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"inbound_storage":       float64(2),
			"inbound_notifications": float64(3),
		},
		whoAmIOverride: &sdk.InstallIdentity{
			AppName: "messaging", ProjectID: "test-proj", InstallID: 47,
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_sns_topic":        {Success: true, Status: 200, Data: json.RawMessage(`{"CreateTopicResponse":{"CreateTopicResult":{"TopicArn":"arn:aws:sns:eu-west-1:111:apteva-ses-inbound-47"}}}`)},
			"set_topic_attributes":    {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_s3_bucket":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"put_s3_bucket_policy":    {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"verify_domain":           {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
			"create_receipt_rule_set": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			// First create_receipt_rule call (for the new domain) fails
			// AlreadyExists; merge path then describes, deletes, recreates.
			"set_active_receipt_rule_set":      {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"subscribe":                        {Success: true, Status: 200, Data: json.RawMessage(`{"SubscribeResponse":{"SubscribeResult":{"SubscriptionArn":"arn:sub"}}}`)},
			"delete_receipt_rule":              {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"describe_active_receipt_rule_set": {Success: true, Status: 200, Data: json.RawMessage(describeReply)},
		},
		// create_receipt_rule needs a per-call switch: first AlreadyExists,
		// second (recreate) success. Use a tiny dispatcher.
		executeOverride: func(tool string, calls int) *sdk.ExecuteResult {
			if tool == "create_receipt_rule" {
				if calls == 0 {
					return &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"Error":{"Code":"AlreadyExists","Message":"Rule already exists"}}`)}
				}
				return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
			}
			return nil
		},
	}
	t.Setenv("APTEVA_PUBLIC_URL", "https://test.public.example")
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"address": "hypnofans.com",
		"inbound": "true",
	}); err != nil {
		t.Fatal(err)
	}

	// Confirm the dispatch order: create → describe → delete → create.
	var seq []string
	for _, c := range plat.executeCalls {
		switch c.Tool {
		case "create_receipt_rule", "describe_active_receipt_rule_set", "delete_receipt_rule":
			seq = append(seq, c.Tool)
		}
	}
	if len(seq) != 4 ||
		seq[0] != "create_receipt_rule" ||
		seq[1] != "describe_active_receipt_rule_set" ||
		seq[2] != "delete_receipt_rule" ||
		seq[3] != "create_receipt_rule" {
		t.Fatalf("expected create→describe→delete→create dispatch, got %v", seq)
	}

	// The recreate (2nd create_receipt_rule call) must carry BOTH the
	// existing (schwartzindustries.com) and the new (hypnofans.com)
	// recipient. Pre-v0.12.5 the bug was the silent no-op; this
	// assertion is the actual fix.
	var recreate *executeCall
	createSeen := 0
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "create_receipt_rule" {
			createSeen++
			if createSeen == 2 {
				recreate = &plat.executeCalls[i]
				break
			}
		}
	}
	if recreate == nil {
		t.Fatal("no 2nd create_receipt_rule call (the recreate after delete)")
	}
	for _, key := range []string{"Rule.Recipients.member.1", "Rule.Recipients.member.2"} {
		if recreate.Input[key] == nil {
			t.Errorf("recreate missing %s: %+v", key, recreate.Input)
		}
	}
	got := []string{
		fmt.Sprint(recreate.Input["Rule.Recipients.member.1"]),
		fmt.Sprint(recreate.Input["Rule.Recipients.member.2"]),
	}
	want := map[string]bool{"schwartzindustries.com": false, "hypnofans.com": false}
	for _, r := range got {
		if _, ok := want[r]; ok {
			want[r] = true
		}
	}
	for r, seen := range want {
		if !seen {
			t.Errorf("merged recipients missing %q (got %v)", r, got)
		}
	}
}

// ─── v0.12.3 toolSendersCreate global-scope + arg-drop fixes ──────

// Reported bug: toolSendersCreate's MCP wrapper never copied
// _project_id out of args, so global-scope installs always 500'd with
// "project_id required" even when the caller dutifully passed it per
// the platform convention.
func TestSendersCreate_GlobalScope_ResolvesProjectIDFromArgs(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}

	out, err := app.toolSendersCreate(ctx, map[string]any{
		"_project_id": "test-proj",
		"address":     "ops@example.com",
	})
	if err != nil {
		t.Fatalf("global-scope create should succeed when _project_id is in args, got %v", err)
	}
	resp := out.(*sendersCreateResp)
	if resp.Address != "ops@example.com" {
		t.Errorf("address=%q", resp.Address)
	}
	// And the row landed in the right project's table.
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "ops@example.com")
	if row == nil {
		t.Error("sender row not persisted under test-proj")
	}
}

// Same-class bug found while fixing: display_name + set_default args
// declared in the schema but never extracted by the MCP wrapper, so
// the panel's Add form silently dropped both even when the operator
// filled them in.
func TestSendersCreate_ExtractsDisplayNameFromArgs(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"address":      "marco@example.com",
		"display_name": "Marco at Apteva",
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "marco@example.com")
	if row == nil || row.DisplayName != "Marco at Apteva" {
		t.Errorf("display_name not persisted from args: %+v", row)
	}
}

// v0.12.4 regression: bootstrapPublishDNSRecord didn't inject
// _project_id into the cross-app call to domains.domain_records_set.
// On global-scope domains installs (prod), every publish_dns step
// failed with "project_id missing"; the domains app fell back to its
// install role binding and rejected (wrong DNS provider for the
// domain). Project-scoped installs (dev) papered over the bug via the
// APTEVA_PROJECT_ID env in the domains sidecar.
func TestSendersCreate_Domain_PublishDNS_InjectsProjectIDIntoCrossAppCall(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"domains":        float64(42),
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(
				`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
		},
		callAppReply: json.RawMessage(`{"action":"created"}`),
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"address": "newdomain.example",
	}); err != nil {
		t.Fatal(err)
	}

	// Every CallApp("domains", ...) the bootstrap fired must carry
	// _project_id. Three DKIM CNAME publishes + one SPF TXT = 4 calls
	// (inbound MX is gated on aws-s3/aws-sns being bound; not in this
	// test, so doInbound=false → no MX call).
	if len(plat.callAppCalls) < 4 {
		t.Fatalf("expected >=4 CallApp invocations (DKIM x3 + SPF), got %d: %+v", len(plat.callAppCalls), plat.callAppCalls)
	}
	for _, c := range plat.callAppCalls {
		if c.App != "domains" || c.Tool != "domain_records_set" {
			continue // domain_list etc. — only the publishes matter here
		}
		pid, _ := c.Input["_project_id"].(string)
		if pid != "test-proj" {
			t.Errorf("publish_dns CallApp missing/wrong _project_id (got %q): %+v", pid, c.Input)
		}
	}
}

// ─── senders_update (local-only patch) ─────────────────────────────

func TestSendersUpdate_SetsDisplayName(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "marco@socialcast.dev", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "marco@socialcast.dev",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendersUpdate(ctx, map[string]any{
		"address":      "marco@socialcast.dev",
		"display_name": "Marco at Socialcast",
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "marco@socialcast.dev")
	if row == nil || row.DisplayName != "Marco at Socialcast" {
		t.Errorf("display_name not persisted: %+v", row)
	}
}

func TestSendersUpdate_EmptyArgsErrors(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "marco@socialcast.dev", Kind: "email_mailbox",
		Provider: "aws-ses", Verified: true,
	})
	_, err := app.toolSendersUpdate(ctx, map[string]any{
		"address": "marco@socialcast.dev",
	})
	if err == nil {
		t.Fatal("expected error when neither display_name nor notes is set")
	}
}

// ─── v0.12.1 friendly From ─────────────────────────────────────────

func TestSendMessage_UsesSenderDisplayNameAsFriendlyFrom(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "marco@socialcast.dev", Kind: "email_mailbox",
		DisplayName: "Marco at Socialcast",
		Provider:    "aws-ses", ProviderIdentityID: "marco@socialcast.dev",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"from":    "marco@socialcast.dev",
		"to":      "alice@example.com",
		"subject": "hi",
		"body":    "test",
	}); err != nil {
		t.Fatal(err)
	}
	call := plat.executeCalls[0]
	got := call.Input["FromEmailAddress"].(string)
	want := `"Marco at Socialcast" <marco@socialcast.dev>`
	if got != want {
		t.Errorf("FromEmailAddress=%q, want %q", got, want)
	}
}

func TestSendMessage_FromNameArgOverridesSenderDisplayName(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "marco@socialcast.dev", Kind: "email_mailbox",
		DisplayName: "Marco at Socialcast",
		Provider:    "aws-ses", ProviderIdentityID: "marco@socialcast.dev",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendMessage(ctx, map[string]any{
		"channel":   "email",
		"from":      "marco@socialcast.dev",
		"from_name": "Apteva Support",
		"to":        "alice@example.com",
		"subject":   "hi",
		"body":      "test",
	}); err != nil {
		t.Fatal(err)
	}
	got := plat.executeCalls[0].Input["FromEmailAddress"].(string)
	want := `"Apteva Support" <marco@socialcast.dev>`
	if got != want {
		t.Errorf("FromEmailAddress=%q, want %q (per-call override should beat sender.display_name)", got, want)
	}
}

func TestSendMessage_NoDisplayNameUsesBareAddress(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "marco@socialcast.dev", Kind: "email_mailbox",
		// No DisplayName.
		Provider: "aws-ses", ProviderIdentityID: "marco@socialcast.dev",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})

	if _, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "email",
		"from":    "marco@socialcast.dev",
		"to":      "alice@example.com",
		"subject": "hi",
		"body":    "test",
	}); err != nil {
		t.Fatal(err)
	}
	got := plat.executeCalls[0].Input["FromEmailAddress"].(string)
	if got != "marco@socialcast.dev" {
		t.Errorf("FromEmailAddress=%q, want bare address (no friendly form when display_name unset)", got)
	}
}

func TestFormatFriendlyAddress_EscapesQuotes(t *testing.T) {
	// Display names with embedded quotes / backslashes must not break
	// the RFC 5322 quoted form. SES would otherwise reject the address
	// (or render confusingly in clients).
	got := formatFriendlyAddress(`Marco "M" Schwartz`, "marco@x.com")
	want := `"Marco \"M\" Schwartz" <marco@x.com>`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ─── v0.12 split: senders / identities ─────────────────────────────

func TestIdentitiesList_ReturnsAnchors(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}

	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "acme.com",
		Provider: "aws-ses", ProviderIdentityID: "acme.com",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
		InboundBootstrapped: true,
		InboundConfig:       `{"bucket":"apteva-ses-inbound-1","topic_arn":"arn:aws:sns:eu-west-1:111:apteva"}`,
	})
	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "other.io",
		Provider: "aws-ses", ProviderIdentityID: "other.io",
		Verified: false, VerificationStatus: "pending", DkimStatus: "PENDING",
	})

	out, err := app.toolIdentitiesList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["count"].(int) != 2 {
		t.Errorf("expected 2 identities, got %v", r)
	}
	rows := r["identities"].([]map[string]any)
	for _, row := range rows {
		if row["address"] == "acme.com" {
			if row["verified"] != true || row["dkim_status"] != "SUCCESS" {
				t.Errorf("acme.com row: %+v", row)
			}
			if row["inbound_bootstrapped"] != true {
				t.Errorf("expected inbound_bootstrapped=true: %+v", row)
			}
			cfg, _ := row["inbound_config"].(map[string]any)
			if cfg["bucket"] != "apteva-ses-inbound-1" {
				t.Errorf("inbound_config not parsed: %+v", row["inbound_config"])
			}
		}
	}
}

func TestIdentityUpsert_PreservesInboundWiringOnRefresh(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})

	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "acme.com",
		Provider: "aws-ses", ProviderIdentityID: "acme.com",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
		InboundBootstrapped: true,
		InboundConfig:       `{"bucket":"apteva-ses-inbound-47","topic_arn":"arn:aws:sns:eu-west-1:111:apteva"}`,
	})

	if _, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{
		ProjectID:          "test-proj",
		Kind:               "email_domain",
		Address:            "acme.com",
		Provider:           "aws-ses",
		ProviderIdentityID: "acme.com",
		Verified:           true,
		VerificationStatus: "verified",
		DkimStatus:         "SUCCESS",
		MarkSyncedNow:      true,
	}); err != nil {
		t.Fatal(err)
	}

	row, _ := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "acme.com")
	if row == nil {
		t.Fatal("identity not found")
	}
	if !row.InboundBootstrapped {
		t.Fatalf("provider refresh cleared inbound_bootstrapped: %+v", row)
	}
	if row.InboundConfig == "" || !strings.Contains(row.InboundConfig, "apteva-ses-inbound-47") {
		t.Fatalf("provider refresh cleared inbound_config: %+v", row)
	}
}

// senders_create on a bare domain should now write to identities, not
// senders. Confirms the v0.12 storage split actually happens through
// the normal create path (not just through pre-seeding).
func TestSendersCreate_Domain_WritesToIdentities(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(
				`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "freshdomain.io"}); err != nil {
		t.Fatal(err)
	}
	// Domain row is in identities…
	ident, _ := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "freshdomain.io")
	if ident == nil {
		t.Fatal("expected freshdomain.io in identities, not found")
	}
	if !ident.Verified || ident.DkimStatus != "SUCCESS" {
		t.Errorf("identity not verified: %+v", ident)
	}
	// …and NOT in senders.
	s, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "freshdomain.io")
	if s != nil {
		t.Errorf("domain leaked into senders: %+v", s)
	}
}

// Inheritance mailbox add must set parent_identity_id pointing at the
// (created-or-found) parent identity row.
func TestSendersCreate_Mailbox_SetsParentIdentityFK(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"domains":        float64(42),
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	parentID := preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "socialcast.dev",
		Provider: "aws-ses", ProviderIdentityID: "socialcast.dev",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})

	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "test@socialcast.dev"}); err != nil {
		t.Fatal(err)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "test@socialcast.dev")
	if row == nil {
		t.Fatal("mailbox row not persisted")
	}
	if row.ParentIdentityID == nil || *row.ParentIdentityID != parentID {
		t.Errorf("expected parent_identity_id=%d, got %v", parentID, row.ParentIdentityID)
	}
}

// ─── /senders/domains (cross-app read from Domains) ───────────────

func TestSendersDomains_UnboundReturnsAvailableFalse(t *testing.T) {
	// No "domains" binding → handler short-circuits before any CallApp.
	plat := &stubPlatform{
		bindingsOverride: map[string]any{"email_provider": float64(1)},
	}
	_ = newTestCtx(t, plat)
	app := &App{}

	r := httptest.NewRequest("GET", "/senders/domains?project_id=test-proj", nil)
	w := httptest.NewRecorder()
	app.handleSendersDomains(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["available"] != false {
		t.Errorf("expected available=false, got %v", out)
	}
	if len(plat.callAppCalls) != 0 {
		t.Errorf("unbound shortcut should skip CallApp, got %+v", plat.callAppCalls)
	}
}

func TestSendersDomains_BoundReturnsList(t *testing.T) {
	// Bindings include domains → handler calls domain_list via CallApp.
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"domains":        float64(42), // any non-nil app install id
		},
		callAppReply: json.RawMessage(`{"domains":[{"name":"acme.com"},{"name":"shop.example"}],"count":2}`),
	}
	_ = newTestCtx(t, plat)
	app := &App{}

	r := httptest.NewRequest("GET", "/senders/domains?project_id=test-proj", nil)
	w := httptest.NewRecorder()
	app.handleSendersDomains(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Available bool `json:"available"`
		Domains   []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Available || len(out.Domains) != 2 ||
		out.Domains[0].Name != "acme.com" || out.Domains[1].Name != "shop.example" {
		t.Errorf("unexpected response: %s", w.Body.String())
	}
	// Verify we hit the right app + tool, and injected _project_id.
	if len(plat.callAppCalls) != 1 {
		t.Fatalf("expected 1 CallApp, got %d", len(plat.callAppCalls))
	}
	c := plat.callAppCalls[0]
	if c.App != "domains" || c.Tool != "domain_list" {
		t.Errorf("wrong target: %+v", c)
	}
	if c.Input["_project_id"] != "test-proj" {
		t.Errorf("missing _project_id injection: %+v", c.Input)
	}
}

// ─── v0.4 provider-mirrored templates ─────────────────────────────

// stubPlatform with phone_provider bound, returning Twilio-shaped
// content_template responses for sync flow.
func newPhoneStub(reply *sdk.ExecuteResult) *stubPlatform {
	p := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			"phone_provider": float64(2),
		},
	}
	if reply != nil {
		p.replyByTool = map[string]*sdk.ExecuteResult{
			"list_content_templates": reply,
		}
	}
	return p
}

func TestTemplatesSyncProvider_UpsertsTwilioContent(t *testing.T) {
	twilioReply := &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"contents": [
			{
				"sid": "HX0000000000000000000000000000aa",
				"friendly_name": "order_confirmation",
				"language": "en",
				"variables": {"1":"name","2":"order_id"},
				"types": {"twilio/text": {"body": "Hi {{1}}, order #{{2}}"}},
				"approval_requests": [{"status": "approved"}]
			},
			{
				"sid": "HX0000000000000000000000000000bb",
				"friendly_name": "shipping_update",
				"variables": {"1":"name"},
				"types": {"twilio/text": {"body": "Hi {{1}}"}},
				"approval_requests": [{"status": "pending"}]
			}
		]
	}`)}
	plat := newPhoneStub(twilioReply)
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolTemplatesSyncProvider(ctx, map[string]any{"channel": "whatsapp"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["synced"] != 2 {
		t.Errorf("synced count: %+v", out)
	}

	// Templates appear in template_list.
	listed, err := app.toolTemplateList(ctx, map[string]any{"channel": "whatsapp"})
	if err != nil {
		t.Fatal(err)
	}
	tpls := listed.(map[string]any)["templates"].([]*Template)
	if len(tpls) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(tpls))
	}
	byName := map[string]*Template{}
	for _, tpl := range tpls {
		byName[tpl.Name] = tpl
	}
	if byName["order_confirmation"].ProviderStatus != "approved" {
		t.Errorf("status=%q", byName["order_confirmation"].ProviderStatus)
	}
	if byName["shipping_update"].ProviderStatus != "pending" {
		t.Errorf("status=%q", byName["shipping_update"].ProviderStatus)
	}
	for _, tpl := range tpls {
		if tpl.ProviderTemplateID == "" {
			t.Errorf("missing ContentSid: %+v", tpl)
		}
		if tpl.VarStyle != "numbered" {
			t.Errorf("var_style=%q", tpl.VarStyle)
		}
	}
}

func TestTemplatesSyncProvider_IdempotentOnRerun(t *testing.T) {
	twilioReply := &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"contents": [{
			"sid": "HX111", "friendly_name": "welcome",
			"types": {"twilio/text": {"body": "Hi"}},
			"approval_requests": [{"status": "approved"}]
		}]
	}`)}
	plat := newPhoneStub(twilioReply)
	ctx := newTestCtx(t, plat)
	app := &App{}

	_, _ = app.toolTemplatesSyncProvider(ctx, map[string]any{"channel": "whatsapp"})
	_, _ = app.toolTemplatesSyncProvider(ctx, map[string]any{"channel": "whatsapp"})

	listed, _ := app.toolTemplateList(ctx, map[string]any{"channel": "whatsapp"})
	tpls := listed.(map[string]any)["templates"].([]*Template)
	if len(tpls) != 1 {
		t.Errorf("expected 1 row after dedup, got %d", len(tpls))
	}
}

func TestTemplatesImportHTTP_SelectedTwilioContent(t *testing.T) {
	twilioReply := &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"contents": [
			{
				"sid": "HXaaa",
				"friendly_name": "approved_one",
				"language": "en",
				"variables": {"1":"name"},
				"types": {"twilio/text": {"body": "Hi {{1}}"}},
				"approval_requests": [{"status": "approved", "category": "UTILITY"}]
			},
			{
				"sid": "HXbbb",
				"friendly_name": "pending_one",
				"types": {"twilio/text": {"body": "Pending"}},
				"approval_requests": [{"status": "pending"}]
			}
		]
	}`)}
	plat := newPhoneStub(twilioReply)
	newTestCtx(t, plat)
	app := &App{}

	r := httptest.NewRequest("GET", "/templates/provider-preview?project_id=test-proj&channel=whatsapp", nil)
	w := httptest.NewRecorder()
	app.handleTemplatesProviderPreview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", w.Code, w.Body.String())
	}
	var preview struct {
		Templates []providerTemplateInfo `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Templates) != 2 || preview.Templates[0].LocalState != "new" {
		t.Fatalf("unexpected preview: %+v", preview)
	}

	body := strings.NewReader(`{"channel":"whatsapp","sids":["HXaaa"],"update_existing":true}`)
	r = httptest.NewRequest("POST", "/templates/import?project_id=test-proj", body)
	w = httptest.NewRecorder()
	app.handleTemplatesImport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", w.Code, w.Body.String())
	}
	var imported map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &imported)
	if imported["imported"] != float64(1) || imported["updated"] != float64(0) {
		t.Fatalf("unexpected import result: %+v", imported)
	}

	listed, err := app.toolTemplateList(globalCtx, map[string]any{"channel": "whatsapp", "_project_id": "test-proj"})
	if err != nil {
		t.Fatal(err)
	}
	tpls := listed.(map[string]any)["templates"].([]*Template)
	if len(tpls) != 1 || tpls[0].ProviderTemplateID != "HXaaa" || tpls[0].ProviderStatus != "approved" {
		t.Fatalf("unexpected templates: %+v", tpls)
	}

	r = httptest.NewRequest("GET", "/templates/provider-preview?project_id=test-proj&channel=whatsapp", nil)
	w = httptest.NewRecorder()
	app.handleTemplatesProviderPreview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("preview after import status=%d body=%s", w.Code, w.Body.String())
	}
	preview = struct {
		Templates []providerTemplateInfo `json:"templates"`
	}{}
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	stateBySID := map[string]string{}
	for _, tpl := range preview.Templates {
		stateBySID[tpl.Sid] = tpl.LocalState
	}
	if stateBySID["HXaaa"] != "imported" || stateBySID["HXbbb"] != "new" {
		t.Fatalf("unexpected local states: %+v", stateBySID)
	}
}

func TestTemplatesSyncProvider_NoOpForEmail(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolTemplatesSyncProvider(ctx, map[string]any{"channel": "email"})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["skipped"] != true {
		t.Errorf("expected skipped, got %+v", r)
	}
	// Should not have called the provider.
	if len(plat.executeCalls) != 0 {
		t.Errorf("expected zero provider calls for email channel, got %d", len(plat.executeCalls))
	}
}

func TestSendersProviderOptions_ListsTwilioPhoneAndWhatsAppSenders(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool = map[string]*sdk.ExecuteResult{
		"list_phone_numbers": {
			Success: true,
			Status:  200,
			Data: json.RawMessage(`{
				"incoming_phone_numbers": [{
					"sid": "PN123",
					"phone_number": "+15551112222",
					"friendly_name": "Support SMS",
					"capabilities": {"sms": true}
				}]
			}`),
		},
		"list_whatsapp_senders": {
			Success: true,
			Status:  200,
			Data: json.RawMessage(`{
				"senders": [{
					"sid": "XE123",
					"sender_id": "whatsapp:+15553334444",
					"friendly_name": "Support WhatsApp",
					"status": "ONLINE"
				}]
			}`),
		},
	}
	newTestCtx(t, plat)
	app := &App{}

	r := httptest.NewRequest("GET", "/senders/provider-options?project_id=test-proj", nil)
	w := httptest.NewRecorder()
	app.handleSendersProviderOptions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Available bool `json:"available"`
		Options   []struct {
			Channel string `json:"channel"`
			Address string `json:"address"`
			Label   string `json:"label"`
			Status  string `json:"status"`
		} `json:"options"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Available || len(out.Options) != 2 {
		t.Fatalf("unexpected output: %+v", out)
	}
	if out.Options[0].Channel != "sms" || out.Options[0].Address != "+15551112222" {
		t.Errorf("sms option: %+v", out.Options[0])
	}
	if out.Options[1].Channel != "whatsapp" || out.Options[1].Address != "+15553334444" || out.Options[1].Status != "online" {
		t.Errorf("whatsapp option: %+v", out.Options[1])
	}
}

func TestTemplateCreate_WhatsAppCreatesAndSubmitsProviderTemplate(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool = map[string]*sdk.ExecuteResult{
		"create_content_template": {
			Success: true,
			Status:  201,
			Data:    json.RawMessage(`{"sid":"HXcreated"}`),
		},
		"submit_content_template_approval": {
			Success: true,
			Status:  201,
			Data:    json.RawMessage(`{"status":"pending"}`),
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolTemplateCreate(ctx, map[string]any{
		"channel":     "whatsapp",
		"name":        "Appointment Reminder",
		"body_text":   "Hi {{1}}, your appointment is {{2}}.",
		"vars_schema": map[string]any{"1": "Alice", "2": "tomorrow"},
		"category":    "UTILITY",
		"language":    "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := out.(map[string]any)["template"].(*Template)
	if tpl.ProviderTemplateID != "HXcreated" {
		t.Fatalf("ProviderTemplateID=%q", tpl.ProviderTemplateID)
	}
	if tpl.ProviderStatus != "pending" {
		t.Fatalf("ProviderStatus=%q", tpl.ProviderStatus)
	}
	if tpl.VarStyle != "numbered" {
		t.Fatalf("VarStyle=%q", tpl.VarStyle)
	}

	var createCall, submitCall *executeCall
	for i := range plat.executeCalls {
		switch plat.executeCalls[i].Tool {
		case "create_content_template":
			createCall = &plat.executeCalls[i]
		case "submit_content_template_approval":
			submitCall = &plat.executeCalls[i]
		}
	}
	if createCall == nil || submitCall == nil {
		t.Fatalf("provider calls missing: %+v", plat.executeCalls)
	}
	if createCall.Input["friendly_name"] != "Appointment Reminder" {
		t.Errorf("friendly_name=%v", createCall.Input["friendly_name"])
	}
	types := createCall.Input["types"].(map[string]any)
	text := types["twilio/text"].(map[string]any)
	if text["body"] != "Hi {{1}}, your appointment is {{2}}." {
		t.Errorf("body=%v", text["body"])
	}
	if submitCall.Input["ContentSid"] != "HXcreated" {
		t.Errorf("ContentSid=%v", submitCall.Input["ContentSid"])
	}
	if submitCall.Input["name"] != "appointment_reminder" {
		t.Errorf("approval name=%v", submitCall.Input["name"])
	}
}

func TestSendMessageTemplate_UsesContentSidWhenProviderTemplate(t *testing.T) {
	twilioListReply := &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"contents": [{
			"sid": "HXabc",
			"friendly_name": "promo",
			"variables": {"1":"name"},
			"types": {"twilio/text": {"body": "Hi {{1}}"}},
			"approval_requests": [{"status": "approved"}]
		}]
	}`)}
	plat := newPhoneStub(twilioListReply)
	plat.replyByTool["send_whatsapp"] = &sdk.ExecuteResult{
		Success: true, Status: 201,
		Data: json.RawMessage(`{"sid":"SMxxxx"}`),
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Sync first.
	if _, err := app.toolTemplatesSyncProvider(ctx, map[string]any{"channel": "whatsapp"}); err != nil {
		t.Fatal(err)
	}
	listed, _ := app.toolTemplateList(ctx, map[string]any{"channel": "whatsapp"})
	tpls := listed.(map[string]any)["templates"].([]*Template)
	tplID := tpls[0].ID

	// Send via provider template.
	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel":     "whatsapp",
		"from":        "+15551112222",
		"to":          "+15553334444",
		"template_id": tplID,
		"vars":        map[string]any{"1": "Alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The send_whatsapp call must include ContentSid + ContentVariables,
	// NOT a Body — Twilio renders server-side.
	var sendCall *executeCall
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "send_whatsapp" {
			sendCall = &plat.executeCalls[i]
			break
		}
	}
	if sendCall == nil {
		t.Fatal("send_whatsapp was not called")
	}
	if sendCall.Input["ContentSid"] != "HXabc" {
		t.Errorf("ContentSid=%v, want HXabc", sendCall.Input["ContentSid"])
	}
	cv, _ := sendCall.Input["ContentVariables"].(string)
	if !strings.Contains(cv, `"1"`) || !strings.Contains(cv, "Alice") {
		t.Errorf("ContentVariables=%q (expected JSON with name)", cv)
	}
	if _, hasBody := sendCall.Input["Body"]; hasBody {
		t.Errorf("Body should be omitted on ContentSid sends, got %v", sendCall.Input["Body"])
	}
	if sendCall.Input["From"] != "whatsapp:+15551112222" {
		t.Errorf("From=%v (expected whatsapp: prefix)", sendCall.Input["From"])
	}
}

func TestSendMessageTemplate_RejectsPendingApproval(t *testing.T) {
	twilioListReply := &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"contents": [{
			"sid": "HXpending",
			"friendly_name": "draft",
			"types": {"twilio/text": {"body": "..."}},
			"approval_requests": [{"status": "pending"}]
		}]
	}`)}
	plat := newPhoneStub(twilioListReply)
	ctx := newTestCtx(t, plat)
	app := &App{}

	_, _ = app.toolTemplatesSyncProvider(ctx, map[string]any{"channel": "whatsapp"})
	listed, _ := app.toolTemplateList(ctx, map[string]any{"channel": "whatsapp"})
	tplID := listed.(map[string]any)["templates"].([]*Template)[0].ID

	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel":     "whatsapp",
		"from":        "+15551112222",
		"to":          "+15553334444",
		"template_id": tplID,
	})
	if err == nil || !strings.Contains(err.Error(), "provider_status") {
		t.Errorf("expected pending-approval error, got %v", err)
	}
}

func TestSendMessageTemplate_RejectsCrossChannelMismatch(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Local SMS template.
	create, _ := app.toolTemplateCreate(ctx, map[string]any{
		"name":      "alert",
		"channel":   "sms",
		"body_text": "Heads up",
	})
	tplID := create.(map[string]any)["template"].(*Template).ID

	_, err := app.toolSendMessage(ctx, map[string]any{
		"channel":     "email", // mismatch
		"from":        "noreply@x.com",
		"to":          "alice@x.com",
		"template_id": tplID,
	})
	if err == nil || !strings.Contains(err.Error(), "channel") {
		t.Errorf("expected channel-mismatch error, got %v", err)
	}
}

func TestSendMessageSMS_UsesDefaultSenderAndStatusCallback(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.whoAmIOverride = &sdk.InstallIdentity{
		AppName:   "messaging",
		ProjectID: "test-proj",
		PublicURL: "https://test.apteva.ai",
	}
	plat.replyByTool = map[string]*sdk.ExecuteResult{}
	plat.replyByTool["send_sms"] = &sdk.ExecuteResult{
		Success: true, Status: 201,
		Data: json.RawMessage(`{"sid":"SMsms1"}`),
	}
	ctx := newTestCtx(t, plat)
	t.Setenv("APTEVA_APP_TOKEN", "tok")
	preseedSender(t, ctx, senderUpsert{
		Channel: "sms", Address: "+15551112222", Kind: "phone",
		Provider: "twilio", ProviderIdentityID: "PNsms",
		Verified: true, VerificationStatus: "verified", SendingEnabled: true,
	})
	if err := dbSetDefaultSender(ctx.AppDB(), "test-proj", "sms", "+15551112222"); err != nil {
		t.Fatal(err)
	}
	app := &App{}

	if _, err := app.toolSendMessage(ctx, map[string]any{
		"channel": "sms",
		"to":      "+15553334444",
		"body":    "hello sms",
	}); err != nil {
		t.Fatal(err)
	}
	var sendCall *executeCall
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "send_sms" {
			sendCall = &plat.executeCalls[i]
			break
		}
	}
	if sendCall == nil {
		t.Fatal("send_sms was not called")
	}
	if sendCall.Input["From"] != "+15551112222" {
		t.Errorf("From=%v, want default sender", sendCall.Input["From"])
	}
	cb, _ := sendCall.Input["StatusCallback"].(string)
	if !strings.Contains(cb, "/api/apps/messaging/webhooks/twilio-status") ||
		!strings.Contains(cb, "project_id=test-proj") ||
		!strings.Contains(cb, "api_key=tok") {
		t.Errorf("StatusCallback=%q", cb)
	}
}

func TestSendersCreateSMS_WiresSmsMethodPost(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.whoAmIOverride = &sdk.InstallIdentity{
		AppName:   "messaging",
		ProjectID: "test-proj",
		PublicURL: "https://test.apteva.ai",
	}
	plat.replyByTool = map[string]*sdk.ExecuteResult{
		"list_phone_numbers": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PNsms","phone_number":"+15551112222","sms_url":"","sms_method":""}]}`),
		},
		"update_phone_number": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
	}
	ctx := newTestCtx(t, plat)
	t.Setenv("APTEVA_APP_TOKEN", "tok")
	app := &App{}

	if _, err := app.toolSendersCreate(ctx, map[string]any{
		"channel": "sms",
		"address": "+15551112222",
		"inbound": "true",
	}); err != nil {
		t.Fatal(err)
	}
	var updateCall *executeCall
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "update_phone_number" {
			updateCall = &plat.executeCalls[i]
			break
		}
	}
	if updateCall == nil {
		t.Fatal("update_phone_number was not called")
	}
	if updateCall.Input["SmsMethod"] != "POST" {
		t.Errorf("SmsMethod=%v, want POST", updateCall.Input["SmsMethod"])
	}
	if !strings.Contains(fmt.Sprint(updateCall.Input["SmsUrl"]), "/api/apps/messaging/webhooks/twilio-inbound") {
		t.Errorf("SmsUrl=%v", updateCall.Input["SmsUrl"])
	}
}

func TestSendersCreateWhatsApp_AdoptsApprovedSender(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.whoAmIOverride = &sdk.InstallIdentity{
		AppName:   "messaging",
		ProjectID: "test-proj",
		PublicURL: "https://test.apteva.ai",
	}
	plat.replyByTool = map[string]*sdk.ExecuteResult{
		"list_whatsapp_senders": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"senders":[{"sid":"WAsender","sender_id":"whatsapp:+15551112222","status":"approved"}]}`),
		},
		"update_whatsapp_sender": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendersCreate(ctx, map[string]any{
		"channel":      "whatsapp",
		"address":      "+15551112222",
		"display_name": "WhatsApp Support",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if resp.Pending {
		t.Fatalf("approved sender should not be pending: %+v", resp)
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "whatsapp", "+15551112222")
	if row == nil {
		rows, _ := dbListSenders(ctx.AppDB(), "test-proj", "", false)
		t.Fatalf("sender row not persisted; resp=%+v rows=%+v", resp, rows)
	}
	if !row.Verified || row.VerificationStatus != "verified" || row.ProviderIdentityID != "WAsender" {
		t.Fatalf("row=%+v", row)
	}
	var updateCall *executeCall
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "update_whatsapp_sender" {
			updateCall = &plat.executeCalls[i]
			break
		}
	}
	if updateCall == nil {
		t.Fatal("update_whatsapp_sender was not called")
	}
	if updateCall.Input["SenderSid"] != "WAsender" {
		t.Errorf("SenderSid=%v", updateCall.Input["SenderSid"])
	}
	webhook, _ := updateCall.Input["webhook"].(map[string]any)
	if webhook["callback_method"] != "POST" {
		t.Errorf("callback_method=%v, want POST", webhook["callback_method"])
	}
	if !strings.Contains(fmt.Sprint(webhook["callback_url"]), "/api/apps/messaging/webhooks/twilio-inbound") {
		t.Errorf("callback_url=%v", webhook["callback_url"])
	}
	if webhook["status_callback_method"] != "POST" {
		t.Errorf("status_callback_method=%v, want POST", webhook["status_callback_method"])
	}
	if !strings.Contains(fmt.Sprint(webhook["status_callback_url"]), "/api/apps/messaging/webhooks/twilio-status") {
		t.Errorf("status_callback_url=%v", webhook["status_callback_url"])
	}
}

// ─── v0.5 inbound: Twilio + STOP + verdicts ───────────────────────

func TestVerifyTwilioSignature_HappyPath(t *testing.T) {
	// Twilio's exact algorithm: HMAC-SHA1 of (URL + sorted KV pairs).
	// We replicate it once to compute the expected sig, then check
	// that verifyTwilioSignature accepts it.
	form := url.Values{
		"From":       []string{"+15551112222"},
		"To":         []string{"+15553334444"},
		"Body":       []string{"hi there"},
		"MessageSid": []string{"SMabc"},
	}
	publicURL := "https://test.apteva.ai/api/apps/messaging/webhooks/twilio-inbound"
	authToken := "supersecret"

	keys := []string{"Body", "From", "MessageSid", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !verifyTwilioSignature(publicURL, form, authToken, expected) {
		t.Errorf("expected signature to verify")
	}
	if verifyTwilioSignature(publicURL, form, "wrongtoken", expected) {
		t.Errorf("verification should fail for wrong token")
	}
	if verifyTwilioSignature(publicURL, form, authToken, "AAAA") {
		t.Errorf("verification should fail for tampered signature")
	}
}

func TestIsStopKeyword(t *testing.T) {
	for _, body := range []string{"STOP", "stop", " STOP ", "Unsubscribe", "QUIT", "OPT-OUT"} {
		if !isStopKeyword(body) {
			t.Errorf("isStopKeyword(%q) = false, want true", body)
		}
	}
	for _, body := range []string{"hello", "stop the train", "no thanks", ""} {
		if isStopKeyword(body) {
			t.Errorf("isStopKeyword(%q) = true, want false", body)
		}
	}
}

func TestTwilioInboundWebhook_PersistsSMSAndDispatches(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"twilio_auth_token": "secret",
	}))
	app := &App{}

	// Register a route so dispatch has a target.
	if _, err := app.toolInboundRouteSet(ctx, map[string]any{
		"channel":      "sms",
		"pattern":      "+15553334444",
		"target_app":   "support",
		"target_route": "/inbound",
	}); err != nil {
		t.Fatal(err)
	}

	// Build a Twilio-shaped form POST.
	form := url.Values{
		"From":       []string{"+15551112222"},
		"To":         []string{"+15553334444"},
		"Body":       []string{"need help with order #1234"},
		"MessageSid": []string{"SMtest1"},
		"AccountSid": []string{"ACtest"},
		"NumMedia":   []string{"0"},
	}
	publicURL := "https://test.apteva.ai/webhooks/twilio-inbound?project_id=test-proj"
	keys := []string{"AccountSid", "Body", "From", "MessageSid", "NumMedia", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("secret"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest("POST", "/webhooks/twilio-inbound?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	app.handleTwilioInboundWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("expected 1 inbound row, got %d", len(rows))
	}
	m := rows[0]
	if m.Channel != "sms" || m.From != "+15551112222" || m.BodyText != "need help with order #1234" {
		t.Errorf("row: %+v", m)
	}
	if m.RouteStatus != "ok" || m.RouteTargetApp != "support" {
		t.Errorf("dispatch: status=%q app=%q", m.RouteStatus, m.RouteTargetApp)
	}
}

func TestTwilioInboundWebhook_UsesBoundConnectionCredentials(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.connectionCreds = map[int64]map[string]string{
		2: map[string]string{"auth_token": "secret"},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	form := url.Values{
		"From":       []string{"+15551112222"},
		"To":         []string{"+15553334444"},
		"Body":       []string{"credential-backed signature"},
		"MessageSid": []string{"SMcred1"},
		"AccountSid": []string{"ACtest"},
	}
	publicURL := "https://test.apteva.ai/webhooks/twilio-inbound?project_id=test-proj"
	keys := []string{"AccountSid", "Body", "From", "MessageSid", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("secret"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest("POST", "/webhooks/twilio-inbound?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	app.handleTwilioInboundWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Channel: "sms", Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("expected one inbound row, got %d", len(rows))
	}
}

func TestTwilioInboundWebhook_DetectsWhatsAppByPrefix(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"twilio_auth_token": "secret",
	}))
	app := &App{}

	form := url.Values{
		"From":       []string{"whatsapp:+15551112222"},
		"To":         []string{"whatsapp:+15553334444"},
		"Body":       []string{"hello over WA"},
		"MessageSid": []string{"SMwa1"},
		"AccountSid": []string{"ACtest"},
	}
	publicURL := "https://test.apteva.ai/webhooks/twilio-inbound?project_id=test-proj"
	keys := []string{"AccountSid", "Body", "From", "MessageSid", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("secret"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest("POST", "/webhooks/twilio-inbound?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	app.handleTwilioInboundWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Channel: "whatsapp", Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("expected 1 whatsapp row, got %d", len(rows))
	}
	if rows[0].From != "+15551112222" {
		t.Errorf("From should be stripped of whatsapp: prefix; got %q", rows[0].From)
	}
}

func TestTwilioInboundWebhook_AutoSuppressesOnSTOP(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"twilio_auth_token": "secret",
	}))
	app := &App{}

	form := url.Values{
		"From":       []string{"+15551112222"},
		"To":         []string{"+15553334444"},
		"Body":       []string{"STOP"},
		"MessageSid": []string{"SMstop"},
		"AccountSid": []string{"ACtest"},
	}
	publicURL := "https://test.apteva.ai/webhooks/twilio-inbound?project_id=test-proj"
	keys := []string{"AccountSid", "Body", "From", "MessageSid", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("secret"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest("POST", "/webhooks/twilio-inbound?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	app.handleTwilioInboundWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	supps, _ := dbSuppressionList(ctx.AppDB(), "test-proj", "sms", 100)
	if len(supps) != 1 {
		t.Fatalf("expected 1 suppression, got %d: %+v", len(supps), supps)
	}
	if supps[0].Address != "+15551112222" || supps[0].Reason != "stop-keyword" {
		t.Errorf("suppression: %+v", supps[0])
	}
}

func TestTwilioInboundWebhook_RejectsBadSignature(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"twilio_auth_token": "secret",
	}))
	_ = ctx
	app := &App{}

	form := url.Values{"From": []string{"+1"}, "To": []string{"+1"}, "Body": []string{"hi"}}
	r := httptest.NewRequest("POST", "/webhooks/twilio-inbound?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", "AAAA")
	w := httptest.NewRecorder()
	app.handleTwilioInboundWebhook(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTwilioStatusWebhook_PersistsDeliveredEvent(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"twilio_auth_token": "secret",
	}))
	app := &App{}

	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'sms', 'out', '+15551112222', '["+15553334444"]', 'sent', 'SMdelivered1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()

	form := url.Values{
		"MessageSid":    []string{"SMdelivered1"},
		"MessageStatus": []string{"delivered"},
		"From":          []string{"+15551112222"},
		"To":            []string{"+15553334444"},
	}
	publicURL := "https://test.apteva.ai/webhooks/twilio-status?project_id=test-proj"
	keys := []string{"From", "MessageSid", "MessageStatus", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("secret"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest("POST", "/webhooks/twilio-status?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	app.handleTwilioStatusWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	events, _ := dbDeliveryEvents(ctx.AppDB(), msgID)
	if len(events) != 1 || events[0].Kind != "delivered" {
		t.Fatalf("events=%+v, want one delivered event", events)
	}
	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if m.Status != "delivered" {
		t.Fatalf("status=%q want delivered", m.Status)
	}
}

func TestTwilioStatusWebhook_ReadPromotesWhatsAppToOpened(t *testing.T) {
	plat := newPhoneStub(nil)
	ctx := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"twilio_auth_token": "secret",
	}))
	app := &App{}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'whatsapp', 'out', '+15551112222', '["+15553334444"]', 'delivered', 'SMread1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()
	form := url.Values{
		"MessageSid":    []string{"SMread1"},
		"MessageStatus": []string{"read"},
		"From":          []string{"whatsapp:+15551112222"},
		"To":            []string{"whatsapp:+15553334444"},
	}
	publicURL := "https://test.apteva.ai/webhooks/twilio-status?project_id=test-proj"
	keys := []string{"From", "MessageSid", "MessageStatus", "To"}
	var b strings.Builder
	b.WriteString(publicURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("secret"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	r := httptest.NewRequest("POST", "/webhooks/twilio-status?project_id=test-proj", strings.NewReader(form.Encode()))
	r.Host = "test.apteva.ai"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "test.apteva.ai")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	app.handleTwilioStatusWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if m.Status != "opened" {
		t.Fatalf("status=%q want opened", m.Status)
	}
}

func TestSESInbound_PersistsVerdicts(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// SES Received notification with a verdicts block.
	innerSES := map[string]any{
		"notificationType": "Received",
		"content":          sampleEml,
		"mail":             map[string]any{"messageId": "ses-verdicts"},
		"receipt": map[string]any{
			"spamVerdict":  map[string]any{"status": "PASS"},
			"virusVerdict": map[string]any{"status": "PASS"},
			"dkimVerdict":  map[string]any{"status": "FAIL"},
			"spfVerdict":   map[string]any{"status": "PASS"},
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", strings.NewReader(string(body)))
	r.Header.Set("X-Amz-Sns-Message-Type", "Notification")
	w := httptest.NewRecorder()
	app.handleInboundWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row")
	}
	var v map[string]string
	_ = json.Unmarshal(rows[0].Verdicts, &v)
	if v["dkim"] != "FAIL" || v["spam"] != "PASS" {
		t.Errorf("verdicts wrong: %+v", v)
	}
}

// ─── senders_create error paths ────────────────────────────────────

func TestSendersCreate_NoEmailProviderBound_Errors(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{}, // no email_provider
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	_, err := app.toolSendersCreate(ctx, map[string]any{"address": "acme.com"})
	if err == nil {
		t.Fatal("expected error when email_provider not bound, got nil")
	}
	if !strings.Contains(err.Error(), "email_provider") {
		t.Errorf("error doesn't mention email_provider: %v", err)
	}
}

func TestSenders_SetDefault_OneDefaultPerCohort(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	// Seed two email senders.
	for _, addr := range []string{"a@x.com", "b@x.com"} {
		if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{
			ProjectID: "test-proj", Channel: "email", Address: addr,
			Kind: "email_mailbox", Provider: "aws-ses", ProviderIdentityID: addr,
			Verified: true, VerificationStatus: "verified", SendingEnabled: true,
			MarkSyncedNow: true,
		}); err != nil {
			t.Fatalf("seed %s: %v", addr, err)
		}
	}
	// Set b as default.
	if _, err := app.toolSendersSetDefault(ctx, map[string]any{"address": "b@x.com"}); err != nil {
		t.Fatal(err)
	}
	def, _ := dbDefaultSender(ctx.AppDB(), "test-proj", "email")
	if def == nil || def.Address != "b@x.com" {
		t.Fatalf("expected b@x.com as default, got %+v", def)
	}
	// Flip to a — partial unique index must allow this (b's flag clears first).
	if _, err := app.toolSendersSetDefault(ctx, map[string]any{"address": "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	def, _ = dbDefaultSender(ctx.AppDB(), "test-proj", "email")
	if def == nil || def.Address != "a@x.com" {
		t.Fatalf("expected a@x.com as default after flip, got %+v", def)
	}
	// Confirm there's exactly one default by counting.
	rows, _ := dbListSenders(ctx.AppDB(), "test-proj", "email", false)
	defaults := 0
	for _, r := range rows {
		if r.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("expected 1 default sender, got %d", defaults)
	}
}

func TestSendersCreate_Domain_PublishDNSSkippedWhenNoDomainsApp(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider": float64(1),
			// domains NOT bound
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(`{
				"DkimAttributes": {"Status": "PENDING", "Tokens": ["aa", "bb", "cc"]}
			}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	outRaw, err := app.toolSendersCreate(ctx, map[string]any{"address": "acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	out := outRaw.(*sendersCreateResp)
	if len(out.DnsRecords) != 3 {
		t.Errorf("expected 3 dns_records, got %d", len(out.DnsRecords))
	}
	// publish_dns step should be skipped with a clear reason — domains app not bound.
	publishStep := false
	for _, s := range out.Steps {
		if s.Step == "publish_dns" {
			publishStep = true
			if s.Skipped == "" || !strings.Contains(s.Skipped, "domains app not bound") {
				t.Errorf("publish_dns step missing skip reason: %+v", s)
			}
		}
	}
	if !publishStep {
		t.Errorf("expected publish_dns step in %+v", out.Steps)
	}
	// No CallApp invocations should have fired.
	for _, c := range plat.callAppCalls {
		if c.App == "domains" {
			t.Errorf("CallApp(domains) shouldn't have fired when domains app not bound: %+v", c)
		}
	}
}
