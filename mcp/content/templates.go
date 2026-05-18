// Site templates — pre-built content kits (Finance Blog, SaaS
// Marketing, Docs Site, …) that scaffold a fresh install in one apply.
//
// The catalog lives in the `templates` table (migration 002). Bundled
// templates ship inside the binary via //go:embed and get UPSERTed
// into the table at boot per project. User-imported templates (source
// != 'bundled') land in the same table.
//
// Apply walks the parsed body inside a single transaction, creating
// settings → terms → pages (parents first) → posts (with term joins)
// → menus (with slug-to-id resolution) → homepage pin. A partial
// failure rolls back everything so the install never ends up in a
// half-populated state.

package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"gopkg.in/yaml.v3"
)

//go:embed templates_bundled
var embeddedTemplatesFS embed.FS

// Template is one row of the templates catalog. Body holds the full
// YAML definition; the other fields are the parsed metadata header
// the panel + agent use for discovery.
type Template struct {
	ID           int64    `json:"id"`
	ProjectID    string   `json:"project_id,omitempty"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"display_name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Tags         []string `json:"tags"`
	PreviewImage string   `json:"preview_image,omitempty"`
	Source       string   `json:"source"`
	Body         string   `json:"body,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

// TemplateBody is the parsed shape of the YAML body. Pages and posts
// share the same shape but go to different `kind` values in posts.
type TemplateBody struct {
	Schema        string                 `yaml:"schema"`
	Name          string                 `yaml:"name"`
	DisplayName   string                 `yaml:"display_name"`
	Version       string                 `yaml:"version"`
	Description   string                 `yaml:"description"`
	Tags          []string               `yaml:"tags"`
	PreviewImage  string                 `yaml:"preview_image"`
	RequiresApps  []string               `yaml:"requires_apps"`
	OptionalApps  []string               `yaml:"optional_apps"`
	Settings      map[string]string      `yaml:"settings"`
	Terms         []TemplateTerm         `yaml:"terms"`
	Pages         []TemplatePost         `yaml:"pages"`
	Posts         []TemplatePost         `yaml:"posts"`
	Menus         []TemplateMenu         `yaml:"menus"`
	HomepageSlug  string                 `yaml:"homepage_slug"`
	Redirects     []TemplateRedirect     `yaml:"redirects"`
}

type TemplateTerm struct {
	Kind        string `yaml:"kind"`
	Name        string `yaml:"name"`
	Slug        string `yaml:"slug"`
	Description string `yaml:"description"`
}

type TemplatePost struct {
	Slug       string   `yaml:"slug"`
	Title      string   `yaml:"title"`
	Excerpt    string   `yaml:"excerpt"`
	Terms      []string `yaml:"terms"`
	ParentSlug string   `yaml:"parent_slug"`
	Template   string   `yaml:"template"`
	Blocks     []Block  `yaml:"blocks"`
}

type TemplateMenu struct {
	Slug  string             `yaml:"slug"`
	Name  string             `yaml:"name"`
	Items []TemplateMenuItem `yaml:"items"`
}

type TemplateMenuItem struct {
	Label       string             `yaml:"label"`
	TargetKind  string             `yaml:"target_kind"`
	TargetSlug  string             `yaml:"target_slug"`
	TargetURL   string             `yaml:"target_url"`
	Children    []TemplateMenuItem `yaml:"children"`
}

type TemplateRedirect struct {
	From string `yaml:"from_path"`
	To   string `yaml:"to_path"`
	Code int    `yaml:"code"`
}

const TemplateSchemaCurrent = "apteva-content-template/v1"

// ── DB layer ─────────────────────────────────────────────────────

func dbUpsertTemplate(db *sql.DB, projectID string, t Template) (*Template, error) {
	tagsJSON, _ := json.Marshal(t.Tags)
	_, err := db.Exec(`
		INSERT INTO templates (project_id, name, display_name, version, description,
			tags, preview_image, source, body, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, name) DO UPDATE SET
			display_name = excluded.display_name,
			version      = excluded.version,
			description  = excluded.description,
			tags         = excluded.tags,
			preview_image = excluded.preview_image,
			source       = excluded.source,
			body         = excluded.body,
			updated_at   = CURRENT_TIMESTAMP`,
		projectID, t.Name, t.DisplayName, t.Version, t.Description,
		string(tagsJSON), t.PreviewImage, t.Source, t.Body)
	if err != nil {
		return nil, fmt.Errorf("upsert template %q: %w", t.Name, err)
	}
	return dbGetTemplate(db, projectID, t.Name)
}

func dbGetTemplate(db *sql.DB, projectID, name string) (*Template, error) {
	row := db.QueryRow(`SELECT id, project_id, name, display_name, version, description,
		tags, preview_image, source, body, created_at, updated_at
		FROM templates WHERE project_id=? AND name=?`, projectID, name)
	return scanTemplate(row, true)
}

func dbListTemplates(db *sql.DB, projectID, sourceFilter, tagFilter string) ([]Template, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if sourceFilter != "" {
		where = append(where, "source = ?")
		args = append(args, sourceFilter)
	}
	if tagFilter != "" {
		// SQLite has no good JSON-array contains; LIKE is good enough
		// for v1 (tags are a small allow-list).
		where = append(where, "tags LIKE ?")
		args = append(args, `%"`+tagFilter+`"%`)
	}
	rows, err := db.Query(`SELECT id, project_id, name, display_name, version, description,
		tags, preview_image, source, body, created_at, updated_at
		FROM templates WHERE `+strings.Join(where, " AND ")+` ORDER BY display_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		t, err := scanTemplate(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

func dbDeleteTemplate(db *sql.DB, projectID, name string) error {
	res, err := db.Exec(`DELETE FROM templates WHERE project_id=? AND name=? AND source != 'bundled'`,
		projectID, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("template %q not found or is bundled (bundled templates cannot be removed)", name)
	}
	return nil
}

// scanTemplate scans one row. When includeBody=false, the body column
// is still loaded but list responses can drop it via the caller; we
// keep it always loaded for simplicity since templates are small.
func scanTemplate(row rowScanner, _ bool) (*Template, error) {
	var t Template
	var tagsJSON, created, updated sql.NullString
	if err := row.Scan(&t.ID, &t.ProjectID, &t.Name, &t.DisplayName, &t.Version, &t.Description,
		&tagsJSON, &t.PreviewImage, &t.Source, &t.Body, &created, &updated); err != nil {
		return nil, err
	}
	if tagsJSON.Valid && tagsJSON.String != "" {
		_ = json.Unmarshal([]byte(tagsJSON.String), &t.Tags)
	}
	if created.Valid {
		t.CreatedAt = created.String
	}
	if updated.Valid {
		t.UpdatedAt = updated.String
	}
	return &t, nil
}

// ── boot-time seeding ────────────────────────────────────────────
//
// Walks the embedded templates_bundled/*.yaml and UPSERTs each into
// the templates table for the supplied project. Idempotent: re-running
// on the same install just refreshes the rows if the version differs.
//
// For project-scoped installs the caller passes APTEVA_PROJECT_ID. For
// global installs the caller iterates over ListProjects.

func seedBundledTemplates(ctx *sdk.AppCtx, projectID string) error {
	if projectID == "" {
		return errors.New("seedBundledTemplates: projectID required")
	}
	var seeded int
	err := fs.WalkDir(embeddedTemplatesFS, "templates_bundled", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		body, err := fs.ReadFile(embeddedTemplatesFS, p)
		if err != nil {
			return err
		}
		var meta TemplateBody
		if err := yaml.Unmarshal(body, &meta); err != nil {
			ctx.Logger().Warn("template parse failed", "file", p, "err", err.Error())
			return nil // skip; don't block boot
		}
		if meta.Schema != TemplateSchemaCurrent {
			ctx.Logger().Warn("template schema mismatch", "file", p, "got", meta.Schema, "want", TemplateSchemaCurrent)
			return nil
		}
		if meta.Name == "" || meta.Version == "" {
			ctx.Logger().Warn("template missing name or version", "file", p)
			return nil
		}
		if _, err := dbUpsertTemplate(ctx.AppDB(), projectID, Template{
			Name:         meta.Name,
			DisplayName:  firstNonEmpty(meta.DisplayName, meta.Name),
			Version:      meta.Version,
			Description:  strings.TrimSpace(meta.Description),
			Tags:         meta.Tags,
			PreviewImage: meta.PreviewImage,
			Source:       "bundled",
			Body:         string(body),
		}); err != nil {
			ctx.Logger().Warn("template upsert failed", "name", meta.Name, "err", err.Error())
			return nil
		}
		seeded++
		return nil
	})
	if err == nil {
		ctx.Logger().Info("seeded bundled templates", "count", seeded, "project_id", projectID)
	}
	return err
}

// ── apply ────────────────────────────────────────────────────────

type ApplyMode string

const (
	ApplyEmptyOnly ApplyMode = "empty_only"
	ApplyAppend    ApplyMode = "append"
	ApplyOverwrite ApplyMode = "overwrite"
)

// ApplySummary is the return shape of templates_apply + templates_preview.
//
// WouldRefuse / RefuseReason / ExistingCount are populated by the
// dry-run path so the panel can warn the user *before* they click
// Apply — preview itself never errors on the empty_only guard; only
// a real apply does.
type ApplySummary struct {
	Template       string         `json:"template"`
	Version        string         `json:"version"`
	Mode           string         `json:"mode"`
	Created        map[string]int `json:"created"`
	Skipped        map[string]int `json:"skipped"`
	HomepagePinned bool           `json:"homepage_pinned"`
	Warnings       []string       `json:"warnings,omitempty"`
	DryRun         bool           `json:"dry_run,omitempty"`
	WouldRefuse    bool           `json:"would_refuse,omitempty"`
	RefuseReason   string         `json:"refuse_reason,omitempty"`
	ExistingCount  int            `json:"existing_count,omitempty"`
}

// applyTemplate is the workhorse — opens a tx, walks the body, returns
// a summary. When dryRun is true it inspects but never writes. The
// template applies to the supplied siteID (multi-site v2.0); the
// templates catalog itself is per-project (not site-scoped).
func applyTemplate(ctx *sdk.AppCtx, projectID string, siteID int64, name string, mode ApplyMode, dryRun bool) (*ApplySummary, error) {
	if mode == "" {
		mode = ApplyEmptyOnly
	}
	switch mode {
	case ApplyEmptyOnly, ApplyAppend, ApplyOverwrite:
	default:
		return nil, fmt.Errorf("unknown mode %q (use empty_only | append | overwrite)", mode)
	}

	t, err := dbGetTemplate(ctx.AppDB(), projectID, name)
	if err == sql.ErrNoRows || t == nil {
		return nil, fmt.Errorf("template %q not found", name)
	}
	if err != nil {
		return nil, err
	}

	var body TemplateBody
	if err := yaml.Unmarshal([]byte(t.Body), &body); err != nil {
		return nil, fmt.Errorf("parse template body: %w", err)
	}

	summary := &ApplySummary{
		Template: t.Name,
		Version:  t.Version,
		Mode:     string(mode),
		Created:  map[string]int{},
		Skipped:  map[string]int{},
		DryRun:   dryRun,
	}

	for _, role := range body.RequiresApps {
		if ctx.IntegrationFor(role) == nil {
			return nil, fmt.Errorf("template requires the %q role to be bound; install/bind that app first", role)
		}
	}

	existing, _ := countExisting(ctx.AppDB(), projectID, siteID)

	if dryRun {
		// Walk the body and report what would be created without
		// touching the DB. Annotate would_refuse so the panel can
		// show a warning before the user clicks Apply.
		summary.Created["pages"] = len(body.Pages)
		summary.Created["posts"] = len(body.Posts)
		summary.Created["terms"] = len(body.Terms)
		summary.Created["menus"] = len(body.Menus)
		summary.Created["settings"] = len(body.Settings)
		summary.Created["redirects"] = len(body.Redirects)
		if body.HomepageSlug != "" {
			summary.HomepagePinned = true
		}
		summary.ExistingCount = existing
		if mode == ApplyEmptyOnly && existing > 0 {
			summary.WouldRefuse = true
			summary.RefuseReason = fmt.Sprintf("Mode 'empty_only' refuses on populated sites — %d existing posts/pages/menus/terms. Switch to append or overwrite.", existing)
		}
		return summary, nil
	}

	// Real apply — the empty_only guard blocks writes here.
	if mode == ApplyEmptyOnly && existing > 0 {
		return nil, fmt.Errorf("install is not empty (%d posts/pages/menus/terms exist); switch mode to append or overwrite to proceed", existing)
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. Settings — site-scoped UPSERT.
	for k, v := range body.Settings {
		if _, err := tx.Exec(`INSERT INTO settings (project_id, site_id, key, value) VALUES (?, ?, ?, ?)
			ON CONFLICT(project_id, site_id, key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
			projectID, siteID, k, v); err != nil {
			return nil, fmt.Errorf("settings %q: %w", k, err)
		}
		summary.Created["settings"]++
	}

	// 2. Terms — build slug→id map for menu items + post joins.
	termIDs := map[string]int64{} // key: kind+"/"+slug
	for _, term := range body.Terms {
		slug := term.Slug
		if slug == "" {
			slug = slugify(term.Name)
		}
		var existing int64
		lerr := tx.QueryRow(`SELECT id FROM terms WHERE project_id=? AND site_id=? AND kind=? AND slug=?`,
			projectID, siteID, term.Kind, slug).Scan(&existing)
		switch {
		case lerr == nil && mode == ApplyOverwrite:
			if _, err := tx.Exec(`UPDATE terms SET name=?, description=? WHERE id=?`,
				term.Name, term.Description, existing); err != nil {
				return nil, fmt.Errorf("update term %q: %w", slug, err)
			}
			termIDs[term.Kind+"/"+slug] = existing
			summary.Created["terms"]++
		case lerr == nil:
			termIDs[term.Kind+"/"+slug] = existing
			summary.Skipped["terms"]++
		case lerr == sql.ErrNoRows:
			res, err := tx.Exec(`INSERT INTO terms (project_id, site_id, kind, name, slug, description) VALUES (?, ?, ?, ?, ?, ?)`,
				projectID, siteID, term.Kind, term.Name, slug, term.Description)
			if err != nil {
				return nil, fmt.Errorf("insert term %q: %w", slug, err)
			}
			id, _ := res.LastInsertId()
			termIDs[term.Kind+"/"+slug] = id
			summary.Created["terms"]++
		default:
			return nil, fmt.Errorf("lookup term %q: %w", slug, lerr)
		}
	}

	// 3. Pages — process in source order; parent slugs come before
	//    children in well-formed templates. We resolve parent_slug
	//    against pages already inserted by this pass.
	pageIDs := map[string]int64{}
	for _, page := range body.Pages {
		var parentID *int64
		if page.ParentSlug != "" {
			if id, ok := pageIDs[page.ParentSlug]; ok {
				parentID = &id
			} else {
				summary.Warnings = append(summary.Warnings,
					fmt.Sprintf("page %q references parent %q which appears later in the template; placed at root", page.Slug, page.ParentSlug))
			}
		}
		created, skipped, id, err := upsertPostInTx(tx, projectID, siteID, "page", page, parentID, mode)
		if err != nil {
			return nil, err
		}
		if id > 0 {
			pageIDs[page.Slug] = id
		}
		if created {
			summary.Created["pages"]++
		}
		if skipped {
			summary.Skipped["pages"]++
		}
	}

	// 4. Posts — same upsert, plus term assignment.
	postIDs := map[string]int64{}
	for _, post := range body.Posts {
		created, skipped, id, err := upsertPostInTx(tx, projectID, siteID, "post", post, nil, mode)
		if err != nil {
			return nil, err
		}
		if id > 0 {
			postIDs[post.Slug] = id
			// Attach terms.
			for _, slug := range post.Terms {
				var tid int64
				if v, ok := termIDs["category/"+slug]; ok {
					tid = v
				} else if v, ok := termIDs["tag/"+slug]; ok {
					tid = v
				} else {
					summary.Warnings = append(summary.Warnings,
						fmt.Sprintf("post %q references term %q which isn't in the template; skipped", post.Slug, slug))
					continue
				}
				if _, err := tx.Exec(`INSERT OR IGNORE INTO post_terms (post_id, term_id) VALUES (?, ?)`, id, tid); err != nil {
					return nil, fmt.Errorf("post_terms %d/%d: %w", id, tid, err)
				}
			}
		}
		if created {
			summary.Created["posts"]++
		}
		if skipped {
			summary.Skipped["posts"]++
		}
	}

	// 5. Menus — replace items atomically per menu.
	for _, menu := range body.Menus {
		menuID, err := upsertMenuInTx(tx, projectID, siteID, menu.Slug, menu.Name)
		if err != nil {
			return nil, err
		}
		// Clear old items + insert new ones (matches dbSetMenuItems
		// semantics).
		if _, err := tx.Exec(`DELETE FROM menu_items WHERE menu_id=?`, menuID); err != nil {
			return nil, err
		}
		if err := insertMenuItemsInTx(tx, menuID, menu.Items, nil, 0, pageIDs, postIDs, termIDs, summary); err != nil {
			return nil, err
		}
		summary.Created["menus"]++
	}

	// 6. Redirects.
	for _, red := range body.Redirects {
		code := red.Code
		if code != 301 && code != 302 {
			code = 301
		}
		if _, err := tx.Exec(`INSERT INTO redirects (project_id, site_id, from_path, to_path, code)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(project_id, site_id, from_path) DO UPDATE SET to_path=excluded.to_path, code=excluded.code`,
			projectID, siteID, red.From, red.To, code); err != nil {
			return nil, fmt.Errorf("redirect %q: %w", red.From, err)
		}
		summary.Created["redirects"]++
	}

	// 7. Homepage pin.
	if body.HomepageSlug != "" {
		if id, ok := pageIDs[body.HomepageSlug]; ok {
			if _, err := tx.Exec(`INSERT INTO settings (project_id, site_id, key, value) VALUES (?, ?, 'homepage_page_id', ?)
				ON CONFLICT(project_id, site_id, key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
				projectID, siteID, fmt.Sprintf("%d", id)); err != nil {
				return nil, fmt.Errorf("set homepage: %w", err)
			}
			summary.HomepagePinned = true
		} else {
			summary.Warnings = append(summary.Warnings,
				fmt.Sprintf("homepage_slug %q didn't match any created page", body.HomepageSlug))
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	invalidatePageCache()
	return summary, nil
}

// upsertPostInTx handles a single page or post within an open
// transaction. Site-scoped: existing-row lookup includes site_id so
// the same slug can coexist across sites in the same project.
func upsertPostInTx(tx *sql.Tx, projectID string, siteID int64, kind string, p TemplatePost, parentID *int64, mode ApplyMode) (created bool, skipped bool, id int64, err error) {
	bodyJSON := emptyDocumentJSON()
	if len(p.Blocks) > 0 {
		doc := Document{Version: documentVersion, Blocks: p.Blocks}
		assignMissingIDs(doc.Blocks)
		enc, _ := json.Marshal(doc)
		bodyJSON = string(enc)
	}
	slug := p.Slug
	if slug == "" {
		slug = slugify(p.Title)
	}
	var existingID int64
	err = tx.QueryRow(`SELECT id FROM posts WHERE project_id=? AND site_id=? AND kind=? AND locale='en' AND slug=? AND deleted_at IS NULL`,
		projectID, siteID, kind, slug).Scan(&existingID)
	if err == sql.ErrNoRows {
		err = nil
		var parentVal any
		if parentID != nil {
			parentVal = *parentID
		}
		res, ierr := tx.Exec(`INSERT INTO posts (project_id, site_id, kind, slug, locale, status, title, excerpt, body_blocks, parent_id, template)
			VALUES (?, ?, ?, ?, 'en', 'published', ?, ?, ?, ?, ?)`,
			projectID, siteID, kind, slug, p.Title, p.Excerpt, bodyJSON, parentVal, p.Template)
		if ierr != nil {
			err = fmt.Errorf("insert %s %q: %w", kind, slug, ierr)
			return
		}
		id, _ = res.LastInsertId()
		now := nowStamp()
		_, _ = tx.Exec(`UPDATE posts SET published_at=? WHERE id=?`, now, id)
		created = true
		return
	}
	if err != nil {
		err = fmt.Errorf("lookup %s %q: %w", kind, slug, err)
		return
	}
	id = existingID
	if mode == ApplyOverwrite {
		var parentVal any
		if parentID != nil {
			parentVal = *parentID
		}
		if _, uerr := tx.Exec(`UPDATE posts SET title=?, excerpt=?, body_blocks=?, body_html='',
			parent_id=?, template=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			p.Title, p.Excerpt, bodyJSON, parentVal, p.Template, existingID); uerr != nil {
			err = fmt.Errorf("update %s %q: %w", kind, slug, uerr)
			return
		}
		created = true
	} else {
		skipped = true
	}
	return
}

func upsertMenuInTx(tx *sql.Tx, projectID string, siteID int64, slug, name string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM menus WHERE project_id=? AND site_id=? AND slug=?`, projectID, siteID, slug).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := tx.Exec(`INSERT INTO menus (project_id, site_id, slug, name) VALUES (?, ?, ?, ?)`, projectID, siteID, slug, name)
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
		return id, nil
	}
	if err != nil {
		return 0, err
	}
	// Existing: refresh name (cheap, lets templates re-apply rename).
	_, _ = tx.Exec(`UPDATE menus SET name=? WHERE id=?`, name, id)
	return id, nil
}

func insertMenuItemsInTx(tx *sql.Tx, menuID int64, items []TemplateMenuItem, parentID *int64, basePos int,
	pageIDs, postIDs map[string]int64, termIDs map[string]int64, summary *ApplySummary) error {
	for i, it := range items {
		pos := basePos + i + 1
		targetKind := defaultTargetKind(it.TargetKind)
		var targetID *int64
		var targetURL string
		switch targetKind {
		case "page":
			if id, ok := pageIDs[it.TargetSlug]; ok {
				targetID = &id
			} else {
				summary.Warnings = append(summary.Warnings,
					fmt.Sprintf("menu item %q references page %q not in template; rendered as #", it.Label, it.TargetSlug))
				targetURL = "#"
			}
		case "post":
			if id, ok := postIDs[it.TargetSlug]; ok {
				targetID = &id
			} else {
				summary.Warnings = append(summary.Warnings,
					fmt.Sprintf("menu item %q references post %q not in template; rendered as #", it.Label, it.TargetSlug))
				targetURL = "#"
			}
		case "term":
			if id, ok := termIDs["category/"+it.TargetSlug]; ok {
				targetID = &id
			} else if id, ok := termIDs["tag/"+it.TargetSlug]; ok {
				targetID = &id
			} else {
				summary.Warnings = append(summary.Warnings,
					fmt.Sprintf("menu item %q references term %q not in template; rendered as #", it.Label, it.TargetSlug))
				targetURL = "#"
			}
		case "url":
			targetURL = it.TargetURL
		}
		var parentVal, targetVal any
		if parentID != nil {
			parentVal = *parentID
		}
		if targetID != nil {
			targetVal = *targetID
		}
		res, err := tx.Exec(`INSERT INTO menu_items (menu_id, parent_id, label, target_kind, target_id, target_url, position)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			menuID, parentVal, it.Label, targetKind, targetVal, targetURL, pos)
		if err != nil {
			return fmt.Errorf("menu item %q: %w", it.Label, err)
		}
		childID, _ := res.LastInsertId()
		if len(it.Children) > 0 {
			if err := insertMenuItemsInTx(tx, menuID, it.Children, &childID, 0, pageIDs, postIDs, termIDs, summary); err != nil {
				return err
			}
		}
	}
	return nil
}

func countExisting(db *sql.DB, projectID string, siteID int64) (int, error) {
	var total int
	queries := []string{
		`SELECT COUNT(*) FROM posts WHERE project_id=? AND site_id=? AND deleted_at IS NULL`,
		`SELECT COUNT(*) FROM terms WHERE project_id=? AND site_id=?`,
		`SELECT COUNT(*) FROM menus WHERE project_id=? AND site_id=?`,
	}
	for _, q := range queries {
		var n int
		if err := db.QueryRow(q, projectID, siteID).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// ── time helper kept here so the file is self-contained ───────────
var _ = time.Now
