package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Single-page apps often render the view chrome (the ready selector) before
// the list or its empty state. Inspection must wait for rows or the verified
// empty state instead of judging the bare shell.
func TestReadViewsWaitForRowsOrEmptyStateAfterShell(t *testing.T) {
	for _, tc := range []struct {
		name         string
		shellReads   int // extracts of /posts that show only the shell
		postsRows    int
		wantComplete bool
	}{
		{name: "empty_state_after_shell", shellReads: 2, wantComplete: true},
		{name: "rows_after_shell", shellReads: 2, postsRows: 2, wantComplete: true},
		{name: "never_renders", shellReads: 1 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plat := newFakePlatform()
			currentURL := ""
			postsReads := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					currentURL = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						currentURL = stringFromAny(in["url"])
						return map[string]any{"current_url": currentURL}
					case "scroll":
						return map[string]any{"current_url": currentURL, "scroll": map[string]any{"actual_target_id": "scroll_document"}}
					case "screenshot":
						return map[string]any{"som_revision": 1, "scroll_regions": []any{map[string]any{"id": "scroll_document", "name": "Document", "role": "document", "scroll_top": 0, "max_scroll_y": 0}}}
					}
				case "computer.browser_extract":
					h := `<body><a id="identity" href="https://example.com/creator">Creator</a><div id="ready"></div><a href="/posts">Posts</a><a href="/drafts">Drafts</a>`
					switch {
					case strings.HasSuffix(currentURL, "/posts"):
						postsReads++
						if postsReads > tc.shellReads {
							if tc.postsRows == 0 {
								h += `<h2 id="posts-empty">No posts yet</h2>`
							}
							for i := 1; i <= tc.postsRows; i++ {
								h += `<article data-id="` + string(rune('0'+i)) + `" data-status="published"><h2>Post</h2><a href="/posts/1/edit">Edit</a></article>`
							}
						}
					case strings.HasSuffix(currentURL, "/drafts"):
						h += `<h2 id="drafts-empty">No drafts</h2>`
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
			config.TimeoutMS = 2000
			b, _ := json.Marshal(config)
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
			started := time.Now()
			if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(started)
			r, err := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if err != nil {
				t.Fatal(err)
			}
			coverage := r["output"].(map[string]any)["coverage"].(map[string]any)
			if (r["status"] == "completed") != tc.wantComplete || coverage["inspection_complete"] != tc.wantComplete {
				t.Fatalf("status=%v error=%v coverage=%#v", r["status"], r["error"], coverage)
			}
			if tc.wantComplete {
				if postsReads <= tc.shellReads {
					t.Fatalf("posts judged after %d reads, before the list rendered", postsReads)
				}
				if got := intFromAny(r["output"].(map[string]any)["item_count"]); got != tc.postsRows {
					t.Fatalf("rows = %d, want %d", got, tc.postsRows)
				}
				return
			}
			// A list that never renders is not a verified empty view: the run
			// fails, says the view did not load, and only after the timeout.
			views, _ := coverage["views"].([]any)
			msg := ""
			for _, v := range views {
				msg += stringFromAny(v.(map[string]any)["error"])
			}
			if !strings.Contains(msg, "did not finish loading") || !strings.Contains(msg, "no verified empty-state marker") {
				t.Fatalf("view error %q", msg)
			}
			if elapsed < 1500*time.Millisecond || postsReads < 2 {
				t.Fatalf("failed after %s and %d reads; expected to keep checking until the timeout", elapsed, postsReads)
			}
		})
	}
}
