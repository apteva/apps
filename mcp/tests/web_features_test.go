package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// webFixture builds an existing-mode suite backed by a fake Computer.
func webFixture(t *testing.T, computer *computerStub, config map[string]string, session *SessionConfig) (*App, *store, Suite, func(Check) Run) {
	t.Helper()
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app == "computer" {
			return computer.call(tool, args, out)
		}
		return fmt.Errorf("unexpected app %s", app)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform), tk.WithConfig(config))
	suite, err := s.saveSuite(Suite{Name: "CRM", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"web": {BaseURL: "https://crm.example.com"}}},
		Credentials: map[string]CredentialRef{"operator": {Ref: "operator"}}, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	run := func(checks ...Check) Run {
		t.Helper()
		for _, check := range checks {
			check.SuiteID = suite.ID
			if _, err := s.saveCheck(check); err != nil {
				t.Fatal(err)
			}
		}
		r, err := a.queue(context.Background(), app, suite.ID, "tool", "", QueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err = a.work(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		done, err := s.run(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return done
	}
	return a, &s, suite, func(c Check) Run { return run(c) }
}

func browserCheck(name string, steps ...Step) Check {
	return Check{Name: name, Kind: "browser", Enabled: true, Definition: Definition{Steps: steps}}
}

func openStep() Step {
	return Step{ID: "open", Kind: "browser", Browser: &BrowserAction{Action: "navigate", Target: "web", Path: "/?screen=admin_dashboard"}}
}

func observeStep(id string, locator BrowserLocator, assertions ...Assertion) Step {
	return Step{ID: id, Kind: "browser", Browser: &BrowserAction{Action: "observe", Locator: locator}, Assertions: assertions}
}

func element(text string, visible bool, extra map[string]any) map[string]any {
	e := map[string]any{"text": text, "visible": visible}
	for k, v := range extra {
		e[k] = v
	}
	return map[string]any{"count": 1, "elements": []any{e}}
}

func TestReadElementPollsHiddenStatus(t *testing.T) {
	status := `[data-testid="fx-api-test-status"]`
	computer := &computerStub{v14: true, reads: map[string][]map[string]any{
		status: {element("loading", false, nil), element("loading", false, nil),
			element("ready", false, map[string]any{"attributes": map[string]any{"data-state": "ready"}})},
		"#email": {element("", true, map[string]any{"value": "ada@example.com"})},
		"#pw":    {element("", true, map[string]any{"value_redacted": true})},
	}}
	_, _, _, run := webFixture(t, computer, nil, nil)
	ready := observeStep("ready", BrowserLocator{TestID: "fx-api-test-status"},
		Assertion{Path: "/text", Op: "equals", Value: "ready"},
		Assertion{Path: "/attributes/data-state", Op: "equals", Value: "ready"},
		Assertion{Path: "/count", Op: "equals", Value: float64(1)},
		Assertion{Path: "/visible", Op: "equals", Value: false})
	ready.PollEveryMS, ready.TimeoutMS = 100, 5000
	r := run(browserCheck("dashboard", openStep(), ready,
		observeStep("email", BrowserLocator{Selector: "#email"}, Assertion{Path: "/value", Op: "equals", Value: "ada@example.com"}),
		observeStep("pw", BrowserLocator{Selector: "#pw"}, Assertion{Path: "/redacted", Op: "equals", Value: true})))
	if r.Status != "passed" {
		t.Fatal(r.Status, r.Error, r.Results)
	}
	raw, _ := json.Marshal(r.Results)
	if !strings.Contains(string(raw), `"path":"/attributes/data-state","value":"ready"`) {
		t.Fatal("attribute assertion evidence missing", string(raw))
	}
	reads := 0
	for _, use := range computer.uses {
		if use["action"] == "read_element" && use["selector"] == status {
			reads++
		}
	}
	if reads != 3 {
		t.Fatalf("status read %d times, want 3 polls", reads)
	}
}

func TestDiagnosticsFailureRules(t *testing.T) {
	targets := map[string]*url.URL{"web": {Scheme: "https", Host: "crm.example.com"}}
	defaults, on := resolveDiagnosticRules(nil, targets)
	if !on {
		t.Fatal("diagnostics off by default")
	}
	cases := []struct {
		req  pageRequest
		fail bool
	}{
		{pageRequest{Origin: "https://crm.example.com", Path: "/api/calls", Status: 500}, true},
		{pageRequest{Origin: "https://crm.example.com", Path: "/api/me", Status: 401}, true},
		{pageRequest{Origin: "https://crm.example.com", Path: "/api/x", Error: "net::ERR_CONNECTION_REFUSED"}, true},
		{pageRequest{Origin: "https://crm.example.com", Path: "/api/x", Error: "net::ERR_ABORTED"}, false},
		{pageRequest{Origin: "https://cdn.other.com", Path: "/font.woff", Status: 404}, false},
		{pageRequest{Origin: "https://crm.example.com", Path: "/api/x", Status: 304}, false},
	}
	for _, c := range cases {
		if got := defaults.failedRequest(c.req); got != c.fail {
			t.Errorf("%+v: fail=%v", c.req, got)
		}
	}
	custom, _ := resolveDiagnosticRules(&DiagnosticsConfig{FailOrigins: []string{"https://api.example.com"},
		Allow: []DiagnosticAllow{{PathPrefix: "/api/telemetry", Status: 404}}}, targets)
	if !custom.failedRequest(pageRequest{Origin: "https://api.example.com", Path: "/v1/calls", Status: 502}) {
		t.Error("fail_origins not applied")
	}
	if custom.failedRequest(pageRequest{Origin: "https://crm.example.com", Path: "/api/telemetry/ping", Status: 404}) ||
		!custom.failedRequest(pageRequest{Origin: "https://crm.example.com", Path: "/api/telemetry/ping", Status: 500}) {
		t.Error("allow entry not applied by status")
	}
	only, _ := resolveDiagnosticRules(&DiagnosticsConfig{FailOn: []string{"exceptions"}}, targets)
	if only.failedRequest(pageRequest{Origin: "https://crm.example.com", Path: "/x", Status: 500}) || !only.exceptions || !only.truncated {
		t.Error("fail_on not applied")
	}
	var off DiagnosticsConfig
	if err := json.Unmarshal([]byte(`"off"`), &off); err != nil || !off.Off {
		t.Fatal(err)
	}
	for _, bad := range []string{`"on"`, `{"fail_on":["warnings"]}`, `{"console_errors":"maybe"}`, `{"allow":[{"path_prefix":"api"}]}`, `{"fail_origins":["ftp://x"]}`} {
		var d DiagnosticsConfig
		if json.Unmarshal([]byte(bad), &d) == nil && d.validate() == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestDiagnosticsFailChecksAndAreRecorded(t *testing.T) {
	status := `[data-testid="fx-api-test-status"]`
	ready := func() Step {
		return observeStep("ready", BrowserLocator{TestID: "fx-api-test-status"}, Assertion{Path: "/text", Op: "equals", Value: "ready"})
	}
	reads := func() map[string][]map[string]any {
		return map[string][]map[string]any{status: {element("ready", false, nil)}}
	}
	t.Run("target request 500", func(t *testing.T) {
		computer := &computerStub{v14: true, reads: reads(), diagnostics: []map[string]any{
			{"requests": []any{map[string]any{"method": "GET", "origin": "https://crm.example.com", "path": "/api/calls", "status": 500, "type": "fetch"}}},
		}}
		_, _, _, run := webFixture(t, computer, nil, nil)
		r := run(browserCheck("dashboard", openStep(), ready()))
		if r.Status != "failed" || len(r.Results) != 1 {
			t.Fatal(r.Status, r.Results)
		}
		raw, _ := json.Marshal(r.Results[0].Output)
		if !strings.Contains(string(raw), "GET https://crm.example.com/api/calls returned 500") || !strings.Contains(string(raw), `"path":"/api/calls"`) {
			t.Fatal(string(raw))
		}
		if computer.opens[0]["diagnostics"] != true {
			t.Fatal("session opened without diagnostics")
		}
	})
	t.Run("page exception after last step", func(t *testing.T) {
		computer := &computerStub{v14: true, reads: reads(), diagnostics: []map[string]any{{}, {}, {"exceptions": []any{"TypeError: calls is undefined"}}}}
		_, _, _, run := webFixture(t, computer, nil, nil)
		r := run(browserCheck("dashboard", openStep(), ready()))
		raw, _ := json.Marshal(r.Results)
		if r.Status != "failed" || !strings.Contains(string(raw), "page exception: TypeError: calls is undefined") || !strings.Contains(string(raw), `"exceptions":["TypeError: calls is undefined"]`) {
			t.Fatal(r.Status, string(raw))
		}
		if fmt.Sprint(computer.sinces) != "[0 1 2]" {
			t.Fatal("cursor not carried forward", computer.sinces)
		}
	})
	t.Run("other origins and console errors are recorded only", func(t *testing.T) {
		computer := &computerStub{v14: true, reads: reads(), diagnostics: []map[string]any{
			{"console_errors": 3, "requests": []any{map[string]any{"origin": "https://cdn.other.com", "path": "/x.js", "status": 404}}},
		}}
		_, _, _, run := webFixture(t, computer, nil, nil)
		if r := run(browserCheck("dashboard", openStep(), ready())); r.Status != "passed" {
			t.Fatal(r.Status, r.Results)
		}
	})
	t.Run("off", func(t *testing.T) {
		computer := &computerStub{v14: true, reads: reads(), diagnostics: []map[string]any{{"exceptions": []any{"boom"}}}}
		_, _, _, run := webFixture(t, computer, nil, nil)
		check := browserCheck("dashboard", openStep(), ready())
		check.Definition.Diagnostics = &DiagnosticsConfig{Off: true}
		if r := run(check); r.Status != "passed" || computer.opens[0]["diagnostics"] != nil || len(computer.sinces) != 0 {
			t.Fatal(r.Status, computer.opens[0], computer.sinces)
		}
	})
}

func TestMaskedScreenshotAfterCredential(t *testing.T) {
	credentials := map[string]string{"credentials": `{"project-a":{"operator":{"email":"ada@example.com","password":"` + testPassword + `"}}}`}
	login := func() Check {
		return browserCheck("login", openStep(),
			Step{ID: "password", Kind: "browser", Browser: &BrowserAction{Action: "fill", Locator: BrowserLocator{TestID: "password"}, ValueRef: "credentials.operator.password"}},
			observeStep("signed-in", BrowserLocator{Selector: "#missing"}, Assertion{Path: "/visible", Op: "equals", Value: true}))
	}
	t.Run("masked", func(t *testing.T) {
		computer := &computerStub{v14: true}
		_, s, _, run := webFixture(t, computer, credentials, nil)
		r := run(login())
		if r.Status != "failed" || len(computer.shots) != 1 {
			t.Fatal(r.Status, computer.shots)
		}
		mask, _ := computer.shots[0]["mask"].(map[string]any)
		if mask == nil || mask["inputs"] != true || !containsString(mask["secrets"].([]string), testPassword) {
			t.Fatal("screenshot not masked", computer.shots[0])
		}
		raw, _ := json.Marshal(r.Results)
		if !strings.Contains(string(raw), `"artifact_id"`) {
			t.Fatal("masked screenshot not kept", string(raw))
		}
		if strings.Contains(storedText(t, *s), testPassword) {
			t.Fatal("password stored")
		}
	})
	t.Run("mask failure stores nothing", func(t *testing.T) {
		computer := &computerStub{v14: true, maskFail: true}
		_, _, _, run := webFixture(t, computer, credentials, nil)
		r := run(login())
		raw, _ := json.Marshal(r.Results)
		if !strings.Contains(string(raw), `"screenshot_skipped":"mask_failed"`) || strings.Contains(string(raw), `"artifact_id"`) {
			t.Fatal(string(raw))
		}
	})
	t.Run("old Computer skips the screenshot", func(t *testing.T) {
		computer := &computerStub{}
		_, _, _, run := webFixture(t, computer, credentials, nil)
		run(login())
		if len(computer.shots) != 0 {
			t.Fatal("unmasked screenshot taken", computer.shots)
		}
	})
}

func TestSignInContextPurgedAtRunEnd(t *testing.T) {
	session := &SessionConfig{Kind: "browser", SetupCheck: "login"}
	t.Run("retry once", func(t *testing.T) {
		computer := &computerStub{v14: true, refuse: 1}
		_, _, _, run := webFixture(t, computer, nil, session)
		r := run(browserCheck("login", openStep()))
		if r.Status != "passed" || len(computer.deletes) != 2 || computer.deletes[0]["purge_profile"] != true || len(r.Warnings) != 0 {
			t.Fatal(r.Status, computer.deletes, r.Warnings)
		}
	})
	t.Run("warning, not failure", func(t *testing.T) {
		computer := &computerStub{v14: true, refuse: 5}
		_, _, _, run := webFixture(t, computer, nil, session)
		r := run(browserCheck("login", openStep()))
		if r.Status != "passed" || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "was not deleted") {
			t.Fatal(r.Status, r.Warnings)
		}
	})
	t.Run("old Computer", func(t *testing.T) {
		computer := &computerStub{}
		_, _, _, run := webFixture(t, computer, nil, session)
		run(browserCheck("login", openStep()))
		if len(computer.deletes) != 1 || computer.deletes[0]["purge_profile"] != nil {
			t.Fatal(computer.deletes)
		}
	})
}

func TestTextAssertionsNeedComputer014(t *testing.T) {
	computer := &computerStub{}
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		return computer.call(tool, args, out)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	suite, err := s.saveSuite(Suite{Name: "CRM", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"web": {BaseURL: "https://crm.example.com"}}}})
	if err != nil {
		t.Fatal(err)
	}
	check := browserCheck("dashboard", openStep(), observeStep("ready", BrowserLocator{TestID: "status"}, Assertion{Path: "/text", Op: "equals", Value: "ready"}))
	check.SuiteID = suite.ID
	raw, _ := json.Marshal(check)
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	if _, err = a.dispatch(context.Background(), app, "tests_check_save", args); err == nil || err.Error() != computerReadVersionMessage {
		t.Fatal(err)
	}
	// A check saved before Computer was downgraded fails at run time.
	if _, err = s.saveCheck(check); err != nil {
		t.Fatal(err)
	}
	r, _ := a.queue(context.Background(), app, suite.ID, "tool", "", QueueOptions{})
	if err = a.work(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	done, _ := s.run(r.ID)
	out, _ := json.Marshal(done.Results)
	if done.Status != "failed" || !strings.Contains(string(out), computerReadVersionMessage) {
		t.Fatal(done.Status, string(out))
	}
	computer.v14 = true
	if _, err = a.dispatch(context.Background(), app, "tests_check_save", args); err != nil {
		t.Fatal(err)
	}
}
