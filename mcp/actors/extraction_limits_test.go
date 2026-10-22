package main

import (
	"context"
	"strings"
	"testing"
)

func TestLargeActorHTMLReadsRemainCompleteAndBounded(t *testing.T) {
	for _, mode := range []string{"raw", "crawl"} {
		for _, tooLarge := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/complete", true: "/over_cap"}[tooLarge], func(t *testing.T) {
				plat := newFakePlatform()
				plat.crawlHTMLMinimumLimit = 1_500_000
				plat.crawlHTMLAlwaysTruncated = tooLarge
				ctx, app := newTestCtx(t, plat)
				e := &actorExecution{app: app, ctx: ctx, workerCtx: context.Background(), session: &browserSession{SessionID: "large"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
				var doc *browserExtractResult
				var err error
				if mode == "raw" {
					doc, err = e.completeRawDOM()
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
