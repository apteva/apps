package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type CourseDetails struct {
	SpaceID             string   `json:"space_id"`
	Summary             string   `json:"summary"`
	Description         string   `json:"description"`
	InstructorMemberID  *string  `json:"instructor_member_id,omitempty"`
	InstructorName      string   `json:"instructor_name"`
	Level               string   `json:"level"`
	Tags                []string `json:"tags"`
	PriceCents          int64    `json:"price_cents"`
	Currency            string   `json:"currency"`
	Prerequisites       []string `json:"prerequisites"`
	Outcomes            []string `json:"outcomes"`
	CoverStorageFileID  *string  `json:"cover_storage_file_id,omitempty"`
	InstructorIDs       []string `json:"instructor_ids"`
	PrimaryInstructorID *string  `json:"primary_instructor_id,omitempty"`
	UpdatedAt           string   `json:"updated_at"`
}

type LessonResource struct {
	ID            string `json:"id"`
	LessonID      string `json:"lesson_id"`
	StorageFileID string `json:"storage_file_id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	ContentType   string `json:"content_type"`
	SizeBytes     *int64 `json:"size_bytes,omitempty"`
	Position      int64  `json:"position"`
	CreatedAt     string `json:"created_at"`
}

type Quiz struct {
	ID           string `json:"id"`
	LessonID     string `json:"lesson_id"`
	Title        string `json:"title"`
	Questions    any    `json:"questions"`
	PassingScore int64  `json:"passing_score"`
	Position     int64  `json:"position"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type Assignment struct {
	ID                      string  `json:"id"`
	LessonID                string  `json:"lesson_id"`
	Title                   string  `json:"title"`
	Instructions            string  `json:"instructions"`
	DueAfterDays            *int64  `json:"due_after_days,omitempty"`
	AttachmentStorageFileID *string `json:"attachment_storage_file_id,omitempty"`
	CreatedAt               string  `json:"created_at"`
	UpdatedAt               string  `json:"updated_at"`
}

type CourseCertificate struct {
	SpaceID               string  `json:"space_id"`
	Enabled               bool    `json:"enabled"`
	Title                 string  `json:"title"`
	Body                  string  `json:"body"`
	TemplateStorageFileID *string `json:"template_storage_file_id,omitempty"`
	IssueOnCompletion     bool    `json:"issue_on_completion"`
	UpdatedAt             string  `json:"updated_at"`
}

type DripSchedule struct {
	ID               string  `json:"id"`
	SpaceID          string  `json:"space_id"`
	LessonID         string  `json:"lesson_id"`
	ReleaseAt        *string `json:"release_at,omitempty"`
	ReleaseAfterDays *int64  `json:"release_after_days,omitempty"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type EnrollmentRule struct {
	SpaceID          string  `json:"space_id"`
	AccessMode       string  `json:"access_mode"`
	RequiresApproval bool    `json:"requires_approval"`
	MaxEnrollments   *int64  `json:"max_enrollments,omitempty"`
	StartsAt         *string `json:"starts_at,omitempty"`
	EndsAt           *string `json:"ends_at,omitempty"`
	UpdatedAt        string  `json:"updated_at"`
}

type CourseEnrollment struct {
	SpaceID         string  `json:"space_id"`
	MemberID        string  `json:"member_id"`
	Status          string  `json:"status"`
	Source          string  `json:"source"`
	SourceRef       *string `json:"source_ref,omitempty"`
	AccessExpiresAt *string `json:"access_expires_at,omitempty"`
	AccessRevokedAt *string `json:"access_revoked_at,omitempty"`
	EnrolledAt      string  `json:"enrolled_at"`
	CompletedAt     *string `json:"completed_at,omitempty"`
}

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

func courseBuilderTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "courses_get_details",
			Description: "Fetch course metadata, pricing, prerequisites, outcomes, enrollment rules, and certificate settings. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolCoursesGetDetails,
		},
		{
			Name:        "courses_update_details",
			Description: "Update course metadata. Args: space_id, summary?, description?, instructor_member_id?, instructor_name?, level?, tags?, price_cents?, currency?, prerequisites?, outcomes?, cover_storage_file_id?. File ids are storage app file ids.",
			InputSchema: schemaObject(map[string]any{
				"space_id":              map[string]any{"type": "string"},
				"summary":               map[string]any{"type": "string"},
				"description":           map[string]any{"type": "string"},
				"instructor_member_id":  map[string]any{"type": "string"},
				"instructor_name":       map[string]any{"type": "string"},
				"level":                 map[string]any{"type": "string"},
				"tags":                  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"price_cents":           map[string]any{"type": "integer"},
				"currency":              map[string]any{"type": "string"},
				"prerequisites":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"outcomes":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"cover_storage_file_id": map[string]any{"type": "string"},
			}, []string{"space_id"}),
			Handler: toolCoursesUpdateDetails,
		},
		{
			Name:        "sections_update",
			Description: "Update a section title or position. Args: id, title?, position?.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "position": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     toolSectionsUpdate,
		},
		{
			Name:        "sections_delete",
			Description: "Delete a section and its lessons. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
			Handler:     toolSectionsDelete,
		},
		{
			Name:        "lessons_delete",
			Description: "Delete a lesson and its progress, comments, resources, quizzes, assignments, and drip schedule. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
			Handler:     toolLessonsDelete,
		},
		{
			Name:        "lesson_resources_add",
			Description: "Attach a storage-backed resource to a lesson. Args: lesson_id, storage_file_id, name?, kind?, content_type?, size_bytes?, position?.",
			InputSchema: schemaObject(map[string]any{
				"lesson_id":       map[string]any{"type": "string"},
				"storage_file_id": map[string]any{"type": "string"},
				"name":            map[string]any{"type": "string"},
				"kind":            map[string]any{"type": "string"},
				"content_type":    map[string]any{"type": "string"},
				"size_bytes":      map[string]any{"type": "integer"},
				"position":        map[string]any{"type": "integer"},
			}, []string{"lesson_id", "storage_file_id"}),
			Handler: toolLessonResourcesAdd,
		},
		{
			Name:        "lesson_resources_list",
			Description: "List resources for a lesson. Args: lesson_id.",
			InputSchema: schemaObject(map[string]any{"lesson_id": map[string]any{"type": "string"}}, []string{"lesson_id"}),
			Handler:     toolLessonResourcesList,
		},
		{
			Name:        "lesson_bundle_get",
			Description: "Fetch one available lesson with its resources, quizzes, assignments, comments, and caller progress.",
			InputSchema: schemaObject(map[string]any{
				"id":        map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolLessonBundleGet,
		},
		{
			Name:        "lesson_resources_delete",
			Description: "Unlink a lesson resource. Does not delete the underlying storage file. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
			Handler:     toolLessonResourcesDelete,
		},
		{
			Name:        "quizzes_create",
			Description: "Create a quiz for a lesson. Args: lesson_id, title, questions? (JSON array), passing_score?, position?.",
			InputSchema: schemaObject(map[string]any{"lesson_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "questions": map[string]any{"type": "array"}, "passing_score": map[string]any{"type": "integer"}, "position": map[string]any{"type": "integer"}}, []string{"lesson_id", "title"}),
			Handler:     toolQuizzesCreate,
		},
		{
			Name:        "quizzes_update",
			Description: "Update a quiz. Args: id, title?, questions?, passing_score?, position?.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "questions": map[string]any{"type": "array"}, "passing_score": map[string]any{"type": "integer"}, "position": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     toolQuizzesUpdate,
		},
		{
			Name:        "quizzes_list",
			Description: "List quizzes for a lesson. Args: lesson_id.",
			InputSchema: schemaObject(map[string]any{"lesson_id": map[string]any{"type": "string"}}, []string{"lesson_id"}),
			Handler:     toolQuizzesList,
		},
		{
			Name:        "quizzes_delete",
			Description: "Delete a quiz. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
			Handler:     toolQuizzesDelete,
		},
		{
			Name:        "assignments_create",
			Description: "Create an assignment for a lesson. Args: lesson_id, title, instructions?, due_after_days?, attachment_storage_file_id?.",
			InputSchema: schemaObject(map[string]any{"lesson_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "instructions": map[string]any{"type": "string"}, "due_after_days": map[string]any{"type": "integer"}, "attachment_storage_file_id": map[string]any{"type": "string"}}, []string{"lesson_id", "title"}),
			Handler:     toolAssignmentsCreate,
		},
		{
			Name:        "assignments_update",
			Description: "Update an assignment. Args: id, title?, instructions?, due_after_days?, attachment_storage_file_id?.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "instructions": map[string]any{"type": "string"}, "due_after_days": map[string]any{"type": "integer"}, "attachment_storage_file_id": map[string]any{"type": "string"}}, []string{"id"}),
			Handler:     toolAssignmentsUpdate,
		},
		{
			Name:        "assignments_list",
			Description: "List assignments for a lesson. Args: lesson_id.",
			InputSchema: schemaObject(map[string]any{"lesson_id": map[string]any{"type": "string"}}, []string{"lesson_id"}),
			Handler:     toolAssignmentsList,
		},
		{
			Name:        "assignments_delete",
			Description: "Delete an assignment. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
			Handler:     toolAssignmentsDelete,
		},
		{
			Name:        "certificates_get",
			Description: "Fetch certificate settings for a course. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolCertificatesGet,
		},
		{
			Name:        "certificates_configure",
			Description: "Configure course certificates. Args: space_id, enabled?, title?, body?, template_storage_file_id?, issue_on_completion?. Template id is a storage app file id.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "template_storage_file_id": map[string]any{"type": "string"}, "issue_on_completion": map[string]any{"type": "boolean"}}, []string{"space_id"}),
			Handler:     toolCertificatesConfigure,
		},
		{
			Name:        "drip_schedule_set",
			Description: "Set or replace a lesson drip schedule. Args: lesson_id, release_at? (timestamp), release_after_days?.",
			InputSchema: schemaObject(map[string]any{"lesson_id": map[string]any{"type": "string"}, "release_at": map[string]any{"type": "string"}, "release_after_days": map[string]any{"type": "integer"}}, []string{"lesson_id"}),
			Handler:     toolDripScheduleSet,
		},
		{
			Name:        "drip_schedule_list",
			Description: "List drip schedules for a course. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolDripScheduleList,
		},
		{
			Name:        "enrollment_rules_get",
			Description: "Fetch enrollment rules for a course. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolEnrollmentRulesGet,
		},
		{
			Name:        "enrollment_rules_set",
			Description: "Set enrollment rules. Args: space_id, access_mode? (free|paid|invite|manual), requires_approval?, max_enrollments?, starts_at?, ends_at?.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}, "access_mode": map[string]any{"type": "string"}, "requires_approval": map[string]any{"type": "boolean"}, "max_enrollments": map[string]any{"type": "integer"}, "starts_at": map[string]any{"type": "string"}, "ends_at": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolEnrollmentRulesSet,
		},
		{
			Name:        "course_enroll",
			Description: "Enroll a member in a course. Args: space_id, member_id, status? (pending|active).",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}, "member_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}}, []string{"space_id", "member_id"}),
			Handler:     toolCourseEnroll,
		},
		{
			Name:        "course_enrollments_list",
			Description: "List enrollments for a course. Args: space_id, status?.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolCourseEnrollmentsList,
		},
		{
			Name:        "course_enrollment_update",
			Description: "Approve, reject, cancel, activate, or complete an enrollment. Operator only. Args: space_id, member_id, status.",
			InputSchema: schemaObject(map[string]any{
				"space_id":  map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
				"status":    map[string]any{"type": "string"},
			}, []string{"space_id", "member_id", "status"}),
			Handler: toolCourseEnrollmentUpdate,
		},
		{
			Name:        "course_analytics",
			Description: "Course analytics summary: content counts, enrollment counts, comments, and progress averages. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}}, []string{"space_id"}),
			Handler:     toolCourseAnalytics,
		},
	}
}

func toolCoursesGetDetails(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	d, err := loadCourseDetails(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	rule, err := loadEnrollmentRule(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	cert, err := loadCertificate(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"details": d, "enrollment_rules": rule, "certificate": cert}, nil
}

func toolCoursesUpdateDetails(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	space, err := ensureCourseSpace(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	cur, err := loadCourseDetails(db, spaceID)
	if err != nil {
		return nil, err
	}
	if v, ok := args["summary"].(string); ok {
		cur.Summary = v
	}
	if v, ok := args["description"].(string); ok {
		cur.Description = v
	}
	if v, ok := args["instructor_member_id"].(string); ok {
		if strings.TrimSpace(v) == "" {
			cur.InstructorMemberID = nil
		} else {
			if err := verifyMember(db, space.CommunityID, v); err != nil {
				return nil, err
			}
			cur.InstructorMemberID = &v
		}
	}
	if v, ok := args["instructor_name"].(string); ok {
		cur.InstructorName = v
	}
	if v, ok := args["level"].(string); ok {
		cur.Level = v
	}
	if v, ok := stringArrayArg(args, "tags"); ok {
		cur.Tags = v
	}
	if v, ok := intArg(args, "price_cents"); ok {
		if v < 0 {
			return nil, errors.New("price_cents cannot be negative")
		}
		cur.PriceCents = v
	}
	if v, ok := args["currency"].(string); ok && strings.TrimSpace(v) != "" {
		currency := strings.ToUpper(strings.TrimSpace(v))
		if !currencyRE.MatchString(currency) {
			return nil, errors.New("currency must be a three-letter ISO code")
		}
		cur.Currency = currency
	}
	if v, ok := stringArrayArg(args, "prerequisites"); ok {
		cur.Prerequisites = v
	}
	if v, ok := stringArrayArg(args, "outcomes"); ok {
		cur.Outcomes = v
	}
	if v, ok := storageFileArg(args, "cover_storage_file_id"); ok {
		if v == "" {
			cur.CoverStorageFileID = nil
		} else {
			if err := validateStorageFile(ctx, v); err != nil {
				return nil, err
			}
			cur.CoverStorageFileID = &v
		}
	}
	tagsJSON, _ := json.Marshal(cur.Tags)
	prereqJSON, _ := json.Marshal(cur.Prerequisites)
	outcomesJSON, _ := json.Marshal(cur.Outcomes)
	_, err = db.Exec(
		`INSERT INTO course_details
		   (space_id, summary, description, instructor_member_id, instructor_name, level, tags_json,
		    price_cents, currency, prerequisites_json, outcomes_json, cover_storage_file_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(space_id) DO UPDATE SET
		   summary = excluded.summary,
		   description = excluded.description,
		   instructor_member_id = excluded.instructor_member_id,
		   instructor_name = excluded.instructor_name,
		   level = excluded.level,
		   tags_json = excluded.tags_json,
		   price_cents = excluded.price_cents,
		   currency = excluded.currency,
		   prerequisites_json = excluded.prerequisites_json,
		   outcomes_json = excluded.outcomes_json,
		   cover_storage_file_id = excluded.cover_storage_file_id,
		   updated_at = CURRENT_TIMESTAMP`,
		spaceID, cur.Summary, cur.Description, cur.InstructorMemberID, cur.InstructorName, cur.Level, string(tagsJSON),
		cur.PriceCents, cur.Currency, string(prereqJSON), string(outcomesJSON), cur.CoverStorageFileID,
	)
	if err != nil {
		return nil, err
	}
	out, err := loadCourseDetails(db, spaceID)
	if err != nil {
		return nil, err
	}
	emit(ctx, "course.details_updated", map[string]any{"community_id": space.CommunityID, "space_id": spaceID})
	return out, nil
}

func toolSectionsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	s, err := loadSection(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), s.SpaceID); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["title"].(string); ok && strings.TrimSpace(v) != "" {
		sets = append(sets, "title = ?")
		vals = append(vals, v)
	}
	if v, ok := intArg(args, "position"); ok {
		sets = append(sets, "position = ?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(`UPDATE sections SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...); err != nil {
		return nil, err
	}
	return loadSection(ctx.AppDB(), id)
}

func toolSectionsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	s, err := loadSection(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), s.SpaceID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM sections WHERE id = ?`, id); err != nil {
		return nil, err
	}
	emit(ctx, "section.deleted", map[string]any{"space_id": s.SpaceID, "section_id": id})
	return map[string]any{"ok": true}, nil
}

func toolLessonsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	l, _, err := ensureLessonVisible(ctx, ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM lessons WHERE id = ?`, id); err != nil {
		return nil, err
	}
	emit(ctx, "lesson.deleted", map[string]any{"community_id": l.CommunityID, "lesson_id": id})
	return map[string]any{"ok": true}, nil
}

func toolLessonResourcesAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	fileID, err := mustStr(args, "storage_file_id")
	if err != nil {
		return nil, err
	}
	l, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID)
	if err != nil {
		return nil, err
	}
	if err := validateStorageFile(ctx, fileID); err != nil {
		return nil, err
	}
	name := strArg(args, "name", "")
	kind := strArg(args, "kind", "file")
	contentType := strArg(args, "content_type", "")
	var size any
	if v, ok := intArg(args, "size_bytes"); ok && v >= 0 {
		size = v
	}
	pos := int64(0)
	if v, ok := intArg(args, "position"); ok {
		pos = v
	} else {
		_ = ctx.AppDB().QueryRow(`SELECT COALESCE(MAX(position)+1, 0) FROM lesson_resources WHERE lesson_id = ?`, lessonID).Scan(&pos)
	}
	id := newID("lres")
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO lesson_resources (id, lesson_id, storage_file_id, name, kind, content_type, size_bytes, position)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, lessonID, fileID, name, kind, contentType, size, pos,
	); err != nil {
		return nil, err
	}
	r, err := loadLessonResource(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "lesson.resource_added", map[string]any{"community_id": l.CommunityID, "lesson_id": lessonID, "resource_id": id, "storage_file_id": fileID})
	return r, nil
}

func toolLessonResourcesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id, lesson_id, storage_file_id, name, kind, content_type, size_bytes, position, created_at FROM lesson_resources WHERE lesson_id = ? ORDER BY position, created_at`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LessonResource{}
	for rows.Next() {
		r, err := scanLessonResource(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"resources": out}, nil
}

func toolLessonBundleGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	lesson, err := toolLessonsGet(ctx, args)
	if err != nil {
		return nil, err
	}
	resources, err := toolLessonResourcesList(ctx, map[string]any{"lesson_id": id})
	if err != nil {
		return nil, err
	}
	quizzes, err := toolQuizzesList(ctx, map[string]any{"lesson_id": id})
	if err != nil {
		return nil, err
	}
	assignments, err := toolAssignmentsList(ctx, map[string]any{"lesson_id": id})
	if err != nil {
		return nil, err
	}
	comments, err := toolLessonCommentsList(ctx, map[string]any{"lesson_id": id, "limit": int64(200)})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"lesson":      lesson,
		"resources":   resources.(map[string]any)["resources"],
		"quizzes":     quizzes.(map[string]any)["quizzes"],
		"assignments": assignments.(map[string]any)["assignments"],
		"comments":    comments.(map[string]any)["comments"],
	}, nil
}

func toolLessonResourcesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	r, err := loadLessonResource(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), r.LessonID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM lesson_resources WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func toolQuizzesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	title, err := mustStr(args, "title")
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	questions, err := jsonArg(args, "questions", []any{})
	if err != nil {
		return nil, err
	}
	score := int64(70)
	if v, ok := intArg(args, "passing_score"); ok {
		score = v
	}
	if score < 0 || score > 100 {
		return nil, errors.New("passing_score must be between 0 and 100")
	}
	pos := int64(0)
	if v, ok := intArg(args, "position"); ok {
		pos = v
	}
	id := newID("quiz")
	if _, err := ctx.AppDB().Exec(`INSERT INTO quizzes (id, lesson_id, title, questions_json, passing_score, position) VALUES (?, ?, ?, ?, ?, ?)`, id, lessonID, title, questions, score, pos); err != nil {
		return nil, err
	}
	return loadQuiz(ctx.AppDB(), id)
}

func toolQuizzesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	q, err := loadQuiz(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), q.LessonID); err != nil {
		return nil, err
	}
	sets, vals := []string{}, []any{}
	if v, ok := args["title"].(string); ok && strings.TrimSpace(v) != "" {
		sets = append(sets, "title = ?")
		vals = append(vals, v)
	}
	if _, ok := args["questions"]; ok {
		j, err := jsonArg(args, "questions", []any{})
		if err != nil {
			return nil, err
		}
		sets = append(sets, "questions_json = ?")
		vals = append(vals, j)
	}
	if v, ok := intArg(args, "passing_score"); ok {
		if v < 0 || v > 100 {
			return nil, errors.New("passing_score must be between 0 and 100")
		}
		sets = append(sets, "passing_score = ?")
		vals = append(vals, v)
	}
	if v, ok := intArg(args, "position"); ok {
		sets = append(sets, "position = ?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(`UPDATE quizzes SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...); err != nil {
		return nil, err
	}
	return loadQuiz(ctx.AppDB(), id)
}

func toolQuizzesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id, lesson_id, title, questions_json, passing_score, position, created_at, updated_at FROM quizzes WHERE lesson_id = ? ORDER BY position, created_at`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Quiz{}
	for rows.Next() {
		q, err := scanQuiz(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"quizzes": out}, nil
}

func toolQuizzesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return deleteByIDAfterLessonCheck(ctx, args, "quizzes", "quiz")
}

func toolAssignmentsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	title, err := mustStr(args, "title")
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	var attach any
	if v, ok := storageFileArg(args, "attachment_storage_file_id"); ok && v != "" {
		if err := validateStorageFile(ctx, v); err != nil {
			return nil, err
		}
		attach = v
	}
	var due any
	if v, ok := intArg(args, "due_after_days"); ok {
		if v < 0 {
			return nil, errors.New("due_after_days cannot be negative")
		}
		due = v
	}
	id := newID("asgn")
	if _, err := ctx.AppDB().Exec(`INSERT INTO assignments (id, lesson_id, title, instructions, due_after_days, attachment_storage_file_id) VALUES (?, ?, ?, ?, ?, ?)`, id, lessonID, title, strArg(args, "instructions", ""), due, attach); err != nil {
		return nil, err
	}
	return loadAssignment(ctx.AppDB(), id)
}

func toolAssignmentsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	a, err := loadAssignment(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), a.LessonID); err != nil {
		return nil, err
	}
	sets, vals := []string{}, []any{}
	if v, ok := args["title"].(string); ok && strings.TrimSpace(v) != "" {
		sets = append(sets, "title = ?")
		vals = append(vals, v)
	}
	if v, ok := args["instructions"].(string); ok {
		sets = append(sets, "instructions = ?")
		vals = append(vals, v)
	}
	if v, ok := intArg(args, "due_after_days"); ok {
		if v < 0 {
			return nil, errors.New("due_after_days cannot be negative")
		}
		sets = append(sets, "due_after_days = ?")
		vals = append(vals, v)
	}
	if v, ok := storageFileArg(args, "attachment_storage_file_id"); ok {
		sets = append(sets, "attachment_storage_file_id = ?")
		if v == "" {
			vals = append(vals, nil)
		} else {
			if err := validateStorageFile(ctx, v); err != nil {
				return nil, err
			}
			vals = append(vals, v)
		}
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(`UPDATE assignments SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...); err != nil {
		return nil, err
	}
	return loadAssignment(ctx.AppDB(), id)
}

func toolAssignmentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id, lesson_id, title, instructions, due_after_days, attachment_storage_file_id, created_at, updated_at FROM assignments WHERE lesson_id = ? ORDER BY created_at`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Assignment{}
	for rows.Next() {
		a, err := scanAssignment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"assignments": out}, nil
}

func toolAssignmentsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return deleteByIDAfterLessonCheck(ctx, args, "assignments", "assignment")
}

func toolCertificatesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	c, err := loadCertificate(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func toolCertificatesConfigure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if _, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	cur, err := loadCertificate(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	if v, ok := args["enabled"].(bool); ok {
		cur.Enabled = v
	}
	if v, ok := args["title"].(string); ok {
		cur.Title = v
	}
	if v, ok := args["body"].(string); ok {
		cur.Body = v
	}
	if v, ok := args["issue_on_completion"].(bool); ok {
		cur.IssueOnCompletion = v
	}
	if v, ok := storageFileArg(args, "template_storage_file_id"); ok {
		if v == "" {
			cur.TemplateStorageFileID = nil
		} else {
			if err := validateStorageFile(ctx, v); err != nil {
				return nil, err
			}
			cur.TemplateStorageFileID = &v
		}
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO course_certificates (space_id, enabled, title, body, template_storage_file_id, issue_on_completion, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(space_id) DO UPDATE SET
		   enabled = excluded.enabled,
		   title = excluded.title,
		   body = excluded.body,
		   template_storage_file_id = excluded.template_storage_file_id,
		   issue_on_completion = excluded.issue_on_completion,
		   updated_at = CURRENT_TIMESTAMP`,
		spaceID, boolToInt(cur.Enabled), cur.Title, cur.Body, cur.TemplateStorageFileID, boolToInt(cur.IssueOnCompletion),
	); err != nil {
		return nil, err
	}
	return loadCertificate(ctx.AppDB(), spaceID)
}

func toolDripScheduleSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	_, spaceID, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID)
	if err != nil {
		return nil, err
	}
	releaseAt := nullableString(args, "release_at")
	var releaseAfter any
	if v, ok := intArg(args, "release_after_days"); ok {
		if v < 0 {
			return nil, errors.New("release_after_days cannot be negative")
		}
		releaseAfter = v
	}
	if releaseAt != nil && releaseAfter != nil {
		return nil, errors.New("set release_at or release_after_days, not both")
	}
	if releaseAt != nil {
		if _, err := time.Parse(time.RFC3339, releaseAt.(string)); err != nil {
			return nil, errors.New("release_at must be RFC3339")
		}
	}
	id := newID("drip")
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO drip_schedules (id, space_id, lesson_id, release_at, release_after_days, updated_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(lesson_id) DO UPDATE SET
		   release_at = excluded.release_at,
		   release_after_days = excluded.release_after_days,
		   updated_at = CURRENT_TIMESTAMP`,
		id, spaceID, lessonID, releaseAt, releaseAfter,
	); err != nil {
		return nil, err
	}
	var outID string
	_ = ctx.AppDB().QueryRow(`SELECT id FROM drip_schedules WHERE lesson_id = ?`, lessonID).Scan(&outID)
	return loadDripSchedule(ctx.AppDB(), outID)
}

func toolDripScheduleList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id, space_id, lesson_id, release_at, release_after_days, created_at, updated_at FROM drip_schedules WHERE space_id = ? ORDER BY release_at, created_at`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DripSchedule{}
	for rows.Next() {
		d, err := scanDripSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"schedules": out}, nil
}

func toolEnrollmentRulesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	return loadEnrollmentRule(ctx.AppDB(), spaceID)
}

func toolEnrollmentRulesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if _, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	cur, err := loadEnrollmentRule(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	if v, ok := args["access_mode"].(string); ok && v != "" {
		if !map[string]bool{"free": true, "paid": true, "invite": true, "manual": true}[v] {
			return nil, fmt.Errorf("access_mode %q invalid", v)
		}
		cur.AccessMode = v
	}
	if v, ok := args["requires_approval"].(bool); ok {
		cur.RequiresApproval = v
	}
	if v, ok := intArg(args, "max_enrollments"); ok {
		if v <= 0 {
			cur.MaxEnrollments = nil
		} else {
			cur.MaxEnrollments = &v
		}
	}
	if v, ok := args["starts_at"].(string); ok {
		if v == "" {
			cur.StartsAt = nil
		} else {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return nil, errors.New("starts_at must be RFC3339")
			}
			cur.StartsAt = &v
		}
	}
	if v, ok := args["ends_at"].(string); ok {
		if v == "" {
			cur.EndsAt = nil
		} else {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return nil, errors.New("ends_at must be RFC3339")
			}
			cur.EndsAt = &v
		}
	}
	if cur.StartsAt != nil && cur.EndsAt != nil {
		start, _ := time.Parse(time.RFC3339, *cur.StartsAt)
		end, _ := time.Parse(time.RFC3339, *cur.EndsAt)
		if !start.Before(end) {
			return nil, errors.New("starts_at must be before ends_at")
		}
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO enrollment_rules (space_id, access_mode, requires_approval, max_enrollments, starts_at, ends_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(space_id) DO UPDATE SET
		   access_mode = excluded.access_mode,
		   requires_approval = excluded.requires_approval,
		   max_enrollments = excluded.max_enrollments,
		   starts_at = excluded.starts_at,
		   ends_at = excluded.ends_at,
		   updated_at = CURRENT_TIMESTAMP`,
		spaceID, cur.AccessMode, boolToInt(cur.RequiresApproval), cur.MaxEnrollments, cur.StartsAt, cur.EndsAt,
	); err != nil {
		return nil, err
	}
	return loadEnrollmentRule(ctx.AppDB(), spaceID)
}

func toolCourseEnroll(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
	rule, err := loadEnrollmentRule(ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	delegated := strArg(args, "_viewer_member_id", "") != ""
	if delegated && rule.AccessMode != "free" {
		return nil, fmt.Errorf("%s courses require operator approval or an external purchase/invitation", rule.AccessMode)
	}
	now := time.Now().UTC()
	if rule.StartsAt != nil {
		start, err := time.Parse(time.RFC3339, *rule.StartsAt)
		if err != nil || now.Before(start) {
			return nil, errors.New("course enrollment has not opened")
		}
	}
	if rule.EndsAt != nil {
		end, err := time.Parse(time.RFC3339, *rule.EndsAt)
		if err != nil || now.After(end) {
			return nil, errors.New("course enrollment has closed")
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if rule.MaxEnrollments != nil {
		var count int64
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM course_enrollments
			  WHERE space_id = ? AND member_id <> ? AND status IN ('pending','active','completed')`,
			spaceID, memberID,
		).Scan(&count); err != nil {
			return nil, err
		}
		if count >= *rule.MaxEnrollments {
			return nil, errors.New("course enrollment limit reached")
		}
	}
	status := strArg(args, "status", "active")
	if delegated {
		status = "active"
	}
	if rule.RequiresApproval && status == "active" {
		status = "pending"
	}
	if !map[string]bool{"pending": true, "active": true}[status] {
		return nil, fmt.Errorf("status %q invalid", status)
	}
	if _, err := tx.Exec(
		`INSERT INTO course_enrollments (space_id, member_id, status, source, source_ref, access_expires_at, access_revoked_at)
		 VALUES (?, ?, ?, 'manual', NULL, NULL, NULL)
		 ON CONFLICT(space_id, member_id) DO UPDATE SET
		   status = excluded.status,
		   source = 'manual',
		   source_ref = NULL,
		   access_expires_at = NULL,
		   access_revoked_at = NULL,
		   completed_at = CASE WHEN excluded.status = 'completed' THEN COALESCE(course_enrollments.completed_at, CURRENT_TIMESTAMP) ELSE NULL END`,
		spaceID, memberID, status,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	emit(ctx, "course.enrolled", map[string]any{"community_id": space.CommunityID, "space_id": spaceID, "member_id": memberID, "status": status})
	return loadCourseEnrollment(ctx.AppDB(), spaceID, memberID)
}

func toolCourseEnrollmentUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	status, err := mustStr(args, "status")
	if err != nil {
		return nil, err
	}
	if !map[string]bool{"pending": true, "active": true, "rejected": true, "cancelled": true, "completed": true}[status] {
		return nil, fmt.Errorf("status %q invalid", status)
	}
	space, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE course_enrollments
		    SET status = ?,
		        source = CASE WHEN ? = 'active' THEN 'manual' ELSE source END,
		        source_ref = CASE WHEN ? = 'active' THEN NULL ELSE source_ref END,
		        access_expires_at = CASE WHEN ? = 'active' THEN NULL ELSE access_expires_at END,
		        access_revoked_at = CASE WHEN ? = 'active' THEN NULL ELSE access_revoked_at END,
		        completed_at = CASE WHEN ? = 'completed' THEN COALESCE(completed_at, CURRENT_TIMESTAMP) ELSE NULL END
		  WHERE space_id = ? AND member_id = ?`,
		status, status, status, status, status, status, spaceID, memberID,
	)
	if err != nil {
		return nil, err
	}
	if changed, _ := res.RowsAffected(); changed == 0 {
		return nil, errors.New("enrollment not found")
	}
	emit(ctx, "course.enrollment_updated", map[string]any{
		"community_id": space.CommunityID, "space_id": spaceID, "member_id": memberID, "status": status,
	})
	return loadCourseEnrollment(ctx.AppDB(), spaceID, memberID)
}

func toolCourseEnrollmentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	status := strArg(args, "status", "")
	q := `SELECT space_id, member_id, status, source, source_ref, access_expires_at, access_revoked_at,
	             enrolled_at, completed_at
	        FROM course_enrollments WHERE space_id = ?`
	vals := []any{spaceID}
	if status != "" {
		q += ` AND status = ?`
		vals = append(vals, status)
	}
	q += ` ORDER BY enrolled_at DESC`
	rows, err := ctx.AppDB().Query(q, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CourseEnrollment{}
	for rows.Next() {
		e, err := scanCourseEnrollment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"enrollments": out}, nil
}

func toolCourseAnalytics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	count := func(q string, vals ...any) (int64, error) {
		var n int64
		err := db.QueryRow(q, vals...).Scan(&n)
		return n, err
	}
	sections, err := count(`SELECT COUNT(*) FROM sections WHERE space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	lessons, err := count(`SELECT COUNT(*) FROM lessons l JOIN sections s ON s.id = l.section_id WHERE s.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	published, err := count(`SELECT COUNT(*) FROM lessons l JOIN sections s ON s.id = l.section_id WHERE s.space_id = ? AND l.published_at IS NOT NULL`, spaceID)
	if err != nil {
		return nil, err
	}
	resources, err := count(`SELECT COUNT(*) FROM lesson_resources r JOIN lessons l ON l.id = r.lesson_id JOIN sections s ON s.id = l.section_id WHERE s.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	quizzes, err := count(`SELECT COUNT(*) FROM quizzes q JOIN lessons l ON l.id = q.lesson_id JOIN sections s ON s.id = l.section_id WHERE s.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	assignments, err := count(`SELECT COUNT(*) FROM assignments a JOIN lessons l ON l.id = a.lesson_id JOIN sections s ON s.id = l.section_id WHERE s.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	comments, err := count(`SELECT COUNT(*) FROM lesson_comments c JOIN lessons l ON l.id = c.lesson_id JOIN sections s ON s.id = l.section_id WHERE s.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	enrollments, err := count(`SELECT COUNT(*) FROM course_enrollments WHERE space_id = ? AND status IN ('active','completed')`, spaceID)
	if err != nil {
		return nil, err
	}
	progressRows, err := count(`SELECT COUNT(*) FROM lesson_progress lp JOIN lessons l ON l.id = lp.lesson_id JOIN sections s ON s.id = l.section_id WHERE s.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	completedRows, err := count(`SELECT COUNT(*) FROM lesson_progress lp JOIN lessons l ON l.id = lp.lesson_id JOIN sections s ON s.id = l.section_id WHERE s.space_id = ? AND lp.status = 'complete'`, spaceID)
	if err != nil {
		return nil, err
	}
	avg := 0
	if progressRows > 0 {
		avg = int((completedRows * 100) / progressRows)
	}
	return map[string]any{
		"space_id":                    spaceID,
		"sections":                    sections,
		"lessons":                     lessons,
		"published_lessons":           published,
		"resources":                   resources,
		"quizzes":                     quizzes,
		"assignments":                 assignments,
		"comments":                    comments,
		"active_enrollments":          enrollments,
		"progress_rows":               progressRows,
		"completed_progress_rows":     completedRows,
		"progress_completion_percent": avg,
	}, nil
}

func ensureCourseSpace(ctx *sdk.AppCtx, db *sql.DB, spaceID string) (Space, error) {
	s, err := ensureSpaceVisible(ctx, db, spaceID)
	if err != nil {
		return s, err
	}
	if s.Kind != "course" {
		return s, fmt.Errorf("space %q is %s, not course", spaceID, s.Kind)
	}
	return s, nil
}

func validateStorageFile(ctx *sdk.AppCtx, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("storage file id required")
	}
	if ctx == nil || ctx.PlatformAPI() == nil || ctx.IntegrationFor("storage") == nil {
		return nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("storage file id %q must be a numeric storage.files id", id)
	}
	args := map[string]any{"id": n}
	if pid := scopeProject(ctx); pid != "" {
		args["project_id"] = pid
	}
	var out struct {
		ID    int64 `json:"id"`
		Found bool  `json:"found"`
		File  any   `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", args, &out); err != nil {
		return fmt.Errorf("storage.files_get(%s): %w", id, err)
	}
	if out.ID == 0 && !out.Found && out.File == nil {
		return fmt.Errorf("storage file %q not found", id)
	}
	return nil
}

func storageFileArg(args map[string]any, key string) (string, bool) {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v), true
	case float64:
		if v != float64(int64(v)) {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	default:
		return "", false
	}
}

func stringArrayArg(args map[string]any, key string) ([]string, bool) {
	raw, ok := args[key]
	if !ok {
		return nil, false
	}
	switch v := raw.(type) {
	case []string:
		out := append([]string(nil), v...)
		return out, true
	case []any:
		out := []string{}
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out, true
	case string:
		if strings.TrimSpace(v) == "" {
			return []string{}, true
		}
		parts := strings.Split(v, ",")
		out := []string{}
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func jsonArg(args map[string]any, key string, def any) (string, error) {
	raw, ok := args[key]
	if !ok {
		raw = def
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func nullableString(args map[string]any, key string) any {
	if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func loadCourseDetails(db *sql.DB, spaceID string) (CourseDetails, error) {
	d := CourseDetails{SpaceID: spaceID, Tags: []string{}, Prerequisites: []string{}, Outcomes: []string{}, InstructorIDs: []string{}, Currency: "USD"}
	var instr, cover, primaryInstructor sql.NullString
	var tagsJSON, prereqJSON, outcomesJSON, instructorIDsJSON string
	err := db.QueryRow(
		`SELECT space_id, summary, description, instructor_member_id, instructor_name, level, tags_json,
		        price_cents, currency, prerequisites_json, outcomes_json, cover_storage_file_id,
		        instructor_ids_json, primary_instructor_id, updated_at
		 FROM course_details WHERE space_id = ?`, spaceID,
	).Scan(&d.SpaceID, &d.Summary, &d.Description, &instr, &d.InstructorName, &d.Level, &tagsJSON, &d.PriceCents, &d.Currency, &prereqJSON, &outcomesJSON, &cover, &instructorIDsJSON, &primaryInstructor, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if instr.Valid {
		d.InstructorMemberID = &instr.String
	}
	if cover.Valid {
		d.CoverStorageFileID = &cover.String
	}
	if primaryInstructor.Valid {
		d.PrimaryInstructorID = &primaryInstructor.String
	}
	_ = json.Unmarshal([]byte(tagsJSON), &d.Tags)
	_ = json.Unmarshal([]byte(prereqJSON), &d.Prerequisites)
	_ = json.Unmarshal([]byte(outcomesJSON), &d.Outcomes)
	_ = json.Unmarshal([]byte(instructorIDsJSON), &d.InstructorIDs)
	return d, nil
}

func scanLessonResource(scan func(...any) error) (LessonResource, error) {
	var r LessonResource
	var size sql.NullInt64
	if err := scan(&r.ID, &r.LessonID, &r.StorageFileID, &r.Name, &r.Kind, &r.ContentType, &size, &r.Position, &r.CreatedAt); err != nil {
		return r, err
	}
	if size.Valid {
		r.SizeBytes = &size.Int64
	}
	return r, nil
}

func loadLessonResource(db *sql.DB, id string) (LessonResource, error) {
	row := db.QueryRow(`SELECT id, lesson_id, storage_file_id, name, kind, content_type, size_bytes, position, created_at FROM lesson_resources WHERE id = ?`, id)
	r, err := scanLessonResource(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("lesson_resource %q not found", id)
	}
	return r, err
}

func scanQuiz(scan func(...any) error) (Quiz, error) {
	var q Quiz
	var raw string
	if err := scan(&q.ID, &q.LessonID, &q.Title, &raw, &q.PassingScore, &q.Position, &q.CreatedAt, &q.UpdatedAt); err != nil {
		return q, err
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		q.Questions = parsed
	} else {
		q.Questions = []any{}
	}
	return q, nil
}

func loadQuiz(db *sql.DB, id string) (Quiz, error) {
	row := db.QueryRow(`SELECT id, lesson_id, title, questions_json, passing_score, position, created_at, updated_at FROM quizzes WHERE id = ?`, id)
	q, err := scanQuiz(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return q, fmt.Errorf("quiz %q not found", id)
	}
	return q, err
}

func scanAssignment(scan func(...any) error) (Assignment, error) {
	var a Assignment
	var due sql.NullInt64
	var attach sql.NullString
	if err := scan(&a.ID, &a.LessonID, &a.Title, &a.Instructions, &due, &attach, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return a, err
	}
	if due.Valid {
		a.DueAfterDays = &due.Int64
	}
	if attach.Valid {
		a.AttachmentStorageFileID = &attach.String
	}
	return a, nil
}

func loadAssignment(db *sql.DB, id string) (Assignment, error) {
	row := db.QueryRow(`SELECT id, lesson_id, title, instructions, due_after_days, attachment_storage_file_id, created_at, updated_at FROM assignments WHERE id = ?`, id)
	a, err := scanAssignment(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("assignment %q not found", id)
	}
	return a, err
}

func loadCertificate(db *sql.DB, spaceID string) (CourseCertificate, error) {
	c := CourseCertificate{SpaceID: spaceID, IssueOnCompletion: true}
	var enabled, issue int
	var template sql.NullString
	err := db.QueryRow(`SELECT space_id, enabled, title, body, template_storage_file_id, issue_on_completion, updated_at FROM course_certificates WHERE space_id = ?`, spaceID).
		Scan(&c.SpaceID, &enabled, &c.Title, &c.Body, &template, &issue, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.Enabled = enabled != 0
	c.IssueOnCompletion = issue != 0
	if template.Valid {
		c.TemplateStorageFileID = &template.String
	}
	return c, nil
}

func scanDripSchedule(scan func(...any) error) (DripSchedule, error) {
	var d DripSchedule
	var releaseAt sql.NullString
	var after sql.NullInt64
	if err := scan(&d.ID, &d.SpaceID, &d.LessonID, &releaseAt, &after, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return d, err
	}
	if releaseAt.Valid {
		d.ReleaseAt = &releaseAt.String
	}
	if after.Valid {
		d.ReleaseAfterDays = &after.Int64
	}
	return d, nil
}

func loadDripSchedule(db *sql.DB, id string) (DripSchedule, error) {
	row := db.QueryRow(`SELECT id, space_id, lesson_id, release_at, release_after_days, created_at, updated_at FROM drip_schedules WHERE id = ?`, id)
	d, err := scanDripSchedule(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return d, fmt.Errorf("drip_schedule %q not found", id)
	}
	return d, err
}

func loadEnrollmentRule(db *sql.DB, spaceID string) (EnrollmentRule, error) {
	r := EnrollmentRule{SpaceID: spaceID, AccessMode: "free"}
	var approval int
	var max sql.NullInt64
	var starts, ends sql.NullString
	err := db.QueryRow(`SELECT space_id, access_mode, requires_approval, max_enrollments, starts_at, ends_at, updated_at FROM enrollment_rules WHERE space_id = ?`, spaceID).
		Scan(&r.SpaceID, &r.AccessMode, &approval, &max, &starts, &ends, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.RequiresApproval = approval != 0
	if max.Valid {
		r.MaxEnrollments = &max.Int64
	}
	if starts.Valid {
		r.StartsAt = &starts.String
	}
	if ends.Valid {
		r.EndsAt = &ends.String
	}
	return r, nil
}

func scanCourseEnrollment(scan func(...any) error) (CourseEnrollment, error) {
	var e CourseEnrollment
	var sourceRef, expires, revoked, completed sql.NullString
	if err := scan(
		&e.SpaceID, &e.MemberID, &e.Status, &e.Source, &sourceRef, &expires, &revoked,
		&e.EnrolledAt, &completed,
	); err != nil {
		return e, err
	}
	if sourceRef.Valid {
		e.SourceRef = &sourceRef.String
	}
	if expires.Valid {
		e.AccessExpiresAt = &expires.String
	}
	if revoked.Valid {
		e.AccessRevokedAt = &revoked.String
	}
	if completed.Valid {
		e.CompletedAt = &completed.String
	}
	return e, nil
}

func loadCourseEnrollment(db *sql.DB, spaceID, memberID string) (CourseEnrollment, error) {
	row := db.QueryRow(
		`SELECT space_id, member_id, status, source, source_ref, access_expires_at, access_revoked_at,
		        enrolled_at, completed_at
		   FROM course_enrollments WHERE space_id = ? AND member_id = ?`,
		spaceID, memberID,
	)
	e, err := scanCourseEnrollment(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return e, fmt.Errorf("course_enrollment %q/%q not found", spaceID, memberID)
	}
	return e, err
}

func deleteByIDAfterLessonCheck(ctx *sdk.AppCtx, args map[string]any, table, label string) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	var lessonID string
	if err := ctx.AppDB().QueryRow(`SELECT lesson_id FROM `+table+` WHERE id = ?`, id).Scan(&lessonID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%s %q not found", label, id)
		}
		return nil, err
	}
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM `+table+` WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}
