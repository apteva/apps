package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func c4sExample(t *testing.T) actorDefinition {
	t.Helper()
	raw, err := os.ReadFile("examples/clips4sale-control.json")
	if err != nil {
		t.Fatal(err)
	}
	var d actorDefinition
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if err = validateActorDefinition(d); err != nil {
		t.Fatal(err)
	}
	return d
}
func c4sInputs() map[string]any {
	return map[string]any{"context_id": "fixture-c4s", "expected_store_id": "1001", "expected_store_name": "Test Store", "clip_id": "501", "title": "Test Clip", "description": "Exact description", "audience": "Straight", "category": "NON-NUDE", "price": "6.99", "expected_currency_symbol": "$", "expected_saved_filename": "approved.mp4", "asset_class": "no_real_person", "request_id": "unique-test", "filename": "approved.mp4", "source_url": "https://example.com/test.mp4"}
}
func c4sAccountDOM(store string) string {
	return `<body><a href="https://workspace.clips4sale.com/stores">Test Store Store ID: ` + store + ` 11 clips</a><div>Total income $0.00 Purchases 0 Next Payout November 7, 2026</div></body>`
}
func c4sDetailsDOM(mode string) string {
	title, body, audience, category, person, filename, store := "Test Clip", "Exact description", "Straight", "NON-NUDE", "2", "approved.mp4", "Test Store"
	switch mode {
	case "wrong_title":
		title = "test clip"
	case "wrong_body":
		body = "Different"
	case "wrong_audience":
		audience = "Other"
	case "wrong_category":
		category = "OTHER"
	case "person_present":
		person = "0"
	case "wrong_filename":
		filename = "other.mp4"
	case "wrong_store":
		store = "Other Store"
	}
	return fmt.Sprintf(`<body>DRAFT<div data-testid="edit-clip_text_store-title">%s</div><div data-testid="edit-clip_input-text_clip-title_input"><textarea>%s</textarea></div><div data-testid="edit-clip_input-text_clip-description_input"><textarea>%s</textarea></div><div id="mui-component-select-orientation_id">%s</div><div id="mui-component-select-category_id">%s</div><input name="controlled-radio-buttons-group" checked value="%s"><div data-testid="edit-clip_text_file-name">%s</div></body>`, store, title, body, audience, category, person, filename)
}
func c4sPreviewDOM(mode string) string {
	id, store, prev := "501", "1001", "501"
	switch mode {
	case "wrong_media":
		prev = "502"
	case "wrong_media_store":
		store = "1002"
	case "wrong_clip":
		id = "503"
	}
	return fmt.Sprintf(`<body><h2 data-testid="edit-clip_header_section-title">Test Clip</h2><img data-testid="clip-thumbnail_image_main-showcase" src="https://imagecdn.clips4sale.com/accounts99/%s/clip_images/generated_%s.jpg"><video data-testid="clip-preview_video_main-showcase"><source src="https://imagecdn.clips4sale.com/accounts99/%s/clips_previews/prev_%s.mp4"></video></body>`, store, id, store, prev)
}
func c4sPriceDOM(mode string) string {
	price, symbol, publish, status := "6.99", "$", "now", "DRAFT"
	switch mode {
	case "wrong_price":
		price = "9.99"
	case "wrong_currency":
		symbol = "€"
	case "scheduled":
		publish = "later"
	case "not_draft":
		status = "REVIEW"
	}
	return fmt.Sprintf(`<body>%s<div data-testid="edit-clip_header_section-title">Test Clip</div><span data-testid="edit-clip_text_price-display-value">%s%s</span><input type="radio" value="%s" checked></body>`, status, symbol, price, publish)
}
func c4sLibraryDOM(status string) string {
	return fmt.Sprintf(`<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">My Stores</a><button data-testid="content_management_section_menu_button">Content Management</button><table id="cm-clips-table"><tbody><tr data-testid="all-content_[0]_table-row"><td><img data-testid="clip_[0]_image_thumb" src="https://imagecdn.clips4sale.com/accounts99/1001/clip_images/generated_501.jpg"><span data-testid="clip_[0]_title" aria-label="Test Clip">Test Clip</span><span data-testid="clip_[0]_filename">approved.mp4</span><span data-testid="clip_[0]_price">$6.99</span><span data-testid="all-content_[0]_cell_status" aria-label="status: %s">%s</span><span data-testid="clip_[0]_performers">No Performer / Animated</span></td></tr></tbody></table><footer data-testid="all-content_pagination_label_range">1-1 of 1</footer></body>`, status, status)
}
func c4sEmptyReviewDOM() string {
	return `<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">My Stores</a><button data-testid="content_management_section_menu_button">Content Management</button><table id="cm-clips-table"><tbody></tbody></table><div>No content matches the current filters</div><footer data-testid="action-required_pagination_label_range">0-0 of 0</footer></body>`
}

func TestClips4SaleExampleAndPublishingGuards(t *testing.T) {
	template := c4sExample(t)
	for _, mode := range []string{"success", "validation_success", "under_review_success", "wrong_title", "wrong_body", "wrong_audience", "wrong_category", "person_present", "wrong_filename", "wrong_store", "wrong_media", "wrong_media_store", "wrong_clip", "wrong_price", "wrong_currency", "scheduled", "not_draft", "unchanged_status", "ambiguous", "post_wrong_title", "post_wrong_price", "post_wrong_filename", "post_missing_clip", "post_attention_required", "existing_attention_required"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			d := template
			op := d.Operations["publish_clip"]
			start := 0
			for i, s := range op.Steps {
				if strings.HasSuffix(s.URL, "/content/{{clip_id}}?step=1") {
					start = i
					break
				}
			}
			op.Steps = op.Steps[start:]
			d.Operations = map[string]actorOperation{"publish_clip": op}
			plat := newFakePlatform()
			current := ""
			commits := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := c4sDetailsDOM(mode)
					if current == "https://workspace.clips4sale.com/" {
						h = c4sAccountDOM("1001")
					}
					if strings.Contains(current, "step=2") {
						h = c4sPreviewDOM(mode)
					}
					if strings.Contains(current, "step=3") {
						h = c4sPriceDOM(mode)
					}
					if strings.Contains(current, "/content") && !strings.Contains(current, "step=") {
						status := "review"
						if mode == "validation_success" {
							status = "validation"
						}
						if mode == "under_review_success" {
							status = "under_review"
						}
						if mode == "unchanged_status" || commits == 0 {
							status = "draft"
						}
						h = c4sLibraryDOM(status)

						if strings.HasSuffix(current, "/content/actions") {
							if mode == "existing_attention_required" || (mode == "post_attention_required" && commits > 0) {
								h = c4sLibraryDOM("pending")
								h = strings.Replace(h, "</tr>", `<td data-testid="clip_[0]_rejection_reason">Duplication</td><td data-testid="clip_[0]_comment">Duplicate of 499</td></tr>`, 1)
							} else {
								h = c4sEmptyReviewDOM()
							}
						}
						if commits > 0 {
							switch mode {
							case "post_wrong_title":
								h = strings.ReplaceAll(h, "Test Clip", "Other Clip")
							case "post_wrong_price":
								h = strings.ReplaceAll(h, "$6.99", "$9.99")
							case "post_wrong_filename":
								h = strings.ReplaceAll(h, "approved.mp4", "other.mp4")
							case "post_missing_clip":
								h = strings.ReplaceAll(h, "generated_501.jpg", "generated_502.jpg")
							}
						}
					}
					if in["readability"] != false {
						t.Error("raw DOM requirement lost")
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "wait", "wait_for":
						return map[string]any{"current_url": current, "matched": true}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 5, "som": []any{map[string]any{"id": "Drafts", "tag": "button", "role": "tab", "accessible_name": "Drafts", "effect": "navigation_only"}, map[string]any{"id": "Review", "tag": "button", "role": "tab", "accessible_name": "Review", "effect": "navigation_only"}, map[string]any{"id": "Deleted", "tag": "button", "role": "tab", "accessible_name": "Deleted", "effect": "navigation_only"}, map[string]any{"id": "previous", "tag": "button", "accessible_name": "Previous", "effect": "navigation_only"}, map[string]any{"id": "next", "tag": "button", "accessible_name": "Next", "effect": "navigation_only"}, map[string]any{"id": "publish", "tag": "button", "accessible_name": "Publish", "dangerous": true, "effect": "immediate_external_commit"}}}
					case "click":
						switch in["target_id"] {
						case "Drafts":
							current = "https://workspace.clips4sale.com/content/drafts"
							return map[string]any{"current_url": current}
						case "Review":
							current = "https://workspace.clips4sale.com/content/actions"
							return map[string]any{"current_url": current}
						case "Deleted":
							current = "https://workspace.clips4sale.com/content/deleted"
							return map[string]any{"current_url": current}
						}
						if in["target_id"] == "previous" {
							current = strings.Replace(current, "step=2", "step=1", 1)
							return map[string]any{"current_url": current}
						}
						if in["target_id"] == "next" {
							if in["expected_effect"] != "navigation_only" || in["coordinate"] != nil {
								t.Error("unguarded composer navigation")
							}
							if strings.Contains(current, "step=1") {
								current = strings.Replace(current, "step=1", "step=2", 1)
							} else {
								current = strings.Replace(current, "step=2", "step=3", 1)
							}
							return map[string]any{"current_url": current}
						}
						if in["target_id"] != "publish" || in["expected_text"] != "Publish" || in["expected_effect"] != "immediate_external_commit" || in["confirm_consequence"] != "immediate_external_commit" || in["coordinate"] != nil {
							t.Error("unguarded publish", in)
						}
						commits++
						return map[string]any{"current_url": current, "success": mode != "ambiguous"}
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
				q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "publish_clip", "input": c4sInputs()})
				if err != nil {
					t.Fatal(err)
				}
				run, _ := claimActorRun(ctx)
				app.executeActorRun(context.Background(), ctx, run)
				r, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
				return r
			}
			r := execute()
			if (r["status"] == "completed") != (mode == "success" || mode == "validation_success" || mode == "under_review_success") {
				t.Fatalf("mode=%s status=%v error=%v", mode, r["status"], r["error"])
			}
			want := 0
			if mode == "success" || mode == "validation_success" || mode == "under_review_success" || mode == "unchanged_status" || mode == "ambiguous" || strings.HasPrefix(mode, "post_") {
				want = 1
			}
			if commits != want {
				t.Fatalf("commits=%d want=%d", commits, want)
			}
			if want == 1 {
				r = execute()
				if r["status"] == "completed" || commits != 1 {
					t.Fatal("duplicate or ambiguous publication replayed")
				}
			}
		})
	}
	// The preflight definition has exactly the same commit guards but no publish
	// or writes. Validate it rather than trusting its name.
	op := template.Operations["preflight_clip"]
	if !op.ReadOnly {
		t.Fatal("preflight is not read-only")
	}
	for _, s := range op.Steps {
		if !readOnlyActorAction(s.Action) {
			t.Fatal("preflight contains a write", s.Action)
		}
	}
}
func TestClips4SaleAccountWrongIdentityStops(t *testing.T) {
	for _, mode := range []string{"success", "wrong_account"} {
		t.Run(mode, func(t *testing.T) {
			d := c4sExample(t)
			plat := newFakePlatform()
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool == "computer.browser_extract" {
					id := "1001"
					if mode == "wrong_account" {
						id = "1002"
					}
					return map[string]any{"html": c4sAccountDOM(id), "current_url": "https://workspace.clips4sale.com/", "rendered": true}
				}
				if tool == "computer.computer_use" {
					return map[string]any{"matched": true, "current_url": "https://workspace.clips4sale.com/"}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, _ := json.Marshal(d)
			var def map[string]any
			json.Unmarshal(raw, &def)
			rec := saveFixtureActor(t, ctx, app, def)
			q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "get_account", "input": c4sInputs()})
			if err != nil {
				t.Fatal(err)
			}
			r, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, r)
			out, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
			if (out["status"] == "completed") != (mode == "success") {
				t.Fatal(out["status"], out["error"])
			}
		})
	}
}

func TestClips4SalePreparationVerifiesAndNeverPublishes(t *testing.T) {
	for _, badCopy := range []bool{false, true} {
		t.Run(fmt.Sprint(badCopy), func(t *testing.T) {
			d := c4sExample(t)
			plat := newFakePlatform()
			current := ""
			saves := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := c4sDetailsDOM("success")
					if badCopy {
						h = c4sDetailsDOM("wrong_body")
					}
					if strings.Contains(current, "step=2") {
						h = c4sPreviewDOM("success")
					}
					if strings.Contains(current, "step=3") {
						h = c4sPriceDOM("success")
					}
					if strings.HasSuffix(current, "/content/drafts") {
						h = c4sLibraryDOM("draft")
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "wait", "wait_for":
						return map[string]any{"current_url": current, "matched": true}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 5, "som": []any{
							map[string]any{"id": "Previous", "tag": "button", "accessible_name": "Previous", "effect": "navigation_only"},
							map[string]any{"id": "Next", "tag": "button", "accessible_name": "Next", "effect": "navigation_only"},
							map[string]any{"id": "audience", "tag": "li", "role": "option", "accessible_name": "Straight"},
							map[string]any{"id": "category", "tag": "li", "role": "menuitem", "accessible_name": "NON-NUDE", "effect": "navigation_only"},
							map[string]any{"id": "Save and Exit", "tag": "button", "accessible_name": "Save and Exit"},
							map[string]any{"id": "Change price", "tag": "button", "accessible_name": "Change price"},
							map[string]any{"id": "Save price", "tag": "button", "accessible_name": "Save price"},
						}}
					case "click":
						if in["coordinate"] != nil {
							t.Fatal("coordinate click", in)
						}
						switch in["target_id"] {
						case "Publish":
							t.Fatal("preparation attempted Publish")
						case "Previous":
							current = strings.Replace(current, "step=2", "step=1", 1)
						case "Next":
							if strings.Contains(current, "step=1") {
								current = strings.Replace(current, "step=1", "step=2", 1)
							} else {
								current = strings.Replace(current, "step=2", "step=3", 1)
							}
						case "Save and Exit":
							saves++
							current = "https://workspace.clips4sale.com/content/drafts"
						}
					}
					return map[string]any{"current_url": current}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, _ := json.Marshal(d)
			var def map[string]any
			json.Unmarshal(raw, &def)
			rec := saveFixtureActor(t, ctx, app, def)
			q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "prepare_existing_clip", "input": c4sInputs()})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			r, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
			if (r["status"] == "completed") == badCopy {
				t.Fatal(r["status"], r["error"])
			}
			want := 1
			if badCopy {
				want = 0
			}
			if saves != want {
				t.Fatal("saves", saves)
			}
		})
	}
}

func TestClips4SaleNewUploadSavesReopensAndRejectsDuplicate(t *testing.T) {
	for _, op := range []string{"prepare_clip", "prepare_clip_base64"} {
		t.Run(op, func(t *testing.T) {
			d := c4sExample(t)
			plat := newFakePlatform()
			current := ""
			uploads, saves := 0, 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := c4sDetailsDOM("success")
					if current == "https://workspace.clips4sale.com/" {
						h = c4sAccountDOM("1001")
					}
					if strings.Contains(current, "step=2") {
						h = c4sPreviewDOM("success")
					}
					if strings.Contains(current, "step=3") {
						h = c4sPriceDOM("success")
					}
					if strings.Contains(current, "/content") && !strings.Contains(current, "step=") {
						h = c4sLibraryDOM("draft")
						if uploads == 0 {
							h = `<body><a data-testid="app-drawer_a_menu-button-my-stores" href="https://workspace.clips4sale.com/stores">My Stores</a><button data-testid="content_management_section_menu_button">Content Management</button><table id="cm-clips-table"><tbody></tbody></table><div>No clips No drafts No content matches the current filters Trash is empty</div><footer data-testid="all-content_pagination_label_range">0-0 of 0</footer></body>`
						}
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
					case "wait", "wait_for":
						// HTML source nodes exist but are not visible targets.
						conditions, _ := in["conditions"].([]actorWaitCondition)
						for _, condition := range conditions {
							if strings.Contains(condition.Selector, " source") {
								return map[string]any{"current_url": current, "matched": false, "timed_out": true}
							}
						}
						return map[string]any{"current_url": current, "matched": true}
					case "upload_file":
						uploads++
						if (op == "prepare_clip") != (in["source_url"] != nil) || (op == "prepare_clip_base64") != (in["base64"] != nil) {
							t.Fatal("wrong media source", in)
						}
						return map[string]any{"current_url": current, "uploaded": true, "filename": "approved.mp4"}
					case "screenshot":
						targets := []any{}
						for _, name := range []string{"Upload", "Upload Files", "Next", "Previous", "Save and Exit", "Change price", "Save price"} {
							targets = append(targets, map[string]any{"id": name, "tag": "button", "accessible_name": name, "effect": "navigation_only"})
						}
						for _, name := range []string{"Drafts", "Review", "Deleted"} {
							targets = append(targets, map[string]any{"id": name, "tag": "button", "role": "tab", "accessible_name": name, "effect": "navigation_only"})
						}
						targets = append(targets, map[string]any{"id": "audience", "tag": "li", "role": "option", "accessible_name": "Straight"}, map[string]any{"id": "category", "tag": "li", "role": "menuitem", "accessible_name": "NON-NUDE", "effect": "navigation_only"})
						return map[string]any{"current_url": current, "som_revision": 9, "som": targets}
					case "click":
						if in["coordinate"] != nil {
							t.Fatal("coordinate fallback")
						}
						switch in["target_id"] {
						case "Publish":
							t.Fatal("preparation published")
						case "Upload":
							current = "https://workspace.clips4sale.com/upload"
						case "Drafts":
							current = "https://workspace.clips4sale.com/content/drafts"
						case "Review":
							current = "https://workspace.clips4sale.com/content/actions"
						case "Deleted":
							current = "https://workspace.clips4sale.com/content/deleted"
						case "Save and Exit":
							saves++
							current = "https://workspace.clips4sale.com/content/drafts"
						case "Next":
							if strings.Contains(current, "step=1") {
								current = strings.Replace(current, "step=1", "step=2", 1)
							} else {
								current = strings.Replace(current, "step=2", "step=3", 1)
							}
						}
					}
					return map[string]any{"current_url": current}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, _ := json.Marshal(d)
			var def map[string]any
			json.Unmarshal(raw, &def)
			rec := saveFixtureActor(t, ctx, app, def)
			inp := c4sInputs()
			inp["media_base64"] = "AAAA"
			for attempt := 0; attempt < 2; attempt++ {
				q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": op, "input": inp})
				if err != nil {
					t.Fatal(err)
				}
				run, _ := claimActorRun(ctx)
				app.executeActorRun(context.Background(), ctx, run)
				r, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
				if (r["status"] == "completed") != (attempt == 0) {
					t.Fatal(r["status"], r["error"])
				}
			}
			if uploads != 1 || saves != 2 {
				t.Fatalf("uploads=%d saves=%d", uploads, saves)
			}
		})
	}
}
