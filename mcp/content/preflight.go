package main

import (
	"database/sql"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// PreflightIssue is intentionally small and machine-readable so the same
// checks can power the dashboard, CI, and an agent's publish workflow.
type PreflightIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	PostID   int64  `json:"post_id,omitempty"`
	Slug     string `json:"slug,omitempty"`
}

func (a *App) toolSitesPreflight(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return runSitePreflight(ctx.AppDB(), pid, siteID)
}

func runSitePreflight(db *sql.DB, projectID string, siteID int64) (map[string]any, error) {
	issues := []PreflightIssue{}
	var title, baseURL string
	var hostname string
	_ = db.QueryRow(`SELECT value FROM settings WHERE project_id=? AND site_id=? AND key='site_title'`, projectID, siteID).Scan(&title)
	_ = db.QueryRow(`SELECT value FROM settings WHERE project_id=? AND site_id=? AND key='public_base_url'`, projectID, siteID).Scan(&baseURL)
	_ = db.QueryRow(`SELECT hostname FROM sites WHERE project_id=? AND id=?`, projectID, siteID).Scan(&hostname)
	if strings.TrimSpace(title) == "" {
		issues = append(issues, PreflightIssue{"error", "missing_site_title", "Add a site title.", 0, ""})
	}
	if strings.TrimSpace(baseURL) == "" {
		issues = append(issues, PreflightIssue{"warning", "missing_public_base_url", "Set public_base_url before sharing canonical links or a sitemap.", 0, ""})
	}
	if strings.TrimSpace(hostname) == "" {
		issues = append(issues, PreflightIssue{"warning", "no_hostname", "Connect a domain before sending a public prospect preview.", 0, ""})
	}
	rows, err := db.Query(`SELECT id,slug,title,status,body_blocks FROM posts WHERE project_id=? AND site_id=? AND deleted_at IS NULL`, projectID, siteID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var slug, postTitle, status, raw string
		if err := rows.Scan(&id, &slug, &postTitle, &status, &raw); err != nil {
			return nil, err
		}
		if strings.TrimSpace(postTitle) == "" {
			issues = append(issues, PreflightIssue{"error", "missing_title", "Page or post has no title.", id, slug})
		}
		if status == "published" {
			doc, err := parseDocument(raw)
			if err != nil || len(doc.Blocks) == 0 {
				issues = append(issues, PreflightIssue{"warning", "empty_body", "Published content has no body blocks.", id, slug})
			}
			walkBlocks(doc.Blocks, func(block *Block) {
				for _, value := range block.Attrs {
					text := strings.ToLower(asString(value))
					if strings.Contains(text, "replace_me") || strings.Contains(text, "lorem ipsum") || strings.Contains(text, "todo") || strings.Contains(text, "example.com") {
						issues = append(issues, PreflightIssue{"warning", "placeholder_content", "Published content still contains placeholder text.", id, slug})
						break
					}
				}
				if block.Type == "core/image" && strings.TrimSpace(asString(block.Attrs["alt"])) == "" {
					issues = append(issues, PreflightIssue{"warning", "missing_image_alt", "Image block is missing alt text.", id, slug})
				}
				if block.Type == "core/form" {
					if len(block.Attrs) == 0 {
						issues = append(issues, PreflightIssue{"error", "empty_form", "Form block has no configuration.", id, slug})
					} else if actions, ok := block.Attrs["actions"].([]any); !ok || len(actions) == 0 {
						issues = append(issues, PreflightIssue{"warning", "form_without_action", "Form has no delivery action configured.", id, slug})
					}
				}
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	menuRows, err := db.Query(`SELECT mi.target_kind, mi.target_id, mi.label FROM menu_items mi JOIN menus m ON m.id=mi.menu_id WHERE m.project_id=? AND m.site_id=? AND mi.target_kind IN ('page','post','term')`, projectID, siteID)
	if err != nil {
		return nil, err
	}
	// AppDB is intentionally configured with a single SQLite connection. Do
	// not run the per-item existence query while menuRows is still open: the
	// nested QueryRow would wait forever for that same connection.
	type menuLink struct {
		kind   string
		target sql.NullInt64
		label  string
	}
	var menuLinks []menuLink
	for menuRows.Next() {
		var kind, label string
		var target sql.NullInt64
		if err := menuRows.Scan(&kind, &target, &label); err != nil {
			menuRows.Close()
			return nil, err
		}
		menuLinks = append(menuLinks, menuLink{kind: kind, target: target, label: label})
	}
	if err := menuRows.Err(); err != nil {
		menuRows.Close()
		return nil, err
	}
	menuRows.Close()
	for _, link := range menuLinks {
		kind, target, label := link.kind, link.target, link.label
		if !target.Valid {
			issues = append(issues, PreflightIssue{"error", "menu_missing_target", "Menu item has no target.", 0, label})
			continue
		}
		table := "posts"
		if kind == "term" {
			table = "terms"
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=? AND project_id=? AND site_id=?`, target.Int64, projectID, siteID).Scan(&count); err == nil && count == 0 {
			issues = append(issues, PreflightIssue{"error", "broken_menu_link", "Menu item points to missing content.", 0, label})
		}
	}
	errors, warnings := 0, 0
	for _, issue := range issues {
		if issue.Severity == "error" {
			errors++
		} else {
			warnings++
		}
	}
	return map[string]any{"ok": errors == 0, "errors": errors, "warnings": warnings, "issues": issues}, nil
}

func (a *App) handleHTTPPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := runSitePreflight(ctx.AppDB(), pid, siteID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, out)
}
