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

type instruction struct {
	ID                int64               `json:"id"`
	ProjectID         string              `json:"project_id"`
	Slug              string              `json:"slug"`
	Name              string              `json:"name"`
	Kind              string              `json:"kind"`
	CurrentVersionID  int64               `json:"current_version_id,omitempty"`
	ArchivedAt        string              `json:"archived_at,omitempty"`
	CreatedAt         string              `json:"created_at"`
	UpdatedAt         string              `json:"updated_at"`
	CurrentVersion    *instructionVersion `json:"current_version,omitempty"`
}

type instructionVersion struct {
	ID                    int64          `json:"id"`
	InstructionID         int64          `json:"instruction_id"`
	Version               int            `json:"version"`
	Status                string         `json:"status"`
	Body                  map[string]any `json:"body"`
	DeclaredVariables     []string       `json:"declared_variables"`
	DefaultResultKey      string         `json:"default_result_key,omitempty"`
	ResultField           map[string]any `json:"result_field,omitempty"`
	CreatedBy             string         `json:"created_by,omitempty"`
	CreatedAt             string         `json:"created_at"`
}

// ─── Tool registry ──────────────────────────────────────────────────

func (a *App) instructionTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "instructions_create",
			Description: "Create a new instruction (kind + body). Media kinds reference a storage_file_id (uploaded separately via storage.files_upload). The first version is created in draft status — publish it before use. Args: name, kind (text|audio|video|image|document|link|script|warning|example|checklist_item|confirmation|timer_hint|input_*), body (object, shape depends on kind), slug? (auto-derived from name), default_result_key?. Returns {instruction}.",
			InputSchema: schemaObject(map[string]any{
				"name":               map[string]any{"type": "string"},
				"kind":               map[string]any{"type": "string"},
				"body":               map[string]any{"type": "object"},
				"slug":               map[string]any{"type": "string"},
				"default_result_key": map[string]any{"type": "string"},
			}, []string{"name", "kind", "body"}),
			Handler: a.toolInstructionsCreate,
		},
		{
			Name:        "instructions_list",
			Description: "Filter the library. Args: kind? (filter by kind), q? (search slug/name), include_archived? (default false), limit? (default 200). Returns {instructions}.",
			InputSchema: schemaObject(map[string]any{
				"kind":             map[string]any{"type": "string"},
				"q":                map[string]any{"type": "string"},
				"include_archived": map[string]any{"type": "boolean"},
				"limit":            map[string]any{"type": "integer"},
			}, []string{}),
			Handler: a.toolInstructionsList,
		},
		{
			Name:        "instructions_get",
			Description: "Fetch one instruction + its current version (with derived result_field). Args: id OR slug. Returns {instruction}.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}, []string{}),
			Handler: a.toolInstructionsGet,
		},
		{
			Name:        "instructions_update",
			Description: "Fork a new version. The previous version stays queryable; templates that pinned it keep working. Args: id, body, default_result_key?. Returns {instruction, new_version}.",
			InputSchema: schemaObject(map[string]any{
				"id":                 map[string]any{"type": "integer"},
				"body":               map[string]any{"type": "object"},
				"default_result_key": map[string]any{"type": "string"},
			}, []string{"id", "body"}),
			Handler: a.toolInstructionsUpdate,
		},
		{
			Name:        "instructions_publish",
			Description: "Move the current draft version to active. The instruction becomes selectable from templates and ad-hoc gigs. Args: id. Returns {instruction}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInstructionsPublish,
		},
		{
			Name:        "instructions_archive",
			Description: "Soft-delete an instruction. Existing in-flight gigs and published templates keep their pinned versions. Args: id. Returns {ok}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInstructionsArchive,
		},
		{
			Name:        "instructions_used_in",
			Description: "Which templates reference this instruction? Impact view before publishing a new version. Args: id. Returns {templates: [{template_id, name, slug, pinned_version, latest_version}]}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInstructionsUsedIn,
		},
	}
}

// ─── Tools ──────────────────────────────────────────────────────────

func (a *App) toolInstructionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	kind := strArg(args, "kind")
	body := mapArg(args, "body")
	if name == "" || kind == "" || body == nil {
		return nil, errors.New("name, kind, body required")
	}
	if err := validateBody(kind, body); err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	if slug == "" {
		slug = slugify(name)
	}
	drk := strArg(args, "default_result_key")
	if drk == "" && (isInputKind(kind) || kind == kindChecklistItem || kind == kindConfirmation) {
		drk = slug
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO instructions (project_id, slug, name, kind) VALUES (?, ?, ?, ?)`,
		pid, slug, name, kind,
	)
	if err != nil {
		return nil, fmt.Errorf("insert instruction: %w", err)
	}
	id, _ := res.LastInsertId()

	declared := deriveDeclaredVariables(kind, body)
	resultField := deriveResultField(kind, body)
	vRes, err := tx.Exec(
		`INSERT INTO instruction_versions
		   (instruction_id, version, status, body_json,
		    declared_variables_json, default_result_key, result_field_json, created_by)
		 VALUES (?, 1, 'draft', ?, ?, ?, ?, ?)`,
		id, mustJSON(body), mustJSON(declared), nullStr(drk), mustJSON(resultField), nullStr(strArg(args, "_actor")),
	)
	if err != nil {
		return nil, fmt.Errorf("insert version: %w", err)
	}
	vid, _ := vRes.LastInsertId()
	if _, err := tx.Exec(
		`UPDATE instructions SET current_version_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		vid, id,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	ins, err := getInstruction(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	ctx.Emit("instruction.created", map[string]any{
		"instruction_id": id,
		"kind":           kind,
	})
	return map[string]any{"instruction": ins}, nil
}

func (a *App) toolInstructionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listInstructions(ctx.AppDB(), pid, listInstructionsFilter{
		Kind:            strArg(args, "kind"),
		Q:               strArg(args, "q"),
		IncludeArchived: boolArg(args, "include_archived", false),
		Limit:           intArg(args, "limit", 200),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"instructions": rows}, nil
}

func (a *App) toolInstructionsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		if slug := strArg(args, "slug"); slug != "" {
			if err := ctx.AppDB().QueryRow(
				`SELECT id FROM instructions WHERE project_id=? AND slug=?`, pid, slug,
			).Scan(&id); err != nil {
				return nil, fmt.Errorf("not found: %w", err)
			}
		}
	}
	if id == 0 {
		return nil, errors.New("id or slug required")
	}
	ins, err := getInstruction(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if ins == nil {
		return nil, errors.New("instruction not found")
	}
	return map[string]any{"instruction": ins}, nil
}

func (a *App) toolInstructionsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	body := mapArg(args, "body")
	if id == 0 || body == nil {
		return nil, errors.New("id and body required")
	}

	ins, err := getInstruction(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if ins == nil {
		return nil, errors.New("instruction not found")
	}
	if err := validateBody(ins.Kind, body); err != nil {
		return nil, err
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var maxVersion int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM instruction_versions WHERE instruction_id=?`, id,
	).Scan(&maxVersion); err != nil {
		return nil, err
	}
	newVer := maxVersion + 1
	declared := deriveDeclaredVariables(ins.Kind, body)
	resultField := deriveResultField(ins.Kind, body)
	drk := strArg(args, "default_result_key")
	if drk == "" {
		// Preserve the existing default_result_key.
		if ins.CurrentVersion != nil {
			drk = ins.CurrentVersion.DefaultResultKey
		}
	}
	vRes, err := tx.Exec(
		`INSERT INTO instruction_versions
		   (instruction_id, version, status, body_json,
		    declared_variables_json, default_result_key, result_field_json, created_by)
		 VALUES (?, ?, 'draft', ?, ?, ?, ?, ?)`,
		id, newVer, mustJSON(body), mustJSON(declared), nullStr(drk), mustJSON(resultField), nullStr(strArg(args, "_actor")),
	)
	if err != nil {
		return nil, err
	}
	vid, _ := vRes.LastInsertId()
	if _, err := tx.Exec(
		`UPDATE instructions SET current_version_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		vid, id,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	updated, _ := getInstruction(ctx.AppDB(), pid, id)
	ctx.Emit("instruction.updated", map[string]any{
		"instruction_id": id,
		"new_version":    newVer,
	})
	return map[string]any{
		"instruction":  updated,
		"new_version":  updated.CurrentVersion,
	}, nil
}

func (a *App) toolInstructionsPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ins, err := getInstruction(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if ins == nil {
		return nil, errors.New("instruction not found")
	}
	if ins.CurrentVersion == nil {
		return nil, errors.New("no current version to publish")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE instruction_versions SET status='active' WHERE id=?`,
		ins.CurrentVersion.ID,
	); err != nil {
		return nil, err
	}
	updated, _ := getInstruction(ctx.AppDB(), pid, id)
	return map[string]any{"instruction": updated}, nil
}

func (a *App) toolInstructionsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE instructions SET archived_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		pid, id,
	); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolInstructionsUsedIn(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	rows, err := ctx.AppDB().Query(
		`SELECT DISTINCT t.id, t.name, t.slug, ti.instruction_version_id, i.current_version_id
		 FROM template_instructions ti
		 JOIN template_versions tv ON tv.id = ti.template_version_id
		 JOIN templates t ON t.id = tv.template_id
		 JOIN instructions i ON i.id = ti.instruction_id
		 WHERE ti.instruction_id=? AND t.project_id=? AND t.archived_at IS NULL`,
		id, pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var tid int64
		var tname, tslug string
		var pinned, latest int64
		if err := rows.Scan(&tid, &tname, &tslug, &pinned, &latest); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"template_id":     tid,
			"name":            tname,
			"slug":            tslug,
			"pinned_version_id": pinned,
			"latest_version_id": latest,
			"stale":           pinned != latest,
		})
	}
	return map[string]any{"templates": out}, nil
}

// ─── DB helpers ─────────────────────────────────────────────────────

func getInstruction(db *sql.DB, pid string, id int64) (*instruction, error) {
	ins := &instruction{ProjectID: pid}
	var currentVID sql.NullInt64
	var archivedAt sql.NullString
	err := db.QueryRow(
		`SELECT id, slug, name, kind, current_version_id, archived_at, created_at, updated_at
		 FROM instructions WHERE project_id=? AND id=?`,
		pid, id,
	).Scan(&ins.ID, &ins.Slug, &ins.Name, &ins.Kind, &currentVID, &archivedAt, &ins.CreatedAt, &ins.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ins.CurrentVersionID = currentVID.Int64
	ins.ArchivedAt = archivedAt.String
	if currentVID.Valid {
		v, err := getInstructionVersion(db, currentVID.Int64)
		if err != nil {
			return nil, err
		}
		ins.CurrentVersion = v
	}
	return ins, nil
}

func getInstructionVersion(db *sql.DB, id int64) (*instructionVersion, error) {
	v := &instructionVersion{}
	var bodyJSON, declaredJSON, resultFieldJSON string
	var drk sql.NullString
	var createdBy sql.NullString
	err := db.QueryRow(
		`SELECT id, instruction_id, version, status, body_json,
		        declared_variables_json, default_result_key, result_field_json, created_by, created_at
		 FROM instruction_versions WHERE id=?`,
		id,
	).Scan(&v.ID, &v.InstructionID, &v.Version, &v.Status, &bodyJSON, &declaredJSON, &drk, &resultFieldJSON, &createdBy, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = parseJSON(bodyJSON, &v.Body)
	_ = parseJSON(declaredJSON, &v.DeclaredVariables)
	_ = parseJSON(resultFieldJSON, &v.ResultField)
	v.DefaultResultKey = drk.String
	v.CreatedBy = createdBy.String
	return v, nil
}

type listInstructionsFilter struct {
	Kind            string
	Q               string
	IncludeArchived bool
	Limit           int
}

func listInstructions(db *sql.DB, pid string, f listInstructionsFilter) ([]*instruction, error) {
	conds := []string{"project_id=?"}
	args := []any{pid}
	if !f.IncludeArchived {
		conds = append(conds, "archived_at IS NULL")
	}
	if f.Kind != "" {
		conds = append(conds, "kind=?")
		args = append(args, f.Kind)
	}
	if f.Q != "" {
		conds = append(conds, "(name LIKE ? OR slug LIKE ?)")
		like := "%" + f.Q + "%"
		args = append(args, like, like)
	}
	if f.Limit <= 0 {
		f.Limit = 200
	}
	q := `SELECT id, slug, name, kind, current_version_id, archived_at, created_at, updated_at
	      FROM instructions WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*instruction{}
	for rows.Next() {
		ins := &instruction{ProjectID: pid}
		var currentVID sql.NullInt64
		var archivedAt sql.NullString
		if err := rows.Scan(
			&ins.ID, &ins.Slug, &ins.Name, &ins.Kind, &currentVID, &archivedAt,
			&ins.CreatedAt, &ins.UpdatedAt,
		); err != nil {
			return nil, err
		}
		ins.CurrentVersionID = currentVID.Int64
		ins.ArchivedAt = archivedAt.String
		out = append(out, ins)
	}
	// Hydrate current versions in a second pass to keep the loop tight.
	for _, ins := range out {
		if ins.CurrentVersionID == 0 {
			continue
		}
		if v, err := getInstructionVersion(db, ins.CurrentVersionID); err == nil {
			ins.CurrentVersion = v
		}
	}
	return out, nil
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPInstructionsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		filter := listInstructionsFilter{
			Kind:            r.URL.Query().Get("kind"),
			Q:               r.URL.Query().Get("q"),
			IncludeArchived: r.URL.Query().Get("include_archived") == "true",
			Limit:           parseQueryIntDefault(r, "limit", 200),
		}
		rows, err := listInstructions(ctx.AppDB(), pid, filter)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"instructions": rows})
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
		out, err := a.toolInstructionsCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPInstructionItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/instructions/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "publish":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
				return
			}
			out, err := a.toolInstructionsPublish(ctx, map[string]any{"_project_id": pid, "id": id})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "used-in":
			out, err := a.toolInstructionsUsedIn(ctx, map[string]any{"_project_id": pid, "id": id})
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolInstructionsGet(ctx, map[string]any{"_project_id": pid, "id": id})
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPatch:
		var body map[string]any
		if err := httpDecode(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		body["_project_id"] = pid
		body["id"] = id
		out, err := a.toolInstructionsUpdate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodDelete:
		out, err := a.toolInstructionsArchive(ctx, map[string]any{"_project_id": pid, "id": id})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
