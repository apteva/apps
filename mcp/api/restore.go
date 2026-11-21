package main

import (
	"context"
	"database/sql"

	sdk "github.com/apteva/app-sdk"
)

// restoreOriginColumns hold stored URLs or issuer strings that can embed the
// runtime's public base URL: HTTP route targets and route/API/stage auth
// policies (app authorizer issuers).
var restoreOriginColumns = []string{
	"api_routes.target_ref",
	"api_configuration_routes.target_ref",
	"apis.auth_json",
	"api_routes.auth_json",
	"api_configurations.auth_json",
	"api_configuration_routes.auth_json",
	"api_stages.auth_json",
}

// OnRestore runs before the API Gateway serves data restored from a snapshot.
// The SDK has already moved every project_id, so APIs, routes, keys,
// configurations and stages are visible under the new project with their ids
// unchanged. Function targets name the function and authenticated routes list
// Functions' function ids, both of which survive the Functions restore as
// they are. What remains is URLs that point at the old runtime: those are
// rewritten to the new public base URL. Browser-origin policies and stage
// exposures are re-registered for the new install at mount.
func (a *App) OnRestore(ctx context.Context, _ *sdk.AppCtx, tx *sql.Tx, rc sdk.RestoreContext) error {
	if tx == nil {
		return nil
	}
	_, err := sdk.RewriteOriginColumns(ctx, tx, rc, restoreOriginColumns...)
	return err
}
