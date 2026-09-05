//go:build !race

// Measure payload and query costs without race-instrumentation overhead.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSummaryReadPayloadAndSearchPlan(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	body := strings.Repeat("representative email text and markup ", 700)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		_, err = tx.Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,status,subject,body_text,body_html,headers) VALUES('test-proj','email','out','support@example.com','["alice@example.com"]','sent',?,?,?,?)`, fmt.Sprintf("Message %d unique%05d", i, i), body, "<p>"+body+"</p>", `{"x-trace":"`+strings.Repeat("h", 2000)+`"}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	full, _, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	small, _, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Limit: 50, Summary: true})
	if err != nil {
		t.Fatal(err)
	}
	fullJSON, _ := json.Marshal(full)
	smallJSON, _ := json.Marshal(small)
	if len(smallJSON)*10 >= len(fullJSON) {
		t.Fatalf("summary not substantially smaller: full=%d summary=%d", len(fullJSON), len(smallJSON))
	}
	start := time.Now()
	_, total, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Limit: 50, Summary: true, Q: "unique00042"})
	elapsed := time.Since(start)
	if err != nil || total != 1 {
		t.Fatalf("indexed lookup count=%d err=%v", total, err)
	}
	plan, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN SELECT rowid FROM message_search WHERE message_search MATCH '"unique00042"'`)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	indexed := false
	for plan.Next() {
		var id, parent, aux int
		var detail string
		if err := plan.Scan(&id, &parent, &aux, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "VIRTUAL TABLE INDEX")
	}
	if !indexed {
		t.Fatal("FTS virtual index was not used")
	}
	t.Logf("100 messages with ~50KB body+HTML each: 50-row JSON full=%d bytes summary=%d bytes (%.1f%% smaller); indexed unique-text query=%s", len(fullJSON), len(smallJSON), 100*(1-float64(len(smallJSON))/float64(len(fullJSON))), elapsed)
}
