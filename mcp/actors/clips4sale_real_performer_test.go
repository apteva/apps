package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func realPerformerDefinition(t *testing.T) actorDefinition {
	t.Helper()
	b, err := os.ReadFile("examples/clips4sale-real-performer-control.json")
	if err != nil {
		t.Fatal(err)
	}
	var d actorDefinition
	if err = json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if err = validateActorDefinition(d); err != nil {
		t.Fatal(err)
	}
	return d
}
func realPerformerInputs() map[string]any {
	v := c4sInputs()
	v["asset_class"] = "real_performer"
	v["real_person_appears"] = true
	v["performer_id"] = "101"
	v["expected_stage_name"] = "Test Performer"
	v["id_document_id"] = "601"
	v["id_document_id_2"] = "602"
	v["consent_document_id"] = "701"
	v["expected_id_document_filename"] = "approved_id.pdf"
	v["expected_id_document_filename_2"] = "approved_id_back.pdf"
	v["expected_consent_document_filename"] = "approved_release.pdf"
	v["recording_date"] = "2026-10-20"
	v["authorization_evidence_ref"] = "gigs:test:recording-20261020:signed-release"
	v["recording_authorization_ref"] = "gigs:test:recording-20261020:recording-authority"
	return v
}
func realAccountDOM() string {
	return `<body><a href="https://workspace.clips4sale.com/stores">Test Store Store ID: 1001 18 clips</a><div>Total income $0.00 C4S Purchases $0.00 Total eligible for next payout $0.00 Next Payout $0.00 of $1,000.00</div></body>`
}
func realPerformerListDOM(mode string) string {
	name := "Test Performer"
	if mode == "wrong_performer" {
		name = "Other Performer"
	}
	return `<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">Stores</a><button data-testid="performers-page_section_menu_button">Performers</button><table><tr data-testid="all-performers_[0]_table-row"><td data-testid="all-performers_[0]_stage_name_title">` + name + `</td><td data-testid="all-performers_[0]_stage_name_store_id">Store ID: 1001</td><td data-testid="all-performers_[0]_cell_id">101</td><td data-testid="all-performers_[0]_cell_clips_count">1</td><td data-testid="all-performers_[0]_cell_created_at" aria-label="created at: 2026-10-20">Oct20</td></tr></table><footer data-testid="all-performers_pagination_label_range">1-1 of 1</footer></body>`
}
func realPerformerProfileDOM(mode string, two bool) string {
	status := "valid"
	if mode == "id_under_review" {
		status = "tbd"
	}
	if mode == "id_rejected" {
		status = "not_valid"
	}
	row := func(id, typ, name, status string) string {
		return fmt.Sprintf(`<tr><td aria-label="id: %s">%s</td><td aria-label="filename: %s">%s</td><td aria-label="type: %s">%s</td><td aria-label="status: %s">%s</td></tr>`, id, id, name, name, typ, typ, status, status)
	}
	rows := row("601", "id", "approved_id.pdf", status)
	count := 1
	if two {
		rows += row("602", "id", "approved_id_back.pdf", status)
		count++
	}
	if mode != "missing_release" && mode != "no_documents" {
		releaseStatus := "valid"
		if mode == "release_under_review" {
			releaseStatus = "tbd"
		}
		if mode == "release_rejected" {
			releaseStatus = "not_valid"
		}
		name := "approved_release.pdf"
		if mode == "wrong_release_file" {
			name = "other_release.pdf"
		}
		rows += row("701", "consent", name, releaseStatus)
		count++
	}
	if mode == "old_rejected_unselected" {
		rows += row("799", "consent", "old_rejected_release.pdf", "not_valid")
		count++
	}
	table := `<table id="performer-documents-table"><tbody>` + rows + `</tbody></table>`
	if mode == "no_documents" {
		count = 0
		table = `<div data-testid="section-page-content-wrapper">There is no data for this filters.</div>`
	}
	return fmt.Sprintf(`<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">Stores</a><input id="performer-stage-name-input" value="Test Performer"><input id="performer-email-input" value="model@example.test"><input name="birthday_date" value="1990-01-01">Performer ID: 101 Documents (%d) %s</body>`, count, table)
}
func realClipDOM(mode, popup string, two bool) string {
	d := c4sDetailsDOM("")
	classification := "0"
	if mode == "no_performer_classification" {
		classification = "2"
	}
	d = strings.Replace(d, `<input name="controlled-radio-buttons-group" checked value="2">`, `<div data-testid="edit-clip_radio-group_performers"><span class="Mui-checked"><input name="controlled-radio-buttons-group" checked value="`+classification+`"></span></div>`, 1)
	status := "Reviewed"
	if mode == "consent_requested" {
		status = "Consent Requested"
	}
	name := "Test Performer"
	if mode == "wrong_clip_performer" {
		name = "Someone Else"
	}
	rows := `<div data-testid="edit-clip_featured-performers_list"><div data-testid="featured-performers_row_0"><span data-testid="featured-performers_row_0_span_stage-name">` + name + `</span><span data-testid="featured-performers_row_0_span_status_label">` + status + `</span><span data-testid="featured-performers_row_0_popover_id-docs_label">1 Photo ID(s)</span><span data-testid="featured-performers_row_0_popover_consent-docs_label">1 Consent Form(s)</span></div>`
	if mode == "unexpected_second_performer" {
		rows += `<div data-testid="featured-performers_row_1"><span data-testid="featured-performers_row_1_span_stage-name">Unexpected</span></div>`
	}
	rows += `</div>`
	if popup != "" {
		kind, doc := "id-docs", "id-documents"
		files := []string{"approved_id.pdf"}
		if two {
			files = append(files, "approved_id_back.pdf")
		}
		if popup == "consent" {
			kind = "consent-docs"
			doc = "consent-documents"
			files = []string{"approved_release.pdf"}
			if mode == "wrong_attached_release" {
				files = []string{"wrong_release.pdf"}
			}
		}
		if mode == "extra_attached_document" {
			files = append(files, "unverified.pdf")
		}
		id := "101"
		if mode == "wrong_popover_performer" {
			id = "999"
		}
		rows += `<div id="documents-upload-` + doc + `-` + id + `">`
		for i, f := range files {
			rows += fmt.Sprintf(`<span data-testid="featured-performers_row_0_popover_%s_content_document_%d">%s</span>`, kind, i, f)
		}
		rows += `</div>`
	}
	return strings.Replace(d, "</body>", rows+"</body>", 1)
}
func TestRealPerformerPublicationAuthorization(t *testing.T) {
	template := realPerformerDefinition(t)
	modes := []string{"success", "two_id_documents", "old_rejected_unselected", "wrong_performer", "id_under_review", "id_rejected", "release_under_review", "release_rejected", "missing_release", "no_documents", "wrong_release_file", "consent_requested", "unexpected_second_performer", "wrong_clip_performer", "no_performer_classification", "wrong_attached_release", "extra_attached_document", "wrong_popover_performer", "wrong_price", "wrong_media", "missing_evidence_ref", "wrong_asset_class", "missing_real_person_flag"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			d := template
			operation := "publish_clip_with_performer"
			two := mode == "two_id_documents"
			if two {
				operation += "_two_id_documents"
			}
			d.Operations = map[string]actorOperation{operation: template.Operations[operation]}
			plat := newFakePlatform()
			current, popup := "", ""
			commits := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := realClipDOM(mode, popup, two)
					switch {
					case current == "https://workspace.clips4sale.com/":
						h = realAccountDOM()
					case strings.HasSuffix(current, "/performers"):
						h = realPerformerListDOM(mode)
					case strings.HasSuffix(current, "/performers/101"):
						h = realPerformerProfileDOM(mode, two)
					case strings.Contains(current, "step=2"):
						h = c4sPreviewDOM(mode)
					case strings.Contains(current, "step=3"):
						h = c4sPriceDOM(mode)
					case strings.Contains(current, "/content") && !strings.Contains(current, "step="):
						status := "draft"
						if commits > 0 {
							status = "under_review"
						}
						h = c4sLibraryDOM(status)
						if strings.HasSuffix(current, "/actions") {
							h = c4sEmptyReviewDOM()
						}
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						popup = ""
						return map[string]any{"current_url": current}
					case "wait", "wait_for":
						return map[string]any{"current_url": current, "matched": true}
					case "key":
						popup = ""
						return map[string]any{"current_url": current}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 5, "som": []any{map[string]any{"id": "Drafts", "tag": "button", "role": "tab", "accessible_name": "Drafts", "effect": "navigation_only"}, map[string]any{"id": "Review", "tag": "button", "role": "tab", "accessible_name": "Review", "effect": "navigation_only"}, map[string]any{"id": "Deleted", "tag": "button", "role": "tab", "accessible_name": "Deleted", "effect": "navigation_only"}, map[string]any{"id": "next", "tag": "button", "accessible_name": "Next", "effect": "navigation_only"}, map[string]any{"id": "publish", "tag": "button", "accessible_name": "Publish", "dangerous": true, "effect": "immediate_external_commit"}}}
					case "click":
						selector := stringFromAny(in["selector"])
						if strings.Contains(selector, "popover_id-docs_label") {
							popup = "id"
							return map[string]any{"current_url": current}
						}
						if strings.Contains(selector, "popover_consent-docs_label") {
							popup = "consent"
							return map[string]any{"current_url": current}
						}
						switch in["target_id"] {
						case "Drafts":
							current = "https://workspace.clips4sale.com/content/drafts"
						case "Review":
							current = "https://workspace.clips4sale.com/content/actions"
						case "Deleted":
							current = "https://workspace.clips4sale.com/content/deleted"
						case "next":
							if strings.Contains(current, "step=1") {
								current = strings.Replace(current, "step=1", "step=2", 1)
							} else {
								current = strings.Replace(current, "step=2", "step=3", 1)
							}
						case "publish":
							if in["expected_effect"] != "immediate_external_commit" || in["confirm_consequence"] != "immediate_external_commit" {
								t.Error("unguarded publication", in)
							}
							commits++
						default:
							t.Errorf("unexpected click: %v", in)
						}
						return map[string]any{"current_url": current, "success": true}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			b, _ := json.Marshal(d)
			var def map[string]any
			json.Unmarshal(b, &def)
			rec := saveFixtureActor(t, ctx, app, def)
			inputs := realPerformerInputs()
			switch mode {
			case "missing_evidence_ref":
				delete(inputs, "authorization_evidence_ref")
			case "wrong_asset_class":
				inputs["asset_class"] = "no_real_person"
			case "missing_real_person_flag":
				delete(inputs, "real_person_appears")
			}
			execute := func() map[string]any {
				q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": operation, "input": inputs})
				if err != nil {
					t.Fatal(err)
				}
				run, _ := claimActorRun(ctx)
				app.executeActorRun(context.Background(), ctx, run)
				v, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
				return v
			}
			r := execute()
			want := mode == "success" || mode == "two_id_documents" || mode == "old_rejected_unselected"
			if (r["status"] == "completed") != want {
				t.Fatalf("status=%v error=%v commits=%d", r["status"], r["error"], commits)
			}
			if want && commits != 1 || !want && commits != 0 {
				t.Fatalf("unexpected commits %d", commits)
			}
			if want {
				r = execute()
				if r["status"] == "completed" || commits != 1 {
					t.Fatalf("duplicate publication allowed: %v commits=%d", r["error"], commits)
				}
			}
		})
	}
}
func TestRealPerformerDefinitionPreservesOriginalAndPrivatePreparation(t *testing.T) {
	d := realPerformerDefinition(t)
	for _, n := range []string{"publish_clip_with_performer", "publish_clip_with_performer_two_id_documents"} {
		var commits []actorStep
		for _, s := range d.Operations[n].Steps {
			if s.ExpectedEffect == "immediate_external_commit" {
				commits = append(commits, s)
			}
		}
		if len(commits) != 1 || commits[0].OnceKey != "c4s:{{expected_store_id}}:clip:{{clip_id}}:publish" || commits[0].OnceScope != "context" {
			t.Fatal(n, commits)
		}
	}
	for _, n := range []string{"upload_performer_clip_draft", "upload_performer_clip_draft_base64", "preflight_clip_with_performer", "prepare_existing_clip_with_performer"} {
		for _, s := range d.Operations[n].Steps {
			if s.ExpectedEffect == "immediate_external_commit" || s.ExpectedEffect == "message_send" || strings.Contains(s.Locator.Selector, "no-performers-option") {
				t.Fatal("preparation can publish/send/misclassify real footage", n, s)
			}
		}
	}
	if d.Operations["verify_clip_consent_request"].Steps[len(d.Operations["verify_clip_consent_request"].Steps)-1].Assertions["consent_status"].EqualsExact != "Consent Requested" {
		t.Fatal("pending receipt verifier changed")
	}
	for _, s := range d.Operations["verify_clip_consent"].Steps {
		if s.ExpectedEffect == "message_send" || s.ExpectedEffect == "immediate_external_commit" {
			t.Fatal("authorization verification sends/publishes")
		}
	}
}
