// Members — the human (or agent) actors inside a community.
//
// 0.1 surface: create, list, get. Tier/role/billing live in 0.4+.
//
// Handle is unique within a community. contact_id is optional and only
// matters when the user has also bound the crm app — community works
// fine without it.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type Member struct {
	ID          string  `json:"id"`
	CommunityID string  `json:"community_id"`
	ContactID   *string `json:"contact_id,omitempty"`
	AuthUserID  *string `json:"auth_user_id,omitempty"`
	Handle      string  `json:"handle"`
	DisplayName string  `json:"display_name"`
	Bio         string  `json:"bio"`
	Status      string  `json:"status"`
	JoinedAt    string  `json:"joined_at"`
	LastSeenAt  *string `json:"last_seen_at,omitempty"`
}

func membersTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "members_create",
			Description: "Create a member in a community. Args: community_id (required), handle (required, lower-case, [a-z0-9_-]), display_name?, bio?, contact_id? (crm link).",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"handle":       map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"bio":          map[string]any{"type": "string"},
				"contact_id":   map[string]any{"type": "string"},
				"auth_user_id": map[string]any{"type": "string"},
			}, []string{"community_id", "handle"}),
			Handler: toolMembersCreate,
		},
		{
			Name:        "members_list",
			Description: "List members of a community. Args: community_id (required), status? (active|suspended|left; default 'active'), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"community_id"}),
			Handler: toolMembersList,
		},
		{
			Name:        "members_get",
			Description: "Fetch one member by id, or by (community_id + handle). Args: id? or {community_id + handle}.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "string"},
				"community_id": map[string]any{"type": "string"},
				"handle":       map[string]any{"type": "string"},
			}, nil),
			Handler: toolMembersGet,
		},
		{
			Name:        "members_me",
			Description: "Return the member linked to the verified Auth user. Delegated-user calls only. Args: community_id?.",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
			}, nil),
			Handler: toolMembersMe,
		},
		{
			Name:        "members_ensure",
			Description: "Create or return the active Community member linked to the verified visitor. Community portal use only.",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
			}, []string{"community_id"}),
			Handler: toolMembersEnsure,
		},
		{
			Name:        "members_update",
			Description: "Update a member's display_name, bio, status, or contact_id. Args: id (required), display_name?, bio?, status? (active|suspended|left), contact_id? (empty string clears).",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"bio":          map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string"},
				"contact_id":   map[string]any{"type": "string"},
				"auth_user_id": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolMembersUpdate,
		},
	}
}

func toolMembersMe(*sdk.AppCtx, map[string]any) (any, error) {
	return nil, errors.New("members_me requires a delegated Auth user")
}

func toolMembersEnsure(*sdk.AppCtx, map[string]any) (any, error) {
	return nil, errors.New("members_ensure requires a delegated Auth user")
}

func ensureMemberForSubject(ctx *sdk.AppCtx, caller *sdk.Caller, communityID, requestedName string) (any, error) {
	communityID = strings.TrimSpace(communityID)
	if communityID == "" {
		return nil, errors.New("community_id is required")
	}
	if caller == nil || caller.SubjectType != "user" || strings.TrimSpace(caller.SubjectID) == "" {
		return nil, errors.New("sign in is required")
	}
	community, err := loadCommunity(ctx.AppDB(), communityID)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityReadable(ctx, community); err != nil {
		return nil, err
	}
	if community.AuthOrganizationID == "" && community.AuthOrganizationSlug == "" {
		return nil, errors.New("this community's sign-in is not configured")
	}
	if community.AuthOrganizationID != "" && community.AuthOrganizationID != strings.TrimSpace(caller.OrganizationID) {
		return nil, errors.New("this account belongs to a different community")
	}
	if community.AuthOrganizationSlug != "" && !strings.EqualFold(community.AuthOrganizationSlug, strings.TrimSpace(caller.OrganizationSlug)) {
		return nil, errors.New("this account belongs to a different community")
	}

	existing, err := loadMemberByAuthUser(ctx.AppDB(), communityID, caller.SubjectID)
	if err == nil {
		if existing.Status != "active" {
			return nil, errors.New("your community access is not active")
		}
		return map[string]any{"member": existing, "created": false}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if community.SignupMode != "open" || !community.AutoCreateMembers {
		return nil, errors.New("this community requires an existing membership")
	}

	displayName := strings.TrimSpace(requestedName)
	if len(displayName) > 120 {
		displayName = displayName[:120]
	}
	if displayName == "" {
		displayName = memberNameFromEmail(caller.SubjectEmail)
	}
	handle, err := availableMemberHandle(ctx.AppDB(), communityID, displayName, caller.SubjectEmail)
	if err != nil {
		return nil, err
	}
	created, err := toolMembersCreate(ctx, map[string]any{
		"community_id": communityID,
		"handle":       handle,
		"display_name": displayName,
		"auth_user_id": caller.SubjectID,
	})
	if err != nil {
		// A repeated browser request may race the first insert. The unique
		// auth-user index makes that safe; return the row that won.
		if member, lookupErr := loadMemberByAuthUser(ctx.AppDB(), communityID, caller.SubjectID); lookupErr == nil {
			return map[string]any{"member": member, "created": false}, nil
		}
		return nil, err
	}
	return map[string]any{"member": created, "created": true}, nil
}

func loadMemberByAuthUser(db *sql.DB, communityID, authUserID string) (Member, error) {
	row := db.QueryRow(
		`SELECT `+memberCols+` FROM members WHERE community_id = ? AND auth_user_id = ?`,
		communityID, authUserID,
	)
	return scanMember(row.Scan)
}

var handleCleanupRE = regexp.MustCompile(`[^a-z0-9_-]+`)

func availableMemberHandle(db *sql.DB, communityID, displayName, email string) (string, error) {
	base := strings.ToLower(strings.TrimSpace(displayName))
	if base == "" {
		base = strings.SplitN(strings.ToLower(strings.TrimSpace(email)), "@", 2)[0]
	}
	base = handleCleanupRE.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_-")
	if len(base) > 24 {
		base = base[:24]
	}
	if len(base) < 2 || base[0] < 'a' || base[0] > 'z' {
		base = "member_" + base
	}
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s_%d", base, i+1)
		}
		if len(candidate) > 31 {
			candidate = candidate[:31]
		}
		var one int
		err := db.QueryRow(`SELECT 1 FROM members WHERE community_id = ? AND handle = ?`, communityID, candidate).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a member handle")
}

func memberNameFromEmail(email string) string {
	local := strings.SplitN(strings.TrimSpace(email), "@", 2)[0]
	local = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(local)
	local = strings.TrimSpace(local)
	if local == "" {
		return "Member"
	}
	words := strings.Fields(local)
	for i := range words {
		if len(words[i]) > 0 {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
	}
	return strings.Join(words, " ")
}

var memberStatuses = map[string]bool{"active": true, "suspended": true, "left": true}

func toolMembersUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	cur, err := loadMember(db, id)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, db, cur.CommunityID); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["display_name"].(string); ok {
		sets = append(sets, "display_name = ?")
		vals = append(vals, v)
	}
	if v, ok := args["bio"].(string); ok {
		sets = append(sets, "bio = ?")
		vals = append(vals, v)
	}
	statusChanged := ""
	if v, ok := args["status"].(string); ok && v != "" {
		if !memberStatuses[v] {
			return nil, fmt.Errorf("status %q invalid", v)
		}
		sets = append(sets, "status = ?")
		vals = append(vals, v)
		if v != cur.Status {
			statusChanged = v
		}
	}
	if v, ok := args["contact_id"].(string); ok {
		if v == "" {
			sets = append(sets, "contact_id = NULL")
		} else {
			sets = append(sets, "contact_id = ?")
			vals = append(vals, v)
		}
	}
	if v, ok := args["auth_user_id"].(string); ok {
		if v == "" {
			sets = append(sets, "auth_user_id = NULL")
		} else {
			sets = append(sets, "auth_user_id = ?")
			vals = append(vals, v)
		}
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	vals = append(vals, id)
	if _, err := db.Exec(
		`UPDATE members SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...,
	); err != nil {
		return nil, err
	}
	m, err := loadMember(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "member.updated", map[string]any{
		"community_id": m.CommunityID,
		"member_id":    m.ID,
	})
	if statusChanged != "" {
		emit(ctx, "member.status_changed", map[string]any{
			"community_id": m.CommunityID,
			"member_id":    m.ID,
			"status":       statusChanged,
		})
	}
	return m, nil
}

var handleRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,30}$`)

func toolMembersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	handle, err := mustStr(args, "handle")
	if err != nil {
		return nil, err
	}
	handle = strings.ToLower(handle)
	if !handleRE.MatchString(handle) {
		return nil, fmt.Errorf("handle %q invalid: must be lowercase, 2-31 chars, [a-z0-9_-]", handle)
	}
	displayName := strArg(args, "display_name", "")
	bio := strArg(args, "bio", "")
	contactID := strArg(args, "contact_id", "")
	authUserID := strArg(args, "auth_user_id", "")

	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	id := newID("m")
	var contactArg any
	if contactID != "" {
		contactArg = contactID
	}
	var authUserArg any
	if authUserID != "" {
		authUserArg = authUserID
	}
	_, err = db.Exec(
		`INSERT INTO members (id, community_id, contact_id, auth_user_id, handle, display_name, bio)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, communityID, contactArg, authUserArg, handle, displayName, bio,
	)
	if err != nil {
		return nil, fmt.Errorf("create member: %w", err)
	}
	m, err := loadMember(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "member.joined", map[string]any{
		"community_id": m.CommunityID,
		"member_id":    m.ID,
		"handle":       m.Handle,
	})
	return m, nil
}

func toolMembersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, ctx.AppDB(), communityID); err != nil {
		return nil, err
	}
	status := strArg(args, "status", "active")
	limit := boundedLimit(args, "limit", 200, 500)
	rows, err := ctx.AppDB().Query(
		`SELECT `+memberCols+` FROM members
		 WHERE community_id = ? AND status = ?
		 ORDER BY joined_at DESC LIMIT ?`,
		communityID, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		m, err := scanMember(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"members": out}, nil
}

func toolMembersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strArg(args, "id", "")
	communityID := strArg(args, "community_id", "")
	handle := strArg(args, "handle", "")
	db := ctx.AppDB()
	if id != "" {
		m, err := loadMember(db, id)
		if err != nil {
			return nil, err
		}
		if err := ensureCommunityVisible(ctx, db, m.CommunityID); err != nil {
			return nil, err
		}
		return m, nil
	}
	if communityID != "" && handle != "" {
		if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
			return nil, err
		}
		return loadMemberByHandle(db, communityID, handle)
	}
	return nil, errors.New("id or (community_id + handle) required")
}

// ─── DB helpers ──────────────────────────────────────────────────

const memberCols = `id, community_id, contact_id, auth_user_id, handle, display_name, bio, status, joined_at, last_seen_at`

func scanMember(scan func(...any) error) (Member, error) {
	var m Member
	var contact, authUser, last sql.NullString
	if err := scan(&m.ID, &m.CommunityID, &contact, &authUser, &m.Handle, &m.DisplayName, &m.Bio, &m.Status, &m.JoinedAt, &last); err != nil {
		return m, err
	}
	if authUser.Valid {
		v := authUser.String
		m.AuthUserID = &v
	}
	if contact.Valid {
		v := contact.String
		m.ContactID = &v
	}
	if last.Valid {
		v := last.String
		m.LastSeenAt = &v
	}
	return m, nil
}

func loadMember(db *sql.DB, id string) (Member, error) {
	row := db.QueryRow(`SELECT `+memberCols+` FROM members WHERE id = ?`, id)
	m, err := scanMember(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return m, fmt.Errorf("member %q not found", id)
	}
	return m, err
}

func loadMemberByHandle(db *sql.DB, communityID, handle string) (Member, error) {
	row := db.QueryRow(
		`SELECT `+memberCols+` FROM members WHERE community_id = ? AND handle = ?`,
		communityID, handle,
	)
	m, err := scanMember(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return m, fmt.Errorf("member %q not found in community", handle)
	}
	return m, err
}

// ─── HTTP ────────────────────────────────────────────────────────

func (a *App) httpMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	communityID := r.URL.Query().Get("community_id")
	if communityID == "" {
		writeErr(w, 400, "community_id required")
		return
	}
	out, err := toolMembersList(globalCtx, map[string]any{
		"community_id": communityID,
		"status":       r.URL.Query().Get("status"),
	})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}
