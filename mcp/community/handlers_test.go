package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Test harness ────────────────────────────────────────────────

func newTestCtx(t *testing.T) (*sdk.AppCtx, *tk.EmitRecorder) {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx, rec
}

func mustCreateCommunity(t *testing.T, ctx *sdk.AppCtx, slug, name string) Community {
	t.Helper()
	out, err := toolCommunitiesCreate(ctx, map[string]any{
		"slug": slug,
		"name": name,
	})
	if err != nil {
		t.Fatalf("communities_create: %v", err)
	}
	return out.(Community)
}

func mustCreateMember(t *testing.T, ctx *sdk.AppCtx, communityID, handle string) Member {
	t.Helper()
	out, err := toolMembersCreate(ctx, map[string]any{
		"community_id": communityID,
		"handle":       handle,
		"display_name": handle,
	})
	if err != nil {
		t.Fatalf("members_create %s: %v", handle, err)
	}
	return out.(Member)
}

func mustCreateSpace(t *testing.T, ctx *sdk.AppCtx, communityID, slug, kind string) Space {
	t.Helper()
	out, err := toolSpacesCreate(ctx, map[string]any{
		"community_id": communityID,
		"slug":         slug,
		"name":         strings.Title(slug),
		"kind":         kind,
	})
	if err != nil {
		t.Fatalf("spaces_create: %v", err)
	}
	return out.(Space)
}

// ─── communities ─────────────────────────────────────────────────

func TestCommunitiesCreate_Emits(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	if !strings.HasPrefix(c.ID, "c_") {
		t.Fatalf("expected c_ prefix, got %q", c.ID)
	}
	if c.ProjectID != "test-proj" {
		t.Fatalf("project_id = %q, want test-proj", c.ProjectID)
	}
	got, ok := rec.WaitForTopic("community.created", 100*time.Millisecond)
	if !ok {
		t.Fatalf("community.created not emitted")
	}
	payload := got.Data.(map[string]any)
	if payload["slug"] != "main" {
		t.Fatalf("payload slug = %v", payload["slug"])
	}
}

func TestCommunitiesCreate_RejectsBadSlug(t *testing.T) {
	ctx, _ := newTestCtx(t)
	for _, slug := range []string{"", "A", "with space", "-leading", "too-long-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		_, err := toolCommunitiesCreate(ctx, map[string]any{"slug": slug, "name": "x"})
		if err == nil {
			t.Errorf("slug %q should be rejected", slug)
		}
	}
}

func TestCommunitiesGet_BySlug(t *testing.T) {
	ctx, _ := newTestCtx(t)
	a := mustCreateCommunity(t, ctx, "alpha", "Alpha")
	got, err := toolCommunitiesGet(ctx, map[string]any{"slug": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if got.(Community).ID != a.ID {
		t.Fatalf("got id %q, want %q", got.(Community).ID, a.ID)
	}
}

func TestPortalBootstrapReturnsBrandAndAutomaticAuthBinding(t *testing.T) {
	ctx, _ := newTestCtx(t)
	community := mustCreateCommunity(t, ctx, "academy", "Academy")
	if _, err := toolCommunitiesUpdate(ctx, map[string]any{
		"id": community.ID, "auth_client_id": "academy-client", "auth_organization_slug": "academy-org",
		"brand_name": "Makecademy", "primary_color": "#123456", "accent_color": "#fedcba",
		"signup_mode": "open", "auto_create_members": true,
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/portal/bootstrap?community=academy", nil)
	rec := httptest.NewRecorder()
	(&App{}).httpPortalBootstrap(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"Makecademy"`, `"client_id":"academy-client"`, `"organization_slug":"academy-org"`, `"enabled":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("bootstrap missing %s: %s", want, body)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bootstrap must not be cached: %q", rec.Header().Get("Cache-Control"))
	}
}

// ─── members ─────────────────────────────────────────────────────

func TestMembersCreate_Emits(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	if m.Status != "active" {
		t.Fatalf("default status = %q", m.Status)
	}
	got, ok := rec.WaitForTopic("member.joined", 100*time.Millisecond)
	if !ok {
		t.Fatalf("member.joined not emitted")
	}
	if got.Data.(map[string]any)["handle"] != "alice" {
		t.Fatalf("payload handle wrong")
	}
}

func TestMembers_UniqueHandle(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	mustCreateMember(t, ctx, c.ID, "alice")
	_, err := toolMembersCreate(ctx, map[string]any{
		"community_id": c.ID,
		"handle":       "alice",
	})
	if err == nil {
		t.Fatalf("duplicate handle should fail")
	}
}

// ─── threads + posts ─────────────────────────────────────────────

func TestThreadsCreate_WithBody_EmitsThreadAndPost(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "feed")
	out, err := toolThreadsCreate(ctx, map[string]any{
		"space_id":  s.ID,
		"author_id": m.ID,
		"title":     "Welcome!",
		"body":      "hi everyone, this is the kickoff thread.",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := out.(map[string]any)
	tt := wrapper["thread"].(Thread)
	if tt.PostCount != 1 {
		t.Fatalf("post_count = %d, want 1", tt.PostCount)
	}
	if _, ok := rec.WaitForTopic("thread.created", 100*time.Millisecond); !ok {
		t.Fatalf("thread.created not emitted")
	}
	if _, ok := rec.WaitForTopic("post.created", 100*time.Millisecond); !ok {
		t.Fatalf("post.created not emitted")
	}
}

func TestPostsList_OldestFirst_IncludesReactions(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	alice := mustCreateMember(t, ctx, c.ID, "alice")
	bob := mustCreateMember(t, ctx, c.ID, "bob")
	s := mustCreateSpace(t, ctx, c.ID, "general", "forum")
	tOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id":  s.ID,
		"author_id": alice.ID,
		"title":     "Q",
		"body":      "first",
	})
	threadID := tOut.(map[string]any)["thread"].(Thread).ID
	// Add a reply.
	pOut, err := toolPostsCreate(ctx, map[string]any{
		"thread_id": threadID,
		"author_id": bob.ID,
		"body":      "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	post2 := pOut.(Post)
	// React to the reply.
	if _, err := toolPostsReact(ctx, map[string]any{
		"post_id":   post2.ID,
		"member_id": alice.ID,
		"emoji":     "👍",
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := toolPostsList(ctx, map[string]any{"thread_id": threadID})
	if err != nil {
		t.Fatal(err)
	}
	posts := listed.(map[string]any)["posts"].([]Post)
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(posts))
	}
	if posts[0].Body != "first" || posts[1].Body != "second" {
		t.Fatalf("posts not oldest-first: %+v", posts)
	}
	if len(posts[1].Reactions) != 1 || posts[1].Reactions[0].Count != 1 {
		t.Fatalf("reactions missing or wrong: %+v", posts[1].Reactions)
	}
}

func TestPostsReact_TogglesOff(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "feed")
	tOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id":  s.ID,
		"author_id": m.ID,
		"body":      "hi",
	})
	threadID := tOut.(map[string]any)["thread"].(Thread).ID
	posts, _ := toolPostsList(ctx, map[string]any{"thread_id": threadID})
	postID := posts.(map[string]any)["posts"].([]Post)[0].ID
	r1, err := toolPostsReact(ctx, map[string]any{
		"post_id": postID, "member_id": m.ID, "emoji": "❤",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r1.(map[string]any)["action"] != "added" {
		t.Fatalf("first react should be added")
	}
	r2, _ := toolPostsReact(ctx, map[string]any{
		"post_id": postID, "member_id": m.ID, "emoji": "❤",
	})
	if r2.(map[string]any)["action"] != "removed" {
		t.Fatalf("re-react should remove")
	}
}

func TestPostsEdit_AuthorOnly(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	alice := mustCreateMember(t, ctx, c.ID, "alice")
	bob := mustCreateMember(t, ctx, c.ID, "bob")
	s := mustCreateSpace(t, ctx, c.ID, "general", "feed")
	tOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id":  s.ID,
		"author_id": alice.ID,
		"body":      "original",
	})
	threadID := tOut.(map[string]any)["thread"].(Thread).ID
	posts, _ := toolPostsList(ctx, map[string]any{"thread_id": threadID})
	postID := posts.(map[string]any)["posts"].([]Post)[0].ID
	// Bob can't edit alice's post.
	_, err := toolPostsEdit(ctx, map[string]any{
		"id": postID, "body": "hijacked", "caller_member_id": bob.ID,
	})
	if err == nil {
		t.Fatalf("non-author edit should fail")
	}
	// Alice can.
	out, err := toolPostsEdit(ctx, map[string]any{
		"id": postID, "body": "fixed typo", "caller_member_id": alice.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(Post).Body != "fixed typo" {
		t.Fatalf("body not updated")
	}
	if out.(Post).EditedAt == nil {
		t.Fatalf("edited_at not set")
	}
}

// ─── DMs ─────────────────────────────────────────────────────────

func TestDMs_OpenIsIdempotent(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	a := mustCreateMember(t, ctx, c.ID, "alice")
	b := mustCreateMember(t, ctx, c.ID, "bob")
	first, err := toolDMsOpen(ctx, map[string]any{
		"participants": []any{a.ID, b.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := toolDMsOpen(ctx, map[string]any{
		"participants": []any{b.ID, a.ID}, // reversed order — should match
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.(DMThread).ID != again.(DMThread).ID {
		t.Fatalf("re-open should return same thread; got %q vs %q",
			first.(DMThread).ID, again.(DMThread).ID)
	}
}

func TestDMs_SendAndList_EmitsDMReceived(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	a := mustCreateMember(t, ctx, c.ID, "alice")
	b := mustCreateMember(t, ctx, c.ID, "bob")
	th, _ := toolDMsOpen(ctx, map[string]any{"participants": []any{a.ID, b.ID}})
	threadID := th.(DMThread).ID
	if _, err := toolDMsSend(ctx, map[string]any{
		"dm_thread_id": threadID,
		"author_id":    a.ID,
		"body":         "hey bob",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.WaitForTopic("dm.received", 100*time.Millisecond); !ok {
		t.Fatalf("dm.received not emitted")
	}
	list, err := toolDMsListThreads(ctx, map[string]any{"member_id": b.ID})
	if err != nil {
		t.Fatal(err)
	}
	threads := list.(map[string]any)["threads"].([]DMThread)
	if len(threads) != 1 {
		t.Fatalf("bob should see 1 dm thread, got %d", len(threads))
	}
	if len(threads[0].Participants) != 2 {
		t.Fatalf("participants not hydrated")
	}
}

func TestDMsGetThread_RequiresParticipantCaller(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	a := mustCreateMember(t, ctx, c.ID, "alice")
	b := mustCreateMember(t, ctx, c.ID, "bob")
	outsider := mustCreateMember(t, ctx, c.ID, "carol")
	th, _ := toolDMsOpen(ctx, map[string]any{"participants": []any{a.ID, b.ID}})
	threadID := th.(DMThread).ID
	if _, err := toolDMsSend(ctx, map[string]any{
		"dm_thread_id": threadID,
		"author_id":    a.ID,
		"body":         "secret",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolDMsGetThread(ctx, map[string]any{"id": threadID}); err == nil {
		t.Fatalf("missing caller_member_id should fail")
	}
	if _, err := toolDMsGetThread(ctx, map[string]any{
		"id":               threadID,
		"caller_member_id": outsider.ID,
	}); err == nil {
		t.Fatalf("non-participant dm read should fail")
	}
	out, err := toolDMsGetThread(ctx, map[string]any{
		"id":               threadID,
		"caller_member_id": b.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(DMThreadView).Messages) != 1 {
		t.Fatalf("participant should see one message")
	}
}

func TestDMs_RejectsCrossCommunity(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c1 := mustCreateCommunity(t, ctx, "one", "One")
	c2 := mustCreateCommunity(t, ctx, "two", "Two")
	a := mustCreateMember(t, ctx, c1.ID, "alice")
	x := mustCreateMember(t, ctx, c2.ID, "xander")
	_, err := toolDMsOpen(ctx, map[string]any{
		"participants": []any{a.ID, x.ID},
	})
	if err == nil || !strings.Contains(err.Error(), "same community") {
		t.Fatalf("cross-community dm should fail; got %v", err)
	}
}

// ─── spaces_add_member same-community guard ───────────────────────

func TestSpacesAddMember_RejectsCrossCommunity(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c1 := mustCreateCommunity(t, ctx, "one", "One")
	c2 := mustCreateCommunity(t, ctx, "two", "Two")
	s := mustCreateSpace(t, ctx, c1.ID, "general", "feed")
	stranger := mustCreateMember(t, ctx, c2.ID, "stranger")
	_, err := toolSpacesAddMember(ctx, map[string]any{
		"space_id":  s.ID,
		"member_id": stranger.ID,
	})
	if err == nil {
		t.Fatalf("cross-community add should fail")
	}
}

func TestHTTPWriteRoutesRejected(t *testing.T) {
	newTestCtx(t)
	app := &App{}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{"communities", app.httpCommunities, "/communities"},
		{"members", app.httpMembers, "/members"},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		tc.handler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s POST status = %d, want 405", tc.name, rec.Code)
		}
	}
}

func TestProjectScopedIDsAreRejected(t *testing.T) {
	ctx, _ := newTestCtx(t)
	db := ctx.AppDB()
	foreignID := "c_foreign"
	if _, err := db.Exec(
		`INSERT INTO communities (id, project_id, slug, name, description)
		 VALUES (?, ?, ?, ?, ?)`,
		foreignID, "other-proj", "foreign", "Foreign", "",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCommunitiesGet(ctx, map[string]any{"id": foreignID}); err == nil {
		t.Fatalf("get by foreign project id should fail")
	}
	if _, err := toolCommunitiesUpdate(ctx, map[string]any{
		"id": foreignID, "name": "Taken",
	}); err == nil {
		t.Fatalf("update by foreign project id should fail")
	}
	if _, err := toolMembersCreate(ctx, map[string]any{
		"community_id": foreignID,
		"handle":       "mallory",
	}); err == nil {
		t.Fatalf("member create in foreign project community should fail")
	}
}

func TestArchivedCommunityAndSpaceBlockWrites(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "feed")
	tOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id": s.ID, "author_id": m.ID, "body": "before archive",
	})
	threadID := tOut.(map[string]any)["thread"].(Thread).ID
	if _, err := toolSpacesArchive(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolThreadsCreate(ctx, map[string]any{
		"space_id": s.ID, "author_id": m.ID, "body": "after archive",
	}); err == nil {
		t.Fatalf("thread create in archived space should fail")
	}
	if _, err := toolPostsCreate(ctx, map[string]any{
		"thread_id": threadID, "author_id": m.ID, "body": "after archive",
	}); err == nil {
		t.Fatalf("post create in archived space should fail")
	}

	c2 := mustCreateCommunity(t, ctx, "second", "Second")
	m2 := mustCreateMember(t, ctx, c2.ID, "bob")
	s2 := mustCreateSpace(t, ctx, c2.ID, "general", "feed")
	if _, err := toolCommunitiesArchive(ctx, map[string]any{"id": c2.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolThreadsCreate(ctx, map[string]any{
		"space_id": s2.ID, "author_id": m2.ID, "body": "after archive",
	}); err == nil {
		t.Fatalf("thread create in archived community should fail")
	}
}

func TestPostsCreate_RejectsReplyToDifferentThread(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	alice := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "forum")
	aOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id": s.ID, "author_id": alice.ID, "body": "thread a",
	})
	threadA := aOut.(map[string]any)["thread"].(Thread).ID
	aPosts, _ := toolPostsList(ctx, map[string]any{"thread_id": threadA})
	parentID := aPosts.(map[string]any)["posts"].([]Post)[0].ID
	bOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id": s.ID, "author_id": alice.ID, "body": "thread b",
	})
	threadB := bOut.(map[string]any)["thread"].(Thread).ID
	if _, err := toolPostsCreate(ctx, map[string]any{
		"thread_id":   threadB,
		"author_id":   alice.ID,
		"body":        "bad reply",
		"reply_to_id": parentID,
	}); err == nil {
		t.Fatalf("cross-thread reply should fail")
	}
}
