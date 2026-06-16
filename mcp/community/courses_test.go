package main

import (
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── shared course-setup ────────────────────────────────────────

// setupCourse creates a community, one member, one course-kind space,
// one section, two published lessons. Returns the ids the test needs.
func setupCourse(t *testing.T) (ctx *sdk.AppCtx, communityID, memberID, courseID, sectionID, lessonAID, lessonBID string) {
	t.Helper()
	ctx, _ = newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	communityID = c.ID
	m := mustCreateMember(t, ctx, c.ID, "alice")
	memberID = m.ID

	course, err := toolCoursesCreate(ctx, map[string]any{
		"community_id": c.ID,
		"slug":         "onboarding",
		"name":         "Onboarding",
	})
	if err != nil {
		t.Fatalf("courses_create: %v", err)
	}
	courseID = course.(Space).ID
	if course.(Space).Kind != "course" {
		t.Fatalf("expected kind=course, got %q", course.(Space).Kind)
	}

	sec, err := toolSectionsCreate(ctx, map[string]any{
		"space_id": courseID, "title": "Getting started",
	})
	if err != nil {
		t.Fatalf("sections_create: %v", err)
	}
	sectionID = sec.(Section).ID

	la, err := toolLessonsCreate(ctx, map[string]any{
		"section_id": sectionID, "title": "Welcome", "body": "hi there",
	})
	if err != nil {
		t.Fatalf("lessons_create A: %v", err)
	}
	lessonAID = la.(Lesson).ID
	if _, err := toolLessonsPublish(ctx, map[string]any{
		"id": lessonAID, "published": true,
	}); err != nil {
		t.Fatal(err)
	}

	lb, err := toolLessonsCreate(ctx, map[string]any{
		"section_id": sectionID, "title": "Setup", "body": "install x",
	})
	if err != nil {
		t.Fatalf("lessons_create B: %v", err)
	}
	lessonBID = lb.(Lesson).ID
	if _, err := toolLessonsPublish(ctx, map[string]any{
		"id": lessonBID, "published": true,
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// ─── courses_create + sections + lessons ─────────────────────────

func TestCoursesCreate_IsSpaceKindCourse(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	out, err := toolCoursesCreate(ctx, map[string]any{
		"community_id": c.ID, "slug": "101", "name": "Apteva 101",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(Space).Kind != "course" {
		t.Fatalf("kind = %q", out.(Space).Kind)
	}
	if _, ok := rec.WaitForTopic("space.created", 100*time.Millisecond); !ok {
		t.Fatalf("space.created not emitted")
	}
}

func TestSections_ReorderRequiresSameSpace(t *testing.T) {
	ctx, _, _, courseID, _, _, _ := setupCourse(t)
	s2, err := toolSectionsCreate(ctx, map[string]any{
		"space_id": courseID, "title": "Deep dive",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := toolSectionsList(ctx, map[string]any{"space_id": courseID})
	if err != nil {
		t.Fatal(err)
	}
	all := out.(map[string]any)["sections"].([]Section)
	if len(all) != 2 {
		t.Fatalf("got %d sections", len(all))
	}
	if _, err := toolSectionsReorder(ctx, map[string]any{
		"space_id": courseID,
		"order":    []any{s2.(Section).ID, all[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := toolSectionsList(ctx, map[string]any{"space_id": courseID})
	ordered := after.(map[string]any)["sections"].([]Section)
	if ordered[0].ID != s2.(Section).ID {
		t.Fatalf("reorder didn't take effect")
	}
}

func TestSections_RejectsNonCourseSpace(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	feed := mustCreateSpace(t, ctx, c.ID, "feed", "feed")
	_, err := toolSectionsCreate(ctx, map[string]any{
		"space_id": feed.ID, "title": "nope",
	})
	if err == nil || !strings.Contains(err.Error(), "course") {
		t.Fatalf("non-course section should fail; got %v", err)
	}
}

// ─── publish gates lessons_list ──────────────────────────────────

func TestLessons_DraftHiddenByDefault(t *testing.T) {
	ctx, _, _, courseID, sectionID, _, _ := setupCourse(t)
	if _, err := toolLessonsCreate(ctx, map[string]any{
		"section_id": sectionID, "title": "WIP",
	}); err != nil {
		t.Fatal(err)
	}
	out, _ := toolLessonsList(ctx, map[string]any{"space_id": courseID})
	if len(out.(map[string]any)["lessons"].([]Lesson)) != 2 {
		t.Fatalf("default list should hide drafts")
	}
	out, _ = toolLessonsList(ctx, map[string]any{
		"space_id":       courseID,
		"include_drafts": true,
	})
	if len(out.(map[string]any)["lessons"].([]Lesson)) != 3 {
		t.Fatalf("include_drafts should surface all")
	}
}

// ─── progress + funnels ──────────────────────────────────────────

func TestProgress_FlowAndPercent(t *testing.T) {
	ctx, _, memberID, courseID, _, lessonAID, lessonBID := setupCourse(t)

	// Mark A complete, B in-progress.
	if _, err := toolLessonsMarkComplete(ctx, map[string]any{
		"lesson_id": lessonAID, "member_id": memberID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolLessonsMarkComplete(ctx, map[string]any{
		"lesson_id":             lessonBID,
		"member_id":             memberID,
		"status":                "in_progress",
		"last_position_seconds": 42,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := toolLessonsProgress(ctx, map[string]any{
		"space_id": courseID, "member_id": memberID,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["total_lessons"].(int) != 2 {
		t.Fatalf("total_lessons = %v, want 2", m["total_lessons"])
	}
	if m["completed"].(int) != 1 {
		t.Fatalf("completed = %v, want 1", m["completed"])
	}
	if m["percent_complete"].(int) != 50 {
		t.Fatalf("percent_complete = %v, want 50", m["percent_complete"])
	}
}

func TestCourseProgress_Funnel(t *testing.T) {
	ctx, _, _, courseID, _, lessonAID, lessonBID := setupCourse(t)
	c, _ := toolCommunitiesGet(ctx, map[string]any{"slug": "main"})
	communityID := c.(Community).ID

	for _, name := range []string{"bob", "carol", "dave"} {
		m := mustCreateMember(t, ctx, communityID, name)
		// Everyone starts A.
		if _, err := toolLessonsMarkComplete(ctx, map[string]any{
			"lesson_id": lessonAID,
			"member_id": m.ID,
			"status":    "in_progress",
		}); err != nil {
			t.Fatal(err)
		}
		// Bob and carol complete A.
		if name != "dave" {
			if _, err := toolLessonsMarkComplete(ctx, map[string]any{
				"lesson_id": lessonAID, "member_id": m.ID,
			}); err != nil {
				t.Fatal(err)
			}
		}
		// Carol completes B too.
		if name == "carol" {
			if _, err := toolLessonsMarkComplete(ctx, map[string]any{
				"lesson_id": lessonBID, "member_id": m.ID,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	out, err := toolCourseProgress(ctx, map[string]any{"space_id": courseID})
	if err != nil {
		t.Fatal(err)
	}
	buckets := out.(map[string]any)["lessons"].([]CourseProgressBucket)
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	// Lesson A: started=3, completed=2.
	if buckets[0].Started != 3 || buckets[0].Completed != 2 {
		t.Fatalf("lesson A funnel wrong: %+v", buckets[0])
	}
	// Lesson B: started=1 (carol completed -> counts as started+completed), completed=1.
	if buckets[1].Started != 1 || buckets[1].Completed != 1 {
		t.Fatalf("lesson B funnel wrong: %+v", buckets[1])
	}
}

// ─── attach_video without ffmpeg bound ───────────────────────────

func TestLessonsAttachVideo_WithoutFFmpeg_AcceptsCallerDuration(t *testing.T) {
	ctx, _, _, _, _, lessonAID, _ := setupCourse(t)
	out, err := toolLessonsAttachVideo(ctx, map[string]any{
		"id":               lessonAID,
		"storage_key":      "f_abc123",
		"duration_seconds": 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	l := out.(Lesson)
	if l.VideoStorageKey == nil || *l.VideoStorageKey != "f_abc123" {
		t.Fatalf("storage_key not set: %+v", l.VideoStorageKey)
	}
	if l.VideoDurationSeconds == nil || *l.VideoDurationSeconds != 600 {
		t.Fatalf("duration not set: %+v", l.VideoDurationSeconds)
	}
}

func TestLessonsAttachVideo_WithoutFFmpeg_LeavesDurationNil(t *testing.T) {
	// No PlatformAPI is wired in the test ctx; probeDurationViaFFmpeg
	// returns (0, false), and the lesson gets video_storage_key but no
	// duration. That's the intended no-deps-bound behaviour.
	ctx, _, _, _, _, lessonAID, _ := setupCourse(t)
	out, err := toolLessonsAttachVideo(ctx, map[string]any{
		"id":          lessonAID,
		"storage_key": "f_def456",
	})
	if err != nil {
		t.Fatal(err)
	}
	l := out.(Lesson)
	if l.VideoDurationSeconds != nil {
		t.Fatalf("duration unexpectedly auto-filled: %v", *l.VideoDurationSeconds)
	}
}

// ─── lesson comments ─────────────────────────────────────────────

func TestLessonComments_PostAndList(t *testing.T) {
	ctx, _, memberID, _, _, lessonAID, _ := setupCourse(t)
	if _, err := toolLessonCommentsPost(ctx, map[string]any{
		"lesson_id": lessonAID, "member_id": memberID, "body": "great lesson",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := toolLessonCommentsList(ctx, map[string]any{
		"lesson_id": lessonAID,
	})
	if err != nil {
		t.Fatal(err)
	}
	comments := out.(map[string]any)["comments"].([]LessonComment)
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(comments))
	}
	if comments[0].Body != "great lesson" {
		t.Fatalf("body wrong")
	}
}

func TestCourseBuilder_MetadataAndAccess(t *testing.T) {
	ctx, _, memberID, courseID, _, _, _ := setupCourse(t)
	out, err := toolCoursesUpdateDetails(ctx, map[string]any{
		"space_id":              courseID,
		"summary":               "Fast start",
		"description":           "Full curriculum",
		"instructor_member_id":  memberID,
		"instructor_name":       "Alice",
		"level":                 "beginner",
		"tags":                  []any{"ops", "launch"},
		"price_cents":           9900,
		"currency":              "eur",
		"prerequisites":         []any{"Account"},
		"outcomes":              []any{"Ship"},
		"cover_storage_file_id": "123",
	})
	if err != nil {
		t.Fatal(err)
	}
	d := out.(CourseDetails)
	if d.Summary != "Fast start" || d.Currency != "EUR" || len(d.Tags) != 2 || d.CoverStorageFileID == nil {
		t.Fatalf("details not saved: %+v", d)
	}
	rule, err := toolEnrollmentRulesSet(ctx, map[string]any{
		"space_id":          courseID,
		"access_mode":       "paid",
		"requires_approval": true,
		"max_enrollments":   10,
		"starts_at":         "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rule.(EnrollmentRule).RequiresApproval || rule.(EnrollmentRule).AccessMode != "paid" {
		t.Fatalf("rule not saved: %+v", rule)
	}
	enroll, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID})
	if err != nil {
		t.Fatal(err)
	}
	if enroll.(CourseEnrollment).Status != "pending" {
		t.Fatalf("approval rule should force pending: %+v", enroll)
	}
}

func TestCourseBuilder_LessonAdjunctsAndAnalytics(t *testing.T) {
	ctx, _, memberID, courseID, sectionID, lessonAID, _ := setupCourse(t)
	if _, err := toolLessonResourcesAdd(ctx, map[string]any{
		"lesson_id": lessonAID, "storage_file_id": "456", "name": "worksheet.pdf", "kind": "pdf",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolQuizzesCreate(ctx, map[string]any{
		"lesson_id": lessonAID, "title": "Check", "questions": []any{map[string]any{"prompt": "Ready?"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolAssignmentsCreate(ctx, map[string]any{
		"lesson_id": lessonAID, "title": "Submit plan", "attachment_storage_file_id": "789",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCertificatesConfigure(ctx, map[string]any{
		"space_id": courseID, "enabled": true, "title": "Completion", "template_storage_file_id": "321",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolDripScheduleSet(ctx, map[string]any{
		"lesson_id": lessonAID, "release_after_days": 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	a, err := toolCourseAnalytics(ctx, map[string]any{"space_id": courseID})
	if err != nil {
		t.Fatal(err)
	}
	m := a.(map[string]any)
	if m["resources"].(int64) != 1 || m["quizzes"].(int64) != 1 || m["assignments"].(int64) != 1 || m["active_enrollments"].(int64) != 1 {
		t.Fatalf("analytics wrong: %+v", m)
	}
	if _, err := toolSectionsUpdate(ctx, map[string]any{"id": sectionID, "title": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolLessonsDelete(ctx, map[string]any{"id": lessonAID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonAID); err == nil {
		t.Fatal("lesson should be deleted")
	}
}

// ─── patch tools (0.1.x) ─────────────────────────────────────────

func TestCommunitiesUpdate_AndArchive(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	out, err := toolCommunitiesUpdate(ctx, map[string]any{
		"id": c.ID, "name": "Main v2", "description": "new desc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(Community).Name != "Main v2" || out.(Community).Description != "new desc" {
		t.Fatalf("update didn't apply")
	}
	if _, err := toolCommunitiesArchive(ctx, map[string]any{"id": c.ID}); err != nil {
		t.Fatal(err)
	}
	// Default list hides archived.
	list, _ := toolCommunitiesList(ctx, map[string]any{})
	if len(list.(map[string]any)["communities"].([]Community)) != 0 {
		t.Fatalf("archived community should be hidden by default")
	}
	// include_archived surfaces it.
	list, _ = toolCommunitiesList(ctx, map[string]any{"include_archived": true})
	if len(list.(map[string]any)["communities"].([]Community)) != 1 {
		t.Fatalf("include_archived should surface it")
	}
}

func TestThreadsPin_AndLock(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "feed")
	tOut, _ := toolThreadsCreate(ctx, map[string]any{
		"space_id": s.ID, "author_id": m.ID, "body": "hi",
	})
	threadID := tOut.(map[string]any)["thread"].(Thread).ID

	pinned, err := toolThreadsPin(ctx, map[string]any{"id": threadID, "pinned": true})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.(Thread).Pinned {
		t.Fatal("not pinned")
	}
	locked, err := toolThreadsLock(ctx, map[string]any{"id": threadID, "locked": true})
	if err != nil {
		t.Fatal(err)
	}
	if !locked.(Thread).Locked {
		t.Fatal("not locked")
	}
	// Locked thread should reject new posts.
	_, err = toolPostsCreate(ctx, map[string]any{
		"thread_id": threadID, "author_id": m.ID, "body": "no",
	})
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("locked thread should reject; got %v", err)
	}
}

func TestDMsMarkRead_AndUnreadCount(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	a := mustCreateMember(t, ctx, c.ID, "alice")
	b := mustCreateMember(t, ctx, c.ID, "bob")
	th, _ := toolDMsOpen(ctx, map[string]any{"participants": []any{a.ID, b.ID}})
	threadID := th.(DMThread).ID

	// alice sends two messages.
	for i := 0; i < 2; i++ {
		if _, err := toolDMsSend(ctx, map[string]any{
			"dm_thread_id": threadID, "author_id": a.ID, "body": "msg",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// bob has 2 unread.
	unread, _ := toolDMsUnreadCount(ctx, map[string]any{"member_id": b.ID})
	if unread.(map[string]any)["unread"].(int) != 2 {
		t.Fatalf("bob unread = %v, want 2", unread.(map[string]any)["unread"])
	}
	// list_threads also shows the per-thread badge.
	list, _ := toolDMsListThreads(ctx, map[string]any{"member_id": b.ID})
	threads := list.(map[string]any)["threads"].([]DMThread)
	if threads[0].UnreadCount != 2 {
		t.Fatalf("list_threads unread = %d, want 2", threads[0].UnreadCount)
	}
	// bob marks read; unread drops to 0.
	if _, err := toolDMsMarkRead(ctx, map[string]any{
		"dm_thread_id": threadID, "member_id": b.ID,
	}); err != nil {
		t.Fatal(err)
	}
	unread, _ = toolDMsUnreadCount(ctx, map[string]any{"member_id": b.ID})
	if unread.(map[string]any)["unread"].(int) != 0 {
		t.Fatalf("after mark_read, unread = %v, want 0", unread.(map[string]any)["unread"])
	}
}

func TestMembersUpdate_StatusEmitsExtraTopic(t *testing.T) {
	ctx, rec := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	rec.Reset()
	if _, err := toolMembersUpdate(ctx, map[string]any{
		"id": m.ID, "status": "suspended",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.WaitForTopic("member.updated", 100*time.Millisecond); !ok {
		t.Fatal("member.updated missing")
	}
	if _, ok := rec.WaitForTopic("member.status_changed", 100*time.Millisecond); !ok {
		t.Fatal("member.status_changed missing")
	}
}

// ─── Sanity check: 0.2 migration didn't break 0.1 flows ──────────

func TestPostInFeedSpace_StillWorksOnV2Schema(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "feed")
	if _, err := toolThreadsCreate(ctx, map[string]any{
		"space_id": s.ID, "author_id": m.ID, "body": "still works",
	}); err != nil {
		t.Fatal(err)
	}
}
