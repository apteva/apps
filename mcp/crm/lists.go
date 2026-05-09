// Lists — explicit, configurable buckets of contacts.
//
// A contact can belong to N lists. Each list carries its own sender
// defaults so a single CRM install can serve multiple brands/products
// without duplicating contact records. The list also names an inbound
// pattern that the messaging coupling uses to route mail back to the
// right list automatically.
//
// MCP tools live here: lists_create / list / get / update / archive,
// lists_add_contact / remove_contact / membership.
// HTTP routes: /lists, /lists/{id}, /lists/{id}/members.

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Domain type ──────────────────────────────────────────────────

type List struct {
	ID                  int64  `json:"id"`
	ProjectID           string `json:"project_id,omitempty"`
	Slug                string `json:"slug"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	DefaultSenderEmail  string `json:"default_sender_email,omitempty"`
	DefaultSenderPhone  string `json:"default_sender_phone,omitempty"`
	InboundRoutePattern string `json:"inbound_route_pattern,omitempty"`
	ArchivedAt          string `json:"archived_at,omitempty"`
	CreatedAt           string `json:"created_at,omitempty"`
	UpdatedAt           string `json:"updated_at,omitempty"`
	MemberCount         int64  `json:"member_count,omitempty"`
}

// ─── DB helpers ───────────────────────────────────────────────────

func dbListCreate(db *sql.DB, pid string, l *List) (*List, error) {
	if l.Slug == "" {
		l.Slug = slugifyName(l.Name)
	}
	if l.Slug == "" {
		return nil, errors.New("slug required (or a non-empty name to derive from)")
	}
	if l.Name == "" {
		l.Name = l.Slug
	}
	res, err := db.Exec(
		`INSERT INTO contact_lists
			(project_id, slug, name, description,
			 default_sender_email, default_sender_phone, inbound_route_pattern,
			 created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, l.Slug, l.Name, nullStr(l.Description),
		nullStr(l.DefaultSenderEmail), nullStr(l.DefaultSenderPhone), nullStr(l.InboundRoutePattern),
	)
	if err != nil {
		// Unique slug collision is the common case — surface it cleanly.
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("slug %q is already in use in this project", l.Slug)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbListGet(db, pid, id)
}

func dbListGet(db *sql.DB, pid string, id int64) (*List, error) {
	row := db.QueryRow(
		`SELECT id, slug, name, COALESCE(description,''),
				COALESCE(default_sender_email,''), COALESCE(default_sender_phone,''),
				COALESCE(inbound_route_pattern,''),
				COALESCE(archived_at,''), created_at, updated_at
		 FROM contact_lists WHERE project_id = ? AND id = ?`,
		pid, id,
	)
	l := &List{}
	if err := row.Scan(&l.ID, &l.Slug, &l.Name, &l.Description,
		&l.DefaultSenderEmail, &l.DefaultSenderPhone, &l.InboundRoutePattern,
		&l.ArchivedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return l, nil
}

func dbListBySlug(db *sql.DB, pid, slug string) (*List, error) {
	row := db.QueryRow(
		`SELECT id, slug, name, COALESCE(description,''),
				COALESCE(default_sender_email,''), COALESCE(default_sender_phone,''),
				COALESCE(inbound_route_pattern,''),
				COALESCE(archived_at,''), created_at, updated_at
		 FROM contact_lists WHERE project_id = ? AND slug = ?`,
		pid, slug,
	)
	l := &List{}
	if err := row.Scan(&l.ID, &l.Slug, &l.Name, &l.Description,
		&l.DefaultSenderEmail, &l.DefaultSenderPhone, &l.InboundRoutePattern,
		&l.ArchivedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return l, nil
}

// dbListByInboundPattern returns the (active) list whose inbound
// pattern matches verbatim. Used by the inbound webhook to derive
// list_id from the messaging payload's matched_pattern.
func dbListByInboundPattern(db *sql.DB, pid, pattern string) (*List, error) {
	if pattern == "" {
		return nil, nil
	}
	row := db.QueryRow(
		`SELECT id, slug, name, COALESCE(description,''),
				COALESCE(default_sender_email,''), COALESCE(default_sender_phone,''),
				COALESCE(inbound_route_pattern,''),
				COALESCE(archived_at,''), created_at, updated_at
		 FROM contact_lists
		 WHERE project_id = ? AND inbound_route_pattern = ? AND archived_at IS NULL
		 LIMIT 1`,
		pid, pattern,
	)
	l := &List{}
	if err := row.Scan(&l.ID, &l.Slug, &l.Name, &l.Description,
		&l.DefaultSenderEmail, &l.DefaultSenderPhone, &l.InboundRoutePattern,
		&l.ArchivedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return l, nil
}

func dbListsAll(db *sql.DB, pid string, includeArchived bool) ([]*List, error) {
	where := "project_id = ?"
	if !includeArchived {
		where += " AND archived_at IS NULL"
	}
	rows, err := db.Query(
		`SELECT cl.id, cl.slug, cl.name, COALESCE(cl.description,''),
				COALESCE(cl.default_sender_email,''), COALESCE(cl.default_sender_phone,''),
				COALESCE(cl.inbound_route_pattern,''),
				COALESCE(cl.archived_at,''), cl.created_at, cl.updated_at,
				(SELECT COUNT(*) FROM contact_list_members WHERE list_id = cl.id) AS member_count
		 FROM contact_lists cl
		 WHERE `+where+`
		 ORDER BY cl.name COLLATE NOCASE`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*List{}
	for rows.Next() {
		l := &List{}
		if err := rows.Scan(&l.ID, &l.Slug, &l.Name, &l.Description,
			&l.DefaultSenderEmail, &l.DefaultSenderPhone, &l.InboundRoutePattern,
			&l.ArchivedAt, &l.CreatedAt, &l.UpdatedAt, &l.MemberCount); err == nil {
			out = append(out, l)
		}
	}
	return out, nil
}

func dbListUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*List, error) {
	// Whitelist mutable fields. Slug stays immutable to avoid breaking
	// references in user docs / agent prompts; rename via name+description.
	allowed := map[string]bool{
		"name": true, "description": true,
		"default_sender_email": true, "default_sender_phone": true,
		"inbound_route_pattern": true,
	}
	sets := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		sets = append(sets, k+" = ?")
		if s, ok := v.(string); ok && s == "" {
			args = append(args, nil)
		} else {
			args = append(args, v)
		}
	}
	if len(sets) == 0 {
		return dbListGet(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, pid, id)
	if _, err := db.Exec(
		`UPDATE contact_lists SET `+strings.Join(sets, ", ")+
			` WHERE project_id = ? AND id = ?`,
		args...,
	); err != nil {
		return nil, err
	}
	return dbListGet(db, pid, id)
}

func dbListArchive(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(
		`UPDATE contact_lists SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ? AND archived_at IS NULL`,
		pid, id,
	)
	return err
}

// dbListAddContact is idempotent — re-adding the same contact is a
// no-op (INSERT OR IGNORE). The caller decides whether "already a
// member" should be a soft success or a separate signal.
func dbListAddContact(db *sql.DB, pid string, listID, contactID int64, source string) error {
	if source == "" {
		source = "human"
	}
	_, err := db.Exec(
		`INSERT OR IGNORE INTO contact_list_members
			(list_id, contact_id, project_id, source)
		 VALUES (?, ?, ?, ?)`,
		listID, contactID, pid, source,
	)
	return err
}

func dbListRemoveContact(db *sql.DB, pid string, listID, contactID int64) error {
	_, err := db.Exec(
		`DELETE FROM contact_list_members
		 WHERE list_id = ? AND contact_id = ? AND project_id = ?`,
		listID, contactID, pid,
	)
	return err
}

// dbListsForContact returns the active lists a contact belongs to.
// Cheap enough to call on every contacts_get_context.
func dbListsForContact(db *sql.DB, pid string, contactID int64) ([]*List, error) {
	rows, err := db.Query(
		`SELECT cl.id, cl.slug, cl.name, COALESCE(cl.description,''),
				COALESCE(cl.default_sender_email,''), COALESCE(cl.default_sender_phone,''),
				COALESCE(cl.inbound_route_pattern,''),
				COALESCE(cl.archived_at,''), cl.created_at, cl.updated_at
		 FROM contact_lists cl
		 JOIN contact_list_members m ON m.list_id = cl.id
		 WHERE cl.project_id = ? AND m.contact_id = ? AND cl.archived_at IS NULL
		 ORDER BY cl.name COLLATE NOCASE`,
		pid, contactID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*List{}
	for rows.Next() {
		l := &List{}
		if err := rows.Scan(&l.ID, &l.Slug, &l.Name, &l.Description,
			&l.DefaultSenderEmail, &l.DefaultSenderPhone, &l.InboundRoutePattern,
			&l.ArchivedAt, &l.CreatedAt, &l.UpdatedAt); err == nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// dbListMembers returns paginated members of a list.
func dbListMembers(db *sql.DB, pid string, listID int64, limit int) ([]*Contact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(
		`SELECT c.id, COALESCE(c.first_name,''), COALESCE(c.last_name,''),
				COALESCE(c.display_name,''), COALESCE(c.primary_email,''),
				COALESCE(c.primary_phone,''), COALESCE(c.company,''),
				COALESCE(c.job_title,''), COALESCE(c.status,'active')
		 FROM contacts c
		 JOIN contact_list_members m ON m.contact_id = c.id
		 WHERE m.list_id = ? AND m.project_id = ? AND c.deleted_at IS NULL
		 ORDER BY m.added_at DESC LIMIT ?`,
		listID, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Contact{}
	for rows.Next() {
		c := &Contact{}
		if err := rows.Scan(&c.ID, &c.FirstName, &c.LastName, &c.DisplayName,
			&c.PrimaryEmail, &c.PrimaryPhone, &c.Company, &c.JobTitle, &c.Status); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

// ─── MCP tools ────────────────────────────────────────────────────

func (a *App) toolListsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	l := &List{
		Slug:                strArg(args, "slug"),
		Name:                strArg(args, "name"),
		Description:         strArg(args, "description"),
		DefaultSenderEmail:  strArg(args, "default_sender_email"),
		DefaultSenderPhone:  strArg(args, "default_sender_phone"),
		InboundRoutePattern: strArg(args, "inbound_route_pattern"),
	}
	if l.Name == "" && l.Slug == "" {
		return nil, errors.New("name or slug required")
	}
	out, err := dbListCreate(ctx.AppDB(), pid, l)
	if err != nil {
		return nil, err
	}
	ctx.Emit("list.created", map[string]any{"id": out.ID, "slug": out.Slug, "name": out.Name})
	return map[string]any{"list": out}, nil
}

func (a *App) toolListsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	out, err := dbListsAll(ctx.AppDB(), pid, includeArchived)
	if err != nil {
		return nil, err
	}
	return map[string]any{"lists": out, "count": len(out)}, nil
}

func (a *App) toolListsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	slug := strArg(args, "slug")
	var l *List
	if id != 0 {
		l, err = dbListGet(ctx.AppDB(), pid, id)
	} else if slug != "" {
		l, err = dbListBySlug(ctx.AppDB(), pid, slug)
	} else {
		return nil, errors.New("id or slug required")
	}
	if err != nil {
		return nil, err
	}
	if l == nil {
		return map[string]any{"list": nil, "found": false}, nil
	}
	return map[string]any{"list": l, "found": true}, nil
}

func (a *App) toolListsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch object required")
	}
	out, err := dbListUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	ctx.Emit("list.updated", map[string]any{"id": id})
	return map[string]any{"list": out}, nil
}

func (a *App) toolListsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbListArchive(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	ctx.Emit("list.archived", map[string]any{"id": id})
	return map[string]any{"archived": true, "id": id}, nil
}

func (a *App) toolListsAddContact(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	listID := int64Arg(args, "list_id")
	cid := int64Arg(args, "contact_id")
	if listID == 0 || cid == 0 {
		return nil, errors.New("list_id and contact_id required")
	}
	source := strArg(args, "source")
	if err := dbListAddContact(ctx.AppDB(), pid, listID, cid, source); err != nil {
		return nil, err
	}
	ctx.Emit("list.member.added", map[string]any{"list_id": listID, "contact_id": cid})
	return map[string]any{"added": true, "list_id": listID, "contact_id": cid}, nil
}

func (a *App) toolListsRemoveContact(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	listID := int64Arg(args, "list_id")
	cid := int64Arg(args, "contact_id")
	if listID == 0 || cid == 0 {
		return nil, errors.New("list_id and contact_id required")
	}
	if err := dbListRemoveContact(ctx.AppDB(), pid, listID, cid); err != nil {
		return nil, err
	}
	ctx.Emit("list.member.removed", map[string]any{"list_id": listID, "contact_id": cid})
	return map[string]any{"removed": true, "list_id": listID, "contact_id": cid}, nil
}

// toolListsEval — bulk member-id dump for a list. Mirrors
// segments_eval's shape so a downstream consumer (campaigns) can
// expand either kind of audience uniformly. Returns active members
// only — archived contacts are filtered out at the SQL layer.
func (a *App) toolListsEval(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	listID := int64Arg(args, "id")
	if listID == 0 {
		listID = int64Arg(args, "list_id")
	}
	if listID == 0 {
		return nil, errors.New("id required")
	}
	limit := intArg(args, "limit", 5000)
	if limit <= 0 || limit > 50000 {
		limit = 5000
	}
	// Hot-path: single JOIN against contacts to filter out soft-
	// deleted / non-active contacts. Cheaper than a two-step.
	rows, err := ctx.AppDB().Query(
		`SELECT c.id FROM contact_list_members m
		 JOIN contacts c ON c.id = m.contact_id
		 WHERE m.list_id = ? AND m.project_id = ?
		   AND c.deleted_at IS NULL AND (c.status IS NULL OR c.status = 'active')
		 ORDER BY c.id LIMIT ?`,
		listID, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	// Total count (independent of limit) — same JOIN.
	var total int64
	totalRow := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_list_members m
		 JOIN contacts c ON c.id = m.contact_id
		 WHERE m.list_id = ? AND m.project_id = ?
		   AND c.deleted_at IS NULL AND (c.status IS NULL OR c.status = 'active')`,
		listID, pid,
	)
	_ = totalRow.Scan(&total)
	return map[string]any{"contact_ids": out, "count": total}, nil
}

func (a *App) toolListsMembership(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "contact_id")
	if cid == 0 {
		return nil, errors.New("contact_id required")
	}
	out, err := dbListsForContact(ctx.AppDB(), pid, cid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"lists": out, "count": len(out)}, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────

// handleHTTPLists dispatches GET (list) / POST (create) on /lists.
func (a *App) handleHTTPLists(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPListsGet(w, r)
	case http.MethodPost:
		a.handleHTTPListsCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHTTPListItem handles /lists/{id}[/members[/{contact_id}]].
func (a *App) handleHTTPListItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/lists/")
	parts := strings.SplitN(rest, "/", 3)
	listID, _ := strconv.ParseInt(parts[0], 10, 64)
	if listID == 0 {
		httpErr(w, http.StatusBadRequest, "list id required")
		return
	}
	if len(parts) >= 2 && parts[1] == "members" {
		switch r.Method {
		case http.MethodGet:
			a.handleHTTPListMembers(w, r, listID)
		case http.MethodPost:
			a.handleHTTPListAddMember(w, r, listID)
		case http.MethodDelete:
			if len(parts) < 3 || parts[2] == "" {
				httpErr(w, http.StatusBadRequest, "contact id required")
				return
			}
			cid, _ := strconv.ParseInt(parts[2], 10, 64)
			a.handleHTTPListRemoveMember(w, r, listID, cid)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPListGet(w, r, listID)
	case http.MethodPatch:
		a.handleHTTPListUpdate(w, r, listID)
	case http.MethodDelete:
		a.handleHTTPListArchive(w, r, listID)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPListsGet(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "1" || r.URL.Query().Get("include_archived") == "true"
	out, err := dbListsAll(globalCtx.AppDB(), pid, includeArchived)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"lists": out, "count": len(out)})
}

func (a *App) handleHTTPListsCreate(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	l := &List{
		Slug:                strArg(body, "slug"),
		Name:                strArg(body, "name"),
		Description:         strArg(body, "description"),
		DefaultSenderEmail:  strArg(body, "default_sender_email"),
		DefaultSenderPhone:  strArg(body, "default_sender_phone"),
		InboundRoutePattern: strArg(body, "inbound_route_pattern"),
	}
	if l.Name == "" && l.Slug == "" {
		httpErr(w, http.StatusBadRequest, "name or slug required")
		return
	}
	out, err := dbListCreate(globalCtx.AppDB(), pid, l)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("list.created", map[string]any{"id": out.ID, "slug": out.Slug, "name": out.Name})
	httpJSON(w, map[string]any{"list": out})
}

func (a *App) handleHTTPListGet(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	l, err := dbListGet(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if l == nil {
		httpErr(w, http.StatusNotFound, "list not found")
		return
	}
	httpJSON(w, map[string]any{"list": l})
}

func (a *App) handleHTTPListUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := dbListUpdate(globalCtx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	globalCtx.Emit("list.updated", map[string]any{"id": id})
	httpJSON(w, map[string]any{"list": out})
}

func (a *App) handleHTTPListArchive(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbListArchive(globalCtx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	globalCtx.Emit("list.archived", map[string]any{"id": id})
	httpJSON(w, map[string]any{"archived": true, "id": id})
}

func (a *App) handleHTTPListMembers(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := dbListMembers(globalCtx.AppDB(), pid, id, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"contacts": out, "count": len(out)})
}

func (a *App) handleHTTPListAddMember(w http.ResponseWriter, r *http.Request, listID int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	cid := int64Arg(body, "contact_id")
	if cid == 0 {
		httpErr(w, http.StatusBadRequest, "contact_id required")
		return
	}
	source := strArg(body, "source")
	if err := dbListAddContact(globalCtx.AppDB(), pid, listID, cid, source); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	globalCtx.Emit("list.member.added", map[string]any{"list_id": listID, "contact_id": cid})
	httpJSON(w, map[string]any{"added": true, "list_id": listID, "contact_id": cid})
}

// handleHTTPContactLists is mounted on /contacts/{id}/lists. GET
// returns the active lists the contact belongs to. POST adds the
// contact to a list (body: {list_id}). DELETE/PATCH not supported
// here — use /lists/{id}/members/{contact_id} for removal so the
// addressing stays symmetric.
func (a *App) handleHTTPContactLists(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/")
	parts := strings.SplitN(rest, "/", 2)
	cid, _ := strconv.ParseInt(parts[0], 10, 64)
	if cid == 0 {
		httpErr(w, http.StatusBadRequest, "contact id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbListsForContact(globalCtx.AppDB(), pid, cid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"lists": out, "count": len(out)})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		listID := int64Arg(body, "list_id")
		if listID == 0 {
			httpErr(w, http.StatusBadRequest, "list_id required")
			return
		}
		if err := dbListAddContact(globalCtx.AppDB(), pid, listID, cid, strArg(body, "source")); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		globalCtx.Emit("list.member.added", map[string]any{"list_id": listID, "contact_id": cid})
		httpJSON(w, map[string]any{"added": true, "list_id": listID, "contact_id": cid})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPListRemoveMember(w http.ResponseWriter, r *http.Request, listID, contactID int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbListRemoveContact(globalCtx.AppDB(), pid, listID, contactID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	globalCtx.Emit("list.member.removed", map[string]any{"list_id": listID, "contact_id": contactID})
	httpJSON(w, map[string]any{"removed": true, "list_id": listID, "contact_id": contactID})
}

// ─── Helpers ──────────────────────────────────────────────────────

// slugifyName turns "SaaS 1 Customers" → "saas_1_customers". Matches
// the slugify in CrmPanel's DefineFieldModal so behaviour is uniform
// across the panel + Go side.
func slugifyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prev := byte('_')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			prev = c
		} else if prev != '_' {
			b.WriteByte('_')
			prev = '_'
		}
	}
	out := b.String()
	out = strings.Trim(out, "_")
	return out
}

// resolveListSenderForChannel returns a list's default sender for the
// given channel, or "" if the list doesn't override. Centralised so
// callers (toolSendMessage) and the panel-side preview agree.
func (l *List) defaultSenderForChannel(channel string) string {
	if l == nil {
		return ""
	}
	switch channel {
	case channelEmail:
		return strings.TrimSpace(l.DefaultSenderEmail)
	case channelSMS, channelWhatsApp:
		return strings.TrimSpace(l.DefaultSenderPhone)
	}
	return ""
}

// time helper to format archive timestamps consistently when we set
// them in code paths other than the DB default.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
