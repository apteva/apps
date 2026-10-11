package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func c4sPerformerInputs() map[string]any {
	in := c4sInputs()
	for k, v := range map[string]any{"performer_id": "601", "expected_stage_name": "Test Performer", "stage_name": "Test Performer", "email": "test@example.com", "request_id": "performer-test", "document_type": "consent", "document_label": "Consent Form", "filename": "release.pdf", "document_mime_type": "application/pdf", "source_url": "https://example.com/release.pdf", "document_base64": "JVBERi0xLjcKZml4dHVyZQ==", "genuine_document": true} {
		in[k] = v
	}
	return in
}
func c4sPerformerLibraryDOM(exists bool) string {
	base := `<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">My Stores</a><button data-testid="performers-page_section_menu_button">Performer Management</button>`
	if !exists {
		return base + `<div data-testid="empty-state">No performers in your store</div></body>`
	}
	return base + `<table><tbody><tr data-testid="all-performers_[0]_table-row"><td data-testid="all-performers_[0]_cell_stage_name"><span data-testid="all-performers_[0]_stage_name_title">Test Performer</span><span data-testid="all-performers_[0]_stage_name_store_id">Store ID: 1001</span></td><td data-testid="all-performers_[0]_cell_name">Not Provided</td><td data-testid="all-performers_[0]_cell_id">601</td><td data-testid="all-performers_[0]_cell_clips_count">0 Clips</td><td data-testid="all-performers_[0]_cell_created_at" aria-label="created at: 2026-10-11T15:36:44-04:00">Oct 11, 2026</td></tr></tbody></table><footer data-testid="all-performers_pagination_label_range">1-1 of 1</footer></body>`
}
func c4sPerformerDetailDOM(mode string, saved bool, label string) string {
	id, email, count, filename := "601", "test@example.com", 0, "release.pdf"
	if saved {
		count = 1
	}
	if mode == "wrong_detail_id" {
		id = "602"
	}
	if mode == "post_wrong_email" {
		email = "other@example.com"
	}
	if mode == "post_count_unchanged" {
		count = 0
	}
	if mode == "post_filename_missing" {
		filename = "other.pdf"
	}
	pending := "release.pdf "
	if saved {
		pending = ""
	}
	persisted := ""
	if saved {
		persisted = "Consent Form " + filename + " Pending"
	}
	return fmt.Sprintf(`<body>Performer ID: %s<div data-testid="add-performer-form-content-details-container"><input id="performer-stage-name-input" value="Test Performer"><input id="performer-email-input" value="%s"><input id="performer-name-input" value=""><input name="birthday_date" value="MM DD, YYYY"><input type="checkbox" checked></div><div role="combobox">Document Type %s</div><div data-testid="performereditpage-documents-upload_upload-button">Upload</div><div data-testid="uploaded-files-merge-list-container">%sSave Documents</div><div>Documents (%d)</div><div>%s</div></body>`, id, email, label, pending, count, persisted)
}
func TestClips4SalePerformerRegistrationAndDocumentGuards(t *testing.T) {
	for _, opName := range []string{"register_performer", "upload_performer_document", "upload_performer_document_base64"} {
		modes := []string{"success", "wrong_store", "ambiguous"}
		if opName == "register_performer" {
			modes = append(modes, "duplicate", "wrong_form_email", "unexpected_file", "post_wrong_email")
		} else {
			modes = append(modes, "not_genuine", "bad_filename", "wrong_detail_id", "wrong_label", "post_count_unchanged", "post_filename_missing")
		}
		for _, mode := range modes {
			t.Run(opName+"/"+mode, func(t *testing.T) {
				d := c4sExample(t)
				op := d.Operations[opName]
				d.Operations = map[string]actorOperation{opName: op}
				plat := newFakePlatform()
				current, label := "", "Performer ID"
				created, dialog, saved := opName != "register_performer" || mode == "duplicate", false, false
				commits, uploads := 0, 0
				plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
					switch tool {
					case "computer.browser_open":
						current = stringFromAny(in["url"])
					case "computer.browser_extract":
						h := c4sPerformerLibraryDOM(created)
						if current == "https://workspace.clips4sale.com/" {
							store := "1001"
							if mode == "wrong_store" {
								store = "1002"
							}
							h = c4sAccountDOM(store)
						}
						if strings.Contains(current, "/performers/601") {
							h = c4sPerformerDetailDOM(mode, saved, label)
						}
						if dialog {
							email, file := "test@example.com", ""
							if mode == "wrong_form_email" {
								email = "other@example.com"
							}
							if mode == "unexpected_file" {
								file = "fake.pdf"
							}
							h = fmt.Sprintf(`<body><div data-testid="add-performer-dialog"><div data-testid="add-performer-form-content-details-container"><input id="performer-stage-name-input" value="Test Performer"><input id="performer-email-input" value="%s"><input type="checkbox" checked></div><input type="file" value="%s"><input type="file" value=""></div></body>`, email, file)
						}
						return map[string]any{"html": h, "current_url": current, "rendered": true}
					case "computer.computer_use":
						switch in["action"] {
						case "navigate":
							current = stringFromAny(in["url"])
							return map[string]any{"current_url": current}
						case "wait", "wait_for", "key", "set_text", "set_checked":
							return map[string]any{"current_url": current, "matched": true, "success": true}
						case "select_option":
							if in["value"] == "consent" {
								label = "Consent Form"
							}
							if mode == "wrong_label" {
								label = "Performer ID"
							}
							return map[string]any{"current_url": current, "success": true}
						case "screenshot":
							return map[string]any{"current_url": current, "som_revision": 8, "som": []any{map[string]any{"id": "open", "tag": "button", "accessible_name": "Add New Performer", "effect": "navigation_only"}, map[string]any{"id": "hide", "tag": "input", "role": "checkbox", "accessible_name": "Hide performer from website"}, map[string]any{"id": "save", "tag": "button", "accessible_name": "Save", "effect": "navigation_only"}, map[string]any{"id": "save-doc", "tag": "button", "accessible_name": "Save Documents", "effect": "navigation_only"}}}
						case "upload_file":
							uploads++
							return map[string]any{"current_url": current, "success": true}
						case "click":
							if in["selector"] == `a[data-testid="app-drawer_a_menu-button-performer-management"]` {
								current = "https://workspace.clips4sale.com/performers"
								return map[string]any{"current_url": current, "success": true}
							}
							if in["target_id"] == "open" {
								dialog = true
								return map[string]any{"current_url": current, "success": true}
							}
							if in["target_id"] == "save" || in["target_id"] == "save-doc" {
								if in["expected_effect"] != "immediate_external_commit" || in["confirm_consequence"] != "immediate_external_commit" || in["coordinate"] != nil {
									t.Fatal("unguarded save")
								}
								commits++
								created = true
								dialog = false
								if in["target_id"] == "save-doc" {
									saved = true
								}
								return map[string]any{"current_url": current, "success": mode != "ambiguous"}
							}
						}
					}
					return nil
				}
				ctx, app := newTestCtx(t, plat)
				raw, _ := json.Marshal(d)
				var def map[string]any
				json.Unmarshal(raw, &def)
				rec := saveFixtureActor(t, ctx, app, def)
				inputs := c4sPerformerInputs()
				if mode == "not_genuine" {
					inputs["genuine_document"] = false
				}
				if mode == "bad_filename" {
					inputs["filename"] = "../release.pdf"
				}
				execute := func() map[string]any {
					q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": opName, "input": inputs})
					if err != nil {
						t.Fatal(err)
					}
					run, _ := claimActorRun(ctx)
					app.executeActorRun(context.Background(), ctx, run)
					r, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
					return r
				}
				r := execute()
				if (r["status"] == "completed") != (mode == "success") {
					t.Fatalf("status=%v error=%v", r["status"], r["error"])
				}
				want := 0
				if mode == "success" || mode == "ambiguous" || strings.HasPrefix(mode, "post_") {
					want = 1
				}
				if commits != want {
					t.Fatalf("commits=%d want=%d", commits, want)
				}
				if opName != "register_performer" && want == 0 && uploads != 0 {
					t.Fatal("failed prerequisite uploaded a document")
				}
				if want == 1 {
					r = execute()
					if r["status"] == "completed" || commits != 1 {
						t.Fatal("save replayed after successful or ambiguous outcome")
					}
				}
			})
		}
	}
}
