package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var delegatedMemberTools = map[string]bool{
	"communities_list": true, "communities_get": true,
	"members_me": true, "members_list": true, "members_get": true, "members_update": true,
	"spaces_list":  true,
	"threads_list": true, "threads_create": true,
	"posts_list": true, "posts_create": true, "posts_edit": true, "posts_react": true, "posts_remove": true,
	"dms_open": true, "dms_send": true, "dms_list_threads": true, "dms_get_thread": true,
	"dms_mark_read": true, "dms_unread_count": true,
	"courses_get_details": true, "sections_list": true, "lessons_list": true, "lessons_get": true,
	"lessons_mark_complete": true, "lessons_progress": true,
	"lesson_resources_list": true, "lesson_bundle_get": true, "quizzes_list": true, "assignments_list": true,
	"certificates_get": true, "drip_schedule_list": true, "enrollment_rules_get": true,
	"course_enroll": true, "lesson_comments_list": true, "lesson_comments_post": true,
	"course_offer_get": true, "course_purchase_start": true,
	"course_purchase_status": true, "course_purchase_cancel": true,
}

var enrollmentRequiredTools = map[string]bool{
	"lessons_list": true, "lessons_get": true, "lessons_mark_complete": true,
	"lessons_progress": true, "lesson_resources_list": true, "lesson_bundle_get": true, "quizzes_list": true,
	"assignments_list": true, "lesson_comments_list": true, "lesson_comments_post": true,
}

func secureTools(tools []sdk.Tool) []sdk.Tool {
	out := make([]sdk.Tool, len(tools))
	for i := range tools {
		tool := tools[i]
		handler := tool.Handler
		tool.Handler = nil
		tool.HandlerCtx = func(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			caller := sdk.CallerFrom(callCtx)
			if caller == nil || caller.SubjectType == "" {
				return handler(app, args)
			}
			if caller.SubjectType != "user" || caller.SubjectID == "" {
				return nil, errors.New("community delegated access requires a verified user identity")
			}
			if !delegatedMemberTools[tool.Name] {
				return nil, fmt.Errorf("%s is restricted to community operators", tool.Name)
			}
			if tool.Name == "communities_list" {
				return communitiesForSubject(app, caller.SubjectID)
			}
			if tool.Name == "members_me" {
				result, err := membersForSubject(app, caller.SubjectID, strArg(args, "community_id", ""))
				return sanitizeDelegatedMembers(result), err
			}

			safeArgs := cloneArgs(args)
			communityID, err := communityForTool(app, tool.Name, safeArgs)
			if err != nil {
				return nil, err
			}
			member, err := memberForSubject(app, communityID, caller.SubjectID)
			if err != nil {
				return nil, err
			}
			safeArgs["_viewer_member_id"] = member.ID
			safeArgs["_auth_subject_id"] = caller.SubjectID
			safeArgs["_subject_email"] = caller.SubjectEmail
			applyMemberIdentity(tool.Name, safeArgs, member.ID)
			if enrollmentRequiredTools[tool.Name] {
				spaceID, err := courseSpaceForTool(app.AppDB(), tool.Name, safeArgs)
				if err != nil {
					return nil, err
				}
				if err := ensureActiveEnrollment(app.AppDB(), spaceID, member.ID); err != nil {
					return nil, err
				}
				if lessonID := lessonIDForTool(tool.Name, safeArgs); lessonID != "" {
					available, err := lessonAvailableToMember(app.AppDB(), lessonID, member.ID)
					if err != nil {
						return nil, err
					}
					if !available {
						return nil, errors.New("lesson is not available yet")
					}
				}
			}
			result, err := handler(app, safeArgs)
			if err != nil {
				return nil, err
			}
			return sanitizeDelegatedMembers(result), nil
		}
		out[i] = tool
	}
	return out
}

func sanitizeDelegatedMembers(result any) any {
	sanitize := func(member Member) Member {
		member.AuthUserID = nil
		member.ContactID = nil
		return member
	}
	switch value := result.(type) {
	case Member:
		return sanitize(value)
	case map[string]any:
		if member, ok := value["member"].(Member); ok {
			value["member"] = sanitize(member)
		}
		if members, ok := value["members"].([]Member); ok {
			clean := make([]Member, len(members))
			for i := range members {
				clean[i] = sanitize(members[i])
			}
			value["members"] = clean
		}
		if memberships, ok := value["memberships"].([]Member); ok {
			clean := make([]Member, len(memberships))
			for i := range memberships {
				clean[i] = sanitize(memberships[i])
			}
			value["memberships"] = clean
		}
	}
	return result
}

func lessonIDForTool(tool string, args map[string]any) string {
	switch tool {
	case "lessons_get", "lesson_bundle_get":
		return strArg(args, "id", "")
	case "lessons_mark_complete", "lesson_resources_list", "quizzes_list",
		"assignments_list", "lesson_comments_list", "lesson_comments_post":
		return strArg(args, "lesson_id", "")
	default:
		return ""
	}
}

func cloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args)+2)
	for k, v := range args {
		out[k] = v
	}
	return out
}

func applyMemberIdentity(tool string, args map[string]any, memberID string) {
	switch tool {
	case "threads_create", "posts_create", "dms_send":
		args["author_id"] = memberID
	case "posts_edit", "posts_remove", "dms_get_thread":
		args["caller_member_id"] = memberID
	case "posts_react", "dms_mark_read", "dms_list_threads", "dms_unread_count",
		"lessons_mark_complete", "lessons_progress", "course_enroll", "lesson_comments_post",
		"course_purchase_start", "course_purchase_status", "course_purchase_cancel":
		args["member_id"] = memberID
	case "members_update":
		args["id"] = memberID
		delete(args, "status")
		delete(args, "contact_id")
		delete(args, "auth_user_id")
	case "lessons_list", "lessons_get":
		args["member_id"] = memberID
		args["include_drafts"] = false
	case "dms_open":
		raw, _ := args["participants"].([]any)
		participants := make([]any, 0, len(raw)+1)
		found := false
		for _, value := range raw {
			if value == "self" {
				if !found {
					participants = append(participants, memberID)
					found = true
				}
				continue
			}
			participants = append(participants, value)
			if value == memberID {
				found = true
			}
		}
		if !found {
			participants = append(participants, memberID)
		}
		args["participants"] = participants
	}
}

func operatorHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Type")) != "" {
			writeErr(w, http.StatusForbidden, "member clients must use the scoped Community tools")
			return
		}
		next(w, r)
	}
}

func communitiesForSubject(ctx *sdk.AppCtx, subjectID string) (any, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT c.id, c.project_id, c.slug, c.name, c.description, c.created_at, c.archived_at,
		        m.id, m.community_id, m.contact_id, m.auth_user_id, m.handle, m.display_name,
		        m.bio, m.status, m.joined_at, m.last_seen_at
		   FROM members m
		   JOIN communities c ON c.id = m.community_id
		  WHERE c.project_id = ? AND c.archived_at IS NULL
		    AND m.auth_user_id = ? AND m.status = 'active'
		  ORDER BY c.name, m.joined_at`,
		scopeProject(ctx), subjectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	communities := []Community{}
	memberships := []Member{}
	for rows.Next() {
		c, m, err := scanCommunityMember(rows.Scan)
		if err != nil {
			return nil, err
		}
		communities = append(communities, c)
		memberships = append(memberships, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"communities": communities, "memberships": memberships}, nil
}

func scanCommunityMember(scan func(...any) error) (Community, Member, error) {
	var (
		c                       Community
		m                       Member
		communityArchived       sql.NullString
		contact, authUser, seen sql.NullString
	)
	err := scan(
		&c.ID, &c.ProjectID, &c.Slug, &c.Name, &c.Description, &c.CreatedAt, &communityArchived,
		&m.ID, &m.CommunityID, &contact, &authUser, &m.Handle, &m.DisplayName, &m.Bio, &m.Status, &m.JoinedAt, &seen,
	)
	if err != nil {
		return c, m, err
	}
	if communityArchived.Valid {
		v := communityArchived.String
		c.ArchivedAt = &v
	}
	if contact.Valid {
		v := contact.String
		m.ContactID = &v
	}
	if authUser.Valid {
		v := authUser.String
		m.AuthUserID = &v
	}
	if seen.Valid {
		v := seen.String
		m.LastSeenAt = &v
	}
	return c, m, nil
}

func membersForSubject(ctx *sdk.AppCtx, subjectID, communityID string) (any, error) {
	if communityID != "" {
		member, err := memberForSubject(ctx, communityID, subjectID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"member": member}, nil
	}
	result, err := communitiesForSubject(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func memberForSubject(ctx *sdk.AppCtx, communityID, subjectID string) (Member, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT `+memberCols+`
		   FROM members m
		  WHERE m.community_id = ? AND m.auth_user_id = ? AND m.status = 'active'
		    AND EXISTS (
		      SELECT 1 FROM communities c
		       WHERE c.id = m.community_id AND c.project_id = ? AND c.archived_at IS NULL
		    )`,
		communityID, subjectID, scopeProject(ctx),
	)
	member, err := scanMember(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return member, errors.New("your Auth user is not linked to an active member of this community")
	}
	return member, err
}

func communityForTool(ctx *sdk.AppCtx, tool string, args map[string]any) (string, error) {
	db := ctx.AppDB()
	if id := strArg(args, "community_id", ""); id != "" {
		return id, nil
	}
	switch tool {
	case "communities_get":
		if id := strArg(args, "id", ""); id != "" {
			return id, nil
		}
		c, err := loadCommunityBySlug(db, scopeProject(ctx), strArg(args, "slug", ""))
		return c.ID, err
	case "members_get", "members_update":
		if id := strArg(args, "id", ""); id != "" {
			m, err := loadMember(db, id)
			return m.CommunityID, err
		}
	case "spaces_list":
		return mustStr(args, "community_id")
	case "threads_create", "threads_list", "courses_get_details", "sections_list",
		"lessons_list", "lessons_progress", "certificates_get", "drip_schedule_list",
		"enrollment_rules_get", "course_enroll", "course_offer_get", "course_purchase_start":
		return communityBySpace(db, strArg(args, "space_id", ""))
	case "course_purchase_status", "course_purchase_cancel":
		if spaceID := strArg(args, "space_id", ""); spaceID != "" {
			return communityBySpace(db, spaceID)
		}
		return communityByPurchase(db, strArg(args, "purchase_id", ""))
	case "posts_create", "posts_list":
		return communityByThread(db, strArg(args, "thread_id", ""))
	case "posts_edit", "posts_remove":
		return communityByPost(db, strArg(args, "id", ""))
	case "posts_react":
		return communityByPost(db, strArg(args, "post_id", ""))
	case "dms_send", "dms_mark_read":
		return communityByDM(db, strArg(args, "dm_thread_id", ""))
	case "dms_get_thread":
		return communityByDM(db, strArg(args, "id", ""))
	case "dms_open", "dms_list_threads", "dms_unread_count":
		return mustStr(args, "community_id")
	case "lessons_get", "lesson_bundle_get":
		return communityByLesson(db, strArg(args, "id", ""))
	case "lessons_mark_complete":
		return communityByLesson(db, strArg(args, "lesson_id", ""))
	case "lesson_resources_list", "quizzes_list", "assignments_list", "lesson_comments_list", "lesson_comments_post":
		return communityByLesson(db, strArg(args, "lesson_id", ""))
	}
	return "", fmt.Errorf("cannot resolve community for %s", tool)
}

func communityBySpace(db *sql.DB, id string) (string, error) {
	var communityID string
	err := db.QueryRow(`SELECT community_id FROM spaces WHERE id = ?`, id).Scan(&communityID)
	return communityID, notFound(err, "space")
}

func communityByThread(db *sql.DB, id string) (string, error) {
	var communityID string
	err := db.QueryRow(`SELECT community_id FROM threads WHERE id = ?`, id).Scan(&communityID)
	return communityID, notFound(err, "thread")
}

func communityByPost(db *sql.DB, id string) (string, error) {
	var communityID string
	err := db.QueryRow(`SELECT community_id FROM posts WHERE id = ?`, id).Scan(&communityID)
	return communityID, notFound(err, "post")
}

func communityByDM(db *sql.DB, id string) (string, error) {
	var communityID string
	err := db.QueryRow(`SELECT community_id FROM dm_threads WHERE id = ?`, id).Scan(&communityID)
	return communityID, notFound(err, "dm thread")
}

func communityByLesson(db *sql.DB, id string) (string, error) {
	var communityID string
	err := db.QueryRow(`SELECT community_id FROM lessons WHERE id = ?`, id).Scan(&communityID)
	return communityID, notFound(err, "lesson")
}

func communityByPurchase(db *sql.DB, id string) (string, error) {
	var communityID string
	err := db.QueryRow(`SELECT community_id FROM course_purchases WHERE id = ?`, id).Scan(&communityID)
	return communityID, notFound(err, "course purchase")
}

func notFound(err error, kind string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s not found", kind)
	}
	return err
}

func courseSpaceForTool(db *sql.DB, tool string, args map[string]any) (string, error) {
	switch tool {
	case "lessons_list", "lessons_progress":
		return mustStr(args, "space_id")
	case "lessons_get", "lesson_bundle_get":
		return spaceByLesson(db, strArg(args, "id", ""))
	default:
		return spaceByLesson(db, strArg(args, "lesson_id", ""))
	}
}

func spaceByLesson(db *sql.DB, lessonID string) (string, error) {
	var spaceID string
	err := db.QueryRow(
		`SELECT s.space_id FROM lessons l JOIN sections s ON s.id = l.section_id WHERE l.id = ?`,
		lessonID,
	).Scan(&spaceID)
	return spaceID, notFound(err, "lesson")
}

func ensureActiveEnrollment(db *sql.DB, spaceID, memberID string) error {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*)
		   FROM course_enrollments e
		   LEFT JOIN enrollment_rules r ON r.space_id = e.space_id
		  WHERE e.space_id = ? AND e.member_id = ? AND e.status IN ('active','completed')
		    AND e.access_revoked_at IS NULL
		    AND (e.access_expires_at IS NULL OR datetime(e.access_expires_at) >= CURRENT_TIMESTAMP)
		    AND (r.starts_at IS NULL OR datetime(r.starts_at) <= CURRENT_TIMESTAMP)
		    AND (r.ends_at IS NULL OR datetime(r.ends_at) >= CURRENT_TIMESTAMP)`,
		spaceID, memberID,
	).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("active course enrollment required")
	}
	return nil
}

func lessonAvailableToMember(db *sql.DB, lessonID, memberID string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*)
		   FROM lessons l
		   JOIN sections s ON s.id = l.section_id
		   JOIN course_enrollments e ON e.space_id = s.space_id AND e.member_id = ?
		   LEFT JOIN drip_schedules d ON d.lesson_id = l.id
		  WHERE l.id = ? AND l.published_at IS NOT NULL
		    AND e.status IN ('active','completed')
		    AND e.access_revoked_at IS NULL
		    AND (e.access_expires_at IS NULL OR datetime(e.access_expires_at) >= CURRENT_TIMESTAMP)
		    AND (d.release_at IS NULL OR datetime(d.release_at) <= CURRENT_TIMESTAMP)
		    AND (d.release_after_days IS NULL OR
		         datetime(e.enrolled_at, '+' || d.release_after_days || ' days') <= CURRENT_TIMESTAMP)`,
		memberID, lessonID,
	).Scan(&count)
	return count > 0, err
}
