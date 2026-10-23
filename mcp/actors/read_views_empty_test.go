package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A new creator has navigation and an explicit empty message, but no table
// header or pagination total. Existing fixtures always included #ready even
// for empty collections, masking the production failure.
func TestReadViewsEmptyLibrariesWithoutTableShell(t *testing.T) {
	for _, tc := range []struct {
		name          string
		posts, drafts int
		failure       string
		wantComplete  bool
	}{
		{name: "both_empty", wantComplete: true},
		{name: "empty_posts_existing_draft", drafts: 1, wantComplete: true},
		{name: "existing_posts_empty_drafts", posts: 2, wantComplete: true},
		{name: "existing_posts_and_drafts", posts: 2, drafts: 1, wantComplete: true},
		{name: "loading_empty_view", failure: "loading"},
		{name: "error_empty_view", failure: "error"},
		{name: "wrong_creator_empty_view", failure: "identity"},
		{name: "unverified_empty_view", failure: "unknown_empty"},
		{name: "wrong_empty_text", failure: "empty_text"},
		{name: "missing_observed_navigation", failure: "navigation"},
		{name: "wrong_target_url", failure: "url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plat := newFakePlatform()
			currentURL := ""
			visited := map[string]bool{}
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					currentURL = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						currentURL = stringFromAny(in["url"])
						visited[currentURL] = true
						if tc.failure == "url" && strings.HasSuffix(currentURL, "/posts") {
							currentURL = "https://example.com/login"
						}
						return map[string]any{"current_url": currentURL}
					case "scroll":
						return map[string]any{"current_url": currentURL, "scroll": map[string]any{"actual_target_id": "scroll_document"}}
					case "screenshot":
						return map[string]any{"som_revision": 1, "scroll_regions": []any{map[string]any{"id": "scroll_document", "name": "Document", "role": "document", "scroll_top": 0, "max_scroll_y": 0}}}
					}
				case "computer.browser_extract":
					h := `<body><a id="identity" href="https://example.com/creator">Creator</a>`
					if tc.failure != "navigation" {
						h += `<a href="/posts">Posts</a><a href="/drafts">Drafts</a>`
					}
					// The entry shell intentionally has neither target table nor
					// target empty marker. Each target must be checked separately.
					isTarget := strings.HasSuffix(currentURL, "/posts") || strings.HasSuffix(currentURL, "/drafts")
					if isTarget {
						n, marker, text := tc.posts, "posts-empty", "No posts yet"
						if strings.HasSuffix(currentURL, "/drafts") {
							n, marker, text = tc.drafts, "drafts-empty", "No drafts"
						}
						if n == 0 {
							if tc.failure != "unknown_empty" {
								if tc.failure == "empty_text" {
									text = "Temporarily unavailable"
								}
								h += fmt.Sprintf(`<h2 id="%s">%s</h2>`, marker, text)
							}
						} else {
							h += `<div id="ready"></div>`
							for i := 1; i <= n; i++ {
								id, status := i, "published"
								if strings.HasSuffix(currentURL, "/drafts") {
									id, status = i+10, "draft"
								}
								h += fmt.Sprintf(`<article data-id="%d" data-status="%s"><h2>Post %d</h2><a href="/posts/%d/edit">Edit</a></article>`, id, status, id, id)
							}
							h += fmt.Sprintf(`<footer>1 - %d of %d</footer>`, n, n)
						}
						if tc.failure == "loading" {
							h += `<div id="loading"></div>`
						}
						if tc.failure == "error" {
							h += `<div id="error">Unavailable</div>`
						}
						if tc.failure == "identity" {
							h = strings.Replace(h, "/creator", "/another-creator", 1)
						}
					}
					return map[string]any{"html": h + "</body>", "rendered": true, "current_url": currentURL}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			config := fixtureReadViews()
			config.Views[0].EmptySelector = "#posts-empty"
			config.Views[0].EmptyTextPattern = "^No posts yet$"
			config.Views[1].EmptySelector = "#drafts-empty"
			config.Views[1].EmptyTextPattern = "^No drafts$"
			for i := range config.Views {
				config.Views[i].Total = &actorField{Selector: "footer", Type: "number", Required: true, Pattern: `of (\d+)$`}
			}
			b, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			var conf map[string]any
			if err := json.Unmarshal(b, &conf); err != nil {
				t.Fatal(err)
			}
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"max_pages": 40, "max_items": 100, "max_duration_seconds": 60, "step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			run, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
				t.Fatal(err)
			}
			r, err := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if err != nil {
				t.Fatal(err)
			}
			out := r["output"].(map[string]any)
			coverage := out["coverage"].(map[string]any)
			if (r["status"] == "completed") != tc.wantComplete || coverage["inspection_complete"] != tc.wantComplete {
				t.Fatalf("status=%v error=%v coverage=%#v", r["status"], r["error"], coverage)
			}
			if tc.wantComplete {
				if got := intFromAny(out["item_count"]); got != tc.posts+tc.drafts {
					t.Fatalf("got %d rows, want %d", got, tc.posts+tc.drafts)
				}
				if !visited["https://example.com/posts"] || !visited["https://example.com/drafts"] {
					t.Fatalf("target views not independently visited: %v", visited)
				}
				if coverage["more_results_remaining"] != false {
					t.Fatal("successful empty view retained incomplete coverage")
				}
			} else if coverage["more_results_remaining"] != true {
				t.Fatal("failed read lost incomplete coverage")
			}
			for _, call := range plat.callsSnapshot() {
				if call.tool == "computer_use" {
					switch call.args["action"] {
					case "navigate", "scroll", "screenshot":
					default:
						t.Fatalf("unexpected consequential browser action: %v", call.args["action"])
					}
				}
			}
		})
	}
}
