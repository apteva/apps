package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
)

func retainedFixture(t *testing.T, db *sql.DB, scope, topic string, payload []byte, properties storage.MessageProperties, expiry int64) {
	t.Helper()
	raw, err := json.Marshal(properties)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO mqtt_retained(scope_id,topic,payload,qos,properties,expires_at) VALUES(?,?,?,2,?,?)`, scope, topic, payload, string(raw), expiry); err != nil {
		t.Fatal(err)
	}
}
func requireRetainedPage(t *testing.T, db *sql.DB, scope string, r retainedPageRequest, now int64) retainedPage {
	t.Helper()
	page, err := readRetainedPage(db, scope, r, now)
	if err != nil {
		t.Fatal(err)
	}
	return page
}
func requireReadError(t *testing.T, err error, status int) {
	t.Helper()
	var e *retainedReadError
	if !errors.As(err, &e) || e.status != status {
		t.Fatalf("want retained error %d, got %v", status, err)
	}
}
func TestRetainedPageCompletePayloadAndProperties(t *testing.T) {
	db := openTestDB(t)
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	binary := bytes.Repeat([]byte{0, 255, 1, 2, 128}, 3000)
	text := []byte(strings.Repeat("é state\n", 2000))
	props := storage.MessageProperties{PayloadFormat: 0, PayloadFormatFlag: true, ContentType: "application/octet-stream", ResponseTopic: root + "reply", CorrelationData: []byte{0, 255, 5}, MessageExpiryInterval: 120, User: []packets.UserProperty{{Key: "key", Val: "first"}, {Key: "key", Val: "second"}, {Key: "apteva.message_id", Val: "msg_fixture"}}}
	retainedFixture(t, db, "a", root+"binary", binary, props, now+120)
	retainedFixture(t, db, "a", root+"text ", text, storage.MessageProperties{}, 0)
	retainedFixture(t, db, "a", root+"zero", []byte{}, storage.MessageProperties{}, 0)
	page := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 100}, now)
	if len(page.Messages) != 3 || page.HasMore || page.NextCursor != "" {
		t.Fatal("incomplete page", page)
	}
	first := page.Messages[0]
	if first.PayloadBase64 == nil || *first.PayloadBase64 != base64.StdEncoding.EncodeToString(binary) || first.SizeBytes != len(binary) || first.QoS != 2 || !first.Retain {
		t.Fatal("binary payload was truncated")
	}
	if first.MessageID != "msg_fixture" || first.ExpiresAt != now+120 || first.Properties == nil || !bytes.Equal(first.Properties.CorrelationData, props.CorrelationData) || first.Properties.ResponseTopic != props.ResponseTopic || len(first.Properties.User) != 3 || !first.Properties.PayloadFormatFlag {
		t.Fatal("retained metadata was lost", first)
	}
	if page.Messages[1].Payload != string(text) {
		t.Fatal("large text was truncated")
	}
	raw, err := json.Marshal(page.Messages[2])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"payload_base64":""`)) {
		t.Fatal("empty binary-safe payload encoding omitted", string(raw))
	}
	get, err := getRetainedMessage(db, "a", root+"text ")
	if err != nil || get.Payload != string(text) || get.PayloadBase64 == nil {
		t.Fatal("single read incomplete", err)
	}
	// The old list deliberately keeps its preview contract.
	legacy, err := listRetainedMessages(db, "a", "", 10)
	if err != nil || legacy[0].PayloadBase64 != nil || legacy[0].SizeBytes != len(binary) {
		t.Fatal("legacy preview contract changed", err)
	}
}
func TestRetainedPageBeyondOneThousandAndScopeIsolation(t *testing.T) {
	db := openTestDB(t)
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	for i := range 1707 {
		retainedFixture(t, db, "a", root+fmt.Sprintf("item/%04d", i), []byte(fmt.Sprint(i)), storage.MessageProperties{}, 0)
	}
	retainedFixture(t, db, "b", scopeTopicRoot("b")+"private", []byte("secret"), storage.MessageProperties{}, 0)
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 1000, Cursor: cursor}, now)
		pages++
		for _, m := range page.Messages {
			if seen[m.Topic] || !scopedTopic("a", m.Topic) {
				t.Fatal("duplicate/foreign topic", m.Topic)
			}
			seen[m.Topic] = true
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor was not cleared")
			}
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatal("cursor failed to progress")
		}
		cursor = page.NextCursor
		if pages > 4 {
			t.Fatal("pagination loop")
		}
	}
	if len(seen) != 1707 || pages != 2 {
		t.Fatal("list did not reach every retained topic", len(seen), pages)
	}
}
func TestRetainedPageSparseFilterEmptyPagesAndCursorBinding(t *testing.T) {
	db := openTestDB(t)
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	for i := range 1200 {
		retainedFixture(t, db, "a", root+fmt.Sprintf("aaa/%04d", i), []byte("skip"), storage.MessageProperties{}, 0)
	}
	for i := range 2 {
		retainedFixture(t, db, "a", root+fmt.Sprintf("zzz/%d", i), []byte("match"), storage.MessageProperties{}, 0)
	}
	filter := root + "zzz/+"
	first := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 100, TopicPattern: filter}, now)
	if len(first.Messages) != 0 || !first.HasMore || first.Scanned != retainedScanLimit {
		t.Fatal("empty filtered page hid more matches", first)
	}
	second := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 1, Cursor: first.NextCursor}, now)
	if len(second.Messages) != 1 || !second.HasMore || second.TopicPattern != filter {
		t.Fatal("sparse filter failed to resume", second)
	}
	last := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 100, Cursor: second.NextCursor}, now)
	if len(last.Messages) != 1 || last.HasMore || last.Messages[0].Topic == second.Messages[0].Topic {
		t.Fatal("sparse result skipped/duplicated")
	}
	_, err := readRetainedPage(db, "b", retainedPageRequest{Limit: 100, Cursor: first.NextCursor}, now)
	requireReadError(t, err, 400)
	_, err = readRetainedPage(db, "a", retainedPageRequest{Limit: 100, Cursor: first.NextCursor, TopicPattern: root + "#"}, now)
	requireReadError(t, err, 400)
	_, err = readRetainedPage(db, "a", retainedPageRequest{Limit: 100, TopicPattern: scopeTopicRoot("b") + "#"}, now)
	requireReadError(t, err, 400)
	token := []byte(first.NextCursor)
	token[10] ^= 1
	_, err = readRetainedPage(db, "a", retainedPageRequest{Limit: 100, Cursor: string(token)}, now)
	requireReadError(t, err, 400)
	_, err = readRetainedPage(db, "a", retainedPageRequest{Limit: 100, Cursor: first.NextCursor}, now+retainedCursorTTL)
	requireReadError(t, err, 410)
	_, err = readRetainedPage(db, "a", retainedPageRequest{Limit: 100, Cursor: first.NextCursor}, now-1)
	requireReadError(t, err, 410)
}
func TestRetainedPageResponseBudgetNeverSkipsWholeRecords(t *testing.T) {
	db := openTestDB(t)
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	payload := bytes.Repeat([]byte{0, 255}, 1<<19)
	for i := range 3 {
		retainedFixture(t, db, "a", root+fmt.Sprint(i), payload, storage.MessageProperties{}, 0)
	}
	cursor := ""
	count := 0
	for {
		page := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 1000, Cursor: cursor}, now)
		if len(page.Messages) != 1 || page.Messages[0].PayloadBase64 == nil || *page.Messages[0].PayloadBase64 != base64.StdEncoding.EncodeToString(payload) {
			t.Fatal("byte budget truncated/skipped a record")
		}
		count++
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
		if count > 3 {
			t.Fatal("loop")
		}
	}
	if count != 3 {
		t.Fatal(count)
	}
	// A single record larger than the target page budget is returned whole.
	retainedFixture(t, db, "large", scopeTopicRoot("large")+"one", bytes.Repeat([]byte{0}, 3<<20), storage.MessageProperties{}, 0)
	big := requireRetainedPage(t, db, "large", retainedPageRequest{Limit: 10}, now)
	if len(big.Messages) != 1 || big.Messages[0].SizeBytes != 3<<20 || big.HasMore {
		t.Fatal("oversized record was not complete")
	}
}
func TestRetainedPageTopicOrderingWhitespaceAndExpiry(t *testing.T) {
	db := openTestDB(t)
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	topics := []string{"a", "A", "a ", "a//b", "a/../b", "é", "e\u0301", "quote\"", "slash\\", "控制"}
	for _, topic := range topics {
		retainedFixture(t, db, "a", root+topic, []byte(topic), storage.MessageProperties{}, 0)
	}
	retainedFixture(t, db, "a", root+"expired", []byte("hidden"), storage.MessageProperties{}, now-1)
	retainedFixture(t, db, "a", root+"boundary", []byte("hidden"), storage.MessageProperties{}, now)
	sorted := append([]string(nil), topics...)
	sort.Strings(sorted)
	cursor := ""
	seen := []string{}
	for {
		page := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 2, Cursor: cursor}, now)
		for _, m := range page.Messages {
			seen = append(seen, strings.TrimPrefix(m.Topic, root))
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(sorted, "|") != strings.Join(seen, "|") {
		t.Fatal("keyset did not preserve exact binary topic order", seen)
	}
	if _, err := getRetainedMessage(db, "a", root+"expired"); err == nil {
		t.Fatal("expired exact read exposed a record")
	}
	old, err := listRetainedMessages(db, "a", "", 100)
	if err != nil || len(old) != len(topics) {
		t.Fatal("legacy list exposed expired records", len(old), err)
	}
	filtered := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 100, TopicPattern: root + "a/#"}, now)
	if len(filtered.Messages) != 3 {
		t.Fatal("MQTT parent/# or empty levels lost", filtered)
	}
	app := &App{ctx: disklessContextForRetainedTest(t, db)}
	out, err := app.toolRetainedGet(app.ctx.WithProject("a"), map[string]any{"topic": root + "a "})
	if err != nil || out.(*retainedMessage).Topic != root+"a " {
		t.Fatal("tool trimmed exact topic", err)
	}
}
func disklessContextForRetainedTest(t *testing.T, db *sql.DB) *sdk.AppCtx {
	t.Helper()
	manifest, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	return sdk.NewAppCtxForTest(manifest, db, sdk.Config{}, nil, nil)
}
func TestRetainedPageCursorSurvivesRestartAndInitializationRace(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	path := filepath.Join(t.TempDir(), "mqtt.db")
	ctx := diskContext(t, path)
	now := time.Now().Unix()
	root := scopeTopicRoot("a")
	retainedFixture(t, ctx.AppDB(), "a", root+"one", []byte("one"), storage.MessageProperties{}, 0)
	retainedFixture(t, ctx.AppDB(), "a", root+"two", []byte("two"), storage.MessageProperties{}, 0)
	var wg sync.WaitGroup
	keys := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := retainedCursorKey(ctx.AppDB())
			if err != nil {
				t.Error(err)
				return
			}
			keys <- base64.StdEncoding.EncodeToString(key)
		}()
	}
	wg.Wait()
	close(keys)
	expected := ""
	for key := range keys {
		if expected == "" {
			expected = key
		}
		if key != expected {
			t.Fatal("cursor initialization raced")
		}
	}
	page := requireRetainedPage(t, ctx.AppDB(), "a", retainedPageRequest{Limit: 1}, now)
	if err := ctx.AppDB().Close(); err != nil {
		t.Fatal(err)
	}
	reopened := diskContext(t, path)
	next := requireRetainedPage(t, reopened.AppDB(), "a", retainedPageRequest{Limit: 1, Cursor: page.NextCursor}, now+1)
	if len(next.Messages) != 1 || next.Messages[0].Topic != root+"two" || next.HasMore {
		t.Fatal("restart invalidated retained cursor")
	}
	encoded, _ := json.Marshal(page)
	if bytes.Contains(encoded, []byte(expected)) {
		t.Fatal("cursor signing key was exposed")
	}
}
func TestRetainedPageLiveScanJournalReconciliation(t *testing.T) {
	a, _, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	for _, topic := range []string{"one", "two"} {
		if err := a.broker.Publish(root+topic, []byte(topic), true, 1); err != nil {
			t.Fatal(err)
		}
	}
	first := requireRetainedPage(t, a.ctx.AppDB(), "a", retainedPageRequest{Limit: 1}, now)
	if first.StartSequence != "2" || first.HighSequence != "2" || first.Consistency != "live" {
		t.Fatal("missing reconciliation watermark", first)
	}
	// Insert behind the cursor and delete a topic already read; replay reconciles both.
	if err := a.broker.Publish(root+"aaa", []byte("new"), true, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.broker.DeleteRetained(root + "one"); err != nil {
		t.Fatal(err)
	}
	second := requireRetainedPage(t, a.ctx.AppDB(), "a", retainedPageRequest{Limit: 100, Cursor: first.NextCursor}, now)
	if second.StartSequence != first.StartSequence || second.HighSequence != "4" || len(second.Messages) != 1 || second.Messages[0].Topic != root+"two" {
		t.Fatal("live boundary or watermark changed", second)
	}
	consumerCall(t, a, "app:reconcile", map[string]any{"action": "create", "name": "state", "topic_filter": root + "#", "start": "after", "after_sequence": first.StartSequence})
	batch := consumerCall(t, a, "app:reconcile", map[string]any{"action": "claim", "name": "state"})
	records := batch["messages"].([]durableMessage)
	if len(records) != 2 || !records[0].Retain || records[0].Topic != root+"aaa" || records[1].Payload != "" || records[1].Topic != root+"one" {
		t.Fatal("journal could not reconcile retained changes", records)
	}
}
func TestRetainedPageHTTPMCPValidationAndCompatibility(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a, ctx := ownershipApp(t)
	root := scopeTopicRoot("a")
	for _, topic := range []string{"one", "two"} {
		retainedFixture(t, ctx.AppDB(), "a", root+topic, []byte(strings.Repeat("full", 2000)), storage.MessageProperties{}, 0)
	}
	retainedFixture(t, ctx.AppDB(), "b", scopeTopicRoot("b")+"secret", []byte("foreign"), storage.MessageProperties{}, 0)
	response := appRequest(t, a, "GET", "/retained/page?limit=1&scope_id=b", "", "a")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var first retainedPage
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 1 || !first.HasMore || first.Messages[0].PayloadBase64 == nil {
		t.Fatal(first)
	}
	body, _ := json.Marshal(map[string]any{"cursor": first.NextCursor, "limit": 1, "scope_id": "b"})
	response = appRequest(t, a, "POST", "/retained/page", string(body), "a")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var next retainedPage
	json.Unmarshal(response.Body.Bytes(), &next)
	if len(next.Messages) != 1 || next.Messages[0].Topic != root+"two" {
		t.Fatal(next)
	}
	response = appRequest(t, a, "GET", "/retained/page?cursor="+url.QueryEscape(first.NextCursor), "", "b")
	if response.Code != 400 {
		t.Fatal("foreign cursor accepted", response.Code)
	}
	response = appRequest(t, a, "GET", "/retained", "", "a")
	if response.Code != 200 || !strings.HasPrefix(response.Body.String(), "[") {
		t.Fatal("legacy array contract changed")
	}
	var tool sdk.Tool
	for _, candidate := range a.MCPTools() {
		if candidate.Name == "mqtt_retained_page" {
			tool = candidate
		}
	}
	out, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "a", AppInstallID: 1}), ctx, map[string]any{"limit": 1, "scope_id": "b"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(retainedPage).Messages[0].Topic != root+"one" {
		t.Fatal("MCP accepted a caller-selected scope")
	}
	for _, query := range []string{"limit=0", "limit=1001", "limit=no", "limit=1&limit=2", "cursor=%ZZ"} {
		response = appRequest(t, a, "GET", "/retained/page?"+query, "", "a")
		if response.Code != 400 {
			t.Fatal("invalid query accepted", query, response.Code)
		}
	}
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"limit":1.5}`, `{"cursor":1}`, `{"topic_pattern":null}`} {
		response = appRequest(t, a, "POST", "/retained/page", body, "a")
		if response.Code != 400 {
			t.Fatal("invalid page request accepted", body, response.Code)
		}
	}
	if response = appRequest(t, a, "DELETE", "/retained/page", "", "a"); response.Code != 405 {
		t.Fatal(response.Code)
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	if response = appRequest(t, a, "GET", "/retained/page", "", "a"); response.Code != 403 {
		t.Fatal("tokenless retained reads accepted")
	}
}
func TestRetainedPageMetadataErrorsSurface(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a, ctx := ownershipApp(t)
	root := scopeTopicRoot("a")
	retainedFixture(t, ctx.AppDB(), "a", root+"corrupt", []byte("body"), storage.MessageProperties{}, 0)
	if _, err := ctx.AppDB().Exec(`UPDATE mqtt_retained SET properties='broken'`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/retained/page", "/retained/" + url.PathEscape(root+"corrupt")} {
		response := appRequest(t, a, "GET", path, "", "a")
		if response.Code != 503 {
			t.Fatal("storage error hidden as empty/not-found", path, response.Code)
		}
	}
	if _, err := (&retainedHook{app: a}).StoredRetainedMessages(); err == nil {
		t.Fatal("restart silently discarded invalid properties")
	}
}

func TestRetainedPageLongestTopicsUsePOSTCursor(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a, ctx := ownershipApp(t)
	root := scopeTopicRoot("a")
	parent := root + strings.Repeat("\x01", 65535-len(root)-2)
	retainedFixture(t, ctx.AppDB(), "a", parent, []byte("parent"), storage.MessageProperties{}, 0)
	retainedFixture(t, ctx.AppDB(), "a", parent+"/b", []byte("child"), storage.MessageProperties{}, 0)
	filter := parent + "/#"
	request, _ := json.Marshal(map[string]any{"topic_pattern": filter, "limit": 1})
	response := appRequest(t, a, "POST", "/retained/page", string(request), "a")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var first retainedPage
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 1 || first.Messages[0].Topic != parent || !first.HasMore || first.NextCursor == "" {
		t.Fatal("long-topic first page failed")
	}
	request, _ = json.Marshal(map[string]any{"topic_pattern": filter, "cursor": first.NextCursor, "limit": 1})
	response = appRequest(t, a, "POST", "/retained/page", string(request), "a")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var second retainedPage
	if err := json.Unmarshal(response.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 1 || second.Messages[0].Topic != parent+"/b" || second.HasMore {
		t.Fatal("long cursor could not resume")
	}
}
func TestRetainedPageExpiryBetweenPagesAndReadOnlyQuotas(t *testing.T) {
	a, _, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false", "quota_defaults_json": `{"accepted_messages":1}`})
	root := scopeTopicRoot("a")
	now := time.Now().Unix()
	if err := a.broker.Publish(root+"aaa", []byte("one"), true, 1); err != nil {
		t.Fatal(err)
	}
	retainedFixture(t, a.ctx.AppDB(), "a", root+"bbb", []byte("expires"), storage.MessageProperties{MessageExpiryInterval: 10}, now+10)
	retainedFixture(t, a.ctx.AppDB(), "a", root+"ccc", []byte("last"), storage.MessageProperties{}, 0)
	first := requireRetainedPage(t, a.ctx.AppDB(), "a", retainedPageRequest{Limit: 1}, now)
	second := requireRetainedPage(t, a.ctx.AppDB(), "a", retainedPageRequest{Limit: 100, Cursor: first.NextCursor}, now+20)
	if len(second.Messages) != 1 || second.Messages[0].Topic != root+"ccc" {
		t.Fatal("expired retained record leaked into next page")
	}
	if value := usageValue(t, a, "a", "accepted_messages", 0); value != 1 {
		t.Fatal("read consumed acceptance quota", value)
	}
	if _, err := getRetainedMessage(a.ctx.AppDB(), "a", root+"aaa"); err != nil {
		t.Fatal("quota exhaustion blocked exact read", err)
	}
}

func TestRetainedPageMigrationBackfillsLegacyExpiry(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.Contains(path, "006_") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	root := scopeTopicRoot("a")
	created := time.Now().Add(-time.Hour).Unix()
	if _, err = db.Exec(`INSERT INTO mqtt_retained(scope_id,topic,payload,properties,updated_at) VALUES('a',?,'expired','{"messageExpiry":10}',datetime(?,'unixepoch')),('a',?,'keep','{}',CURRENT_TIMESTAMP),('a',?,'bad','broken',CURRENT_TIMESTAMP)`, root+"expired", created, root+"keep", root+"invalid"); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/006_retained_reads.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var expiry int64
	if err = db.QueryRow(`SELECT expires_at FROM mqtt_retained WHERE topic=?`, root+"expired").Scan(&expiry); err != nil || expiry != created+10 {
		t.Fatal("legacy expiry was not backfilled", expiry, err)
	}
	if _, err = getRetainedMessage(db, "a", root+"expired"); err == nil {
		t.Fatal("expired legacy retained value was exposed")
	}
	page := requireRetainedPage(t, db, "a", retainedPageRequest{Limit: 100, TopicPattern: root + "keep"}, time.Now().Unix())
	if len(page.Messages) != 1 || page.Messages[0].ExpiresAt != 0 {
		t.Fatal("non-expiring legacy data changed")
	}
	var count int
	if err = db.QueryRow(`SELECT messages FROM mqtt_retained_usage WHERE scope_id='a'`).Scan(&count); err != nil || count != 3 {
		t.Fatal("expiry migration changed storage accounting", count, err)
	}
}
