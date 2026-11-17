package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestFixtureErrorsArePrecise(t *testing.T) {
	release := &ReleaseConfig{EnvironmentID: "env_a", EnvironmentRevision: 3}
	cases := []struct {
		def  environmentDefinition
		want string
	}{
		{environmentDefinition{ID: "env_b", SpecVersion: 3, Spec: json.RawMessage(`{}`)}, `environment ID mismatch: pinned "env_a"`},
		{environmentDefinition{ID: "env_a", SpecVersion: 4, Spec: json.RawMessage(`{}`)}, "pinned revision v3, found v4"},
		{environmentDefinition{ID: "env_a", SpecVersion: 3}, "has an empty spec"},
		{environmentDefinition{ID: "env_a", SpecVersion: 3, Spec: json.RawMessage(`"` + strings.Repeat("x", maxPayload) + `"`)}, "accept up to 256 KiB"},
	}
	for _, c := range cases {
		if err := checkFixture(c.def, release); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("want %q, got %v", c.want, err)
		}
	}
}

func TestDecodedPointerAndBudgets(t *testing.T) {
	output := map[string]any{"response": `{"items":[{"total":3}]}`}
	results := evaluate(output, []Assertion{
		{Path: "/response/items/0/total", Op: "equals", Value: float64(3), Decode: "json"},
		{Path: "/response/items/0/total", Op: "exists"},
	})
	if !results[0].Passed || results[1].Passed {
		t.Fatal(results)
	}
	if err := validateAssertions([]Assertion{{Path: "/x", Op: "exists", Decode: "yaml"}}); err == nil {
		t.Fatal("accepted unknown decode")
	}
	row := StepResult{Status: "passed", DurationMS: 50}
	applyBudget(&row, Step{BudgetMS: 20})
	if row.Status != "failed" || !strings.Contains(row.Error, "over its 20 ms budget") || row.Assertions[0].Path != "/duration_ms" {
		t.Fatal(row)
	}
	row = StepResult{Status: "passed", DurationMS: 5}
	applyBudget(&row, Step{BudgetMS: 20})
	if row.Status != "passed" || !row.Assertions[0].Passed {
		t.Fatal(row)
	}
}

func TestTargetValidation(t *testing.T) {
	_, _, s := fixture(t)
	bad := []Suite{
		{Name: "x", Target: &TargetConfig{Mode: "elsewhere"}},
		{Name: "x", Target: &TargetConfig{Mode: "existing"}},
		{Name: "x", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: "ftp://x"}}}},
		{Name: "x", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: "https://a.test/api?x=1"}}}},
		{Name: "x", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: "https://a.test"}}, Candidate: &CandidateRef{DeploymentID: 2, Build: "latest_succeeded"}}},
		{Name: "x", Target: &TargetConfig{Mode: "isolated", Environment: &GeneratedEnvironment{Apps: []string{"api"}}, Targets: map[string]TargetEndpoint{"web": {App: "site", Path: "/"}}}},
		{Name: "x", Target: &TargetConfig{Mode: "isolated", Environment: &GeneratedEnvironment{Apps: []string{"api"}}}, Session: &SessionConfig{Kind: "browser", SetupCheck: "login"}},
		{Name: "x", Session: &SessionConfig{Kind: "bearer", SetupCheck: "login", Capture: "token"}},
		{Name: "x", Release: browserReleaseConfig(), Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: "https://a.test"}}}},
		{Name: "x", Credentials: map[string]CredentialRef{"bad alias": {Ref: "op"}}},
	}
	for i, suite := range bad {
		if _, err := s.saveSuite(suite); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	suite, err := s.saveSuite(Suite{Name: "staging", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{
		"api": {BaseURL: "https://staging.example.com/api"}, "frontend": {BaseURL: "https://staging.example.com/ui"}}},
		Credentials: map[string]CredentialRef{"operator": {Ref: "operator"}}})
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := s.getSuite(suite.ID); err != nil || loaded.Target == nil || loaded.Credentials["operator"].Ref != "operator" {
		t.Fatal(loaded, err)
	}
	browserStep := func(action BrowserAction, assertions ...Assertion) Check {
		return Check{SuiteID: suite.ID, Name: "b", Kind: "browser", Definition: Definition{Steps: []Step{{ID: "s", Kind: "browser", Browser: &action, Assertions: assertions}}}}
	}
	rejected := []Check{
		browserStep(BrowserAction{Action: "click", Locator: BrowserLocator{Role: "button", Name: "Go"}}),
		browserStep(BrowserAction{Action: "click", Locator: BrowserLocator{Text: "Go"}}),
		browserStep(BrowserAction{Action: "navigate", Target: "admin", Path: "/"}),
		browserStep(BrowserAction{Action: "fill", Locator: BrowserLocator{TestID: "pw"}, ValueRef: "credentials.unknown.password"}),
		browserStep(BrowserAction{Action: "observe", Locator: BrowserLocator{Selector: "#x"}}, Assertion{Path: "/count", Op: "equals", Value: float64(1)}),
		{SuiteID: suite.ID, Name: "h", Kind: "http", Definition: Definition{Target: "nope", Path: "/x", Assertions: []Assertion{{Path: "/status_code", Op: "exists"}}}},
		{SuiteID: suite.ID, Name: "h", Kind: "http", Definition: Definition{URL: "https://a.test", HeaderRefs: map[string]HeaderRef{"Authorization": {Ref: "credentials.operator.token"}}, Assertions: []Assertion{{Path: "/status_code", Op: "exists"}}}},
		{SuiteID: suite.ID, Name: "h", Kind: "http", Definition: Definition{Target: "api", Path: "/x", HeaderRefs: map[string]HeaderRef{"Host": {Ref: "credentials.operator.token"}}, Assertions: []Assertion{{Path: "/status_code", Op: "exists"}}}},
	}
	for i, check := range rejected {
		if _, err := s.saveCheck(check); err == nil {
			t.Fatalf("check %d accepted", i)
		}
	}
	accepted := []Check{
		browserStep(BrowserAction{Action: "fill", Locator: BrowserLocator{TestID: "pw"}, ValueRef: "credentials.operator.password"}),
		browserStep(BrowserAction{Action: "observe", Locator: BrowserLocator{Text: "Welcome"}}, Assertion{Path: "/visible", Op: "equals", Value: true}),
		{SuiteID: suite.ID, Name: "h", Kind: "http", Definition: Definition{Target: "api", Path: "/health", BudgetMS: 500, HeaderRefs: map[string]HeaderRef{"X-API-Key": {Ref: "credentials.operator.key"}}, Assertions: []Assertion{{Path: "/status_code", Op: "equals", Value: float64(200)}}}},
	}
	for i, check := range accepted {
		if _, err := s.saveCheck(check); err != nil {
			t.Fatalf("check %d rejected: %v", i, err)
		}
	}
}

// deployStub answers Deploy calls; live returns the current live build.
type deployStub struct {
	mu   sync.Mutex
	live func() int64
}

func digestFor(build int64) string { return fmt.Sprintf("%064x", build) }

func (d *deployStub) call(tool string, args map[string]any, out any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var reply any
	switch tool {
	case "deploy_status":
		if args["_project_id"] != "project-a" {
			return errors.New("missing project")
		}
		reply = map[string]any{"current_release": map[string]any{"build_id": d.live()}, "builds": []any{}}
	case "deploy_candidate_digest":
		if args["build"] == "latest_succeeded" {
			id := d.live()
			reply = map[string]any{"build_id": id, "candidate_digest": digestFor(id)}
			break
		}
		id := int64(args["build_id"].(int64))
		reply = map[string]any{"build_id": id, "candidate_digest": digestFor(id)}
	default:
		return fmt.Errorf("unexpected deploy tool %s", tool)
	}
	raw, _ := json.Marshal(reply)
	return json.Unmarshal(raw, out)
}

const testPassword = "s3cret-pw-value"
const testToken = "tok-abcdef-123456"

func existingServer(t *testing.T, loginStatus int) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			if r.Header.Get("X-Login-Password") != testPassword || loginStatus != 200 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"token":%q}`, testToken)
		case "/api/me":
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Echo secrets to prove they are scrubbed before storage.
			fmt.Fprintf(w, `{"name":"Ada","echo":%q,"token":%q}`, testPassword, testToken)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func existingSuite(t *testing.T, a *App, s store, base string) Suite {
	t.Helper()
	suite, err := s.saveSuite(Suite{Name: "Staging API", Environment: "staging",
		Target:      &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: base + "/api"}}, Candidate: &CandidateRef{DeploymentID: 3}},
		Credentials: map[string]CredentialRef{"operator": {Ref: "operator"}},
		Session:     &SessionConfig{Kind: "bearer", SetupCheck: "login", Capture: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	checks := []Check{
		{SuiteID: suite.ID, Name: "me", Kind: "scenario", Enabled: true, Definition: Definition{Steps: []Step{{ID: "me", Kind: "http", Target: "api", Path: "/me",
			Assertions: []Assertion{{Path: "/json/name", Op: "equals", Value: "Ada"}}, Evidence: []string{"/json/echo", "/body"}}}}},
		{SuiteID: suite.ID, Name: "login", Kind: "scenario", Enabled: true, Definition: Definition{Session: "none", Steps: []Step{{ID: "login", Kind: "http", Method: "POST", Target: "api", Path: "/login",
			HeaderRefs: map[string]HeaderRef{"X-Login-Password": {Ref: "credentials.operator.password"}},
			Assertions: []Assertion{{Path: "/status_code", Op: "equals", Value: float64(200)}}, Capture: map[string]string{"token": "/json/token"}}}}},
		{SuiteID: suite.ID, Name: "me (plain)", Kind: "http", Enabled: true, Definition: Definition{Target: "api", Path: "/me", BudgetMS: 5000,
			Assertions: []Assertion{{Path: "/json/name", Op: "equals", Value: "Ada"}}}},
	}
	for _, check := range checks {
		if _, err := s.saveCheck(check); err != nil {
			t.Fatal(err)
		}
	}
	return suite
}

func existingFixture(t *testing.T, live func() int64) (*App, *deployStub, store, func() error) {
	deploy := &deployStub{live: live}
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app == "deploy" {
			return deploy.call(tool, args, out)
		}
		return fmt.Errorf("unexpected app %s", app)
	}}
	credentials := `{"project-a":{"operator":{"password":"` + testPassword + `"}}}`
	a, app, s := fixture(t, tk.WithPlatform(platform), tk.WithConfig(map[string]string{"credentials": credentials}))
	return a, deploy, s, func() error { return a.work(context.Background(), app) }
}

func storedText(t *testing.T, s store) string {
	t.Helper()
	var all []string
	for _, query := range []string{`SELECT output||assertions||error FROM test_results`, `SELECT error||release_record||snapshot||fixture_spec FROM test_runs`} {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var text string
			if err = rows.Scan(&text); err != nil {
				t.Fatal(err)
			}
			all = append(all, text)
		}
		rows.Close()
	}
	return strings.Join(all, "\n")
}

func TestExistingModeBearerSessionCredentialsAndVerdict(t *testing.T) {
	server := existingServer(t, 200)
	a, _, s, work := existingFixture(t, func() int64 { return 7 })
	suite := existingSuite(t, a, s, server.URL)
	app := a.ctx
	r, err := a.queue(context.Background(), app, suite.ID, "tool", "", QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Release == nil || r.Release.Mode != "existing" || r.Release.CandidateDigest != digestFor(7) || r.Release.BuildID != 7 {
		t.Fatal(r.Release)
	}
	if err = work(); err != nil {
		t.Fatal(err)
	}
	done, err := s.run(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "passed" || done.Passed != 3 || done.Error != "" {
		t.Fatal(done.Status, done.Passed, done.Failed, done.Error, done.Results)
	}
	if done.Results[0].Name != "login" {
		t.Fatal("setup check did not run first")
	}
	spec, err := a.releaseSpec(context.Background(), app, suite.ID)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := s.verdict(suite.ID, QueueOptions{CandidateDigest: digestFor(7), SuiteRevision: spec["suite_revision"].(string), FixtureRevision: spec["fixture_revision"].(string)})
	if err != nil || !verdict.Allowed || verdict.State != "passed" {
		t.Fatal(verdict, err)
	}
	text := storedText(t, s)
	if strings.Contains(text, testPassword) || strings.Contains(text, testToken) {
		i := strings.Index(text, testToken)
		if i < 0 {
			i = strings.Index(text, testPassword)
		}
		t.Fatal("a credential or session token was stored: ", text[max(0, i-300):min(len(text), i+100)])
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatal("expected redaction markers in stored output")
	}
}

func TestExistingModeDeployChangeIsInconclusive(t *testing.T) {
	server := existingServer(t, 200)
	calls := 0
	a, _, s, work := existingFixture(t, func() int64 {
		calls++
		if calls > 2 { // queue + start see build 7, the end of the run sees build 8
			return 8
		}
		return 7
	})
	suite := existingSuite(t, a, s, server.URL)
	r, err := a.queue(context.Background(), a.ctx, suite.ID, "tool", "", QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = work(); err != nil {
		t.Fatal(err)
	}
	done, _ := s.run(r.ID)
	if done.Status != "failed" || !strings.Contains(done.Error, "inconclusive") || !done.Release.Inconclusive {
		t.Fatal(done.Status, done.Error)
	}
	spec, _ := a.releaseSpec(context.Background(), a.ctx, suite.ID)
	verdict, _ := s.verdict(suite.ID, QueueOptions{CandidateDigest: digestFor(7), SuiteRevision: spec["suite_revision"].(string), FixtureRevision: spec["fixture_revision"].(string)})
	if verdict.Allowed || verdict.State != "inconclusive" {
		t.Fatal(verdict)
	}
}

func TestExistingModeFailedSetupBlocksDependents(t *testing.T) {
	server := existingServer(t, 500)
	a, _, s, work := existingFixture(t, func() int64 { return 7 })
	suite := existingSuite(t, a, s, server.URL)
	r, err := a.queue(context.Background(), a.ctx, suite.ID, "tool", "", QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = work(); err != nil {
		t.Fatal(err)
	}
	done, _ := s.run(r.ID)
	if done.Status != "failed" || done.Blocked != 2 || done.Failed != 1 || done.Passed != 0 {
		t.Fatal(done.Status, done.Passed, done.Failed, done.Blocked, done.Error, done.Results)
	}
	for _, result := range done.Results[1:] {
		if result.Status != "blocked" || !strings.Contains(result.Error, `"login" did not pass`) {
			t.Fatal(result)
		}
	}
}

func TestExistingModeRequiresCandidateForVerdictsAndMissingCredentials(t *testing.T) {
	server := existingServer(t, 200)
	a, _, s, work := existingFixture(t, func() int64 { return 7 })
	suite, err := s.saveSuite(Suite{Name: "No candidate", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: server.URL + "/api"}}},
		Credentials: map[string]CredentialRef{"other": {Ref: "missing"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.saveCheck(Check{SuiteID: suite.ID, Name: "h", Kind: "http", Enabled: true, Definition: Definition{Target: "api", Path: "/login",
		HeaderRefs: map[string]HeaderRef{"X-Login-Password": {Ref: "credentials.other.password"}}, Assertions: []Assertion{{Path: "/status_code", Op: "exists"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.queue(context.Background(), a.ctx, suite.ID, "tool", "", QueueOptions{CandidateDigest: digestFor(7)}); err == nil || !strings.Contains(err.Error(), "need target.candidate") {
		t.Fatal(err)
	}
	r, err := a.queue(context.Background(), a.ctx, suite.ID, "tool", "", QueueOptions{})
	if err != nil || r.Release != nil {
		t.Fatal(r, err)
	}
	if err = work(); err != nil {
		t.Fatal(err)
	}
	done, _ := s.run(r.ID)
	if done.Status != "failed" || !strings.Contains(done.Error, `credential "missing" has no "password" value`) {
		t.Fatal(done.Status, done.Error)
	}
}

// computerStub simulates Computer's regular browser tools.
type computerStub struct {
	mu       sync.Mutex
	opens    []map[string]any
	uses     []map[string]any
	deleted  []string
	sessions int
	// hashRewrite makes pages add "#/app" to their URL on load, and makes
	// navigate fail like Computer when the URL does not change.
	hashRewrite bool
	current     map[string]string
}

func (c *computerStub) call(tool string, args map[string]any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var reply any
	switch tool {
	case "computer_context_list":
		reply = map[string]any{"contexts": []any{}}
	case "browser_session":
		if args["action"] == "close" {
			reply = map[string]any{"closed": true}
			break
		}
		c.opens = append(c.opens, args)
		c.sessions++
		start, _ := args["url"].(string)
		if start == "" {
			start = "about:blank"
		} else if c.hashRewrite && !strings.Contains(start, "#") {
			start += "#/app"
		}
		id := fmt.Sprintf("br_%d", c.sessions)
		if c.current == nil {
			c.current = map[string]string{}
		}
		c.current[id] = start
		r := map[string]any{"session_id": id, "current_url": start}
		if args["auto_create_context"] == true {
			r["app_context_id"] = "ctx_signin"
		} else if args["context_id"] != nil {
			r["app_context_id"] = args["context_id"]
		}
		reply = r
	case "computer_use":
		c.uses = append(c.uses, args)
		r := map[string]any{"success": true}
		switch args["action"] {
		case "navigate":
			id, target := args["session_id"].(string), args["url"].(string)
			before, after := c.current[id], target
			if c.hashRewrite && !strings.Contains(after, "#") {
				after += "#/app"
			}
			if before == after && target != after {
				return fmt.Errorf("navigation_ineffective: browser remained at %q; session_id=%s; action=navigate", after, id)
			}
			c.current[id] = after
			r["current_url"] = after
		case "wait_for":
			r["matched"] = true
		case "set_text":
			r["text_value"] = args["text"]
		}
		reply = r
	case "computer_context_delete":
		c.deleted = append(c.deleted, args["id"].(string))
		reply = map[string]any{"deleted": true}
	case "browser_screenshot":
		reply = map[string]any{"png_b64": ""}
	default:
		return fmt.Errorf("unexpected computer tool %s", tool)
	}
	raw, _ := json.Marshal(reply)
	return json.Unmarshal(raw, out)
}

func TestExistingModeBrowserSignsInOnce(t *testing.T) {
	computer := &computerStub{}
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app == "computer" {
			return computer.call(tool, args, out)
		}
		return fmt.Errorf("unexpected app %s", app)
	}}
	credentials := `{"project-a":{"operator":{"email":"ada@example.com","password":"` + testPassword + `"}}}`
	a, app, s := fixture(t, tk.WithPlatform(platform), tk.WithConfig(map[string]string{"credentials": credentials}))
	suite, err := s.saveSuite(Suite{Name: "Staging UI", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"frontend": {BaseURL: "https://staging.example.com/ui"}}},
		Credentials: map[string]CredentialRef{"operator": {Ref: "operator"}}, Session: &SessionConfig{Kind: "browser", SetupCheck: "login"}})
	if err != nil {
		t.Fatal(err)
	}
	step := func(id string, b BrowserAction, assertions ...Assertion) Step {
		return Step{ID: id, Kind: "browser", Browser: &b, Assertions: assertions}
	}
	login := Check{SuiteID: suite.ID, Name: "login", Kind: "browser", Enabled: true, Definition: Definition{Steps: []Step{
		step("open", BrowserAction{Action: "navigate", Target: "frontend", Path: "/login"}),
		step("email", BrowserAction{Action: "fill", Locator: BrowserLocator{TestID: "email"}, ValueRef: "credentials.operator.email"}),
		step("password", BrowserAction{Action: "fill", Locator: BrowserLocator{TestID: "password"}, ValueRef: "credentials.operator.password"}),
		step("submit", BrowserAction{Action: "click", Locator: BrowserLocator{Selector: "button[type=submit]"}}),
	}}}
	var screens []Check
	for i := 0; i < 3; i++ {
		screens = append(screens, Check{SuiteID: suite.ID, Name: fmt.Sprintf("screen %d", i), Kind: "browser", Enabled: true, Definition: Definition{Steps: []Step{
			step("open", BrowserAction{Action: "navigate", Target: "frontend", Path: fmt.Sprintf("/screen/%d", i)}),
			step("heading", BrowserAction{Action: "observe", Locator: BrowserLocator{Selector: "h1"}}, Assertion{Path: "/visible", Op: "equals", Value: true}, Assertion{Path: "/path", Op: "equals", Value: fmt.Sprintf("/ui/screen/%d", i)}),
		}}})
	}
	for _, check := range append(screens, login) {
		if _, err = s.saveCheck(check); err != nil {
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
	done, _ := s.run(r.ID)
	if done.Status != "passed" || done.Passed != 4 {
		t.Fatal(done.Status, done.Error, done.Results)
	}
	if len(computer.opens) != 4 || computer.opens[0]["auto_create_context"] != true {
		t.Fatal(computer.opens)
	}
	for _, open := range computer.opens[1:] {
		if open["context_id"] != "ctx_signin" || open["auto_create_context"] != nil {
			t.Fatal("later checks did not reuse the sign-in", open)
		}
	}
	if len(computer.deleted) != 1 || computer.deleted[0] != "ctx_signin" {
		t.Fatal(computer.deleted)
	}
	typed := false
	for _, use := range computer.uses {
		if use["action"] == "set_text" && use["text"] == testPassword && use["selector"] == `[data-testid="password"]` {
			typed = true
		}
	}
	if !typed {
		t.Fatal("password was not entered")
	}
	if text := storedText(t, s); strings.Contains(text, testPassword) {
		t.Fatal("password stored")
	}
}

func TestExistingModeNavigatesHashRewritingApp(t *testing.T) {
	computer := &computerStub{hashRewrite: true}
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app == "computer" {
			return computer.call(tool, args, out)
		}
		return fmt.Errorf("unexpected app %s", app)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	suite, err := s.saveSuite(Suite{Name: "SPA", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"frontend": {BaseURL: "https://crm.example.com"}}}})
	if err != nil {
		t.Fatal(err)
	}
	nav := BrowserAction{Action: "navigate", Target: "frontend", Path: "/?screen=admin_dashboard"}
	other := BrowserAction{Action: "navigate", Target: "frontend", Path: "/?screen=calls"}
	if _, err = s.saveCheck(Check{SuiteID: suite.ID, Name: "dashboard", Kind: "browser", Enabled: true, Definition: Definition{Steps: []Step{
		{ID: "open", Kind: "browser", Browser: &nav},
		// Navigating again to the page already shown is accepted too.
		{ID: "again", Kind: "browser", Browser: &nav},
		{ID: "calls", Kind: "browser", Browser: &other, Assertions: []Assertion{{Path: "/path", Op: "equals", Value: "/"}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	r, err := a.queue(context.Background(), app, suite.ID, "tool", "", QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.work(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	done, _ := s.run(r.ID)
	if done.Status != "passed" {
		t.Fatal(done.Status, done.Error, done.Results)
	}
	if _, ok := computer.opens[0]["url"]; ok {
		t.Fatal("session opened on the destination instead of about:blank", computer.opens[0])
	}
}

func TestSameIgnoringFragment(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"https://x.com/?s=a", "https://x.com/?s=a#/a", true},
		{"https://X.com", "https://x.com/#top", true},
		{"https://x.com/?s=a", "https://x.com/?s=b#/a", false},
		{"https://x.com/login", "https://x.com/?s=a", false},
		{"about:blank", "about:blank", false},
	} {
		if got := sameIgnoringFragment(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %v", c.a, c.b, got)
		}
	}
}

func TestIsolatedTargetPinsGeneratedEnvironment(t *testing.T) {
	var mu sync.Mutex
	version, digest := 1, strings.Repeat("d", 64)
	var created, runArgs map[string]any
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		mu.Lock()
		defer mu.Unlock()
		var reply any
		switch app + "/" + tool {
		case "environments/environment_catalog":
			reply = map[string]any{"apps": []any{map[string]any{"install_id": 11, "name": "api"}, map[string]any{"install_id": 12, "name": "deploy"}}}
		case "environments/environment_create", "environments/environment_update":
			created = args
			reply = map[string]any{"id": "env_gen", "spec_version": version, "spec_digest": digest, "spec": args["spec"]}
		case "environments/environment_get":
			reply = map[string]any{"id": "env_gen", "spec_version": version, "spec_digest": digest, "spec": created["spec"]}
		case "environments/environment_run_create":
			runArgs = args
			return errors.New("runtime unavailable in this test")
		case "environments/environment_run_stop":
			reply = map[string]any{"ok": true, "runtime_state": "stopped"}
		case "deploy/deploy_status", "deploy/deploy_candidate_digest":
			return (&deployStub{live: func() int64 { return 9 }}).call(tool, args, out)
		default:
			return fmt.Errorf("unexpected %s/%s", app, tool)
		}
		raw, _ := json.Marshal(reply)
		return json.Unmarshal(raw, out)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	saved, err := a.dispatch(context.Background(), app, "tests_suite_save", map[string]any{
		"name": "Isolated API",
		"target": map[string]any{"mode": "isolated", "candidate": map[string]any{"deployment_id": 4},
			"targets":     map[string]any{"api": map[string]any{"app": "api", "path": "/gw/items"}},
			"environment": map[string]any{"apps": []any{"api", "deploy"}, "ttl_seconds": 600, "pinned": map[string]any{"environment_id": "forged", "spec_version": 99, "spec_digest": strings.Repeat("f", 64)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := saved.(map[string]any)["suite"].(Suite)
	pinned := suite.Target.Environment.Pinned
	if pinned == nil || pinned.EnvironmentID != "env_gen" || pinned.SpecDigest != digest {
		t.Fatal("pin not recorded", pinned)
	}
	spec := created["spec"].(map[string]any)
	if spec["network_mode"] != "block" || spec["integration_mode"] != "mock" || fmt.Sprint(spec["app_install_ids"]) != "[11 12]" {
		t.Fatal(spec)
	}
	if _, err = s.saveCheck(Check{SuiteID: suite.ID, Name: "list", Kind: "scenario", Enabled: true, Definition: Definition{Steps: []Step{
		{ID: "list", Kind: "http", Target: "api", Path: "/items", Assertions: []Assertion{{Path: "/status_code", Op: "equals", Value: float64(200)}}}}}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	version, digest = 2, strings.Repeat("e", 64)
	mu.Unlock()
	if _, err = a.queue(context.Background(), app, suite.ID, "tool", "", QueueOptions{}); err == nil || !strings.Contains(err.Error(), "pinned v1/dddddddddddd, found v2/eeeeeeeeeeee") {
		t.Fatal(err)
	}
	mu.Lock()
	version, digest = 1, strings.Repeat("d", 64)
	mu.Unlock()
	r, err := a.queue(context.Background(), app, suite.ID, "tool", "", QueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Release.Mode != "isolated" || r.Release.CandidateDigest != digestFor(9) {
		t.Fatal(r.Release)
	}
	snapshot, _ := s.getReleaseSnapshot(r.ID)
	if !snapshot.Config.Generated || snapshot.Config.IdentityProbe.Tool != "deploy_candidate_digest" {
		t.Fatal(snapshot.Config)
	}
	if err = a.work(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if runArgs["environment_id"] != "env_gen" || runArgs["spec"] != nil || runArgs["spec_digest"] != strings.Repeat("d", 64) {
		t.Fatal(runArgs)
	}
	vars := runArgs["seed_vars"].(map[string]any)
	if vars["candidate_digest"] != digestFor(9) || vars["candidate_build_id"] != "9" {
		t.Fatal(vars)
	}
}

func TestRunSelectedChecksAddsSetupCheck(t *testing.T) {
	computer := &computerStub{}
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app == "computer" {
			return computer.call(tool, args, out)
		}
		return fmt.Errorf("unexpected app %s", app)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	suite, err := s.saveSuite(Suite{Name: "UI", Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"frontend": {BaseURL: "https://crm.example.com"}}},
		Session: &SessionConfig{Kind: "browser", SetupCheck: "login"}})
	if err != nil {
		t.Fatal(err)
	}
	browserCheck := func(name string, enabled bool) Check {
		nav := BrowserAction{Action: "navigate", Target: "frontend", Path: "/" + strings.ReplaceAll(name, " ", "-")}
		return Check{SuiteID: suite.ID, Name: name, Kind: "browser", Enabled: enabled, Definition: Definition{Steps: []Step{{ID: "open", Kind: "browser", Browser: &nav}}}}
	}
	for _, check := range []Check{browserCheck("login", true), browserCheck("screen quality", false), browserCheck("other", true)} {
		if _, err = s.saveCheck(check); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.dispatch(context.Background(), app, "tests_run", map[string]any{"suite_id": suite.ID, "check_names": []any{"nope"}}); err == nil || !strings.Contains(err.Error(), `no check named "nope"`) {
		t.Fatal(err)
	}
	out, err := a.dispatch(context.Background(), app, "tests_run", map[string]any{"suite_id": suite.ID, "check_names": []any{"screen quality"}})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)["run"].(Run)
	if err = a.work(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	done, _ := s.run(r.ID)
	var names []string
	for _, result := range done.Results {
		names = append(names, result.Name)
	}
	if done.Status != "passed" || fmt.Sprint(names) != "[login screen quality]" {
		t.Fatal(done.Status, done.Error, names)
	}
	checks, _ := s.checks(suite.ID)
	for _, check := range checks {
		if check.Enabled != (check.Name != "screen quality") {
			t.Fatal("enabled flags changed", check.Name, check.Enabled)
		}
	}
}

func TestPartialRunNeverYieldsVerdict(t *testing.T) {
	_, _, s := fixture(t)
	suite, err := s.saveSuite(Suite{Name: "UI"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.queueRelease(suite.ID, "tool", "", QueueOptions{CheckNames: []string{"a"}, SuiteRevision: digestFor(2)}, queuePrep{}); err == nil || !strings.Contains(err.Error(), "never produce a release verdict") {
		t.Fatal(err)
	}
	for _, partial := range []bool{true, false} {
		record := ReleaseRecord{Mode: "existing", Partial: partial, CandidateDigest: digestFor(1), ObservedDigest: digestFor(1), SuiteRevision: digestFor(2), FixtureRevision: digestFor(3)}
		if _, err = s.db.Exec(`INSERT INTO test_runs(project_id,suite_id,suite_name,environment,status,trigger,snapshot,created_at,release_record,fixture_spec) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			s.project, suite.ID, "UI", "", "passed", "tool", "[]", now(), encode(&record), "{}"); err != nil {
			t.Fatal(err)
		}
		v, err := s.verdict(suite.ID, QueueOptions{CandidateDigest: digestFor(1), SuiteRevision: digestFor(2), FixtureRevision: digestFor(3)})
		if err != nil {
			t.Fatal(err)
		}
		if partial && v.State != "missing" || !partial && !v.Allowed {
			t.Fatal(partial, v)
		}
	}
}

func TestCandidateErrorsNameDeployAndItsReason(t *testing.T) {
	unbound := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		return errors.New("app deploy is not bound to this install")
	}}
	a, app, _ := fixture(t, tk.WithPlatform(unbound))
	suite := map[string]any{"name": "Live", "target": map[string]any{"mode": "existing", "candidate": map[string]any{"deployment_id": 1},
		"targets": map[string]any{"web": map[string]any{"base_url": "https://crm.example.com"}}}}
	if _, err := a.dispatch(context.Background(), app, "tests_suite_save", suite); err == nil ||
		!strings.Contains(err.Error(), "Bind Deploy to Tests") || !strings.Contains(err.Error(), "not bound to this install") {
		t.Fatal(err)
	}

	refusing := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if tool == "deploy_candidate_digest" {
			return errors.New("release artifact differs from attested build")
		}
		return (&deployStub{live: func() int64 { return 34 }}).call(tool, args, out)
	}}
	a, app, _ = fixture(t, tk.WithPlatform(refusing))
	if _, err := resolveCandidate(context.Background(), app, &CandidateRef{DeploymentID: 1, Build: "live"}); err == nil ||
		!strings.Contains(err.Error(), "Deploy could not verify build 34: release artifact differs from attested build") {
		t.Fatal(err)
	}
	_ = a
}
