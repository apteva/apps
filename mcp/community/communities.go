// Community-level handlers — create / list / get.
//
// "communities" is the tenancy boundary: every other table joins back
// here via community_id. Slug is unique within (project_id), so callers
// can address a community by slug from outside the DB without juggling
// the random id.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type Community struct {
	ID                   string  `json:"id"`
	ProjectID            string  `json:"project_id"`
	Slug                 string  `json:"slug"`
	Name                 string  `json:"name"`
	Description          string  `json:"description"`
	AuthClientID         string  `json:"auth_client_id,omitempty"`
	AuthOrganizationID   string  `json:"auth_organization_id,omitempty"`
	AuthOrganizationSlug string  `json:"auth_organization_slug,omitempty"`
	BrandName            string  `json:"brand_name,omitempty"`
	LogoURL              string  `json:"logo_url,omitempty"`
	FaviconURL           string  `json:"favicon_url,omitempty"`
	PrimaryColor         string  `json:"primary_color"`
	AccentColor          string  `json:"accent_color"`
	SupportEmail         string  `json:"support_email,omitempty"`
	PortalHost           string  `json:"portal_host,omitempty"`
	PortalDNSManaged     bool    `json:"portal_dns_managed,omitempty"`
	PortalDNSDomain      string  `json:"portal_dns_domain,omitempty"`
	PortalDNSName        string  `json:"portal_dns_name,omitempty"`
	PortalDNSType        string  `json:"portal_dns_type,omitempty"`
	PortalDNSValue       string  `json:"portal_dns_value,omitempty"`
	PortalDomainError    string  `json:"portal_domain_error,omitempty"`
	SignupMode           string  `json:"signup_mode"`
	AutoCreateMembers    bool    `json:"auto_create_members"`
	CreatedAt            string  `json:"created_at"`
	ArchivedAt           *string `json:"archived_at,omitempty"`
}

var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func communitiesTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "communities_create",
			Description: "Create a community. Args: slug (required, url-safe), name (required), description?. Portal/Auth settings may be configured with communities_update.",
			InputSchema: schemaObject(map[string]any{
				"slug":        map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			Handler: toolCommunitiesCreate,
		},
		{
			Name:        "communities_list",
			Description: "List communities in this project scope. Archived hidden by default; pass include_archived=true to see them.",
			InputSchema: schemaObject(map[string]any{
				"include_archived": map[string]any{"type": "boolean"},
			}, nil),
			Handler: toolCommunitiesList,
		},
		{
			Name:        "communities_get",
			Description: "Fetch one community by id or slug. Args: id? or slug?. Exactly one must be set.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "string"},
				"slug": map[string]any{"type": "string"},
			}, nil),
			Handler: toolCommunitiesGet,
		},
		{
			Name:        "communities_update",
			Description: "Update a community, including its customer portal branding and per-community Auth binding.",
			InputSchema: schemaObject(map[string]any{
				"id":                     map[string]any{"type": "string"},
				"name":                   map[string]any{"type": "string"},
				"description":            map[string]any{"type": "string"},
				"auth_client_id":         map[string]any{"type": "string"},
				"auth_organization_id":   map[string]any{"type": "string"},
				"auth_organization_slug": map[string]any{"type": "string"},
				"brand_name":             map[string]any{"type": "string"},
				"logo_url":               map[string]any{"type": "string"},
				"favicon_url":            map[string]any{"type": "string"},
				"primary_color":          map[string]any{"type": "string"},
				"accent_color":           map[string]any{"type": "string"},
				"support_email":          map[string]any{"type": "string"},
				"portal_host":            map[string]any{"type": "string"},
				"signup_mode":            map[string]any{"type": "string", "enum": []string{"open", "closed"}},
				"auto_create_members":    map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: toolCommunitiesUpdate,
		},
		{
			Name:        "communities_archive",
			Description: "Soft-delete a community. Rows stay; default list filters it out. Args: id (required).",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolCommunitiesArchive,
		},
	}
}

func toolCommunitiesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, id); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["name"].(string); ok && v != "" {
		sets = append(sets, "name = ?")
		vals = append(vals, v)
	}
	if v, ok := args["description"].(string); ok {
		sets = append(sets, "description = ?")
		vals = append(vals, v)
	}
	for arg, column := range map[string]string{
		"auth_client_id": "auth_client_id", "auth_organization_id": "auth_organization_id",
		"auth_organization_slug": "auth_organization_slug", "brand_name": "brand_name",
		"logo_url": "logo_url", "favicon_url": "favicon_url", "support_email": "support_email",
	} {
		if v, ok := args[arg].(string); ok {
			sets = append(sets, column+" = ?")
			vals = append(vals, strings.TrimSpace(v))
		}
	}
	for _, arg := range []string{"primary_color", "accent_color"} {
		if v, ok := args[arg].(string); ok {
			v = strings.TrimSpace(v)
			if !colorRE.MatchString(v) {
				return nil, fmt.Errorf("%s must be a six-digit hex color", arg)
			}
			sets = append(sets, arg+" = ?")
			vals = append(vals, strings.ToLower(v))
		}
	}
	if v, ok := args["portal_host"].(string); ok {
		v = strings.TrimRight(strings.TrimSpace(v), "/")
		if v != "" {
			parsed, parseErr := url.Parse(v)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return nil, errors.New("portal_host must be an absolute http(s) URL")
			}
		}
		sets = append(sets, "portal_host = ?")
		vals = append(vals, v)
	}
	if v, ok := args["signup_mode"].(string); ok {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "open" && v != "closed" {
			return nil, errors.New("signup_mode must be open or closed")
		}
		sets = append(sets, "signup_mode = ?")
		vals = append(vals, v)
	}
	if v, ok := args["auto_create_members"].(bool); ok {
		sets = append(sets, "auto_create_members = ?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	vals = append(vals, id)
	if _, err := db.Exec(
		`UPDATE communities SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...,
	); err != nil {
		return nil, err
	}
	c, err := loadCommunity(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "community.updated", map[string]any{
		"community_id": c.ID,
		"slug":         c.Slug,
	})
	return c, nil
}

func toolCommunitiesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, id); err != nil {
		return nil, err
	}
	if _, err := db.Exec(
		`UPDATE communities SET archived_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
	); err != nil {
		return nil, err
	}
	emit(ctx, "community.archived", map[string]any{"community_id": id})
	return map[string]any{"ok": true}, nil
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func toolCommunitiesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	slug, err := mustStr(args, "slug")
	if err != nil {
		return nil, err
	}
	slug = strings.ToLower(slug)
	if !slugRE.MatchString(slug) {
		return nil, fmt.Errorf("slug %q invalid: must be lowercase, 2-63 chars, [a-z0-9-]", slug)
	}
	name, err := mustStr(args, "name")
	if err != nil {
		return nil, err
	}
	desc := strArg(args, "description", "")
	projectID := scopeProject(ctx)
	if projectID == "" {
		return nil, errors.New("no project context")
	}
	id := newID("c")
	db := ctx.AppDB()
	_, err = db.Exec(
		`INSERT INTO communities (id, project_id, slug, name, description) VALUES (?, ?, ?, ?, ?)`,
		id, projectID, slug, name, desc,
	)
	if err != nil {
		return nil, fmt.Errorf("create community: %w", err)
	}
	c, err := loadCommunity(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "community.created", map[string]any{
		"community_id": c.ID,
		"slug":         c.Slug,
		"name":         c.Name,
	})
	return c, nil
}

func toolCommunitiesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	includeArchived, _ := args["include_archived"].(bool)
	projectID := scopeProject(ctx)
	if projectID == "" {
		return nil, errors.New("no project context")
	}
	q := `SELECT ` + communityCols + `
	      FROM communities WHERE project_id = ?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY created_at`
	rows, err := ctx.AppDB().Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Community{}
	for rows.Next() {
		c, err := scanCommunity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"communities": out}, nil
}

func toolCommunitiesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strArg(args, "id", "")
	slug := strArg(args, "slug", "")
	if id == "" && slug == "" {
		return nil, errors.New("id or slug required")
	}
	projectID := scopeProject(ctx)
	db := ctx.AppDB()
	var c Community
	var err error
	if id != "" {
		c, err = loadCommunity(db, id)
	} else {
		c, err = loadCommunityBySlug(db, projectID, slug)
	}
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityReadable(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

const communityCols = `id, project_id, slug, name, description,
       auth_client_id, auth_organization_id, auth_organization_slug,
       brand_name, logo_url, favicon_url, primary_color, accent_color,
       support_email, portal_host, portal_dns_managed, portal_dns_domain,
       portal_dns_name, portal_dns_type, portal_dns_value, portal_domain_error,
       signup_mode, auto_create_members,
       created_at, archived_at`

func scanCommunity(scan func(...any) error) (Community, error) {
	var c Community
	var arch sql.NullString
	if err := scan(
		&c.ID, &c.ProjectID, &c.Slug, &c.Name, &c.Description,
		&c.AuthClientID, &c.AuthOrganizationID, &c.AuthOrganizationSlug,
		&c.BrandName, &c.LogoURL, &c.FaviconURL, &c.PrimaryColor, &c.AccentColor,
		&c.SupportEmail, &c.PortalHost, &c.PortalDNSManaged, &c.PortalDNSDomain,
		&c.PortalDNSName, &c.PortalDNSType, &c.PortalDNSValue, &c.PortalDomainError,
		&c.SignupMode, &c.AutoCreateMembers,
		&c.CreatedAt, &arch,
	); err != nil {
		return c, err
	}
	if arch.Valid {
		v := arch.String
		c.ArchivedAt = &v
	}
	return c, nil
}

func loadCommunity(db *sql.DB, id string) (Community, error) {
	row := db.QueryRow(`SELECT `+communityCols+` FROM communities WHERE id = ?`, id)
	c, err := scanCommunity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("community %q not found", id)
	}
	return c, err
}

func loadCommunityBySlug(db *sql.DB, projectID, slug string) (Community, error) {
	row := db.QueryRow(
		`SELECT `+communityCols+` FROM communities WHERE project_id = ? AND slug = ?`,
		projectID, slug,
	)
	c, err := scanCommunity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("community %q not found", slug)
	}
	return c, err
}

// ─── HTTP ────────────────────────────────────────────────────────

func (a *App) httpCommunities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	out, err := toolCommunitiesList(globalCtx, map[string]any{
		"include_archived": r.URL.Query().Get("include_archived") == "true",
	})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}
