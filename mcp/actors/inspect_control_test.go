package main

import (
	"context"
	"testing"
)

func TestReadOnlyConfigurationInspectionRejectsSelectionAndCommit(t *testing.T) {
	for _, effect := range []string{"open_configuration", "persistent_mutation", "immediate_external_commit", "financial_action", "", "navigation_only"} {
		for _, role := range []string{"button", "menuitemcheckbox"} {
			t.Run(effect+"/"+role, func(t *testing.T) {
				p := newFakePlatform()
				clicks := 0
				p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
					if tool == "computer.computer_use" {
						if in["action"] == "screenshot" {
							return map[string]any{"som_revision": 1, "som": []any{map[string]any{"id": "target", "tag": role, "role": role, "text": "Details", "accessible_name": "Details", "effect": effect}}}
						}
						if in["action"] == "click" {
							clicks++
						}
						return map[string]any{"current_url": "https://example.com"}
					}
					return nil
				}
				ctx, a := newTestCtx(t, p)
				e := actorExecution{app: a, ctx: ctx, workerCtx: context.Background(), session: &browserSession{SessionID: "fixture"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
				err := e.inspectControl(actorStep{Locator: actorLocator{Text: "Details", Exact: true, SOMOnly: true}})
				expected := effect == "open_configuration" && role == "button"
				if (err == nil) != expected || (clicks == 1) != expected {
					t.Fatal(effect, role, clicks, err)
				}
			})
		}
	}
}
