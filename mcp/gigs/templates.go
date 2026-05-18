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

type template struct {
	ID                int64            `json:"id"`
	ProjectID         string           `json:"project_id"`
	Slug              string           `json:"slug"`
	Name              string           `json:"name"`
	Kind              string           `json:"kind"`
	CurrentVersionID  int64            `json:"current_version_id,omitempty"`
	ArchivedAt        string           `json:"archived_at,omitempty"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	CurrentVersion    *templateVersion `json:"current_version,omitempty"`
}

type templateVersion struct {
	ID                     int64                `json:"id"`
	TemplateID             int64                `json:"template_id"`
	Version                int                  `json:"version"`
	Status                 string               `json:"status"`
	TitleTemplate          string               `json:"title_template"`
	DefaultDeadlineHours   int                  `json:"default_deadline_hours,omitempty"`
	DefaultSkillIDs        []int64              `json:"default_skill_ids,omitempty"`
	DefaultPriority        string               `json:"default_priority,omitempty"`
	VariableOverrides      map[string]any       `json:"variable_overrides,omitempty"`
	CreatedBy              string               `json:"created_by,omitempty"`
	CreatedAt              string               `json:"created_at"`
	Composition            []compositionItem    `json:"composition,omitempty"`
	Derived                *derivedComposition  `json:"derived,omitempty"`
}

// ─── Tool registry ──────────────────────────────────────────────────

func (a *App) templateTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "templates_create",
			Description: "Create a new template (empty composition, draft). Args: name, kind? (decision|action|creative|expert|micro|physical; default action), slug? (auto-derived), title_template (with {{var}} interpolation), default_deadline_hours?, default_priority?. Returns {template}.",
			InputSchema: schemaObject(map[string]any{
				"name":                   map[string]any{"type": "string"},
				"kind":                   map[string]any{"type": "string"},
				"slug":                   map[string]any{"type": "string"},
				"title_template":         map[string]any{"type": "string"},
				"default_deadline_hours": map[string]any{"type": "integer"},
				"default_priority":       map[string]any{"type": "string"},
			}, []string{"name", "title_template"}),
			Handler: a.toolTemplatesCreate,
		},
		{
			Name:        "templates_list",
			Description: "List templates in the project. Args: include_archived? (default false), kind?, limit? (default 100). Returns {templates}.",
			InputSchema: schemaObject(map[string]any{
				"include_archived": map[string]any{"type": "boolean"},
				"kind":             map[string]any{"type": "string"},
				"limit":            map[string]any{"type": "integer"},
			}, []string{}),
			Handler: a.toolTemplatesList,
		},
		{
			Name:        "templates_get",
			Description: "Fetch template + current version + composition + derived schema/manifest/variables. Args: id OR slug. Returns {template}.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}, []string{}),
			Handler: a.toolTemplatesGet,
		},
		{
			Name:        "templates_set_instructions",
			Description: "Replace the composition on the current draft version (auto-forks a draft if the current version is active). Args: id, instructions ([{instruction_id, instruction_version_id?, result_key?, overrides?}]). Returns {template, derived}.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"instructions": map[string]any{"type": "array"},
			}, []string{"id", "instructions"}),
			Handler: a.toolTemplatesSetInstructions,
		},
		{
			Name:        "templates_insert_instruction",
			Description: "Insert one instruction at sort_order. Args: id, sort_order, instruction_id, instruction_version_id?, result_key?, overrides?. Returns {template, derived}.",
			InputSchema: schemaObject(map[string]any{
				"id":                     map[string]any{"type": "integer"},
				"sort_order":             map[string]any{"type": "integer"},
				"instruction_id":         map[string]any{"type": "integer"},
				"instruction_version_id": map[string]any{"type": "integer"},
				"result_key":             map[string]any{"type": "string"},
				"overrides":              map[string]any{"type": "object"},
			}, []string{"id", "sort_order", "instruction_id"}),
			Handler: a.toolTemplatesInsertInstruction,
		},
		{
			Name:        "templates_remove_instruction",
			Description: "Remove the row at sort_order. Args: id, sort_order. Returns {template, derived}.",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"sort_order": map[string]any{"type": "integer"},
			}, []string{"id", "sort_order"}),
			Handler: a.toolTemplatesRemoveInstruction,
		},
		{
			Name:        "templates_reorder_instructions",
			Description: "Permute the draft composition. Args: id, order ([sort_order]) — must be a permutation of current sort_orders. Returns {template, derived}.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"order": map[string]any{"type": "array"},
			}, []string{"id", "order"}),
			Handler: a.toolTemplatesReorder,
		},
		{
			Name:        "templates_publish",
			Description: "Move the current draft version → active. Args: id. Returns {template}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolTemplatesPublish,
		},
		{
			Name:        "templates_archive",
			Description: "Soft-delete the template. In-flight gigs keep their snapshot. Args: id. Returns {ok}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolTemplatesArchive,
		},
		{
			Name:        "templates_check_updates",
			Description: "Report which pinned instruction versions have newer active versions. Use before forking a new template version. Args: id. Returns {stale: [{instruction_id, pinned_version_id, latest_version_id}]}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolTemplatesCheckUpdates,
		},
		{
			Name:        "templates_render_preview",
			Description: "Render title + each instruction with sample vars. No gig created. Args: id, vars. Returns {title, composition (rendered)}.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"vars": map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: a.toolTemplatesRenderPreview,
		},
	}
}

// ─── Tools ──────────────────────────────────────────────────────────

func (a *App) toolTemplatesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	title := strings.TrimSpace(strArg(args, "title_template"))
	if name == "" || title == "" {
		return nil, errors.New("name and title_template required")
	}
	kind := strArg(args, "kind")
	if kind == "" {
		kind = "action"
	}
	slug := strArg(args, "slug")
	if slug == "" {
		slug = slugify(name)
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO templates (project_id, slug, name, kind) VALUES (?, ?, ?, ?)`,
		pid, slug, name, kind,
	)
	if err != nil {
		return nil, fmt.Errorf("insert template: %w", err)
	}
	id, _ := res.LastInsertId()
	vRes, err := tx.Exec(
		`INSERT INTO template_versions
		   (template_id, version, status, title_template,
		    default_deadline_hours, default_priority)
		 VALUES (?, 1, 'draft', ?, ?, ?)`,
		id, title,
		nullInt64(int64(intArg(args, "default_deadline_hours", 0))),
		nullStr(strArg(args, "default_priority")),
	)
	if err != nil {
		return nil, err
	}
	vid, _ := vRes.LastInsertId()
	if _, err := tx.Exec(
		`UPDATE templates SET current_version_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		vid, id,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	tpl, err := getTemplate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"template": tpl}, nil
}

func (a *App) toolTemplatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := listTemplates(ctx.AppDB(), pid, listTemplatesFilter{
		Kind:            strArg(args, "kind"),
		IncludeArchived: boolArg(args, "include_archived", false),
		Limit:           intArg(args, "limit", 100),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"templates": rows}, nil
}

func (a *App) toolTemplatesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		if slug := strArg(args, "slug"); slug != "" {
			_ = ctx.AppDB().QueryRow(
				`SELECT id FROM templates WHERE project_id=? AND slug=?`, pid, slug,
			).Scan(&id)
		}
	}
	if id == 0 {
		return nil, errors.New("id or slug required")
	}
	tpl, err := getTemplate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if tpl == nil {
		return nil, errors.New("template not found")
	}
	return map[string]any{"template": tpl}, nil
}

func (a *App) toolTemplatesSetInstructions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	raw := sliceArg(args, "instructions")
	if id == 0 {
		return nil, errors.New("id required")
	}

	draftVID, err := ensureDraft(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM template_instructions WHERE template_version_id=?`, draftVID); err != nil {
		return nil, err
	}
	for i, ref := range raw {
		m, ok := ref.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("instructions[%d] not an object", i)
		}
		if err := insertCompositionRowTx(tx, pid, draftVID, i, m); err != nil {
			return nil, fmt.Errorf("instructions[%d]: %w", i, err)
		}
	}
	if _, err := tx.Exec(`UPDATE templates SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	tpl, _ := getTemplate(ctx.AppDB(), pid, id)
	return map[string]any{"template": tpl, "derived": tpl.CurrentVersion.Derived}, nil
}

func (a *App) toolTemplatesInsertInstruction(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	sortOrder := intArg(args, "sort_order", -1)
	if id == 0 || sortOrder < 0 {
		return nil, errors.New("id and sort_order required")
	}
	draftVID, err := ensureDraft(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Bump every existing row at sort_order >= n.
	if _, err := tx.Exec(
		`UPDATE template_instructions SET sort_order = sort_order + 1
		 WHERE template_version_id=? AND sort_order >= ?`,
		draftVID, sortOrder,
	); err != nil {
		return nil, err
	}
	if err := insertCompositionRowTx(tx, pid, draftVID, sortOrder, args); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE templates SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tpl, _ := getTemplate(ctx.AppDB(), pid, id)
	return map[string]any{"template": tpl, "derived": tpl.CurrentVersion.Derived}, nil
}

func (a *App) toolTemplatesRemoveInstruction(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	sortOrder := intArg(args, "sort_order", -1)
	if id == 0 || sortOrder < 0 {
		return nil, errors.New("id and sort_order required")
	}
	draftVID, err := ensureDraft(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM template_instructions WHERE template_version_id=? AND sort_order=?`,
		draftVID, sortOrder,
	); err != nil {
		return nil, err
	}
	// Compact sort_orders.
	if _, err := tx.Exec(
		`UPDATE template_instructions SET sort_order = sort_order - 1
		 WHERE template_version_id=? AND sort_order > ?`,
		draftVID, sortOrder,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE templates SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tpl, _ := getTemplate(ctx.AppDB(), pid, id)
	return map[string]any{"template": tpl, "derived": tpl.CurrentVersion.Derived}, nil
}

func (a *App) toolTemplatesReorder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	order := sliceArg(args, "order")
	if len(order) == 0 {
		return nil, errors.New("order required (array of current sort_orders)")
	}
	draftVID, err := ensureDraft(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Two-phase update to avoid uniqueness collisions on (template_version_id, sort_order).
	// First write large temporary sort_orders, then compact to 0..N-1 in the requested order.
	for i, raw := range order {
		old := intCast(raw)
		tmp := 1_000_000 + i
		if _, err := tx.Exec(
			`UPDATE template_instructions SET sort_order=?
			 WHERE template_version_id=? AND sort_order=?`,
			tmp, draftVID, old,
		); err != nil {
			return nil, err
		}
	}
	for i := range order {
		tmp := 1_000_000 + i
		if _, err := tx.Exec(
			`UPDATE template_instructions SET sort_order=?
			 WHERE template_version_id=? AND sort_order=?`,
			i, draftVID, tmp,
		); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE templates SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tpl, _ := getTemplate(ctx.AppDB(), pid, id)
	return map[string]any{"template": tpl, "derived": tpl.CurrentVersion.Derived}, nil
}

func (a *App) toolTemplatesPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	tpl, err := getTemplate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if tpl == nil || tpl.CurrentVersion == nil {
		return nil, errors.New("template has no current version")
	}
	if tpl.CurrentVersion.Status == "active" {
		return map[string]any{"template": tpl, "noop": true}, nil
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE template_versions SET status='active' WHERE id=?`,
		tpl.CurrentVersion.ID,
	); err != nil {
		return nil, err
	}
	tpl, _ = getTemplate(ctx.AppDB(), pid, id)
	ctx.Emit("template.published", map[string]any{
		"template_id":         id,
		"template_version_id": tpl.CurrentVersion.ID,
	})
	return map[string]any{"template": tpl}, nil
}

func (a *App) toolTemplatesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE templates SET archived_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		pid, id,
	); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolTemplatesCheckUpdates(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	tpl, err := getTemplate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if tpl == nil || tpl.CurrentVersion == nil {
		return nil, errors.New("template has no current version")
	}
	rows, err := ctx.AppDB().Query(
		`SELECT ti.instruction_id, ti.instruction_version_id, i.current_version_id
		 FROM template_instructions ti
		 JOIN instructions i ON i.id = ti.instruction_id
		 WHERE ti.template_version_id=? AND ti.instruction_version_id != i.current_version_id
		   AND i.archived_at IS NULL`,
		tpl.CurrentVersion.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stale := []map[string]any{}
	for rows.Next() {
		var iid, pinned, latest int64
		if err := rows.Scan(&iid, &pinned, &latest); err != nil {
			return nil, err
		}
		stale = append(stale, map[string]any{
			"instruction_id":    iid,
			"pinned_version_id": pinned,
			"latest_version_id": latest,
		})
	}
	return map[string]any{"stale": stale}, nil
}

func (a *App) toolTemplatesRenderPreview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	tpl, err := getTemplate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if tpl == nil || tpl.CurrentVersion == nil {
		return nil, errors.New("template has no current version")
	}
	vars := mapArg(args, "vars")
	rendered := renderCompositionForGig(tpl.CurrentVersion.Composition, vars)
	return map[string]any{
		"title":       interpolate(tpl.CurrentVersion.TitleTemplate, vars),
		"composition": rendered,
	}, nil
}

// ─── Composition write helper ──────────────────────────────────────

// insertCompositionRowTx resolves an instruction ref (id, optional
// version_id, optional result_key, optional overrides) and writes
// one template_instructions row.
func insertCompositionRowTx(tx *sql.Tx, pid string, tvid int64, sortOrder int, ref map[string]any) error {
	iid := int64Cast(ref["instruction_id"])
	if iid == 0 {
		return errors.New("instruction_id required")
	}
	// Validate the instruction belongs to this project, fetch kind +
	// current_version_id.
	var kind string
	var currentVID sql.NullInt64
	if err := tx.QueryRow(
		`SELECT kind, current_version_id FROM instructions WHERE id=? AND project_id=? AND archived_at IS NULL`,
		iid, pid,
	).Scan(&kind, &currentVID); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("instruction %d not in project %s", iid, pid)
	} else if err != nil {
		return err
	}
	ivid := int64Cast(ref["instruction_version_id"])
	if ivid == 0 {
		if !currentVID.Valid {
			return fmt.Errorf("instruction %d has no current version", iid)
		}
		ivid = currentVID.Int64
	}
	resultKey := strOf(ref["result_key"])
	overrides := ref["overrides"]
	var overridesJSON sql.NullString
	if overrides != nil {
		overridesJSON = sql.NullString{Valid: true, String: mustJSON(overrides)}
	}
	if _, err := tx.Exec(
		`INSERT INTO template_instructions
		   (template_version_id, instruction_id, instruction_version_id,
		    sort_order, result_key, overrides_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		tvid, iid, ivid, sortOrder, nullStr(resultKey), overridesJSON,
	); err != nil {
		return err
	}
	return nil
}

// ensureDraft returns the id of the template's current draft version,
// auto-forking from the current active version if none exists.
func ensureDraft(db *sql.DB, pid string, templateID int64) (int64, error) {
	var currentVID sql.NullInt64
	if err := db.QueryRow(
		`SELECT current_version_id FROM templates WHERE id=? AND project_id=?`,
		templateID, pid,
	).Scan(&currentVID); errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("template %d not found", templateID)
	} else if err != nil {
		return 0, err
	}
	if !currentVID.Valid {
		return 0, fmt.Errorf("template %d has no current version", templateID)
	}
	var status string
	if err := db.QueryRow(
		`SELECT status FROM template_versions WHERE id=?`, currentVID.Int64,
	).Scan(&status); err != nil {
		return 0, err
	}
	if status == "draft" {
		return currentVID.Int64, nil
	}
	// Fork: copy header + composition into a new draft version.
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var maxVersion int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM template_versions WHERE template_id=?`, templateID,
	).Scan(&maxVersion); err != nil {
		return 0, err
	}
	var titleTpl, defPriority sql.NullString
	var defDeadlineHours sql.NullInt64
	var skillsJSON, overridesJSON sql.NullString
	_ = tx.QueryRow(
		`SELECT title_template, default_deadline_hours, default_skill_ids_json,
		        default_priority, variable_overrides_json
		 FROM template_versions WHERE id=?`,
		currentVID.Int64,
	).Scan(&titleTpl, &defDeadlineHours, &skillsJSON, &defPriority, &overridesJSON)
	res, err := tx.Exec(
		`INSERT INTO template_versions
		   (template_id, version, status, title_template,
		    default_deadline_hours, default_skill_ids_json,
		    default_priority, variable_overrides_json)
		 VALUES (?, ?, 'draft', ?, ?, ?, ?, ?)`,
		templateID, maxVersion+1, titleTpl, defDeadlineHours,
		skillsJSON, defPriority, overridesJSON,
	)
	if err != nil {
		return 0, err
	}
	newVID, _ := res.LastInsertId()
	// Copy composition rows.
	if _, err := tx.Exec(
		`INSERT INTO template_instructions
		   (template_version_id, instruction_id, instruction_version_id,
		    sort_order, result_key, overrides_json)
		 SELECT ?, instruction_id, instruction_version_id, sort_order, result_key, overrides_json
		 FROM template_instructions WHERE template_version_id=?`,
		newVID, currentVID.Int64,
	); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`UPDATE templates SET current_version_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		newVID, templateID,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newVID, nil
}

// ─── DB helpers ─────────────────────────────────────────────────────

func getTemplate(db *sql.DB, pid string, id int64) (*template, error) {
	t := &template{ProjectID: pid}
	var currentVID sql.NullInt64
	var archivedAt sql.NullString
	err := db.QueryRow(
		`SELECT id, slug, name, kind, current_version_id, archived_at, created_at, updated_at
		 FROM templates WHERE project_id=? AND id=?`,
		pid, id,
	).Scan(&t.ID, &t.Slug, &t.Name, &t.Kind, &currentVID, &archivedAt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.CurrentVersionID = currentVID.Int64
	t.ArchivedAt = archivedAt.String
	if currentVID.Valid {
		v, err := getTemplateVersion(db, currentVID.Int64)
		if err != nil {
			return nil, err
		}
		// Hydrate composition + derived.
		items, err := loadComposition(db, currentVID.Int64)
		if err != nil {
			return nil, err
		}
		v.Composition = items
		derived := deriveFromComposition(items)
		v.Derived = &derived
		t.CurrentVersion = v
	}
	return t, nil
}

func getTemplateVersion(db *sql.DB, id int64) (*templateVersion, error) {
	v := &templateVersion{}
	var defDeadline sql.NullInt64
	var skillsJSON, overridesJSON, defPriority, createdBy sql.NullString
	err := db.QueryRow(
		`SELECT id, template_id, version, status, title_template,
		        default_deadline_hours, default_skill_ids_json,
		        default_priority, variable_overrides_json, created_by, created_at
		 FROM template_versions WHERE id=?`,
		id,
	).Scan(&v.ID, &v.TemplateID, &v.Version, &v.Status, &v.TitleTemplate,
		&defDeadline, &skillsJSON, &defPriority, &overridesJSON, &createdBy, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if defDeadline.Valid {
		v.DefaultDeadlineHours = int(defDeadline.Int64)
	}
	_ = parseJSON(skillsJSON.String, &v.DefaultSkillIDs)
	_ = parseJSON(overridesJSON.String, &v.VariableOverrides)
	v.DefaultPriority = defPriority.String
	v.CreatedBy = createdBy.String
	return v, nil
}

func loadComposition(db *sql.DB, templateVersionID int64) ([]compositionItem, error) {
	rows, err := db.Query(
		`SELECT ti.sort_order, ti.instruction_id, ti.instruction_version_id,
		        ti.result_key, ti.overrides_json,
		        i.kind, iv.body_json, iv.declared_variables_json, iv.default_result_key
		 FROM template_instructions ti
		 JOIN instructions i ON i.id = ti.instruction_id
		 JOIN instruction_versions iv ON iv.id = ti.instruction_version_id
		 WHERE ti.template_version_id=?
		 ORDER BY ti.sort_order`,
		templateVersionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []compositionItem{}
	for rows.Next() {
		var it compositionItem
		var resultKey, overridesJSON, drk sql.NullString
		var bodyJSON, declaredJSON string
		if err := rows.Scan(
			&it.SortOrder, &it.InstructionID, &it.InstructionVersionID,
			&resultKey, &overridesJSON,
			&it.Kind, &bodyJSON, &declaredJSON, &drk,
		); err != nil {
			return nil, err
		}
		_ = parseJSON(bodyJSON, &it.Body)
		_ = parseJSON(declaredJSON, &it.DeclaredVariables)
		// Result-key resolution: explicit row override > version default.
		it.ResultKey = resultKey.String
		if it.ResultKey == "" {
			it.ResultKey = drk.String
		}
		// Per-row overrides are merged into the body for derivation
		// and rendering. Keeps the version body immutable.
		if overridesJSON.Valid && overridesJSON.String != "" {
			var ov map[string]any
			if err := parseJSON(overridesJSON.String, &ov); err == nil {
				for k, v := range ov {
					it.Body[k] = v
				}
			}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type listTemplatesFilter struct {
	Kind            string
	IncludeArchived bool
	Limit           int
}

func listTemplates(db *sql.DB, pid string, f listTemplatesFilter) ([]*template, error) {
	conds := []string{"project_id=?"}
	args := []any{pid}
	if !f.IncludeArchived {
		conds = append(conds, "archived_at IS NULL")
	}
	if f.Kind != "" {
		conds = append(conds, "kind=?")
		args = append(args, f.Kind)
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q := `SELECT id FROM templates WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*template{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, err := getTemplate(db, pid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPTemplatesCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := listTemplates(ctx.AppDB(), pid, listTemplatesFilter{
			Kind:            r.URL.Query().Get("kind"),
			IncludeArchived: r.URL.Query().Get("include_archived") == "true",
			Limit:           parseQueryIntDefault(r, "limit", 100),
		})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"templates": rows})
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
		out, err := a.toolTemplatesCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPTemplateItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/templates/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "instructions":
			if r.Method != http.MethodPut {
				httpErr(w, http.StatusMethodNotAllowed, "PUT only")
				return
			}
			var body struct {
				Instructions []map[string]any `json:"instructions"`
			}
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			out, err := a.toolTemplatesSetInstructions(ctx, map[string]any{
				"_project_id":  pid,
				"id":           id,
				"instructions": toAnySlice(body.Instructions),
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "publish":
			out, err := a.toolTemplatesPublish(ctx, map[string]any{"_project_id": pid, "id": id})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "preview":
			var body map[string]any
			_ = httpDecode(r, &body)
			out, err := a.toolTemplatesRenderPreview(ctx, map[string]any{
				"_project_id": pid,
				"id":          id,
				"vars":        body["vars"],
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTemplatesGet(ctx, map[string]any{"_project_id": pid, "id": id})
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodDelete:
		out, err := a.toolTemplatesArchive(ctx, map[string]any{"_project_id": pid, "id": id})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func toAnySlice(in []map[string]any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
