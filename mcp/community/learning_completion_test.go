package main

import (
	"strings"
	"testing"
)

func TestFinalQuizPassGraduatesAndIssuesCertificate(t *testing.T) {
	ctx, _, memberID, courseID, _, lessonA, lessonB := setupCourse(t)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCertificatesConfigure(ctx, map[string]any{"space_id": courseID, "enabled": true, "title": "Graduate", "require_quizzes_passed": true}); err != nil {
		t.Fatal(err)
	}
	out, err := toolQuizzesCreate(ctx, map[string]any{"lesson_id": lessonB, "title": "Final quiz", "questions": []any{map[string]any{"prompt": "2+2?", "options": []string{"4", "5"}, "correct_index": 0}}})
	if err != nil {
		t.Fatal(err)
	}
	quiz := out.(Quiz)
	for _, id := range []string{lessonA, lessonB} {
		if _, err := toolLessonsMarkComplete(ctx, map[string]any{"lesson_id": id, "member_id": memberID}); err != nil {
			t.Fatal(err)
		}
	}
	assertCompletion := func(want string, certificates int) {
		t.Helper()
		enrollment, err := loadCourseEnrollment(ctx.AppDB(), courseID, memberID)
		if err != nil || enrollment.Status != want {
			t.Fatalf("enrollment = %+v, err = %v; want %s", enrollment, err, want)
		}
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM issued_certificates WHERE space_id=? AND member_id=?`, courseID, memberID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != certificates {
			t.Fatalf("certificates = %d; want %d", count, certificates)
		}
	}
	assertCompletion("active", 0)
	submit := delegatedTool(t, "quiz_submit")
	for _, test := range []struct {
		answer       int
		status       string
		certificates int
	}{{1, "active", 0}, {0, "completed", 1}, {0, "completed", 1}} {
		if _, err := submit(userCallContext("auth-alice"), ctx, map[string]any{"quiz_id": quiz.ID, "member_id": "spoofed", "answers": []int{test.answer}}); err != nil {
			t.Fatal(err)
		}
		assertCompletion(test.status, test.certificates)
	}
}

func TestAssignmentsAcceptEvidenceWithoutWrittenBody(t *testing.T) {
	ctx, _, memberID, courseID, _, lessonID, _ := setupCourse(t)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET auth_user_id='auth-alice' WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnroll(ctx, map[string]any{"space_id": courseID, "member_id": memberID}); err != nil {
		t.Fatal(err)
	}
	out, err := toolAssignmentsCreate(ctx, map[string]any{"lesson_id": lessonID, "title": "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	assignment := out.(Assignment)
	// Represent an upload already registered by course_file_upload.
	if _, err := ctx.AppDB().Exec(`INSERT INTO learning_files(file_id,space_id,member_id,name) VALUES('42',?,?, 'proof.pdf')`, courseID, memberID); err != nil {
		t.Fatal(err)
	}
	submit := delegatedTool(t, "assignment_submit")
	for _, test := range []struct {
		name     string
		evidence map[string]any
		wantErr  bool
	}{
		{"link only, omitted body", map[string]any{"links": []any{"https://example.test/proof"}}, false},
		{"file only, empty body", map[string]any{"body": "", "files": []any{"42"}}, false},
		{"empty", map[string]any{}, true},
		{"whitespace", map[string]any{"body": " \n", "links": []any{" "}}, true},
		{"unowned file", map[string]any{"files": []any{"99"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := cloneArgs(test.evidence)
			args["assignment_id"] = assignment.ID
			args["member_id"] = "spoofed"
			out, err := submit(userCallContext("auth-alice"), ctx, args)
			if test.wantErr {
				if err == nil {
					t.Fatal("accepted invalid submission")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s := out.(AssignmentSubmission)
			if s.MemberID != memberID || strings.TrimSpace(s.Body) != "" || s.Status != "submitted" {
				t.Fatalf("submission: %+v", s)
			}
			var links, files string
			if err := ctx.AppDB().QueryRow(`SELECT links_json,files_json FROM assignment_submission_versions WHERE assignment_id=? AND member_id=? AND version=?`, assignment.ID, memberID, s.Version).Scan(&links, &files); err != nil {
				t.Fatal(err)
			}
			if len(s.Links)+len(s.Files) == 0 || (links == "[]" && files == "[]") {
				t.Fatal("evidence was not persisted")
			}
		})
	}
}
