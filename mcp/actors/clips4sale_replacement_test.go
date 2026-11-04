package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPrepareUploadRequiresBackendAndByteProof(t *testing.T) {
	for _, mode := range []string{"ready", "wrong_backend", "not_prepared", "hash_missing"} {
		t.Run(mode, func(t *testing.T) {
			p := newFakePlatform()
			p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool == "computer.computer_use" && args["action"] == "prepare_upload" {
					out := map[string]any{"prepared": true, "uploaded": false, "backend": "browserbase", "sha256": strings.Repeat("a", 64), "size_bytes": 22}
					if mode == "wrong_backend" {
						out["backend"] = "local"
					}
					if mode == "not_prepared" {
						out["prepared"] = false
					}
					if mode == "hash_missing" {
						delete(out, "sha256")
					}
					return out
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			e := &actorExecution{app: app, ctx: ctx, session: &browserSession{SessionID: "fixture", Backend: "browserbase"}, workerCtx: context.Background()}
			err := e.prepareUpload(actorStep{SourceURL: "https://example.com/private.mp4?token=secret", Filename: "test.mp4"})
			if (err == nil) != (mode == "ready") {
				t.Fatal(mode, err)
			}
			if strings.Contains(stringFromAny(e.trace), "secret") {
				t.Fatal("signed source leaked")
			}
		})
	}
}

func TestRevealRequiresFullControlBeforeSave(t *testing.T) {
	p := newFakePlatform()
	scrolled := false
	p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
		if tool != "computer.computer_use" {
			return nil
		}
		if args["action"] == "scroll" {
			scrolled = true
			return map[string]any{"scroll": map[string]any{"actual_target_id": "doc"}}
		}
		if args["action"] == "screenshot" {
			y := 380
			top := 0
			if scrolled {
				y = 120
				top = 260
			}
			return map[string]any{"current_url": "https://example.com", "width": 800, "height": 400, "som_revision": 1, "som": []any{map[string]any{"id": "save", "text": "Save Documents", "role": "button", "x": 20, "y": y, "w": 100, "h": 40}}, "scroll_regions": []any{map[string]any{"id": "doc", "name": "Document", "role": "document", "h": 400, "scroll_top": top, "max_scroll_y": 800}}}
		}
		return nil
	}
	ctx, app := newTestCtx(t, p)
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []string{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}}})
	app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	run, _ := claimActorRun(ctx)
	e := &actorExecution{app: app, ctx: ctx, run: run, workerCtx: context.Background(), deadline: time.Now().Add(time.Minute), currentURL: "https://example.com", session: &browserSession{SessionID: "fixture"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
	if err := e.revealControl(actorStep{Action: "reveal_control", Locator: actorLocator{Text: "Save Documents", Exact: true, SOMOnly: true}, Reveal: &actorReveal{ScrollTargetName: "Document", Direction: "down", MaxScrolls: 4, FullyVisible: true}}); err != nil {
		t.Fatal(err)
	}
	if !scrolled {
		t.Fatal("partly clipped Save accepted")
	}
}

func TestReplacementDefinitionRequiresStagingPersistenceAndNoPublication(t *testing.T) {
	d := realPerformerDefinition(t)
	for _, name := range []string{"replace_clip_video"} {
		op := d.Operations[name]
		prepared, uploaded, saved, reloaded := false, false, false, false
		for _, s := range op.Steps {
			if s.Action == "prepare_upload" {
				prepared = true
				if s.ExpectedSHA256 != "{{source_sha256}}" {
					t.Fatal("unverified replacement source")
				}
			}
			if s.ExpectedText == "Replace File" && (!prepared || s.OnceKey == "" || s.ConfirmConsequence == "") {
				t.Fatal("replacement before source proof/guard")
			}
			if s.Action == "upload_file" {
				uploaded = true
				if !prepared || !s.RequirePrepared {
					t.Fatal("refetch after replacement permitted")
				}
			}
			if s.ExpectedText == "Save and Exit" {
				saved = true
				if !uploaded || s.OnceKey == "" {
					t.Fatal("save missing upload/guard")
				}
			}
			if saved && s.Action == "goto" {
				reloaded = true
			}
			if s.ExpectedText == "Publish" || s.ExpectedEffect == "message_send" {
				t.Fatal("replacement publishes or sends")
			}
		}
		if !prepared || !uploaded || !saved || !reloaded {
			t.Fatal("incomplete replacement", name)
		}
		final := op.Steps[len(op.Steps)-1].Assertions
		for _, field := range []string{"saved_filename", "performer_names", "performer_cards", "current_url", "title", "description", "store_name"} {
			if _, ok := final[field]; !ok {
				t.Fatal("missing persistence evidence", field)
			}
		}
	}
	// A stale old filename must not be allowed to satisfy the new media assertion.
	e := &actorExecution{lastValues: map[string]any{"filename": "new_video_261104115709.mp4"}}
	if err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{"filename": {NotMatches: `^new_video(?:_[0-9]{12})?\.mp4$`}}}); err == nil {
		t.Fatal("stale old media accepted")
	}
	for _, name := range []string{"upload_performer_document", "upload_performer_document_base64"} {
		stages, reveals, saves := 0, 0, 0
		for _, s := range d.Operations[name].Steps {
			if s.Action == "prepare_upload" {
				stages++
			}
			if s.Action == "reveal_control" && s.Reveal.FullyVisible {
				reveals++
			}
			if s.ExpectedText == "Save Documents" {
				saves++
				if s.OnceKey == "" || stages != 1 || reveals != 1 {
					t.Fatal("unsafe document save")
				}
			}
		}
		if saves != 1 {
			t.Fatal("save retry or missing save")
		}
	}
	for _, s := range d.Operations["verify_performer_document_saved"].Steps {
		if !readOnlyActorAction(s.Action) {
			t.Fatal("reconciliation writes", s.Action)
		}
	}
}

func TestReplacementExecutionNeedsPersistedSameClipAndPerformers(t *testing.T) {
	for _, mode := range []string{"success", "source_rejected", "not_persisted", "performer_lost", "queued"} {
		t.Run(mode, func(t *testing.T) {
			d := realPerformerDefinition(t)
			p := newFakePlatform()
			current := ""
			saved := false
			replacements, uploads, saves := 0, 0, 0
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_session":
					return map[string]any{"session_id": "fixture", "backend": "browserbase", "effective_backend": "browserbase", "app_context_id": "fixture-c4s", "current_url": current}
				case "computer.browser_extract":
					body := realAccountDOM()
					if strings.Contains(current, "/content") {
						if strings.Contains(current, "step=1") {
							body = c4sDetailsDOM("")
							body = strings.Replace(body, `<input name="controlled-radio-buttons-group" checked value="2">`, `<div data-testid="edit-clip_radio-group_performers"><span class="Mui-checked"><input name="controlled-radio-buttons-group" checked value="0"></span></div>`, 1)
							cards := `<div data-testid="edit-clip_featured-performers_list"><div data-testid="featured-performers_row_0"><span data-testid="featured-performers_row_0_span_stage-name">P1</span></div><div data-testid="featured-performers_row_1"><span data-testid="featured-performers_row_1_span_stage-name">P2</span></div></div>`
							if saved && mode == "performer_lost" {
								cards = strings.ReplaceAll(cards, "P2", "Other")
							}
							body = strings.Replace(body, "</body>", cards+"</body>", 1)
							if saved && mode != "not_persisted" {
								body = strings.ReplaceAll(body, "approved.mp4", "new_video_261104115709.mp4")
							}
						} else {
							status := "draft"
							if mode == "queued" {
								status = "under_review"
							}
							body = c4sLibraryDOM(status)
						}
					}
					return map[string]any{"html": body, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "wait_for":
						return map[string]any{"matched": true, "current_url": current}
					case "prepare_upload":
						return map[string]any{"prepared": mode != "source_rejected", "uploaded": false, "backend": "browserbase", "sha256": strings.Repeat("a", 64), "size_bytes": 22}
					case "screenshot":
						targets := []any{}
						for i, name := range []string{"Replace video", "Replace File", "Upload Files", "Save and Exit"} {
							targets = append(targets, map[string]any{"id": name, "text": name, "accessible_name": name, "role": "button", "x": 10, "y": 30 + i*50, "w": 200, "h": 40})
						}
						return map[string]any{"current_url": current, "width": 1600, "height": 1000, "som_revision": 1, "som": targets}
					case "click":
						switch in["target_id"] {
						case "Replace File":
							replacements++
						case "Save and Exit":
							saves++
							saved = true
							current = "https://workspace.clips4sale.com/content"
						case "Replace video":
						default:
							t.Fatal("unexpected click", in)
						}
						return map[string]any{"current_url": current, "success": true}
					case "upload_file":
						uploads++
						if in["require_prepared"] != true {
							t.Fatal("missing staged-file requirement")
						}
						return map[string]any{"current_url": current, "uploaded": true}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			raw, _ := json.Marshal(d)
			var definition map[string]any
			json.Unmarshal(raw, &definition)
			rec := saveFixtureActor(t, ctx, app, definition)
			inputs := c4sInputs()
			inputs["expected_clip_store_name"] = "Test Store"
			inputs["expected_performer_names"] = []string{"P1", "P2"}
			inputs["expected_performer_cards"] = []string{"P1", "P2"}
			inputs["filename_stem"] = "new_video"
			inputs["filename"] = "new_video.mp4"
			inputs["source_sha256"] = strings.Repeat("a", 64)
			inputs["replacement_authorized"] = true
			_, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "replace_clip_video", "input": inputs})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			result, _ := getActorRun(ctx, run.ID)
			if (result["status"] == "completed") != (mode == "success") {
				t.Fatalf("status=%v error=%v replacement=%d", result["status"], result["error"], replacements)
			}
			if mode == "source_rejected" || mode == "queued" {
				if replacements+uploads+saves != 0 {
					t.Fatal("clip mutated before successful preflight")
				}
			} else if replacements != 1 || uploads != 1 || saves != 1 {
				t.Fatal("duplicate or missing write", replacements, uploads, saves)
			}
		})
	}
}

func TestDocumentUploadNeedsClearedStagingAndNativeReceiptAfterReload(t *testing.T) {
	for _, mode := range []string{"success", "save_noop", "still_staged", "not_persisted"} {
		t.Run(mode, func(t *testing.T) {
			d := realPerformerDefinition(t)
			p := newFakePlatform()
			current := ""
			uploaded, saved := false, false
			profileLoads, saves := 0, 0
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_session":
					return map[string]any{"session_id": "fixture", "backend": "browserbase", "effective_backend": "browserbase", "app_context_id": "fixture-c4s", "current_url": current}
				case "computer.browser_extract":
					body := realAccountDOM()
					if strings.HasSuffix(current, "/performers") {
						body = realPerformerListDOM("")
					}
					if strings.HasSuffix(current, "/performers/101") {
						row := func(id, name string) string {
							return `<tr><td aria-label="id: ` + id + `">` + id + `</td><td aria-label="filename: ` + name + `">` + name + `</td><td aria-label="type: id">id</td><td aria-label="status: tbd">tbd</td></tr>`
						}
						rows := row("1", "old_id.jpeg")
						count := "1"
						persisted := saved && mode != "save_noop" && !(mode == "not_persisted" && profileLoads > 1)
						if persisted {
							rows += row("902", "photo_id_1793809082_fixture.jpeg")
							count = "2"
						}
						staged := ""
						if uploaded && (!saved || mode == "save_noop" || mode == "still_staged") {
							staged = `<div data-testid="uploaded-files-merge-list-container">fixture.jpeg</div>`
						}
						body = `<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">Stores</a><input id="performer-stage-name-input" value="Test Performer">Performer ID: 101 Documents (` + count + `)<div role="combobox">Performer ID</div><table id="performer-documents-table"><tbody>` + rows + `</tbody></table>` + staged + `</body>`
					}
					return map[string]any{"html": body, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						if strings.HasSuffix(current, "/performers/101") {
							profileLoads++
						}
						return map[string]any{"current_url": current}
					case "wait_for":
						return map[string]any{"matched": true, "current_url": current}
					case "prepare_upload":
						return map[string]any{"prepared": true, "uploaded": false, "backend": "browserbase", "sha256": strings.Repeat("a", 64), "size_bytes": 22}
					case "screenshot":
						return map[string]any{"current_url": current, "width": 1600, "height": 1000, "som_revision": 1, "som": []any{map[string]any{"id": "save", "text": "Save Documents", "accessible_name": "Save Documents", "x": 50, "y": 200, "w": 150, "h": 44, "role": "button"}}}
					case "select_option":
						return map[string]any{"current_url": current, "success": true}
					case "upload_file":
						uploaded = true
						if in["require_prepared"] != true {
							t.Fatal("source not staged")
						}
						return map[string]any{"current_url": current, "uploaded": true}
					case "click":
						if in["target_id"] == "save" {
							saves++
							saved = true
						}
						return map[string]any{"current_url": current, "success": true}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			raw, _ := json.Marshal(d)
			var definition map[string]any
			json.Unmarshal(raw, &definition)
			rec := saveFixtureActor(t, ctx, app, definition)
			inputs := c4sInputs()
			inputs["performer_id"] = "101"
			inputs["expected_stage_name"] = "Test Performer"
			inputs["document_label"] = "Performer ID"
			inputs["document_type"] = "id"
			inputs["filename"] = "fixture.jpeg"
			inputs["document_mime_type"] = "image/jpeg"
			inputs["genuine_document"] = true
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "upload_performer_document", "input": inputs}); err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			result, _ := getActorRun(ctx, run.ID)
			if (result["status"] == "completed") != (mode == "success") {
				t.Fatalf("status=%v error=%v saves=%d", result["status"], result["error"], saves)
			}
			if saves != 1 {
				t.Fatal("save missing or automatically retried", saves)
			}
		})
	}
}
