package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Exercise the saved recipe against both Patreon navigation variants. A missing
// menu may be skipped only when the required empty editor and creator URL pass.
func TestPatreonComposerFlows(t *testing.T) {
	raw, err := os.ReadFile("examples/patreon-native-image-drafts.json")
	if err != nil {
		t.Fatal(err)
	}
	var template actorDefinition
	if err = json.Unmarshal(raw, &template); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"menu", "direct", "missing", "wrong_creator", "wrong_editor_creator", "existing_copy", "broken_menu"} {
		t.Run(mode, func(t *testing.T) {
			stage, creates, posts := "home", 0, 0
			current := "https://www.patreon.com/c/owner"
			dom := func() string {
				identity := "owner"
				if mode == "wrong_creator" {
					identity = "other"
				}
				h := `<body><a aria-label="Your page" href="https://www.patreon.com/cw/` + identity + `"></a>`
				if stage == "home" {
					h += `<nav aria-label="Creator navigation"><button aria-label="Create post">Create post</button></nav>`
				}
				if stage == "menu" {
					h += `<div role="menu"><button role="menuitem">Post</button></div>`
				}
				if stage == "editor" {
					copy := ""
					if mode == "existing_copy" {
						copy = "Do not overwrite me"
					}
					h += `<textarea aria-label="Title">` + copy + `</textarea><div aria-label="Text input field for post content"></div>`
				}
				return h + `</body>`
			}
			openEditor := func() {
				stage = "editor"
				current = "https://www.patreon.com/owner/posts/123/edit"
				if mode == "wrong_editor_creator" {
					current = "https://www.patreon.com/other/posts/123/edit"
				}
			}
			plat := newFakePlatform()
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_extract":
					return map[string]any{"html": dom(), "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "wait_for":
						b, _ := json.Marshal(in["conditions"])
						var conditions []actorWaitCondition
						json.Unmarshal(b, &conditions)
						root, _ := html.Parse(strings.NewReader(dom()))
						matched := in["match"] == "all"
						for _, c := range conditions {
							yes := readHas(root, c.Selector)
							if in["match"] == "all" {
								matched = matched && yes
							} else {
								matched = matched || yes
							}
						}
						return map[string]any{"matched": matched, "timed_out": !matched, "current_url": current}
					case "screenshot":
						var som []any
						if stage == "menu" {
							som = []any{map[string]any{"id": "post", "role": "menuitem", "accessible_name": "Post"}}
						}
						return map[string]any{"current_url": current, "som_revision": 1, "som": som}
					case "click":
						if in["selector"] != nil {
							creates++
							if mode == "menu" || mode == "broken_menu" {
								stage = "menu"
							} else if mode == "missing" {
								stage = "missing"
							} else {
								openEditor()
							}
						} else if in["target_id"] == "post" {
							posts++
							if mode != "broken_menu" {
								openEditor()
							}
						}
						return map[string]any{"current_url": current}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			def := template
			def.Browser.ContextID = ""
			def.Defaults = map[string]any{"creator_url": "https://www.patreon.com/c/owner", "expected_creator_identity": "owner"}
			steps := append([]actorStep{}, def.Operations["image_url_post"].Steps[:10]...)
			def.Operations = nil
			def.Steps = steps
			def.Recovery = nil
			def.OutputSchema = nil
			b, _ := json.Marshal(def)
			var value map[string]any
			json.Unmarshal(b, &value)
			rec := saveFixtureActor(t, ctx, app, value)
			if _, err = app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			queued, _ := claimActorRun(ctx)
			_ = app.executeActorRun(context.Background(), ctx, queued)
			run, err := getActorRun(ctx, queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := mode == "menu" || mode == "direct"
			if (run["status"] == "completed") != want {
				t.Fatalf("mode=%s run=%#v", mode, run)
			}
			if creates > 1 || posts > 1 {
				t.Fatalf("repeated opener creates=%d posts=%d", creates, posts)
			}
			if mode == "direct" && posts != 0 {
				t.Fatal("clicked Post in direct editor")
			}
			if mode == "wrong_creator" && creates != 0 {
				t.Fatal("created before creator guard")
			}
		})
	}
}
