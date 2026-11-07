package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestPublicationURLsTrustedPlatformNormalization(t *testing.T) {
	for _, tc := range []struct {
		platform, raw, want string
		bad                 bool
	}{
		{"facebook", "/reel/123/", "https://www.facebook.com/reel/123/", false},
		{"facebook", "reel/123/?a=1&a=2#comment", "https://www.facebook.com/reel/123/?a=1&a=2#comment", false},
		{"facebook", "https://www.facebook.com/reel/123/?fbclid=keep&x=a%2Fb", "https://www.facebook.com/reel/123/?fbclid=keep&x=a%2Fb", false},
		{"facebook", "http://m.facebook.com/123", "http://m.facebook.com/123", false},
		{"facebook", "//www.facebook.com/reel/123/", "https://www.facebook.com/reel/123/", false},
		{"youtube", "https://youtu.be/video?t=2", "https://youtu.be/video?t=2", false},
		{"twitter", "/someone/status/9", "https://x.com/someone/status/9", false},
		{"pinterest", "/pin/4/", "https://www.pinterest.com/pin/4/", false},
		{"facebook", "#fragment", "", true},
		{"facebook", "//", "", true},
		{"facebook", "https://www.facebook.com/reel/1?access_token=secret", "", true},
		{"facebook", "", "", false}, {"facebook", "   ", "", false},
		{"facebook", "javascript:alert(1)", "", true}, {"facebook", "data:text/html,evil", "", true},
		{"facebook", "https://evil.test/reel/123/", "", true}, {"facebook", "//facebook.com.evil.test/x", "", true},
		{"facebook", "https://evil@facebook.com/x", "", true}, {"facebook", "https://facebook.com:999/x", "", true},
		{"facebook", "https://facebook.com./x", "", true}, {"facebook", "https://facebook.com/\\evil", "", true},
		{"facebook", "/reel/%zz", "", true}, {"facebook", " /reel/1", "", true}, {"facebook", "https://facebook.com/\n1", "", true},
		{"unknown", "/post/1", "", true},
	} {
		t.Run(tc.platform+":"+tc.raw, func(t *testing.T) {
			got, err := normalizePublicationURL(tc.platform, tc.raw)
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatal(got, err)
			}
		})
	}
}

func TestNativePublicationTimeMeaningAndTimezone(t *testing.T) {
	for _, tc := range []struct{ platform, provider, visibility, at, want, upload string }{
		{"facebook", "native", "public", "2026-10-01T11:00:00+02:00", "2026-10-01T09:00:00Z", ""},
		{"youtube", "native", "public", "2026-10-01T11:00:00+02:00", "2026-10-01T09:00:00Z", ""},
		{"youtube", "native", "private", "2026-10-01T11:00:00+02:00", "", "2026-10-01T09:00:00Z"},
		{"youtube", "native", "unlisted", "2026-10-01T11:00:00+02:00", "", "2026-10-01T09:00:00Z"},
		{"youtube", "native", "", "2026-10-01T09:00:00Z", "", "2026-10-01T09:00:00Z"},
		{"facebook", "zernio", "public", "2026-10-01T09:00:00Z", "", ""},
		{"tiktok", "native", "public", "", "", ""}, {"facebook", "native", "public", "2099-10-01T09:00:00Z", "", ""},
	} {
		t.Run(tc.platform+tc.provider+tc.visibility+tc.at, func(t *testing.T) {
			n := normalizeNativeObservation(historyAccount{destinationAccount: destinationAccount{Platform: tc.platform, Provider: tc.provider}}, nativePost{ID: "id", Visibility: optionalText(tc.visibility), PublishedAt: optionalText(tc.at)})
			if valueOrEmpty(n.PublishedAt) != tc.want || valueOrEmpty(n.UploadedAt) != tc.upload {
				t.Fatal(n)
			}
			if tc.provider == "zernio" && valueOrEmpty(n.ProviderPublishedAt) != tc.at {
				t.Fatal("provider date lost", n)
			}
		})
	}
}

type publicationRecordsPlatform struct {
	*recordingPlatform
	recordMu           sync.Mutex
	editorial, catalog map[string]any
	writes             map[string]int
	loseOnce           map[string]bool
}

func (p *publicationRecordsPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.recordMu.Lock()
	defer p.recordMu.Unlock()
	p.callAppCalls = append(p.callAppCalls, callAppCall{AppName: app, Tool: tool, Input: input})
	var result any
	switch tool {
	case "releases_get":
		result = p.editorial
	case "content_catalog_posts_get":
		result = map[string]any{"post": p.catalog}
	case "releases_update":
		if intArg(input, "revision", -1) != intArg(p.editorial, "revision", 0) {
			return errors.New("Editorial revision conflict")
		}
		for k, v := range input["patch"].(map[string]any) {
			p.editorial[k] = v
		}
		p.editorial["revision"] = intArg(p.editorial, "revision", 0) + 1
		p.writes[app]++
		result = p.editorial
	case "content_catalog_posts_record":
		if toString(input["post_id"]) != toString(p.catalog["id"]) || intArg(input, "expected_revision", -1) != intArg(p.catalog, "revision", 0) {
			return errors.New("Catalog revision conflict")
		}
		for k, v := range input {
			if k != "post_id" && k != "expected_revision" {
				p.catalog[k] = v
			}
		}
		p.catalog["revision"] = intArg(p.catalog, "revision", 0) + 1
		p.writes[app]++
		result = map[string]any{"post_id": p.catalog["id"], "revision": p.catalog["revision"], "post": p.catalog}
	default:
		return errors.New("unexpected record operation: " + app + "/" + tool)
	}
	if p.loseOnce[tool] {
		delete(p.loseOnce, tool)
		return errors.New("response lost after commit")
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}
func publicationReconcileFixture(t *testing.T) (*App, *sdk.AppCtx, *publicationRecordsPlatform, map[string]any) {
	t.Helper()
	base := newRecordingPlatform()
	base.identity.Bindings = map[string]any{"editorial": 17, "content-catalog": 18}
	base.executeResponses["facebook_get_post"] = analyticsOK(`{"id":"page-1_native","from":{"id":"page-1"},"is_published":true,"created_time":"2026-10-01T11:00:00+02:00","permalink_url":"/reel/123/?track=a%2Fb"}`)
	p := &publicationRecordsPlatform{recordingPlatform: base, writes: map[string]int{}, loseOnce: map[string]bool{}}
	ctx := newSocialCtx(t, p)
	for _, q := range []string{
		`INSERT INTO social_accounts(id,project_id,platform,connection_id,external_account_id,display_name,status,page_credentials) VALUES(1,'test-proj','facebook',42,'page-1','Page','active','{"access_token":"fixture"}')`,
		`INSERT INTO posts(id,project_id,body,status,revision,legacy_published_at,delivery_recorded_at) VALUES(10,'test-proj','Approved copy','published',3,'2026-10-01T08:00:00Z','2026-10-01T08:00:00Z')`,
		`INSERT INTO post_targets(id,post_id,social_account_id,status,attempts,platform_post_id,platform_url,legacy_published_at,delivery_recorded_at,submitted_at) VALUES(20,10,1,'published',1,'page-1_native','/reel/123/?track=a%2Fb','2026-10-01T08:00:00Z','2026-10-01T08:00:00Z','2026-10-01T07:59:00Z')`,
		`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name,status) VALUES(2,'test-proj','youtube',43,'YT','active')`,
		`INSERT INTO post_targets(id,post_id,social_account_id,status,attempts,platform_post_id,published_at,publication_time_source) VALUES(21,10,2,'published',1,'yt-native','2026-10-01T10:00:00Z','youtube.snippet.publishedAt:public')`,
	} {
		if _, err := ctx.AppDB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	p.editorial = map[string]any{"id": 7, "revision": 4, "app": "social", "external_id": 10, "status": "published", "channel": "Facebook", "published_at": "2026-10-01T08:00:00Z", "url": "/reel/123/?track=a%2Fb", "synced_at": "2026-10-01T08:01:00Z", "results": map[string]any{"confidence": "verified", "custom": "keep", "targets": []any{map[string]any{"id": 20, "social_account_id": 1, "platform": "facebook", "platform_post_id": "page-1_native", "platform_url": "/reel/123/?track=a%2Fb", "status": "published"}, map[string]any{"id": 21, "social_account_id": 2, "platform": "youtube", "platform_post_id": "yt-native", "status": "published", "published_at": "2026-10-01T10:00:00Z"}}}}
	p.catalog = map[string]any{"id": "catalog-post", "revision": 5, "destination": "facebook", "account_ref": "social:99:account:1", "external_post_id": "page-1_native", "external_url": "/reel/123/?track=a%2Fb", "status": "verified_published", "actual_at": "2026-10-01T08:00:00Z", "observed_at": "2026-10-01T08:01:00Z", "audience_type": "public", "group_scope": "unknown", "asset_ids": []any{"asset-1", "asset-2"}, "evidence_source": "Original verified evidence"}
	args := map[string]any{"post_id": 10, "target_id": 20, "social_account_id": 1, "expected_post_revision": 3, "expected_publication_revision": 0, "refresh_native": true, "records": []any{map[string]any{"app": "editorial", "record_id": "7", "expected_revision": 4}, map[string]any{"app": "content-catalog", "record_id": "catalog-post", "expected_revision": 5}}}
	return &App{}, ctx, p, args
}
func callPublicationReconcile(t *testing.T, a *App, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	out, err := a.toolPostPublicationReconcile(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}
func TestPublicationReconciliationSameRecordsAuditedNoPublishing(t *testing.T) {
	a, ctx, p, args := publicationReconcileFixture(t)
	preview := callPublicationReconcile(t, a, ctx, args)
	if preview["status"] != "preview" || len(p.writes) != 0 {
		t.Fatal(preview, p.writes)
	}
	beforeSibling := snapshotPublicationTarget(t, ctx, 21)
	args["apply"] = true
	out := callPublicationReconcile(t, a, ctx, args)
	if out["status"] != "complete" || p.writes["editorial"] != 1 || p.writes["content-catalog"] != 1 {
		t.Fatal(out, p.writes)
	}
	facts, err := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
	if err != nil {
		t.Fatal(err)
	}
	if valueOrEmpty(facts.URL) != "https://www.facebook.com/reel/123/?track=a%2Fb" || valueOrEmpty(facts.PublishedAt) != "2026-10-01T09:00:00Z" || valueOrEmpty(facts.DeliveryRecordedAt) != "2026-10-01T08:00:00Z" || facts.Revision != 1 {
		t.Fatal(facts)
	}
	if snapshotPublicationTarget(t, ctx, 21) != beforeSibling {
		t.Fatal("sibling was rewritten")
	}
	if p.catalog["id"] != "catalog-post" || p.catalog["status"] != "verified_published" || p.catalog["observed_at"] != "2026-10-01T08:01:00Z" || p.catalog["audience_type"] != "public" || len(p.catalog["asset_ids"].([]any)) != 2 {
		t.Fatal(p.catalog)
	}
	if p.catalog["external_url"] != valueOrEmpty(facts.URL) || p.catalog["actual_at"] != valueOrEmpty(facts.PublishedAt) || p.editorial["url"] != valueOrEmpty(facts.URL) || p.editorial["published_at"] != valueOrEmpty(facts.PublishedAt) {
		t.Fatal("inconsistent propagation", p.catalog, p.editorial)
	}
	if p.editorial["status"] != "published" || p.editorial["synced_at"] != "2026-10-01T08:01:00Z" || p.editorial["results"].(map[string]any)["confidence"] != "verified" {
		t.Fatal(p.editorial)
	}
	var old, new string
	if err := ctx.AppDB().QueryRow(`SELECT before_facts,after_facts FROM social_publication_corrections WHERE id=?`, out["correction_id"]).Scan(&old, &new); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(old, "/reel/123/") || !strings.Contains(old, "08:00:00Z") || !strings.Contains(new, "09:00:00Z") {
		t.Fatal(old, new)
	}
	// A new App instance simulates restart: replay reads the durable receipt.
	replay := callPublicationReconcile(t, &App{}, ctx, map[string]any{"post_id": 10, "target_id": 20, "social_account_id": 1, "apply": true, "correction_id": out["correction_id"]})
	if replay["status"] != "complete" || p.writes["editorial"] != 1 || p.writes["content-catalog"] != 1 {
		t.Fatal(replay, p.writes)
	}
	for _, c := range p.executeCalls {
		if c.Tool != "facebook_get_post" {
			t.Fatal("provider write during record repair", c)
		}
	}
	for _, c := range p.callAppCalls {
		if c.Tool != "releases_get" && c.Tool != "releases_update" && c.Tool != "content_catalog_posts_get" && c.Tool != "content_catalog_posts_record" {
			t.Fatal("unexpected app mutation", c)
		}
	}
}
func snapshotPublicationTarget(t *testing.T, ctx *sdk.AppCtx, id int64) string {
	t.Helper()
	var status, native, url, options, published, recorded, confirmed any
	var attempts int
	if err := ctx.AppDB().QueryRow(`SELECT status,platform_post_id,platform_url,options,published_at,delivery_recorded_at,audience_confirmed_at,attempts FROM post_targets WHERE id=?`, id).Scan(&status, &native, &url, &options, &published, &recorded, &confirmed, &attempts); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]any{status, native, url, options, published, recorded, confirmed, attempts})
	return string(raw)
}
func TestPublicationRecordRepairsResumeAfterLostResponses(t *testing.T) {
	for _, tool := range []string{"releases_update", "content_catalog_posts_record"} {
		t.Run(tool, func(t *testing.T) {
			a, ctx, p, args := publicationReconcileFixture(t)
			args["apply"] = true
			p.loseOnce[tool] = true
			out := callPublicationReconcile(t, a, ctx, args)
			if out["status"] != "records_pending" {
				t.Fatal(out)
			}
			out = callPublicationReconcile(t, &App{}, ctx, map[string]any{"post_id": 10, "target_id": 20, "social_account_id": 1, "apply": true, "correction_id": out["correction_id"]})
			if out["status"] != "complete" || p.writes["editorial"] != 1 || p.writes["content-catalog"] != 1 {
				t.Fatal(out, p.writes)
			}
		})
	}
}
func TestPublicationReconciliationRejectsStaleAndMismatchedRecords(t *testing.T) {
	for _, scenario := range []string{"post_revision", "facts_revision", "social_account", "project", "native_owner", "catalog_account", "catalog_native", "catalog_revision", "editorial_binding", "editorial_account", "editorial_revision"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, p, args := publicationReconcileFixture(t)
			args["apply"] = true
			switch scenario {
			case "post_revision":
				args["expected_post_revision"] = 2
			case "facts_revision":
				args["expected_publication_revision"] = 9
			case "social_account":
				args["social_account_id"] = 2
			case "project":
				ctx = ctx.WithProject("another-project")
			case "native_owner":
				p.executeResponses["facebook_get_post"] = analyticsOK(`{"id":"page-1_native","from":{"id":"someone-else"},"created_time":"2026-10-01T09:00:00Z"}`)
			case "catalog_account":
				p.catalog["account_ref"] = "social:99:account:2"
			case "catalog_native":
				p.catalog["external_post_id"] = "different-native"
			case "catalog_revision":
				p.catalog["revision"] = 6
			case "editorial_binding":
				p.editorial["external_id"] = 11
			case "editorial_account":
				p.editorial["results"].(map[string]any)["targets"].([]any)[0].(map[string]any)["social_account_id"] = 2
			case "editorial_revision":
				p.editorial["revision"] = 5
			}
			out := callPublicationReconcile(t, a, ctx, args)
			if strings.HasPrefix(scenario, "catalog") {
				if p.writes["content-catalog"] != 0 || out["status"] != "records_pending" {
					t.Fatal(out, p.writes)
				}
			} else if strings.HasPrefix(scenario, "editorial") {
				if p.writes["editorial"] != 0 || out["status"] != "records_pending" {
					t.Fatal(out, p.writes)
				}
			} else if len(p.writes) != 0 {
				t.Fatal(out, p.writes)
			}
		})
	}
}
func TestPublicationURLOnlyRepairPreservesIndependentTimes(t *testing.T) {
	a, ctx, p, args := publicationReconcileFixture(t)
	args["apply"], args["refresh_native"] = true, false
	out := callPublicationReconcile(t, a, ctx, args)
	if out["status"] != "complete" || p.catalog["actual_at"] != "2026-10-01T08:00:00Z" || p.editorial["published_at"] != "2026-10-01T08:00:00Z" {
		t.Fatal(out, p.catalog, p.editorial)
	}
	f, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
	if f.PublishedAt != nil || f.ObservedAt != nil {
		t.Fatal("URL normalization invented a time", f)
	}
}
func TestPublicationReconciliationConcurrentCAS(t *testing.T) {
	a, ctx, p, args := publicationReconcileFixture(t)
	args["apply"] = true
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.toolPostPublicationReconcile(ctx, args); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p.writes["editorial"] != 1 || p.writes["content-catalog"] != 1 {
		t.Fatal(p.writes)
	}
}
func TestPublicationFactsMigrationPreservesLegacyEvidence(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/upgrade.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, _ := os.ReadDir("migrations")
	for _, entry := range entries {
		if entry.Name() >= "031_publication_facts.sql" || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, _ := os.ReadFile("migrations/" + entry.Name())
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(entry.Name(), err)
		}
	}
	_, err = db.Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name) VALUES(1,'p','facebook',42,'Page');INSERT INTO posts(id,project_id,body,status,published_at) VALUES(1,'p','Keep','published','2026-10-01T08:00:00Z');INSERT INTO post_targets(id,post_id,social_account_id,status,platform_post_id,published_at) VALUES(1,1,1,'published','native','2026-10-01T08:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO posts(id,project_id,body,status,published_at,imported_at) VALUES(2,'p','Legacy imported','published','2018-01-01T08:00:00Z','2026-10-01T08:00:00Z');INSERT INTO post_targets(id,post_id,social_account_id,status,platform_post_id,published_at) VALUES(2,2,1,'published','imported-native','2018-01-01T08:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("migrations/031_publication_facts.sql")
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	f, err := loadPublicationFacts(db, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if f.PublishedAt != nil || valueOrEmpty(f.LegacyPublishedAt) != "2026-10-01T08:00:00Z" || valueOrEmpty(f.DeliveryRecordedAt) != "2026-10-01T08:00:00Z" {
		t.Fatal(f)
	}
	imported, err := loadPublicationFacts(db, "p", 2)
	if err != nil || imported.PublishedAt != nil || imported.DeliveryRecordedAt != nil || valueOrEmpty(imported.LegacyPublishedAt) != "2018-01-01T08:00:00Z" {
		t.Fatal("legacy upstream date was mistaken for local recording", imported, err)
	}
	var id, status string
	db.QueryRow(`SELECT platform_post_id,status FROM post_targets WHERE id=1`).Scan(&id, &status)
	if id != "native" || status != "published" {
		t.Fatal(id, status)
	}
}

func TestPublicationMissingAndScheduledTimesStayUnknown(t *testing.T) {
	for _, scenario := range []string{"missing", "scheduled", "processing"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, p, args := publicationReconcileFixture(t)
			args["apply"] = true
			switch scenario {
			case "missing":
				p.executeResponses["facebook_get_post"] = analyticsOK(`{"id":"page-1_native","from":{"id":"page-1"},"is_published":true,"permalink_url":"/reel/123/"}`)
			case "scheduled":
				ctx.AppDB().Exec(`UPDATE posts SET status='scheduled',schedule_at='2099-10-01T09:00:00Z' WHERE id=10`)
				ctx.AppDB().Exec(`UPDATE post_targets SET status='scheduled' WHERE id=20`)
				p.executeResponses["facebook_get_post"] = analyticsOK(`{"id":"page-1_native","from":{"id":"page-1"},"is_published":false,"created_time":"2026-10-01T09:00:00Z","permalink_url":"/reel/123/"}`)
			case "processing":
				n := normalizeNativeObservation(historyAccount{destinationAccount: destinationAccount{Platform: "youtube", Provider: "native"}}, nativePost{ID: "v", PublishedAt: optionalText("2026-10-01T09:00:00Z"), Visibility: optionalText("public"), ProcessingState: optionalText("processing")})
				if n.PublishedAt != nil {
					t.Fatal("processing invented publication", n)
				}
				return
			}
			currentFacts, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
			args["expected_publication_revision"] = currentFacts.Revision
			out := callPublicationReconcile(t, a, ctx, args)
			if out["status"] != "complete" {
				t.Fatal(out)
			}
			facts, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
			if facts.PublishedAt != nil || facts.ObservedAt == nil {
				t.Fatal(facts)
			}
			if p.catalog["actual_at"] != "2026-10-01T08:00:00Z" {
				t.Fatal("unknown native time replaced independently held date", p.catalog)
			}
			if scenario == "scheduled" {
				var status string
				ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=20`).Scan(&status)
				if status != "scheduled" {
					t.Fatal("record repair published a scheduled target")
				}
			}
		})
	}
}

func TestPrivateUploadAndAudienceConfirmationCannotDateMembership(t *testing.T) {
	a, ctx, p, args := publicationReconcileFixture(t)
	args["apply"] = true
	ctx.AppDB().Exec(`UPDATE social_accounts SET platform='youtube',external_account_id='UC-owned' WHERE id=1`)
	ctx.AppDB().Exec(`UPDATE post_targets SET status='awaiting_audience_setup',options='{"audience":{"type":"members"}}',platform_post_id='video-1',audience_status='awaiting_setup' WHERE id=20`)
	ctx.AppDB().Exec(`UPDATE posts SET status='awaiting_audience_setup' WHERE id=10`)
	p.executeResponses["get_my_channel"] = analyticsOK(`{"items":[{"id":"UC-owned","contentDetails":{"relatedPlaylists":{"uploads":"UU-owned"}}}]}`)
	p.executeResponses["get_video"] = analyticsOK(`{"items":[{"id":"video-1","snippet":{"channelId":"UC-owned","publishedAt":"2018-01-01T10:00:00Z"},"status":{"privacyStatus":"private","uploadStatus":"processed"}}]}`)
	currentFacts, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
	args["expected_publication_revision"] = currentFacts.Revision
	p.catalog["destination"], p.catalog["external_post_id"], p.catalog["audience_type"], p.catalog["access_policy"], p.catalog["actual_at"] = "youtube", "video-1", "restricted", "membership", "2026-10-02T17:00:00Z"
	p.editorial["channel"], p.editorial["published_at"] = "YouTube", "2026-10-02T17:00:00Z"
	target := p.editorial["results"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	target["platform"], target["platform_post_id"] = "youtube", "video-1"
	out := callPublicationReconcile(t, a, ctx, args)
	if out["status"] != "complete" {
		t.Fatal(out)
	}
	facts, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
	if facts.PublishedAt != nil || valueOrEmpty(facts.UploadedAt) != "2018-01-01T10:00:00Z" {
		t.Fatal(facts)
	}
	if p.catalog["actual_at"] != "2026-10-02T17:00:00Z" || p.editorial["published_at"] != "2026-10-02T17:00:00Z" {
		t.Fatal("private upload overwrote independent membership date", p.catalog, p.editorial)
	}
	// A prior public date, if present, is archived before audience confirmation
	// clears it; the confirmation's clock must not become native publication.
	ctx.AppDB().Exec(`UPDATE post_targets SET published_at='2019-01-01T10:00:00Z' WHERE id=20`)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/audiences/confirm", strings.NewReader(`{"post_id":10,"target_id":20,"expected_revision":3,"confirmed":true}`))
	a.handleAudienceConfirm(recorder, req)
	if recorder.Code != 200 {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	facts, _ = loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
	if facts.PublishedAt != nil || valueOrEmpty(facts.UploadedAt) != "2018-01-01T10:00:00Z" || valueOrEmpty(facts.DeliveryRecordedAt) != "2026-10-01T08:00:00Z" {
		t.Fatal("confirmation replaced publication or delivery time", facts)
	}
	var previous string
	ctx.AppDB().QueryRow(`SELECT before_facts FROM social_publication_corrections WHERE target_id=20 ORDER BY rowid DESC LIMIT 1`).Scan(&previous)
	if !strings.Contains(previous, "2019-01-01T10:00:00Z") {
		t.Fatal("old date lost", previous)
	}
}

func TestPublicationSubmissionAndAcceptanceAreSeparateFromCompletion(t *testing.T) {
	a, ctx, pf, _, target, _ := recoveryFixture(t, "facebook")
	ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing' WHERE id=?`, target)
	pf.executeResponses["native_publish"] = analyticsOK(`{"id":"new-native","accepted_at":"2026-10-01T10:00:00+02:00"}`)
	_, err := executePublicationWrite(ctx, publishJob{targetID: target, connID: 42, platform: "facebook", deliveryAttempt: 1}, "native_publish", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", target)
	if f.SubmittedAt == nil || valueOrEmpty(f.ProviderAcceptedAt) != "2026-10-01T08:00:00Z" || f.DeliveryRecordedAt != nil || f.PublishedAt != nil || f.ObservedAt != nil {
		t.Fatal(f)
	}
	_ = a
}

func TestDeliveryRecordingHistorySurvivesRetriesAndPagination(t *testing.T) {
	a, ctx, _, args := publicationReconcileFixture(t)
	// Start a new attempt: its current submission/acceptance are unknown again,
	// while the durable ledger retains the first attempt's known timestamps.
	ctx.AppDB().Exec(`UPDATE post_targets SET status='pending',platform_post_id=NULL,publish_operation_id='',provider_accepted_at='2026-10-01T08:00:00Z' WHERE id=20`)
	ctx.AppDB().Exec(`UPDATE posts SET status='partial' WHERE id=10`)
	claimed, err := claimPostTarget(ctx, 20)
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	f, _ := loadPublicationFacts(ctx.AppDB(), "test-proj", 20)
	if f.SubmittedAt != nil || f.ProviderAcceptedAt != nil || f.PublishedAt != nil {
		t.Fatal("new attempt inherited old clocks", f)
	}
	cursor := int64(0)
	seen := map[int64]bool{}
	foundOriginal := false
	for page := 0; page < 20; page++ {
		ledger, err := deliveryRecordingHistory(ctx, 20, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		rows := ledger["records"].([]map[string]any)
		for _, row := range rows {
			n := row["id"].(int64)
			if seen[n] {
				t.Fatal("duplicate ledger cursor", n)
			}
			seen[n] = true
			raw, _ := json.Marshal(row["facts"])
			if strings.Contains(string(raw), "2026-10-01T07:59:00Z") {
				foundOriginal = true
			}
		}
		next := ledger["next_after_id"]
		if next == nil {
			break
		}
		cursor = next.(int64)
	}
	if !foundOriginal || len(seen) < 3 {
		t.Fatal("original attempt evidence lost", seen)
	}
	// Inspection is still non-publishing, including its durable-history page.
	delete(args, "records")
	args["history_limit"] = 2
	if _, err := a.toolPostDeliveryInspect(ctx, map[string]any{"post_id": 10, "target_id": 20, "history_limit": 2}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationCorrectionCannotFollowAccountOrContentRebinding(t *testing.T) {
	for _, change := range []string{"account", "options"} {
		t.Run(change, func(t *testing.T) {
			a, ctx, p, args := publicationReconcileFixture(t)
			args["apply"] = true
			p.loseOnce["releases_update"] = true
			out := callPublicationReconcile(t, a, ctx, args)
			if out["status"] != "records_pending" {
				t.Fatal(out)
			}
			writes := p.writes["editorial"] + p.writes["content-catalog"]
			if change == "account" {
				ctx.AppDB().Exec(`UPDATE social_accounts SET connection_id=44 WHERE id=1`)
			} else {
				ctx.AppDB().Exec(`UPDATE post_targets SET options='{"body":"Changed without a revision"}' WHERE id=20`)
			}
			replay := callPublicationReconcile(t, &App{}, ctx, map[string]any{"post_id": 10, "target_id": 20, "social_account_id": 1, "apply": true, "correction_id": out["correction_id"]})
			if replay["isError"] != true || p.writes["editorial"]+p.writes["content-catalog"] != writes {
				t.Fatal("stale correction followed rebound identity", replay, p.writes)
			}
		})
	}
}

func TestDeliveryHistoryNormalizesLinksWithoutLosingMeaningfulQueries(t *testing.T) {
	_, ctx, _, _ := publicationReconcileFixture(t)
	history, err := deliveryRecordingHistory(ctx, 20, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	records := history["records"].([]map[string]any)
	if len(records) == 0 {
		t.Fatal("missing delivery history")
	}
	raw, _ := json.Marshal(records[0]["facts"])
	var facts map[string]any
	json.Unmarshal(raw, &facts)
	if facts["url"] != "https://www.facebook.com/reel/123/?track=a%2Fb" {
		t.Fatal("meaningful URL query was lost", facts)
	}
}
