package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func mustCollection(t *testing.T, ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) *Collection {
	t.Helper()
	collection, err := createCollection(ctx, pid, spaceID, args)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	return collection
}

func mustPost(t *testing.T, ctx *sdk.AppCtx, pid string, spaceID int64, title, status, visibility string) *Post {
	t.Helper()
	post, err := createPost(ctx, pid, spaceID, map[string]any{
		"title": title, "body": "body:" + title, "status": status, "visibility": visibility,
	})
	if err != nil {
		t.Fatalf("create post %q: %v", title, err)
	}
	return post
}

func TestCollectionOrderingManyToManyAndSpaceIsolation(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	other := mustSpace(t, ctx, testProject, "Other", "other")
	first := mustPost(t, ctx, testProject, space.ID, "First", "published", "public")
	second := mustPost(t, ctx, testProject, space.ID, "Second", "published", "members")
	foreign := mustPost(t, ctx, testProject, other.ID, "Foreign", "published", "public")
	session := mustCollection(t, ctx, testProject, space.ID, map[string]any{
		"title": "Session One", "status": "published",
		"metadata": map[string]any{"models": []any{"Alice", "Bea"}, "session_date": "2026-07-24"},
	})
	featured := mustCollection(t, ctx, testProject, space.ID, map[string]any{
		"title": "Featured", "status": "published",
	})

	ordered, err := setCollectionPosts(ctx, testProject, space.ID, session.ID, []int64{second.ID, first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Post.ID != second.ID || ordered[0].Position != 1 || ordered[1].Post.ID != first.ID {
		t.Fatalf("unexpected collection order: %#v", ordered)
	}
	if _, err := setCollectionPosts(ctx, testProject, space.ID, featured.ID, []int64{first.ID}); err != nil {
		t.Fatal(err)
	}
	first, err = getPost(ctx.AppDB(), testProject, space.ID, first.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.CollectionIDs) != 2 || first.CollectionIDs[0] != session.ID || first.CollectionIDs[1] != featured.ID {
		t.Fatalf("post collection IDs = %#v", first.CollectionIDs)
	}

	if _, err := setCollectionPosts(ctx, testProject, space.ID, session.ID, []int64{first.ID, first.ID}); err == nil {
		t.Fatal("duplicate post IDs were accepted")
	}
	if _, err := setCollectionPosts(ctx, testProject, space.ID, session.ID, []int64{foreign.ID}); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("cross-space post error = %v", err)
	}
	ordered, err = listCollectionPosts(ctx.AppDB(), testProject, space.ID, session.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Post.ID != second.ID || ordered[1].Post.ID != first.ID {
		t.Fatalf("failed update changed existing order: %#v", ordered)
	}

	var metadata map[string]any
	if err := json.Unmarshal(session.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	models, _ := metadata["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("multi-model session metadata = %#v", metadata)
	}
}

func TestPostCollectionIDsCreateUpdateAndClear(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	first := mustCollection(t, ctx, testProject, space.ID, map[string]any{"title": "First"})
	second := mustCollection(t, ctx, testProject, space.ID, map[string]any{"title": "Second"})

	post, err := createPost(ctx, testProject, space.ID, map[string]any{
		"title": "In both", "collection_ids": []any{float64(first.ID), float64(second.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(post.CollectionIDs) != 2 {
		t.Fatalf("create collection IDs = %#v", post.CollectionIDs)
	}
	post, err = updatePost(ctx, testProject, space.ID, post.ID, map[string]any{
		"collection_ids": []any{float64(second.ID)},
	})
	if err != nil || len(post.CollectionIDs) != 1 || post.CollectionIDs[0] != second.ID {
		t.Fatalf("update collections: post=%#v err=%v", post, err)
	}
	post, err = updatePost(ctx, testProject, space.ID, post.ID, map[string]any{"collection_ids": []any{}})
	if err != nil || len(post.CollectionIDs) != 0 {
		t.Fatalf("clear collections: post=%#v err=%v", post, err)
	}
	if _, err := updatePost(ctx, testProject, space.ID, post.ID, map[string]any{
		"collection_ids": []any{1.5},
	}); err == nil {
		t.Fatal("fractional collection ID was accepted")
	}
}

func TestCollectionPortalProjectionsRedactLockedAndHiddenContent(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	tier := mustTier(t, ctx, testProject, space.ID, "month")
	otherTier, err := createTier(ctx, testProject, space.ID, map[string]any{
		"name": "Other", "price_cents": 500, "currency": "USD", "interval": "month",
	})
	if err != nil {
		t.Fatal(err)
	}
	public := mustPost(t, ctx, testProject, space.ID, "Public", "published", "public")
	members := mustPost(t, ctx, testProject, space.ID, "Members secret", "published", "members")
	tierPost, err := createPost(ctx, testProject, space.ID, map[string]any{
		"title": "Tier secret", "body": "body:Tier secret", "status": "published",
		"visibility": "tier", "tier_ids": []int64{tier.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	private := mustPost(t, ctx, testProject, space.ID, "Private secret", "published", "private")
	draft := mustPost(t, ctx, testProject, space.ID, "Draft secret", "draft", "public")
	collection := mustCollection(t, ctx, testProject, space.ID, map[string]any{
		"title": "Mixed Session", "status": "published",
	})
	if _, err := setCollectionPosts(ctx, testProject, space.ID, collection.ID, []int64{
		public.ID, members.ID, tierPost.ID, private.ID, draft.ID,
	}); err != nil {
		t.Fatal(err)
	}
	for _, post := range []*Post{public, members, tierPost} {
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO attachments
			 (project_id, space_id, post_id, storage_file_id, filename, content_type, visibility)
			 VALUES (?, ?, ?, ?, ?, 'image/png', 'inherit')`,
			testProject, space.ID, post.ID, post.ID+100, post.Slug+".png",
		); err != nil {
			t.Fatal(err)
		}
	}

	publicViews, err := collectionPostViews(ctx.AppDB(), testProject, space.ID, collection.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicViews) != 3 {
		t.Fatalf("public views included hidden posts: %#v", publicViews)
	}
	if publicViews[0].Locked || publicViews[0].Body == "" || len(publicViews[0].Attachments) != 1 {
		t.Fatalf("public post projection = %#v", publicViews[0])
	}
	for _, view := range publicViews[1:] {
		if !view.Locked || view.Body != "" || len(view.Attachments) != 0 {
			t.Fatalf("locked post leaked content: %#v", view)
		}
	}

	eligible := mustMember(t, ctx, testProject, space.ID, map[string]any{
		"email": "eligible@example.com", "status": "active", "tier_id": tier.ID,
	})
	eligibleViews, err := collectionPostViews(ctx.AppDB(), testProject, space.ID, collection.ID, eligible)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range eligibleViews {
		if view.Locked || view.Body == "" || len(view.Attachments) != 1 {
			t.Fatalf("eligible member projection = %#v", view)
		}
	}

	wrongTier := mustMember(t, ctx, testProject, space.ID, map[string]any{
		"email": "other@example.com", "status": "active", "tier_id": otherTier.ID,
	})
	wrongTierViews, err := collectionPostViews(ctx.AppDB(), testProject, space.ID, collection.ID, wrongTier)
	if err != nil {
		t.Fatal(err)
	}
	if wrongTierViews[1].Locked || !wrongTierViews[2].Locked || wrongTierViews[2].Body != "" {
		t.Fatalf("wrong-tier projection = %#v", wrongTierViews)
	}

	request := httptest.NewRequest(http.MethodGet, "/public/primary/collections/mixed-session?project_id="+testProject, nil)
	recorder := httptest.NewRecorder()
	(&App{}).handlePublic(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("public collection status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "body:Members secret") || strings.Contains(body, "body:Tier secret") ||
		strings.Contains(body, "Private secret") || strings.Contains(body, "Draft secret") {
		t.Fatalf("public collection leaked content: %s", body)
	}
	if !strings.Contains(body, `"post_count":3`) || !strings.Contains(body, `"locked":true`) {
		t.Fatalf("public collection omitted safe summaries: %s", body)
	}

	request = httptest.NewRequest(http.MethodGet, "/member/"+eligible.PortalToken+"/collections/mixed-session", nil)
	recorder = httptest.NewRecorder()
	(&App{}).handleMemberPortal(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "body:Tier secret") {
		t.Fatalf("member collection status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), eligible.PortalToken) {
		t.Fatal("member collection leaked portal token")
	}
}

func TestCollectionDraftIsNotPublicAndVisibleCountDoesNotLeak(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	public := mustPost(t, ctx, testProject, space.ID, "Public", "published", "public")
	private := mustPost(t, ctx, testProject, space.ID, "Private", "published", "private")
	published := mustCollection(t, ctx, testProject, space.ID, map[string]any{
		"title": "Published", "status": "published",
	})
	draft := mustCollection(t, ctx, testProject, space.ID, map[string]any{
		"title": "Draft", "status": "draft",
	})
	if _, err := setCollectionPosts(ctx, testProject, space.ID, published.ID, []int64{public.ID, private.ID}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/public/primary/collections?project_id="+testProject, nil)
	recorder := httptest.NewRecorder()
	(&App{}).handlePublic(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"post_count":1`) {
		t.Fatalf("public list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), draft.Title) {
		t.Fatalf("draft collection appeared publicly: %s", recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/public/primary/collections/draft?project_id="+testProject, nil)
	recorder = httptest.NewRecorder()
	(&App{}).handlePublic(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("draft collection status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCollectionCoverRequiresStorageImageAndRedirects(t *testing.T) {
	platform := &storageLookupPlatform{
		found: true, wrapped: true, contentType: "image/jpeg", signedURL: "https://storage.example/cover.jpg",
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
	globalCtx = ctx
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	collection := mustCollection(t, ctx, testProject, space.ID, map[string]any{
		"title": "Covered", "status": "published", "cover_storage_file_id": int64(42),
	})
	request := httptest.NewRequest(http.MethodGet, "/public/primary/collections/covered/cover?project_id="+testProject, nil)
	recorder := httptest.NewRecorder()
	(&App{}).handlePublic(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != platform.signedURL {
		t.Fatalf("cover response status=%d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
	if collection.CoverStorageFileID == nil || *collection.CoverStorageFileID != 42 {
		t.Fatalf("cover file = %#v", collection.CoverStorageFileID)
	}

	platform.contentType = "application/pdf"
	if _, err := createCollection(ctx, testProject, space.ID, map[string]any{
		"title": "Not an image", "cover_storage_file_id": int64(43),
	}); err == nil || !strings.Contains(err.Error(), "must be an image") {
		t.Fatalf("non-image cover error = %v", err)
	}
}
