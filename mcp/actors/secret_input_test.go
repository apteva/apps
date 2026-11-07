package main

import (
	"context"
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"testing"
	"time"
)

func TestBrowserSecretProjectContextHostExpiry(t *testing.T) {
	now := time.Now().UTC()
	entry := actorBrowserSecret{Value: "private-value", ContextID: "qa-context", Host: "www.example.com", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	raw, _ := json.Marshal(map[string]map[string]actorBrowserSecret{"p": {"ref": entry}})
	value, err := resolveActorBrowserSecret(string(raw), "p", "ref", "qa-context", "https://www.example.com/signup", now)
	if err != nil || value != entry.Value {
		t.Fatal("valid secret could not resolve", err)
	}
	for _, tc := range []struct {
		project, ref, ctx, url string
		at                     time.Time
	}{
		{"other", "ref", "qa-context", "https://www.example.com/", now}, {"p", "missing", "qa-context", "https://www.example.com/", now}, {"p", "ref", "brand-context", "https://www.example.com/", now}, {"p", "ref", "qa-context", "https://evil.example/", now}, {"p", "ref", "qa-context", "http://www.example.com/", now}, {"p", "ref", "qa-context", "https://www.example.com:443/", now}, {"p", "ref", "qa-context", "https://user@www.example.com/", now}, {"p", "ref", "qa-context", "https://www.example.com/", now.Add(2 * time.Hour)},
	} {
		v, e := resolveActorBrowserSecret(string(raw), tc.project, tc.ref, tc.ctx, tc.url, tc.at)
		if e == nil || v != "" || strings.Contains(e.Error(), entry.Value) {
			t.Fatalf("unsafe reference accepted or leaked: %+v", tc)
		}
	}
}

func TestSecretFillKeepsReferenceAndSuppressesArtifacts(t *testing.T) {
	raw, _ := json.Marshal(map[string]map[string]actorBrowserSecret{"p": {"ref": {Value: "not-in-history", ContextID: "qa-context", Host: "example.com", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}})
	p := newFakePlatform()
	ctx, app := newTestCtx(t, p, tk.WithConfig(map[string]string{"browser_secrets": string(raw)}))
	ctx = ctx.WithProject("p")
	e := &actorExecution{ctx: ctx, app: app, workerCtx: context.Background(), definition: actorDefinition{AllowedHosts: []string{"example.com"}, Browser: actorBrowser{ContextID: "qa-context"}}, session: &browserSession{SessionID: "s", AppContextID: "qa-context"}, currentURL: "https://example.com/"}
	step := actorStep{Action: "fill_secret", SecretRef: "ref", Locator: actorLocator{Selector: `input[type="password"]`}}
	if err := e.fillSecret(step); err != nil {
		t.Fatal(err)
	}
	if step.Text != "" || step.SecretRef != "ref" || !e.secretInputUsed {
		t.Fatal("secret mutated persisted step")
	}
	found := false
	for _, call := range p.callsSnapshot() {
		if call.args["action"] == "set_text" && call.args["text"] == "not-in-history" {
			found = true
		}
	}
	if !found {
		t.Fatal("secret not dispatched to field")
	}
	count := len(p.callsSnapshot())
	if _, err := e.storeCurrentScreenshot("after-secret"); err == nil || len(p.callsSnapshot()) != count {
		t.Fatal("secret-bearing screenshot retained")
	}
	p.failAction = "set_text"
	err := e.fillSecret(step)
	if err == nil || strings.Contains(err.Error(), "not-in-history") || !strings.Contains(err.Error(), "suppressed") {
		t.Fatal("sensitive error not suppressed", err)
	}
	e.session.AppContextID = "wrong"
	count = len(p.callsSnapshot())
	if e.fillSecret(step) == nil || len(p.callsSnapshot()) != count {
		t.Fatal("secret crossed session context")
	}
}

func TestSecretFillValidation(t *testing.T) {
	d := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "fill_secret", SecretRef: "{{password_ref}}", Locator: actorLocator{Selector: `input[type="password"]`}}}}
	if err := validateActorDefinition(d); err != nil {
		t.Fatal(err)
	}
	for _, s := range []actorStep{{Action: "fill_secret", SecretRef: "ref"}, {Action: "fill_secret", SecretRef: "ref", Text: "literal", Locator: actorLocator{Selector: "input"}}, {Action: "fill", SecretRef: "ref", Locator: actorLocator{Selector: "input"}}} {
		d.Steps = []actorStep{s}
		if validateActorDefinition(d) == nil {
			t.Fatal("invalid secret step accepted")
		}
	}
}

func TestSecretNavigationNeverPersistsBearerURL(t *testing.T) {
	raw, _ := json.Marshal(map[string]map[string]actorBrowserSecret{"p": {"link": {Value: "https://example.com/verify?token=private-token#private-fragment", ContextID: "qa-context", Host: "example.com", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}})
	p := newFakePlatform()
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool == "computer.computer_use" && in["action"] == "navigate" {
			return map[string]any{"current_url": in["url"]}
		}
		return nil
	}
	ctx, app := newTestCtx(t, p, tk.WithConfig(map[string]string{"browser_secrets": string(raw)}))
	ctx = ctx.WithProject("p")
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	start, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := start.(map[string]any)["run_id"].(int64)
	if _, err = ctx.AppDB().Exec(`UPDATE actors_runs SET status='running' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	e := &actorExecution{ctx: ctx, app: app, run: &actorQueuedRun{ID: id}, workerCtx: context.Background(), definition: actorDefinition{AllowedHosts: []string{"example.com"}, Browser: actorBrowser{ContextID: "qa-context"}}, session: &browserSession{SessionID: "s", AppContextID: "qa-context"}, currentURL: "https://example.com/"}
	if err = e.gotoSecret(actorStep{Action: "goto_secret", SecretRef: "link"}); err != nil {
		t.Fatal(err)
	}
	if e.currentURL != "https://example.com" || !e.secretInputUsed || e.secretNavigation {
		t.Fatal("bearer URL not stripped after navigation")
	}
	var output string
	if err = ctx.AppDB().QueryRow(`SELECT output_json FROM actors_runs WHERE id=?`, id).Scan(&output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "private-token") || strings.Contains(output, "private-fragment") {
		t.Fatal("secret URL persisted in progress")
	}
	if got := withoutSecretURLParameters("https://example.com/path?a=secret#b"); got != "https://example.com" {
		t.Fatal(got)
	}
}
