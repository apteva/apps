package main

import (
	"context"
	"testing"
)

func TestActorConsumesAndVerifiesProxyEndpoint(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		platform := newFakePlatform()
		if wrong {
			platform.proxyEndpointOverride = "pe_wrong"
		}
		ctx, app := newTestCtx(t, platform)
		definition := map[string]any{"schema_version": 1, "browser": map[string]any{"backend": "local", "proxy_mode": "auto", "proxy_endpoint": "pe_selected"}, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"max_duration_seconds": 60, "step_retries": 0}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/qa"}}, "output_schema": map[string]any{}}
		created, err := app.toolActorSave(ctx, map[string]any{"name": "Endpoint QA", "definition": definition})
		if err != nil {
			t.Fatal(err)
		}
		id := created.(map[string]any)["actor"].(*actorRecord).ID
		queuedResult, err := app.toolActorRun(ctx, map[string]any{"actor_id": id})
		if err != nil {
			t.Fatal(err)
		}
		queued, err := claimActorRun(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = app.executeActorRun(context.Background(), ctx, queued)
		run, err := getActorRun(ctx, queuedResult.(map[string]any)["run_id"].(int64))
		if err != nil {
			t.Fatal(err)
		}
		if (run["status"] == "completed") == wrong {
			t.Fatalf("wrong=%v run=%#v", wrong, run)
		}
		open := platform.lastCall("computer", "browser_open")
		if open["proxy_endpoint"] != "pe_selected" {
			t.Fatal("endpoint selector not forwarded")
		}
	}
}
func TestActorRejectsIncompatibleProxyEndpointSelectors(t *testing.T) {
	def := actorDefinition{SchemaVersion: 1, Browser: actorBrowser{Backend: "local", ProxyEndpoint: "pe_selected"}, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "goto", URL: "https://example.com"}}}
	if err := validateActorDefinition(def); err != nil {
		t.Fatal(err)
	}
	for _, browser := range []actorBrowser{{Backend: "browserbase", ProxyEndpoint: "pe_selected"}, {Backend: "local", ProxyEndpoint: "pe_selected", ProxyProfile: "other"}, {Backend: "local", ProxyEndpoint: "pe_selected", ProxyMode: "direct"}} {
		def.Browser = browser
		if err := validateActorDefinition(def); err == nil {
			t.Fatal("incompatible proxy route accepted")
		}
	}
}
