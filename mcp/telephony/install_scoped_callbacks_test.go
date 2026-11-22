package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Two telephony installs can share a project. The platform resolves the
// name-only form (/api/apps/telephony/...?project_id=) to the project's default
// install, so every URL handed to a carrier must name its own installation.
func TestCarrierCallbacksTargetOwningInstall(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://agents.example.test")
	route := routeRow{ID: "route", CarrierSlug: "telnyx", Secret: "route-secret", ProjectID: "project-a"}
	for _, install := range []int64{19, 57} {
		a := &App{installID: install}
		prefix := "https://agents.example.test/api/apps/telephony/_install/" + itoa(install) + "/"
		urls := map[string]string{
			"inbound":             a.inboundRouteURL(route),
			"status":              a.statusCallbackURL("call", "secret", "project-a"),
			"twilio stream":       a.twilioStreamStatusURL("call", "secret", "project-a"),
			"twilio recording":    a.twilioRecordingStatusURL("call", "secret", "project-a"),
			"twilio route status": a.twilioRouteStatusURL(route),
			"plivo recording":     a.plivoRecordingStatusURL("call", "secret", "project-a"),
		}
		for name, raw := range urls {
			if !strings.HasPrefix(raw, prefix) {
				t.Fatalf("install %d %s URL %q does not target its install", install, name, raw)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("project_id") != "project-a" {
				t.Fatalf("install %d %s URL lost project_id: %s", install, name, raw)
			}
		}
	}
}

func TestCarrierCallbacksFallBackWithoutInstallID(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://agents.example.test")
	a := &App{}
	got := a.inboundRouteURL(routeRow{ID: "route", CarrierSlug: "telnyx", Secret: "s", ProjectID: "p"})
	if !strings.HasPrefix(got, "https://agents.example.test/api/apps/telephony/inbound/telnyx/route?") {
		t.Fatalf("fallback inbound URL = %q", got)
	}
}

// Routes configured before install-scoped URLs keep the name-only URL on the
// carrier. Signatures over either form must verify; anything else must not.
func TestCarrierSignatureAcceptsBothOwnedURLForms(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://agents.example.test")
	a := &App{installID: 42}
	form := url.Values{"CallSid": {"CA1"}}
	req := httptest.NewRequest(http.MethodPost, "/inbound/twilio/route/status?project_id=p&secret=s", strings.NewReader(form.Encode()))
	candidates := a.publicRequestURLs(req)
	want := []string{
		"https://agents.example.test/api/apps/telephony/_install/42/inbound/twilio/route/status?project_id=p&secret=s",
		"https://agents.example.test/api/apps/telephony/inbound/twilio/route/status?project_id=p&secret=s",
	}
	if len(candidates) != 2 || candidates[0] != want[0] || candidates[1] != want[1] {
		t.Fatalf("signature candidates = %q", candidates)
	}
	for _, signedURL := range want {
		sig := twilioTestSignature(signedURL, form, "test-auth-token")
		ok := false
		for _, c := range candidates {
			ok = ok || verifyTwilioSignature(c, form, "test-auth-token", sig)
		}
		if !ok {
			t.Fatalf("signature over owned URL %q rejected", signedURL)
		}
	}
	foreign := twilioTestSignature("https://evil.example/api/apps/telephony/inbound/twilio/route/status?project_id=p&secret=s", form, "test-auth-token")
	for _, c := range candidates {
		if verifyTwilioSignature(c, form, "test-auth-token", foreign) {
			t.Fatal("signature over a foreign URL accepted")
		}
	}
	// Media streams keep their single install-scoped form.
	media := httptest.NewRequest(http.MethodGet, "/media/twilio/call/secret", nil)
	if got := a.publicRequestURLs(media); len(got) != 1 {
		t.Fatalf("media signature candidates = %q", got)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
