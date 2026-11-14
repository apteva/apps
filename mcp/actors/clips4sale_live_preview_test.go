package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestClips4SaleLivePreviewGuards(t *testing.T) {
	data, err := os.ReadFile("examples/clips4sale-real-performer-control.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	def, _, err := decodeActorDefinition(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"thumbnail", "preview_video"} {
		t.Run(asset, func(t *testing.T) {
			preflight := def.Operations["preflight_live_clip_"+asset]
			if !preflight.ReadOnly {
				t.Fatal("live preflight must be read-only")
			}
			edit := def.Operations["set_live_clip_"+asset]
			commitCount := 0
			statusVerified := false
			for i, step := range edit.Steps {
				if step.Action == "upload_file" && (!step.RequirePrepared || step.ExpectedSHA256 != "{{source_sha256}}") {
					t.Fatal("live upload must consume the SHA-verified prepared file")
				}
				if step.ExpectedEffect == "immediate_external_commit" {
					commitCount++
					switch step.ExpectedText {
					case "Save and Exit":
						if step.Optional || commitCount != 1 {
							t.Fatal("Save and Exit must be the first, required commit")
						}
					case "Publish changes":
						if !step.Optional || commitCount != 2 ||
							edit.Steps[i-1].Action != "wait_for" ||
							!strings.Contains(edit.Steps[i-1].Conditions[0].Selector, "republish-changes-dialog-republish") {
							t.Fatal("expected only the guarded live version confirmation")
						}
					default:
						t.Fatalf("unexpected live commit %q", step.ExpectedText)
					}
					if step.OnceKey == "" || step.OnceScope != "context" || !step.Locator.SOMOnly || !step.Locator.Exact {
						t.Fatal("live commits require exact controls and durable request keys")
					}
				}
				if step.Assertions["after_display_status"].Matches != "" {
					statusVerified = true
					for label, allowed := range map[string]bool{
						"ACTIVE Up to date": true, "ACTIVE Edits Queued for Review": true, "ACTIVE Edits in review": true,
						"ACTIVE Changes in progress": false, "NOT ACTIVE Queued for review": false,
					} {
						execution := &actorExecution{lastValues: map[string]any{"after_display_status": label}}
						proof := actorStep{Assertions: map[string]actorAssertion{"after_display_status": step.Assertions["after_display_status"]}}
						if err := execution.assertValues(proof); (err == nil) != allowed {
							t.Fatalf("unexpected result for %q: %v", label, err)
						}
					}
				}
			}
			if commitCount != 2 || !statusVerified || edit.Steps[0].Assertions["live_edit_authorized"].EqualsExact != true {
				t.Fatal("live workflow is missing an authorization, commit, or status check")
			}
		})
	}
}
