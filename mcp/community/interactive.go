package main

// Interactive lessons.
//
// A lesson can be made of ordered blocks (Markdown, the lesson video,
// resources, quizzes, assignments, and "elements"). An element is a small
// HTML/JS program that the portal mounts in a sandboxed iframe
// (sandbox="allow-scripts", opaque origin, inline SDK, strict CSP) and
// talks to through a private MessagePort. The server's part is:
//
//   - a per-community element library with immutable published versions,
//     so a lesson pins a version and edits never change live lessons;
//   - per-member element state and recorded steps; when every required
//     step of a lesson's element blocks is recorded, the lesson completes;
//   - runners: an element may ask the host to execute something on an app
//     tool the operator bound to a runner name (e.g. "js" -> functions).
//
// Lessons without blocks keep rendering the classic way.

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	elementSourceLimit  = 400_000
	elementStateLimit   = 64_000
	blockSettingsLimit  = 64_000
	maxBlocksPerLesson  = 50
	maxStepsPerBlock    = 50
	maxStepLength       = 64
	runnerCallsPerMin   = 20
	runnerInputLimit    = 200_000
	coursePackageFormat = "apteva.community.course"
)

var (
	lessonLayouts = map[string]bool{"article": true, "stage": true}
	blockSlots    = map[string]bool{"main": true, "stage": true, "side": true}
	blockTypes    = map[string]bool{
		"markdown": true, "lesson_body": true, "video": true, "resources": true,
		"quizzes": true, "assignments": true, "element": true,
	}
	elementSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	runnerNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,40}$`)
	stepRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
)

//go:embed elements/*.html elements/starters.json
var starterElementFS embed.FS

type CourseElement struct {
	ID                  string          `json:"id"`
	CommunityID         string          `json:"community_id"`
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	CurrentVersion      int64           `json:"current_version"`
	DraftRunners        []string        `json:"draft_runners"`
	DraftSettingsSchema json.RawMessage `json:"draft_settings_schema"`
	DraftChanged        bool            `json:"draft_changed"`
	DraftSource         *string         `json:"draft_source,omitempty"`
	ArchivedAt          *string         `json:"archived_at,omitempty"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
	UsedByBlocks        int64           `json:"used_by_blocks"`
}

type CourseElementVersion struct {
	ElementID      string          `json:"element_id"`
	Version        int64           `json:"version"`
	Checksum       string          `json:"checksum"`
	Runners        []string        `json:"runners"`
	SettingsSchema json.RawMessage `json:"settings_schema"`
	PublishedAt    string          `json:"published_at"`
	Source         *string         `json:"source,omitempty"`
}

type LessonBlock struct {
	ID             string          `json:"id"`
	LessonID       string          `json:"lesson_id"`
	Position       int64           `json:"position"`
	Slot           string          `json:"slot"`
	Type           string          `json:"type"`
	Title          string          `json:"title"`
	ElementID      *string         `json:"element_id,omitempty"`
	ElementVersion *int64          `json:"element_version,omitempty"`
	ElementSlug    *string         `json:"element_slug,omitempty"`
	ElementName    *string         `json:"element_name,omitempty"`
	Checksum       *string         `json:"checksum,omitempty"`
	Runners        []string        `json:"runners,omitempty"`
	Settings       json.RawMessage `json:"settings"`
	RequiredSteps  []string        `json:"required_steps"`
	Height         *int64          `json:"height,omitempty"`
}

func interactiveTools() []sdk.Tool {
	str := map[string]any{"type": "string"}
	obj := map[string]any{"type": "object"}
	strList := map[string]any{"type": "array", "items": str}
	return []sdk.Tool{
		{
			Name:        "course_elements_list",
			Description: "List the community's interactive element library (sandboxed HTML/JS programs used by lesson blocks). Args: community_id, include_archived?.",
			InputSchema: schemaObject(map[string]any{"community_id": str, "include_archived": map[string]any{"type": "boolean"}}, []string{"community_id"}),
			Handler:     toolCourseElementsList,
		},
		{
			Name:        "course_elements_get",
			Description: "Get an element with its draft source and published versions. Args: id, version? (also returns that version's source).",
			InputSchema: schemaObject(map[string]any{"id": str, "version": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     toolCourseElementsGet,
		},
		{
			Name: "course_elements_save",
			Description: "Create or update an element draft. Args: id? (update) or community_id + slug (create), name, description?, html (a full HTML document; " +
				"it gets window.lesson with ready/save/emit/on/complete/run), runners? (runner names it may call), settings_schema? (JSON Schema for per-block settings). " +
				"Drafts never reach members until course_elements_publish.",
			InputSchema: schemaObject(map[string]any{
				"id": str, "community_id": str, "slug": str, "name": str, "description": str,
				"html": str, "runners": strList, "settings_schema": obj,
			}, nil),
			Handler: toolCourseElementsSave,
		},
		{
			Name:        "course_elements_publish",
			Description: "Publish the element draft as a new immutable version. Args: id, upgrade_blocks? (move every lesson block on an older version to the new one).",
			InputSchema: schemaObject(map[string]any{"id": str, "upgrade_blocks": map[string]any{"type": "boolean"}}, []string{"id"}),
			Handler:     toolCourseElementsPublish,
		},
		{
			Name:        "course_elements_archive",
			Description: "Archive or restore an element. Archived elements cannot be added to new blocks; existing blocks keep working. Args: id, archived (bool).",
			InputSchema: schemaObject(map[string]any{"id": str, "archived": map[string]any{"type": "boolean"}}, []string{"id", "archived"}),
			Handler:     toolCourseElementsArchive,
		},
		{
			Name:        "course_elements_install_starters",
			Description: "Install the bundled starter elements (code-playground, finance-lab, board-scene) into a community's library. Existing slugs are left alone. Args: community_id.",
			InputSchema: schemaObject(map[string]any{"community_id": str}, []string{"community_id"}),
			Handler:     toolCourseElementsInstallStarters,
		},
		{
			Name:        "lesson_blocks_list",
			Description: "List a lesson's layout and ordered blocks. Args: lesson_id.",
			InputSchema: schemaObject(map[string]any{"lesson_id": str}, []string{"lesson_id"}),
			Handler:     toolLessonBlocksList,
		},
		{
			Name: "lesson_blocks_set",
			Description: "Replace a lesson's blocks (order = array order). Args: lesson_id, layout? (article|stage), blocks: [{id? (keep to preserve member state), " +
				"type: markdown|lesson_body|video|resources|quizzes|assignments|element, slot?: main|stage|side, title?, element_id?, element_version? (defaults to current), " +
				"settings? (object; markdown uses settings.body), required_steps? (element steps that complete the lesson), height? (px)}]. An empty list restores the classic lesson view.",
			InputSchema: schemaObject(map[string]any{
				"lesson_id": str, "layout": str,
				"blocks": map[string]any{"type": "array", "items": obj},
			}, []string{"lesson_id", "blocks"}),
			Handler: toolLessonBlocksSet,
		},
		{
			Name:        "element_runners_list",
			Description: "List runner bindings elements may call through the host. Args: community_id.",
			InputSchema: schemaObject(map[string]any{"community_id": str}, []string{"community_id"}),
			Handler:     toolElementRunnersList,
		},
		{
			Name: "element_runners_set",
			Description: "Replace the runner bindings. A runner maps a name an element asks for to an app tool that receives {runner, input, element, version}. " +
				"Args: community_id, runners: [{name, app, tool}].",
			InputSchema: schemaObject(map[string]any{
				"community_id": str,
				"runners":      map[string]any{"type": "array", "items": obj},
			}, []string{"community_id", "runners"}),
			Handler: toolElementRunnersSet,
		},
		{
			Name:        "lesson_element_source",
			Description: "Fetch the published source of an element version used by a lesson. Args: lesson_id, element_id, version.",
			InputSchema: schemaObject(map[string]any{"lesson_id": str, "element_id": str, "version": map[string]any{"type": "integer"}}, []string{"lesson_id", "element_id", "version"}),
			Handler:     toolLessonElementSource,
		},
		{
			Name:        "element_state_save",
			Description: "Save a member's state for one element block. Args: lesson_id, block_id, member_id, state (object, at most 64 KB).",
			InputSchema: schemaObject(map[string]any{"lesson_id": str, "block_id": str, "member_id": str, "state": obj}, []string{"lesson_id", "block_id", "member_id", "state"}),
			Handler:     toolElementStateSave,
		},
		{
			Name:        "lesson_step_complete",
			Description: "Record a step an element reports. When every required step of the lesson's element blocks is recorded, the lesson is marked complete. Args: lesson_id, block_id, member_id, step.",
			InputSchema: schemaObject(map[string]any{"lesson_id": str, "block_id": str, "member_id": str, "step": str}, []string{"lesson_id", "block_id", "member_id", "step"}),
			Handler:     toolLessonStepComplete,
		},
		{
			Name:        "element_run",
			Description: "Run input on a runner the block's element version declares and the community has bound. Args: lesson_id, block_id, member_id, runner, input (object).",
			InputSchema: schemaObject(map[string]any{"lesson_id": str, "block_id": str, "member_id": str, "runner": str, "input": obj}, []string{"lesson_id", "block_id", "member_id", "runner"}),
			Handler:     toolElementRun,
		},
		{
			Name:        "course_export",
			Description: "Export a course (sections, lessons, blocks, quizzes, assignments, and the element versions it uses) as a portable JSON package. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": str}, []string{"space_id"}),
			Handler:     toolCourseExport,
		},
		{
			Name: "course_import",
			Description: "Import a course package from course_export as a new course. Elements are matched by slug and checksum; changed ones get a new version. " +
				"Args: community_id, package (object), slug?, name?.",
			InputSchema: schemaObject(map[string]any{"community_id": str, "package": obj, "slug": str, "name": str}, []string{"community_id", "package"}),
			Handler:     toolCourseImport,
		},
	}
}

// ─── Element library ─────────────────────────────────────────────

const elementCols = `e.id, e.community_id, e.slug, e.name, e.description, e.current_version,
	e.draft_runners_json, e.draft_settings_schema_json, e.archived_at, e.created_at, e.updated_at,
	(SELECT COUNT(*) FROM lesson_blocks b WHERE b.element_id = e.id),
	COALESCE((SELECT v.checksum FROM course_element_versions v WHERE v.element_id = e.id AND v.version = e.current_version), ''),
	e.draft_source`

func scanElement(scan func(...any) error, withSource bool) (CourseElement, error) {
	var e CourseElement
	var runners, schema, publishedChecksum, draft string
	var archived sql.NullString
	if err := scan(&e.ID, &e.CommunityID, &e.Slug, &e.Name, &e.Description, &e.CurrentVersion,
		&runners, &schema, &archived, &e.CreatedAt, &e.UpdatedAt, &e.UsedByBlocks, &publishedChecksum, &draft); err != nil {
		return e, err
	}
	e.DraftRunners = decodeStrings(runners)
	e.DraftSettingsSchema = rawJSON(schema, "{}")
	if archived.Valid {
		e.ArchivedAt = &archived.String
	}
	e.DraftChanged = draft != "" && checksumOf(draft) != publishedChecksum
	if withSource {
		e.DraftSource = &draft
	}
	return e, nil
}

func loadElement(db *sql.DB, id string, withSource bool) (CourseElement, error) {
	e, err := scanElement(db.QueryRow(`SELECT `+elementCols+` FROM course_elements e WHERE e.id = ?`, id).Scan, withSource)
	if errors.Is(err, sql.ErrNoRows) {
		return e, fmt.Errorf("element %q not found", id)
	}
	return e, err
}

func ensureElementVisible(ctx *sdk.AppCtx, db *sql.DB, id string, withSource bool) (CourseElement, error) {
	e, err := loadElement(db, id, withSource)
	if err != nil {
		return e, err
	}
	return e, ensureCommunityVisible(ctx, db, e.CommunityID)
}

func loadElementVersion(db *sql.DB, elementID string, version int64, withSource bool) (CourseElementVersion, error) {
	var v CourseElementVersion
	var source, runners, schema string
	err := db.QueryRow(`SELECT element_id, version, checksum, runners_json, settings_schema_json, published_at, source
		FROM course_element_versions WHERE element_id = ? AND version = ?`, elementID, version).
		Scan(&v.ElementID, &v.Version, &v.Checksum, &runners, &schema, &v.PublishedAt, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("element %q has no version %d", elementID, version)
	}
	if err != nil {
		return v, err
	}
	v.Runners = decodeStrings(runners)
	v.SettingsSchema = rawJSON(schema, "{}")
	if withSource {
		v.Source = &source
	}
	return v, nil
}

func toolCourseElementsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	q := `SELECT ` + elementCols + ` FROM course_elements e WHERE e.community_id = ?`
	if v, _ := args["include_archived"].(bool); !v {
		q += ` AND e.archived_at IS NULL`
	}
	rows, err := db.Query(q+` ORDER BY e.name COLLATE NOCASE`, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CourseElement{}
	for rows.Next() {
		e, err := scanElement(rows.Scan, false)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return map[string]any{"elements": out}, rows.Err()
}

func toolCourseElementsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	e, err := ensureElementVisible(ctx, db, id, true)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT version FROM course_element_versions WHERE element_id = ? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	var numbers []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		numbers = append(numbers, n)
	}
	rows.Close()
	versions := []CourseElementVersion{}
	want, hasWant := intArg(args, "version")
	for _, n := range numbers {
		v, err := loadElementVersion(db, id, n, hasWant && n == want)
		if err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return map[string]any{"element": e, "versions": versions}, nil
}

func toolCourseElementsSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	html, hasHTML := args["html"].(string)
	if hasHTML && len(html) > elementSourceLimit {
		return nil, fmt.Errorf("html must be at most %d bytes", elementSourceLimit)
	}
	runners, err := runnerNamesArg(args, "runners")
	if err != nil {
		return nil, err
	}
	var schema string
	if raw, ok := args["settings_schema"]; ok {
		b, err := json.Marshal(raw)
		if err != nil || len(b) > blockSettingsLimit {
			return nil, errors.New("settings_schema must be a JSON object of at most 64 KB")
		}
		if _, ok := raw.(map[string]any); !ok {
			return nil, errors.New("settings_schema must be an object")
		}
		schema = string(b)
	}
	if id := strArg(args, "id", ""); id != "" {
		cur, err := ensureElementVisible(ctx, db, id, false)
		if err != nil {
			return nil, err
		}
		sets := []string{"updated_at = CURRENT_TIMESTAMP"}
		vals := []any{}
		if v, ok := args["name"].(string); ok && strings.TrimSpace(v) != "" {
			sets, vals = append(sets, "name = ?"), append(vals, strings.TrimSpace(v))
		}
		if v, ok := args["description"].(string); ok {
			sets, vals = append(sets, "description = ?"), append(vals, v)
		}
		if hasHTML {
			sets, vals = append(sets, "draft_source = ?"), append(vals, html)
		}
		if runners != nil {
			sets, vals = append(sets, "draft_runners_json = ?"), append(vals, encodeJSON(runners))
		}
		if schema != "" {
			sets, vals = append(sets, "draft_settings_schema_json = ?"), append(vals, schema)
		}
		if slug, ok := args["slug"].(string); ok && slug != "" && slug != cur.Slug {
			return nil, errors.New("slug cannot change; create a new element instead")
		}
		vals = append(vals, id)
		if _, err := db.Exec(`UPDATE course_elements SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...); err != nil {
			return nil, err
		}
		return loadElement(db, id, true)
	}
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(strArg(args, "slug", ""))
	if !elementSlugRe.MatchString(slug) {
		return nil, errors.New("slug must be lowercase letters, digits and dashes (max 63)")
	}
	name := strings.TrimSpace(strArg(args, "name", ""))
	if name == "" {
		return nil, errors.New("name is required")
	}
	if runners == nil {
		runners = []string{}
	}
	if schema == "" {
		schema = "{}"
	}
	id := newID("elm")
	if _, err := db.Exec(`INSERT INTO course_elements (id, community_id, slug, name, description, draft_source, draft_runners_json, draft_settings_schema_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, communityID, slug, name, strArg(args, "description", ""), html, encodeJSON(runners), schema); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("an element with slug %q already exists", slug)
		}
		return nil, err
	}
	return loadElement(db, id, true)
}

func toolCourseElementsPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	e, err := ensureElementVisible(ctx, db, id, true)
	if err != nil {
		return nil, err
	}
	version, created, err := publishElementDraft(db, e)
	if err != nil {
		return nil, err
	}
	upgraded := int64(0)
	if v, _ := args["upgrade_blocks"].(bool); v {
		res, err := db.Exec(`UPDATE lesson_blocks SET element_version = ?, updated_at = CURRENT_TIMESTAMP
			WHERE element_id = ? AND element_version < ?`, version, id, version)
		if err != nil {
			return nil, err
		}
		upgraded, _ = res.RowsAffected()
	}
	if created {
		emit(ctx, "element.published", map[string]any{"community_id": e.CommunityID, "element_id": id, "version": version})
	}
	out, err := loadElement(db, id, false)
	return map[string]any{"element": out, "version": version, "created": created, "upgraded_blocks": upgraded}, err
}

// publishElementDraft snapshots the draft as the next version, or returns
// the current version when the draft is unchanged.
func publishElementDraft(db *sql.DB, e CourseElement) (int64, bool, error) {
	source := ""
	if e.DraftSource != nil {
		source = *e.DraftSource
	}
	if strings.TrimSpace(source) == "" {
		return 0, false, errors.New("element has no html to publish")
	}
	if !e.DraftChanged && e.CurrentVersion > 0 {
		cur, err := loadElementVersion(db, e.ID, e.CurrentVersion, false)
		if err == nil && sameStrings(cur.Runners, e.DraftRunners) && string(cur.SettingsSchema) == string(e.DraftSettingsSchema) {
			return e.CurrentVersion, false, nil
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var next int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM course_element_versions WHERE element_id = ?`, e.ID).Scan(&next); err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(`INSERT INTO course_element_versions (element_id, version, source, checksum, runners_json, settings_schema_json)
		VALUES (?, ?, ?, ?, ?, ?)`, e.ID, next, source, checksumOf(source), encodeJSON(e.DraftRunners), string(e.DraftSettingsSchema)); err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(`UPDATE course_elements SET current_version = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, next, e.ID); err != nil {
		return 0, false, err
	}
	return next, true, tx.Commit()
}

func toolCourseElementsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	archived, ok := args["archived"].(bool)
	if !ok {
		return nil, errors.New("archived (bool) is required")
	}
	db := ctx.AppDB()
	if _, err := ensureElementVisible(ctx, db, id, false); err != nil {
		return nil, err
	}
	q := `UPDATE course_elements SET archived_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	if archived {
		q = `UPDATE course_elements SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	}
	if _, err := db.Exec(q, id); err != nil {
		return nil, err
	}
	return loadElement(db, id, false)
}

type starterElement struct {
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	File           string         `json:"file"`
	Runners        []string       `json:"runners"`
	SettingsSchema map[string]any `json:"settings_schema"`
}

func toolCourseElementsInstallStarters(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	raw, err := starterElementFS.ReadFile("elements/starters.json")
	if err != nil {
		return nil, err
	}
	var starters []starterElement
	if err := json.Unmarshal(raw, &starters); err != nil {
		return nil, err
	}
	installed, skipped := []CourseElement{}, []string{}
	for _, s := range starters {
		var existing string
		err := db.QueryRow(`SELECT id FROM course_elements WHERE community_id = ? AND slug = ?`, communityID, s.Slug).Scan(&existing)
		if err == nil {
			skipped = append(skipped, s.Slug)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		html, err := starterElementFS.ReadFile("elements/" + s.File)
		if err != nil {
			return nil, err
		}
		out, err := toolCourseElementsSave(ctx, map[string]any{
			"community_id": communityID, "slug": s.Slug, "name": s.Name, "description": s.Description,
			"html": string(html), "runners": anySlice(s.Runners), "settings_schema": s.SettingsSchema,
		})
		if err != nil {
			return nil, err
		}
		e := out.(CourseElement)
		if _, _, err := publishElementDraft(db, e); err != nil {
			return nil, err
		}
		e, err = loadElement(db, e.ID, false)
		if err != nil {
			return nil, err
		}
		installed = append(installed, e)
	}
	return map[string]any{"installed": installed, "skipped": skipped}, nil
}

// ─── Lesson blocks ───────────────────────────────────────────────

const blockCols = `b.id, b.lesson_id, b.position, b.slot, b.type, b.title, b.element_id, b.element_version,
	b.settings_json, b.required_steps_json, b.height, e.slug, e.name, v.checksum, v.runners_json`

const blockFrom = ` FROM lesson_blocks b
	LEFT JOIN course_elements e ON e.id = b.element_id
	LEFT JOIN course_element_versions v ON v.element_id = b.element_id AND v.version = b.element_version`

func listLessonBlocks(db *sql.DB, lessonID string) ([]LessonBlock, error) {
	rows, err := db.Query(`SELECT `+blockCols+blockFrom+` WHERE b.lesson_id = ? ORDER BY b.position, b.created_at`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LessonBlock{}
	for rows.Next() {
		var b LessonBlock
		var elementID, slug, name, checksum, runners sql.NullString
		var version, height sql.NullInt64
		var settings, steps string
		if err := rows.Scan(&b.ID, &b.LessonID, &b.Position, &b.Slot, &b.Type, &b.Title, &elementID, &version,
			&settings, &steps, &height, &slug, &name, &checksum, &runners); err != nil {
			return nil, err
		}
		b.Settings = rawJSON(settings, "{}")
		b.RequiredSteps = decodeStrings(steps)
		if elementID.Valid {
			b.ElementID = &elementID.String
		}
		if version.Valid {
			b.ElementVersion = &version.Int64
		}
		if height.Valid {
			b.Height = &height.Int64
		}
		if slug.Valid {
			b.ElementSlug = &slug.String
		}
		if name.Valid {
			b.ElementName = &name.String
		}
		if checksum.Valid {
			b.Checksum = &checksum.String
		}
		if runners.Valid {
			b.Runners = decodeStrings(runners.String)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func lessonLayout(db *sql.DB, lessonID string) (string, error) {
	var layout string
	err := db.QueryRow(`SELECT layout FROM lessons WHERE id = ?`, lessonID).Scan(&layout)
	return layout, notFound(err, "lesson")
}

func toolLessonBlocksList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if _, _, err := ensureLessonVisible(ctx, db, lessonID); err != nil {
		return nil, err
	}
	layout, err := lessonLayout(db, lessonID)
	if err != nil {
		return nil, err
	}
	blocks, err := listLessonBlocks(db, lessonID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"lesson_id": lessonID, "layout": layout, "blocks": blocks}, nil
}

type blockInput struct {
	ID             string
	Slot           string
	Type           string
	Title          string
	ElementID      any
	ElementVersion any
	Settings       string
	RequiredSteps  string
	Height         any
}

func toolLessonBlocksSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	lesson, _, err := ensureLessonVisible(ctx, db, lessonID)
	if err != nil {
		return nil, err
	}
	layout := strArg(args, "layout", "")
	if layout != "" && !lessonLayouts[layout] {
		return nil, errors.New("layout must be article or stage")
	}
	raw, ok := args["blocks"].([]any)
	if !ok && args["blocks"] != nil {
		return nil, errors.New("blocks must be an array")
	}
	if len(raw) > maxBlocksPerLesson {
		return nil, fmt.Errorf("a lesson can have at most %d blocks", maxBlocksPerLesson)
	}
	existing := map[string]bool{}
	current, err := listLessonBlocks(db, lessonID)
	if err != nil {
		return nil, err
	}
	for _, b := range current {
		existing[b.ID] = true
	}
	inputs := make([]blockInput, 0, len(raw))
	seen := map[string]bool{}
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("blocks[%d] must be an object", i)
		}
		in, err := normalizeBlock(db, lesson.CommunityID, m)
		if err != nil {
			return nil, fmt.Errorf("blocks[%d]: %w", i, err)
		}
		if in.ID != "" {
			if !existing[in.ID] {
				return nil, fmt.Errorf("blocks[%d]: block %q does not belong to this lesson", i, in.ID)
			}
			if seen[in.ID] {
				return nil, fmt.Errorf("blocks[%d]: duplicate block id %q", i, in.ID)
			}
			seen[in.ID] = true
		}
		inputs = append(inputs, in)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for id := range existing {
		if !seen[id] {
			if _, err := tx.Exec(`DELETE FROM lesson_blocks WHERE id = ?`, id); err != nil {
				return nil, err
			}
		}
	}
	for i, in := range inputs {
		if in.ID == "" {
			in.ID = newID("blk")
			_, err = tx.Exec(`INSERT INTO lesson_blocks (id, lesson_id, position, slot, type, title, element_id, element_version, settings_json, required_steps_json, height)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, in.ID, lessonID, i, in.Slot, in.Type, in.Title, in.ElementID, in.ElementVersion, in.Settings, in.RequiredSteps, in.Height)
		} else {
			_, err = tx.Exec(`UPDATE lesson_blocks SET position = ?, slot = ?, type = ?, title = ?, element_id = ?, element_version = ?,
				settings_json = ?, required_steps_json = ?, height = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				i, in.Slot, in.Type, in.Title, in.ElementID, in.ElementVersion, in.Settings, in.RequiredSteps, in.Height, in.ID)
		}
		if err != nil {
			return nil, err
		}
	}
	if layout != "" {
		if _, err := tx.Exec(`UPDATE lessons SET layout = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, layout, lessonID); err != nil {
			return nil, err
		}
	} else {
		_, err = tx.Exec(`UPDATE lessons SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, lessonID)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return toolLessonBlocksList(ctx, map[string]any{"lesson_id": lessonID})
}

func normalizeBlock(db *sql.DB, communityID string, m map[string]any) (blockInput, error) {
	in := blockInput{ID: strArg(m, "id", ""), Type: strArg(m, "type", ""), Slot: strArg(m, "slot", "main"), Title: strings.TrimSpace(strArg(m, "title", ""))}
	if !blockTypes[in.Type] {
		return in, fmt.Errorf("type %q is not a block type", in.Type)
	}
	if !blockSlots[in.Slot] {
		return in, errors.New("slot must be main, stage or side")
	}
	if len(in.Title) > 300 {
		return in, errors.New("title must be at most 300 bytes")
	}
	settings := map[string]any{}
	if v, ok := m["settings"]; ok && v != nil {
		s, ok := v.(map[string]any)
		if !ok {
			return in, errors.New("settings must be an object")
		}
		settings = s
	}
	b, _ := json.Marshal(settings)
	if len(b) > blockSettingsLimit {
		return in, errors.New("settings must be at most 64 KB")
	}
	in.Settings = string(b)
	if in.Type == "markdown" {
		if body, _ := settings["body"].(string); strings.TrimSpace(body) == "" {
			return in, errors.New("markdown blocks need settings.body")
		}
	}
	if h, ok := intArg(m, "height"); ok {
		if h < 120 || h > 2000 {
			return in, errors.New("height must be between 120 and 2000")
		}
		in.Height = h
	}
	steps, err := stringListArg(m, "required_steps")
	if err != nil {
		return in, err
	}
	if len(steps) > maxStepsPerBlock {
		return in, fmt.Errorf("at most %d required steps per block", maxStepsPerBlock)
	}
	for _, s := range steps {
		if err := validateStep(s); err != nil {
			return in, err
		}
	}
	if in.Type != "element" {
		if len(steps) > 0 {
			return in, errors.New("only element blocks can have required_steps")
		}
		in.RequiredSteps = "[]"
		return in, nil
	}
	in.RequiredSteps = encodeJSON(steps)
	elementID := strArg(m, "element_id", "")
	if elementID == "" {
		return in, errors.New("element blocks need element_id")
	}
	e, err := loadElement(db, elementID, false)
	if err != nil {
		return in, err
	}
	if e.CommunityID != communityID {
		return in, errors.New("element belongs to another community")
	}
	version, ok := intArg(m, "element_version")
	if !ok {
		if e.ArchivedAt != nil {
			return in, fmt.Errorf("element %q is archived", e.Slug)
		}
		version = e.CurrentVersion
	}
	if version <= 0 {
		return in, fmt.Errorf("element %q has no published version yet", e.Slug)
	}
	if _, err := loadElementVersion(db, elementID, version, false); err != nil {
		return in, err
	}
	in.ElementID, in.ElementVersion = elementID, version
	return in, nil
}

// ─── Member-facing element calls ─────────────────────────────────

// lessonElementBlock loads a block of the lesson and checks the member (when
// given) belongs to the lesson's community.
func lessonElementBlock(ctx *sdk.AppCtx, lessonID, blockID, memberID string) (Lesson, LessonBlock, error) {
	db := ctx.AppDB()
	lesson, _, err := ensureLessonVisible(ctx, db, lessonID)
	if err != nil {
		return lesson, LessonBlock{}, err
	}
	if memberID != "" {
		var community string
		if err := db.QueryRow(`SELECT community_id FROM members WHERE id = ?`, memberID).Scan(&community); err != nil {
			return lesson, LessonBlock{}, notFound(err, "member")
		}
		if community != lesson.CommunityID {
			return lesson, LessonBlock{}, errors.New("lesson and member belong to different communities")
		}
	}
	blocks, err := listLessonBlocks(db, lessonID)
	if err != nil {
		return lesson, LessonBlock{}, err
	}
	for _, b := range blocks {
		if b.ID == blockID {
			if b.Type != "element" {
				return lesson, b, errors.New("block is not an element block")
			}
			return lesson, b, nil
		}
	}
	return lesson, LessonBlock{}, fmt.Errorf("block %q not found in lesson", blockID)
}

func toolLessonElementSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return nil, err
	}
	elementID, err := mustStr(args, "element_id")
	if err != nil {
		return nil, err
	}
	version, ok := intArg(args, "version")
	if !ok {
		return nil, errors.New("version is required")
	}
	db := ctx.AppDB()
	if _, _, err := ensureLessonVisible(ctx, db, lessonID); err != nil {
		return nil, err
	}
	var used int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lesson_blocks WHERE lesson_id = ? AND element_id = ? AND element_version = ?`,
		lessonID, elementID, version).Scan(&used); err != nil {
		return nil, err
	}
	if used == 0 {
		return nil, errors.New("element version is not used by this lesson")
	}
	v, err := loadElementVersion(db, elementID, version, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"element_id": elementID, "version": version, "checksum": v.Checksum, "runners": v.Runners, "source": *v.Source}, nil
}

func toolElementStateSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, blockID, memberID, err := blockCallArgs(args)
	if err != nil {
		return nil, err
	}
	state, ok := args["state"]
	if !ok || state == nil {
		return nil, errors.New("state is required")
	}
	if _, isObj := state.(map[string]any); !isObj {
		return nil, errors.New("state must be an object")
	}
	b, err := json.Marshal(state)
	if err != nil {
		return nil, errors.New("state must be JSON")
	}
	if len(b) > elementStateLimit {
		return nil, fmt.Errorf("state must be at most %d bytes", elementStateLimit)
	}
	if _, _, err := lessonElementBlock(ctx, lessonID, blockID, memberID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO element_state (block_id, member_id, lesson_id, state_json, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(block_id, member_id) DO UPDATE SET state_json = excluded.state_json, updated_at = CURRENT_TIMESTAMP`,
		blockID, memberID, lessonID, string(b)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "bytes": len(b)}, nil
}

func toolLessonStepComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, blockID, memberID, err := blockCallArgs(args)
	if err != nil {
		return nil, err
	}
	step, err := mustStr(args, "step")
	if err != nil {
		return nil, err
	}
	if err := validateStep(step); err != nil {
		return nil, err
	}
	lesson, _, err := lessonElementBlock(ctx, lessonID, blockID, memberID)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lesson_steps WHERE block_id = ? AND member_id = ?`, blockID, memberID).Scan(&count); err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT OR IGNORE INTO lesson_steps (block_id, member_id, lesson_id, step) VALUES (?, ?, ?, ?)`, blockID, memberID, lessonID, step)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if count >= maxStepsPerBlock*2 {
			_, _ = db.Exec(`DELETE FROM lesson_steps WHERE block_id = ? AND member_id = ? AND step = ?`, blockID, memberID, step)
			return nil, errors.New("too many steps recorded for this block")
		}
		emit(ctx, "lesson.step_completed", map[string]any{
			"community_id": lesson.CommunityID, "lesson_id": lessonID, "block_id": blockID, "member_id": memberID, "step": step,
		})
	}
	steps, required, done, err := lessonStepStatus(db, lessonID, memberID)
	if err != nil {
		return nil, err
	}
	completed := false
	if required > 0 && done >= required {
		prog, ok, err := loadLessonProgress(db, lessonID, memberID)
		if err != nil {
			return nil, err
		}
		if !ok || prog.Status != "complete" {
			if _, err := toolLessonsMarkComplete(ctx, map[string]any{"lesson_id": lessonID, "member_id": memberID, "status": "complete"}); err != nil {
				return nil, err
			}
		}
		completed = true
	}
	return map[string]any{"steps": steps, "required": required, "done": done, "lesson_completed": completed}, nil
}

// lessonStepStatus returns the member's recorded steps per block and how many
// of the lesson's required steps are recorded.
func lessonStepStatus(db *sql.DB, lessonID, memberID string) (map[string][]string, int, int, error) {
	steps := map[string][]string{}
	rows, err := db.Query(`SELECT block_id, step FROM lesson_steps WHERE lesson_id = ? AND member_id = ? ORDER BY created_at`, lessonID, memberID)
	if err != nil {
		return nil, 0, 0, err
	}
	for rows.Next() {
		var block, step string
		if err := rows.Scan(&block, &step); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		steps[block] = append(steps[block], step)
	}
	rows.Close()
	blocks, err := listLessonBlocks(db, lessonID)
	if err != nil {
		return nil, 0, 0, err
	}
	required, done := 0, 0
	for _, b := range blocks {
		have := map[string]bool{}
		for _, s := range steps[b.ID] {
			have[s] = true
		}
		for _, s := range b.RequiredSteps {
			required++
			if have[s] {
				done++
			}
		}
	}
	return steps, required, done, nil
}

var runnerLimiter = struct {
	sync.Mutex
	calls map[string][]time.Time
}{calls: map[string][]time.Time{}}

func allowRunnerCall(memberID string, now time.Time) bool {
	runnerLimiter.Lock()
	defer runnerLimiter.Unlock()
	cutoff := now.Add(-time.Minute)
	recent := runnerLimiter.calls[memberID][:0]
	for _, t := range runnerLimiter.calls[memberID] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= runnerCallsPerMin {
		runnerLimiter.calls[memberID] = recent
		return false
	}
	runnerLimiter.calls[memberID] = append(recent, now)
	return true
}

func toolElementRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lessonID, blockID, memberID, err := blockCallArgs(args)
	if err != nil {
		return nil, err
	}
	runner, err := mustStr(args, "runner")
	if err != nil {
		return nil, err
	}
	input := map[string]any{}
	if v, ok := args["input"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("input must be an object")
		}
		input = m
	}
	if b, _ := json.Marshal(input); len(b) > runnerInputLimit {
		return nil, fmt.Errorf("input must be at most %d bytes", runnerInputLimit)
	}
	lesson, block, err := lessonElementBlock(ctx, lessonID, blockID, memberID)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, r := range block.Runners {
		if r == runner {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("this element version may not use runner %q", runner)
	}
	var app, tool string
	err = ctx.AppDB().QueryRow(`SELECT app, tool FROM element_runners WHERE community_id = ? AND name = ?`, lesson.CommunityID, runner).Scan(&app, &tool)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("runner %q is not set up for this community", runner)
	}
	if err != nil {
		return nil, err
	}
	if !allowRunnerCall(memberID, time.Now()) {
		return nil, errors.New("too many runs; wait a minute and try again")
	}
	slug := ""
	if block.ElementSlug != nil {
		slug = *block.ElementSlug
	}
	var out any
	if err := ctx.PlatformAPI().CallAppResult(app, tool, map[string]any{
		"runner": runner, "input": input, "element": slug, "version": *block.ElementVersion,
	}, &out); err != nil {
		return nil, fmt.Errorf("runner %q failed: %w", runner, err)
	}
	return map[string]any{"runner": runner, "result": out}, nil
}

func blockCallArgs(args map[string]any) (string, string, string, error) {
	lessonID, err := mustStr(args, "lesson_id")
	if err != nil {
		return "", "", "", err
	}
	blockID, err := mustStr(args, "block_id")
	if err != nil {
		return "", "", "", err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return "", "", "", err
	}
	return lessonID, blockID, memberID, nil
}

// lessonInteractiveBundle is merged into lesson_bundle_get.
func lessonInteractiveBundle(db *sql.DB, lessonID, memberID string) (map[string]any, error) {
	layout, err := lessonLayout(db, lessonID)
	if err != nil {
		return nil, err
	}
	blocks, err := listLessonBlocks(db, lessonID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"layout": layout, "blocks": blocks}
	if memberID == "" || len(blocks) == 0 {
		return out, nil
	}
	state := map[string]json.RawMessage{}
	rows, err := db.Query(`SELECT block_id, state_json FROM element_state WHERE lesson_id = ? AND member_id = ?`, lessonID, memberID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var block, raw string
		if err := rows.Scan(&block, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		state[block] = rawJSON(raw, "{}")
	}
	rows.Close()
	steps, _, _, err := lessonStepStatus(db, lessonID, memberID)
	if err != nil {
		return nil, err
	}
	out["element_state"] = state
	out["steps"] = steps
	return out, nil
}

// ─── Runners ─────────────────────────────────────────────────────

type ElementRunner struct {
	Name string `json:"name"`
	App  string `json:"app"`
	Tool string `json:"tool"`
}

func toolElementRunnersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT name, app, tool FROM element_runners WHERE community_id = ? ORDER BY name`, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ElementRunner{}
	for rows.Next() {
		var r ElementRunner
		if err := rows.Scan(&r.Name, &r.App, &r.Tool); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return map[string]any{"runners": out}, rows.Err()
}

func toolElementRunnersSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	raw, _ := args["runners"].([]any)
	if len(raw) > 20 {
		return nil, errors.New("at most 20 runners")
	}
	runners := make([]ElementRunner, 0, len(raw))
	seen := map[string]bool{}
	for i, item := range raw {
		m, _ := item.(map[string]any)
		r := ElementRunner{Name: strArg(m, "name", ""), App: strArg(m, "app", ""), Tool: strArg(m, "tool", "")}
		if !runnerNameRe.MatchString(r.Name) {
			return nil, fmt.Errorf("runners[%d]: invalid name", i)
		}
		if r.App == "" || r.Tool == "" {
			return nil, fmt.Errorf("runners[%d]: app and tool are required", i)
		}
		if r.App == "community" {
			return nil, fmt.Errorf("runners[%d]: a runner cannot call Community itself", i)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("runners[%d]: duplicate name %q", i, r.Name)
		}
		seen[r.Name] = true
		runners = append(runners, r)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM element_runners WHERE community_id = ?`, communityID); err != nil {
		return nil, err
	}
	for _, r := range runners {
		if _, err := tx.Exec(`INSERT INTO element_runners (community_id, name, app, tool) VALUES (?, ?, ?, ?)`, communityID, r.Name, r.App, r.Tool); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"runners": runners}, nil
}

// ─── Export / import ─────────────────────────────────────────────

type packageElement struct {
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Version        int64           `json:"version"`
	Checksum       string          `json:"checksum"`
	Runners        []string        `json:"runners"`
	SettingsSchema json.RawMessage `json:"settings_schema"`
	HTML           string          `json:"html"`
}

type packageBlock struct {
	Type           string          `json:"type"`
	Slot           string          `json:"slot"`
	Title          string          `json:"title,omitempty"`
	ElementSlug    string          `json:"element,omitempty"`
	ElementVersion int64           `json:"element_version,omitempty"`
	Settings       json.RawMessage `json:"settings"`
	RequiredSteps  []string        `json:"required_steps,omitempty"`
	Height         *int64          `json:"height,omitempty"`
}

type packageQuiz struct {
	Title        string `json:"title"`
	Questions    any    `json:"questions"`
	PassingScore int64  `json:"passing_score"`
}

type packageAssignment struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	DueAfterDays *int64 `json:"due_after_days,omitempty"`
}

type packageLesson struct {
	Title          string              `json:"title"`
	Body           string              `json:"body"`
	Layout         string              `json:"layout"`
	Published      bool                `json:"published"`
	PreviewEnabled bool                `json:"preview_enabled"`
	Blocks         []packageBlock      `json:"blocks"`
	Quizzes        []packageQuiz       `json:"quizzes"`
	Assignments    []packageAssignment `json:"assignments"`
}

type packageSection struct {
	Title   string          `json:"title"`
	Lessons []packageLesson `json:"lessons"`
}

type coursePackage struct {
	Format   string `json:"format"`
	Version  int    `json:"version"`
	Exported string `json:"exported_at"`
	Course   struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Summary     string `json:"summary"`
		Description string `json:"description"`
	} `json:"course"`
	Elements []packageElement `json:"elements"`
	Sections []packageSection `json:"sections"`
	Notes    []string         `json:"notes,omitempty"`
}

func toolCourseExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := requireCourseSpace(ctx, db, spaceID); err != nil {
		return nil, err
	}
	var pkg coursePackage
	pkg.Format, pkg.Version, pkg.Exported = coursePackageFormat, 1, time.Now().UTC().Format(time.RFC3339)
	if err := db.QueryRow(`SELECT slug, name FROM spaces WHERE id = ?`, spaceID).Scan(&pkg.Course.Slug, &pkg.Course.Name); err != nil {
		return nil, notFound(err, "course")
	}
	_ = db.QueryRow(`SELECT COALESCE(summary, ''), COALESCE(description, '') FROM course_details WHERE space_id = ?`, spaceID).
		Scan(&pkg.Course.Summary, &pkg.Course.Description)
	type elemKey struct {
		id      string
		version int64
	}
	elements := map[elemKey]bool{}
	secRows, err := db.Query(`SELECT id, title FROM sections WHERE space_id = ? ORDER BY position, created_at`, spaceID)
	if err != nil {
		return nil, err
	}
	type sec struct{ id, title string }
	var sections []sec
	for secRows.Next() {
		var s sec
		if err := secRows.Scan(&s.id, &s.title); err != nil {
			secRows.Close()
			return nil, err
		}
		sections = append(sections, s)
	}
	secRows.Close()
	resourceCount := 0
	videoCount := 0
	for _, s := range sections {
		ps := packageSection{Title: s.title, Lessons: []packageLesson{}}
		lessonRows, err := db.Query(`SELECT id, title, body, layout, published_at IS NOT NULL, preview_enabled, video_storage_key IS NOT NULL
			FROM lessons WHERE section_id = ? ORDER BY position, created_at`, s.id)
		if err != nil {
			return nil, err
		}
		type les struct {
			id    string
			pl    packageLesson
			video bool
		}
		var lessons []les
		for lessonRows.Next() {
			var l les
			if err := lessonRows.Scan(&l.id, &l.pl.Title, &l.pl.Body, &l.pl.Layout, &l.pl.Published, &l.pl.PreviewEnabled, &l.video); err != nil {
				lessonRows.Close()
				return nil, err
			}
			lessons = append(lessons, l)
		}
		lessonRows.Close()
		for _, l := range lessons {
			if l.video {
				videoCount++
			}
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM lesson_resources WHERE lesson_id = ?`, l.id).Scan(&n)
			resourceCount += n
			blocks, err := listLessonBlocks(db, l.id)
			if err != nil {
				return nil, err
			}
			l.pl.Blocks = []packageBlock{}
			for _, b := range blocks {
				pb := packageBlock{Type: b.Type, Slot: b.Slot, Title: b.Title, Settings: b.Settings, RequiredSteps: b.RequiredSteps, Height: b.Height}
				if b.ElementID != nil && b.ElementVersion != nil {
					pb.ElementSlug, pb.ElementVersion = derefString(b.ElementSlug), *b.ElementVersion
					elements[elemKey{*b.ElementID, *b.ElementVersion}] = true
				}
				l.pl.Blocks = append(l.pl.Blocks, pb)
			}
			quizzes, err := listQuizzes(db, l.id)
			if err != nil {
				return nil, err
			}
			l.pl.Quizzes = []packageQuiz{}
			for _, q := range quizzes.(map[string]any)["quizzes"].([]Quiz) {
				l.pl.Quizzes = append(l.pl.Quizzes, packageQuiz{Title: q.Title, Questions: q.Questions, PassingScore: q.PassingScore})
			}
			assignments, err := listAssignments(db, l.id)
			if err != nil {
				return nil, err
			}
			l.pl.Assignments = []packageAssignment{}
			for _, a := range assignments.(map[string]any)["assignments"].([]Assignment) {
				l.pl.Assignments = append(l.pl.Assignments, packageAssignment{Title: a.Title, Instructions: a.Instructions, DueAfterDays: a.DueAfterDays})
			}
			ps.Lessons = append(ps.Lessons, l.pl)
		}
		pkg.Sections = append(pkg.Sections, ps)
	}
	pkg.Elements = []packageElement{}
	for key := range elements {
		e, err := loadElement(db, key.id, false)
		if err != nil {
			return nil, err
		}
		v, err := loadElementVersion(db, key.id, key.version, true)
		if err != nil {
			return nil, err
		}
		pkg.Elements = append(pkg.Elements, packageElement{
			Slug: e.Slug, Name: e.Name, Description: e.Description, Version: v.Version, Checksum: v.Checksum,
			Runners: v.Runners, SettingsSchema: v.SettingsSchema, HTML: *v.Source,
		})
	}
	if videoCount > 0 {
		pkg.Notes = append(pkg.Notes, fmt.Sprintf("%d lesson video(s) are Storage files and are not included; re-attach them after import.", videoCount))
	}
	if resourceCount > 0 {
		pkg.Notes = append(pkg.Notes, fmt.Sprintf("%d lesson resource(s) are Storage files and are not included.", resourceCount))
	}
	if pkg.Sections == nil {
		pkg.Sections = []packageSection{}
	}
	return pkg, nil
}

func toolCourseImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args["package"])
	if err != nil {
		return nil, errors.New("package must be JSON")
	}
	var pkg coursePackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("package is not a course export: %w", err)
	}
	if pkg.Format != coursePackageFormat || pkg.Version != 1 {
		return nil, errors.New("package is not a version 1 course export")
	}
	slug := strArg(args, "slug", pkg.Course.Slug)
	name := strArg(args, "name", pkg.Course.Name)
	// Resolve elements first so a bad package fails before a course exists.
	type key struct {
		slug    string
		version int64
	}
	resolved := map[key][2]any{}
	for _, pe := range pkg.Elements {
		if !elementSlugRe.MatchString(pe.Slug) || pe.HTML == "" || len(pe.HTML) > elementSourceLimit {
			return nil, fmt.Errorf("package element %q is invalid", pe.Slug)
		}
		if checksumOf(pe.HTML) != pe.Checksum {
			return nil, fmt.Errorf("package element %q checksum does not match its html", pe.Slug)
		}
	}
	for _, pe := range pkg.Elements {
		elementID, version, err := importElement(ctx, db, communityID, pe)
		if err != nil {
			return nil, fmt.Errorf("element %q: %w", pe.Slug, err)
		}
		resolved[key{pe.Slug, pe.Version}] = [2]any{elementID, version}
	}
	course, err := toolCoursesCreate(ctx, map[string]any{"community_id": communityID, "slug": slug, "name": name})
	if err != nil {
		return nil, err
	}
	spaceID := course.(Space).ID
	if pkg.Course.Summary != "" || pkg.Course.Description != "" {
		if _, err := toolCoursesUpdateDetails(ctx, map[string]any{"space_id": spaceID, "summary": pkg.Course.Summary, "description": pkg.Course.Description}); err != nil {
			return nil, err
		}
	}
	lessonCount := 0
	for _, ps := range pkg.Sections {
		sec, err := toolSectionsCreate(ctx, map[string]any{"space_id": spaceID, "title": ps.Title})
		if err != nil {
			return nil, err
		}
		sectionID := sec.(Section).ID
		for _, pl := range ps.Lessons {
			created, err := toolLessonsCreate(ctx, map[string]any{"section_id": sectionID, "title": pl.Title, "body": pl.Body})
			if err != nil {
				return nil, err
			}
			lessonID := created.(Lesson).ID
			lessonCount++
			blocks := make([]any, 0, len(pl.Blocks))
			for _, pb := range pl.Blocks {
				var settings map[string]any
				_ = json.Unmarshal(pb.Settings, &settings)
				b := map[string]any{"type": pb.Type, "slot": pb.Slot, "title": pb.Title, "settings": settings, "required_steps": anySlice(pb.RequiredSteps)}
				if pb.Height != nil {
					b["height"] = *pb.Height
				}
				if pb.Type == "element" {
					r, ok := resolved[key{pb.ElementSlug, pb.ElementVersion}]
					if !ok {
						return nil, fmt.Errorf("lesson %q uses element %s@%d missing from the package", pl.Title, pb.ElementSlug, pb.ElementVersion)
					}
					b["element_id"], b["element_version"] = r[0], r[1]
				}
				blocks = append(blocks, b)
			}
			layout := pl.Layout
			if !lessonLayouts[layout] {
				layout = "article"
			}
			if _, err := toolLessonBlocksSet(ctx, map[string]any{"lesson_id": lessonID, "layout": layout, "blocks": blocks}); err != nil {
				return nil, fmt.Errorf("lesson %q: %w", pl.Title, err)
			}
			for _, q := range pl.Quizzes {
				qa := map[string]any{"lesson_id": lessonID, "title": q.Title, "questions": q.Questions}
				if q.PassingScore > 0 {
					qa["passing_score"] = q.PassingScore
				}
				if _, err := toolQuizzesCreate(ctx, qa); err != nil {
					return nil, fmt.Errorf("lesson %q quiz: %w", pl.Title, err)
				}
			}
			for _, a := range pl.Assignments {
				aa := map[string]any{"lesson_id": lessonID, "title": a.Title, "instructions": a.Instructions}
				if a.DueAfterDays != nil {
					aa["due_after_days"] = *a.DueAfterDays
				}
				if _, err := toolAssignmentsCreate(ctx, aa); err != nil {
					return nil, fmt.Errorf("lesson %q assignment: %w", pl.Title, err)
				}
			}
			if pl.PreviewEnabled {
				if _, err := toolLessonsUpdate(ctx, map[string]any{"id": lessonID, "preview_enabled": true}); err != nil {
					return nil, err
				}
			}
			if pl.Published {
				if _, err := toolLessonsPublish(ctx, map[string]any{"id": lessonID, "published": true}); err != nil {
					return nil, err
				}
			}
		}
	}
	return map[string]any{"space_id": spaceID, "slug": slug, "sections": len(pkg.Sections), "lessons": lessonCount, "elements": len(pkg.Elements), "notes": pkg.Notes}, nil
}

// importElement reuses a matching version (same slug and checksum), else
// publishes the package html as a new version of the existing or a new element.
func importElement(ctx *sdk.AppCtx, db *sql.DB, communityID string, pe packageElement) (string, int64, error) {
	var elementID string
	err := db.QueryRow(`SELECT id FROM course_elements WHERE community_id = ? AND slug = ?`, communityID, pe.Slug).Scan(&elementID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	var schema map[string]any
	_ = json.Unmarshal(pe.SettingsSchema, &schema)
	if elementID != "" {
		var version int64
		err := db.QueryRow(`SELECT version FROM course_element_versions WHERE element_id = ? AND checksum = ? ORDER BY version DESC LIMIT 1`, elementID, pe.Checksum).Scan(&version)
		if err == nil {
			return elementID, version, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", 0, err
		}
		if _, err := toolCourseElementsSave(ctx, map[string]any{"id": elementID, "html": pe.HTML, "runners": anySlice(pe.Runners), "settings_schema": schema}); err != nil {
			return "", 0, err
		}
	} else {
		name := pe.Name
		if name == "" {
			name = pe.Slug
		}
		out, err := toolCourseElementsSave(ctx, map[string]any{
			"community_id": communityID, "slug": pe.Slug, "name": name, "description": pe.Description,
			"html": pe.HTML, "runners": anySlice(pe.Runners), "settings_schema": schema,
		})
		if err != nil {
			return "", 0, err
		}
		elementID = out.(CourseElement).ID
	}
	e, err := loadElement(db, elementID, true)
	if err != nil {
		return "", 0, err
	}
	version, _, err := publishElementDraft(db, e)
	return elementID, version, err
}

// ─── Helpers ─────────────────────────────────────────────────────

func checksumOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func decodeStrings(raw string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func encodeJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func rawJSON(raw, def string) json.RawMessage {
	if strings.TrimSpace(raw) == "" || !json.Valid([]byte(raw)) {
		return json.RawMessage(def)
	}
	return json.RawMessage(raw)
}

func anySlice(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stringListArg(args map[string]any, key string) ([]string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return []string{}, nil
	}
	switch list := v.(type) {
	case []string:
		return list, nil
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of strings", key)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s must be a list of strings", key)
}

// runnerNamesArg returns nil when the key is absent.
func runnerNamesArg(args map[string]any, key string) ([]string, error) {
	if _, ok := args[key]; !ok {
		return nil, nil
	}
	list, err := stringListArg(args, key)
	if err != nil {
		return nil, err
	}
	if len(list) > 10 {
		return nil, errors.New("at most 10 runners")
	}
	for _, name := range list {
		if !runnerNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid runner name %q", name)
		}
	}
	return list, nil
}

func validateStep(step string) error {
	if step == "" || len(step) > maxStepLength || !stepRe.MatchString(step) {
		return fmt.Errorf("step %q must be 1-%d letters, digits, or _.:-", step, maxStepLength)
	}
	return nil
}
