package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func c4sConsentDOM(mode string, selected, request, sent, revealed bool) string {
	h := c4sDetailsDOM(mode)
	radio := `<div data-testid="edit-clip_radio-group_performers"><span class="Mui-checked"><input value="2"></span></div>`
	if revealed {
		radio = `<div data-testid="edit-clip_radio-group_performers"><span class="Mui-checked"><input value="0"></span></div>`
	}
	h = strings.Replace(h, `<input name="controlled-radio-buttons-group" checked value="2">`, radio, 1)
	name, id, store, email := "Test Performer", "601", "1001", "test@example.com"
	if mode == "wrong_menu_id" {
		id = "602"
	}
	if mode == "wrong_menu_name" {
		name = "Other Performer"
	}
	if mode == "wrong_menu_store" {
		store = "1002"
	}
	h = strings.Replace(h, "</body>", fmt.Sprintf(`<div role="listbox" data-testid="edit-clip_performers-dropdown_section_list"><li role="menuitem" value="%s"><span data-testid="edit-clip_performers-dropdown_section_item_label_0">%s</span><span data-testid="edit-clip_performers-dropdown_section_item_description_0">Store %s</span></li></div></body>`, id, name, store), 1)
	if selected {
		checked := ""
		if request && mode != "wrong_request_mode" {
			checked = "Mui-checked"
		}
		if mode == "wrong_form_email" {
			email = "other@example.com"
		}
		h = strings.Replace(h, "</body>", fmt.Sprintf(`<div id="performers-dropdown-select">%s</div><span class="%s"><input data-testid="performers_featured_radio_request-digital-consent" value="0"></span><div data-testid="performers_add-digital-consent_email_input"><input value="%s"></div></body>`, name, checked, email), 1)
	}
	if sent && revealed {
		h = strings.Replace(h, "</body>", `<div data-testid="featured-performers_row_0"><span data-testid="featured-performers_row_0_span_stage-name">Test Performer</span><span data-testid="featured-performers_row_0_span_status_label">Consent Requested</span></div></body>`, 1)
	}
	if mode == "post_wrong_status" || mode == "verify_wrong_status" {
		h = strings.ReplaceAll(h, "Consent Requested", "Consent Signed")
	}
	if mode == "post_wrong_stage" && sent {
		h = strings.ReplaceAll(h, ">Test Performer</span>", ">Other Performer</span>")
	}
	return h
}

func TestClips4SaleConsentGuards(t *testing.T) {
	for _, mode := range []string{"success", "wrong_store", "wrong_saved_email", "wrong_title", "wrong_body", "wrong_audience", "wrong_category", "wrong_filename", "not_draft", "wrong_menu_id", "wrong_menu_name", "wrong_menu_store", "wrong_form_email", "wrong_request_mode", "wrong_confirmation", "ambiguous", "post_missing_evidence", "post_dialog_stays_open", "post_wrong_status", "post_wrong_stage", "existing_request", "verify_success", "verify_missing", "verify_wrong_status", "not_real_person", "preflight_success", "test_success", "save_ambiguous", "post_missing_saved", "post_wrong_saved_mode"} {
		t.Run(mode, func(t *testing.T) {
			d := c4sExample(t)
			opName := "request_clip_consent"
			if strings.HasPrefix(mode, "verify_") {
				opName = "verify_clip_consent"
			}
			if mode == "test_success" {
				opName = "request_clip_consent_test"
			}
			if mode == "preflight_success" {
				opName = "preflight_clip_consent"
			}
			op := d.Operations[opName]
			d.Operations = map[string]actorOperation{opName: op}
			plat := newFakePlatform()
			current := ""
			selected, request, sent, modal, revealed := false, false, strings.HasPrefix(mode, "verify_") || mode == "existing_request", false, false
			commits, saves := 0, 0
			saved := strings.HasPrefix(mode, "verify_")
			transientPending := false
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := c4sPerformerLibraryDOM(true)
					if current == "https://workspace.clips4sale.com/" {
						store := "1001"
						if mode == "wrong_store" {
							store = "1002"
						}
						h = c4sAccountDOM(store)
					}
					if strings.Contains(current, "/performers/601") {
						h = c4sPerformerDetailDOM("", false, "Performer ID")
						if mode == "wrong_saved_email" {
							h = strings.ReplaceAll(h, "test@example.com", "other@example.com")
						}
					}
					if strings.Contains(current, "/content") {
						status := "draft"
						if mode == "not_draft" {
							status = "published"
						}
						h = c4sLibraryDOM(status)
						if strings.HasSuffix(current, "/actions") {
							h = c4sEmptyReviewDOM()
						}
						if strings.Contains(current, "step=1") {
							h = c4sConsentDOM(mode, selected, request, (transientPending || (saved && sent) || mode == "existing_request") && mode != "post_missing_evidence" && mode != "verify_missing" && !(saved && mode == "post_missing_saved"), revealed && !(saved && mode == "post_wrong_saved_mode"))
							if modal {
								name := "Test Performer"
								if mode == "wrong_confirmation" {
									name = "Other Performer"
								}
								h = fmt.Sprintf(`<body><div data-testid="request-digital-consent-dialog"><h2 data-testid="request-digital-consent-dialog-header-title">Request Digital Consent</h2><div data-testid="request-digital-consent-dialog-content-text">You're about to request digital consent from %s. They will be notified via email. The clip status will remain &quot;Consent requested&quot; until the document is signed.</div><button data-testid="request-digital-consent-dialog-actions-button-confirm">Request Consent</button></div></body>`, name)
							}
						}
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						selected = false
						request = false
						revealed = saved
						transientPending = false
						return map[string]any{"current_url": current, "success": true}
					case "wait_for":
						for _, condition := range func() []any { v, _ := in["conditions"].([]any); return v }() {
							if strings.Contains(stringFromAny(mapFromAny(condition)["selector"]), ":contains(") {
								return map[string]any{"success": false, "matched": false, "error": "extraction-only selector is invalid native CSS"}
							}
						}
						if mode == "post_dialog_stays_open" && sent {
							return map[string]any{"current_url": current, "matched": false, "success": false}
						}
						return map[string]any{"current_url": current, "matched": true, "success": true}
					case "wait", "key", "set_text":
						return map[string]any{"current_url": current, "matched": true, "success": true}
					case "set_checked":
						if strings.Contains(stringFromAny(in["selector"]), "edit-clip_radio-input_performers-option") {
							revealed = true
						}
						if strings.Contains(stringFromAny(in["selector"]), "request-digital-consent") {
							request = true
						}
						return map[string]any{"current_url": current, "success": true}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 7, "som": []any{
							map[string]any{"id": "Drafts", "tag": "button", "role": "tab", "accessible_name": "Drafts", "effect": "navigation_only"}, map[string]any{"id": "Review", "tag": "button", "role": "tab", "accessible_name": "Review", "effect": "navigation_only"}, map[string]any{"id": "Deleted", "tag": "button", "role": "tab", "accessible_name": "Deleted", "effect": "navigation_only"},
							map[string]any{"id": "performer", "tag": "li", "role": "menuitem", "accessible_name": "Test Performer Store 1001", "effect": "navigation_only"}, map[string]any{"id": "open-dialog", "tag": "button", "accessible_name": "Send Consent Request", "effect": "message_send"}, map[string]any{"id": "send", "tag": "button", "accessible_name": "Request Consent", "effect": "message_send"}, map[string]any{"id": "save-draft", "tag": "button", "accessible_name": "Save and Exit", "effect": "immediate_external_commit"}}}
					case "click":
						switch in["target_id"] {
						case "Drafts":
							current = "https://workspace.clips4sale.com/content/drafts"
						case "Review":
							current = "https://workspace.clips4sale.com/content/actions"
						case "Deleted":
							current = "https://workspace.clips4sale.com/content/deleted"
						case "performer":
							selected = true
						case "open-dialog":
							if in["expected_effect"] != "message_send" || in["confirm_consequence"] != "message_send" {
								t.Fatal("incorrect dialog opening effect")
							}
							modal = true
						case "save-draft":
							if in["expected_effect"] != "immediate_external_commit" || in["confirm_consequence"] != "immediate_external_commit" {
								t.Fatal("unguarded draft association save")
							}
							saves++
							saved = true
							current = "https://workspace.clips4sale.com/content/drafts"
							return map[string]any{"current_url": current, "success": mode != "save_ambiguous"}
						case "send":
							if in["expected_effect"] != "message_send" || in["confirm_consequence"] != "message_send" || in["coordinate"] != nil {
								t.Fatal("unguarded send")
							}
							commits++
							sent = true
							transientPending = true
							modal = mode == "post_dialog_stays_open"
							return map[string]any{"current_url": current, "success": mode != "ambiguous"}
						}
						return map[string]any{"current_url": current, "success": true}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, _ := json.Marshal(d)
			var def map[string]any
			json.Unmarshal(raw, &def)
			rec := saveFixtureActor(t, ctx, app, def)
			execute := func() map[string]any {
				q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": opName, "input": func() map[string]any {
					inputs := c4sPerformerInputs()
					inputs["real_person_appears"] = mode != "test_success" && mode != "not_real_person"
					inputs["allow_unsaved_test_request"] = mode == "test_success"
					return inputs
				}()})
				if err != nil {
					t.Fatal(err)
				}
				run, _ := claimActorRun(ctx)
				app.executeActorRun(context.Background(), ctx, run)
				r, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
				return r
			}
			r := execute()
			if (r["status"] == "completed") != (mode == "success" || mode == "verify_success" || mode == "test_success" || mode == "preflight_success") {
				t.Fatalf("status=%v error=%v", r["status"], r["error"])
			}
			want := 0
			if mode == "success" || mode == "ambiguous" || mode == "post_missing_evidence" || mode == "post_dialog_stays_open" || mode == "post_wrong_status" || mode == "post_wrong_stage" || mode == "test_success" || mode == "save_ambiguous" || mode == "post_missing_saved" || mode == "post_wrong_saved_mode" {
				want = 1
			}
			if commits != want {
				t.Fatalf("sends=%d want=%d", commits, want)
			}
			wantSaves := 0
			if mode == "success" || mode == "save_ambiguous" || mode == "post_missing_saved" || mode == "post_wrong_saved_mode" {
				wantSaves = 1
			}
			if saves != wantSaves {
				t.Fatalf("saves=%d want=%d", saves, wantSaves)
			}
			if want == 1 {
				selected = false
				request = false
				r = execute()
				if r["status"] == "completed" || commits != 1 || saves != wantSaves {
					t.Fatal("successful or ambiguous request replayed")
				}
			}
		})
	}
}
