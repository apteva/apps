package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type AssignmentReviewItem struct {
	AssignmentID    string   `json:"assignment_id"`
	AssignmentTitle string   `json:"assignment_title"`
	LessonID        string   `json:"lesson_id"`
	MemberID        string   `json:"member_id"`
	Body            string   `json:"body"`
	Links           []string `json:"links"`
	Files           []string `json:"files"`
	Status          string   `json:"status"`
	Feedback        string   `json:"feedback"`
	Version         int64    `json:"version"`
	UpdatedAt       string   `json:"updated_at"`
	ReviewedAt      *string  `json:"reviewed_at,omitempty"`
}

type MilestoneDefinition struct {
	ID               string  `json:"id"`
	SpaceID          string  `json:"space_id"`
	TrackID          *string `json:"track_id,omitempty"`
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	Position         int64   `json:"position"`
	RequiresApproval bool    `json:"requires_approval"`
	EvidenceType     string  `json:"evidence_type"`
	Active           bool    `json:"active"`
}

type MemberMilestone struct {
	Definition    MilestoneDefinition `json:"definition"`
	MemberID      string              `json:"member_id"`
	Status        string              `json:"status"`
	EvidenceText  string              `json:"evidence_text"`
	EvidenceLinks []string            `json:"evidence_links"`
	EvidenceFiles []string            `json:"evidence_files"`
	Feedback      string              `json:"feedback"`
	ApprovedBy    *string             `json:"approved_by,omitempty"`
	SubmittedAt   *string             `json:"submitted_at,omitempty"`
	ApprovedAt    *string             `json:"approved_at,omitempty"`
}

func reviewMilestoneTools() []sdk.Tool {
	str := map[string]any{"type": "string"}
	arr := map[string]any{"type": "array", "items": str}
	return []sdk.Tool{
		{Name: "assignment_reviews_list", Description: "List student assignment submissions for instructor review. Args: space_id, status?, limit?, offset?.", InputSchema: schemaObject(map[string]any{"space_id": str, "status": str, "limit": map[string]any{"type": "integer"}, "offset": map[string]any{"type": "integer"}}, []string{"space_id"}), Handler: toolAssignmentReviewsList},
		{Name: "assignment_review", Description: "Review a submission. status is needs_changes or approved. Args: assignment_id, member_id, status, feedback?, reviewer_id?.", InputSchema: schemaObject(map[string]any{"assignment_id": str, "member_id": str, "status": str, "feedback": str, "reviewer_id": str}, []string{"assignment_id", "member_id", "status"}), Handler: toolAssignmentReview},
		{Name: "milestones_create", Description: "Create a student milestone for a course or track. Args: space_id, title, description?, track_id?, position?, requires_approval?, evidence_type?.", InputSchema: schemaObject(map[string]any{"space_id": str, "title": str, "description": str, "track_id": str, "position": map[string]any{"type": "integer"}, "requires_approval": map[string]any{"type": "boolean"}, "evidence_type": str}, []string{"space_id", "title"}), Handler: toolMilestonesCreate},
		{Name: "milestones_update", Description: "Update a milestone definition. Args: id, title?, description?, track_id?, position?, requires_approval?, evidence_type?, active?.", InputSchema: schemaObject(map[string]any{"id": str, "title": str, "description": str, "track_id": str, "position": map[string]any{"type": "integer"}, "requires_approval": map[string]any{"type": "boolean"}, "evidence_type": str, "active": map[string]any{"type": "boolean"}}, []string{"id"}), Handler: toolMilestonesUpdate},
		{Name: "milestones_list", Description: "List active course milestones with the member's status and next action. Args: space_id, member_id?.", InputSchema: schemaObject(map[string]any{"space_id": str, "member_id": str}, []string{"space_id"}), Handler: toolMilestonesList},
		{Name: "milestone_submit", Description: "Submit or update milestone evidence. Args: definition_id, member_id, evidence_text?, evidence_links?, evidence_files?.", InputSchema: schemaObject(map[string]any{"definition_id": str, "member_id": str, "evidence_text": str, "evidence_links": arr, "evidence_files": arr}, []string{"definition_id", "member_id"}), Handler: toolMilestoneSubmit},
		{Name: "milestone_review", Description: "Approve or request changes on milestone evidence. Args: definition_id, member_id, status, feedback?, reviewer_id?.", InputSchema: schemaObject(map[string]any{"definition_id": str, "member_id": str, "status": str, "feedback": str, "reviewer_id": str}, []string{"definition_id", "member_id", "status"}), Handler: toolMilestoneReview},
	}
}

func toolAssignmentReviewsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err = requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	limit := boundedLimit(args, "limit", 50, 200)
	offset := boundedOffset(args)
	status := strArg(args, "status", "")
	q := `SELECT a.id,a.title,l.id,s.member_id,s.body,s.links_json,s.files_json,s.status,s.feedback,s.version,s.updated_at,s.reviewed_at FROM assignments a JOIN lessons l ON l.id=a.lesson_id JOIN sections sec ON sec.id=l.section_id JOIN assignment_submissions s ON s.assignment_id=a.id WHERE sec.space_id=?`
	vals := []any{spaceID}
	if status != "" {
		q += ` AND s.status=?`
		vals = append(vals, status)
	}
	q += ` ORDER BY s.updated_at ASC LIMIT ? OFFSET ?`
	vals = append(vals, limit, offset)
	rows, err := ctx.AppDB().Query(q, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssignmentReviewItem{}
	for rows.Next() {
		var x AssignmentReviewItem
		var links, files string
		var reviewed sql.NullString
		if err := rows.Scan(&x.AssignmentID, &x.AssignmentTitle, &x.LessonID, &x.MemberID, &x.Body, &links, &files, &x.Status, &x.Feedback, &x.Version, &x.UpdatedAt, &reviewed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(links), &x.Links)
		_ = json.Unmarshal([]byte(files), &x.Files)
		if reviewed.Valid {
			x.ReviewedAt = &reviewed.String
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"submissions": out, "limit": limit, "offset": offset}, nil
}

func toolAssignmentReview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	aid, err := mustStr(args, "assignment_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status", "")
	if status != "approved" && status != "needs_changes" {
		return nil, errors.New("status must be approved or needs_changes")
	}
	db := ctx.AppDB()
	a, err := loadAssignment(db, aid)
	if err != nil {
		return nil, err
	}
	spaceID, err := spaceByLesson(db, a.LessonID)
	if err != nil {
		return nil, err
	}
	reviewer := strArg(args, "reviewer_id", "")
	if reviewer != "" {
		if err := verifyMember(db, mustCommunityForSpace(db, spaceID), reviewer); err != nil {
			return nil, err
		}
	}
	res, err := db.Exec(`UPDATE assignment_submissions SET status=?,feedback=?,reviewed_by=?,reviewed_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE assignment_id=? AND member_id=?`, status, strArg(args, "feedback", ""), nullableValue(reviewer), aid, memberID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, errors.New("assignment submission not found")
	}
	var version int64
	_ = db.QueryRow(`SELECT version FROM assignment_submissions WHERE assignment_id=? AND member_id=?`, aid, memberID).Scan(&version)
	if _, err = db.Exec(`INSERT INTO assignment_reviews(id,assignment_id,member_id,version,status,feedback,reviewer_id) VALUES(?,?,?,?,?,?,?)`, newID("review"), aid, memberID, version, status, strArg(args, "feedback", ""), nullableValue(reviewer)); err != nil {
		return nil, err
	}
	communityID, _ := communityBySpace(db, spaceID)
	emit(ctx, "assignment.reviewed", map[string]any{"community_id": communityID, "space_id": spaceID, "assignment_id": aid, "member_id": memberID, "status": status})
	return map[string]any{"assignment_id": aid, "member_id": memberID, "status": status, "feedback": strArg(args, "feedback", "")}, nil
}

func toolMilestonesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err = requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	title, err := mustStr(args, "title")
	if err != nil {
		return nil, err
	}
	track := nullableValue(strArg(args, "track_id", ""))
	if track != nil {
		var n int
		if err = db.QueryRow(`SELECT COUNT(*) FROM course_tracks WHERE id=? AND space_id=?`, track, spaceID).Scan(&n); err != nil || n == 0 {
			return nil, errors.New("track not found in this course")
		}
	}
	pos, _ := intArg(args, "position")
	requires := 0
	if v, ok := args["requires_approval"].(bool); ok && v {
		requires = 1
	}
	etype := strArg(args, "evidence_type", "any")
	if etype != "text" && etype != "link" && etype != "file" && etype != "any" {
		return nil, errors.New("invalid evidence_type")
	}
	id := newID("mile")
	if _, err = db.Exec(`INSERT INTO milestone_definitions(id,space_id,track_id,title,description,position,requires_approval,evidence_type) VALUES(?,?,?,?,?,?,?,?)`, id, spaceID, track, title, strArg(args, "description", ""), pos, requires, etype); err != nil {
		return nil, err
	}
	return loadMilestoneDefinition(db, id)
}

func toolMilestonesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	m, err := loadMilestoneDefinition(db, id)
	if err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["title"].(string); ok {
		sets = append(sets, "title=?")
		vals = append(vals, v)
	}
	if v, ok := args["description"].(string); ok {
		sets = append(sets, "description=?")
		vals = append(vals, v)
	}
	if v, ok := args["track_id"].(string); ok {
		sets = append(sets, "track_id=?")
		vals = append(vals, nullableValue(v))
	}
	if v, ok := intArg(args, "position"); ok {
		sets = append(sets, "position=?")
		vals = append(vals, v)
	}
	if v, ok := args["requires_approval"].(bool); ok {
		sets = append(sets, "requires_approval=?")
		if v {
			vals = append(vals, 1)
		} else {
			vals = append(vals, 0)
		}
	}
	if v, ok := args["evidence_type"].(string); ok {
		sets = append(sets, "evidence_type=?")
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
		if _, err = db.Exec(`UPDATE milestone_definitions SET `+strings.Join(sets, ",")+`,updated_at=CURRENT_TIMESTAMP WHERE id=?`, vals...); err != nil {
			return nil, err
		}
	}
	_ = m
	return loadMilestoneDefinition(db, id)
}

func toolMilestonesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err = requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	member := strArg(args, "member_id", "")
	track, err := selectedTrackID(db, spaceID, member)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id,space_id,track_id,title,description,position,requires_approval,evidence_type,active FROM milestone_definitions WHERE space_id=? AND active=1 AND (track_id IS NULL OR track_id=?) ORDER BY position,title`, spaceID, nullableValue(track))
	if err != nil {
		return nil, err
	}
	defs := []MilestoneDefinition{}
	for rows.Next() {
		d, err := scanMilestoneDefinition(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		defs = append(defs, d)
	}
	if err := rows.Err(); err != nil { rows.Close(); return nil, err }
	rows.Close()
	out := []MemberMilestone{}
	for _, d := range defs {
		var x MemberMilestone
		x.Definition = d
		x.MemberID = member
		x.Status = "not_started"
		if member != "" {
			_ = scanMemberMilestone(db, d.ID, member, &x)
		}
		out = append(out, x)
	}
	return map[string]any{"milestones": out, "next_action": nextMilestoneAction(out)}, nil
}

func toolMilestoneSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "definition_id")
	if err != nil {
		return nil, err
	}
	member, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	d, err := loadMilestoneDefinition(db, id)
	if err != nil {
		return nil, err
	}
	if err = verifyMember(db, mustCommunityForSpace(db, d.SpaceID), member); err != nil {
		return nil, err
	}
	if err = ensureActiveEnrollment(db, d.SpaceID, member); err != nil {
		return nil, err
	}
	links, _ := stringArrayArg(args, "evidence_links")
	files, _ := stringArrayArg(args, "evidence_files")
	lj, _ := json.Marshal(links)
	fj, _ := json.Marshal(files)
	status := "submitted"
	if !d.RequiresApproval {
		status = "approved"
	}
	_, err = db.Exec(`INSERT INTO member_milestones(definition_id,member_id,status,evidence_text,evidence_links_json,evidence_files_json,feedback,submitted_at,approved_at) VALUES(?,?,?,?,?,?,'',CURRENT_TIMESTAMP,CASE WHEN ?='approved' THEN CURRENT_TIMESTAMP ELSE NULL END) ON CONFLICT(definition_id,member_id) DO UPDATE SET status=excluded.status,evidence_text=excluded.evidence_text,evidence_links_json=excluded.evidence_links_json,evidence_files_json=excluded.evidence_files_json,feedback='',submitted_at=CURRENT_TIMESTAMP,approved_at=CASE WHEN excluded.status='approved' THEN CURRENT_TIMESTAMP ELSE NULL END,updated_at=CURRENT_TIMESTAMP`, id, member, status, strArg(args, "evidence_text", ""), string(lj), string(fj), status)
	if err != nil {
		return nil, err
	}
	communityID, _ := communityBySpace(db, d.SpaceID)
	emit(ctx, "milestone.achieved", map[string]any{"community_id": communityID, "space_id": d.SpaceID, "member_id": member, "milestone_id": id, "status": status})
	return map[string]any{"definition_id": id, "member_id": member, "status": status}, nil
}

func toolMilestoneReview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "definition_id")
	if err != nil {
		return nil, err
	}
	member, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status", "")
	if status != "approved" && status != "needs_changes" {
		return nil, errors.New("status must be approved or needs_changes")
	}
	db := ctx.AppDB()
	d, err := loadMilestoneDefinition(db, id)
	if err != nil {
		return nil, err
	}
	reviewer := nullableValue(strArg(args, "reviewer_id", ""))
	res, err := db.Exec(`UPDATE member_milestones SET status=?,feedback=?,approved_by=?,approved_at=CASE WHEN ?='approved' THEN CURRENT_TIMESTAMP ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE definition_id=? AND member_id=?`, status, strArg(args, "feedback", ""), reviewer, status, id, member)
	if err != nil {
		return nil, err
	}
	changed, _ := res.RowsAffected()
	if changed == 0 {
		return nil, errors.New("milestone evidence not found")
	}
	communityID, _ := communityBySpace(db, d.SpaceID)
	emit(ctx, "milestone.reviewed", map[string]any{"community_id": communityID, "space_id": d.SpaceID, "member_id": member, "milestone_id": id, "status": status})
	return map[string]any{"definition_id": id, "member_id": member, "status": status, "feedback": strArg(args, "feedback", "")}, nil
}

func loadMilestoneDefinition(db *sql.DB, id string) (MilestoneDefinition, error) {
	var d MilestoneDefinition
	var track sql.NullString
	var req, active int
	err := db.QueryRow(`SELECT id,space_id,track_id,title,description,position,requires_approval,evidence_type,active FROM milestone_definitions WHERE id=?`, id).Scan(&d.ID, &d.SpaceID, &track, &d.Title, &d.Description, &d.Position, &req, &d.EvidenceType, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return d, fmt.Errorf("milestone %q not found", id)
	}
	if track.Valid {
		d.TrackID = &track.String
	}
	d.RequiresApproval = req != 0
	d.Active = active != 0
	return d, err
}
func scanMilestoneDefinition(scan func(...any) error) (MilestoneDefinition, error) {
	var d MilestoneDefinition
	var track sql.NullString
	var req, active int
	err := scan(&d.ID, &d.SpaceID, &track, &d.Title, &d.Description, &d.Position, &req, &d.EvidenceType, &active)
	if track.Valid {
		d.TrackID = &track.String
	}
	d.RequiresApproval = req != 0
	d.Active = active != 0
	return d, err
}
func scanMemberMilestone(db *sql.DB, id, member string, x *MemberMilestone) error {
	var links, files string
	var approvedBy, submitted, approved sql.NullString
	err := db.QueryRow(`SELECT status,evidence_text,evidence_links_json,evidence_files_json,feedback,approved_by,submitted_at,approved_at FROM member_milestones WHERE definition_id=? AND member_id=?`, id, member).Scan(&x.Status, &x.EvidenceText, &links, &files, &x.Feedback, &approvedBy, &submitted, &approved)
	_ = json.Unmarshal([]byte(links), &x.EvidenceLinks)
	_ = json.Unmarshal([]byte(files), &x.EvidenceFiles)
	if approvedBy.Valid {
		x.ApprovedBy = &approvedBy.String
	}
	if submitted.Valid {
		x.SubmittedAt = &submitted.String
	}
	if approved.Valid {
		x.ApprovedAt = &approved.String
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
func nextMilestoneAction(items []MemberMilestone) string {
	for _, m := range items {
		if m.Status == "not_started" || m.Status == "needs_changes" {
			return m.Definition.Title
		}
	}
	return ""
}
