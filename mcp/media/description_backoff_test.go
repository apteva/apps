package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestDescriptionRetryMetadata(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, value string
		absolute    bool
		want        time.Time
	}{
		{"seconds", "30", false, now.Add(30 * time.Second)},
		{"http date", now.Add(2 * time.Minute).Format("Mon, 02 Jan 2006 15:04:05 GMT"), false, now.Add(2 * time.Minute)},
		{"duration", "6m0s", false, now.Add(6 * time.Minute)},
		{"unix", fmt.Sprint(now.Add(time.Hour).Unix()), true, now.Add(time.Hour)},
		{"invalid", "tomorrow", false, time.Time{}},
		{"negative", "-1", false, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseDescriptionRetryTime(c.value, now, c.absolute); !got.Equal(c.want) {
				t.Fatalf("got=%s want=%s", got, c.want)
			}
		})
	}
	reset := now.Add(2 * time.Hour).Unix()
	res := &sdk.ExecuteResult{Status: 429, Headers: map[string]string{"Retry-After": "30", "X-RateLimit-Reset-Requests": "6m0s", "Set-Cookie": "secret"}, Data: json.RawMessage(fmt.Sprintf(`{"error":{"type":"usage_limit_reached","reset_at":%d}}`, reset))}
	info, until, limited := descriptionRateLimitInfo(res, nil, now)
	if !limited || info.RetryAfter != "30" || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("info=%+v until=%s limited=%v", info, until, limited)
	}
	raw, _ := json.Marshal(info)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("unrelated headers retained")
	}
	for _, err := range []error{errors.New("platform execute: http 429: busy"), errors.New("status=429"), errors.New("usage_limit_reached")} {
		if _, _, ok := descriptionRateLimitInfo(nil, err, now); !ok {
			t.Fatalf("429 not detected: %v", err)
		}
	}
	if _, _, ok := descriptionRateLimitInfo(&sdk.ExecuteResult{Status: 401, Data: json.RawMessage(`{"error":{"message":"bad key"}}`)}, nil, now); ok {
		t.Fatal("401 treated as rate limit")
	}
	_, until, limited = descriptionRateLimitInfo(nil, errors.New(fmt.Sprintf(`platform execute: HTTP 429: {"error":{"type":"usage_limit_reached","resets_at":%d}}`, reset)), now)
	if !limited || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("wrapped upstream reset lost: limited=%v until=%s", limited, until)
	}
}

func TestDescriptionBackoffPersistsIsolatesAndCapsFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := os.ReadFile("migrations/023_description_backoff.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= 6; i++ {
		info, err := recordDescriptionBackoff(db, 11, "chat_completion", "model-a", descriptionRetryInfo{Status: 429}, time.Time{}, now, 600)
		if err != nil {
			t.Fatal(err)
		}
		want := 600 * time.Second * time.Duration(1<<uint(i-1))
		if want > time.Hour {
			want = time.Hour
		}
		next, _ := time.Parse(time.RFC3339, info.NextAttemptAt)
		if next.Sub(now) != want {
			t.Fatalf("attempt %d delay=%s want=%s", i, next.Sub(now), want)
		}
	}
	reopened, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	active, err := descriptionBackoffActive(reopened, 11, "chat_completion", "model-a", now)
	if err != nil || !active {
		t.Fatalf("persistent active=%v err=%v", active, err)
	}
	for _, scope := range []struct {
		conn  int64
		model string
	}{{11, "model-b"}, {12, "model-a"}} {
		if active, err := descriptionBackoffActive(reopened, scope.conn, "chat_completion", scope.model, now); err != nil || active {
			t.Fatalf("scope=%+v active=%v err=%v", scope, active, err)
		}
	}
	info, err := recordDescriptionBackoff(db, 11, "chat_completion", "model-a", descriptionRetryInfo{Status: 429, RetryAfter: "7200"}, now.Add(2*time.Hour), now, 600)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := time.Parse(time.RFC3339, info.NextAttemptAt)
	if !next.Equal(now.Add(2 * time.Hour)) {
		t.Fatal("upstream hint shortened")
	}
	if active, err := descriptionBackoffActive(reopened, 11, "chat_completion", "model-a", next); err != nil || active {
		t.Fatal("expired cooldown remains active")
	}
}

func TestDescription429StopsLaterFilesAndClearsAfterSuccess(t *testing.T) {
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{Success: false, Status: 429, Headers: map[string]string{"Retry-After": "1200"}, Data: json.RawMessage(`{"error":{"message":"Too Many Requests","type":"rate_limit_exceeded"}}`)}
	app := newTestCtxWithPlatform(t, stub)
	for _, id := range []string{"1", "2"} {
		if err := upsertMedia(app.AppDB(), testProj, id, sampleAVProbe(3000), "sha", "/", "source.mp4"); err != nil {
			t.Fatal(err)
		}
		if err := upsertTranscript(app.AppDB(), &TranscriptRow{FileID: id, ProjectID: testProj, Status: "ok", Text: "spoken words"}); err != nil {
			t.Fatal(err)
		}
	}
	bound := app.IntegrationFor("descriptions")
	runOneDescription(app, bound, testProj, "1")
	runOneDescription(app, bound, testProj, "2")
	runOneDescription(app, bound, testProj, "1")
	if len(stub.ExecuteCalls) != 1 {
		t.Fatalf("provider hammered: %d calls", len(stub.ExecuteCalls))
	}
	first, _ := getMedia(app.AppDB(), testProj, "1")
	second, _ := getMedia(app.AppDB(), testProj, "2")
	if !strings.Contains(first.DescriptionError, `"retry_after":"1200"`) || !strings.Contains(first.DescriptionError, `"next_attempt_at"`) {
		t.Fatalf("metadata lost: %s", first.DescriptionError)
	}
	if second.DescriptionAttemptedAt != "" || second.DescriptionError != "" {
		t.Fatal("unattempted file marked failed")
	}
	if _, err := app.AppDB().Exec(`UPDATE description_backoff SET next_attempt_at=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK(`{"description":"A spoken scene.","audience_rating":"general","audience_reasoning":"Everyday dialogue."}`)}
	runOneDescription(app, bound, testProj, "2")
	var remaining int
	if err := app.AppDB().QueryRow(`SELECT COUNT(*) FROM description_backoff`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("backoff not reset: n=%d err=%v", remaining, err)
	}
	second, _ = getMedia(app.AppDB(), testProj, "2")
	if second.Description != "A spoken scene." {
		t.Fatalf("retry did not succeed: %+v", second)
	}
}
