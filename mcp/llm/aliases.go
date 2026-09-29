package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// AliasTarget is one backend that may serve a public alias. Targets are tried
// in order, so moving an alias onto different hardware is a data change rather
// than a customer-visible model rename.
type AliasTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ModelAlias is the public name a caller uses. Callers contract for the alias;
// which provider currently serves it is an operational detail we keep free to
// change, which is what makes a Fireworks-to-own-GPU migration invisible.
type ModelAlias struct {
	ID          int64         `json:"id,omitempty"`
	ProjectID   string        `json:"project_id"`
	Alias       string        `json:"alias"`
	DisplayName string        `json:"display_name,omitempty"`
	Targets     []AliasTarget `json:"targets"`
	Status      string        `json:"status"`
	CreatedAt   string        `json:"created_at,omitempty"`
	UpdatedAt   string        `json:"updated_at,omitempty"`
}

// resolvedRoute is the outcome of turning a caller-supplied model string into
// the ordered backends allowed to serve it. PolicyProvider/PolicyModel are what
// policy is evaluated against: for an aliased request that is the alias itself.
type resolvedRoute struct {
	Alias          string
	PolicyProvider string
	PolicyModel    string
	Attempts       []providerAttempt
}

// aliasNamespace returns the prefix an alias is grouped under, so that
// allowed_providers policies can grant a whole public catalog at once.
func aliasNamespace(alias string) string {
	prefix, _, ok := strings.Cut(strings.TrimSpace(alias), "/")
	if !ok {
		return ""
	}
	return normalizeProvider(prefix)
}

func normalizeAlias(alias string) string {
	return strings.ToLower(strings.TrimSpace(alias))
}

func decodeAliasTargets(raw string) []AliasTarget {
	var targets []AliasTarget
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil
	}
	out := make([]AliasTarget, 0, len(targets))
	for _, t := range targets {
		t.Provider = normalizeProvider(t.Provider)
		t.Model = strings.TrimSpace(t.Model)
		if t.Provider == "" {
			t.Provider = providerFromModel(t.Model)
		}
		if t.Provider == "" || t.Model == "" {
			continue
		}
		t.Model = gatewayModelID(t.Provider, t.Model)
		out = append(out, t)
	}
	return out
}

// aliasAttempts expands an alias into the ordered attempt list the chat
// execution loop already understands, deduplicated the same way fallback
// routes are.
func aliasAttempts(alias *ModelAlias) []providerAttempt {
	if alias == nil {
		return nil
	}
	out := []providerAttempt{}
	seen := map[string]bool{}
	for _, t := range alias.Targets {
		key := t.Provider + "\x00" + t.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, providerAttempt{Provider: t.Provider, Model: t.Model})
	}
	return out
}

func scanModelAlias(scan func(dest ...any) error) (*ModelAlias, error) {
	var (
		out         ModelAlias
		targetsJSON string
	)
	if err := scan(&out.ID, &out.ProjectID, &out.Alias, &out.DisplayName, &targetsJSON, &out.Status, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return nil, err
	}
	out.Targets = decodeAliasTargets(targetsJSON)
	return &out, nil
}

const modelAliasColumns = `id, project_id, alias, display_name, targets_json, status, created_at, updated_at`

// dbModelAliasResolve prefers a project-scoped alias and falls back to the
// global catalog, so a customer-specific pin can shadow a shared name.
func dbModelAliasResolve(db *sql.DB, projectID, name string) (*ModelAlias, error) {
	name = normalizeAlias(name)
	if name == "" {
		return nil, nil
	}
	for _, scope := range []string{strings.TrimSpace(projectID), ""} {
		if scope == "" && strings.TrimSpace(projectID) == "" {
			// Avoid querying the same global scope twice.
			if projectID != "" {
				continue
			}
		}
		row := db.QueryRow(`SELECT `+modelAliasColumns+` FROM model_aliases
			WHERE project_id = ? AND alias = ? AND status = 'active'`, scope, name)
		alias, err := scanModelAlias(row.Scan)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return alias, nil
	}
	return nil, nil
}

func dbModelAliasList(db *sql.DB, projectID string, includeInactive bool) ([]*ModelAlias, error) {
	query := `SELECT ` + modelAliasColumns + ` FROM model_aliases WHERE (project_id = ? OR project_id = '')`
	args := []any{strings.TrimSpace(projectID)}
	if !includeInactive {
		query += ` AND status = 'active'`
	}
	query += ` ORDER BY alias ASC, project_id DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ModelAlias{}
	for rows.Next() {
		alias, err := scanModelAlias(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, alias)
	}
	return out, rows.Err()
}

func parseAliasTargetArgs(args map[string]any) ([]AliasTarget, error) {
	raw, ok := args["targets"].([]any)
	if !ok {
		return nil, userError("targets must be an array of {provider, model} objects")
	}
	out := []AliasTarget{}
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, userError("each target must be an object with provider and model")
		}
		provider := normalizeProvider(strArg(obj, "provider"))
		model := strings.TrimSpace(strArg(obj, "model"))
		if provider == "" {
			provider = providerFromModel(model)
		}
		if provider == "" || model == "" {
			return nil, userError("each target requires a provider and a model")
		}
		out = append(out, AliasTarget{Provider: provider, Model: gatewayModelID(provider, model)})
	}
	if len(out) == 0 {
		return nil, userError("at least one target is required")
	}
	return out, nil
}

func dbModelAliasUpsert(db *sql.DB, projectID string, args map[string]any) (*ModelAlias, error) {
	alias := normalizeAlias(strArg(args, "alias"))
	if alias == "" {
		return nil, userError("alias is required")
	}
	if strings.ContainsAny(alias, " \t") {
		return nil, userError("alias must not contain whitespace")
	}
	targets, err := parseAliasTargetArgs(args)
	if err != nil {
		return nil, err
	}
	// A public alias must not collide with a provider-prefixed model string, or
	// resolution would silently change meaning for existing callers.
	if ns := aliasNamespace(alias); ns != "" && defaultBaseURL(ns) != "" {
		return nil, userError(fmt.Sprintf("alias namespace %q is reserved by provider routing", ns))
	}
	status := firstNonEmpty(strings.TrimSpace(strArg(args, "status")), "active")
	if status != "active" && status != "deprecated" {
		return nil, userError("status must be active or deprecated")
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`INSERT INTO model_aliases (project_id, alias, display_name, targets_json, status, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, alias) DO UPDATE SET
			display_name=excluded.display_name,
			targets_json=excluded.targets_json,
			status=excluded.status,
			updated_at=CURRENT_TIMESTAMP`,
		strings.TrimSpace(projectID), alias, strings.TrimSpace(strArg(args, "display_name")), string(targetsJSON), status); err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT `+modelAliasColumns+` FROM model_aliases WHERE project_id = ? AND alias = ?`,
		strings.TrimSpace(projectID), alias)
	return scanModelAlias(row.Scan)
}

// dbModelAliasDelete deprecates rather than drops, so usage history that
// references the alias stays interpretable for billing.
func dbModelAliasDelete(db *sql.DB, projectID, name string) error {
	alias := normalizeAlias(name)
	if alias == "" {
		return userError("alias is required")
	}
	res, err := db.Exec(`UPDATE model_aliases SET status='deprecated', updated_at=CURRENT_TIMESTAMP
		WHERE project_id = ? AND alias = ?`, strings.TrimSpace(projectID), alias)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return userError("alias not found")
	}
	return nil
}

// resolveModelRoute maps the caller's model string onto backends. Unaliased
// provider-prefixed strings keep working exactly as before.
func resolveModelRoute(db *sql.DB, projectID, model string, policies []*Policy) (*resolvedRoute, error) {
	alias, err := dbModelAliasResolve(db, projectID, model)
	if err != nil {
		return nil, err
	}
	if alias != nil {
		attempts := aliasAttempts(alias)
		if len(attempts) == 0 {
			return nil, userError(fmt.Sprintf("model alias %q has no usable targets", alias.Alias))
		}
		return &resolvedRoute{
			Alias:          alias.Alias,
			PolicyProvider: aliasNamespace(alias.Alias),
			PolicyModel:    alias.Alias,
			Attempts:       attempts,
		}, nil
	}
	provider := providerFromModel(model)
	if provider == "" {
		return nil, userError("model must include a provider prefix, for example openai/gpt-4.1, or name a configured gateway alias")
	}
	return &resolvedRoute{
		PolicyProvider: provider,
		PolicyModel:    model,
		Attempts:       fallbackAttempts(policies, provider, model),
	}, nil
}

func (a *App) toolAliasesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbModelAliasList(ctx.AppDB(), projectFromArgs(args), boolArg(args, "include_inactive"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"aliases": rows}, nil
}

func (a *App) toolAliasUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	alias, err := dbModelAliasUpsert(ctx.AppDB(), projectFromArgs(args), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"alias": alias}, nil
}

func (a *App) toolAliasDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := dbModelAliasDelete(ctx.AppDB(), projectFromArgs(args), strArg(args, "alias")); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deprecated"}, nil
}
