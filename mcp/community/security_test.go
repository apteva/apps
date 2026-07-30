package main

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func delegatedTool(t *testing.T, name string) sdk.ToolHandlerCtx {
	t.Helper()
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == name {
			if tool.HandlerCtx == nil {
				t.Fatalf("%s has no context-aware handler", name)
			}
			return tool.HandlerCtx
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func userCallContext(subjectID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		SubjectType: "user",
		SubjectID:   subjectID,
	})
}

func mustCreateLinkedMember(t *testing.T, ctx *sdk.AppCtx, communityID, handle, subjectID string) Member {
	t.Helper()
	out, err := toolMembersCreate(ctx, map[string]any{
		"community_id": communityID,
		"handle":       handle,
		"display_name": handle,
		"auth_user_id": subjectID,
	})
	if err != nil {
		t.Fatalf("members_create %s: %v", handle, err)
	}
	return out.(Member)
}

func TestDelegatedMemberCannotSpoofPostAuthorOrUseOperatorTools(t *testing.T) {
	ctx, _ := newTestCtx(t)
	community := mustCreateCommunity(t, ctx, "main", "Main")
	alice := mustCreateLinkedMember(t, ctx, community.ID, "alice", "auth-alice")
	bob := mustCreateMember(t, ctx, community.ID, "bob")
	space := mustCreateSpace(t, ctx, community.ID, "general", "forum")
	threadOut, err := toolThreadsCreate(ctx, map[string]any{
		"space_id": space.ID, "author_id": alice.ID, "title": "Hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	threadID := threadOut.(map[string]any)["thread"].(Thread).ID

	out, err := delegatedTool(t, "posts_create")(
		userCallContext("auth-alice"),
		ctx,
		map[string]any{"thread_id": threadID, "author_id": bob.ID, "body": "mine"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(Post).AuthorID; got != alice.ID {
		t.Fatalf("delegated author = %q, want linked member %q", got, alice.ID)
	}
	listed, err := delegatedTool(t, "members_list")(
		userCallContext("auth-alice"),
		ctx,
		map[string]any{"community_id": community.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range listed.(map[string]any)["members"].([]Member) {
		if member.AuthUserID != nil || member.ContactID != nil {
			t.Fatalf("delegated directory leaked private identifiers: %+v", member)
		}
	}

	_, err = delegatedTool(t, "communities_create")(
		userCallContext("auth-alice"),
		ctx,
		map[string]any{"slug": "forbidden", "name": "Forbidden"},
	)
	if err == nil || !strings.Contains(err.Error(), "operators") {
		t.Fatalf("operator tool should be denied, got %v", err)
	}
}

func TestDelegatedMemberMustBeLinkedAndActive(t *testing.T) {
	ctx, _ := newTestCtx(t)
	community := mustCreateCommunity(t, ctx, "main", "Main")
	member := mustCreateLinkedMember(t, ctx, community.ID, "alice", "auth-alice")

	_, err := delegatedTool(t, "members_get")(
		userCallContext("unlinked"),
		ctx,
		map[string]any{"community_id": community.ID, "id": member.ID},
	)
	if err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("unlinked user should be denied, got %v", err)
	}

	if _, err := toolMembersUpdate(ctx, map[string]any{"id": member.ID, "status": "suspended"}); err != nil {
		t.Fatal(err)
	}
	_, err = delegatedTool(t, "members_get")(
		userCallContext("auth-alice"),
		ctx,
		map[string]any{"community_id": community.ID, "id": member.ID},
	)
	if err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("suspended user should be denied, got %v", err)
	}
}

func TestDelegatedDMOpenReplacesSelfPlaceholder(t *testing.T) {
	ctx, _ := newTestCtx(t)
	community := mustCreateCommunity(t, ctx, "main", "Main")
	alice := mustCreateLinkedMember(t, ctx, community.ID, "alice", "auth-alice")
	bob := mustCreateMember(t, ctx, community.ID, "bob")

	out, err := delegatedTool(t, "dms_open")(
		userCallContext("auth-alice"),
		ctx,
		map[string]any{
			"community_id": community.ID,
			"participants": []any{"self", bob.ID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	thread := out.(DMThread)
	if len(thread.Participants) != 2 ||
		!containsString(thread.Participants, alice.ID) ||
		!containsString(thread.Participants, bob.ID) {
		t.Fatalf("unexpected participants: %#v", thread.Participants)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSpacesArchiveCannotCrossProjectBoundary(t *testing.T) {
	ctx, _ := newTestCtx(t)
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO communities (id, project_id, slug, name) VALUES ('foreign-c', 'other-proj', 'foreign', 'Foreign')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO spaces (id, community_id, slug, name, kind, visibility)
		 VALUES ('foreign-s', 'foreign-c', 'private', 'Private', 'forum', 'members')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := toolSpacesArchive(ctx, map[string]any{"id": "foreign-s"}); err == nil {
		t.Fatal("cross-project archive should be denied")
	}
}

func TestDelegatedCourseAccessRequiresEnrollmentAndPublishedLesson(t *testing.T) {
	ctx, communityID, memberID, courseID, sectionID, lessonID, _ := setupCourse(t)
	if _, err := ctx.AppDB().Exec(
		`UPDATE members SET auth_user_id = ? WHERE id = ?`,
		"auth-alice", memberID,
	); err != nil {
		t.Fatal(err)
	}
	call := delegatedTool(t, "lessons_get")
	_, err := call(userCallContext("auth-alice"), ctx, map[string]any{"id": lessonID})
	if err == nil || !strings.Contains(err.Error(), "enrollment") {
		t.Fatalf("unenrolled lesson access should fail, got %v", err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(userCallContext("auth-alice"), ctx, map[string]any{"id": lessonID}); err != nil {
		t.Fatalf("published enrolled lesson should be readable: %v", err)
	}

	draft, err := toolLessonsCreate(ctx, map[string]any{"section_id": sectionID, "title": "Draft"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(userCallContext("auth-alice"), ctx, map[string]any{"id": draft.(Lesson).ID})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("draft lesson should be hidden, got %v", err)
	}

	if _, err := toolEnrollmentRulesSet(ctx, map[string]any{
		"space_id": courseID, "access_mode": "paid",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = delegatedTool(t, "course_enroll")(
		userCallContext("auth-alice"),
		ctx,
		map[string]any{"space_id": courseID, "member_id": memberID},
	)
	if err == nil || !strings.Contains(err.Error(), "external purchase") {
		t.Fatalf("delegated paid enrollment should fail, got %v", err)
	}

	_ = communityID
}

func TestReorderRequiresEveryUniqueItem(t *testing.T) {
	ctx, _, _, courseID, sectionID, lessonAID, lessonBID := setupCourse(t)
	section2, err := toolSectionsCreate(ctx, map[string]any{"space_id": courseID, "title": "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toolSectionsReorder(ctx, map[string]any{
		"space_id": courseID, "order": []any{sectionID},
	}); err == nil {
		t.Fatal("partial section order should fail")
	}
	if _, err := toolSectionsReorder(ctx, map[string]any{
		"space_id": courseID, "order": []any{sectionID, sectionID},
	}); err == nil {
		t.Fatal("duplicate section order should fail")
	}
	if _, err := toolSectionsReorder(ctx, map[string]any{
		"space_id": courseID, "order": []any{section2.(Section).ID, sectionID},
	}); err != nil {
		t.Fatalf("complete section order should pass: %v", err)
	}
	if _, err := toolLessonsReorder(ctx, map[string]any{
		"section_id": sectionID, "order": []any{lessonAID},
	}); err == nil {
		t.Fatal("partial lesson order should fail")
	}
	if _, err := toolLessonsReorder(ctx, map[string]any{
		"section_id": sectionID, "order": []any{lessonAID, lessonBID},
	}); err != nil {
		t.Fatalf("complete lesson order should pass: %v", err)
	}
}

func TestCourseCompletionAndProgressReopenStayConsistent(t *testing.T) {
	ctx, _, memberID, courseID, _, lessonAID, lessonBID := setupCourse(t)
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCertificatesConfigure(ctx, map[string]any{
		"space_id": courseID, "enabled": true, "issue_on_completion": true, "title": "Graduate",
	}); err != nil {
		t.Fatal(err)
	}
	for _, lessonID := range []string{lessonAID, lessonBID} {
		if _, err := toolLessonsMarkComplete(ctx, map[string]any{
			"lesson_id": lessonID, "member_id": memberID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	enrollment, err := loadCourseEnrollment(ctx.AppDB(), courseID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Status != "completed" || enrollment.CompletedAt == nil {
		t.Fatalf("course not completed: %+v", enrollment)
	}
	var certificates int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM issued_certificates WHERE space_id = ? AND member_id = ?`,
		courseID, memberID,
	).Scan(&certificates); err != nil {
		t.Fatal(err)
	}
	if certificates != 1 {
		t.Fatalf("issued certificates = %d, want 1", certificates)
	}
	community, err := toolCommunitiesGet(ctx, map[string]any{"slug": "main"})
	if err != nil {
		t.Fatal(err)
	}
	unenrolled := mustCreateMember(t, ctx, community.(Community).ID, "bob")
	for _, lessonID := range []string{lessonAID, lessonBID} {
		if _, err := toolLessonsMarkComplete(ctx, map[string]any{
			"lesson_id": lessonID, "member_id": unenrolled.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM issued_certificates WHERE space_id = ? AND member_id = ?`,
		courseID, unenrolled.ID,
	).Scan(&certificates); err != nil {
		t.Fatal(err)
	}
	if certificates != 0 {
		t.Fatalf("unenrolled member received %d certificates", certificates)
	}

	progress, err := toolLessonsMarkComplete(ctx, map[string]any{
		"lesson_id": lessonAID, "member_id": memberID, "status": "in_progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress.(LessonProgress).CompletedAt != nil {
		t.Fatal("reopened lesson retained completed_at")
	}
	enrollment, err = loadCourseEnrollment(ctx.AppDB(), courseID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Status != "active" || enrollment.CompletedAt != nil {
		t.Fatalf("course completion was not reopened: %+v", enrollment)
	}
}
