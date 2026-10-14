package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func audiencePost(t *testing.T, a *App, ctxArgs func(map[string]any) (any, error), asset, platform, account, external, kind, scope string) map[string]any {
	t.Helper()
	args := map[string]any{"asset_ids": []string{asset}, "destination": platform, "account_ref": account, "external_post_id": external, "external_url": "https://example.com/" + external, "status": "verified_published", "evidence_source": "creator_page_verified", "audience_type": kind, "group_scope": scope}
	if kind == "restricted" {
		args["access_policy"] = "membership"
	}
	if scope == "selected" {
		args["group_ids"] = []string{"gold"}
		args["group_labels"] = map[string]string{"gold": "Provider Gold"}
	}
	r, err := ctxArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	return r.(map[string]any)
}
func TestPublicationAudienceUnknownDatesReconcileAndAudit(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	args := map[string]any{"asset_ids": []string{asset}, "destination": "youtube", "account_ref": "yt-channel", "external_post_id": "native-1", "status": "verified_published", "evidence_source": "Studio screenshot", "audience_type": "restricted", "access_policy": "membership", "group_scope": "unknown", "publisher_ref": map[string]any{"app": "social", "install_id": 52, "record_id": "video-1"}}
	first, err := a.postsRecord(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	id := first.(map[string]any)["post_id"].(string)
	p, err := postByID(ctx.AppDB(), "project-a", id)
	if err != nil {
		t.Fatal(err)
	}
	if p.ActualAt != "" || p.ObservedAt == "" || p.GroupScope != "unknown" || len(p.GroupIDs) != 0 || p.Revision != 1 || p.PublisherRef.App != "social" {
		t.Fatal(p)
	}
	replay, err := a.postsRecord(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out := replay.(map[string]any)
	if out["post_id"] != id || out["unchanged"] != true || out["reconciled"] != true {
		t.Fatal(out)
	}
	var records, events int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&records)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_events`).Scan(&events)
	if records != 1 || events != 1 {
		t.Fatal(records, events)
	}
	change := map[string]any{"post_id": id, "status": "verified_published", "group_scope": "selected", "group_ids": []string{"gold"}, "group_labels": map[string]string{"gold": "Provider Gold"}}
	if _, err = a.postsRecord(ctx, change); err == nil {
		t.Fatal("unguarded audience edit")
	}
	change["expected_revision"] = int64(1)
	if _, err = a.postsRecord(ctx, change); err == nil {
		t.Fatal("reused previous evidence")
	}
	change["evidence_source"] = "Provider tier confirmation"
	if _, err = a.postsRecord(ctx, change); err != nil {
		t.Fatal(err)
	}
	change["group_ids"] = []string{"silver"}
	change["group_labels"] = map[string]string{"silver": "Provider Silver"}
	if _, err = a.postsRecord(ctx, change); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatal("stale audience write", err)
	}
	detail, err := a.postsGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	history := detail.(map[string]any)["history"].([]map[string]any)
	if len(history) != 2 {
		t.Fatal(history)
	}
	before := history[0]["before"].(map[string]any)
	after := history[0]["after"].(map[string]any)
	if before["group_scope"] != "unknown" || after["group_scope"] != "selected" || before["actual_at"] != "" || after["actual_at"] != "" {
		t.Fatal(history)
	}
	p, _ = postByID(ctx.AppDB(), "project-a", id)
	if p.Revision != 2 || p.GroupIDs[0] != "gold" {
		t.Fatal(p)
	}
	// Preserve the first external identity when new observations/retries arrive.
	args["group_scope"] = "selected"
	args["group_ids"] = []string{"gold"}
	args["group_labels"] = map[string]string{"gold": "Provider Gold"}
	args["evidence_source"] = "Provider tier confirmation"
	args["expected_revision"] = int64(1)
	if r, err := a.postsRecord(ctx, args); err != nil || r.(map[string]any)["unchanged"] != true {
		t.Fatal("identical guarded retry", r, err)
	}
}
func TestPublicationAudienceAvailabilityMatchesHistoryAndUnknownScope(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	public := attach(t, a, ctx, session, 1)
	unknown := attach(t, a, ctx, session, 2)
	selected := attach(t, a, ctx, session, 3)
	all := attach(t, a, ctx, session, 4)
	for _, id := range []string{public, unknown, selected, all} {
		if _, err := a.assetReview(ctx, map[string]any{"asset_id": id, "review_status": "approved"}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(args map[string]any) (any, error) { return a.postsRecord(ctx, args) }
	publicRecord := audiencePost(t, a, call, public, "youtube", "channel", "public", "public", "unknown")
	audiencePost(t, a, call, unknown, "youtube", "channel", "unknown", "restricted", "unknown")
	audiencePost(t, a, call, selected, "youtube", "channel", "selected", "restricted", "selected")
	audiencePost(t, a, call, all, "youtube", "channel", "all", "restricted", "all_eligible")
	args := map[string]any{"entity_type": "assets", "destination": "youtube", "account_ref": "channel", "audience_type": "restricted", "access_policy": "membership", "availability": "published"}
	hits := assetHits(t, a, ctx, args)
	if len(hits) != 3 {
		t.Fatal(hits)
	}
	args["group_id"] = "gold"
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 2 {
		t.Fatal("unknown treated as all", hits)
	}
	for _, h := range hits {
		if h.ID == unknown || len(h.Uses) != 1 || h.Uses[0].AudienceType != "restricted" || h.Uses[0].ObservedAt == "" {
			t.Fatal(h)
		}
	}
	args["availability"] = "ready_to_publish"
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 1 || hits[0].ID != public {
		t.Fatal("unknown overlap automatically ready / public counts as members", hits)
	}
	args["group_id"] = "silver"
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 2 {
		t.Fatal("selected unrelated group should be available", hits)
	}
	args["destination"] = "patreon"
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 4 {
		t.Fatal("youtube blocks patreon", hits)
	}
	args["destination"] = "youtube-short"
	args["availability"] = "published"
	if len(assetHits(t, a, ctx, args)) != 0 {
		t.Fatal("platform prefix matching")
	}
	// Changing current audience cannot erase or misattribute earlier proof.
	id := publicRecord["post_id"].(string)
	if _, err := a.postsRecord(ctx, map[string]any{"post_id": id, "expected_revision": 1, "status": "planned", "audience_type": "restricted", "access_policy": "membership", "group_scope": "unknown"}); err != nil {
		t.Fatal(err)
	}
	args = map[string]any{"entity_type": "assets", "destination": "youtube", "account_ref": "channel", "audience_type": "public", "availability": "published"}
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 1 || hits[0].ID != public {
		t.Fatal("public history lost", hits)
	}
	args["audience_type"] = "restricted"
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 3 {
		t.Fatal("old public proof reassigned to current membership", hits)
	}
	if _, err := a.postsRecord(ctx, map[string]any{"post_id": id, "status": "removed", "evidence_source": "removal check"}); err != nil {
		t.Fatal(err)
	}
	args["audience_type"] = "public"
	hits = assetHits(t, a, ctx, args)
	if len(hits) != 1 {
		t.Fatal("removed history lost")
	}
	args["availability"] = "never_used"
	for _, h := range assetHits(t, a, ctx, args) {
		if h.ID == public {
			t.Fatal("historical public use reported never used")
		}
	}

}
func TestPublicationAudienceValidationIsolationAndRollback(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	other := attach(t, a, ctx, session, 2)
	call := func(args map[string]any) (any, error) { return a.postsRecord(ctx, args) }
	first := audiencePost(t, a, call, asset, "youtube", "one", "same", "public", "unknown")
	audiencePost(t, a, call, asset, "youtube", "two", "same", "public", "unknown")
	audiencePost(t, a, call, asset, "patreon", "one", "same", "public", "unknown")
	listed, err := a.postsList(ctx, map[string]any{"destination": "youtube", "account_ref": "one", "audience_type": "public", "external_post_id": "same"})
	if err != nil || len(listed.(map[string]any)["posts"].([]Publication)) != 1 {
		t.Fatal(listed, err)
	}
	foreign := ctx.WithProject("other-project")
	if _, err := a.postsRecord(foreign, map[string]any{"post_id": first["post_id"], "status": "unknown", "evidence_source": "check"}); err == nil {
		t.Fatal("cross-project post edit")
	}
	id := first["post_id"].(string)
	before, _ := postByID(ctx.AppDB(), "project-a", id)
	var events int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_events`).Scan(&events)
	if _, err = ctx.AppDB().Exec(`CREATE TRIGGER fail_audience_audit BEFORE INSERT ON post_events BEGIN SELECT RAISE(ABORT,'forced audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{"post_id": id, "expected_revision": 1, "status": "verified_published", "audience_type": "restricted", "access_policy": "membership", "group_scope": "selected", "group_ids": []string{"gold"}, "evidence_source": "New confirmed audience"}
	if _, err = a.postsRecord(ctx, update); err == nil {
		t.Fatal("missing audit didn't roll back")
	}
	after, _ := postByID(ctx.AppDB(), "project-a", id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial update", before, after)
	}
	ctx.AppDB().Exec(`DROP TRIGGER fail_audience_audit`)
	if _, err = a.postsRecord(ctx, map[string]any{"destination": "youtube", "account_ref": "one", "external_post_id": "same", "asset_ids": []string{other}, "status": "verified_published", "evidence_source": "check"}); err == nil {
		t.Fatal("verified asset identity replaced")
	}
	base := map[string]any{"destination": "test", "account_ref": "account", "asset_ids": []string{asset}, "status": "planned", "audience_type": "restricted"}
	for _, bad := range []map[string]any{{"audience_type": "members"}, {"group_scope": "all"}, {"group_scope": "selected", "group_ids": []string{}}, {"group_scope": "unknown", "group_ids": []string{"gold"}}, {"audience_type": "public", "access_policy": "membership"}, {"group_scope": "selected", "group_ids": []string{"gold", "gold"}}, {"group_scope": "selected", "group_ids": []string{"gold"}, "group_labels": map[string]string{"silver": "Wrong provider label"}}, {"observed_at": "not-a-date"}, {"publisher_ref": map[string]any{"app": "social"}}} {
		args := map[string]any{}
		for k, v := range base {
			args[k] = v
		}
		for k, v := range bad {
			args[k] = v
		}
		if _, err = a.postsRecord(ctx, args); err == nil {
			t.Fatal("accepted invalid audience", bad)
		}
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_events`).Scan(&count)
	if count != events {
		t.Fatal("rejected mutations wrote events")
	}
	for _, args := range []map[string]any{{"group_id": "gold"}, {"destination": "youtube", "group_id": "gold"}, {"audience_type": "bogus"}, {"account_ref": "one"}} {
		if _, err = a.search(ctx, args); err == nil {
			t.Fatal("unscoped/invalid filter", args)
		}
	}
}
func TestPublicationListsPaginationAndLegacyDuplicates(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	call := func(args map[string]any) (any, error) { return a.postsRecord(ctx, args) }
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		audiencePost(t, a, call, asset, "generic", "account", id, "restricted", "all_eligible")
	}
	args := map[string]any{"destination": "generic", "account_ref": "account", "group_id": "tier", "limit": int64(2)}
	seen := map[string]bool{}
	pages := 0
	for {
		r, err := a.postsList(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		o := r.(map[string]any)
		for _, p := range o["posts"].([]Publication) {
			if seen[p.ID] {
				t.Fatal("duplicate page")
			}
			seen[p.ID] = true
		}
		pages++
		cursor := o["next_cursor"].(string)
		if cursor == "" {
			break
		}
		args["cursor"] = cursor
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatal(seen, pages)
	}
	listed, err := a.assetPublicationsList(ctx, map[string]any{"asset_id": asset, "destination": "generic", "account_ref": "account", "audience_type": "public"})
	if err != nil || len(listed.(map[string]any)["publications"].([]Publication)) != 0 {
		t.Fatal(listed, err)
	}
	// Retain pre-existing duplicate records and reject ambiguous reconciliation.
	ctx.AppDB().Exec(`INSERT INTO posts(id,project_id,brand_id,destination,account_ref,external_post_id) SELECT 'legacy-copy',project_id,brand_id,destination,account_ref,external_post_id FROM posts LIMIT 1`)
	var ext string
	ctx.AppDB().QueryRow(`SELECT external_post_id FROM posts WHERE id='legacy-copy'`).Scan(&ext)
	_, err = a.postsRecord(ctx, map[string]any{"destination": "generic", "account_ref": "account", "external_post_id": ext, "asset_ids": []string{asset}, "status": "verified_published", "evidence_source": "new check"})
	if err == nil || !strings.Contains(err.Error(), "legacy duplicates") {
		t.Fatal(err)
	}
	// Wire results carry explicit unknown/default fields and read-only metadata.
	r, err := a.search(ctx, map[string]any{"entity_type": "assets", "destination": "generic", "account_ref": "account", "group_id": "tier", "availability": "published"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"audience_type":"restricted"`) || !strings.Contains(string(b), `"observed_at"`) {
		t.Fatal(string(b))
	}
}

func TestPublicationAudienceMigrationPreservesLegacyRecords(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() >= "013" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("migrations", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(b)); err != nil {
			t.Fatal(file.Name(), err)
		}
	}
	for _, statement := range []string{`INSERT INTO brands(id,project_id,slug,name,storage_root) VALUES('brand','p','brand','Brand','/')`, `INSERT INTO sessions(id,project_id,brand_id,title,session_date) VALUES('session','p','brand','Session','')`, `INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name) VALUES('asset','p','session',16,'file-1','Original')`, `INSERT INTO posts(id,project_id,brand_id,destination,account_ref,audience,status,actual_at,external_post_id,evidence_source) VALUES('post1','p','brand','youtube','account','members','verified_published','','native','screenshot'),('post2','p','brand','youtube','account','members','verified_published','','native','screenshot')`, `INSERT INTO post_assets(project_id,post_id,asset_id,position) VALUES('p','post1','asset',0),('p','post2','asset',0)`, `INSERT INTO post_events(id,project_id,post_id,status,evidence_source,observed_at) VALUES('event','p','post1','verified_published','screenshot','2023-01-01T00:00:00Z')`} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile("migrations/013_publication_audiences.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&count)
	if count != 2 {
		t.Fatal("legacy duplicates deleted")
	}
	db.QueryRow(`SELECT COUNT(*) FROM post_external_identities`).Scan(&count)
	if count != 0 {
		t.Fatal("ambiguous identity silently chosen")
	}
	p, err := postByID(db, "p", "post1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Audience != "members" || p.AudienceType != "unknown" || p.GroupScope != "unknown" || p.ActualAt != "" || p.ObservedAt != "" || p.AssetIDs[0] != "asset" {
		t.Fatal("legacy values inferred or lost", p)
	}
	var audience, recorded string
	db.QueryRow(`SELECT audience_type,recorded_at FROM post_events WHERE id='event'`).Scan(&audience, &recorded)
	if audience != "unknown" || recorded != "2023-01-01T00:00:00Z" {
		t.Fatal("history rewritten")
	}
}
func TestPublicationDetailHTTPAndAccountScopedFilters(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	call := func(args map[string]any) (any, error) { return a.postsRecord(ctx, args) }
	r := audiencePost(t, a, call, asset, "generic", "account", "post", "restricted", "selected")
	previous := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = previous }()
	rec := httptest.NewRecorder()
	a.handlePost(rec, httptest.NewRequest("GET", "/posts/"+r["post_id"].(string)+"?project_id=project-a", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"group_scope":"selected"`) || !strings.Contains(rec.Body.String(), `"history"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.handleSearch(rec, httptest.NewRequest("GET", "/search?project_id=project-a&entity_type=assets&destination=generic&account_ref=account&audience_type=restricted&access_policy=membership&group_id=gold&availability=published", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), asset) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.handleList("content_catalog_posts_list")(rec, httptest.NewRequest("GET", "/posts?project_id=project-a&destination=generic&account_ref=account&group_id=gold&limit=1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next_cursor"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestUnknownAccountBlocksReadinessUntilExplicitReconciliation(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	a.assetReview(ctx, map[string]any{"asset_id": asset, "review_status": "approved"})
	r, err := a.postsRecord(ctx, map[string]any{"asset_ids": []string{asset}, "destination": "generic", "external_post_id": "native", "status": "verified_published", "evidence_source": "Legacy evidence"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"entity_type": "assets", "destination": "generic", "account_ref": "one", "audience_type": "restricted", "access_policy": "membership", "group_id": "gold", "availability": "ready_to_publish"}
	if len(assetHits(t, a, ctx, args)) != 0 {
		t.Fatal("unknown account advertised ready")
	}
	update := map[string]any{"post_id": r.(map[string]any)["post_id"], "account_ref": "two", "status": "verified_published"}
	if _, err = a.postsRecord(ctx, update); err == nil {
		t.Fatal("unknown identity filled unguarded")
	}
	update["expected_revision"] = 1
	if _, err = a.postsRecord(ctx, update); err == nil {
		t.Fatal("unknown identity filled without new proof")
	}
	update["evidence_source"] = "Provider account confirmed"
	if _, err = a.postsRecord(ctx, update); err != nil {
		t.Fatal(err)
	}
	if len(assetHits(t, a, ctx, args)) != 1 {
		t.Fatal("known other account blocks this account")
	}
	update["account_ref"] = "one"
	update["expected_revision"] = 2
	if _, err = a.postsRecord(ctx, update); err == nil {
		t.Fatal("known verified identity reassigned")
	}
}
