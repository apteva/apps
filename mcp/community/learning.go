package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

// Limits are bytes, matching SQLite storage and event payload costs.
func validateContentArgs(args map[string]any) error {
	for key, value := range args {
		text, ok := value.(string)
		if !ok {
			continue
		}
		limit := 4096
		switch key {
		case "body", "description", "instructions":
			limit = 100_000
		case "bio", "summary":
			limit = 4000
		case "title", "name", "display_name", "instructor_name":
			limit = 300
		case "emoji":
			limit = 64
		}
		if !utf8.ValidString(text) {
			return fmt.Errorf("%s must be valid UTF-8", key)
		}
		if len(text) > limit {
			return fmt.Errorf("%s must be at most %d bytes", key, limit)
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return errors.New("invalid input payload")
	}
	if len(raw) > 1_000_000 {
		return errors.New("input payload must be at most 1000000 bytes")
	}
	return nil
}

type QuizQuestion struct {
	Prompt       string   `json:"prompt"`
	Options      []string `json:"options"`
	CorrectIndex int      `json:"correct_index"`
}

// Normalize authoring payloads once. Grading always stays on the server.
func quizQuestions(value any) ([]QuizQuestion, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("invalid quiz questions")
	}
	var entries []map[string]json.RawMessage
	if err = json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 || len(entries) > 100 {
		return nil, errors.New("questions must contain 1 to 100 multiple-choice questions")
	}
	out := make([]QuizQuestion, 0, len(entries))
	for _, entry := range entries {
		var q QuizQuestion
		if json.Unmarshal(entry["prompt"], &q.Prompt) != nil || strings.TrimSpace(q.Prompt) == "" || len(q.Prompt) > 4000 {
			return nil, errors.New("quiz prompt must contain 1 to 4000 bytes")
		}
		if json.Unmarshal(entry["options"], &q.Options) != nil || len(q.Options) < 2 || len(q.Options) > 20 {
			return nil, errors.New("each quiz question must have 2 to 20 options")
		}
		for _, option := range q.Options {
			if strings.TrimSpace(option) == "" || len(option) > 4000 {
				return nil, errors.New("quiz options must contain 1 to 4000 bytes")
			}
		}
		answer, ok := entry["correct_index"]
		if !ok {
			answer, ok = entry["correct_answer"]
		}
		if !ok || string(answer) == "null" || json.Unmarshal(answer, &q.CorrectIndex) != nil || q.CorrectIndex < 0 || q.CorrectIndex >= len(q.Options) {
			return nil, errors.New("quiz correct_index must identify an option (zero-based)")
		}
		out = append(out, q)
	}
	return out, nil
}

func publicQuizzes(quizzes []Quiz) []Quiz {
	out := make([]Quiz, len(quizzes))
	for i, quiz := range quizzes {
		raw, _ := json.Marshal(quiz.Questions)
		var questions []map[string]any
		if json.Unmarshal(raw, &questions) == nil {
			for j, question := range questions {
				questions[j] = map[string]any{"prompt": question["prompt"], "options": question["options"]}
			}
			quiz.Questions = questions
		} else {
			quiz.Questions = []any{}
		}
		out[i] = quiz
	}
	return out
}

type QuizAttempt struct {
	ID        int64  `json:"id"`
	QuizID    string `json:"quiz_id"`
	MemberID  string `json:"member_id"`
	Score     int    `json:"score"`
	Passed    bool   `json:"passed"`
	CreatedAt string `json:"created_at"`
}

type AssignmentSubmission struct {
	AssignmentID string `json:"assignment_id"`
	MemberID     string `json:"member_id"`
	Body         string `json:"body"`
	UpdatedAt    string `json:"updated_at"`
}

type IssuedCertificate struct {
	ID       string `json:"id"`
	SpaceID  string `json:"space_id"`
	MemberID string `json:"member_id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	IssuedAt string `json:"issued_at"`
}

func learningTools() []sdk.Tool {
	str := map[string]any{"type": "string"}
	return []sdk.Tool{
		{Name: "quiz_submit", Description: "Grade and persist a quiz attempt. Answers are zero-based option indices in question order.", InputSchema: schemaObject(map[string]any{"quiz_id": str, "member_id": str, "answers": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}, []string{"quiz_id", "member_id", "answers"}), Handler: toolQuizSubmit},
		{Name: "assignment_submit", Description: "Save a member's text assignment submission.", InputSchema: schemaObject(map[string]any{"assignment_id": str, "member_id": str, "body": str}, []string{"assignment_id", "member_id", "body"}), Handler: toolAssignmentSubmit},
		{Name: "learning_status", Description: "Fetch the member's latest quiz attempts and assignment submissions for a lesson.", InputSchema: schemaObject(map[string]any{"lesson_id": str, "member_id": str}, []string{"lesson_id", "member_id"}), Handler: toolLearningStatus},
		{Name: "issued_certificate_get", Description: "Fetch a member's earned course certificate.", InputSchema: schemaObject(map[string]any{"space_id": str, "member_id": str}, []string{"space_id", "member_id"}), Handler: toolIssuedCertificateGet},
		{Name: "lesson_file_url", Description: "Mint a short-lived URL for a lesson video, resource, or assignment attachment. The file must belong to the lesson.", InputSchema: schemaObject(map[string]any{"lesson_id": str, "file_id": str}, []string{"lesson_id", "file_id"}), Handler: toolLessonFileURL},
	}
}

func validateLearningMember(ctx *sdk.AppCtx, lessonID, memberID string) error {
	lesson, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID)
	if err != nil {
		return err
	}
	if err := verifyMember(ctx.AppDB(), lesson.CommunityID, memberID); err != nil {
		return err
	}
	spaceID, err := spaceByLesson(ctx.AppDB(), lessonID)
	if err != nil {
		return err
	}
	if err := ensureActiveEnrollment(ctx.AppDB(), spaceID, memberID); err != nil {
		return err
	}
	available, err := lessonAvailableToMember(ctx.AppDB(), lessonID, memberID)
	if err != nil {
		return err
	}
	if !available {
		return errors.New("lesson is not available yet")
	}
	return nil
}

func toolQuizSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := validateContentArgs(args); err != nil {
		return nil, err
	}
	id, err := mustStr(args, "quiz_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	quiz, err := loadQuiz(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := validateLearningMember(ctx, quiz.LessonID, memberID); err != nil {
		return nil, err
	}
	questions, err := quizQuestions(quiz.Questions)
	if err != nil {
		return nil, fmt.Errorf("quiz requires operator configuration: %w", err)
	}
	raw, err := json.Marshal(args["answers"])
	if err != nil {
		return nil, errors.New("invalid answers")
	}
	var answers []int
	if json.Unmarshal(raw, &answers) != nil || len(answers) != len(questions) {
		return nil, errors.New("answers must contain one option index per question")
	}
	var rawAnswers []json.RawMessage
	_ = json.Unmarshal(raw, &rawAnswers)
	for _, answer := range rawAnswers {
		if string(answer) == "null" {
			return nil, errors.New("answers must contain option indices, not null")
		}
	}
	correct := 0
	for i, answer := range answers {
		if answer < 0 || answer >= len(questions[i].Options) {
			return nil, errors.New("invalid quiz answer option")
		}
		if answer == questions[i].CorrectIndex {
			correct++
		}
	}
	score := correct * 100 / len(questions)
	passed := int64(score) >= quiz.PassingScore
	result, err := ctx.AppDB().Exec(`INSERT INTO quiz_attempts (quiz_id, member_id, answers_json, score, passed) VALUES (?, ?, ?, ?, ?)`, id, memberID, string(raw), score, passed)
	if err != nil {
		return nil, err
	}
	attemptID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	var attempt QuizAttempt
	err = ctx.AppDB().QueryRow(`SELECT id, quiz_id, member_id, score, passed, created_at FROM quiz_attempts WHERE id = ?`, attemptID).Scan(&attempt.ID, &attempt.QuizID, &attempt.MemberID, &attempt.Score, &attempt.Passed, &attempt.CreatedAt)
	if err != nil {
		return nil, err
	}
	emit(ctx, "quiz.submitted", map[string]any{"lesson_id": quiz.LessonID, "member_id": memberID, "quiz_id": quiz.ID})
	return attempt, nil
}

func toolAssignmentSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := validateContentArgs(args); err != nil {
		return nil, err
	}
	id, err := mustStr(args, "assignment_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	body, err := mustStr(args, "body")
	if err != nil {
		return nil, err
	}
	assignment, err := loadAssignment(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := validateLearningMember(ctx, assignment.LessonID, memberID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO assignment_submissions (assignment_id,member_id,body) VALUES (?,?,?) ON CONFLICT(assignment_id,member_id) DO UPDATE SET body=excluded.body, updated_at=CURRENT_TIMESTAMP`, id, memberID, body); err != nil {
		return nil, err
	}
	var submission AssignmentSubmission
	err = ctx.AppDB().QueryRow(`SELECT assignment_id, member_id, body, updated_at FROM assignment_submissions WHERE assignment_id=? AND member_id=?`, id, memberID).Scan(&submission.AssignmentID, &submission.MemberID, &submission.Body, &submission.UpdatedAt)
	if err != nil {
		return nil, err
	}
	emit(ctx, "assignment.submitted", map[string]any{"lesson_id": assignment.LessonID, "member_id": memberID})
	return submission, nil
}

func toolLearningStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := validateContentArgs(args); err != nil {
		return nil, err
	}
	id, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	if err := validateLearningMember(ctx, id, memberID); err != nil {
		return nil, err
	}
	attempts := []QuizAttempt{}
	rows, err := ctx.AppDB().Query(`SELECT a.id,a.quiz_id,a.member_id,a.score,a.passed,a.created_at FROM quiz_attempts a JOIN quizzes q ON q.id=a.quiz_id WHERE q.lesson_id=? AND a.member_id=? AND a.id=(SELECT MAX(latest.id) FROM quiz_attempts latest WHERE latest.quiz_id=a.quiz_id AND latest.member_id=a.member_id)`, id, memberID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a QuizAttempt
		if err := rows.Scan(&a.ID, &a.QuizID, &a.MemberID, &a.Score, &a.Passed, &a.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		attempts = append(attempts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	submissions := []AssignmentSubmission{}
	rows, err = ctx.AppDB().Query(`SELECT s.assignment_id,s.member_id,s.body,s.updated_at FROM assignment_submissions s JOIN assignments a ON a.id=s.assignment_id WHERE a.lesson_id=? AND s.member_id=?`, id, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s AssignmentSubmission
		if err := rows.Scan(&s.AssignmentID, &s.MemberID, &s.Body, &s.UpdatedAt); err != nil {
			return nil, err
		}
		submissions = append(submissions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"attempts": attempts, "submissions": submissions}, nil
}

func toolIssuedCertificateGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := validateContentArgs(args); err != nil {
		return nil, err
	}
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	space, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	if err := verifyMember(ctx.AppDB(), space.CommunityID, memberID); err != nil {
		return nil, err
	}
	var cert IssuedCertificate
	err = ctx.AppDB().QueryRow(`SELECT id,space_id,member_id,title,body,issued_at FROM issued_certificates WHERE space_id=? AND member_id=?`, spaceID, memberID).Scan(&cert.ID, &cert.SpaceID, &cert.MemberID, &cert.Title, &cert.Body, &cert.IssuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"certificate": nil}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"certificate": cert}, nil
}

func toolLessonFileURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := validateContentArgs(args); err != nil {
		return nil, err
	}
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	fileID, err := mustStr(args, "file_id")
	if err != nil {
		return nil, err
	}
	lesson, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID)
	if err != nil {
		return nil, err
	}
	allowed := lesson.VideoStorageKey != nil && *lesson.VideoStorageKey == fileID
	if !allowed {
		var count int
		err = ctx.AppDB().QueryRow(`SELECT (SELECT COUNT(*) FROM lesson_resources WHERE lesson_id=? AND storage_file_id=?) + (SELECT COUNT(*) FROM assignments WHERE lesson_id=? AND attachment_storage_file_id=?)`, lessonID, fileID, lessonID, fileID).Scan(&count)
		if err != nil {
			return nil, err
		}
		allowed = count > 0
	}
	if !allowed {
		return nil, errors.New("forbidden: file is not attached to this lesson")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("storage integration is unavailable")
	}
	var out struct {
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{"id": fileID, "ttl_seconds": 3600, "delivery": "direct"}, &out); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(out.URL)
	if err != nil || out.URL == "" || (parsed.Scheme != "https" && parsed.Scheme != "http" && !(strings.HasPrefix(out.URL, "/") && !strings.HasPrefix(out.URL, "//"))) {
		return nil, errors.New("storage returned an invalid file URL")
	}
	return out, nil
}
