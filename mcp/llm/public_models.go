package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Public models are gateway-owned names such as "text-fast" that resolve to
// an ordered list of provider routes. Callers never need a provider prefix,
// and operators can re-point a name without touching clients. Pricing is not
// part of a public model; that belongs to billing.

type PublicModel struct {
	ID          int64           `json:"id"`
	ProjectID   string          `json:"project_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Routes      []FallbackRoute `json:"routes"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   string          `json:"created_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
}

var publicModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func dbPublicModelUpsert(db *sql.DB, projectID string, args map[string]any) (*PublicModel, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("project_id is required")
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if !publicModelName.MatchString(name) {
		return nil, errors.New("name must be 1-64 letters, digits, '.', '_', ':' or '-' and cannot contain '/'")
	}
	var routes []FallbackRoute
	raw := jsonFromAny(args["routes"])
	if len(raw) == 0 {
		return nil, errors.New("routes is required")
	}
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, errors.New("routes must be an array of {provider, model}")
	}
	if len(routes) == 0 {
		return nil, errors.New("routes must contain at least one route")
	}
	for i := range routes {
		model := strings.TrimSpace(routes[i].Model)
		provider := normalizeProvider(firstNonEmpty(routes[i].Provider, providerFromModel(model)))
		if model == "" || provider == "" {
			return nil, fmt.Errorf("route %d needs a provider or a provider-prefixed model", i+1)
		}
		routes[i] = FallbackRoute{Provider: provider, Model: gatewayModelID(provider, model)}
	}
	enabled := true
	if _, ok := args["enabled"]; ok {
		enabled = boolArg(args, "enabled")
	}
	routesJSON, _ := json.Marshal(routes)
	if _, err := db.Exec(`INSERT INTO public_models (project_id, name, description, routes_json, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, name) DO UPDATE SET
			description=excluded.description, routes_json=excluded.routes_json,
			enabled=excluded.enabled, updated_at=CURRENT_TIMESTAMP`,
		projectID, name, strArg(args, "description"), string(routesJSON), boolToInt(enabled)); err != nil {
		return nil, err
	}
	return dbPublicModelGet(db, projectID, name, false)
}

func dbPublicModelGet(db *sql.DB, projectID, name string, enabledOnly bool) (*PublicModel, error) {
	query := `SELECT id, project_id, name, description, routes_json, enabled, COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM public_models WHERE project_id=? AND name=?`
	if enabledOnly {
		query += ` AND enabled=1`
	}
	pm, err := scanPublicModel(db.QueryRow(query, projectID, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return pm, err
}

func dbPublicModelsList(db *sql.DB, projectID string, enabledOnly bool) ([]PublicModel, error) {
	query := `SELECT id, project_id, name, description, routes_json, enabled, COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM public_models WHERE project_id=?`
	if enabledOnly {
		query += ` AND enabled=1`
	}
	rows, err := db.Query(query+` ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicModel{}
	for rows.Next() {
		pm, err := scanPublicModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pm)
	}
	return out, rows.Err()
}

func dbPublicModelDelete(db *sql.DB, projectID, name string) (bool, error) {
	res, err := db.Exec(`DELETE FROM public_models WHERE project_id=? AND name=?`, projectID, strings.TrimSpace(name))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func scanPublicModel(row interface{ Scan(...any) error }) (*PublicModel, error) {
	var pm PublicModel
	var routes string
	var enabled int
	if err := row.Scan(&pm.ID, &pm.ProjectID, &pm.Name, &pm.Description, &routes, &enabled, &pm.CreatedAt, &pm.UpdatedAt); err != nil {
		return nil, err
	}
	pm.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(routes), &pm.Routes); err != nil {
		return nil, fmt.Errorf("public model %s has invalid routes: %w", pm.Name, err)
	}
	return &pm, nil
}

// publicModelAllowed checks the caller-facing name against policy. The
// routes behind it are checked separately, ignoring allowed_models, so a
// policy can grant "text-fast" without listing every provider model.
func publicModelAllowed(policies []*Policy, name string) error {
	for _, pol := range policies {
		if pol == nil {
			continue
		}
		if pol.Disabled {
			return forbiddenError("access disabled by policy")
		}
		if len(pol.AllowedModels) > 0 && !matchesAny(pol.AllowedModels, name) {
			return forbiddenError("model not allowed by policy")
		}
		if matchesAny(pol.BlockedModels, name) {
			return forbiddenError("model blocked by policy")
		}
	}
	return nil
}

func (a *App) toolPublicModelsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbPublicModelsList(ctx.AppDB(), projectFromArgs(args), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"public_models": rows}, nil
}

func (a *App) toolPublicModelUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pm, err := dbPublicModelUpsert(ctx.AppDB(), projectFromArgs(args), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"public_model": pm}, nil
}

func (a *App) toolPublicModelDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	deleted, err := dbPublicModelDelete(ctx.AppDB(), projectFromArgs(args), strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": deleted}, nil
}

func (a *App) handlePublicModels(w http.ResponseWriter, r *http.Request) {
	db := globalCtx.AppDB()
	projectID := projectFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		rows, err := dbPublicModelsList(db, projectID, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"public_models": rows})
	case http.MethodPost, http.MethodPut:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		pm, err := dbPublicModelUpsert(db, projectID, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"public_model": pm})
	case http.MethodDelete:
		deleted, err := dbPublicModelDelete(db, projectID, r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"deleted": deleted})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
