package main

import (
	"encoding/json"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Read-only JSON routes for the panel. Every change goes through MCP tools so
// the panel and agents share one set of checks.
func (a *App) routes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/overview", Handler: a.getOnly(a.httpOverview)},
		{Pattern: "/profiles", Handler: a.getOnly(a.httpProfiles)},
		{Pattern: "/products", Handler: a.getOnly(a.httpProducts)},
		{Pattern: "/pools", Handler: a.getOnly(a.httpPools)},
		{Pattern: "/machines", Handler: a.getOnly(a.httpMachines)},
		{Pattern: "/tenants", Handler: a.getOnly(a.httpTenants)},
		{Pattern: "/tenants/", Handler: a.getOnly(a.httpTenantItem)},
		{Pattern: "/operations", Handler: a.getOnly(a.httpOperations)},
	}
}

func handleJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

func (a *App) getOnly(fn func(*sdk.AppCtx, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := fn(globalCtx, r)
		if err != nil {
			code := http.StatusInternalServerError
			if strings.Contains(err.Error(), "not found") {
				code = http.StatusNotFound
			}
			httpErr(w, code, err.Error())
			return
		}
		handleJSON(w, out)
	}
}

func queryArgs(r *http.Request, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v := r.URL.Query().Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

func (a *App) httpOverview(ctx *sdk.AppCtx, _ *http.Request) (any, error) {
	db := ctx.AppDB()
	tenants := map[string]int{}
	rows, err := db.Query(`SELECT status, COUNT(*) FROM hosting_tenants GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if rows.Scan(&s, &n) == nil {
			tenants[s] = n
		}
	}
	rows.Close()
	var failed int
	_ = db.QueryRow(`SELECT COUNT(*) FROM hosting_operations WHERE status='failed' AND (machine_id IS NULL OR machine_id IN (SELECT id FROM hosting_machines WHERE state='quarantined'))`).Scan(&failed)
	pools, err := a.toolPoolList(ctx, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": appVersion, "tenants": tenants, "failed_runs": failed, "pools": pools.(map[string]any)["pools"]}, nil
}

func (a *App) httpProfiles(ctx *sdk.AppCtx, r *http.Request) (any, error) {
	db := ctx.AppDB()
	profiles, err := dbProfileList(db, r.URL.Query().Get("include_archived") == "1")
	if err != nil {
		return nil, err
	}
	type withAvailability struct {
		*Profile
		Availability *Availability `json:"availability"`
	}
	out := make([]withAvailability, 0, len(profiles))
	for _, p := range profiles {
		av, err := availability(db, p)
		if err != nil {
			return nil, err
		}
		out = append(out, withAvailability{Profile: p, Availability: av})
	}
	return map[string]any{"profiles": out, "count": len(out)}, nil
}

func (a *App) httpProducts(ctx *sdk.AppCtx, _ *http.Request) (any, error) {
	return a.toolProductList(ctx, nil)
}

func (a *App) httpPools(ctx *sdk.AppCtx, _ *http.Request) (any, error) {
	return a.toolPoolList(ctx, nil)
}

func (a *App) httpMachines(ctx *sdk.AppCtx, r *http.Request) (any, error) {
	return a.toolMachineList(ctx, queryArgs(r, "pool_key", "state"))
}

func (a *App) httpTenants(ctx *sdk.AppCtx, r *http.Request) (any, error) {
	args := queryArgs(r, "status", "profile_key", "backend", "owner_email", "external_ref")
	if r.URL.Query().Get("include_released") == "1" {
		args["include_released"] = true
	}
	return a.toolTenantList(ctx, args)
}

func (a *App) httpTenantItem(ctx *sdk.AppCtx, r *http.Request) (any, error) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tenants/"), "/")
	out, err := a.toolTenantGet(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		return nil, err
	}
	ops, err := dbOperationList(ctx.AppDB(), map[string]any{"tenant_id": id, "limit": 10})
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	m["operations"] = ops
	return m, nil
}

func (a *App) httpOperations(ctx *sdk.AppCtx, r *http.Request) (any, error) {
	args := queryArgs(r, "tenant_id", "status")
	if v := r.URL.Query().Get("machine_id"); v != "" {
		args["machine_id"] = v
	}
	return a.toolOperationList(ctx, args)
}
