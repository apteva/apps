// Courses — the 0.2 surface.
//
// A course is a space with kind='course'. Inside live sections (top-
// level chapters) which hold lessons (individual learning units).
// Lessons carry markdown body + optional video reference (a storage
// app file_id stored as TEXT). Per-member progress is tracked in
// lesson_progress with the state machine
//
//	not_started → in_progress → complete
//
// Cross-app calls follow the SDK's `ctx.PlatformAPI().CallAppResult()`
// pattern. Both storage and ffmpeg are optional integrations — if
// neither is bound, courses still work (text-only lessons, manual
// duration).

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Types ───────────────────────────────────────────────────────

type Section struct {
	ID        string `json:"id"`
	SpaceID   string `json:"space_id"`
	Title     string `json:"title"`
	Position  int64  `json:"position"`
	CreatedAt string `json:"created_at"`
}

type Lesson struct {
	ID                   string  `json:"id"`
	CommunityID          string  `json:"community_id"`
	SectionID            string  `json:"section_id"`
	Title                string  `json:"title"`
	Body                 string  `json:"body"`
	VideoStorageKey      *string `json:"video_storage_key,omitempty"`
	VideoDurationSeconds *int64  `json:"video_duration_seconds,omitempty"`
	Position             int64   `json:"position"`
	PublishedAt          *string `json:"published_at,omitempty"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	// Populated by list/get when a member_id is supplied.
	Progress *LessonProgress `json:"progress,omitempty"`
}

type LessonProgress struct {
	LessonID            string  `json:"lesson_id"`
	MemberID            string  `json:"member_id"`
	Status              string  `json:"status"`
	CompletedAt         *string `json:"completed_at,omitempty"`
	LastPositionSeconds *int64  `json:"last_position_seconds,omitempty"`
	UpdatedAt           string  `json:"updated_at"`
}

type LessonComment struct {
	ID        string `json:"id"`
	LessonID  string `json:"lesson_id"`
	MemberID  string `json:"member_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// CourseProgressBucket is one bar of the per-lesson funnel.
type CourseProgressBucket struct {
	LessonID  string `json:"lesson_id"`
	Title     string `json:"title"`
	Started   int    `json:"started"` // in_progress + complete
	Completed int    `json:"completed"`
}

// ─── Tools ───────────────────────────────────────────────────────

func coursesTools() []sdk.Tool {
	tools := []sdk.Tool{
		// Course creation is sugar over spaces_create with kind=course.
		{
			Name:        "courses_create",
			Description: "Create a course (sugar for spaces_create with kind=course). Args: community_id (required), slug (required), name (required), visibility?.",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"slug":         map[string]any{"type": "string"},
				"name":         map[string]any{"type": "string"},
				"visibility":   map[string]any{"type": "string"},
			}, []string{"community_id", "slug", "name"}),
			Handler: toolCoursesCreate,
		},

		// Sections
		{
			Name:        "sections_create",
			Description: "Create a section inside a course. Args: space_id (course, required), title (required), position? (default 0).",
			InputSchema: schemaObject(map[string]any{
				"space_id": map[string]any{"type": "string"},
				"title":    map[string]any{"type": "string"},
				"position": map[string]any{"type": "integer"},
			}, []string{"space_id", "title"}),
			Handler: toolSectionsCreate,
		},
		{
			Name:        "sections_list",
			Description: "List sections of a course in position order. Args: space_id (required).",
			InputSchema: schemaObject(map[string]any{
				"space_id": map[string]any{"type": "string"},
			}, []string{"space_id"}),
			Handler: toolSectionsList,
		},
		{
			Name:        "sections_reorder",
			Description: "Reorder sections within a course. Args: space_id (required), order (required, ordered array of section ids).",
			InputSchema: schemaObject(map[string]any{
				"space_id": map[string]any{"type": "string"},
				"order": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			}, []string{"space_id", "order"}),
			Handler: toolSectionsReorder,
		},

		// Lessons
		{
			Name:        "lessons_create",
			Description: "Create a lesson inside a section. Lessons start as drafts (published_at NULL). Args: section_id (required), title (required), body? (markdown), position? (default end).",
			InputSchema: schemaObject(map[string]any{
				"section_id": map[string]any{"type": "string"},
				"title":      map[string]any{"type": "string"},
				"body":       map[string]any{"type": "string"},
				"position":   map[string]any{"type": "integer"},
			}, []string{"section_id", "title"}),
			Handler: toolLessonsCreate,
		},
		{
			Name:        "lessons_update",
			Description: "Update a lesson's title or body. Args: id (required), title?, body?.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "string"},
				"title": map[string]any{"type": "string"},
				"body":  map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolLessonsUpdate,
		},
		{
			Name:        "lessons_publish",
			Description: "Publish (sets published_at to now) or unpublish (clears it). Args: id (required), published (bool, required).",
			InputSchema: schemaObject(map[string]any{
				"id":        map[string]any{"type": "string"},
				"published": map[string]any{"type": "boolean"},
			}, []string{"id", "published"}),
			Handler: toolLessonsPublish,
		},
		{
			Name:        "lessons_reorder",
			Description: "Reorder lessons within a section. Args: section_id (required), order (ordered array of lesson ids).",
			InputSchema: schemaObject(map[string]any{
				"section_id": map[string]any{"type": "string"},
				"order": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			}, []string{"section_id", "order"}),
			Handler: toolLessonsReorder,
		},
		{
			Name:        "lessons_list",
			Description: "List lessons of a course. Args: space_id (required), include_drafts? (default false), member_id? (when set, each lesson gets a `progress` field for that member).",
			InputSchema: schemaObject(map[string]any{
				"space_id":       map[string]any{"type": "string"},
				"include_drafts": map[string]any{"type": "boolean"},
				"member_id":      map[string]any{"type": "string"},
			}, []string{"space_id"}),
			Handler: toolLessonsList,
		},
		{
			Name:        "lessons_get",
			Description: "Fetch one lesson with body + caller's progress when member_id is set. Args: id (required), member_id?.",
			InputSchema: schemaObject(map[string]any{
				"id":        map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolLessonsGet,
		},
		{
			Name:        "lessons_attach_video",
			Description: "Attach a storage app file as the lesson's video. The caller uploads the file to storage first (recommended folder /.community/lessons/) and passes the returned file id here. If the ffmpeg app is bound and duration_seconds is omitted, community calls ffmpeg_probe to fill it in. Args: id (required), storage_key (required), duration_seconds?.",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "string"},
				"storage_key":      map[string]any{"type": "string"},
				"duration_seconds": map[string]any{"type": "integer"},
			}, []string{"id", "storage_key"}),
			Handler: toolLessonsAttachVideo,
		},

		// Progress
		{
			Name:        "lessons_mark_complete",
			Description: "Mark a lesson complete (or in_progress with last_position_seconds). Args: lesson_id (required), member_id (required), status? (in_progress|complete; default 'complete'), last_position_seconds?.",
			InputSchema: schemaObject(map[string]any{
				"lesson_id":             map[string]any{"type": "string"},
				"member_id":             map[string]any{"type": "string"},
				"status":                map[string]any{"type": "string"},
				"last_position_seconds": map[string]any{"type": "integer"},
			}, []string{"lesson_id", "member_id"}),
			Handler: toolLessonsMarkComplete,
		},
		{
			Name:        "lessons_progress",
			Description: "Get one member's progress across a course: per-lesson status + overall percent_complete. Args: space_id (required), member_id (required).",
			InputSchema: schemaObject(map[string]any{
				"space_id":  map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
			}, []string{"space_id", "member_id"}),
			Handler: toolLessonsProgress,
		},
		{
			Name:        "course_progress",
			Description: "Aggregate per-lesson funnel for a course: started + completed counts across all members. Args: space_id (required).",
			InputSchema: schemaObject(map[string]any{
				"space_id": map[string]any{"type": "string"},
			}, []string{"space_id"}),
			Handler: toolCourseProgress,
		},

		// Comments
		{
			Name:        "lesson_comments_post",
			Description: "Post a comment on a lesson. Args: lesson_id (required), member_id (required), body (required).",
			InputSchema: schemaObject(map[string]any{
				"lesson_id": map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
				"body":      map[string]any{"type": "string"},
			}, []string{"lesson_id", "member_id", "body"}),
			Handler: toolLessonCommentsPost,
		},
		{
			Name:        "lesson_comments_list",
			Description: "List comments on a lesson, oldest first. Args: lesson_id (required), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"lesson_id": map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
			}, []string{"lesson_id"}),
			Handler: toolLessonCommentsList,
		},
	}
	return append(tools, courseBuilderTools()...)
}

// ─── courses_create (sugar) ──────────────────────────────────────

func toolCoursesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	clone := map[string]any{
		"community_id": args["community_id"],
		"slug":         args["slug"],
		"name":         args["name"],
		"kind":         "course",
	}
	if v, ok := args["visibility"]; ok {
		clone["visibility"] = v
	}
	return toolSpacesCreate(ctx, clone)
}

// ─── Sections ────────────────────────────────────────────────────

func toolSectionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	title, err := mustStr(args, "title")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	pos := int64(0)
	if v, ok := intArg(args, "position"); ok {
		pos = v
	} else {
		// Default to end of list.
		err := db.QueryRow(
			`SELECT COALESCE(MAX(position)+1, 0) FROM sections WHERE space_id = ?`,
			spaceID,
		).Scan(&pos)
		if err != nil {
			return nil, err
		}
	}
	id := newID("sec")
	if _, err := db.Exec(
		`INSERT INTO sections (id, space_id, title, position) VALUES (?, ?, ?, ?)`,
		id, spaceID, title, pos,
	); err != nil {
		return nil, fmt.Errorf("create section: %w", err)
	}
	s, err := loadSection(db, id)
	if err != nil {
		return nil, err
	}
	cid, _ := lookupCommunityForSpace(db, spaceID)
	emit(ctx, "section.created", map[string]any{
		"community_id": cid,
		"space_id":     spaceID,
		"section_id":   id,
		"title":        title,
	})
	return s, nil
}

func toolSectionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, space_id, title, position, created_at FROM sections
		 WHERE space_id = ? ORDER BY position, created_at`,
		spaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Section{}
	for rows.Next() {
		var s Section
		if err := rows.Scan(&s.ID, &s.SpaceID, &s.Title, &s.Position, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"sections": out}, nil
}

func toolSectionsReorder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	rawOrder, ok := args["order"].([]any)
	if !ok || len(rawOrder) == 0 {
		return nil, errors.New("order must be a non-empty array of section ids")
	}
	order := make([]string, len(rawOrder))
	seen := map[string]bool{}
	for i, v := range rawOrder {
		s, _ := v.(string)
		if s == "" {
			return nil, errors.New("section id is empty")
		}
		if seen[s] {
			return nil, fmt.Errorf("section %q appears more than once", s)
		}
		seen[s] = true
		order[i] = s
	}
	db := ctx.AppDB()
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sections WHERE space_id = ?`, spaceID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing != len(order) {
		return nil, fmt.Errorf("order must contain every section exactly once: got %d, want %d", len(order), existing)
	}
	for i, secID := range order {
		res, err := tx.Exec(
			`UPDATE sections SET position = ? WHERE id = ? AND space_id = ?`,
			i, secID, spaceID,
		)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil, fmt.Errorf("section %q not in space %q", secID, spaceID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	cid, _ := lookupCommunityForSpace(db, spaceID)
	emit(ctx, "sections.reordered", map[string]any{
		"community_id": cid,
		"space_id":     spaceID,
	})
	return map[string]any{"ok": true, "order": order}, nil
}

// ─── Lessons ─────────────────────────────────────────────────────

func toolLessonsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sectionID, err := mustStr(args, "section_id")
	if err != nil {
		return nil, err
	}
	title, err := mustStr(args, "title")
	if err != nil {
		return nil, err
	}
	body := strArg(args, "body", "")
	db := ctx.AppDB()
	// Resolve community_id via the section's space.
	var spaceID, communityID string
	if err := db.QueryRow(
		`SELECT s.space_id, sp.community_id FROM sections s
		 JOIN spaces sp ON sp.id = s.space_id
		 WHERE s.id = ?`, sectionID,
	).Scan(&spaceID, &communityID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("section %q not found", sectionID)
		}
		return nil, err
	}
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	pos := int64(0)
	if v, ok := intArg(args, "position"); ok {
		pos = v
	} else {
		err := db.QueryRow(
			`SELECT COALESCE(MAX(position)+1, 0) FROM lessons WHERE section_id = ?`,
			sectionID,
		).Scan(&pos)
		if err != nil {
			return nil, err
		}
	}
	id := newID("les")
	if _, err := db.Exec(
		`INSERT INTO lessons (id, community_id, section_id, title, body, position)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, communityID, sectionID, title, body, pos,
	); err != nil {
		return nil, fmt.Errorf("create lesson: %w", err)
	}
	l, err := loadLesson(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "lesson.created", map[string]any{
		"community_id": communityID,
		"section_id":   sectionID,
		"lesson_id":    id,
		"title":        title,
	})
	return l, nil
}

func toolLessonsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	cur, _, err := ensureLessonVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["title"].(string); ok && v != "" {
		sets = append(sets, "title = ?")
		vals = append(vals, v)
	}
	if v, ok := args["body"].(string); ok {
		sets = append(sets, "body = ?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := db.Exec(
		`UPDATE lessons SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...,
	); err != nil {
		return nil, err
	}
	l, err := loadLesson(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "lesson.updated", map[string]any{
		"community_id": cur.CommunityID,
		"section_id":   l.SectionID,
		"lesson_id":    l.ID,
	})
	return l, nil
}

func toolLessonsPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	publish, _ := args["published"].(bool)
	db := ctx.AppDB()
	cur, _, err := ensureLessonVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	var q string
	if publish {
		q = `UPDATE lessons SET published_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	} else {
		q = `UPDATE lessons SET published_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	}
	if _, err := db.Exec(q, id); err != nil {
		return nil, err
	}
	l, err := loadLesson(db, id)
	if err != nil {
		return nil, err
	}
	topic := "lesson.published"
	if !publish {
		topic = "lesson.unpublished"
	}
	emit(ctx, topic, map[string]any{
		"community_id": cur.CommunityID,
		"section_id":   l.SectionID,
		"lesson_id":    l.ID,
	})
	return l, nil
}

func toolLessonsReorder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sectionID, err := mustStr(args, "section_id")
	if err != nil {
		return nil, err
	}
	rawOrder, ok := args["order"].([]any)
	if !ok || len(rawOrder) == 0 {
		return nil, errors.New("order must be a non-empty array of lesson ids")
	}
	order := make([]string, len(rawOrder))
	seen := map[string]bool{}
	for i, v := range rawOrder {
		s, _ := v.(string)
		if s == "" {
			return nil, errors.New("lesson id is empty")
		}
		if seen[s] {
			return nil, fmt.Errorf("lesson %q appears more than once", s)
		}
		seen[s] = true
		order[i] = s
	}
	db := ctx.AppDB()
	var spaceID string
	if err := db.QueryRow(`SELECT space_id FROM sections WHERE id = ?`, sectionID).Scan(&spaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("section %q not found", sectionID)
		}
		return nil, err
	}
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM lessons WHERE section_id = ?`, sectionID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing != len(order) {
		return nil, fmt.Errorf("order must contain every lesson exactly once: got %d, want %d", len(order), existing)
	}
	for i, lID := range order {
		res, err := tx.Exec(
			`UPDATE lessons SET position = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ? AND section_id = ?`,
			i, lID, sectionID,
		)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil, fmt.Errorf("lesson %q not in section %q", lID, sectionID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "order": order}, nil
}

func toolLessonsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	includeDrafts, _ := args["include_drafts"].(bool)
	memberID := strArg(args, "member_id", "")
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	q := `SELECT ` + lessonCols + `
	      FROM lessons l
	      JOIN sections s ON s.id = l.section_id`
	queryArgs := []any{}
	if viewerID := strArg(args, "_viewer_member_id", ""); viewerID != "" {
		q += `
	      JOIN course_enrollments e ON e.space_id = s.space_id AND e.member_id = ?
	      LEFT JOIN drip_schedules d ON d.lesson_id = l.id`
		queryArgs = append(queryArgs, viewerID)
	}
	q += ` WHERE s.space_id = ?`
	queryArgs = append(queryArgs, spaceID)
	if !includeDrafts {
		q += ` AND l.published_at IS NOT NULL`
	}
	if viewerID := strArg(args, "_viewer_member_id", ""); viewerID != "" {
		q += ` AND e.status IN ('active','completed')
		       AND (d.release_at IS NULL OR datetime(d.release_at) <= CURRENT_TIMESTAMP)
		       AND (d.release_after_days IS NULL OR
		            datetime(e.enrolled_at, '+' || d.release_after_days || ' days') <= CURRENT_TIMESTAMP)`
	}
	q += ` ORDER BY s.position, l.position`
	rows, err := ctx.AppDB().Query(q, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lesson{}
	for rows.Next() {
		l, err := scanLesson(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if memberID != "" && len(out) > 0 {
		prog, err := progressMap(ctx.AppDB(), memberID, out)
		if err != nil {
			return nil, err
		}
		for i := range out {
			if p, ok := prog[out[i].ID]; ok {
				out[i].Progress = &p
			}
		}
	}
	return map[string]any{"lessons": out}, nil
}

func toolLessonsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	memberID := strArg(args, "member_id", "")
	db := ctx.AppDB()
	l, _, err := ensureLessonVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if viewerID := strArg(args, "_viewer_member_id", ""); viewerID != "" {
		available, err := lessonAvailableToMember(db, id, viewerID)
		if err != nil {
			return nil, err
		}
		if !available {
			return nil, errors.New("lesson is not available yet")
		}
	}
	if memberID != "" {
		p, ok, err := loadLessonProgress(db, id, memberID)
		if err != nil {
			return nil, err
		}
		if ok {
			l.Progress = &p
		}
	}
	return l, nil
}

// toolLessonsAttachVideo binds the lesson to a storage file, and
// (when ffmpeg is bound) auto-probes the duration via the storage
// app's signed URL.
func toolLessonsAttachVideo(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	storageKey, err := mustStr(args, "storage_key")
	if err != nil {
		return nil, err
	}
	var duration int64
	if v, ok := intArg(args, "duration_seconds"); ok && v > 0 {
		duration = v
	}
	db := ctx.AppDB()
	cur, _, err := ensureLessonVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if err := validateStorageFile(ctx, storageKey); err != nil {
		return nil, err
	}
	// Try to fill duration from ffmpeg if not supplied.
	autoProbed := false
	if duration == 0 {
		if probed, ok := probeDurationViaFFmpeg(ctx, storageKey); ok {
			duration = probed
			autoProbed = true
		}
	}
	var durArg any
	if duration > 0 {
		durArg = duration
	}
	if _, err := db.Exec(
		`UPDATE lessons SET video_storage_key = ?, video_duration_seconds = ?,
		 updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		storageKey, durArg, id,
	); err != nil {
		return nil, err
	}
	l, err := loadLesson(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "lesson.video_attached", map[string]any{
		"community_id":     cur.CommunityID,
		"lesson_id":        id,
		"storage_key":      storageKey,
		"duration_seconds": duration,
		"auto_probed":      autoProbed,
	})
	return l, nil
}

// probeDurationViaFFmpeg returns (duration_seconds, true) on success,
// (0, false) on any failure — the call is best-effort and the lesson
// can be created without a duration.
//
// Flow: ask storage for a signed URL → ask ffmpeg to probe it →
// parse seconds from ffprobe's JSON. Both calls happen via the
// platform's CallAppResult so they're permission-gated server-side
// against this install's bindings; if either app isn't bound the
// call returns a 403 and we skip.
func probeDurationViaFFmpeg(ctx *sdk.AppCtx, storageKey string) (int64, bool) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return 0, false
	}
	api := ctx.PlatformAPI()
	// 1. Signed URL from storage.
	var urlOut struct {
		URL string `json:"url"`
	}
	if err := api.CallAppResult("storage", "files_get_url",
		map[string]any{"id": storageKey}, &urlOut); err != nil || urlOut.URL == "" {
		return 0, false
	}
	// 2. ffprobe.
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := api.CallAppResult("ffmpeg", "ffmpeg_probe",
		map[string]any{"url": urlOut.URL}, &probe); err != nil {
		return 0, false
	}
	if probe.Format.Duration == "" {
		return 0, false
	}
	// ffprobe returns duration as a string like "123.456". Truncate to
	// whole seconds — sub-second precision doesn't earn its keep on a
	// lesson row.
	dot := strings.Index(probe.Format.Duration, ".")
	whole := probe.Format.Duration
	if dot >= 0 {
		whole = probe.Format.Duration[:dot]
	}
	var n int64
	if _, err := fmt.Sscan(whole, &n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ─── Progress ────────────────────────────────────────────────────

var lessonStatuses = map[string]bool{
	"in_progress": true,
	"complete":    true,
}

func toolLessonsMarkComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status", "complete")
	if !lessonStatuses[status] {
		return nil, fmt.Errorf("status %q invalid: must be in_progress|complete", status)
	}
	db := ctx.AppDB()
	// Verify lesson and member belong to the same community.
	var communityID, memberCommunity string
	lesson, _, err := ensureLessonVisible(ctx, db, lessonID)
	if err != nil {
		return nil, err
	}
	communityID = lesson.CommunityID
	if err := db.QueryRow(`SELECT community_id FROM members WHERE id = ?`, memberID).
		Scan(&memberCommunity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("member %q not found", memberID)
		}
		return nil, err
	}
	if communityID != memberCommunity {
		return nil, errors.New("lesson and member belong to different communities")
	}
	var lastPos any
	if v, ok := intArg(args, "last_position_seconds"); ok && v >= 0 {
		lastPos = v
	}
	// Upsert.
	var q string
	var execArgs []any
	if status == "complete" {
		q = `INSERT INTO lesson_progress (lesson_id, member_id, status, completed_at, last_position_seconds, updated_at)
		     VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?, CURRENT_TIMESTAMP)
		     ON CONFLICT(lesson_id, member_id) DO UPDATE SET
		       status = excluded.status,
		       completed_at = excluded.completed_at,
		       last_position_seconds = COALESCE(excluded.last_position_seconds, lesson_progress.last_position_seconds),
		       updated_at = CURRENT_TIMESTAMP`
		execArgs = []any{lessonID, memberID, status, lastPos}
	} else {
		q = `INSERT INTO lesson_progress (lesson_id, member_id, status, last_position_seconds, updated_at)
		     VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
			     ON CONFLICT(lesson_id, member_id) DO UPDATE SET
			       status = excluded.status,
			       completed_at = NULL,
			       last_position_seconds = COALESCE(excluded.last_position_seconds, lesson_progress.last_position_seconds),
		       updated_at = CURRENT_TIMESTAMP`
		execArgs = []any{lessonID, memberID, status, lastPos}
	}
	if _, err := db.Exec(q, execArgs...); err != nil {
		return nil, err
	}
	if err := syncCourseCompletion(db, lessonID, memberID); err != nil {
		return nil, err
	}
	prog, _, err := loadLessonProgress(db, lessonID, memberID)
	if err != nil {
		return nil, err
	}
	topic := "lesson.in_progress"
	if status == "complete" {
		topic = "lesson.completed"
	}
	emit(ctx, topic, map[string]any{
		"community_id": communityID,
		"lesson_id":    lessonID,
		"member_id":    memberID,
		"status":       status,
	})
	return prog, nil
}

func toolLessonsProgress(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	// All published lessons in the course.
	rows, err := db.Query(
		`SELECT l.id, l.title, COALESCE(lp.status, 'not_started') AS status,
		        lp.completed_at, lp.last_position_seconds
		 FROM lessons l
		 JOIN sections s ON s.id = l.section_id
		 LEFT JOIN course_enrollments e ON e.space_id = s.space_id AND e.member_id = ?
		 LEFT JOIN drip_schedules d ON d.lesson_id = l.id
		 LEFT JOIN lesson_progress lp ON lp.lesson_id = l.id AND lp.member_id = ?
		 WHERE s.space_id = ? AND l.published_at IS NOT NULL
		   AND (? = '' OR (
		     e.status IN ('active','completed')
		     AND (d.release_at IS NULL OR datetime(d.release_at) <= CURRENT_TIMESTAMP)
		     AND (d.release_after_days IS NULL OR
		          datetime(e.enrolled_at, '+' || d.release_after_days || ' days') <= CURRENT_TIMESTAMP)
		   ))
		 ORDER BY s.position, l.position`,
		memberID, memberID, spaceID, strArg(args, "_viewer_member_id", ""),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		LessonID            string  `json:"lesson_id"`
		Title               string  `json:"title"`
		Status              string  `json:"status"`
		CompletedAt         *string `json:"completed_at,omitempty"`
		LastPositionSeconds *int64  `json:"last_position_seconds,omitempty"`
	}
	out := []row{}
	completed := 0
	for rows.Next() {
		var r row
		var completedAt sql.NullString
		var lastPos sql.NullInt64
		if err := rows.Scan(&r.LessonID, &r.Title, &r.Status, &completedAt, &lastPos); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			v := completedAt.String
			r.CompletedAt = &v
		}
		if lastPos.Valid {
			v := lastPos.Int64
			r.LastPositionSeconds = &v
		}
		if r.Status == "complete" {
			completed++
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	percent := 0
	if len(out) > 0 {
		percent = (completed * 100) / len(out)
	}
	return map[string]any{
		"space_id":         spaceID,
		"member_id":        memberID,
		"total_lessons":    len(out),
		"completed":        completed,
		"percent_complete": percent,
		"lessons":          out,
	}, nil
}

func toolCourseProgress(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT l.id, l.title,
		        COUNT(CASE WHEN lp.status IN ('in_progress','complete') THEN 1 END) AS started,
		        COUNT(CASE WHEN lp.status = 'complete' THEN 1 END) AS completed
		 FROM lessons l
		 JOIN sections s ON s.id = l.section_id
		 LEFT JOIN lesson_progress lp ON lp.lesson_id = l.id
		 WHERE s.space_id = ? AND l.published_at IS NOT NULL
		 GROUP BY l.id, l.title, s.position, l.position
		 ORDER BY s.position, l.position`,
		spaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CourseProgressBucket{}
	for rows.Next() {
		var b CourseProgressBucket
		if err := rows.Scan(&b.LessonID, &b.Title, &b.Started, &b.Completed); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"space_id": spaceID, "lessons": out}, nil
}

// ─── Lesson comments ─────────────────────────────────────────────

func toolLessonCommentsPost(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
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
	db := ctx.AppDB()
	lesson, _, err := ensureLessonVisible(ctx, db, lessonID)
	if err != nil {
		return nil, err
	}
	communityID := lesson.CommunityID
	if err := verifyMember(db, communityID, memberID); err != nil {
		return nil, err
	}
	id := newID("lcom")
	if _, err := db.Exec(
		`INSERT INTO lesson_comments (id, lesson_id, member_id, body) VALUES (?, ?, ?, ?)`,
		id, lessonID, memberID, body,
	); err != nil {
		return nil, err
	}
	c, err := loadLessonComment(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "lesson.commented", map[string]any{
		"community_id": communityID,
		"lesson_id":    lessonID,
		"comment_id":   id,
		"member_id":    memberID,
		"preview":      preview(body),
	})
	return c, nil
}

func toolLessonCommentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	limit := boundedLimit(args, "limit", 200, 500)
	if _, _, err := ensureLessonVisible(ctx, ctx.AppDB(), lessonID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, lesson_id, member_id, body, created_at FROM lesson_comments
		 WHERE lesson_id = ? ORDER BY created_at, id LIMIT ?`,
		lessonID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LessonComment{}
	for rows.Next() {
		var c LessonComment
		if err := rows.Scan(&c.ID, &c.LessonID, &c.MemberID, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"comments": out}, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

const lessonCols = `l.id, l.community_id, l.section_id, l.title, l.body,
                    l.video_storage_key, l.video_duration_seconds,
                    l.position, l.published_at, l.created_at, l.updated_at`

func scanLesson(scan func(...any) error) (Lesson, error) {
	var l Lesson
	var vk, pub sql.NullString
	var dur sql.NullInt64
	if err := scan(
		&l.ID, &l.CommunityID, &l.SectionID, &l.Title, &l.Body,
		&vk, &dur, &l.Position, &pub, &l.CreatedAt, &l.UpdatedAt,
	); err != nil {
		return l, err
	}
	if vk.Valid {
		v := vk.String
		l.VideoStorageKey = &v
	}
	if dur.Valid {
		v := dur.Int64
		l.VideoDurationSeconds = &v
	}
	if pub.Valid {
		v := pub.String
		l.PublishedAt = &v
	}
	return l, nil
}

func loadLesson(db *sql.DB, id string) (Lesson, error) {
	// loadLesson uses the `l.` prefix from lessonCols too — the single-
	// row query joins to itself trivially via the table alias.
	row := db.QueryRow(`SELECT `+lessonCols+` FROM lessons l WHERE l.id = ?`, id)
	l, err := scanLesson(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return l, fmt.Errorf("lesson %q not found", id)
	}
	return l, err
}

func loadSection(db *sql.DB, id string) (Section, error) {
	var s Section
	row := db.QueryRow(
		`SELECT id, space_id, title, position, created_at FROM sections WHERE id = ?`, id,
	)
	if err := row.Scan(&s.ID, &s.SpaceID, &s.Title, &s.Position, &s.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s, fmt.Errorf("section %q not found", id)
		}
		return s, err
	}
	return s, nil
}

func loadLessonProgress(db *sql.DB, lessonID, memberID string) (LessonProgress, bool, error) {
	var p LessonProgress
	var completedAt sql.NullString
	var lastPos sql.NullInt64
	err := db.QueryRow(
		`SELECT lesson_id, member_id, status, completed_at, last_position_seconds, updated_at
		 FROM lesson_progress WHERE lesson_id = ? AND member_id = ?`,
		lessonID, memberID,
	).Scan(&p.LessonID, &p.MemberID, &p.Status, &completedAt, &lastPos, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if completedAt.Valid {
		v := completedAt.String
		p.CompletedAt = &v
	}
	if lastPos.Valid {
		v := lastPos.Int64
		p.LastPositionSeconds = &v
	}
	return p, true, nil
}

func loadLessonComment(db *sql.DB, id string) (LessonComment, error) {
	var c LessonComment
	err := db.QueryRow(
		`SELECT id, lesson_id, member_id, body, created_at FROM lesson_comments WHERE id = ?`, id,
	).Scan(&c.ID, &c.LessonID, &c.MemberID, &c.Body, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("lesson_comment %q not found", id)
	}
	return c, err
}

// progressMap returns a member's progress rows for a given lesson set.
func progressMap(db *sql.DB, memberID string, lessons []Lesson) (map[string]LessonProgress, error) {
	if len(lessons) == 0 {
		return map[string]LessonProgress{}, nil
	}
	placeholders := strings.Repeat("?,", len(lessons))
	placeholders = strings.TrimRight(placeholders, ",")
	args := make([]any, 0, 1+len(lessons))
	args = append(args, memberID)
	for _, l := range lessons {
		args = append(args, l.ID)
	}
	rows, err := db.Query(
		`SELECT lesson_id, member_id, status, completed_at, last_position_seconds, updated_at
		 FROM lesson_progress WHERE member_id = ? AND lesson_id IN (`+placeholders+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LessonProgress{}
	for rows.Next() {
		var p LessonProgress
		var completedAt sql.NullString
		var lastPos sql.NullInt64
		if err := rows.Scan(&p.LessonID, &p.MemberID, &p.Status, &completedAt, &lastPos, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			v := completedAt.String
			p.CompletedAt = &v
		}
		if lastPos.Valid {
			v := lastPos.Int64
			p.LastPositionSeconds = &v
		}
		out[p.LessonID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func syncCourseCompletion(db *sql.DB, lessonID, memberID string) error {
	var spaceID string
	if err := db.QueryRow(
		`SELECT s.space_id FROM lessons l JOIN sections s ON s.id = l.section_id WHERE l.id = ?`,
		lessonID,
	).Scan(&spaceID); err != nil {
		return err
	}
	var total, completed int
	if err := db.QueryRow(
		`SELECT COUNT(*), COUNT(lp.lesson_id)
		   FROM lessons l
		   JOIN sections s ON s.id = l.section_id
		   LEFT JOIN lesson_progress lp
		     ON lp.lesson_id = l.id AND lp.member_id = ? AND lp.status = 'complete'
		  WHERE s.space_id = ? AND l.published_at IS NOT NULL`,
		memberID, spaceID,
	).Scan(&total, &completed); err != nil {
		return err
	}
	if total > 0 && total == completed {
		result, err := db.Exec(
			`UPDATE course_enrollments
			    SET status = 'completed', completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
			  WHERE space_id = ? AND member_id = ? AND status IN ('active','completed')`,
			spaceID, memberID,
		)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return nil
		}
		var enabled, issue int
		var title, body string
		if err := db.QueryRow(
			`SELECT enabled, issue_on_completion, title, body FROM course_certificates WHERE space_id = ?`,
			spaceID,
		).Scan(&enabled, &issue, &title, &body); err == nil && enabled != 0 && issue != 0 {
			_, err = db.Exec(
				`INSERT INTO issued_certificates (id, space_id, member_id, title, body)
				 VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT(space_id, member_id) DO NOTHING`,
				newID("cert"), spaceID, memberID, title, body,
			)
			return err
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return nil
	}
	_, err := db.Exec(
		`UPDATE course_enrollments SET status = 'active', completed_at = NULL
		  WHERE space_id = ? AND member_id = ? AND status = 'completed'`,
		spaceID, memberID,
	)
	return err
}

func lookupCommunityForSpace(db *sql.DB, spaceID string) (string, error) {
	var c string
	if err := db.QueryRow(`SELECT community_id FROM spaces WHERE id = ?`, spaceID).Scan(&c); err != nil {
		return "", err
	}
	return c, nil
}

// requireCourseSpace fails fast when the caller hands us a non-course
// space — sections only exist under kind=course.
func requireCourseSpace(ctx *sdk.AppCtx, db *sql.DB, spaceID string) error {
	s, err := ensureSpaceVisible(ctx, db, spaceID)
	if err != nil {
		return err
	}
	if s.Kind != "course" {
		return fmt.Errorf("space %q is %s, not course", spaceID, s.Kind)
	}
	return nil
}

func ensureLessonVisible(ctx *sdk.AppCtx, db *sql.DB, lessonID string) (Lesson, string, error) {
	l, err := loadLesson(db, lessonID)
	if err != nil {
		return l, "", err
	}
	var spaceID string
	err = db.QueryRow(
		`SELECT s.space_id FROM sections s WHERE s.id = ?`, l.SectionID,
	).Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return l, "", fmt.Errorf("section %q not found", l.SectionID)
	}
	if err != nil {
		return l, "", err
	}
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return l, "", err
	}
	return l, spaceID, nil
}

// ─── HTTP ────────────────────────────────────────────────────────

func (a *App) httpSections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	if spaceID == "" {
		writeErr(w, 400, "space_id required")
		return
	}
	out, err := toolSectionsList(globalCtx, map[string]any{"space_id": spaceID})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *App) httpLessons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	if spaceID == "" {
		writeErr(w, 400, "space_id required")
		return
	}
	args := map[string]any{"space_id": spaceID}
	if r.URL.Query().Get("include_drafts") == "true" {
		args["include_drafts"] = true
	}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		args["member_id"] = mid
	}
	out, err := toolLessonsList(globalCtx, args)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *App) httpLesson(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, 400, "id required")
		return
	}
	args := map[string]any{"id": id}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		args["member_id"] = mid
	}
	out, err := toolLessonsGet(globalCtx, args)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}

// ─── compile-time guards ─────────────────────────────────────────

// Reference imports so the file stays clean even if a handler is
// commented out during local iteration.
var _ = json.Marshal
