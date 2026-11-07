package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func accountTestObservation() accountObservation {
	return accountObservation{Project: "project-a", Provider: "telnyx", Connection: 10, Scope: "api:dial_call", Operation: "outbound_calls", Reason: "insufficient_credit", Source: "carrier_api", At: time.Now().Add(-time.Minute)}
}
func accountTestRecord(t *testing.T, db *sql.DB, o accountObservation) {
	t.Helper()
	if err := recordAccountObservation(db, o); err != nil {
		t.Fatal(err)
	}
}
func accountTestCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func accountTestEvents(t *testing.T, db *sql.DB) int {
	return accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_event_outbox`)
}
func TestAccountEventClassification(t *testing.T) {
	for _, tt := range []struct {
		status     int
		body, want string
	}{
		{402, `{"errors":[{"code":"insufficient_funds","detail":"secret +15551231234"}]}`, "insufficient_credit"},
		{403, `{"error":{"message":"Account suspended"}}`, "account_suspended"},
		{403, `{"message":"Forbidden"}`, ""},
		{400, `{"metadata":{"message":"insufficient funds"}}`, ""},
		{400, `{"message":"not insufficient funds"}`, ""},
		{402, `{}`, "billing_blocked"},
		{401, `{}`, "authentication_failed"},
		{429, `{}`, "rate_limited"},
		{503, `{"errors":[{"code":"10007","title":"Unexpected error"}]}`, "provider_unavailable"},
		{400, `{"message":"insufficient credit"}`, "insufficient_credit"},
		{400, `{"message":"insufficient credit","padding":"` + strings.Repeat("x", 65536) + `"}`, ""},
	} {
		t.Run(fmt.Sprint(tt.status, tt.want), func(t *testing.T) {
			if got := accountAPIReason(tt.status, json.RawMessage(tt.body)); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		status       int
		phrase, want string
	}{
		{403, "Forbidden", ""}, {486, "Busy Here", ""}, {480, "Temporarily Unavailable", ""}, {503, "Service Unavailable", ""},
		{401, "Insufficient funds", ""}, {407, "Proxy Authentication Required", ""},
		{403, "Insufficient funds", "insufficient_credit"}, {402, "Payment Required", "billing_blocked"},
	} {
		if got := accountSIPReason(tt.status, tt.phrase); got != tt.want {
			t.Fatalf("SIP %d: %q", tt.status, got)
		}
	}
}
func TestAccountEventsDeduplicateConcurrentAndIsolate(t *testing.T) {
	ctx := softphoneTestCtx(t)
	db := ctx.AppDB()
	base := accountTestObservation()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := base
			o.At = o.At.Add(time.Duration(i) * time.Millisecond)
			if err := recordAccountObservation(db, o); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := accountTestEvents(t, db); n != 1 {
		t.Fatalf("repeated failures emitted %d", n)
	}
	for _, o := range []accountObservation{
		func() accountObservation { v := base; v.Connection = 11; return v }(),
		func() accountObservation { v := base; v.Project = "project-b"; return v }(),
	} {
		accountTestRecord(t, db, o)
	}
	recovery := base
	recovery.At = base.At.Add(time.Second)
	recovery.Scope = "outbound_confirmed"
	recovery.Success = true
	recovery.Reason = ""
	recovery.OutboundConfirmed = true
	accountTestRecord(t, db, recovery)
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_issues WHERE active=1`); n != 2 {
		t.Fatalf("cross-account recovery: %d active", n)
	}
	if n := accountTestEvents(t, db); n != 4 {
		t.Fatalf("events %d", n)
	}
	accountTestRecord(t, db, base)     // Older failure cannot reopen.
	accountTestRecord(t, db, recovery) // Duplicate answer cannot repeat recovery.
	if n := accountTestEvents(t, db); n != 4 {
		t.Fatalf("stale/duplicate changed events %d", n)
	}
	base.At = recovery.At.Add(time.Second)
	accountTestRecord(t, db, base)
	if n := accountTestEvents(t, db); n != 5 {
		t.Fatalf("new failure not emitted: %d", n)
	}
}
func TestAccountEventsRecoveryEvidence(t *testing.T) {
	ctx := softphoneTestCtx(t)
	db := ctx.AppDB()
	o := accountTestObservation()
	accountTestRecord(t, db, o)
	accepted := o
	accepted.Success = true
	accepted.Reason = ""
	accepted.At = o.At.Add(time.Second)
	accountTestRecord(t, db, accepted) // Accepted placement is not a confirmed outbound answer.
	inventory := accepted
	inventory.Scope = "api:list_phone_numbers"
	inventory.Operation = "inventory"
	accountTestRecord(t, db, inventory)
	if n := accountTestEvents(t, db); n != 1 {
		t.Fatalf("weak evidence cleared credit: %d", n)
	}
	app := &App{}
	row := insertSoftphoneCall(t, app, "initiated")
	row.CarrierSlug = "telnyx"
	facts := lifecycleFacts{Source: "provider", OccurredAt: time.Now().Format(time.RFC3339Nano)}
	row.Direction = "inbound"
	app.db().observeAccountCall(&row, "answered", "", facts)
	row.Direction = "outbound"
	row.PlacedAt = o.At.Add(-time.Second).Format(time.RFC3339Nano)
	app.db().observeAccountCall(&row, "answered", "", facts)
	if n := accountTestEvents(t, db); n != 1 {
		t.Fatalf("inbound/old call cleared credit: %d", n)
	}
	row.PlacedAt = o.At.Add(2 * time.Second).Format(time.RFC3339Nano)
	app.db().observeAccountCall(&row, "answered", "", facts)
	if n := accountTestEvents(t, db); n != 2 {
		t.Fatalf("confirmed new call did not recover %d", n)
	}
	// A healthy SIP call must not clear REST authentication failures.
	o.Reason = "authentication_failed"
	o.At = time.Now().Add(-time.Second)
	accountTestRecord(t, db, o)
	row.PlacedAt = time.Now().Format(time.RFC3339Nano)
	app.db().observeAccountCall(&row, "answered", "", facts)
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_issues WHERE reason='authentication_failed' AND active=1`); n != 1 {
		t.Fatal("SIP cleared API authentication")
	}
	accepted.At = time.Now()
	accountTestRecord(t, db, accepted)
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_issues WHERE active=1`); n != 0 {
		t.Fatalf("API success did not clear auth: %d", n)
	}
}
func TestAccountEventsTransientScopesAndLateFailure(t *testing.T) {
	ctx := softphoneTestCtx(t)
	db := ctx.AppDB()
	o := accountTestObservation()
	o.Reason = "provider_unavailable"
	o.Scope = "api:list_phone_numbers"
	o.Operation = "inventory"
	accountTestRecord(t, db, o)
	other := o
	other.At = o.At.Add(time.Second)
	other.Success = true
	other.Reason = ""
	other.Scope = "api:dial_call"
	accountTestRecord(t, db, other)
	if n := accountTestEvents(t, db); n != 1 {
		t.Fatal("other operation cleared inventory")
	}
	other.Scope = o.Scope
	accountTestRecord(t, db, other)
	accountTestRecord(t, db, o)
	if n := accountTestEvents(t, db); n != 2 {
		t.Fatalf("late response reopened issue %d", n)
	}
	// A success that arrived before the first failed response still wins.
	o.Connection = 22
	other.Connection = 22
	accountTestRecord(t, db, other)
	accountTestRecord(t, db, o)
	if n := accountTestEvents(t, db); n != 2 {
		t.Fatal("late first response created obsolete issue")
	}
	// Conversely, REST acceptance must not erase a delayed credit rejection.
	o.Connection = 23
	o.Scope = "api:dial_call"
	o.Operation = "outbound_calls"
	o.Reason = "insufficient_credit"
	other.Connection = 23
	other.Scope = o.Scope
	accountTestRecord(t, db, other)
	accountTestRecord(t, db, o)
	if n := accountTestEvents(t, db); n != 3 {
		t.Fatal("acceptance erased credit evidence")
	}
}

type accountEventPlatform struct {
	*answerPlatform
	response *sdk.ExecuteResult
	failure  error
}

func (p *accountEventPlatform) ExecuteIntegrationTool(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
	return p.response, p.failure
}

func TestAccountEventsAPIIntegrationAndFailureIsolation(t *testing.T) {
	var lookups atomic.Int32
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		if r.URL.Path != "/api/apps/callback/connections/10" || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("incorrect metadata request")
		}
		_ = json.NewEncoder(w).Encode(sdk.PlatformConnection{AppSlug: "telnyx"})
	}))
	defer metadata.Close()
	t.Setenv("APTEVA_GATEWAY_URL", metadata.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test")
	p := &accountEventPlatform{answerPlatform: multiCarrierPlatform(), response: &sdk.ExecuteResult{Success: false, Status: 402, Data: json.RawMessage(`{"errors":[{"code":"insufficient_funds","detail":"token-secret +15551231234"}]}`)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	_, err := executeCarrierTool(ctx, 10, "dial_call", map[string]any{"to": "+15551231234", "api_key": "token-secret"})
	var carrierErr *carrierHTTPError
	if !errors.As(err, &carrierErr) || carrierErr.status != 402 {
		t.Fatalf("carrier result changed: %v", err)
	}
	if n := accountTestEvents(t, ctx.AppDB()); n != 1 {
		t.Fatalf("missing API event: %d", n)
	}
	var payload string
	if err = ctx.AppDB().QueryRow(`SELECT payload_json FROM telephony_account_event_outbox`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "token-secret") || strings.Contains(payload, "+15551231234") {
		t.Fatalf("sensitive data leaked: %s", payload)
	}
	p.response = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"ok":true}`)}
	if _, err = executeCarrierTool(ctx, 10, "dial_call", nil); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 {
		t.Fatal("successful operation added gateway lookups")
	}
	if n := accountTestEvents(t, ctx.AppDB()); n != 1 {
		t.Fatal("HTTP acceptance cleared billing")
	}
	if _, err = ctx.AppDB().Exec(`DROP TABLE telephony_account_observations`); err != nil {
		t.Fatal(err)
	}
	if _, err = executeCarrierTool(ctx, 10, "dial_call", nil); err != nil {
		t.Fatalf("event persistence failure broke calling: %v", err)
	}
	p.response = nil
	p.failure = errors.New("network unavailable")
	if _, err = executeCarrierTool(ctx, 10, "dial_call", nil); err != p.failure {
		t.Fatal("network error changed")
	}
}

func TestAccountEventsSIPAndAuthenticatedCallback(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := insertSoftphoneCall(t, app, "initiated")
	app.db().observeAccountSIP(row.ID, 407, "Proxy Authentication Required")
	app.db().observeAccountSIP(row.ID, 486, "Busy Here")
	if n := accountTestEvents(t, ctx.AppDB()); n != 0 {
		t.Fatal("ordinary SIP outcomes classified as account issue")
	}
	app.db().observeAccountSIP(row.ID, 403, "Insufficient funds")
	if n := accountTestEvents(t, ctx.AppDB()); n != 1 {
		t.Fatal("explicit SIP credit failure missing")
	}
	form := url.Values{"CallStatus": {"failed"}, "ErrorMessage": {"Account suspended"}, "Timestamp": {time.Now().Format(time.RFC3339Nano)}}
	request := func(token string) *http.Request {
		r := httptest.NewRequest("POST", "/webhook/status/"+row.ID+"?token="+token, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Twilio-Signature", twilioTestSignature(app.publicRequestURL(r), form, "test-auth-token"))
		return r
	}
	denied := httptest.NewRecorder()
	app.handleStatusCallback(denied, request("bad-token"))
	if denied.Code != 403 || accountTestEvents(t, ctx.AppDB()) != 1 {
		t.Fatal("unauthenticated callback affected account events")
	}
	good := httptest.NewRecorder()
	app.handleStatusCallback(good, request(row.CallbackSecret))
	if good.Code >= 300 || accountTestEvents(t, ctx.AppDB()) != 2 {
		t.Fatalf("callback event missing: status %d body %s", good.Code, good.Body.String())
	}
}

func TestAccountEventsOutboxRetryOrderingAndRestart(t *testing.T) {
	ctx := softphoneTestCtx(t)
	db := ctx.AppDB()
	o := accountTestObservation()
	accountTestRecord(t, db, o)
	o.Success = true
	o.OutboundConfirmed = true
	o.Scope = "outbound_confirmed"
	o.Reason = ""
	o.At = o.At.Add(time.Second)
	accountTestRecord(t, db, o)
	var mu sync.Mutex
	var bodies []map[string]any
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app-events/internal/emit" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong bus request")
		}
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, b)
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	app := &App{}
	if err := app.publishAccountEvents(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_event_outbox WHERE published_ms>0`); n != 0 {
		t.Fatal("failed request acknowledged")
	}
	if err := app.publishAccountEvents(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(bodies) != 1 {
		t.Error("ignored retry backoff or sent recovery first")
	}
	fail = false
	mu.Unlock()
	if _, err := db.Exec(`UPDATE telephony_account_event_outbox SET due_ms=0`); err != nil {
		t.Fatal(err)
	}
	// A new App instance has no in-memory queue; publication resumes from DB.
	app = &App{}
	if err := app.publishAccountEvents(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.publishAccountEvents(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("sends: %d", len(bodies))
	}
	if bodies[0]["event_id"] != bodies[1]["event_id"] || bodies[1]["topic"] != "telephony.account.issue" || bodies[2]["topic"] != "telephony.account.recovered" {
		t.Fatalf("bad dedup/order: %#v", bodies)
	}
	for _, b := range bodies {
		if b["project_id"] != "project-a" || b["event_id"] != b["data"].(map[string]any)["event_id"] {
			t.Fatal("bad project/envelope")
		}
	}
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_event_outbox WHERE published_ms>0`); n != 2 {
		t.Fatalf("published: %d", n)
	}
}

func TestAccountEventsOutboxScopeTimeoutAndLease(t *testing.T) {
	ctx := softphoneTestCtx(t)
	o := accountTestObservation()
	accountTestRecord(t, ctx.AppDB(), o)
	o.Project = "project-b"
	accountTestRecord(t, ctx.AppDB(), o)
	t.Setenv("APTEVA_GATEWAY_URL", "")
	t.Setenv("APTEVA_APP_TOKEN", "")
	if err := (&App{}).publishAccountEvents(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := accountTestCount(t, ctx.AppDB(), `SELECT SUM(attempts) FROM telephony_account_event_outbox`); n != 0 {
		t.Fatal("missing gateway consumed attempts")
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(204)
	}))
	defer server.Close()
	defer close(release)
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test")
	done := make(chan error, 1)
	go func() { done <- (&App{}).publishAccountEvents(context.Background(), ctx) }()
	<-entered
	if err := (&App{}).publishAccountEvents(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := accountTestCount(t, ctx.AppDB(), `SELECT SUM(attempts) FROM telephony_account_event_outbox`); n != 1 {
		t.Fatal("lease/scope allowed duplicate publication")
	}
	if n := accountTestCount(t, ctx.AppDB(), `SELECT COUNT(*) FROM telephony_account_event_outbox WHERE published_ms>0`); n != 0 {
		t.Fatal("timed-out send acknowledged")
	}
}

func TestAccountEventsAtomicPersistenceAndDatabaseReopen(t *testing.T) {
	path := t.TempDir() + "/accounts.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	migration, err := os.ReadFile("migrations/064_account_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER reject_account_event BEFORE INSERT ON telephony_account_event_outbox BEGIN SELECT RAISE(ABORT,'test storage failure'); END;`); err != nil {
		t.Fatal(err)
	}
	o := accountTestObservation()
	if err = recordAccountObservation(db, o); err == nil {
		t.Fatal("storage failure was hidden")
	}
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_issues`); n != 0 {
		t.Fatal("issue committed without its event")
	}
	if n := accountTestCount(t, db, `SELECT COUNT(*) FROM telephony_account_observations`); n != 0 {
		t.Fatal("failed transition consumed observation watermark")
	}
	if _, err = db.Exec(`DROP TRIGGER reject_account_event`); err != nil {
		t.Fatal(err)
	}
	accountTestRecord(t, db, o)
	var id string
	if err = db.QueryRow(`SELECT event_id FROM telephony_account_event_outbox`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	o.At = o.At.Add(time.Second)
	accountTestRecord(t, db, o)
	if n := accountTestEvents(t, db); n != 1 {
		t.Fatal("database reopen lost deduplication")
	}
	var restored string
	if err = db.QueryRow(`SELECT event_id FROM telephony_account_event_outbox WHERE published_ms=0`).Scan(&restored); err != nil || restored != id {
		t.Fatalf("pending event not preserved: %q %v", restored, err)
	}
}

func TestAccountEventsLocalLifecycleCannotForgeIssueOrRecovery(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{}
	row := insertSoftphoneCall(t, app, "initiated")
	if _, err := app.db().updateStatusWithFacts(row.ID, "failed", "insufficient funds", lifecycleFacts{Source: "telephony"}); err != nil {
		t.Fatal(err)
	}
	if n := accountTestEvents(t, ctx.AppDB()); n != 0 {
		t.Fatal("local error classified as provider evidence")
	}
}

func TestAccountEventsMetadataLookupBounded(t *testing.T) {
	ctx := softphoneTestCtx(t)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(sdk.PlatformConnection{AppSlug: "telnyx"})
	}))
	defer server.Close()
	defer close(release)
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test")
	start := time.Now()
	if got := accountProvider(ctx, 10); got != "" {
		t.Fatal("late metadata accepted")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("metadata blocked carrier result: %v", elapsed)
	}
}
