package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Preserve verification semantics of historical immutable publishing definitions against
// realistic DOM evidence. No fill/upload step can hide incorrect saved data.
func TestPatreonHistoricalSavedDraftGuards(t *testing.T) {
	raw, err := os.ReadFile("testdata/patreon-publishing-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var template actorDefinition
	if err := json.Unmarshal(raw, &template); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"text_post", "image_post", "image_url_post", "gallery_post", "video_post", "poll_post"} {
		modes := []string{"success", "public_hidden_sale", "wrong_creator", "wrong_title", "wrong_body", "wrong_audience", "wrong_tier", "sale_on"}
		if strings.Contains(operation, "image") {
			modes = append(modes, "wrong_image", "extra_image")
		}
		if operation == "gallery_post" {
			modes = append(modes, "swapped_gallery", "missing_gallery", "extra_gallery")
		}
		if operation == "video_post" {
			modes = append(modes, "wrong_video", "video_error", "unsaved_video")
		}
		if operation == "poll_post" {
			modes = append(modes, "wrong_choice", "extra_choice")
		}
		for _, mode := range modes {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				title, body, identity := "Approved title", "<p>First paragraph</p><p>Second paragraph</p>", "owner"
				audience, sale, tier := "paid", "false", "Supporter"
				if mode == "public_hidden_sale" {
					audience = "public"
					tier = ""
				}
				if mode == "wrong_creator" {
					identity = "someone_else"
				}
				if mode == "wrong_title" {
					title = "approved title"
				}
				if mode == "wrong_body" {
					body = "<p>First paragraph Second paragraph</p>"
				}
				if mode == "wrong_audience" {
					audience = "public"
				}
				if mode == "wrong_tier" {
					tier = "Premium"
				}
				if mode == "sale_on" {
					sale = "true"
				}
				first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
				if mode == "wrong_image" {
					first = strings.Repeat("c", 32)
				}
				if mode == "swapped_gallery" {
					first, second = second, first
				}
				image := func(id string) string {
					return `<div class="editor__imageWrapper"><img class="media__blurred" src="https://c10.patreonusercontent.com/4/patreon-media/p/post/123/` + id + `/image.png"></div>`
				}
				media := image(first)
				if operation == "gallery_post" && mode != "missing_gallery" {
					media += image(second)
				}
				if mode == "extra_image" || mode == "extra_gallery" {
					media += image(strings.Repeat("c", 32))
				}
				poll1 := "Option one"
				if mode == "wrong_choice" {
					poll1 = "Other"
				}
				poll := `<input id="enterOption1-1" value="` + poll1 + `"><input id="enterOption1-2" value="Option two">`
				if mode == "extra_choice" {
					poll += `<input id="enterOption1-3" value="Unexpected">`
				}
				saleHTML := `<button id="sell-post-toggle" aria-checked="` + sale + `"></button>`
				if mode == "public_hidden_sale" {
					saleHTML = ""
					media = strings.ReplaceAll(media, "media__blurred", "media")
				}
				h := `<body><a aria-label="Your page" href="https://www.patreon.com/cw/` + identity + `"></a><textarea aria-label="Title">` + title + `</textarea><div aria-label="Text input field for post content">` + body + `</div><input type="radio" value="` + audience + `" aria-checked="true">` + saleHTML + `<div role="menuitemcheckbox" aria-checked="true">` + tier + `</div><div data-tag="image-layout">` + media + `</div>` + poll + `<img class="preview" src="https://c10.patreonusercontent.com/4/patreon-media/p/post/123/` + first + `/image.png"></body>`
				current := "https://www.patreon.com/" + identity + "/posts/123/edit"
				commits := 0
				plat := newFakePlatform()
				plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
					switch tool {
					case "computer.browser_open":
						return nil
					case "computer.browser_extract":
						return map[string]any{"html": h, "current_url": current, "rendered": true}
					case "computer.computer_use":
						if in["action"] == "screenshot" {
							video, status, save := "https://video.example/player/approved", "loaded", "saved"
							if mode == "wrong_video" {
								video = "https://video.example/player/other"
							}
							if mode == "video_error" {
								status = "error"
							}
							if mode == "unsaved_video" {
								save = "saving"
							}
							return map[string]any{"current_url": current, "som_revision": 1, "som": []any{map[string]any{"id": "publish", "tag": "button", "accessible_name": "Publish", "effect": "immediate_external_commit"}}, "media_iframe_src": video, "media_embed_status": status, "draft_save_state": save}
						}
						if in["action"] == "click" {
							if in["target_id"] != "publish" || in["coordinate"] != nil || in["confirm_consequence"] != "immediate_external_commit" {
								t.Fatal("unverified commit", in)
							}
							commits++
							return map[string]any{"current_url": current}
						}
					}
					return nil
				}
				ctx, app := newTestCtx(t, plat)
				def := template
				def.Browser.ContextID = ""
				def.Defaults = map[string]any{"creator_url": "https://www.patreon.com/c/owner", "expected_creator_identity": "owner", "tier_labels": []any{"Supporter"}}
				op := def.Operations[operation]
				// Keep the real verification steps after saved reload, plus identity checks.
				checks := append([]actorStep{}, op.Steps[2:4]...)
				start := 0
				for i, s := range op.Steps {
					if s.Action == "key" && s.Key == "CTRL+R" {
						start = i + 1
						break
					}
				}
				for _, s := range op.Steps[start:] {
					if s.Action == "extract" || s.Action == "observe_page" || s.Action == "assert_values" {
						checks = append(checks, s)
					}
					if s.OnceKey != "" {
						checks = append(checks, s)
						break
					}
				}
				def.Operations = nil
				def.Steps = checks
				def.Recovery = nil
				b, _ := json.Marshal(def)
				var value map[string]any
				json.Unmarshal(b, &value)
				rec := saveFixtureActor(t, ctx, app, value)
				desiredAudience := "paid"
				labels := []any{"Supporter"}
				if mode == "public_hidden_sale" {
					desiredAudience = "public"
					labels = []any{}
				}
				input := map[string]any{"post_title": "Approved title", "post_body": "First paragraph\n\nSecond paragraph", "audience": desiredAudience, "tier_labels": labels, "poll_option_1": "Option one", "poll_option_2": "Option two", "video_url": "https://video.example/player/approved", "request_id": "fixture"}
				if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": input}); err != nil {
					t.Fatal(err)
				}
				queued, _ := claimActorRun(ctx)
				e, err := newActorExecution(context.Background(), app, ctx, queued)
				if err != nil {
					t.Fatal(err)
				}
				if err := e.gotoURL(current); err != nil {
					t.Fatal(err)
				}
				e.lastValues["uploaded_image_id"] = strings.Repeat("a", 32)
				e.lastValues["uploaded_first_id"] = strings.Repeat("a", 32)
				e.lastValues["uploaded_second_id"] = strings.Repeat("b", 32)
				for _, s := range e.definition.Steps {
					if err = e.runStep(s); err != nil {
						break
					}
				}
				want := mode == "success" || mode == "public_hidden_sale"
				if (err == nil) != want || commits != map[bool]int{true: 1, false: 0}[want] {
					t.Fatal(fmt.Sprintf("err=%v commits=%d want_success=%t", err, commits, want))
				}
			})
		}
	}
}

func TestPatreonPreflightsStopBeforePublish(t *testing.T) {
	raw, err := os.ReadFile("testdata/patreon-publishing-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var d actorDefinition
	json.Unmarshal(raw, &d)
	for name, op := range d.Operations {
		if !strings.HasSuffix(name, "_preflight") {
			continue
		}
		for _, s := range op.Steps {
			if s.OnceKey != "" || s.ConfirmConsequence != "" || consequentialActorStep(s) {
				t.Fatal(name, "reaches a commit")
			}
		}
	}
}
