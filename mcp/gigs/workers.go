package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Types ──────────────────────────────────────────────────────────

type worker struct {
	ID             int64       `json:"id"`
	ProjectID      string      `json:"project_id"`
	ContactID      int64       `json:"contact_id"`
	Status         string      `json:"status"`
	DefaultChannel string      `json:"default_channel,omitempty"`
	Notes          string      `json:"notes,omitempty"`
	RatingAvg      float64     `json:"rating_avg"`
	AcceptedCount  int64       `json:"accepted_count"`
	RejectedCount  int64       `json:"rejected_count"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
	ArchivedAt     string      `json:"archived_at,omitempty"`
	// Hydrated from CRM at read time.
	Contact *crmContact `json:"contact,omitempty"`
	// Hydrated from worker_skills.
	Skills []workerSkillView `json:"skills,omitempty"`
	// Hydrated from gig_assignments.
	OpenAssignments int64 `json:"open_assignments,omitempty"`
}

type workerSkillView struct {
	SkillID int64  `json:"skill_id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Level   int    `json:"level"`
}

type skill struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// ─── Tool registry ──────────────────────────────────────────────────

func (a *App) workerTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "workers_create",
			Description: "Create a worker by name + email/phone. Upserts the CRM contact (find-or-create by channel), then promotes it to a worker. Args: name (display name), email and/or phone (at least one required), company?, default_channel? (email|sms|whatsapp), notes?, skill_ids? ([int]), source? (default \"gigs\"). Returns {worker, contact, was_created, was_promoted}.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"email":           map[string]any{"type": "string"},
				"phone":           map[string]any{"type": "string"},
				"company":         map[string]any{"type": "string"},
				"default_channel": map[string]any{"type": "string"},
				"notes":           map[string]any{"type": "string"},
				"skill_ids":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"source":          map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolWorkersCreate,
		},
		{
			Name:        "workers_promote_contact",
			Description: "Promote an existing CRM contact to a worker. Idempotent — returns the existing worker row if already promoted. Args: contact_id, default_channel?, notes?, skill_ids?. Returns {worker, was_promoted}.",
			InputSchema: schemaObject(map[string]any{
				"contact_id":      map[string]any{"type": "integer"},
				"default_channel": map[string]any{"type": "string"},
				"notes":           map[string]any{"type": "string"},
				"skill_ids":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, []string{"contact_id"}),
			Handler: a.toolWorkersPromote,
		},
		{
			Name:        "workers_list",
			Description: "List workers in the project. Args: status? (active|paused|retired), skill_id?, include_contact? (default true), limit? (default 100). Returns {workers}.",
			InputSchema: schemaObject(map[string]any{
				"status":           map[string]any{"type": "string"},
				"skill_id":         map[string]any{"type": "integer"},
				"include_contact":  map[string]any{"type": "boolean"},
				"limit":            map[string]any{"type": "integer"},
			}, []string{}),
			Handler: a.toolWorkersList,
		},
		{
			Name:        "workers_get",
			Description: "Fetch one worker with the resolved CRM contact + skills + open assignment count. Args: id. Returns {worker}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolWorkersGet,
		},
		{
			Name:        "workers_search",
			Description: "Filtered worker search. Args: skill_id?, min_level? (default 1), only_available? (default true), order_by? (rating|recent|accepted; default rating), limit? (default 25). Returns {workers}.",
			InputSchema: schemaObject(map[string]any{
				"skill_id":       map[string]any{"type": "integer"},
				"min_level":      map[string]any{"type": "integer"},
				"only_available": map[string]any{"type": "boolean"},
				"order_by":       map[string]any{"type": "string"},
				"limit":          map[string]any{"type": "integer"},
			}, []string{}),
			Handler: a.toolWorkersSearch,
		},
		{
			Name:        "workers_set_availability",
			Description: "Pause or resume a worker. Args: id, status (active|paused|retired). Returns {worker}.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"status": map[string]any{"type": "string"},
			}, []string{"id", "status"}),
			Handler: a.toolWorkersSetAvailability,
		},
		{
			Name:        "workers_set_skills",
			Description: "Replace a worker's skill set. Args: id, skills ([{skill_id, level}]). Returns {worker}.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"skills": map[string]any{"type": "array"},
			}, []string{"id", "skills"}),
			Handler: a.toolWorkersSetSkills,
		},
		{
			Name:        "skills_create",
			Description: "Define a new skill. Args: name, slug? (auto-derived from name when omitted). Returns {skill}.",
			InputSchema: schemaObject(map[string]any{
				"name": map[string]any{"type": "string"},
				"slug": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolSkillsCreate,
		},
		{
			Name:        "skills_list",
			Description: "List all skills in the project. Returns {skills}.",
			InputSchema: schemaObject(map[string]any{}, []string{}),
			Handler:     a.toolSkillsList,
		},
	}
}

// ─── Tools ──────────────────────────────────────────────────────────

// toolWorkersCreate is the one-shot path: name + contact channel(s)
// → CRM contact (find-or-create) → worker row. The whole point of
// this tool is that an operator (or agent) can onboard a new worker
// without first navigating to CRM.
func (a *App) toolWorkersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	email := strings.TrimSpace(strArg(args, "email"))
	phone := strings.TrimSpace(strArg(args, "phone"))
	if name == "" {
		return nil, errors.New("name required")
	}
	if email == "" && phone == "" {
		return nil, errors.New("at least one of email or phone required")
	}

	source := strArg(args, "source")
	if source == "" {
		source = "gigs"
	}

	// Build the CRM defaults — applied only on create. We pick one
	// channel to seed contacts_upsert_by_channel; the second channel
	// (if both supplied) gets added via the defaults block in CRM's
	// upsert handler when present.
	first, last := splitName(name)
	defaults := map[string]any{
		"first_name":   first,
		"last_name":    last,
		"display_name": name,
	}
	if company := strArg(args, "company"); company != "" {
		defaults["company"] = company
	}
	// CRM's contacts_upsert_by_channel keys on one channel; the
	// second channel needs a follow-on write. Keep this simple in
	// v0.1: pick whichever is supplied first, the operator can attach
	// the other from the CRM panel.
	kind, value := "email", email
	if email == "" {
		kind, value = "phone", phone
	}

	contact, wasCreated, err := crmUpsertByChannel(ctx, pid, kind, value, defaults, source)
	if err != nil {
		return nil, err
	}

	w, wasPromoted, err := promoteContact(ctx.AppDB(), pid, contact.ID, strArg(args, "default_channel"), strArg(args, "notes"))
	if err != nil {
		return nil, err
	}

	if skillIDs := sliceArg(args, "skill_ids"); len(skillIDs) > 0 {
		skills := make([]map[string]any, 0, len(skillIDs))
		for _, sid := range skillIDs {
			skills = append(skills, map[string]any{
				"skill_id": int64Cast(sid),
				"level":    3,
			})
		}
		if err := replaceWorkerSkills(ctx.AppDB(), pid, w.ID, skills); err != nil {
			return nil, err
		}
	}

	w.Contact = contact
	if err := hydrateSkills(ctx.AppDB(), pid, w); err != nil {
		return nil, err
	}
	ctx.Emit("worker.created", map[string]any{
		"worker_id":    w.ID,
		"contact_id":   w.ContactID,
		"was_promoted": wasPromoted,
	})
	return map[string]any{
		"worker":        w,
		"contact":       contact,
		"was_created":   wasCreated,
		"was_promoted":  wasPromoted,
	}, nil
}

func (a *App) toolWorkersPromote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "contact_id")
	if cid == 0 {
		return nil, errors.New("contact_id required")
	}
	contact, err := crmGetContact(ctx, pid, cid)
	if err != nil {
		return nil, err
	}
	if contact == nil {
		return nil, fmt.Errorf("contact %d not found in CRM", cid)
	}
	w, wasPromoted, err := promoteContact(ctx.AppDB(), pid, cid, strArg(args, "default_channel"), strArg(args, "notes"))
	if err != nil {
		return nil, err
	}
	if skillIDs := sliceArg(args, "skill_ids"); len(skillIDs) > 0 {
		skills := make([]map[string]any, 0, len(skillIDs))
		for _, sid := range skillIDs {
			skills = append(skills, map[string]any{
				"skill_id": int64Cast(sid),
				"level":    3,
			})
		}
		if err := replaceWorkerSkills(ctx.AppDB(), pid, w.ID, skills); err != nil {
			return nil, err
		}
	}
	w.Contact = contact
	if err := hydrateSkills(ctx.AppDB(), pid, w); err != nil {
		return nil, err
	}
	return map[string]any{"worker": w, "was_promoted": wasPromoted}, nil
}

func (a *App) toolWorkersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listWorkers(ctx.AppDB(), pid, listWorkersFilter{
		Status:  strArg(args, "status"),
		SkillID: int64Arg(args, "skill_id"),
		Limit:   intArg(args, "limit", 100),
	})
	if err != nil {
		return nil, err
	}
	includeContact := boolArg(args, "include_contact", true)
	for _, w := range rows {
		_ = hydrateSkills(ctx.AppDB(), pid, w)
		if includeContact {
			if c, err := crmGetContact(ctx, pid, w.ContactID); err == nil {
				w.Contact = c
			}
		}
	}
	return map[string]any{"workers": rows}, nil
}

func (a *App) toolWorkersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := getWorker(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("worker %d not found", id)
	}
	if c, err := crmGetContact(ctx, pid, w.ContactID); err == nil {
		w.Contact = c
	}
	if err := hydrateSkills(ctx.AppDB(), pid, w); err != nil {
		return nil, err
	}
	w.OpenAssignments, _ = countOpenAssignments(ctx.AppDB(), w.ID)
	return map[string]any{"worker": w}, nil
}

func (a *App) toolWorkersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := searchWorkers(ctx.AppDB(), pid, searchWorkersOpts{
		SkillID:       int64Arg(args, "skill_id"),
		MinLevel:      intArg(args, "min_level", 1),
		OnlyAvailable: boolArg(args, "only_available", true),
		OrderBy:       strArg(args, "order_by"),
		Limit:         intArg(args, "limit", 25),
	})
	if err != nil {
		return nil, err
	}
	for _, w := range rows {
		_ = hydrateSkills(ctx.AppDB(), pid, w)
		if c, err := crmGetContact(ctx, pid, w.ContactID); err == nil {
			w.Contact = c
		}
		w.OpenAssignments, _ = countOpenAssignments(ctx.AppDB(), w.ID)
	}
	return map[string]any{"workers": rows}, nil
}

func (a *App) toolWorkersSetAvailability(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	status := strArg(args, "status")
	if id == 0 || status == "" {
		return nil, errors.New("id and status required")
	}
	if status != "active" && status != "paused" && status != "retired" {
		return nil, errors.New("status must be active|paused|retired")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE workers SET status=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		status, pid, id,
	); err != nil {
		return nil, err
	}
	w, err := getWorker(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"worker": w}, nil
}

func (a *App) toolWorkersSetSkills(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	raw := sliceArg(args, "skills")
	skills := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		if m, ok := s.(map[string]any); ok {
			skills = append(skills, m)
		}
	}
	if err := replaceWorkerSkills(ctx.AppDB(), pid, id, skills); err != nil {
		return nil, err
	}
	w, err := getWorker(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if err := hydrateSkills(ctx.AppDB(), pid, w); err != nil {
		return nil, err
	}
	return map[string]any{"worker": w}, nil
}

func (a *App) toolSkillsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	slug := strArg(args, "slug")
	if slug == "" {
		slug = slugify(name)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO skills (project_id, slug, name) VALUES (?, ?, ?)
		 ON CONFLICT(project_id, slug) DO UPDATE SET name=excluded.name`,
		pid, slug, name,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		// ON CONFLICT path — re-read the row.
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM skills WHERE project_id=? AND slug=?`,
			pid, slug,
		).Scan(&id)
	}
	s := &skill{ID: id, ProjectID: pid, Slug: slug, Name: name}
	return map[string]any{"skill": s}, nil
}

func (a *App) toolSkillsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, slug, name, created_at FROM skills WHERE project_id=? ORDER BY name`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*skill{}
	for rows.Next() {
		s := &skill{ProjectID: pid}
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return map[string]any{"skills": out}, nil
}

// ─── DB helpers ─────────────────────────────────────────────────────

// promoteContact inserts a worker row for `contactID` if one does not
// already exist; returns (worker, true) on insert, (existing, false)
// when the contact was already promoted. Idempotent by construction.
func promoteContact(db *sql.DB, pid string, contactID int64, defaultChannel, notes string) (*worker, bool, error) {
	existing, err := getWorkerByContact(db, pid, contactID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		// Apply non-destructive updates (only set when the caller
		// supplied a non-empty value).
		if defaultChannel != "" || notes != "" {
			set := []string{}
			args := []any{}
			if defaultChannel != "" {
				set = append(set, "default_channel=?")
				args = append(args, defaultChannel)
			}
			if notes != "" {
				set = append(set, "notes=?")
				args = append(args, notes)
			}
			args = append(args, pid, existing.ID)
			if _, err := db.Exec(
				`UPDATE workers SET `+strings.Join(set, ", ")+`, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
				args...,
			); err != nil {
				return nil, false, err
			}
			existing, _ = getWorker(db, pid, existing.ID)
		}
		return existing, false, nil
	}
	res, err := db.Exec(
		`INSERT INTO workers (project_id, contact_id, default_channel, notes) VALUES (?, ?, ?, ?)`,
		pid, contactID, nullStr(defaultChannel), nullStr(notes),
	)
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	w, err := getWorker(db, pid, id)
	return w, true, err
}

func getWorker(db *sql.DB, pid string, id int64) (*worker, error) {
	w := &worker{ProjectID: pid}
	var defaultChannel, notes, archivedAt sql.NullString
	err := db.QueryRow(
		`SELECT id, contact_id, status, default_channel, notes, rating_avg,
		        accepted_count, rejected_count, created_at, updated_at, archived_at
		 FROM workers WHERE project_id=? AND id=?`,
		pid, id,
	).Scan(
		&w.ID, &w.ContactID, &w.Status, &defaultChannel, &notes,
		&w.RatingAvg, &w.AcceptedCount, &w.RejectedCount,
		&w.CreatedAt, &w.UpdatedAt, &archivedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.DefaultChannel = defaultChannel.String
	w.Notes = notes.String
	w.ArchivedAt = archivedAt.String
	return w, nil
}

func getWorkerByContact(db *sql.DB, pid string, contactID int64) (*worker, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM workers WHERE project_id=? AND contact_id=?`,
		pid, contactID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getWorker(db, pid, id)
}

type listWorkersFilter struct {
	Status  string
	SkillID int64
	Limit   int
}

func listWorkers(db *sql.DB, pid string, f listWorkersFilter) ([]*worker, error) {
	q := `SELECT w.id, w.contact_id, w.status, w.default_channel, w.notes, w.rating_avg,
	             w.accepted_count, w.rejected_count, w.created_at, w.updated_at, w.archived_at
	      FROM workers w`
	conds := []string{"w.project_id=?", "w.archived_at IS NULL"}
	args := []any{pid}
	if f.SkillID > 0 {
		q += ` JOIN worker_skills ws ON ws.worker_id=w.id`
		conds = append(conds, "ws.skill_id=?")
		args = append(args, f.SkillID)
	}
	if f.Status != "" {
		conds = append(conds, "w.status=?")
		args = append(args, f.Status)
	}
	q += " WHERE " + strings.Join(conds, " AND ")
	q += " ORDER BY w.updated_at DESC"
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q += " LIMIT ?"
	args = append(args, f.Limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkerRows(rows, pid)
}

type searchWorkersOpts struct {
	SkillID       int64
	MinLevel      int
	OnlyAvailable bool
	OrderBy       string
	Limit         int
}

func searchWorkers(db *sql.DB, pid string, o searchWorkersOpts) ([]*worker, error) {
	q := `SELECT w.id, w.contact_id, w.status, w.default_channel, w.notes, w.rating_avg,
	             w.accepted_count, w.rejected_count, w.created_at, w.updated_at, w.archived_at
	      FROM workers w`
	conds := []string{"w.project_id=?", "w.archived_at IS NULL"}
	args := []any{pid}
	if o.SkillID > 0 {
		q += ` JOIN worker_skills ws ON ws.worker_id=w.id AND ws.skill_id=? AND ws.level >= ?`
		args = append(args, o.SkillID, o.MinLevel)
	}
	if o.OnlyAvailable {
		conds = append(conds, "w.status='active'")
	}
	q += " WHERE " + strings.Join(conds, " AND ")
	switch o.OrderBy {
	case "recent":
		q += " ORDER BY w.updated_at DESC"
	case "accepted":
		q += " ORDER BY w.accepted_count DESC, w.rating_avg DESC"
	default:
		q += " ORDER BY w.rating_avg DESC, w.accepted_count DESC"
	}
	if o.Limit <= 0 {
		o.Limit = 25
	}
	q += " LIMIT ?"
	args = append(args, o.Limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkerRows(rows, pid)
}

func scanWorkerRows(rows *sql.Rows, pid string) ([]*worker, error) {
	out := []*worker{}
	for rows.Next() {
		w := &worker{ProjectID: pid}
		var defaultChannel, notes, archivedAt sql.NullString
		if err := rows.Scan(
			&w.ID, &w.ContactID, &w.Status, &defaultChannel, &notes,
			&w.RatingAvg, &w.AcceptedCount, &w.RejectedCount,
			&w.CreatedAt, &w.UpdatedAt, &archivedAt,
		); err != nil {
			return nil, err
		}
		w.DefaultChannel = defaultChannel.String
		w.Notes = notes.String
		w.ArchivedAt = archivedAt.String
		out = append(out, w)
	}
	return out, rows.Err()
}

func hydrateSkills(db *sql.DB, pid string, w *worker) error {
	rows, err := db.Query(
		`SELECT s.id, s.slug, s.name, ws.level
		 FROM worker_skills ws
		 JOIN skills s ON s.id = ws.skill_id
		 WHERE ws.worker_id=? AND s.project_id=?
		 ORDER BY s.name`,
		w.ID, pid,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	w.Skills = w.Skills[:0]
	for rows.Next() {
		v := workerSkillView{}
		if err := rows.Scan(&v.SkillID, &v.Slug, &v.Name, &v.Level); err != nil {
			return err
		}
		w.Skills = append(w.Skills, v)
	}
	return rows.Err()
}

func replaceWorkerSkills(db *sql.DB, pid string, workerID int64, skills []map[string]any) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM worker_skills WHERE worker_id=?`, workerID); err != nil {
		return err
	}
	for _, s := range skills {
		sid := int64Cast(s["skill_id"])
		if sid == 0 {
			continue
		}
		lvl := intCast(s["level"])
		if lvl < 1 || lvl > 5 {
			lvl = 3
		}
		// Bind on project_id so a global install never accidentally
		// attaches a skill from another project.
		var exists int
		if err := tx.QueryRow(
			`SELECT 1 FROM skills WHERE id=? AND project_id=?`, sid, pid,
		).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("skill %d not in project %s", sid, pid)
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO worker_skills (worker_id, skill_id, level) VALUES (?, ?, ?)`,
			workerID, sid, lvl,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func countOpenAssignments(db *sql.DB, workerID int64) (int64, error) {
	var n int64
	err := db.QueryRow(
		`SELECT COUNT(*) FROM gig_assignments WHERE worker_id=? AND status IN ('offered','accepted')`,
		workerID,
	).Scan(&n)
	return n, err
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPWorkersCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		filter := listWorkersFilter{
			Status:  r.URL.Query().Get("status"),
			SkillID: parseQueryInt(r, "skill_id"),
			Limit:   parseQueryIntDefault(r, "limit", 100),
		}
		rows, err := listWorkers(ctx.AppDB(), pid, filter)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, wk := range rows {
			_ = hydrateSkills(ctx.AppDB(), pid, wk)
			if c, err := crmGetContact(ctx, pid, wk.ContactID); err == nil {
				wk.Contact = c
			}
		}
		httpJSON(w, map[string]any{"workers": rows})
	case http.MethodPost:
		var body map[string]any
		if err := httpDecode(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		body["_project_id"] = pid
		out, err := a.toolWorkersCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPWorkerItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/workers/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 && parts[1] == "skills" {
		switch r.Method {
		case http.MethodGet:
			wk, err := getWorker(ctx.AppDB(), pid, id)
			if err != nil || wk == nil {
				httpErr(w, http.StatusNotFound, "worker not found")
				return
			}
			_ = hydrateSkills(ctx.AppDB(), pid, wk)
			httpJSON(w, map[string]any{"skills": wk.Skills})
		case http.MethodPut:
			var body struct {
				Skills []map[string]any `json:"skills"`
			}
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			if err := replaceWorkerSkills(ctx.AppDB(), pid, id, body.Skills); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			wk, _ := getWorker(ctx.AppDB(), pid, id)
			_ = hydrateSkills(ctx.AppDB(), pid, wk)
			httpJSON(w, map[string]any{"worker": wk})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		wk, err := getWorker(ctx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if wk == nil {
			httpErr(w, http.StatusNotFound, "worker not found")
			return
		}
		if c, err := crmGetContact(ctx, pid, wk.ContactID); err == nil {
			wk.Contact = c
		}
		_ = hydrateSkills(ctx.AppDB(), pid, wk)
		wk.OpenAssignments, _ = countOpenAssignments(ctx.AppDB(), wk.ID)
		httpJSON(w, map[string]any{"worker": wk})
	case http.MethodPatch:
		var body map[string]any
		if err := httpDecode(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		set := []string{}
		args := []any{}
		if v, ok := body["status"].(string); ok && v != "" {
			set = append(set, "status=?")
			args = append(args, v)
		}
		if v, ok := body["default_channel"].(string); ok {
			set = append(set, "default_channel=?")
			args = append(args, nullStr(v))
		}
		if v, ok := body["notes"].(string); ok {
			set = append(set, "notes=?")
			args = append(args, nullStr(v))
		}
		if len(set) == 0 {
			httpErr(w, http.StatusBadRequest, "no editable fields supplied")
			return
		}
		args = append(args, pid, id)
		if _, err := ctx.AppDB().Exec(
			`UPDATE workers SET `+strings.Join(set, ", ")+`, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
			args...,
		); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		wk, _ := getWorker(ctx.AppDB(), pid, id)
		httpJSON(w, map[string]any{"worker": wk})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPSkills(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolSkillsList(ctx, map[string]any{"_project_id": pid})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := httpDecode(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		body["_project_id"] = pid
		out, err := a.toolSkillsCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── Small utilities only used in this file ─────────────────────────

func splitName(full string) (first, last string) {
	parts := strings.Fields(full)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " ")
}

func int64Cast(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func intCast(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func parseQueryInt(r *http.Request, key string) int64 {
	if s := r.URL.Query().Get(key); s != "" {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	return 0
}

func parseQueryIntDefault(r *http.Request, key string, def int) int {
	if s := r.URL.Query().Get(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}
