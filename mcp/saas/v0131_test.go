package main

import (
	"database/sql"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// ─── Past-due actions during the initial checkout ────────────────────

func addPastDueSuspendAction(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "crm-pro",
		"event":     "account_past_due",
		"app_name":  "hosting",
		"tool_name": "hosting_tenant_suspend",
		"args":      map[string]any{"account": "{{account.id}}"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestV0131_InitialCheckoutSkipsPastDueActions(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	addPastDueSuspendAction(t, app, ctx)

	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "awaiting_payment" {
		t.Fatalf("checkout status=%v, want awaiting_payment", out.(map[string]any)["status"])
	}
	if hasCall(pf.calls, "hosting:hosting_tenant_suspend") {
		t.Fatal("past_due action ran before the first payment")
	}
	acct := mustSingleAccount(t, db)
	if acct.Status != StatusPastDue {
		t.Fatalf("account status=%s, want past_due", acct.Status)
	}
	if n := countEvents(t, db, "fulfillment.failed"); n != 0 {
		t.Fatalf("fulfillment.failed events=%d, want 0", n)
	}
	if n := countEvents(t, db, "fulfillment.skipped"); n != 1 {
		t.Fatalf("fulfillment.skipped events=%d, want 1", n)
	}
	// subscription.past_due arriving mid-checkout is skipped too.
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"subscription_id": 601, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	if hasCall(pf.calls, "hosting:hosting_tenant_suspend") {
		t.Fatal("subscription.past_due during the initial checkout ran the past_due action")
	}
}

func TestV0131_RenewalFailureAfterFirstPaymentRunsPastDueActions(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	addPastDueSuspendAction(t, app, ctx)

	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if _, err := app.toolCheckoutMarkPaid(ctx, map[string]any{"checkout_id": checkout.ID, "amount_cents": 2900, "method": "wire"}); err != nil {
		t.Fatal(err)
	}
	acct := mustSingleAccount(t, db)
	if acct.Status != StatusActive || strArg(mapFromAny(acct.Metadata), "checkout_status") != "active" {
		t.Fatalf("paid account status=%s checkout_status=%s", acct.Status, strArg(mapFromAny(acct.Metadata), "checkout_status"))
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"subscription_id": 601, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "hosting", "hosting_tenant_suspend"); got != 1 {
		t.Fatalf("renewal past_due should run the suspend action once, got %d", got)
	}
}

func TestV0131_AccountAwaitingFirstPayment(t *testing.T) {
	cases := []struct {
		meta string
		want bool
	}{
		{`{"checkout_status":"processing"}`, true},
		{`{"checkout_status":"awaiting_payment"}`, true},
		{`{"checkout_status":"active"}`, false},
		{`{"checkout_status":"awaiting_payment","trial_ends_at":"2026-12-01T00:00:00Z"}`, false},
		{`{"checkout_status":"awaiting_payment_method"}`, false},
		{`{}`, false},
	}
	for _, c := range cases {
		if got := accountAwaitingFirstPayment(&Account{Metadata: []byte(c.meta)}); got != c.want {
			t.Errorf("meta %s: got %v, want %v", c.meta, got, c.want)
		}
	}
}

// ─── Email delivery ─────────────────────────────────────────────────

func countEvents(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM saas_events WHERE event_type=?`, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sendCalls(calls []callAppCall) []callAppCall {
	var out []callAppCall
	for _, c := range calls {
		if c.App == "messaging" && c.Tool == "send_message" {
			out = append(out, c)
		}
	}
	return out
}

func outboxRow(t *testing.T, db *sql.DB) (status string, attempts int64, sendKey, lastError string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, attempts, send_key, last_error FROM saas_email_outbox`).Scan(&status, &attempts, &sendKey, &lastError); err != nil {
		t.Fatal(err)
	}
	return
}

func makeOutboxDue(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE saas_email_outbox SET next_attempt_at=datetime('now', '-1 minute')`); err != nil {
		t.Fatal(err)
	}
}

// emailTestAccount creates an account on a free plan and returns it
// without sending any email.
func emailTestAccount(t *testing.T, app *App, ctx *sdk.AppCtx, db *sql.DB, planMeta map[string]any) *Account {
	t.Helper()
	meta := map[string]any{}
	for k, v := range planMeta {
		meta[k] = v
	}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm-pro", "name": "CRM Pro", "metadata": meta}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	return created.(map[string]any)["account"].(*Account)
}

func TestV0131_EmailSendsChannelAndPlanSender(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := emailTestAccount(t, app, ctx, db, map[string]any{"email_from": "contact@mycloudmac.com", "email_from_name": "My Cloud Mac"})

	app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "29.00 USD", "", "")
	sends := sendCalls(pf.calls)
	if len(sends) != 1 {
		t.Fatalf("sends=%d, want 1", len(sends))
	}
	in := sends[0].Input
	if in["channel"] != "email" || in["to"] != "buyer@example.com" {
		t.Fatalf("channel=%v to=%v", in["channel"], in["to"])
	}
	if in["from"] != "contact@mycloudmac.com" || in["from_name"] != "My Cloud Mac" {
		t.Fatalf("from=%v from_name=%v", in["from"], in["from_name"])
	}
	if status, attempts, _, _ := outboxRow(t, db); status != emailOutboxSent || attempts != 1 {
		t.Fatalf("outbox status=%s attempts=%d", status, attempts)
	}
	if countEvents(t, db, "notification.sent") != 1 {
		t.Fatal("want one notification.sent")
	}
	// The same email again is not resent.
	app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "29.00 USD", "", "")
	if len(sendCalls(pf.calls)) != 1 {
		t.Fatal("a repeated email was sent twice")
	}
}

func TestV0131_EmailWithoutPlanSenderUsesProjectDefault(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := emailTestAccount(t, app, ctx, db, nil)
	app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "", "", "")
	in := sendCalls(pf.calls)[0].Input
	if _, ok := in["from"]; ok {
		t.Fatalf("from should be left to Messaging's default sender, got %v", in["from"])
	}
	if _, ok := in["from_name"]; ok {
		t.Fatalf("from_name should be omitted, got %v", in["from_name"])
	}
}

func TestV0131_ErrorRetriesWithSameKey(t *testing.T) {
	pf := &platformStub{failureMessages: map[string]string{"messaging:send_message": "from: required"}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := emailTestAccount(t, app, ctx, db, nil)
	app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "", "", "")
	baseKey := "saas:" + acct.ID + ":payment_link:702"

	status, attempts, sendKey, lastErr := outboxRow(t, db)
	if status != emailOutboxPending || attempts != 1 || sendKey != baseKey || !strings.Contains(lastErr, "from: required") {
		t.Fatalf("after error: status=%s attempts=%d key=%s err=%q", status, attempts, sendKey, lastErr)
	}
	if countEvents(t, db, "notification.failed") != 1 || countEvents(t, db, "notification.sent") != 0 {
		t.Fatal("an error must record notification.failed only")
	}
	// Not due yet: the worker leaves it.
	if err := app.retryOutboxEmails(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sendCalls(pf.calls)) != 1 {
		t.Fatal("worker retried before the backoff")
	}
	pf.failureMessages = nil
	makeOutboxDue(t, db)
	if err := app.retryOutboxEmails(ctx); err != nil {
		t.Fatal(err)
	}
	sends := sendCalls(pf.calls)
	if len(sends) != 2 || sends[1].Input["idempotency_key"] != baseKey {
		t.Fatalf("retry sends=%d key=%v, want same key %s", len(sends), sends[len(sends)-1].Input["idempotency_key"], baseKey)
	}
	if status, attempts, _, _ := outboxRow(t, db); status != emailOutboxSent || attempts != 2 {
		t.Fatalf("after retry: status=%s attempts=%d", status, attempts)
	}
}

func TestV0131_FailedStatusRetriesWithNewKey(t *testing.T) {
	pf := &platformStub{messagingStatuses: []string{"failed", "failed"}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := emailTestAccount(t, app, ctx, db, nil)
	if app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "", "", ""); countEvents(t, db, "notification.sent") != 0 {
		t.Fatal("status failed must not be recorded as sent")
	}
	baseKey := "saas:" + acct.ID + ":payment_link:702"
	if status, _, sendKey, lastErr := outboxRow(t, db); status != emailOutboxPending || sendKey != baseKey+":retry:2" || !strings.Contains(lastErr, "provider rejected") {
		t.Fatalf("after failed: status=%s key=%s err=%q", status, sendKey, lastErr)
	}
	makeOutboxDue(t, db)
	_ = app.retryOutboxEmails(ctx)
	makeOutboxDue(t, db)
	_ = app.retryOutboxEmails(ctx)
	sends := sendCalls(pf.calls)
	want := []string{baseKey, baseKey + ":retry:2", baseKey + ":retry:3"}
	if len(sends) != len(want) {
		t.Fatalf("sends=%d, want %d", len(sends), len(want))
	}
	for i, k := range want {
		if sends[i].Input["idempotency_key"] != k {
			t.Fatalf("send %d key=%v, want %s", i, sends[i].Input["idempotency_key"], k)
		}
	}
	if status, attempts, _, _ := outboxRow(t, db); status != emailOutboxSent || attempts != 3 {
		t.Fatalf("final status=%s attempts=%d", status, attempts)
	}
	if countEvents(t, db, "notification.failed") != 2 || countEvents(t, db, "notification.sent") != 1 {
		t.Fatal("want 2 notification.failed and 1 notification.sent")
	}
}

func TestV0131_PendingStatusIsNeverRetried(t *testing.T) {
	pf := &platformStub{messagingStatuses: []string{"pending"}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := emailTestAccount(t, app, ctx, db, nil)
	app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "", "", "")
	makeOutboxDue(t, db)
	_ = app.retryOutboxEmails(ctx)
	if len(sendCalls(pf.calls)) != 1 {
		t.Fatal("a pending email was sent again")
	}
	if status, _, _, _ := outboxRow(t, db); status != emailOutboxSent {
		t.Fatalf("pending outbox status=%s, want sent", status)
	}
}

func TestV0131_OutboxGivesUpAfterMaxAttempts(t *testing.T) {
	pf := &platformStub{failureMessages: map[string]string{"messaging:send_message": "app messaging is not installed"}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	acct := emailTestAccount(t, app, ctx, db, nil)
	app.sendPaymentLinkEmail(ctx, "proj-test", acct, 702, "https://pay.example/x", "", "", "")
	for i := 0; i < emailOutboxMaxAttempts+2; i++ {
		makeOutboxDue(t, db)
		_ = app.retryOutboxEmails(ctx)
	}
	if got := len(sendCalls(pf.calls)); got != emailOutboxMaxAttempts {
		t.Fatalf("sends=%d, want %d", got, emailOutboxMaxAttempts)
	}
	if status, _, _, _ := outboxRow(t, db); status != emailOutboxAbandoned {
		t.Fatalf("status=%s, want abandoned", status)
	}
}

func TestV0131_PlanEmailSenderValidation(t *testing.T) {
	bad := []map[string]any{
		{"email_from": "not-an-email"},
		{"email_from": "My Cloud Mac <contact@mycloudmac.com>"},
		{"email_from": 12},
		{"email_from_name": "line\nbreak"},
	}
	for _, m := range bad {
		if err := validatePlanEmailSender(m); err == nil {
			t.Errorf("metadata %v should be rejected", m)
		}
	}
	good := []map[string]any{
		{},
		{"email_from": "contact@mycloudmac.com", "email_from_name": "My Cloud Mac"},
		{"email_from": ""},
	}
	for _, m := range good {
		if err := validatePlanEmailSender(m); err != nil {
			t.Errorf("metadata %v: %v", m, err)
		}
	}
	if emailOutboxBackoff(1) != emailOutboxBaseBackoff || emailOutboxBackoff(30) != emailOutboxMaxBackoff {
		t.Fatal("backoff bounds")
	}
}
