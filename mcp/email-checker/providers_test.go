package main

import (
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type providerPlatform struct {
	sdk.PlatformClient
	bindings    map[string]any
	connections map[int64]string
	response    *sdk.ExecuteResult
	calledID    int64
	calledTool  string
	calledInput map[string]any
}

func providerTestContext(platform sdk.PlatformClient) *sdk.AppCtx {
	manifest := (&App{}).Manifest()
	return sdk.NewAppCtxForTest(&manifest, nil, nil, platform, nil)
}

func (p *providerPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: p.bindings}, nil
}

func (p *providerPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.connections[id], Status: "ready"}, nil
}

func (p *providerPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calledID = id
	p.calledTool = tool
	p.calledInput = input
	return p.response, nil
}

func TestManifestDeclaresProviderBindings(t *testing.T) {
	manifest := (&App{}).Manifest()
	hasExecute := false
	for _, permission := range manifest.Requires.Permissions {
		if permission == sdk.PermConnectionsExecute {
			hasExecute = true
		}
	}
	if !hasExecute {
		t.Fatal("manifest must declare platform.connections.execute")
	}
	if len(manifest.Requires.Integrations) != 1 {
		t.Fatalf("integration dependencies=%d, want 1", len(manifest.Requires.Integrations))
	}
	dep := manifest.Requires.Integrations[0]
	if dep.Role != verificationProviderRole || dep.Mode != "multiple" || dep.Required {
		t.Fatalf("unexpected provider dependency: %#v", dep)
	}
	want := map[string]bool{
		"zerobounce": true, "bouncer": true, "neverbounce": true,
		"kickbox": true, "millionverifier": true, "hunter": true,
	}
	for _, slug := range dep.CompatibleSlugs {
		delete(want, slug)
	}
	if len(want) != 0 {
		t.Fatalf("missing compatible providers: %#v", want)
	}
}

func TestSelectVerificationProviderUsesDefaultAndExplicitBinding(t *testing.T) {
	platform := &providerPlatform{
		bindings: map[string]any{
			verificationProviderRole: map[string]any{
				"ids":        []any{float64(7), float64(9)},
				"default_id": float64(9),
			},
		},
		connections: map[int64]string{7: "zerobounce", 9: "bouncer"},
	}
	ctx := providerTestContext(platform)

	bound, err := selectVerificationProvider(ctx, "auto", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ConnectionID != 9 || bound.AppSlug != "bouncer" {
		t.Fatalf("auto selected %#v, want default Bouncer connection 9", bound)
	}

	bound, err = selectVerificationProvider(ctx, "zerobounce", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ConnectionID != 7 {
		t.Fatalf("explicit provider selected connection %d, want 7", bound.ConnectionID)
	}
}

func TestExecuteZeroBounceNormalizesAndForwardsSafeOptions(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"status":          "do_not_mail",
		"sub_status":      "disposable",
		"free_email":      false,
		"catchall_domain": false,
		"did_you_mean":    "person@example.com",
		// Provider enrichment is intentionally omitted by the normalized result.
		"firstname": "Private",
		"city":      "Private City",
	})
	platform := &providerPlatform{
		response: &sdk.ExecuteResult{Success: true, Status: 200, Data: payload},
	}
	ctx := providerTestContext(platform)
	result := executeVerificationProvider(ctx, &sdk.BoundIntegration{
		ConnectionID: 42,
		AppSlug:      "zerobounce",
	}, "person@example.com", CheckOptions{
		Timeout:   8 * time.Second,
		IPAddress: "203.0.113.10",
	})

	if platform.calledID != 42 || platform.calledTool != "validate" {
		t.Fatalf("called connection/tool=%d/%q", platform.calledID, platform.calledTool)
	}
	if platform.calledInput["timeout"] != 8 || platform.calledInput["ip_address"] != "203.0.113.10" {
		t.Fatalf("unexpected provider input: %#v", platform.calledInput)
	}
	if result.Verdict != "risky" || result.Recommendation != "do_not_send" || result.Status != "do_not_mail" {
		t.Fatalf("unexpected normalized result: %#v", result)
	}
	if result.Disposable == nil || !*result.Disposable || result.SuggestedEmail != "person@example.com" {
		t.Fatalf("missing normalized signals: %#v", result)
	}
}

func TestNormalizeProviderResponses(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		payload   map[string]any
		verdict   string
		recommend string
		status    string
	}{
		{
			name: "Bouncer deliverable", provider: "bouncer",
			payload: map[string]any{
				"status": "deliverable", "reason": "accepted_email", "score": float64(99),
				"domain":  map[string]any{"acceptAll": "no", "disposable": "no", "free": "no"},
				"account": map[string]any{"role": "no"},
			},
			verdict: "deliverable", recommend: "send", status: "deliverable",
		},
		{
			name: "NeverBounce catchall", provider: "neverbounce",
			payload: map[string]any{"result": "catchall", "flags": []any{"has_dns_mx", "accepts_all"}},
			verdict: "risky", recommend: "review", status: "catchall",
		},
		{
			name: "Kickbox rejected", provider: "kickbox",
			payload: map[string]any{"result": "undeliverable", "reason": "rejected_email", "sendex": 0.05},
			verdict: "undeliverable", recommend: "do_not_send", status: "undeliverable",
		},
		{
			name: "MillionVerifier disposable", provider: "millionverifier",
			payload: map[string]any{"result": "disposable", "subresult": "disposable", "role": false},
			verdict: "risky", recommend: "do_not_send", status: "disposable",
		},
		{
			name: "Hunter envelope", provider: "hunter",
			payload: map[string]any{"data": map[string]any{"status": "accept_all", "score": float64(82), "accept_all": true}},
			verdict: "risky", recommend: "review", status: "accept_all",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeProviderResponse(tc.provider, tc.payload, 17)
			if got.Verdict != tc.verdict || got.Recommendation != tc.recommend || got.Status != tc.status {
				t.Fatalf("normalize=%#v", got)
			}
		})
	}
}

func TestProviderErrorInsideSuccessfulHTTPResponseIsNotTreatedAsAVerdict(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"error": "Invalid API key or no credits"})
	platform := &providerPlatform{
		response: &sdk.ExecuteResult{Success: true, Status: 200, Data: payload},
	}
	ctx := providerTestContext(platform)
	result := executeVerificationProvider(ctx, &sdk.BoundIntegration{
		ConnectionID: 42,
		AppSlug:      "zerobounce",
	}, "person@example.com", CheckOptions{Timeout: 5 * time.Second})
	if result.Error == "" || result.Verdict != "unknown" || result.Recommendation != "review" {
		t.Fatalf("provider error was not preserved safely: %#v", result)
	}
}

func TestLocalInvalidAddressDoesNotNeedAProvider(t *testing.T) {
	result, err := runCheck(nil, "not-an-email", CheckOptions{Provider: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "undeliverable" || result.Recommendation != "do_not_send" {
		t.Fatalf("unexpected local decision: %#v", result)
	}
	if result.Provider == nil || result.Provider.Checked || result.Provider.Reason != "local_checks_failed" {
		t.Fatalf("expected provider credit-saving skip, got %#v", result.Provider)
	}
}
