package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strings"
	"testing"
)

func TestNativeMediaSavedDraftPreflightCurrentLayouts(t *testing.T) {
	raw, err := os.ReadFile("examples/patreon-native-image-drafts.json")
	if err != nil {
		t.Fatal(err)
	}
	var template map[string]any
	json.Unmarshal(raw, &template)
	for _, audience := range []string{"public", "paid"} {
		for _, kind := range []string{"image", "gallery"} {
			for _, mode := range []string{"nested_blurred", "wrong", "extra", "reordered", "wrong_creator", "wrong_title", "wrong_body", "wrong_audience", "wrong_tier", "sale_on"} {
				if kind == "image" && mode == "reordered" {
					continue
				}
				t.Run(audience+"/"+kind+"/"+mode, func(t *testing.T) {
					plat := newFakePlatform()
					current := ""
					commits := 0
					tiers := []any{}
					menu := ""
					radio := fmt.Sprintf(`<input type="radio" value="%s" aria-checked="true">`, audience)
					if audience == "paid" {
						tiers = []any{"Creator tier"}
						menu = `<div role="menuitemcheckbox" aria-checked="true">Creator tier</div>`
					}
					ids := []any{"m1"}
					names := []any{"first.png"}
					sizes := []any{float64(10)}
					if kind == "gallery" {
						ids = append(ids, "m2")
						names = append(names, "second.png")
						sizes = append(sizes, float64(20))
					}
					expectedIDs := append([]any(nil), ids...)
					expectedNames := append([]any(nil), names...)
					expectedSizes := append([]any(nil), sizes...)
					switch mode {
					case "wrong":
						ids[0] = "other"
					case "extra":
						ids = append(ids, "extra")
						names = append(names, "extra.png")
						sizes = append(sizes, float64(30))
					case "reordered":
						ids[0], ids[1] = ids[1], ids[0]
						names[0], names[1] = names[1], names[0]
						sizes[0], sizes[1] = sizes[1], sizes[0]
					}
					refs := []any{}
					inc := []any{}
					for i, id := range ids {
						refs = append(refs, map[string]any{"id": id, "type": "media"})
						inc = append(inc, map[string]any{"id": id, "type": "media", "attributes": map[string]any{"file_name": names[i], "size_bytes": sizes[i], "state": "ready", "media_type": "image"}})
					}
					ruleType := "public"
					var tier any
					if audience == "paid" {
						ruleType = "tier"
						tier = map[string]any{"id": "t1", "type": "reward"}
						inc = append(inc, map[string]any{"id": "t1", "type": "reward", "attributes": map[string]any{"title": "Creator tier"}})
					}
					inc = append(inc, map[string]any{"id": "r1", "type": "access-rule", "attributes": map[string]any{"access_rule_type": ruleType}, "relationships": map[string]any{"tier": map[string]any{"data": tier}}})
					doc := map[string]any{"data": map[string]any{"id": "42", "attributes": map[string]any{"title": "Exact Title", "published_at": nil, "scheduled_for": nil, "patreon_url": "/creator/posts/exact-42", "post_type": "image_file"}, "relationships": map[string]any{"images": map[string]any{"data": refs}, "access_rules": map[string]any{"data": []any{map[string]any{"id": "r1", "type": "access-rule"}}}}}, "included": inc}
					docRaw, _ := json.Marshal(doc)
					editor := `<body><nav aria-label="Creator navigation"><button aria-label="Create post"></button></nav><a aria-label="Your page" href="https://www.patreon.com/c/creator">Creator</a><span>Saved</span><textarea aria-label="Title">Exact Title</textarea><div aria-label="Text input field for post content"><p>Exact first paragraph.</p><p>Exact second paragraph.</p></div>` + radio + `<input id="sell-post-toggle" aria-checked="false"><button data-tag="make-a-post-action-publish">Publish</button>` + menu + `<section><div><img data-tag="gallery-image" class="new-generated-class blurred-layer" src="https://different.example/rendered"></div><div data-tag="preview-thumbnail-container"><span><img alt="Preview" src="https://another.example/thumb"></span></div></section></body>`

					switch mode {
					case "wrong_creator":
						editor = strings.Replace(editor, "/c/creator", "/c/other", 1)
					case "wrong_title":
						editor = strings.Replace(editor, ">Exact Title</textarea>", ">Wrong Title</textarea>", 1)
					case "wrong_body":
						editor = strings.Replace(editor, "Exact second paragraph.", "Changed paragraph.", 1)
					case "wrong_audience":
						other := "paid"
						if audience == "paid" {
							other = "public"
						}
						editor = strings.Replace(editor, radio, fmt.Sprintf(`<input type="radio" value="%s" aria-checked="true">`, other), 1)
					case "wrong_tier":
						if audience == "paid" {
							editor = strings.Replace(editor, "Creator tier", "Other tier", 1)
						} else {
							editor = strings.Replace(editor, "</body>", `<div role="menuitemcheckbox" aria-checked="true">Unexpected tier</div></body>`, 1)
						}
					case "sale_on":
						editor = strings.Replace(editor, `id="sell-post-toggle" aria-checked="false"`, `id="sell-post-toggle" aria-checked="true"`, 1)
					}
					plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
						switch tool {
						case "computer.browser_open":
							current = stringFromAny(in["url"])
						case "computer.computer_use":
							switch in["action"] {
							case "navigate":
								current = stringFromAny(in["url"])
							case "click":
								if in["target_id"] == "publish" {
									commits++
								}
							case "screenshot":
								return map[string]any{"current_url": current, "som_revision": 1, "draft_save_state": "saved", "som": []any{map[string]any{"id": "tiers", "tag": "button", "text": "Select tiers", "accessible_name": "Select tiers", "effect": "open_configuration"}, map[string]any{"id": "publish", "tag": "button", "text": "Publish", "accessible_name": "Publish", "effect": "immediate_external_commit"}}}
							}
							return map[string]any{"current_url": current, "conditions_met": true, "matched": true}
						case "computer.browser_extract":
							h := editor
							if strings.Contains(current, "/api/") {
								h = "<pre>" + html.EscapeString(string(docRaw)) + "</pre>"
							}
							return map[string]any{"html": h, "rendered": true, "current_url": current}
						}
						return nil
					}
					ctx, a := newTestCtx(t, plat)
					var fixture map[string]any
					json.Unmarshal(raw, &fixture)
					fixture["browser"] = map[string]any{"context_id": "fixture-context"}
					rec := saveFixtureActor(t, ctx, a, fixture)
					input := map[string]any{"creator_url": "https://www.patreon.com/c/creator", "expected_creator_identity": "creator", "post_id": "42", "post_edit_url": "https://www.patreon.com/creator/posts/exact-42/edit", "post_title": "Exact Title", "post_body": "Exact first paragraph.\n\nExact second paragraph.", "audience": audience, "tier_labels": tiers, "request_id": "same-request", "expected_attachment_ids": expectedIDs, "expected_attachment_filenames": expectedNames, "expected_attachment_sizes": expectedSizes}
					q, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "publish_" + kind + "_draft_preflight", "input": input})
					if err != nil {
						t.Fatal(err)
					}
					queued, _ := claimActorRun(ctx)
					a.executeActorRun(context.Background(), ctx, queued)
					result, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
					if mode == "nested_blurred" {
						if result["status"] != "completed" {
							t.Fatal(result["error"])
						}
					} else {
						if result["status"] != "failed" {
							t.Fatal("bad attachments accepted", mode)
						}
						out := mapFromAny(result["output"])
						if out["failed_condition"] == nil || ((mode == "wrong" || mode == "extra" || mode == "reordered") && len(mapFromAny(out["document_evidence"])) == 0) {
							t.Fatal("failure diagnostics lost")
						}
					}
					if commits != 0 {
						t.Fatal("preflight committed")
					}
				})
			}
		}
	}
}

func TestNativePublishedURLRefreshesEarlierEditorEvidence(t *testing.T) {
	raw, _ := os.ReadFile("examples/patreon-native-image-drafts.json")
	var d actorDefinition
	json.Unmarshal(raw, &d)
	for _, kind := range []string{"image", "gallery"} {
		t.Run(kind, func(t *testing.T) {
			op := d.Operations["publish_"+kind+"_draft"]
			steps := op.Steps[len(op.Steps)-2:]
			if steps[0].Action != "observe_page" {
				t.Fatal("published observation must refresh URL")
			}
			p := newFakePlatform()
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool == "computer.browser_extract" {
					return map[string]any{"rendered": true, "current_url": "https://www.patreon.com/creator/posts/title-42?pr=true", "html": "<body>Your post is live and ready to share!</body>"}
				}
				if tool == "computer.computer_use" {
					return map[string]any{"current_url": "https://www.patreon.com/creator/posts/title-42?pr=true", "som": []any{}}
				}
				return nil
			}
			ctx, a := newTestCtx(t, p)
			selected := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"www.patreon.com"}, Steps: steps}
			b, _ := json.Marshal(selected)
			input := mustJSONString(map[string]any{"input": map[string]any{"expected_creator_identity": "creator", "post_id": "42"}})
			e, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{ID: 1, InputJSON: input, DefinitionSnapshot: string(b)})
			if err != nil {
				t.Fatal(err)
			}
			res, err := ctx.AppDB().Exec(`INSERT INTO actors_runs(project_id,kind,input_json,status) VALUES(?,?,?,'running')`, projectID(ctx), "actor", input)
			if err != nil {
				t.Fatal(err)
			}
			e.run.ID, _ = res.LastInsertId()
			e.session = &browserSession{SessionID: "fixture"}
			e.lastValues["current_url"] = "https://www.patreon.com/creator/posts/title-42/edit"
			if err := e.runStep(e.definition.Steps[0]); err != nil {
				t.Fatal(err)
			}
			if err := e.runStep(e.definition.Steps[1]); err != nil {
				t.Fatal(err)
			}
		})
	}
}
