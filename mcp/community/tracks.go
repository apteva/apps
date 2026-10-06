package main

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type CourseTrack struct {
	ID          string `json:"id"`
	SpaceID     string `json:"space_id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Position    int64  `json:"position"`
	Active      bool   `json:"active"`
	Selected    bool   `json:"selected"`
}

var trackSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func trackTools() []sdk.Tool {
	str := map[string]any{"type": "string"}
	return []sdk.Tool{
		{Name: "course_tracks_list", Description: "List active learning tracks for a course and the member's selected track. Args: space_id, member_id?.", InputSchema: schemaObject(map[string]any{"space_id": str, "member_id": str}, []string{"space_id"}), Handler: toolCourseTracksList},
		{Name: "course_track_select", Description: "Select or switch a learning track without changing existing lesson progress. Args: space_id, track_id, member_id.", InputSchema: schemaObject(map[string]any{"space_id": str, "track_id": str, "member_id": str}, []string{"space_id", "track_id", "member_id"}), Handler: toolCourseTrackSelect},
		{Name: "course_tracks_create", Description: "Create a learning track. Lessons with no track assignment remain shared. Args: space_id, slug, name, description?, position?.", InputSchema: schemaObject(map[string]any{"space_id": str, "slug": str, "name": str, "description": str, "position": map[string]any{"type": "integer"}}, []string{"space_id", "slug", "name"}), Handler: toolCourseTracksCreate},
		{Name: "course_tracks_update", Description: "Update a learning track. Args: id, slug?, name?, description?, position?, active?.", InputSchema: schemaObject(map[string]any{"id": str, "slug": str, "name": str, "description": str, "position": map[string]any{"type": "integer"}, "active": map[string]any{"type": "boolean"}}, []string{"id"}), Handler: toolCourseTracksUpdate},
		{Name: "course_track_lessons_set", Description: "Set the track assignments for a lesson. Empty track_ids makes the lesson shared. Args: lesson_id, track_ids[].", InputSchema: schemaObject(map[string]any{"lesson_id": str, "track_ids": map[string]any{"type": "array", "items": str}}, []string{"lesson_id", "track_ids"}), Handler: toolCourseTrackLessonsSet},
	}
}

func toolCourseTracksList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err = requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	memberID := strArg(args, "member_id", "")
	selected, err := selectedTrackID(ctx.AppDB(), spaceID, memberID)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,space_id,slug,name,description,position,active FROM course_tracks WHERE space_id=? AND active=1 ORDER BY position,name`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CourseTrack{}
	for rows.Next() {
		var t CourseTrack
		var active int
		if err := rows.Scan(&t.ID, &t.SpaceID, &t.Slug, &t.Name, &t.Description, &t.Position, &active); err != nil {
			return nil, err
		}
		t.Active = active != 0
		t.Selected = t.ID == selected
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"space_id": spaceID, "selected_track_id": nullableValue(selected), "tracks": out}, nil
}

func toolCourseTrackSelect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	trackID, err := mustStr(args, "track_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err = requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	if err = verifyMember(db, mustCommunityForSpace(db, spaceID), memberID); err != nil {
		return nil, err
	}
	if err = ensureActiveEnrollment(db, spaceID, memberID); err != nil {
		return nil, err
	}
	var found int
	if err = db.QueryRow(`SELECT COUNT(*) FROM course_tracks WHERE id=? AND space_id=? AND active=1`, trackID, spaceID).Scan(&found); err != nil {
		return nil, err
	}
	if found == 0 {
		return nil, errors.New("learning track not found")
	}
	_, err = db.Exec(`INSERT INTO member_course_tracks(space_id,member_id,track_id) VALUES(?,?,?) ON CONFLICT(space_id,member_id) DO UPDATE SET track_id=excluded.track_id,updated_at=CURRENT_TIMESTAMP`, spaceID, memberID, trackID)
	if err != nil {
		return nil, err
	}
	if err = syncCourseCompletionForSpace(db, spaceID, memberID); err != nil {
		return nil, err
	}
	communityID, _ := communityBySpace(db, spaceID)
	emit(ctx, "course.track_selected", map[string]any{"community_id": communityID, "space_id": spaceID, "member_id": memberID, "track_id": trackID})
	return map[string]any{"space_id": spaceID, "member_id": memberID, "track_id": trackID}, nil
}

func toolCourseTracksCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	slug, err := mustStr(args, "slug")
	if err != nil {
		return nil, err
	}
	name, err := mustStr(args, "name")
	if err != nil {
		return nil, err
	}
	if !trackSlugRE.MatchString(slug) {
		return nil, errors.New("slug must contain lowercase letters, numbers, and hyphens")
	}
	db := ctx.AppDB()
	if err = requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	position, _ := intArg(args, "position")
	desc := strArg(args, "description", "")
	id := newID("track")
	if _, err = db.Exec(`INSERT INTO course_tracks(id,space_id,slug,name,description,position) VALUES(?,?,?,?,?,?)`, id, spaceID, slug, name, desc, position); err != nil {
		return nil, err
	}
	return loadCourseTrack(db, id)
}

func toolCourseTracksUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	current, err := loadCourseTrack(db, id)
	if err != nil {
		return nil, err
	}
	if err = requireCourseSpace(ctx, db, current.SpaceID); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["slug"].(string); ok {
		if !trackSlugRE.MatchString(v) {
			return nil, errors.New("invalid track slug")
		}
		sets = append(sets, "slug=?")
		vals = append(vals, v)
	}
	if v, ok := args["name"].(string); ok {
		sets = append(sets, "name=?")
		vals = append(vals, v)
	}
	if v, ok := args["description"].(string); ok {
		sets = append(sets, "description=?")
		vals = append(vals, v)
	}
	if v, ok := intArg(args, "position"); ok {
		sets = append(sets, "position=?")
		vals = append(vals, v)
	}
	if v, ok := args["active"].(bool); ok {
		sets = append(sets, "active=?")
		if v {
			vals = append(vals, 1)
		} else {
			vals = append(vals, 0)
		}
	}
	if len(sets) > 0 {
		vals = append(vals, id)
		if _, err = db.Exec(`UPDATE course_tracks SET `+strings.Join(sets, ",")+`,updated_at=CURRENT_TIMESTAMP WHERE id=?`, vals...); err != nil {
			return nil, err
		}
	}
	return loadCourseTrack(db, id)
}

func toolCourseTrackLessonsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	raw, ok := stringArrayArg(args, "track_ids")
	if !ok || len(raw) > 20 {
		return nil, errors.New("track_ids must be an array of at most 20 ids")
	}
	db := ctx.AppDB()
	lesson, _, err := ensureLessonVisible(ctx, db, lessonID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, v := range raw {
		id := v
		if id == "" || seen[id] {
			return nil, errors.New("track_ids must contain unique ids")
		}
		seen[id] = true
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM course_tracks WHERE id=? AND space_id=(SELECT space_id FROM sections WHERE id=?)`, id, lesson.SectionID).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("track %q is not in this course", id)
		}
		ids = append(ids, id)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM lesson_track_assignments WHERE lesson_id=?`, lessonID); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err = tx.Exec(`INSERT INTO lesson_track_assignments(lesson_id,track_id) VALUES(?,?)`, lessonID, id); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"lesson_id": lessonID, "track_ids": ids}, nil
}

func loadCourseTrack(db *sql.DB, id string) (CourseTrack, error) {
	var t CourseTrack
	var active int
	err := db.QueryRow(`SELECT id,space_id,slug,name,description,position,active FROM course_tracks WHERE id=?`, id).Scan(&t.ID, &t.SpaceID, &t.Slug, &t.Name, &t.Description, &t.Position, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("track %q not found", id)
	}
	t.Active = active != 0
	return t, err
}
func selectedTrackID(db *sql.DB, spaceID, memberID string) (string, error) {
	if memberID == "" {
		return "", nil
	}
	var id string
	err := db.QueryRow(`SELECT track_id FROM member_course_tracks WHERE space_id=? AND member_id=?`, spaceID, memberID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func nullableValue(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func mustCommunityForSpace(db *sql.DB, spaceID string) string {
	c, _ := communityBySpace(db, spaceID)
	return c
}

// lessonTrackClause returns a predicate that keeps shared lessons and lessons
// assigned to the member's selected track. Courses without tracks keep their
// existing behaviour. The caller must pass memberID as the first argument.
func lessonTrackClause(alias string) string {
	return `(NOT EXISTS (SELECT 1 FROM lesson_track_assignments lta0 WHERE lta0.lesson_id=` + alias + `.id) OR EXISTS (SELECT 1 FROM lesson_track_assignments lta1 JOIN member_course_tracks mct1 ON mct1.track_id=lta1.track_id AND mct1.space_id=? AND mct1.member_id=? WHERE lta1.lesson_id=` + alias + `.id))`
}
