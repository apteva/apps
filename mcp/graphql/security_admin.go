package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolSecurityGet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	api, err := resolveGraphQLAPI(ctx.AppDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	policy, err := getSecurity(ctx.AppReadDB(), project, api.Slug)
	return map[string]any{"security": policy, "endpoint": "/public/graphql/" + api.Slug}, err
}
func (a *App) toolSecuritySet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	policy, err := setSecurity(ctx.AppDB(), project, apiSlugArg(args), args["security"])
	return map[string]any{"security": policy}, err
}
func (a *App) toolSecurityValidate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	return a.validateSecurity(callCtx, project, apiSlugArg(args))
}

func validateSecurityBindings(db *sql.DB, project, api string) error {
	p, err := getSecurity(db, project, api)
	if err != nil {
		return err
	}
	if err := validateRowFilterTargets(db, project, api, p); err != nil {
		return err
	}
	resolvers, err := listResolversForAPI(db, project, api)
	if err != nil {
		return err
	}
	for _, r := range resolvers {
		source, err := getSourceForAPI(db, project, api, r.SourceID, "")
		if err != nil {
			return err
		}
		if source == nil {
			return invalid("resolver source missing")
		}
		if source.Kind != "function" {
			continue
		}
		c, err := functionSecurity(mergeMaps(source.Config, r.Config))
		if err != nil {
			return err
		}
		if c.Authenticated && (p.Mode != "auth" || r.ParentType == "Subscription") {
			return invalid("trusted Function resolvers require Auth mode and cannot be subscriptions")
		}
	}
	return nil
}

func (a *App) validateSecurity(ctx context.Context, project, api string) (any, error) {
	if _, err := resolveGraphQLAPI(a.ctx.AppDB(), project, api); err != nil {
		return nil, err
	}
	issues := []string{}
	if err := validateSecurityBindings(a.ctx.AppReadDB(), project, api); err != nil {
		issues = append(issues, err.Error())
	}
	p, err := getSecurity(a.ctx.AppReadDB(), project, api)
	if err != nil {
		return nil, err
	}
	if p.Mode == "auth" && a.ctx.CurrentProject() == "" {
		issues = append(issues, "Authenticated public execution requires a project-scoped GraphQL installation.")
	}
	resolvers, err := listResolversForAPI(a.ctx.AppReadDB(), project, api)
	if err != nil {
		return nil, err
	}
	installID, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	checked := map[int64]bool{}
	for _, r := range resolvers {
		source, err := getSourceForAPI(a.ctx.AppReadDB(), project, api, r.SourceID, "")
		if err != nil {
			return nil, err
		}
		if source == nil || source.Kind != "function" {
			continue
		}
		c, err := functionSecurity(mergeMaps(source.Config, r.Config))
		if err != nil || !c.Authenticated {
			continue
		}
		for _, id := range c.FunctionIDs {
			if checked[id] {
				continue
			}
			checked[id] = true
			var out struct {
				Function struct {
					ID               int64  `json:"id"`
					ProjectID        string `json:"project_id"`
					InvocationPolicy struct {
						AuthenticatedCallers []struct {
							InstallationID int64    `json:"installation_id"`
							Issuers        []string `json:"issuers"`
						} `json:"authenticated_callers"`
					} `json:"invocation_policy"`
				} `json:"function"`
			}
			err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(project).PlatformAPI(), "functions", "functions_get", map[string]any{"id": id, "_project_id": project}, &out)
			if err != nil || out.Function.ID != id || out.Function.ProjectID != project {
				issues = append(issues, fmt.Sprintf("Function %d could not be verified in this project.", id))
				continue
			}
			allowed := false
			for _, caller := range out.Function.InvocationPolicy.AuthenticatedCallers {
				for _, issuer := range caller.Issuers {
					if installID > 0 && caller.InstallationID == installID && issuer == "apteva:auth:"+p.TenantID {
						allowed = true
					}
				}
			}
			if !allowed {
				issues = append(issues, fmt.Sprintf("Function %d must explicitly trust this GraphQL installation and issuer apteva:auth:%s. No policy was changed.", id, p.TenantID))
			}
		}
	}
	return map[string]any{"valid": len(issues) == 0, "issues": issues, "checked_functions": len(checked)}, nil
}

func (a *App) verifyPublishSecurity(ctx context.Context, project, api string) error {
	p, err := getSecurity(a.ctx.AppReadDB(), project, api)
	if err != nil {
		return err
	}
	if p.Mode != "auth" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := a.validateSecurity(ctx, project, api)
	if err != nil {
		return err
	}
	report := result.(map[string]any)
	if report["valid"] != true {
		return invalid("security validation failed: %s", strings.Join(report["issues"].([]string), "; "))
	}
	return nil
}
