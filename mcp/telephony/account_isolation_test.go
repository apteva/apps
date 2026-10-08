package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type accountFaultPlatform struct {
	*answerPlatform
	mu                 sync.Mutex
	faults             map[int64]map[string]string
	credentialFailures map[int64]bool
	credentialGates    map[int64]chan struct{}
	credentialDone     chan struct{}
	requests           map[int64]int
}

func (p *accountFaultPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	p.mu.Lock()
	gate := p.credentialGates[id]
	p.mu.Unlock()
	if gate != nil {
		<-gate
		defer close(p.credentialDone)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.credentialFailures[id] {
		return nil, errors.New("account credentials unavailable")
	}
	return p.answerPlatform.GetConnectionCredentials(id)
}
func (p *accountFaultPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	return p.ExecuteIntegrationToolContext(context.Background(), id, tool, args)
}
func (p *accountFaultPlatform) ExecuteIntegrationToolContext(request context.Context, id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	fault := p.faults[id][tool]
	p.requests[id]++
	p.mu.Unlock()
	switch fault {
	case "stall":
		<-request.Done()
		return nil, request.Err()
	case "error":
		return nil, errors.New("carrier account unavailable")
	case "reject":
		return &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"message":"carrier account suspended"}`)}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answerPlatform.ExecuteIntegrationTool(id, tool, args)
}
func accountIsolationFixture(t *testing.T, sameProvider bool) (*App, *sdk.AppCtx, *accountFaultPlatform) {
	t.Helper()
	t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
	base := multiCarrierPlatform()
	base.bindings["carrier"] = map[string]any{"ids": []any{11, 10}, "default_id": 11}
	base.integrationResponse["dial_call"] = json.RawMessage(`{"data":{"call_control_id":"healthy-account-call"}}`)
	if sameProvider {
		base.connectionsByID[10].AppSlug = "twilio"
		base.credentialsByID[10].Slug = "twilio"
		base.credentialsByID[10].Fields["auth_token"] = "test-auth-token"
		base.integrationResponsesByConn[10]["list_phone_numbers"] = json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN-healthy-account","phone_number":"+33189313431","capabilities":{"voice":true},"status":"active"}]}`)
	}
	p := &accountFaultPlatform{answerPlatform: base, faults: map[int64]map[string]string{}, credentialFailures: map[int64]bool{}, requests: map[int64]int{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	return &App{installID: 42}, ctx, p
}
func assertHealthyAccountDial(t *testing.T, a *App, ctx *sdk.AppCtx, from string, conn int64) {
	t.Helper()
	session, err := a.placeHumanCall(ctx, "project-a", "+33189999901", from, 30, nil)
	if err != nil {
		t.Fatal("healthy account could not dial", err)
	}
	row, err := a.db().findCall(session.CallID)
	if err != nil || row == nil || row.CarrierConnectionID != conn || row.FromNumber != from || row.Status != "initiated" {
		t.Fatalf("wrong outbound account: %+v %v", row, err)
	}
}
func TestAccountFailureDoesNotBlockHealthyOutboundPlacement(t *testing.T) {
	for _, sameProvider := range []bool{false, true} {
		name := "different_providers"
		if sameProvider {
			name = "same_provider"
		}
		t.Run(name, func(t *testing.T) {
			for _, failure := range []string{"inventory", "credentials", "dial_rejection"} {
				t.Run(failure, func(t *testing.T) {
					a, ctx, p := accountIsolationFixture(t, sameProvider)
					switch failure {
					case "inventory":
						p.faults[11] = map[string]string{"list_phone_numbers": "error"}
					case "credentials":
						p.credentialFailures[11] = true
					case "dial_rejection":
						p.faults[11] = map[string]string{"make_call": "reject"}
					}
					result, err := a.connectedNumbers(ctx)
					if err != nil {
						t.Fatal(err)
					}
					healthyVisible := false
					for _, n := range result["numbers"].([]connectedNumberView) {
						if n.CarrierConnectionID == 10 {
							healthyVisible = n.OutboundEnabled && n.Outbound.Status == outboundReady
						}
					}
					if !healthyVisible {
						t.Fatal("healthy account is not selectable and outbound ready")
					}
					if failure != "dial_rejection" {
						if result["inventory_status"] != "partial" || len(result["numbers"].([]connectedNumberView)) != 1 {
							t.Fatalf("healthy inventory lost: %+v", result)
						}
						warnings := result["warnings"].([]numberInventoryWarning)
						if len(warnings) != 1 || warnings[0].ConnectionID != 11 {
							t.Fatalf("warning not account scoped: %+v", warnings)
						}
					} else {
						if _, err = a.placeHumanCall(ctx, "project-a", "+33189999902", "+33189313432", 30, nil); err == nil {
							t.Fatal("unhealthy account call reported success")
						}
					}
					assertHealthyAccountDial(t, a, ctx, "+33189313431", 10)
					// Calling again after a failure must not trip an installation-wide health flag.
					assertHealthyAccountDial(t, a, ctx, "+33189313431", 10)
				})
			}
		})
	}
}

func TestStalledAccountsCannotStarveHealthyInventory(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, true)
	ids := []any{11, 12, 13, 14, 10}
	p.bindings["carrier"] = map[string]any{"ids": ids, "default_id": 11}
	for _, id := range []int64{11, 12, 13, 14} {
		connection := *p.connectionsByID[11]
		credentials := *p.credentialsByID[11]
		p.connectionsByID[id] = &connection
		p.credentialsByID[id] = &credentials
		p.faults[id] = map[string]string{"list_phone_numbers": "stall"}
	}
	request, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result, err := a.connectedNumbers(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	numbers := result["numbers"].([]connectedNumberView)
	warnings := result["warnings"].([]numberInventoryWarning)
	if result["inventory_status"] != "partial" || len(numbers) != 1 || numbers[0].CarrierConnectionID != 10 || len(warnings) != 4 {
		t.Fatalf("stalled accounts starved healthy inventory: %+v", result)
	}
	assertHealthyAccountDial(t, a, ctx, numbers[0].PhoneNumber, 10)
}

func TestStalledCredentialReadCannotDelayHealthyInventory(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, false)
	gate := make(chan struct{})
	p.credentialGates = map[int64]chan struct{}{11: gate}
	p.credentialDone = make(chan struct{})
	t.Cleanup(func() {
		close(gate)
		select {
		case <-p.credentialDone:
		case <-time.After(time.Second):
			t.Error("stalled credential request did not exit")
		}
	})
	request, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := a.connectedNumbers(ctx, request)
	if err != nil || time.Since(start) > time.Second {
		t.Fatalf("stalled credential request blocked inventory: %+v %v", result, err)
	}
	numbers := result["numbers"].([]connectedNumberView)
	warnings := result["warnings"].([]numberInventoryWarning)
	if result["inventory_status"] != "partial" || len(numbers) != 1 || numbers[0].CarrierConnectionID != 10 || len(warnings) != 1 || warnings[0].Code != "inventory_timeout" {
		t.Fatalf("stalled credentials hid healthy inventory: %+v", result)
	}
	assertHealthyAccountDial(t, a, ctx, numbers[0].PhoneNumber, 10)
}
func TestStalledAccountInventoryReturnsHealthyChoicesAndDoesNotDelaySelectedDial(t *testing.T) {
	for _, sameProvider := range []bool{false, true} {
		t.Run(map[bool]string{false: "different_providers", true: "same_provider"}[sameProvider], func(t *testing.T) {
			a, ctx, p := accountIsolationFixture(t, sameProvider)
			p.faults[11] = map[string]string{"list_phone_numbers": "stall"}
			request, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			result, err := a.connectedNumbers(ctx, request)
			if err != nil || time.Since(start) > time.Second || result["inventory_status"] != "partial" {
				t.Fatalf("stalled inventory blocked request: %+v %v", result, err)
			}
			numbers := result["numbers"].([]connectedNumberView)
			warnings := result["warnings"].([]numberInventoryWarning)
			if len(numbers) != 1 || numbers[0].CarrierConnectionID != 10 || len(warnings) != 1 || warnings[0].Code != "inventory_timeout" || warnings[0].ConnectionID != 11 {
				t.Fatalf("healthy choices lost: %+v", result)
			}
			p.mu.Lock()
			badReads := p.requests[11]
			p.mu.Unlock()
			start = time.Now()
			assertHealthyAccountDial(t, a, ctx, numbers[0].PhoneNumber, 10)
			p.mu.Lock()
			after := p.requests[11]
			p.mu.Unlock()
			if after != badReads || time.Since(start) > time.Second {
				t.Fatal("selected healthy number queried unrelated stalled account")
			}
		})
	}
}
func TestAccountReadinessErrorDoesNotPoisonOtherCarrier(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, false)
	p.faults[10] = map[string]string{"list_outbound_voice_profiles": "error"}
	result, err := a.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range result["numbers"].([]connectedNumberView) {
		if n.CarrierConnectionID == 10 {
			found = true
			if n.Outbound.Status != outboundConfigError {
				t.Fatal("unhealthy readiness not surfaced")
			}
		}
	}
	if !found {
		t.Fatal("configuration failure hid owned number")
	}
	assertHealthyAccountDial(t, a, ctx, "+33189313432", 11)
}
func TestInventoryHintsNeverAuthorizeStaleOwnershipOrAnUnboundAccount(t *testing.T) {
	a, ctx, p := accountIsolationFixture(t, true)
	if _, err := a.connectedNumbers(ctx); err != nil {
		t.Fatal(err)
	}
	p.credentialsByID[10].Fields["phone_number"] = ""
	p.integrationResponsesByConn[10]["list_phone_numbers"] = json.RawMessage(`{"incoming_phone_numbers":[]}`)
	if _, _, _, err := a.resolveCarrierBinding(ctx, "project-a", "+33189313431"); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatal("stale inventory hint granted ownership", err)
	}
	p.bindings["carrier"] = map[string]any{"ids": []any{11}, "default_id": 11}
	if _, _, _, err := a.resolveCarrierBinding(ctx, "project-a", "+33189313431"); err == nil {
		t.Fatal("hint retained unbound account authorization")
	}
}
func TestInventoryHintsAreScopedAndDisabledReasonRemainsVisible(t *testing.T) {
	a, ctx, _ := accountIsolationFixture(t, false)
	setAdmissionRule(t, a, ctx, "number", "+33189313431", false)
	result, err := a.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range result["numbers"].([]connectedNumberView) {
		if n.PhoneNumber == "+33189313431" && (n.OutboundEnabled || n.OutboundDisabledReason != "outbound_disabled") {
			t.Fatal("disabled reason lost")
		}
	}
	bindings := ctx.IntegrationsFor("carrier")
	if got := a.outboundHints.candidates("another-project", "+33189313431", bindings); got[0].ConnectionID != 11 {
		t.Fatal("inventory hint leaked projects")
	}
	if _, _, _, err := a.resolveCarrierBinding(ctx, "project-a", "+33189313431"); !errors.Is(err, errOutboundDisabled) {
		t.Fatal("hint bypassed admission rule", err)
	}
}
