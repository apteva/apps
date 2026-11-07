package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

const contactIDFixture = "1ZdMXpapqp9sle5dl8BlppTJXAzf5"

func newSpaceshipPurchaseStub() *stubPlatform {
	return &stubPlatform{connSlug: "spaceship", replyByTool: map[string]*sdk.ExecuteResult{
		"check_single_domain_availability": {Success: true, Status: 200, Data: json.RawMessage(`{"domain":"example.com","result":"available","premiumPricing":[]}`)},
		"get_contact":                      {Success: true, Status: 200, Data: json.RawMessage(`{"id":"` + contactIDFixture + `"}`)},
		"register_domain":                  {Success: true, Status: 202, Data: json.RawMessage(`{"operation_id":"operation123"}`)},
		"get_async_operation":              {Success: true, Status: 200, Data: json.RawMessage(`{"status":"pending","type":"domains_Register"}`)},
		"get_domain_info":                  {Success: true, Status: 200, Data: json.RawMessage(`{"name":"example.com","lifecycleStatus":"registered"}`)},
	}}
}
func spaceshipPrepareArgs() map[string]any {
	return map[string]any{"domain": "example.com", "connection_id": 1, "accept_account_rate": true, "years": 2, "auto_renew": false, "contacts": map[string]any{"registrant": contactIDFixture}}
}
func TestSpaceshipPurchasePersistsOptionsAndPollsWithoutResubmission(t *testing.T) {
	plat := newSpaceshipPurchaseStub()
	ctx := newTestCtx(t, plat)
	app := &App{}
	prepared, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
	if err != nil {
		t.Fatal(err)
	}
	token := prepared.(map[string]any)["confirmation_token"].(string)
	in, err := dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
	if err != nil {
		t.Fatal(err)
	}
	if in.BillingMode != "account_rate" || in.CostCents != 0 || in.Years != 2 || in.AutoRenew {
		t.Fatalf("wrong persisted consent: %+v", in)
	}
	var payload map[string]any
	if err = json.Unmarshal(in.RequestJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["years"] != float64(2) || payload["autoRenew"] != false {
		t.Fatalf("payload %v", payload)
	}
	result, err := app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["registered"] != false {
		t.Fatal("202 must not mean registered")
	}
	saved, _ := dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
	if saved.OperationID != "operation123" || saved.Status != "processing" {
		t.Fatalf("handle not saved: %+v", saved)
	}
	if d, _ := dbDomainGetByName(ctx.AppDB(), "test-proj", "example.com"); d != nil {
		t.Fatal("inventory added before async success")
	}
	for range 2 {
		if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token, "resume": true}); err != nil {
			t.Fatal(err)
		}
	}
	plat.replyByTool["get_async_operation"].Data = json.RawMessage(`{"status":"success","type":"domains_Register","details":{"name":"example.com","message":"Registered","orderId":"fixture-order"}}`)
	if _, err = app.toolRegistrationStatus(ctx, map[string]any{"confirmation_token": token}); err != nil {
		t.Fatal(err)
	}
	saved, _ = dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
	if saved.Status != "succeeded" {
		t.Fatal(saved.Status)
	}
	if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err != nil {
		t.Fatal(err)
	}
	purchases := 0
	for _, call := range plat.calls {
		if call.Tool == "register_domain" {
			purchases++
			if intArg(call.Input, "years", 0) != 2 {
				t.Fatal("changed purchase options")
			}
		}
	}
	if purchases != 1 {
		t.Fatalf("submitted %d times", purchases)
	}
	d, _ := dbDomainGetByName(ctx.AppDB(), "test-proj", "example.com")
	if d == nil || d.RegistrarSlug != "spaceship" {
		t.Fatal("completed domain not recorded")
	}
}
func TestSpaceshipUnknownSubmissionNeverRepeats(t *testing.T) {
	for _, mode := range []string{"timeout", "missing-header"} {
		t.Run(mode, func(t *testing.T) {
			plat := newSpaceshipPurchaseStub()
			ctx := newTestCtx(t, plat)
			app := &App{}
			prepared, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
			if err != nil {
				t.Fatal(err)
			}
			token := prepared.(map[string]any)["confirmation_token"].(string)
			if mode == "timeout" {
				plat.executeFn = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
					return nil, errors.New("timeout")
				}
			} else {
				plat.replyByTool["register_domain"].Data = json.RawMessage(`{}`)
			}
			if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err == nil {
				t.Fatal("expected unknown")
			}
			in, _ := dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
			if in.Status != "unknown" {
				t.Fatal(in.Status)
			}
			if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token, "resume": true}); err == nil || !strings.Contains(err.Error(), "must not be repeated") {
				t.Fatal(err)
			}
			if _, err = app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs()); err == nil {
				t.Fatal("conflicting purchase allowed")
			}
		})
	}
}
func TestSpaceshipPrepareRequiresConsentContactsAndRejectsPremium(t *testing.T) {
	for _, mode := range []string{"consent", "contact", "premium", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			plat := newSpaceshipPurchaseStub()
			ctx := newTestCtx(t, plat)
			app := &App{}
			args := spaceshipPrepareArgs()
			switch mode {
			case "consent":
				delete(args, "accept_account_rate")
			case "contact":
				args["contacts"] = map[string]any{"registrant": "invalid"}
			case "premium":
				plat.replyByTool["check_single_domain_availability"].Data = json.RawMessage(`{"domain":"example.com","result":"available","premiumPricing":[{"operation":"register","price":999,"currency":"USD"}]}`)
			case "unavailable":
				plat.replyByTool["check_single_domain_availability"].Data = json.RawMessage(`{"domain":"example.com","result":"taken","premiumPricing":[]}`)
			}
			if _, err := app.toolDomainRegistrationPrepare(ctx, args); err == nil {
				t.Fatal("invalid preparation accepted")
			}
			for _, call := range plat.calls {
				if call.Tool == "register_domain" {
					t.Fatal("prepare spent money")
				}
			}
		})
	}
}
func TestSpaceshipAsyncFailureAndScope(t *testing.T) {
	plat := newSpaceshipPurchaseStub()
	ctx := newTestCtx(t, plat)
	app := &App{}
	p, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
	if err != nil {
		t.Fatal(err)
	}
	token := p.(map[string]any)["confirmation_token"].(string)
	t.Setenv("APTEVA_PROJECT_ID", "")
	if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token, "_project_id": "other"}); err == nil {
		t.Fatal("cross-project purchase allowed")
	}
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err != nil {
		t.Fatal(err)
	}
	plat.replyByTool["get_async_operation"].Data = json.RawMessage(`{"status":"failed","type":"domains_Register"}`)
	if _, err = app.toolRegistrationStatus(ctx, map[string]any{"confirmation_token": token}); err != nil {
		t.Fatal(err)
	}
	in, _ := dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
	if in.Status != "failed" {
		t.Fatal(in.Status)
	}
	if d, _ := dbDomainGetByName(ctx.AppDB(), "test-proj", "example.com"); d != nil {
		t.Fatal("failed registration added inventory")
	}
}

func TestSpaceshipConcurrentSubmissionIsClaimedOnce(t *testing.T) {
	plat := newSpaceshipPurchaseStub()
	ctx := newTestCtx(t, plat)
	app := &App{}
	prepared, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
	if err != nil {
		t.Fatal(err)
	}
	token := prepared.(map[string]any)["confirmation_token"].(string)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	plat.executeFn = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "register_domain" {
			close(started)
			<-release
		}
		return plat.replyByTool[tool], nil
	}
	go func() {
		_, err := app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token})
		finished <- err
	}()
	<-started
	_, secondErr := app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token})
	close(release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if secondErr == nil {
		t.Fatal("concurrent submit should not proceed")
	}
	count := 0
	for _, call := range plat.calls {
		if call.Tool == "register_domain" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d submissions", count)
	}
}
func TestSpaceshipExpiryCancellationAndOwnershipMismatch(t *testing.T) {
	for _, mode := range []string{"expiry", "cancel", "ownership"} {
		t.Run(mode, func(t *testing.T) {
			plat := newSpaceshipPurchaseStub()
			ctx := newTestCtx(t, plat)
			app := &App{}
			p, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
			if err != nil {
				t.Fatal(err)
			}
			token := p.(map[string]any)["confirmation_token"].(string)
			switch mode {
			case "expiry":
				_, err = ctx.AppDB().Exec(`UPDATE registration_intents SET expires_at='2000-01-01T00:00:00Z' WHERE token=?`, token)
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err = app.toolRegistrationStatus(ctx, map[string]any{"confirmation_token": token, "cancel": true}); err != nil {
					t.Fatal(err)
				}
			case "ownership":
				if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err != nil {
					t.Fatal(err)
				}
				plat.replyByTool["get_async_operation"].Data = json.RawMessage(`{"status":"success","type":"domains_Register","details":{"name":"example.com","message":"Registered","orderId":"fixture-order"}}`)
				plat.replyByTool["get_domain_info"].Data = json.RawMessage(`{"name":"another.com","lifecycleStatus":"registered"}`)
			}
			if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err == nil {
				t.Fatal("unsafe completion permitted")
			}
			if d, _ := dbDomainGetByName(ctx.AppDB(), "test-proj", "example.com"); d != nil {
				t.Fatal("inventory updated despite failed validation")
			}
		})
	}
}

func TestSpaceshipRegistrationRejectsUnrelatedOperation(t *testing.T) {
	for _, response := range []string{
		`{"status":"success","type":"domains_Transfer","details":{"name":"example.com"}}`,
		`{"status":"success","type":"domains_Register","details":{"name":"other.com"}}`,
		`{"status":"success","type":"domains_Register"}`,
	} {
		t.Run(response, func(t *testing.T) {
			plat := newSpaceshipPurchaseStub()
			ctx := newTestCtx(t, plat)
			app := &App{}
			p, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
			if err != nil {
				t.Fatal(err)
			}
			token := p.(map[string]any)["confirmation_token"].(string)
			if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err != nil {
				t.Fatal(err)
			}
			plat.replyByTool["get_async_operation"].Data = json.RawMessage(response)
			if _, err = app.toolRegistrationStatus(ctx, map[string]any{"confirmation_token": token}); err == nil {
				t.Fatal("unrelated operation accepted")
			}
			in, _ := dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
			if in.Status != "processing" {
				t.Fatal(in.Status)
			}
			if len(plat.callsFor("register_domain")) != 1 {
				t.Fatal("purchase repeated")
			}
			if d, _ := dbDomainGetByName(ctx.AppDB(), "test-proj", "example.com"); d != nil {
				t.Fatal("unconfirmed domain added")
			}
		})
	}
}
