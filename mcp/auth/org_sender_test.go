package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newSenderCtx builds an Auth install with Messaging stubbed, a public URL,
// and two organizations that each have a public client and one user.
func newSenderCtx(t *testing.T, installFrom string) (*sdk.AppCtx, *recoveryMessagingStub) {
	t.Helper()
	platform := &recoveryMessagingStub{}
	cfg := map[string]string{"email_verification_required": "false", "app_url": "http://localhost:8080"}
	if installFrom != "" {
		cfg["from_email"] = installFrom
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform), tk.WithConfig(cfg))
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })
	app := &App{}
	for _, slug := range []string{"default", "runlane"} {
		oid, err := dbCreateOrg(ctx.AppDB(), "test-proj", slug, strings.ToUpper(slug[:1])+slug[1:], "#94a3b8")
		if err != nil {
			t.Fatal(err)
		}
		if err := ensureSigningKey(ctx.AppDB(), "test-proj", oid); err != nil {
			t.Fatal(err)
		}
		if _, err := app.toolClientsCreate(ctx, map[string]any{
			"organization_slug": slug, "name": slug + "-web", "type": "spa",
			"redirect_uris": []any{"http://localhost:3000/callback"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := dbCreateUser(ctx.AppDB(), "test-proj", oid, "user@"+slug+".example", "", "User", emailPending, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, platform
}

func sendResetFor(t *testing.T, ctx *sdk.AppCtx, slug string) error {
	t.Helper()
	org, err := dbGetOrgBySlug(ctx.AppDB(), "test-proj", slug)
	if err != nil {
		t.Fatal(err)
	}
	u, err := dbGetUserByEmail(ctx.AppDB(), "test-proj", org.ID, "user@"+slug+".example")
	if err != nil {
		t.Fatal(err)
	}
	return issueResetToken(ctx, "test-proj", org, u.ID, u.Email)
}

func TestOrgSenderOverridesInstallSenderForThatOrgOnly(t *testing.T) {
	ctx, platform := newSenderCtx(t, "noreply@platform.example")
	app := &App{}
	if _, err := app.toolOrgsUpdate(ctx, map[string]any{
		"organization_slug": "runlane",
		"policy_overrides":  `{"from_email":"hello@runlane.example","from_name":"Runlane Team"}`,
	}); err != nil {
		t.Fatal(err)
	}
	ev := lastAudit(t, ctx, "org_updated")
	if !strings.Contains(ev.Metadata, `"email_sender":{"from":{"from_email":"","from_name":""},"to":{"from_email":"hello@runlane.example","from_name":"Runlane Team"}}`) {
		t.Fatalf("org_updated did not record the sender change: %s", ev.Metadata)
	}

	if err := sendResetFor(t, ctx, "runlane"); err != nil {
		t.Fatal(err)
	}
	if platform.input["from"] != "hello@runlane.example" || platform.input["from_name"] != "Runlane Team" {
		t.Fatalf("runlane sent from %v <%v>", platform.input["from_name"], platform.input["from"])
	}

	if err := sendResetFor(t, ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if platform.input["from"] != "noreply@platform.example" || platform.input["from_name"] != "Default" {
		t.Fatalf("default org sent from %v <%v>", platform.input["from_name"], platform.input["from"])
	}
}

func TestOrgSenderWorksWithoutInstallSender(t *testing.T) {
	ctx, platform := newSenderCtx(t, "")
	app := &App{}
	if err := sendResetFor(t, ctx, "runlane"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("no sender anywhere: err=%v", err)
	}
	if _, err := app.toolOrgsUpdate(ctx, map[string]any{
		"organization_slug": "runlane",
		"policy_overrides":  `{"from_email":"hello@runlane.example"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sendResetFor(t, ctx, "runlane"); err != nil {
		t.Fatal(err)
	}
	// Without from_name the display name falls back to the organization name.
	if platform.input["from"] != "hello@runlane.example" || platform.input["from_name"] != "Runlane" {
		t.Fatalf("runlane sent from %v <%v>", platform.input["from_name"], platform.input["from"])
	}
	// The other organization still has no sender.
	if err := sendResetFor(t, ctx, "default"); err == nil {
		t.Fatal("default org sent email without any configured sender")
	}
}

func TestOrgSenderOverridesAreValidated(t *testing.T) {
	for _, bad := range []string{
		`{"from_email":"not-an-address"}`,
		`{"from_email":"Runlane <hello@runlane.example>"}`,
		`{"from_email":42}`,
		`{"from_email":"hello@runlane.example","from_name":"Runlane\r\nBcc: x@evil.example"}`,
		`{"from_email":"hello@runlane.example","from_name":""}`,
		`{"from_email":"hello@runlane.example","from_name":" Runlane"}`,
		`{"from_email":"hello@runlane.example","from_name":"` + strings.Repeat("x", maxFromNameLength+1) + `"}`,
		`{"from_name":7}`,
	} {
		if err := validatePolicyJSON(&bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	good := `{"from_email":"hello@runlane.example","from_name":"Runlane","lockout_threshold":5}`
	if err := validatePolicyJSON(&good); err != nil {
		t.Fatal(err)
	}
}

func TestRequestBrandNameStillWinsOverOrgFromName(t *testing.T) {
	org := &Organization{Name: "Runlane"}
	if got := recoveryBrand(org, "Runlane Console", "Runlane Team"); got != "Runlane Console" {
		t.Fatalf("brand=%q", got)
	}
	if got := recoveryBrand(org, "", "Runlane Team"); got != "Runlane Team" {
		t.Fatalf("brand=%q", got)
	}
	if got := recoveryBrand(org, "", ""); got != "Runlane" {
		t.Fatalf("brand=%q", got)
	}
}
