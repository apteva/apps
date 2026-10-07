package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func reportProfile(t *testing.T, ctx *sdk.AppCtx, name string) *Profile {
	t.Helper()
	out, err := (&App{}).toolProfileCreate(ctx, map[string]any{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["profile"].(*Profile)
}

func reportPost(t *testing.T, ctx *sdk.AppCtx, project string, profile int64, status, date string, accounts ...int64) int64 {
	t.Helper()
	r, err := ctx.AppDB().Exec(`INSERT INTO posts(project_id, profile_id, body, status, created_at)
		VALUES (?, ?, 'Reporting fixture', ?, ?)`, project, profile, status, date)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	for _, account := range accounts {
		if _, err := ctx.AppDB().Exec(`INSERT INTO post_targets(post_id, social_account_id, status) VALUES (?, ?, 'pending')`, id, account); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func reportList(t *testing.T, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	out, err := (&App{}).toolPostList(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["isError"] == true {
		t.Fatalf("post_list failed: %+v", result)
	}
	return result
}

func TestPostReporting_Reconciles74Posts45Records60Targets(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	ctx.AppDB().SetMaxOpenConns(1)
	profile := reportProfile(t, ctx, "HGV")
	accounts := []int64{widgetAccountFixture(t, ctx, "test-proj", profile.ID), widgetAccountFixture(t, ctx, "test-proj", profile.ID)}
	// Project-wide recent window: 155 other posts + 45 HGV posts (60
	// targets). The remaining 29 older HGV posts fall outside that window.
	for range 155 {
		reportPost(t, ctx, "test-proj", 0, "published", "2026-10-03T10:00:00Z")
	}
	for i := range 74 {
		date := "2026-10-02T10:00:00Z"
		if i >= 45 {
			date = "2026-09-01T10:00:00Z"
		}
		targets := accounts[:1]
		if i < 15 {
			targets = accounts
		}
		reportPost(t, ctx, "test-proj", profile.ID, "published", date, targets...)
	}
	window := reportList(t, ctx, map[string]any{"limit": 200})
	hgvPosts, hgvTargets := 0, 0
	for _, post := range window["posts"].([]map[string]any) {
		if post["profile_id"] == profile.ID {
			hgvPosts++
			hgvTargets += len(post["targets"].([]map[string]any))
		}
	}
	if hgvPosts != 45 || hgvTargets != 60 || window["total"] != 229 || window["has_more"] != true || window["next_offset"] != 200 {
		t.Fatalf("recent window: posts=%d targets=%d metadata=%+v", hgvPosts, hgvTargets, window)
	}
	for _, filter := range []map[string]any{{"profile": profile.Slug, "limit": 200}, {"profile_id": profile.ID, "limit": 200}} {
		full := reportList(t, ctx, filter)
		if len(full["posts"].([]map[string]any)) != 74 || full["total"] != 74 || full["total_targets"] != 89 || full["returned_targets"] != 89 || full["has_more"] != false || full["next_offset"] != nil {
			t.Fatalf("profile-scoped result did not reconcile: %+v", full)
		}
	}
	out, err := (&App{}).toolProfileList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counted := out.(map[string]any)["profiles"].([]Profile)[0]
	if counted.PostCount != 74 || counted.TargetCount != 89 {
		t.Fatalf("profile counts: %+v", counted)
	}
}

func TestPostReporting_PagesBeyond200WithoutDuplicatesOrOmissions(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	profile := reportProfile(t, ctx, "Large history")
	account := widgetAccountFixture(t, ctx, "test-proj", profile.ID)
	ids := make([]int64, 0, 413)
	// Equal timestamps exercise the ID tie-breaker across pages.
	for range 413 {
		ids = append(ids, reportPost(t, ctx, "test-proj", profile.ID, "published", "2026-10-01T10:00:00Z", account))
	}
	seen := map[int64]bool{}
	offset, pages, targets := 0, 0, 0
	for {
		out := reportList(t, ctx, map[string]any{"profile_id": profile.ID, "limit": 999, "offset": offset})
		posts := out["posts"].([]map[string]any)
		if out["limit"] != 200 || out["offset"] != offset || out["total"] != 413 || out["total_targets"] != 413 {
			t.Fatalf("incorrect page metadata: %+v", out)
		}
		for i, post := range posts {
			id := post["id"].(int64)
			if seen[id] || id != ids[412-offset-i] {
				t.Fatalf("duplicate or wrong order at offset %d: %d", offset+i, id)
			}
			seen[id] = true
		}
		targets += out["returned_targets"].(int)
		pages++
		if out["has_more"] == false {
			if out["next_offset"] != nil || len(posts) != 13 {
				t.Fatalf("invalid last page: %+v", out)
			}
			break
		}
		next := out["next_offset"].(int)
		if next <= offset || len(posts) != 200 {
			t.Fatalf("pagination did not advance: %+v", out)
		}
		offset = next
	}
	if len(seen) != 413 || targets != 413 || pages != 3 {
		t.Fatalf("incomplete traversal: posts=%d targets=%d pages=%d", len(seen), targets, pages)
	}
	if out := reportList(t, ctx, nil); len(out["posts"].([]map[string]any)) != 50 || out["next_offset"] != 50 {
		t.Fatalf("default listing changed: %+v", out)
	}
	if out := reportList(t, ctx, map[string]any{"limit": 0}); out["limit"] != 1 {
		t.Fatalf("minimum limit changed: %+v", out)
	}
	for _, end := range []int{413, 999, math.MaxInt} {
		out := reportList(t, ctx, map[string]any{"offset": end})
		if len(out["posts"].([]map[string]any)) != 0 || out["total"] != 413 || out["returned_targets"] != 0 || out["has_more"] != false || out["next_offset"] != nil {
			t.Fatalf("out-of-range page: %+v", out)
		}
	}
}

func TestPostReporting_FilteredTotalsAndTrustedProject(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	p := reportProfile(t, ctx, "Scoped")
	other := reportProfile(t, ctx, "Other")
	account := widgetAccountFixture(t, ctx, "test-proj", p.ID)
	one := reportPost(t, ctx, "test-proj", p.ID, "published", "2026-10-01T00:00:00Z", account)
	two := reportPost(t, ctx, "test-proj", p.ID, "published", "2026-10-02T00:00:00Z", account)
	reportPost(t, ctx, "test-proj", p.ID, "published", "2026-10-03T00:00:00Z", account) // excluded upper bound
	reportPost(t, ctx, "test-proj", p.ID, "draft", "2026-10-01T00:00:00Z")
	reportPost(t, ctx, "test-proj", other.ID, "published", "2026-10-01T00:00:00Z", account)
	// Legacy cross-project associations must not inflate counts.
	reportPost(t, ctx, "foreign-project", p.ID, "published", "2026-10-02T00:00:00Z", account)
	args := map[string]any{"profile_id": p.ID, "status": "published", "from": "2026-10-01T00:00:00Z", "to": "2026-10-03T00:00:00Z", "limit": 1, "_project_id": "foreign-project"}
	for offset, id := range []int64{two, one} {
		args["offset"] = offset
		out := reportList(t, ctx, args)
		posts := out["posts"].([]map[string]any)
		if len(posts) != 1 || posts[0]["id"] != id || out["total"] != 2 || out["total_targets"] != 2 || out["returned_targets"] != 1 || out["has_more"] != (offset == 0) {
			t.Fatalf("filters and totals diverged: %+v", out)
		}
	}
	get, err := (&App{}).toolProfileGet(ctx, map[string]any{"id": p.ID})
	if err != nil {
		t.Fatal(err)
	}
	counted := get.(map[string]any)["profile"].(*Profile)
	if counted.PostCount != 4 || counted.TargetCount != 3 {
		t.Fatalf("profile totals must include drafts but exclude foreign posts: %+v", counted)
	}
	listed, err := (&App{}).toolProfileList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range listed.(map[string]any)["profiles"].([]Profile) {
		if row.ID == p.ID && (row.PostCount != counted.PostCount || row.TargetCount != counted.TargetCount) {
			t.Fatalf("profile_list and profile_get disagree: %+v %+v", row, counted)
		}
	}
	foreign := reportProfile(t, ctx.WithProject("foreign-project"), "Foreign")
	for _, filter := range []map[string]any{{"profile": "missing"}, {"profile_id": 9999}, {"profile_id": foreign.ID}} {
		out, err := (&App{}).toolPostList(ctx, filter)
		if err != nil || out.(map[string]any)["isError"] != true {
			t.Fatalf("invalid profile widened scope: out=%+v err=%v", out, err)
		}
	}
}

func TestPostReporting_EmptyCountsAreExplicitAndErrorsAreNotZero(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	p := reportProfile(t, ctx, "Empty")
	out := reportList(t, ctx, map[string]any{"profile_id": p.ID})
	if out["total"] != 0 || out["total_targets"] != 0 || out["has_more"] != false || out["next_offset"] != nil || len(out["posts"].([]map[string]any)) != 0 {
		t.Fatalf("empty page: %+v", out)
	}
	profiles, err := (&App{}).toolProfileList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(profiles)
	if !strings.Contains(string(raw), `"post_count":0`) || !strings.Contains(string(raw), `"target_count":0`) {
		t.Fatalf("zero counts omitted: %s", raw)
	}
	if _, err := ctx.AppDB().Exec(`DROP TABLE post_targets`); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolProfileList(ctx, nil); err == nil {
		t.Fatal("count query failure reported as zero")
	}
	if _, err := (&App{}).toolPostList(ctx, nil); err == nil {
		t.Fatal("count query failure reported as complete empty history")
	}
}

func TestPostReporting_ProfileUpdatesPreserveCounts(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	ctx.AppDB().SetMaxOpenConns(1)
	p := reportProfile(t, ctx, "Update counts")
	account := widgetAccountFixture(t, ctx, "test-proj", p.ID)
	reportPost(t, ctx, "test-proj", p.ID, "draft", "2026-10-01T00:00:00Z")
	reportPost(t, ctx, "test-proj", p.ID, "failed", "2026-10-01T00:00:00Z", account)
	for _, args := range []map[string]any{{"id": p.ID}, {"id": p.ID, "name": "Renamed"}} {
		out, err := (&App{}).toolProfileUpdate(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		profile := out.(map[string]any)["profile"].(*Profile)
		if profile.PostCount != 2 || profile.TargetCount != 1 {
			t.Fatalf("update reported false zero counts: %+v", profile)
		}
	}
}

func TestPostReporting_RejectsInvalidOffsets(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	for _, value := range []any{-1, -1.0, 1.5, "-1", "1.5", "bad", "", true, math.Inf(1), math.NaN(), "9223372036854775808"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			out, err := (&App{}).toolPostList(ctx, map[string]any{"offset": value})
			if err != nil || out.(map[string]any)["isError"] != true {
				t.Fatalf("invalid offset accepted: %+v %v", out, err)
			}
		})
	}
	for _, value := range []any{nil, 0, int64(0), 0.0, "0", json.Number("0")} {
		if out := reportList(t, ctx, map[string]any{"offset": value}); out["offset"] != 0 {
			t.Fatalf("valid offset rejected: %+v", out)
		}
	}
}

func TestPostReporting_SchemaExposesFiltersAndPagination(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "post_list" {
			continue
		}
		props := tool.InputSchema["properties"].(map[string]any)
		for _, field := range []string{"profile_id", "profile", "from", "to", "status", "limit", "offset"} {
			if _, present := props[field]; !present {
				t.Errorf("post_list schema omits %s", field)
			}
		}
		if props["limit"].(map[string]any)["maximum"] != 200 || props["offset"].(map[string]any)["minimum"] != 0 {
			t.Fatal("pagination bounds missing from schema")
		}
		return
	}
	t.Fatal("post_list missing from MCP tools")
}

func TestPostReporting_HTTPForwardsPaginationAndKeepsCalendarCap(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	p := reportProfile(t, ctx, "HTTP")
	for range 205 {
		reportPost(t, ctx, "test-proj", p.ID, "draft", "2026-10-01T00:00:00Z")
	}
	for _, args := range []string{"limit=1000", "limit=2000", "limit=2&offset=203"} {
		w := httptest.NewRecorder()
		(&App{}).handlePostsAPI(w, httptest.NewRequest("GET", "/posts?profile=http&project_id=spoofed&"+args, nil))
		var out struct {
			Posts                []map[string]any
			Total, Limit, Offset int
			HasMore              bool `json:"has_more"`
			NextOffset           *int `json:"next_offset"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Total != 205 || out.HasMore || out.NextOffset != nil {
			t.Fatalf("HTTP response: %d %s", w.Code, w.Body)
		}
		if strings.Contains(args, "offset") {
			if out.Offset != 203 || len(out.Posts) != 2 || out.Limit != 2 {
				t.Fatalf("HTTP offset ignored: %+v", out)
			}
		} else if len(out.Posts) != 205 || out.Limit != 1000 {
			t.Fatalf("panel calendar cap changed: %+v", out)
		}
	}
}

func TestPostReporting_SidecarMCPAndHTTPAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an isolated sidecar")
	}
	sidecar := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	created := sidecar.MCP("profile_create", map[string]any{"name": "Reporting"})
	if created["profile"] == nil {
		t.Fatalf("create profile: %+v", created)
	}
	for i := range 3 {
		out := sidecar.MCP("post_create", map[string]any{"mode": "draft", "body": fmt.Sprintf("Draft %d", i), "profile": "reporting"})
		if out["status"] != "draft" {
			t.Fatalf("create draft: %+v", out)
		}
	}
	first := sidecar.MCP("post_list", map[string]any{"profile": "reporting", "limit": 2})
	if first["total"] != float64(3) || first["has_more"] != true || first["next_offset"] != float64(2) {
		t.Fatalf("MCP pagination: %+v", first)
	}
	var httpPage map[string]any
	if response := sidecar.GET("/posts?profile=reporting&limit=2&offset=2", &httpPage); response.Status != 200 {
		t.Fatalf("HTTP pagination: %+v", response)
	}
	mcpPage := sidecar.MCP("post_list", map[string]any{"profile": "reporting", "limit": 2, "offset": 2})
	if !reflect.DeepEqual(httpPage, mcpPage) || len(mcpPage["posts"].([]any)) != 1 || mcpPage["has_more"] != false || mcpPage["next_offset"] != nil {
		t.Fatalf("MCP and HTTP diverged: http=%+v mcp=%+v", httpPage, mcpPage)
	}
	for _, path := range []string{"/posts?limit=2&offset=2", "/profiles", "/accounts", "/accounts/start"} {
		response, err := http.Get(sidecar.URL() + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s returned %d", path, response.StatusCode)
		}
	}
}
