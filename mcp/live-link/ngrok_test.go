package main

// Unit coverage for the ngrok provider addition. End-to-end testing
// (real ngrok binary, real Cloudflare API, real tunnel) lives in the
// scenarios/ directory and runs opt-in.

import (
	"regexp"
	"testing"
)

// TestNgrokURL_Logfmt_Matches confirms the regex pulls the
// public URL out of ngrok's logfmt info line. Three shapes covered:
// free tier (*.ngrok-free.app), paid tier random (*.ngrok.app), and
// the legacy *.ngrok.io which ngrok still serves for some accounts.
func TestNgrokURL_Logfmt_Matches(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"free tier",
			`t=2026-05-18T13:24:09+0200 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://localhost:5280 url=https://abc-12-34-56.ngrok-free.app`,
			"https://abc-12-34-56.ngrok-free.app",
		},
		{
			"paid random",
			`t=2026-05-18T13:24:09+0200 lvl=info msg="started tunnel" url=https://random-words.ngrok.app addr=http://localhost:5280`,
			"https://random-words.ngrok.app",
		},
		{
			"legacy domain",
			`url=https://abc.ngrok.io`,
			"https://abc.ngrok.io",
		},
		{
			"reserved subdomain",
			`url=https://myapp.ngrok.app`,
			"https://myapp.ngrok.app",
		},
	}
	for _, c := range cases {
		groups := ngrokURL.FindStringSubmatch(c.line)
		if len(groups) < 2 {
			t.Errorf("%s: no match for %q", c.name, c.line)
			continue
		}
		if groups[1] != c.want {
			t.Errorf("%s: got %q, want %q", c.name, groups[1], c.want)
		}
	}
}

// TestNgrokURL_Logfmt_DoesNotMatchNoise confirms unrelated lines
// (which ngrok emits constantly during the connection handshake) don't
// produce a false URL — scanForURL only records the first match, but
// false matches early on would lock in a bad publicURL.
func TestNgrokURL_Logfmt_DoesNotMatchNoise(t *testing.T) {
	noise := []string{
		`t=... lvl=info msg="open config file" path=/Users/x/.config/ngrok/ngrok.yml`,
		`t=... lvl=info msg="starting web service" obj=web addr=127.0.0.1:4040`,
		`t=... lvl=info msg="client session established" obj=tunnels.session`,
		`t=... lvl=warn msg="reconnecting"`,
	}
	for _, line := range noise {
		if m := ngrokURL.FindString(line); m != "" {
			t.Errorf("ngrokURL should not match noise line; got %q from %q", m, line)
		}
	}
}

// TestStartParams_ValidationByMode exercises the per-mode required-
// field checks in Manager.Start. We don't actually start subprocesses
// here — pre-validation runs before exec.Command is touched, and an
// invalid params struct should return synchronously without spawning
// anything.
func TestStartParams_ValidationByMode(t *testing.T) {
	cases := []struct {
		name      string
		params    StartParams
		wantMatch *regexp.Regexp
	}{
		{
			"ngrok-no-target",
			StartParams{Binary: "ngrok", Mode: ModeNgrok, Authtoken: "tok", RunID: 1},
			regexp.MustCompile(`target URL is empty`),
		},
		{
			"ngrok-no-authtoken",
			StartParams{Binary: "ngrok", Mode: ModeNgrok, Target: "http://127.0.0.1:5280", RunID: 1},
			regexp.MustCompile(`authtoken is empty`),
		},
		{
			"quick-no-target",
			StartParams{Binary: "cloudflared", Mode: ModeQuick, RunID: 1},
			regexp.MustCompile(`target URL is empty`),
		},
		{
			"named-no-token",
			StartParams{Binary: "cloudflared", Mode: ModeNamed, Hostname: "tun.example.com", RunID: 1},
			regexp.MustCompile(`tunnel token is empty`),
		},
	}
	for _, c := range cases {
		m := &Manager{status: StatusIdle}
		err := m.Start(c.params)
		if err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
			continue
		}
		if !c.wantMatch.MatchString(err.Error()) {
			t.Errorf("%s: got %q, want match /%s/", c.name, err.Error(), c.wantMatch)
		}
	}
}

// TestNgrokProvider_ConfiguredFalseWithoutBinding sanity-checks the
// "configured" flag is false when the ngrok integration hasn't been
// bound. activeProvider relies on this returning false to fall
// through to cloudflare-quick on a fresh install.
func TestNgrokProvider_ConfiguredFalseWithoutBinding(t *testing.T) {
	p := &ngrokProvider{}
	// nil ctx → false (defensive)
	if p.Configured(nil) {
		t.Error("Configured should be false with nil ctx")
	}
	// A non-nil ctx whose IntegrationFor returns nil would also be
	// false — but constructing an *sdk.AppCtx without the platform
	// plumbing is the integration test's job; the unit-level
	// guarantee is the nil-ctx path above.
}
