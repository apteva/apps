package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type emailVerificationCall struct {
	AppName string
	Tool    string
	Input   map[string]any
}

type emailVerificationPlatform struct {
	tk.BasePlatformClient
	mu       sync.Mutex
	calls    []emailVerificationCall
	result   emailCheckerResult
	callErr  error
	response func(string) emailCheckerResult
}

func (p *emailVerificationPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName:   "crm",
		InstallID: 1,
		ProjectID: "test-proj",
		Bindings:  map[string]any{"email_verification": float64(77)},
	}, nil
}

func (p *emailVerificationPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "email-checker", Status: "running", ProjectID: "test-proj"}, nil
}

func (p *emailVerificationPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, emailVerificationCall{AppName: appName, Tool: tool, Input: cloneAnyMap(input)})
	callErr := p.callErr
	result := p.result
	if p.response != nil {
		email, _ := input["email"].(string)
		result = p.response(email)
	}
	p.mu.Unlock()
	if callErr != nil {
		return callErr
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (p *emailVerificationPlatform) snapshot() []emailVerificationCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]emailVerificationCall(nil), p.calls...)
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func deliverableEmailResult() emailCheckerResult {
	return emailCheckerResult{
		Valid:          true,
		SyntaxOK:       true,
		DomainStatus:   "mx",
		Verdict:        "deliverable",
		Confidence:     "high",
		Recommendation: "accept",
	}
}

func TestEmailVerificationOptionalAndAutomaticAnnotation(t *testing.T) {
	withoutBinding := newTestCtx(t)
	contact, results, err := createContactWithEmailVerification(withoutBinding, "test-proj", map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "optional@example.test", "is_primary": true}},
	}, true)
	if err != nil || contact == nil || len(results) != 0 {
		t.Fatalf("optional create contact=%v results=%v err=%v", contact, results, err)
	}

	platform := &emailVerificationPlatform{result: deliverableEmailResult()}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	created, checks, err := createContactWithEmailVerification(ctx, "test-proj", map[string]any{
		"display_name": "Alice",
		"channels":     []any{map[string]any{"kind": "email", "value": " Alice@Example.Test ", "is_primary": true}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].ChannelID == 0 {
		t.Fatalf("checks=%+v", checks)
	}
	channel := created.Channels[0]
	if channel.VerificationVerdict != "deliverable" || channel.VerificationConfidence != "high" || channel.VerifiedAt != "" {
		t.Fatalf("channel=%+v", channel)
	}
	calls := platform.snapshot()
	if len(calls) != 1 || calls[0].AppName != "email-checker" || calls[0].Tool != "email_check" {
		t.Fatalf("calls=%+v", calls)
	}
	if calls[0].Input["provider"] != "local" || calls[0].Input["smtp"] != false || calls[0].Input["_project_id"] != "test-proj" {
		t.Fatalf("input=%v", calls[0].Input)
	}

	oldID := channel.ID
	updated, updateChecks, err := updateContactWithEmailVerification(ctx, "test-proj", created.ID, map[string]any{
		"display_name": "Alice Updated",
		"channels":     []any{map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true}},
	}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if len(updateChecks) != 0 || len(platform.snapshot()) != 1 {
		t.Fatalf("unchanged email was rechecked: results=%v calls=%v", updateChecks, platform.snapshot())
	}
	if updated.Channels[0].ID != oldID || updated.Channels[0].VerificationVerdict != "deliverable" {
		t.Fatalf("unchanged channel metadata was not preserved: %+v", updated.Channels[0])
	}
}

func TestEmailVerificationChangedChannelsAndFailOpen(t *testing.T) {
	platform := &emailVerificationPlatform{response: func(email string) emailCheckerResult {
		result := deliverableEmailResult()
		if email == "second@example.test" {
			result.Verdict = "risky"
			result.Confidence = "medium"
			result.Reasons = []string{"role_account"}
		}
		return result
	}}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	created, _, err := createContactWithEmailVerification(ctx, "test-proj", map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "first@example.test", "is_primary": true}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	updated, results, err := updateContactWithEmailVerification(ctx, "test-proj", created.ID, map[string]any{
		"channels": []any{
			map[string]any{"kind": "email", "value": "first@example.test", "is_primary": true},
			map[string]any{"kind": "email", "value": "second@example.test"},
		},
	}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Email != "second@example.test" || len(platform.snapshot()) != 2 {
		t.Fatalf("results=%+v calls=%+v", results, platform.snapshot())
	}
	if len(updated.Channels) != 2 || updated.Channels[1].VerificationVerdict != "risky" {
		t.Fatalf("channels=%+v", updated.Channels)
	}

	failing := &emailVerificationPlatform{callErr: errors.New("temporary outage")}
	failOpenCtx := newTestCtx(t, tk.WithPlatform(failing))
	failOpen, unavailable, err := createContactWithEmailVerification(failOpenCtx, "test-proj", map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "saved@example.test", "is_primary": true}},
	}, true)
	if err != nil || failOpen == nil || len(unavailable) != 1 || unavailable[0].Reason != "verifier_unavailable" {
		t.Fatalf("contact=%v results=%+v err=%v", failOpen, unavailable, err)
	}
	if failOpen.Channels[0].VerificationVerdict != "unknown" {
		t.Fatalf("channel=%+v", failOpen.Channels[0])
	}
}

func TestEmailVerificationMultipleEmailsAndExistingUpsert(t *testing.T) {
	platform := &emailVerificationPlatform{result: deliverableEmailResult()}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	contact, results, err := createContactWithEmailVerification(ctx, "test-proj", map[string]any{
		"channels": []any{
			map[string]any{"kind": "email", "value": "one@example.test", "is_primary": true},
			map[string]any{"kind": "email", "value": "two@example.test"},
		},
	}, true)
	if err != nil || len(results) != 2 || len(platform.snapshot()) != 2 {
		t.Fatalf("contact=%v results=%+v calls=%+v err=%v", contact, results, platform.snapshot(), err)
	}
	for _, channel := range contact.Channels {
		if channel.VerificationVerdict != "deliverable" {
			t.Fatalf("channel=%+v", channel)
		}
	}

	upsertPlatform := &emailVerificationPlatform{result: deliverableEmailResult()}
	upsertCtx := newTestCtx(t, tk.WithPlatform(upsertPlatform))
	first, created, firstResults, err := upsertContactWithEmailVerification(
		upsertCtx, "test-proj", "email", "upsert@example.test", nil, "agent", true,
	)
	if err != nil || !created || first == nil || len(firstResults) != 1 {
		t.Fatalf("first=%v created=%v results=%+v err=%v", first, created, firstResults, err)
	}
	second, createdAgain, secondResults, err := upsertContactWithEmailVerification(
		upsertCtx, "test-proj", "email", "upsert@example.test", nil, "agent", true,
	)
	if err != nil || createdAgain || second.ID != first.ID || len(secondResults) != 0 || len(upsertPlatform.snapshot()) != 1 {
		t.Fatalf("second=%v created=%v results=%+v calls=%+v err=%v", second, createdAgain, secondResults, upsertPlatform.snapshot(), err)
	}
}

func TestEmailVerificationStrictPolicyIsAtomicAndInboundRemainsFailOpen(t *testing.T) {
	invalid := deliverableEmailResult()
	invalid.Valid = false
	invalid.SyntaxOK = false
	invalid.Verdict = "undeliverable"
	invalid.Reasons = []string{"bad_syntax"}
	platform := &emailVerificationPlatform{result: invalid}
	ctx := newTestCtx(t,
		tk.WithPlatform(platform),
		tk.WithConfig(map[string]string{"email_verification_policy": "reject_definitive"}),
	)
	contact, results, err := createContactWithEmailVerification(ctx, "test-proj", map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "invalid@example.test", "is_primary": true}},
	}, true)
	var policyErr *EmailVerificationPolicyError
	if contact != nil || !errors.As(err, &policyErr) || len(results) != 1 {
		t.Fatalf("contact=%v results=%+v err=%v", contact, results, err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE project_id = ?`, "test-proj").Scan(&count); err != nil || count != 0 {
		t.Fatalf("strict write was not atomic: count=%d err=%v", count, err)
	}

	disposable := deliverableEmailResult()
	disposable.Valid = false
	disposable.Disposable = true
	disposable.Verdict = "undeliverable"
	disposable.Reasons = []string{"disposable_domain"}
	disposableCtx := newTestCtx(t,
		tk.WithPlatform(&emailVerificationPlatform{result: disposable}),
		tk.WithConfig(map[string]string{"email_verification_policy": "reject_definitive"}),
	)
	allowedDisposable, _, err := createContactWithEmailVerification(disposableCtx, "test-proj", map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "temporary@example.test", "is_primary": true}},
	}, true)
	if err != nil || allowedDisposable == nil {
		t.Fatalf("disposable should be annotated by default: contact=%v err=%v", allowedDisposable, err)
	}
	blockingDisposableCtx := newTestCtx(t,
		tk.WithPlatform(&emailVerificationPlatform{result: disposable}),
		tk.WithConfig(map[string]string{
			"email_verification_policy":           "reject_definitive",
			"email_verification_block_disposable": "true",
		}),
	)
	if blocked, _, err := createContactWithEmailVerification(blockingDisposableCtx, "test-proj", map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "temporary@example.test", "is_primary": true}},
	}, true); blocked != nil || !errors.As(err, &policyErr) {
		t.Fatalf("disposable should be blocked only when configured: contact=%v err=%v", blocked, err)
	}

	inbound, created, inboundResults, err := upsertContactWithEmailVerification(
		ctx, "test-proj", "email", "invalid@example.test", nil, "messaging_inbound", false,
	)
	if err != nil || !created || inbound == nil || len(inboundResults) != 1 {
		t.Fatalf("inbound contact=%v created=%v results=%+v err=%v", inbound, created, inboundResults, err)
	}

	httpCtx := newTestCtx(t,
		tk.WithPlatform(&emailVerificationPlatform{result: invalid}),
		tk.WithConfig(map[string]string{"email_verification_policy": "reject_definitive"}),
	)
	previousGlobalCtx := globalCtx
	globalCtx = httpCtx
	defer func() { globalCtx = previousGlobalCtx }()
	req := httptest.NewRequest(http.MethodPost, "/contacts?project_id=test-proj", bytes.NewBufferString(
		`{"channels":[{"kind":"email","value":"invalid-http@example.test","is_primary":true}]}`,
	))
	recorder := httptest.NewRecorder()
	(&App{}).handleHTTPCreate(recorder, req)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("HTTP strict status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestManualEmailVerificationCanRequestSMTPAndEmitsUpdate(t *testing.T) {
	informative := true
	catchAll := true
	result := deliverableEmailResult()
	result.Verdict = "risky"
	result.Confidence = "medium"
	result.SMTP = emailCheckerSMTPResult{Checked: true, RcptStatus: "catch_all", Informative: &informative, CatchAll: &catchAll}
	platform := &emailVerificationPlatform{result: result}
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t,
		tk.WithPlatform(platform),
		tk.WithEmitter(recorder),
		tk.WithConfig(map[string]string{"email_verification_mode": "off"}),
	)
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "smtp@example.test", "is_primary": true}},
	})
	if contact.Channels == nil {
		_ = loadChannels(ctx.AppDB(), contact)
	}
	out, err := (&App{}).toolVerifyEmail(ctx, map[string]any{
		"contact_id": contact.ID,
		"channel_id": contact.Channels[0].ID,
		"smtp":       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["contact"].(*Contact)
	if got.Channels[0].VerificationVerdict != "risky" || got.Channels[0].VerificationReason != "catch_all" || got.Channels[0].VerifiedAt != "" {
		t.Fatalf("channel=%+v", got.Channels[0])
	}
	calls := platform.snapshot()
	if len(calls) != 1 || calls[0].Input["smtp"] != true || calls[0].Input["provider"] != "local" {
		t.Fatalf("calls=%+v", calls)
	}
	if len(recorder.EventsByTopic("contact.updated")) != 1 {
		t.Fatalf("events=%+v", recorder.Events())
	}

	previousGlobalCtx := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = previousGlobalCtx }()
	req := httptest.NewRequest(
		http.MethodPost,
		"/contacts/"+strconv.FormatInt(contact.ID, 10)+"/channels/"+strconv.FormatInt(contact.Channels[0].ID, 10)+"/verify?project_id=test-proj",
		bytes.NewBufferString(`{"smtp":false}`),
	)
	httpRecorder := httptest.NewRecorder()
	(&App{}).handleHTTPContactItem(httpRecorder, req)
	if httpRecorder.Code != http.StatusOK {
		t.Fatalf("HTTP verify status=%d body=%s", httpRecorder.Code, httpRecorder.Body.String())
	}
	calls = platform.snapshot()
	if len(calls) != 2 || calls[1].Input["smtp"] != false {
		t.Fatalf("HTTP calls=%+v", calls)
	}
}
