package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLargeActorHTMLReadsRemainCompleteAndBounded(t *testing.T) {
	for _, mode := range []string{"raw", "crawl", "view"} {
		for _, tooLarge := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/complete", true: "/over_cap"}[tooLarge], func(t *testing.T) {
				plat := newFakePlatform()
				plat.crawlHTMLMinimumLimit = 1_500_000
				plat.crawlHTMLAlwaysTruncated = tooLarge
				if mode == "view" {
					plat.crawlHTMLMinimumLimit = 0
					plat.crawlHTMLAlwaysTruncated = false
					plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
						if tool != "computer.browser_extract" {
							return nil
						}
						return map[string]any{"html": readFixtureHTML(1), "rendered": true, "current_url": "https://example.com/posts", "truncated": tooLarge || intArg(in, "max_chars") < 1_500_000}
					}
				}
				ctx, app := newTestCtx(t, plat)
				e := &actorExecution{app: app, ctx: ctx, workerCtx: context.Background(), session: &browserSession{SessionID: "large"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
				var doc *browserExtractResult
				var err error
				if mode == "raw" {
					doc, err = e.completeRawDOM()
				} else if mode == "view" {
					record := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/posts"}}})
					if _, queueErr := app.toolActorRun(ctx, map[string]any{"actor_id": record.ID}); queueErr != nil {
						t.Fatal(queueErr)
					}
					run, claimErr := claimActorRun(ctx)
					if claimErr != nil {
						t.Fatal(claimErr)
					}
					e.run = run
					e.deadline = time.Now().Add(time.Minute)
					e.coverage = &actorReadCoverage{}
					e.currentURL = "https://example.com/posts"
					c := fixtureReadViews()
					root, readErr := e.readViewDOM(c, c.Views[0])
					err = readErr
					if root != nil {
						doc = &browserExtractResult{Rendered: true}
					}
				} else {
					doc, err = e.extractCrawlDOM()
				}
				if tooLarge {
					if err == nil || !strings.Contains(err.Error(), "5 MB") || doc != nil {
						t.Fatal("over-cap HTML accepted", err)
					}
				} else if err != nil || doc == nil || doc.Truncated {
					t.Fatal("complete HTML above the former cap rejected", err)
				}
				largest := 0
				for _, call := range plat.callsSnapshot() {
					limit := intArg(call.args, "max_chars")
					if limit > maxActorExtractChars {
						t.Fatal("request exceeds new cap", limit)
					}
					largest = max(largest, limit)
				}
				if (tooLarge && largest != maxActorExtractChars) || (!tooLarge && largest <= 1_000_000) {
					t.Fatal("new budget was not exercised", largest)
				}
			})
		}
	}
}
