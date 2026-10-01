package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestDelegatedReadsCannotOverrideResourceCommunity(t *testing.T) {
	ctx, _ := newTestCtx(t)
	a := mustCreateCommunity(t, ctx, "alpha", "A")
	b := mustCreateCommunity(t, ctx, "beta", "B")
	alice := mustCreateLinkedMember(t, ctx, a.ID, "alice", "auth-alice")
	bob := mustCreateMember(t, ctx, b.ID, "bob")
	space := mustCreateSpace(t, ctx, b.ID, "private", "forum")
	out, err := toolThreadsCreate(ctx, map[string]any{"space_id": space.ID, "author_id": bob.ID, "title": "Private", "body": "Secret"})
	if err != nil {
		t.Fatal(err)
	}
	thread := out.(map[string]any)["thread"].(Thread)
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"posts_list", map[string]any{"community_id": a.ID, "thread_id": thread.ID}},
		{"threads_list", map[string]any{"community_id": a.ID, "space_id": space.ID}},
		{"members_get", map[string]any{"community_id": a.ID, "id": bob.ID}},
		{"communities_get", map[string]any{"community_id": a.ID, "id": b.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := delegatedTool(t, test.name)(userCallContext("auth-alice"), ctx, test.args); err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Fatalf("cross-community read: %v", err)
			}
		})
	}
	// Self updates cannot resolve the literal "self" as a database ID.
	out, err = delegatedTool(t, "members_update")(userCallContext("auth-alice"), ctx, map[string]any{"community_id": a.ID, "id": "self", "display_name": "Alice Updated", "status": "suspended"})
	if err != nil || out.(Member).ID != alice.ID || out.(Member).Status != "active" {
		t.Fatalf("self update: %+v, %v", out, err)
	}
}

func TestMembersHTTPDefaultsAndGlobalRequestScope(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	response := httptest.NewRecorder()
	(&App{}).httpMembers(response, httptest.NewRequest("GET", "/members?community_id="+c.ID, nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), m.ID) {
		t.Fatalf("members: %d %s", response.Code, response.Body.String())
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	globalCtx = ctx.WithProject("")
	for _, test := range []struct {
		project string
		status  int
		present bool
	}{{"test-proj", 200, true}, {"other-proj", 200, false}, {"", 400, false}} {
		req := httptest.NewRequest("GET", "/communities", nil)
		req.Header.Set("X-Apteva-Project-ID", test.project)
		response = httptest.NewRecorder()
		(&App{}).httpCommunities(response, req)
		if response.Code != test.status || strings.Contains(response.Body.String(), c.ID) != test.present {
			t.Fatalf("project %q: %d %s", test.project, response.Code, response.Body.String())
		}
	}
	if scopeProject(globalCtx) != "" {
		t.Fatal("request mutated global project")
	}
}

func TestDMReadCursorDoesNotMissSameSecondOrUnseenMessages(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	a := mustCreateMember(t, ctx, c.ID, "alice")
	b := mustCreateMember(t, ctx, c.ID, "bob")
	out, err := toolDMsOpen(ctx, map[string]any{"participants": []any{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	thread := out.(DMThread)
	send := func(body string) DMMessage {
		t.Helper()
		out, err := toolDMsSend(ctx, map[string]any{"dm_thread_id": thread.ID, "author_id": b.ID, "body": body})
		if err != nil {
			t.Fatal(err)
		}
		return out.(DMMessage)
	}
	first := send("First")
	if _, err := toolDMsMarkRead(ctx, map[string]any{"dm_thread_id": thread.ID, "member_id": a.ID, "through_message_id": first.ID}); err != nil {
		t.Fatal(err)
	}
	second := send("Second")
	// Force identical timestamps to prove ordering is independent of precision.
	if _, err := ctx.AppDB().Exec(`UPDATE dm_messages SET created_at='2026-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	assertUnread := func(want int) {
		t.Helper()
		out, err := toolDMsUnreadCount(ctx, map[string]any{"member_id": a.ID})
		if err != nil || out.(map[string]any)["unread"] != want {
			t.Fatalf("unread %+v %v, want %d", out, err, want)
		}
		listed, err := toolDMsListThreads(ctx, map[string]any{"member_id": a.ID})
		if err != nil || listed.(map[string]any)["threads"].([]DMThread)[0].UnreadCount != want {
			t.Fatalf("thread unread: %+v %v", listed, err)
		}
	}
	assertUnread(1)
	// Re-reading an earlier page must neither regress the cursor nor read later messages.
	if _, err := toolDMsMarkRead(ctx, map[string]any{"dm_thread_id": thread.ID, "member_id": a.ID, "through_message_id": first.ID}); err != nil {
		t.Fatal(err)
	}
	assertUnread(1)
	if _, err := toolDMsMarkRead(ctx, map[string]any{"dm_thread_id": thread.ID, "member_id": a.ID, "through_message_id": second.ID}); err != nil {
		t.Fatal(err)
	}
	assertUnread(0)
	if _, err := ctx.AppDB().Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	send("After vacuum")
	assertUnread(1)
}

func TestRemovingNewestPostRecomputesThreadActivity(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	m := mustCreateMember(t, ctx, c.ID, "alice")
	s := mustCreateSpace(t, ctx, c.ID, "general", "forum")
	out, err := toolThreadsCreate(ctx, map[string]any{"space_id": s.ID, "author_id": m.ID, "body": "First"})
	if err != nil {
		t.Fatal(err)
	}
	thread := out.(map[string]any)["thread"].(Thread)
	first := out.(map[string]any)["first_post"].(*Post)
	out, err = toolPostsCreate(ctx, map[string]any{"thread_id": thread.ID, "author_id": m.ID, "body": "Newest"})
	if err != nil {
		t.Fatal(err)
	}
	second := out.(Post)
	if _, err := ctx.AppDB().Exec(`UPDATE posts SET created_at='2026-01-01 00:00:00' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolPostsRemove(ctx, map[string]any{"id": second.ID, "caller_member_id": m.ID}); err != nil {
		t.Fatal(err)
	}
	var last string
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT last_post_at,post_count FROM threads WHERE id=?`, thread.ID).Scan(&last, &count); err != nil {
		t.Fatal(err)
	}
	if last != "2026-01-01T00:00:00Z" || count != 1 {
		t.Fatalf("activity=%q count=%d", last, count)
	}
}

func TestLearningSubmissionsPersistAndAnswerKeysStayPrivate(t *testing.T) {
	ctx, _, memberID, courseID, _, lessonID, _ := setupCourse(t)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	out, err := toolQuizzesCreate(ctx, map[string]any{"lesson_id": lessonID, "title": "Quiz", "questions": []any{map[string]any{"prompt": "2+2?", "options": []string{"4", "5"}, "correct_index": 0}}})
	if err != nil {
		t.Fatal(err)
	}
	quiz := out.(Quiz)
	out, err = delegatedTool(t, "quizzes_list")(userCallContext("auth-alice"), ctx, map[string]any{"lesson_id": lessonID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "correct_index") {
		t.Fatalf("answer key leaked: %s", raw)
	}
	for _, test := range []struct {
		answer int
		score  int
		passed bool
	}{{1, 0, false}, {0, 100, true}} {
		out, err = delegatedTool(t, "quiz_submit")(userCallContext("auth-alice"), ctx, map[string]any{"quiz_id": quiz.ID, "member_id": "spoofed", "answers": []int{test.answer}})
		if err != nil {
			t.Fatal(err)
		}
		attempt := out.(QuizAttempt)
		if attempt.MemberID != memberID || attempt.Score != test.score || attempt.Passed != test.passed {
			t.Fatalf("attempt: %+v", attempt)
		}
	}
	out, err = toolAssignmentsCreate(ctx, map[string]any{"lesson_id": lessonID, "title": "Homework"})
	if err != nil {
		t.Fatal(err)
	}
	assignment := out.(Assignment)
	for _, body := range []string{"Draft", "Final answer"} {
		if _, err := delegatedTool(t, "assignment_submit")(userCallContext("auth-alice"), ctx, map[string]any{"assignment_id": assignment.ID, "member_id": "spoofed", "body": body}); err != nil {
			t.Fatal(err)
		}
	}
	out, err = delegatedTool(t, "learning_status")(userCallContext("auth-alice"), ctx, map[string]any{"lesson_id": lessonID, "member_id": "spoofed"})
	if err != nil {
		t.Fatal(err)
	}
	status := out.(map[string]any)
	if len(status["attempts"].([]QuizAttempt)) != 1 || status["attempts"].([]QuizAttempt)[0].Score != 100 || status["submissions"].([]AssignmentSubmission)[0].Body != "Final answer" {
		t.Fatalf("status: %+v", status)
	}
	// Draft lessons and cancelled enrollment deny both submissions and their history.
	if _, err := ctx.AppDB().Exec(`UPDATE course_enrollments SET status='cancelled' WHERE space_id=? AND member_id=?`, courseID, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := delegatedTool(t, "learning_status")(userCallContext("auth-alice"), ctx, map[string]any{"lesson_id": lessonID}); err == nil {
		t.Fatal("cancelled enrollment allowed learning history")
	}
}

func TestContentLimitsAndPagination(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	for i := 0; i < 3; i++ {
		mustCreateMember(t, ctx, c.ID, "member-"+string(rune('a'+i)))
	}
	out, err := toolMembersList(ctx, map[string]any{"community_id": c.ID, "limit": 1, "offset": 1})
	if err != nil || len(out.(map[string]any)["members"].([]Member)) != 1 {
		t.Fatalf("page: %+v %v", out, err)
	}
	first, _ := toolMembersList(ctx, map[string]any{"community_id": c.ID, "limit": 1})
	if out.(map[string]any)["members"].([]Member)[0].ID == first.(map[string]any)["members"].([]Member)[0].ID {
		t.Fatal("offset ignored")
	}
	if _, err := toolMembersUpdate(ctx, map[string]any{"id": out.(map[string]any)["members"].([]Member)[0].ID, "bio": strings.Repeat("x", 4001)}); err == nil {
		t.Fatal("oversized optional bio accepted")
	}
	for _, args := range []map[string]any{{"body": strings.Repeat("x", 100001)}, {"title": strings.Repeat("x", 301)}, {"questions": []string{strings.Repeat("x", 1000001)}}} {
		if validateContentArgs(args) == nil {
			t.Fatal("oversized content accepted")
		}
	}
}

func TestReadCursorMigrationPreservesExistingData(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.Contains(file, "012_") {
			break
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	_, err = db.Exec(`INSERT INTO communities(id,project_id,slug,name) VALUES ('c','p','main','Main'); INSERT INTO members(id,community_id,handle) VALUES ('a','c','a'),('b','c','b'); INSERT INTO dm_threads(id,community_id) VALUES ('dm','c'); INSERT INTO dm_participants(dm_thread_id,member_id,last_read_at) VALUES ('dm','a','2026-01-01 00:00:00'); INSERT INTO dm_messages(id,community_id,dm_thread_id,author_id,body,created_at) VALUES ('one','c','dm','b','old','2026-01-01 00:00:00'),('two','c','dm','b','new','2026-01-02 00:00:00')`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/012_learning_and_read_cursors.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var unread int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dm_messages m JOIN dm_participants p ON p.dm_thread_id=m.dm_thread_id WHERE m.seq>p.last_read_seq`).Scan(&unread); err != nil || unread != 1 {
		t.Fatalf("upgrade unread=%d %v", unread, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after upgrade")
	}
}

func TestLessonFileLinksAreBoundToEnrolledLesson(t *testing.T) {
	stub := newSalesPlatformStub()
	ctx, _, memberID, courseID, _, lessonID, _ := setupCourse(t, tk.WithPlatform(stub))
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE lessons SET video_storage_key='42' WHERE id=?`, lessonID); err != nil {
		t.Fatal(err)
	}
	call := delegatedTool(t, "lesson_file_url")
	if _, err := call(userCallContext("auth-alice"), ctx, map[string]any{"lesson_id": lessonID, "file_id": "42"}); err == nil {
		t.Fatal("unenrolled file access accepted")
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(userCallContext("auth-alice"), ctx, map[string]any{"lesson_id": lessonID, "file_id": "other"}); err == nil {
		t.Fatal("unattached file access accepted")
	}
	out, err := call(userCallContext("auth-alice"), ctx, map[string]any{"lesson_id": lessonID, "file_id": "42"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "signed-preview") || stub.signedURLTTL != 3600 {
		t.Fatalf("file URL: %s ttl=%d", raw, stub.signedURLTTL)
	}
}

func TestBundleUsesCallerProgressAndHidesQuizAnswers(t *testing.T) {
	ctx, _, memberID, courseID, _, lessonID, _ := setupCourse(t)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolLessonsMarkComplete(ctx, map[string]any{"lesson_id": lessonID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolQuizzesCreate(ctx, map[string]any{"lesson_id": lessonID, "title": "Question", "questions": []any{map[string]any{"prompt": "Ready?", "options": []string{"Yes", "No"}, "correct_index": 0}}}); err != nil {
		t.Fatal(err)
	}
	out, err := delegatedTool(t, "lesson_bundle_get")(userCallContext("auth-alice"), ctx, map[string]any{"id": lessonID, "member_id": "self"})
	if err != nil {
		t.Fatal(err)
	}
	lesson := out.(map[string]any)["lesson"].(Lesson)
	if lesson.Progress == nil || lesson.Progress.MemberID != memberID || lesson.Progress.Status != "complete" {
		t.Fatalf("caller progress: %+v", lesson.Progress)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "correct_index") {
		t.Fatalf("bundle answer leak: %s", raw)
	}
}

func TestDMHistoryPaginatesFromNewestAndEmptyMessagesAreAnArray(t *testing.T) {
	ctx, _ := newTestCtx(t)
	c := mustCreateCommunity(t, ctx, "main", "Main")
	a := mustCreateMember(t, ctx, c.ID, "alice")
	b := mustCreateMember(t, ctx, c.ID, "bob")
	out, err := toolDMsOpen(ctx, map[string]any{"participants": []any{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	thread := out.(DMThread)
	out, err = toolDMsGetThread(ctx, map[string]any{"id": thread.ID, "caller_member_id": a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(DMThreadView).Messages == nil {
		t.Fatal("empty messages encoded as null")
	}
	for _, body := range []string{"Oldest", "Middle", "Newest"} {
		if _, err := toolDMsSend(ctx, map[string]any{"dm_thread_id": thread.ID, "author_id": b.ID, "body": body}); err != nil {
			t.Fatal(err)
		}
	}
	out, err = toolDMsGetThread(ctx, map[string]any{"id": thread.ID, "caller_member_id": a.ID, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	messages := out.(DMThreadView).Messages
	if len(messages) != 2 || messages[0].Body != "Middle" || messages[1].Body != "Newest" {
		t.Fatalf("newest page: %+v", messages)
	}
	out, err = toolDMsGetThread(ctx, map[string]any{"id": thread.ID, "caller_member_id": a.ID, "limit": 2, "offset": 2})
	if err != nil {
		t.Fatal(err)
	}
	messages = out.(DMThreadView).Messages
	if len(messages) != 1 || messages[0].Body != "Oldest" {
		t.Fatalf("older page: %+v", messages)
	}
}

func TestQuizConfigurationRejectsInvalidAnswerKeys(t *testing.T) {
	for _, answer := range []any{nil, -1, 2, 0.5, "0"} {
		if _, err := quizQuestions([]any{map[string]any{"prompt": "Question", "options": []string{"A", "B"}, "correct_index": answer}}); err == nil {
			t.Fatalf("invalid correct_index accepted: %#v", answer)
		}
	}
}
