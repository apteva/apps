package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func setupManualCRMPlan(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	if _, err := app.toolPlanUpsert(ctx, map[string]any{
		"key":                   "crm-pro",
		"name":                  "CRM Pro",
		"billing_mode":          "paid",
		"catalog_product_id":    7001,
		"catalog_price_id":      8001,
		"subscription_required": true,
		"metadata":              map[string]any{"collection_method": "send_invoice"},
	}); err != nil {
		t.Fatal(err)
	}
}

func mustSingleAccount(t *testing.T, db *sql.DB) *Account {
	t.Helper()
	accounts, err := dbAccountList(db, "proj-test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts=%d, want 1", len(accounts))
	}
	return accounts[0]
}

func TestCycleDue_ManualCollectionEmailsPaymentLink(t *testing.T) {
	pf := &platformStub{priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": false}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupManualCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 803, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	send := findCall(pf.calls, "messaging", "send_message")
	if send == nil {
		t.Fatalf("manual-collection cycle should email the payment link: %+v", pf.calls)
	}
	if send.Input["to"] != "mailto:buyer@example.com" {
		t.Fatalf("payment link email to=%v, want mailto:buyer@example.com", send.Input["to"])
	}
	if subject := strFromAny(send.Input["subject"]); !strings.Contains(subject, "Payment due for CRM Pro") {
		t.Fatalf("payment link email subject=%q", subject)
	}
	if body := strFromAny(send.Input["body"]); !strings.Contains(body, "https://pay.example/session") || !strings.Contains(body, "29.00 USD") {
		t.Fatalf("payment link email body missing link or amount: %q", body)
	}
	acct := mustSingleAccount(t, db)
	wantKey := "saas:" + acct.ID + ":payment_link:702"
	if key := strFromAny(send.Input["idempotency_key"]); key != wantKey {
		t.Fatalf("payment link idempotency_key=%q, want %q", key, wantKey)
	}
	var sent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM saas_events WHERE event_type='notification.sent'`).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("notification.sent events=%d, want 1", sent)
	}
}

func TestCycleDue_AutomaticCollectionSendsNoEmail(t *testing.T) {
	pf := paidInvoicePlatformStub()
	pf.priceTrialDays = 7
	pf.priceMetadata = map[string]any{"trial_requires_payment_method": false}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 803, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if !hasCall(pf.calls, "billing:invoices_collect") {
		t.Fatalf("expected automatic collection: %+v", pf.calls)
	}
	if hasCall(pf.calls, "messaging:send_message") {
		t.Fatal("automatically collected cycle must not email a payment link")
	}
}

func TestInvoicePaid_EmailsReceipt(t *testing.T) {
	pf := paidInvoicePlatformStub()
	pf.priceTrialDays = 7
	pf.priceMetadata = map[string]any{"trial_requires_payment_method": false}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 803, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "paid"}}); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != 1 {
		t.Fatalf("send_message calls=%d, want exactly the receipt", got)
	}
	send := findCall(pf.calls, "messaging", "send_message")
	if subject := strFromAny(send.Input["subject"]); !strings.Contains(subject, "Payment received — invoice #702") {
		t.Fatalf("receipt subject=%q", subject)
	}
	if body := strFromAny(send.Input["body"]); !strings.Contains(body, "29.00 USD") {
		t.Fatalf("receipt body missing amount: %q", body)
	}
	acct := mustSingleAccount(t, db)
	if key := strFromAny(send.Input["idempotency_key"]); key != "saas:"+acct.ID+":receipt:702" {
		t.Fatalf("receipt idempotency_key=%q", key)
	}
}

func TestInvoicePaid_MessagingFailureDoesNotFailHandler(t *testing.T) {
	pf := paidInvoicePlatformStub()
	pf.priceTrialDays = 7
	pf.priceMetadata = map[string]any{"trial_requires_payment_method": false}
	pf.failureMessages = map[string]string{"messaging:send_message": "app messaging is not installed"}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 803, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "paid"}}); err != nil {
		t.Fatalf("missing messaging app must not fail payment activation: %v", err)
	}
	var failed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM saas_events WHERE event_type='notification.failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 1 {
		t.Fatalf("notification.failed events=%d, want 1", failed)
	}
}

func TestCollectionFailed_EmailsPaymentFailedNotice(t *testing.T) {
	pf := &platformStub{priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": false}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupManualCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 803, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	linkEmails := countCalls(pf.calls, "messaging", "send_message")
	if err := app.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: "invoice.payment_failed", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "payment_failed"}}); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != linkEmails+1 {
		t.Fatalf("payment failure should send one notice, got %d new sends", got-linkEmails)
	}
	send := pf.calls[len(pf.calls)-1]
	for i := len(pf.calls) - 1; i >= 0; i-- {
		if pf.calls[i].App == "messaging" {
			send = pf.calls[i]
			break
		}
	}
	if subject := strFromAny(send.Input["subject"]); !strings.Contains(subject, "Payment failed for CRM Pro") {
		t.Fatalf("failure notice subject=%q", subject)
	}
	if body := strFromAny(send.Input["body"]); !strings.Contains(body, "https://pay.example/session") {
		t.Fatalf("failure notice should include the retry link: %q", body)
	}
	// Voided invoices are operator actions and must not notify.
	if err := app.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: "invoice.voided", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "voided"}}); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != linkEmails+1 {
		t.Fatalf("voided invoice must not email, got %d sends", got)
	}
}

func TestTrialReminder_SendsOnceWithinWindow(t *testing.T) {
	pf := &platformStub{priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": false}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "trial@example.com", "slug": "trial", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	// A 7-day trial is outside the 48h reminder window.
	if err := app.runTrialReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if hasCall(pf.calls, "messaging:send_message") {
		t.Fatal("trial outside the reminder window must not email")
	}
	acct := mustSingleAccount(t, db)
	endsAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	meta := mapFromAny(acct.Metadata)
	meta["trial_ends_at"] = endsAt
	if err := dbAccountSetMetadata(db, "proj-test", acct.ID, meta); err != nil {
		t.Fatal(err)
	}
	if err := app.runTrialReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != 1 {
		t.Fatalf("in-window trial should email once, got %d", got)
	}
	send := findCall(pf.calls, "messaging", "send_message")
	if send.Input["to"] != "mailto:trial@example.com" {
		t.Fatalf("reminder to=%v", send.Input["to"])
	}
	if subject := strFromAny(send.Input["subject"]); !strings.Contains(subject, "trial ends on") {
		t.Fatalf("reminder subject=%q", subject)
	}
	if key := strFromAny(send.Input["idempotency_key"]); key != "saas:"+acct.ID+":trial_reminder:"+endsAt {
		t.Fatalf("reminder idempotency_key=%q", key)
	}
	// Second sweep is a no-op: the sent marker persists.
	if err := app.runTrialReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != 1 {
		t.Fatalf("reminder must send once, got %d", got)
	}
	acct = mustSingleAccount(t, db)
	if strArg(mapFromAny(acct.Metadata), "trial_reminder_sent_at") == "" {
		t.Fatal("sent marker missing from account metadata")
	}
}

func TestTrialReminder_FailedSendRetriesAfterThrottle(t *testing.T) {
	pf := &platformStub{priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": false}}
	pf.failureMessages = map[string]string{"messaging:send_message": "app messaging is not installed"}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "trial@example.com", "slug": "trial", "plan_key": "crm-pro"}); err != nil {
		t.Fatal(err)
	}
	acct := mustSingleAccount(t, db)
	endsAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	meta := mapFromAny(acct.Metadata)
	meta["trial_ends_at"] = endsAt
	if err := dbAccountSetMetadata(db, "proj-test", acct.ID, meta); err != nil {
		t.Fatal(err)
	}
	if err := app.runTrialReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != 1 {
		t.Fatalf("first sweep should attempt once, got %d", got)
	}
	// Immediately after a failure the attempt is throttled.
	if err := app.runTrialReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != 1 {
		t.Fatalf("failed attempt must throttle retries, got %d", got)
	}
	// Once the throttle lapses and messaging works, the reminder sends.
	pf.failureMessages = nil
	acct = mustSingleAccount(t, db)
	meta = mapFromAny(acct.Metadata)
	meta["trial_reminder_attempted_at"] = time.Now().UTC().Add(-7 * time.Hour).Format(time.RFC3339)
	if err := dbAccountSetMetadata(db, "proj-test", acct.ID, meta); err != nil {
		t.Fatal(err)
	}
	if err := app.runTrialReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "messaging", "send_message"); got != 2 {
		t.Fatalf("post-throttle sweep should retry, got %d", got)
	}
	acct = mustSingleAccount(t, db)
	if strArg(mapFromAny(acct.Metadata), "trial_reminder_sent_at") == "" {
		t.Fatal("sent marker missing after successful retry")
	}
}
