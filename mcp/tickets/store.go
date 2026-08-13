package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func slugify(value string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-"), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}

func ensureProject(db *sql.DB, pid string) error {
	if db == nil {
		return errors.New("database unavailable")
	}
	if strings.TrimSpace(pid) == "" {
		return errors.New("project_id is required")
	}
	defaults := []struct {
		slug, name, color string
		order             int
	}{
		{"general", "General", "#6b7280", 10},
		{"backend", "Backend", "#8b5cf6", 20},
		{"frontend", "Frontend", "#3b82f6", 30},
		{"design", "Design", "#ec4899", 40},
		{"mobile", "Mobile", "#10b981", 50},
	}
	for _, a := range defaults {
		if _, err := db.Exec(`INSERT OR IGNORE INTO ticket_areas (project_id, slug, name, color, sort_order) VALUES (?, ?, ?, ?, ?)`, pid, a.slug, a.name, a.color, a.order); err != nil {
			return err
		}
	}
	var exists int
	err := db.QueryRow(`SELECT 1 FROM ticket_portals WHERE project_id=?`, pid).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		token, tokenErr := randomToken()
		if tokenErr != nil {
			return tokenErr
		}
		_, err = db.Exec(`INSERT OR IGNORE INTO ticket_portals (project_id, token) VALUES (?, ?)`, pid, token)
	}
	return err
}

func listAreas(db *sql.DB, pid string, includeArchived bool) ([]*Area, error) {
	q := `SELECT id, project_id, slug, name, color, sort_order, archived, created_at, updated_at FROM ticket_areas WHERE project_id=?`
	if !includeArchived {
		q += ` AND archived=0`
	}
	q += ` ORDER BY sort_order, name, id`
	rows, err := db.Query(q, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Area{}
	for rows.Next() {
		a := &Area{}
		var archived int
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Slug, &a.Name, &a.Color, &a.SortOrder, &archived, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.Archived = archived == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

func createArea(db *sql.DB, pid string, args map[string]any) (*Area, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return nil, errors.New("name is required")
	}
	slug := slugify(firstNonEmpty(stringArg(args, "slug"), name))
	if slug == "" {
		return nil, errors.New("valid slug is required")
	}
	color := firstNonEmpty(stringArg(args, "color"), "#6b7280")
	order := int(int64Arg(args, "sort_order"))
	res, err := db.Exec(`INSERT INTO ticket_areas (project_id, slug, name, color, sort_order) VALUES (?, ?, ?, ?, ?)`, pid, slug, name, color, order)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getArea(db, pid, id)
}

func getArea(db *sql.DB, pid string, id int64) (*Area, error) {
	a := &Area{}
	var archived int
	err := db.QueryRow(`SELECT id, project_id, slug, name, color, sort_order, archived, created_at, updated_at FROM ticket_areas WHERE project_id=? AND id=?`, pid, id).Scan(&a.ID, &a.ProjectID, &a.Slug, &a.Name, &a.Color, &a.SortOrder, &archived, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.Archived = archived == 1
	return a, nil
}

func updateArea(db *sql.DB, pid string, id int64, args map[string]any) (*Area, error) {
	if _, err := getArea(db, pid, id); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	push := func(column string, value any) { sets = append(sets, column+"=?"); vals = append(vals, value) }
	if _, ok := args["name"]; ok {
		name := stringArg(args, "name")
		if name == "" {
			return nil, errors.New("name cannot be empty")
		}
		push("name", name)
	}
	if _, ok := args["slug"]; ok {
		slug := slugify(stringArg(args, "slug"))
		if slug == "" {
			return nil, errors.New("slug cannot be empty")
		}
		push("slug", slug)
	}
	if _, ok := args["color"]; ok {
		push("color", stringArg(args, "color"))
	}
	if _, ok := args["sort_order"]; ok {
		push("sort_order", int64Arg(args, "sort_order"))
	}
	if _, ok := args["archived"]; ok {
		if boolArg(args, "archived") {
			push("archived", 1)
		} else {
			push("archived", 0)
		}
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=?")
		vals = append(vals, nowUTC(), pid, id)
		if _, err := db.Exec(`UPDATE ticket_areas SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, vals...); err != nil {
			return nil, err
		}
	}
	return getArea(db, pid, id)
}

func resolveAreaID(db *sql.DB, pid string, args map[string]any) (*int64, error) {
	if _, ok := args["area_id"]; ok {
		id := int64Arg(args, "area_id")
		if id == 0 {
			return nil, nil
		}
		if _, err := getArea(db, pid, id); err != nil {
			return nil, fmt.Errorf("area_id: %w", err)
		}
		return &id, nil
	}
	slug := slugify(stringArg(args, "area"))
	if slug == "" {
		slug = "general"
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM ticket_areas WHERE project_id=? AND slug=? AND archived=0`, pid, slug).Scan(&id); err != nil {
		return nil, fmt.Errorf("area %q: %w", slug, err)
	}
	return &id, nil
}

const ticketSelect = `SELECT t.id, t.project_id, t.area_id,
 COALESCE(a.slug,''), COALESCE(a.name,''), COALESCE(a.color,''),
 t.title, t.description, t.type, t.status, t.priority, t.source,
 t.requester_name, t.requester_email, t.requester_organization, t.requester_crm_contact_id,
 t.assignee_kind, t.assignee_ref, t.assignee_name, COALESCE(t.due_at,''), t.portal_token,
 t.created_by_kind, t.created_by_ref, t.created_by_name,
 COALESCE(t.resolved_at,''), COALESCE(t.closed_at,''), t.created_at, t.updated_at,
 (SELECT COUNT(*) FROM ticket_comments c WHERE c.ticket_id=t.id AND c.visibility='public'),
 (SELECT COUNT(*) FROM ticket_comments c WHERE c.ticket_id=t.id AND c.visibility='internal'),
 (SELECT COUNT(*) FROM ticket_attachments x WHERE x.ticket_id=t.id)
 FROM tickets t LEFT JOIN ticket_areas a ON a.id=t.area_id`

type scanner interface{ Scan(...any) error }

func scanTicket(row scanner) (*Ticket, error) {
	t := &Ticket{}
	var areaID, crmID sql.NullInt64
	err := row.Scan(&t.ID, &t.ProjectID, &areaID, &t.AreaSlug, &t.AreaName, &t.AreaColor,
		&t.Title, &t.Description, &t.Type, &t.Status, &t.Priority, &t.Source,
		&t.RequesterName, &t.RequesterEmail, &t.RequesterOrganization, &crmID,
		&t.AssigneeKind, &t.AssigneeRef, &t.AssigneeName, &t.DueAt, &t.PortalToken,
		&t.CreatedByKind, &t.CreatedByRef, &t.CreatedByName, &t.ResolvedAt, &t.ClosedAt, &t.CreatedAt, &t.UpdatedAt,
		&t.PublicCommentCount, &t.InternalNoteCount, &t.AttachmentCount)
	if err != nil {
		return nil, err
	}
	if areaID.Valid {
		t.AreaID = &areaID.Int64
	}
	if crmID.Valid {
		t.RequesterCRMContactID = &crmID.Int64
	}
	t.Key = "TKT-" + strconv.FormatInt(t.ID, 10)
	return t, nil
}

func getTicket(db *sql.DB, pid string, id int64) (*Ticket, error) {
	return scanTicket(db.QueryRow(ticketSelect+` WHERE t.project_id=? AND t.id=?`, pid, id))
}

func getTicketByToken(db *sql.DB, token string) (*Ticket, error) {
	return scanTicket(db.QueryRow(ticketSelect+` WHERE t.portal_token=?`, token))
}

func createTicket(db *sql.DB, pid string, args map[string]any, actor Actor) (*Ticket, error) {
	title := strings.TrimSpace(stringArg(args, "title"))
	if title == "" {
		return nil, errors.New("title is required")
	}
	if len([]rune(title)) > 240 {
		return nil, errors.New("title exceeds 240 characters")
	}
	if len(stringArg(args, "description")) > 100000 {
		return nil, errors.New("description exceeds 100000 characters")
	}
	typeName := firstNonEmpty(stringArg(args, "type"), "feedback")
	priority := firstNonEmpty(stringArg(args, "priority"), "normal")
	if !contains(typeValues, typeName) {
		return nil, fmt.Errorf("unsupported type %q", typeName)
	}
	if !contains(priorityValues, priority) {
		return nil, fmt.Errorf("unsupported priority %q", priority)
	}
	areaID, err := resolveAreaID(db, pid, args)
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	source := firstNonEmpty(stringArg(args, "source"), "internal")
	var crm any
	if id := int64Arg(args, "requester_crm_contact_id"); id > 0 {
		crm = id
	}
	res, err := db.Exec(`INSERT INTO tickets (
 project_id, area_id, title, description, type, priority, source,
 requester_name, requester_email, requester_organization, requester_crm_contact_id,
 due_at, portal_token, created_by_kind, created_by_ref, created_by_name)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?)`,
		pid, areaID, title, stringArg(args, "description"), typeName, priority, source,
		stringArg(args, "requester_name"), strings.ToLower(stringArg(args, "requester_email")), stringArg(args, "requester_organization"), crm,
		stringArg(args, "due_at"), token, actor.Kind, actor.Ref, actor.Name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ticket, err := getTicket(db, pid, id)
	if err != nil {
		return nil, err
	}
	if err := appendEvent(db, pid, id, "ticket.created", "public", actor, map[string]any{"title": ticket.Title, "type": ticket.Type, "priority": ticket.Priority, "area": ticket.AreaSlug, "source": ticket.Source}); err != nil {
		return nil, err
	}
	return ticket, nil
}

func listTickets(db *sql.DB, pid string, filter TicketFilter) ([]*Ticket, int, error) {
	where := []string{"t.project_id=?"}
	args := []any{pid}
	if filter.Status != "" {
		where = append(where, "t.status=?")
		args = append(args, filter.Status)
	}
	if filter.Type != "" {
		where = append(where, "t.type=?")
		args = append(args, filter.Type)
	}
	if filter.Priority != "" {
		where = append(where, "t.priority=?")
		args = append(args, filter.Priority)
	}
	if filter.Area != "" {
		where = append(where, "(a.slug=? OR CAST(t.area_id AS TEXT)=?)")
		args = append(args, slugify(filter.Area), filter.Area)
	}
	if filter.RequesterEmail != "" {
		where = append(where, "LOWER(t.requester_email)=LOWER(?)")
		args = append(args, filter.RequesterEmail)
	}
	if filter.Q != "" {
		like := "%" + escapeLike(filter.Q) + "%"
		where = append(where, "(t.title LIKE ? ESCAPE '\\' OR t.description LIKE ? ESCAPE '\\' OR t.requester_name LIKE ? ESCAPE '\\' OR t.requester_email LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like, like)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tickets t LEFT JOIN ticket_areas a ON a.id=t.area_id WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := db.Query(ticketSelect+` WHERE `+whereSQL+` ORDER BY CASE t.priority WHEN 'urgent' THEN 1 WHEN 'high' THEN 2 WHEN 'normal' THEN 3 ELSE 4 END, t.updated_at DESC, t.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Ticket{}
	for rows.Next() {
		t, scanErr := scanTicket(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func updateTicket(db *sql.DB, pid string, id int64, args map[string]any, actor Actor) (*Ticket, map[string]any, error) {
	before, err := getTicket(db, pid, id)
	if err != nil {
		return nil, nil, err
	}
	sets := []string{}
	vals := []any{}
	push := func(column string, value any) { sets = append(sets, column+"=?"); vals = append(vals, value) }
	stringFields := map[string]string{
		"title": "title", "description": "description", "requester_name": "requester_name", "requester_email": "requester_email",
		"requester_organization": "requester_organization", "assignee_kind": "assignee_kind", "assignee_ref": "assignee_ref", "assignee_name": "assignee_name",
	}
	for arg, column := range stringFields {
		if _, ok := args[arg]; ok {
			value := stringArg(args, arg)
			if arg == "title" && value == "" {
				return nil, nil, errors.New("title cannot be empty")
			}
			if arg == "title" && len([]rune(value)) > 240 {
				return nil, nil, errors.New("title exceeds 240 characters")
			}
			if arg == "description" && len(value) > 100000 {
				return nil, nil, errors.New("description exceeds 100000 characters")
			}
			if arg == "requester_email" {
				value = strings.ToLower(value)
			}
			push(column, value)
		}
	}
	if _, ok := args["type"]; ok {
		v := stringArg(args, "type")
		if !contains(typeValues, v) {
			return nil, nil, fmt.Errorf("unsupported type %q", v)
		}
		push("type", v)
	}
	if _, ok := args["priority"]; ok {
		v := stringArg(args, "priority")
		if !contains(priorityValues, v) {
			return nil, nil, fmt.Errorf("unsupported priority %q", v)
		}
		push("priority", v)
	}
	if _, ok := args["area"]; ok {
		areaID, e := resolveAreaID(db, pid, args)
		if e != nil {
			return nil, nil, e
		}
		push("area_id", areaID)
	} else if _, ok := args["area_id"]; ok {
		areaID, e := resolveAreaID(db, pid, args)
		if e != nil {
			return nil, nil, e
		}
		push("area_id", areaID)
	}
	if _, ok := args["requester_crm_contact_id"]; ok {
		idValue := int64Arg(args, "requester_crm_contact_id")
		if idValue > 0 {
			push("requester_crm_contact_id", idValue)
		} else {
			push("requester_crm_contact_id", nil)
		}
	}
	if _, ok := args["due_at"]; ok {
		if v := stringArg(args, "due_at"); v == "" {
			push("due_at", nil)
		} else {
			push("due_at", v)
		}
	}
	if len(sets) == 0 {
		return before, map[string]any{}, nil
	}
	sets = append(sets, "updated_at=?")
	vals = append(vals, nowUTC(), pid, id)
	if _, err := db.Exec(`UPDATE tickets SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, nil, err
	}
	after, err := getTicket(db, pid, id)
	if err != nil {
		return nil, nil, err
	}
	changes := ticketChanges(before, after)
	if len(changes) > 0 {
		publicChanges := map[string]any{}
		for key, value := range changes {
			if key != "requester_crm_contact_id" {
				publicChanges[key] = value
			}
		}
		if len(publicChanges) > 0 {
			if err := appendEvent(db, pid, id, "ticket.updated", "public", actor, map[string]any{"changes": publicChanges}); err != nil {
				return nil, nil, err
			}
		}
		if crmChange, ok := changes["requester_crm_contact_id"]; ok {
			if err := appendEvent(db, pid, id, "ticket.crm_contact.changed", "internal", actor, map[string]any{"change": crmChange}); err != nil {
				return nil, nil, err
			}
		}
	}
	return after, changes, nil
}

func setTicketStatus(db *sql.DB, pid string, id int64, status, reason string, actor Actor) (*Ticket, error) {
	if !contains(statusValues, status) {
		return nil, fmt.Errorf("unsupported status %q", status)
	}
	before, err := getTicket(db, pid, id)
	if err != nil {
		return nil, err
	}
	if before.Status == status {
		return before, nil
	}
	now := nowUTC()
	resolved, closed := any(nil), any(nil)
	if status == "resolved" {
		resolved = now
	}
	if status == "closed" {
		closed = now
		if before.ResolvedAt != "" {
			resolved = before.ResolvedAt
		}
	}
	if status != "resolved" && status != "closed" {
		resolved, closed = nil, nil
	}
	_, err = db.Exec(`UPDATE tickets SET status=?, resolved_at=?, closed_at=?, updated_at=? WHERE project_id=? AND id=?`, status, resolved, closed, now, pid, id)
	if err != nil {
		return nil, err
	}
	eventType := "ticket.status.changed"
	if status == "resolved" {
		eventType = "ticket.resolved"
	}
	if (before.Status == "resolved" || before.Status == "closed") && status != "resolved" && status != "closed" {
		eventType = "ticket.reopened"
	}
	if err := appendEvent(db, pid, id, eventType, "public", actor, map[string]any{"from": before.Status, "to": status, "reason": reason}); err != nil {
		return nil, err
	}
	return getTicket(db, pid, id)
}

func ticketChanges(before, after *Ticket) map[string]any {
	old := map[string]any{"title": before.Title, "description": before.Description, "area": before.AreaSlug, "type": before.Type, "priority": before.Priority, "requester_name": before.RequesterName, "requester_email": before.RequesterEmail, "requester_organization": before.RequesterOrganization, "requester_crm_contact_id": before.RequesterCRMContactID, "assignee_kind": before.AssigneeKind, "assignee_ref": before.AssigneeRef, "assignee_name": before.AssigneeName, "due_at": before.DueAt}
	newV := map[string]any{"title": after.Title, "description": after.Description, "area": after.AreaSlug, "type": after.Type, "priority": after.Priority, "requester_name": after.RequesterName, "requester_email": after.RequesterEmail, "requester_organization": after.RequesterOrganization, "requester_crm_contact_id": after.RequesterCRMContactID, "assignee_kind": after.AssigneeKind, "assignee_ref": after.AssigneeRef, "assignee_name": after.AssigneeName, "due_at": after.DueAt}
	out := map[string]any{}
	for key, from := range old {
		to := newV[key]
		if fmt.Sprint(from) != fmt.Sprint(to) {
			out[key] = map[string]any{"from": from, "to": to}
		}
	}
	return out
}

func addComment(db *sql.DB, pid string, ticketID int64, visibility, body string, actor Actor) (*Comment, error) {
	if _, err := getTicket(db, pid, ticketID); err != nil {
		return nil, err
	}
	if visibility != "public" && visibility != "internal" {
		return nil, errors.New("visibility must be public or internal")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("body is required")
	}
	if len(body) > 50000 {
		return nil, errors.New("comment exceeds 50000 characters")
	}
	res, err := db.Exec(`INSERT INTO ticket_comments (project_id,ticket_id,visibility,body,author_kind,author_ref,author_name) VALUES (?,?,?,?,?,?,?)`, pid, ticketID, visibility, body, actor.Kind, actor.Ref, actor.Name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	c, err := getComment(db, pid, ticketID, id)
	if err != nil {
		return nil, err
	}
	eventType := "ticket.commented"
	if visibility == "internal" {
		eventType = "ticket.internal_note.added"
	}
	if err := appendEvent(db, pid, ticketID, eventType, visibility, actor, map[string]any{"comment_id": id, "body": body}); err != nil {
		return nil, err
	}
	_, _ = db.Exec(`UPDATE tickets SET updated_at=? WHERE project_id=? AND id=?`, nowUTC(), pid, ticketID)
	return c, nil
}

func getComment(db *sql.DB, pid string, ticketID, commentID int64) (*Comment, error) {
	c := &Comment{}
	var edited sql.NullString
	err := db.QueryRow(`SELECT id,ticket_id,visibility,body,author_kind,author_ref,author_name,edited_at,created_at FROM ticket_comments WHERE project_id=? AND ticket_id=? AND id=?`, pid, ticketID, commentID).Scan(&c.ID, &c.TicketID, &c.Visibility, &c.Body, &c.AuthorKind, &c.AuthorRef, &c.AuthorName, &edited, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	if edited.Valid {
		c.EditedAt = edited.String
	}
	return c, nil
}

func editComment(db *sql.DB, pid string, ticketID, commentID int64, body string, actor Actor) (*Comment, error) {
	before, err := getComment(db, pid, ticketID, commentID)
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("body is required")
	}
	if len(body) > 50000 {
		return nil, errors.New("comment exceeds 50000 characters")
	}
	if body == before.Body {
		return before, nil
	}
	now := nowUTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO ticket_comment_revisions (project_id,ticket_id,comment_id,body,edited_by_kind,edited_by_ref,edited_by_name) VALUES (?,?,?,?,?,?,?)`, pid, ticketID, commentID, before.Body, actor.Kind, actor.Ref, actor.Name); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE ticket_comments SET body=?,edited_at=? WHERE project_id=? AND ticket_id=? AND id=?`, body, now, pid, ticketID, commentID); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(map[string]any{"comment_id": commentID, "before": before.Body, "after": body})
	if _, err = tx.Exec(`INSERT INTO ticket_events (project_id,ticket_id,event_type,visibility,actor_kind,actor_ref,actor_name,data_json) VALUES (?,?,?,?,?,?,?,?)`, pid, ticketID, "ticket.comment.edited", before.Visibility, actor.Kind, actor.Ref, actor.Name, string(data)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE tickets SET updated_at=? WHERE project_id=? AND id=?`, now, pid, ticketID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return getComment(db, pid, ticketID, commentID)
}

func appendEvent(db *sql.DB, pid string, ticketID int64, eventType, visibility string, actor Actor, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO ticket_events (project_id,ticket_id,event_type,visibility,actor_kind,actor_ref,actor_name,data_json) VALUES (?,?,?,?,?,?,?,?)`, pid, ticketID, eventType, visibility, actor.Kind, actor.Ref, actor.Name, string(raw))
	return err
}

func listComments(db *sql.DB, pid string, ticketID int64, includeInternal bool) ([]*Comment, error) {
	q := `SELECT id,ticket_id,visibility,body,author_kind,author_ref,author_name,edited_at,created_at FROM ticket_comments WHERE project_id=? AND ticket_id=?`
	if !includeInternal {
		q += ` AND visibility='public'`
	}
	q += ` ORDER BY created_at,id`
	rows, err := db.Query(q, pid, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Comment{}
	for rows.Next() {
		c := &Comment{}
		var edited sql.NullString
		if err := rows.Scan(&c.ID, &c.TicketID, &c.Visibility, &c.Body, &c.AuthorKind, &c.AuthorRef, &c.AuthorName, &edited, &c.CreatedAt); err != nil {
			return nil, err
		}
		if edited.Valid {
			c.EditedAt = edited.String
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func listEvents(db *sql.DB, pid string, ticketID int64, includeInternal bool) ([]*Event, error) {
	q := `SELECT id,ticket_id,event_type,visibility,actor_kind,actor_ref,actor_name,data_json,created_at FROM ticket_events WHERE project_id=? AND ticket_id=?`
	if !includeInternal {
		q += ` AND visibility='public'`
	}
	q += ` ORDER BY created_at,id`
	rows, err := db.Query(q, pid, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		e := &Event{}
		var raw string
		if err := rows.Scan(&e.ID, &e.TicketID, &e.EventType, &e.Visibility, &e.ActorKind, &e.ActorRef, &e.ActorName, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(raw)
		out = append(out, e)
	}
	return out, rows.Err()
}

func addAttachmentRecord(db *sql.DB, pid string, ticketID int64, commentID *int64, fileID, name, contentType string, size int64, url, visibility string, actor Actor) (*Attachment, error) {
	if _, err := getTicket(db, pid, ticketID); err != nil {
		return nil, err
	}
	if visibility == "" {
		visibility = "public"
	}
	if visibility != "public" && visibility != "internal" {
		return nil, errors.New("visibility must be public or internal")
	}
	res, err := db.Exec(`INSERT INTO ticket_attachments (project_id,ticket_id,comment_id,storage_file_id,name,content_type,size_bytes,url,visibility,uploaded_by_kind,uploaded_by_ref,uploaded_by_name) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, pid, ticketID, commentID, fileID, name, firstNonEmpty(contentType, "application/octet-stream"), size, url, visibility, actor.Kind, actor.Ref, actor.Name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	a, err := getAttachment(db, pid, ticketID, id)
	if err != nil {
		return nil, err
	}
	if err = appendEvent(db, pid, ticketID, "ticket.attachment.added", visibility, actor, map[string]any{"attachment_id": id, "name": name, "storage_file_id": fileID}); err != nil {
		return nil, err
	}
	_, _ = db.Exec(`UPDATE tickets SET updated_at=? WHERE project_id=? AND id=?`, nowUTC(), pid, ticketID)
	return a, nil
}

func getAttachment(db *sql.DB, pid string, ticketID, id int64) (*Attachment, error) {
	a := &Attachment{}
	var comment sql.NullInt64
	err := db.QueryRow(`SELECT id,ticket_id,comment_id,storage_file_id,name,content_type,size_bytes,url,visibility,uploaded_by_kind,uploaded_by_ref,uploaded_by_name,created_at FROM ticket_attachments WHERE project_id=? AND ticket_id=? AND id=?`, pid, ticketID, id).Scan(&a.ID, &a.TicketID, &comment, &a.StorageFileID, &a.Name, &a.ContentType, &a.SizeBytes, &a.URL, &a.Visibility, &a.UploadedByKind, &a.UploadedByRef, &a.UploadedByName, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	if comment.Valid {
		a.CommentID = &comment.Int64
	}
	return a, nil
}

func listAttachments(db *sql.DB, pid string, ticketID int64, includeInternal bool) ([]*Attachment, error) {
	q := `SELECT id,ticket_id,comment_id,storage_file_id,name,content_type,size_bytes,url,visibility,uploaded_by_kind,uploaded_by_ref,uploaded_by_name,created_at FROM ticket_attachments WHERE project_id=? AND ticket_id=?`
	if !includeInternal {
		q += ` AND visibility='public'`
	}
	q += ` ORDER BY created_at,id`
	rows, err := db.Query(q, pid, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Attachment{}
	for rows.Next() {
		a := &Attachment{}
		var comment sql.NullInt64
		if err := rows.Scan(&a.ID, &a.TicketID, &comment, &a.StorageFileID, &a.Name, &a.ContentType, &a.SizeBytes, &a.URL, &a.Visibility, &a.UploadedByKind, &a.UploadedByRef, &a.UploadedByName, &a.CreatedAt); err != nil {
			return nil, err
		}
		if comment.Valid {
			a.CommentID = &comment.Int64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func addLink(db *sql.DB, pid string, ticketID int64, args map[string]any, actor Actor) (*Link, error) {
	if _, err := getTicket(db, pid, ticketID); err != nil {
		return nil, err
	}
	kind := stringArg(args, "kind")
	if kind == "" {
		return nil, errors.New("kind is required")
	}
	meta := "{}"
	if v, ok := args["metadata"]; ok {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		meta = string(raw)
	}
	res, err := db.Exec(`INSERT INTO ticket_links (project_id,ticket_id,kind,label,app_name,external_id,url,metadata_json,created_by_kind,created_by_ref,created_by_name) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, pid, ticketID, kind, stringArg(args, "label"), stringArg(args, "app_name"), stringArg(args, "external_id"), stringArg(args, "url"), meta, actor.Kind, actor.Ref, actor.Name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	l, err := getLink(db, pid, ticketID, id)
	if err != nil {
		return nil, err
	}
	if err = appendEvent(db, pid, ticketID, "ticket.link.added", "internal", actor, map[string]any{"link_id": id, "kind": kind, "label": l.Label, "external_id": l.ExternalID}); err != nil {
		return nil, err
	}
	_, _ = db.Exec(`UPDATE tickets SET updated_at=? WHERE project_id=? AND id=?`, nowUTC(), pid, ticketID)
	return l, nil
}

func getLink(db *sql.DB, pid string, ticketID, id int64) (*Link, error) {
	l := &Link{}
	var raw string
	err := db.QueryRow(`SELECT id,ticket_id,kind,label,app_name,external_id,url,metadata_json,created_by_kind,created_by_ref,created_by_name,created_at FROM ticket_links WHERE project_id=? AND ticket_id=? AND id=?`, pid, ticketID, id).Scan(&l.ID, &l.TicketID, &l.Kind, &l.Label, &l.AppName, &l.ExternalID, &l.URL, &raw, &l.CreatedByKind, &l.CreatedByRef, &l.CreatedByName, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	l.Metadata = json.RawMessage(raw)
	return l, nil
}

func listLinks(db *sql.DB, pid string, ticketID int64) ([]*Link, error) {
	rows, err := db.Query(`SELECT id,ticket_id,kind,label,app_name,external_id,url,metadata_json,created_by_kind,created_by_ref,created_by_name,created_at FROM ticket_links WHERE project_id=? AND ticket_id=? ORDER BY created_at,id`, pid, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Link{}
	for rows.Next() {
		l := &Link{}
		var raw string
		if err := rows.Scan(&l.ID, &l.TicketID, &l.Kind, &l.Label, &l.AppName, &l.ExternalID, &l.URL, &raw, &l.CreatedByKind, &l.CreatedByRef, &l.CreatedByName, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.Metadata = json.RawMessage(raw)
		out = append(out, l)
	}
	return out, rows.Err()
}

func ticketDetail(db *sql.DB, pid string, id int64, includeInternal bool) (*TicketDetail, error) {
	t, err := getTicket(db, pid, id)
	if err != nil {
		return nil, err
	}
	comments, err := listComments(db, pid, id, includeInternal)
	if err != nil {
		return nil, err
	}
	events, err := listEvents(db, pid, id, includeInternal)
	if err != nil {
		return nil, err
	}
	attachments, err := listAttachments(db, pid, id, includeInternal)
	if err != nil {
		return nil, err
	}
	links := []*Link{}
	if includeInternal {
		links, err = listLinks(db, pid, id)
		if err != nil {
			return nil, err
		}
	}
	return &TicketDetail{Ticket: t, Comments: comments, Events: events, Attachments: attachments, Links: links}, nil
}

func getPortalByProject(db *sql.DB, pid string) (*Portal, error) {
	p := &Portal{}
	var enabled int
	err := db.QueryRow(`SELECT project_id,token,title,welcome_text,enabled,created_at,updated_at FROM ticket_portals WHERE project_id=?`, pid).Scan(&p.ProjectID, &p.Token, &p.Title, &p.Welcome, &enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled == 1
	return p, nil
}
func getPortalByToken(db *sql.DB, token string) (*Portal, error) {
	p := &Portal{}
	var enabled int
	err := db.QueryRow(`SELECT project_id,token,title,welcome_text,enabled,created_at,updated_at FROM ticket_portals WHERE token=?`, token).Scan(&p.ProjectID, &p.Token, &p.Title, &p.Welcome, &enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled == 1
	return p, nil
}
func updatePortal(db *sql.DB, pid string, args map[string]any) (*Portal, error) {
	sets := []string{}
	vals := []any{}
	push := func(k string, v any) { sets = append(sets, k+"=?"); vals = append(vals, v) }
	if _, ok := args["title"]; ok {
		v := stringArg(args, "title")
		if v == "" {
			return nil, errors.New("title cannot be empty")
		}
		push("title", v)
	}
	if _, ok := args["welcome_text"]; ok {
		push("welcome_text", stringArg(args, "welcome_text"))
	}
	if _, ok := args["enabled"]; ok {
		if boolArg(args, "enabled") {
			push("enabled", 1)
		} else {
			push("enabled", 0)
		}
	}
	if boolArg(args, "rotate_token") {
		token, err := randomToken()
		if err != nil {
			return nil, err
		}
		push("token", token)
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=?")
		vals = append(vals, nowUTC(), pid)
		if _, err := db.Exec(`UPDATE ticket_portals SET `+strings.Join(sets, ",")+` WHERE project_id=?`, vals...); err != nil {
			return nil, err
		}
	}
	return getPortalByProject(db, pid)
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}
