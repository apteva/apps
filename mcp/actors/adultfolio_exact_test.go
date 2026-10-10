package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestEditableExactModelLookupGuards(t *testing.T) {
	for _, mode := range []string{"success", "wrong_account", "wrong_input", "wrong_name", "non_model", "ambiguous", "missing_selected", "selection_changed", "wrong_profile_id", "wrong_profile_name", "wrong_profile_url", "account_changed"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			current := ""
			selected := false
			typed := false
			visits := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "screenshot":
						name := "Studio Pro"
						if mode == "wrong_input" && typed {
							name = "Another"
						}
						return map[string]any{"current_url": current, "som_revision": "search-live", "som": []any{map[string]any{"id": "search", "tag": "input", "text": name}}}
					case "wait_for":
						return map[string]any{"matched": true, "current_url": current}
					case "type":
						typed = true
						if in["text"] != " " {
							t.Fatal("literal keyboard event lost")
						}
					case "key":
						if in["key"] == "ArrowDown" {
							selected = mode != "missing_selected"
						}
					case "click":
						if in["coordinate"] != nil {
							t.Fatal("coordinate lookup")
						}
						if in["selector"] != nil {
							if in["expected_effect"] != "navigation_only" || in["expected_text"] != "- model" || in["selector"] != "#eac-container-basic-url li.selected .eac-item > span" {
								t.Fatal("unguarded suggestion")
							}
							visits++
							current = "https://www.adultfolio.com/studio-profile"
						} else if in["target_id"] != "search" || in["som_revision"] != "search-live" {
							t.Fatal("unguarded search input focus")
						}
					}
					return map[string]any{"current_url": current}
				case "computer.browser_extract":
					account := "owner"
					if mode == "wrong_account" || (mode == "account_changed" && visits > 0) {
						account = "other"
					}
					h := `<html><body><div class="navbar"><a href="/` + account + `">Your Profile</a></div>`
					if visits == 0 {
						name, role, id := "Studio Pro", "model", "42"
						if mode == "wrong_name" {
							name = "Another"
						}
						if mode == "non_model" {
							role = "photographer"
						}
						if mode == "selection_changed" && selected {
							id = "43"
						}
						class := ""
						if selected {
							class = ` class="selected"`
						}
						row := `<li` + class + `><div class="eac-item"><img src="/images/thumbs/` + id + `.jpg"> ` + name + ` <span>- ` + role + `</span></div></li>`
						h += `<input id="basic-url" value=""><div id="eac-container-basic-url"><ul>` + row
						if mode == "ambiguous" {
							h += row
						}
						h += `</ul></div>`
					} else {
						id, name := "42", "Studio Pro"
						if mode == "wrong_profile_id" {
							id = "99"
						}
						if mode == "wrong_profile_name" {
							name = "Another"
						}
						if mode == "wrong_profile_url" {
							current = "https://www.adultfolio.com/message.php?user=42"
						}
						h += `<h1 class="MainTitle"><span id="online"></span> ` + name + ` <a class="MembershipLevelText">Premium Plus</a></h1><p>Female Model</p><button onclick="document.location='/message.php?user=` + id + `&amp;casting=&amp;search='">Message</button>`
					}
					return map[string]any{"html": h + `</body></html>`, "current_url": current, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, err := os.ReadFile("examples/adultfolio-control.json")
			if err != nil {
				t.Fatal(err)
			}
			var def map[string]any
			json.Unmarshal(raw, &def)
			def["browser"].(map[string]any)["context_id"] = ""
			rec := saveFixtureActor(t, ctx, app, def)
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "search_exact_model", "input": map[string]any{"account_url": "https://www.adultfolio.com/owner", "model_query": "Studio Pro", "max_duration_seconds": 60}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app.executeActorRun(context.Background(), ctx, run)
			r, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if (r["status"] == "completed") != (mode == "success") {
				t.Fatalf("%s: %v", mode, r["error"])
			}
			want := 0
			if mode == "success" || mode == "wrong_profile_id" || mode == "wrong_profile_name" || mode == "wrong_profile_url" || mode == "account_changed" {
				want = 1
			}
			if visits != want {
				t.Fatalf("visits=%d want=%d", visits, want)
			}
		})
	}
}
