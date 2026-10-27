package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func (a *App) googleGroupedAudienceCreate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	name := strings.TrimSpace(firstString(args, "name"))
	if name == "" {
		return mcpError("name required"), nil
	}
	if len(asMap(args["platform_options"])) > 0 {
		return mcpError("grouped audiences use structured segment IDs"), nil
	}
	native, err := a.googleGroupedSegments(ctx, acct, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	create, err := googleGroupedAudienceDefinition(acct, native)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	parsed, out := a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": []any{map[string]any{"audienceOperation": map[string]any{"create": create}}}, "partialFailure": false})
	if out != nil {
		return out, nil
	}
	id := googleMutateID(parsed, "audienceResult")
	if id == "" {
		return mcpError("Google did not return an audience ID"), nil
	}
	r, err := a.saveGoogleAudience(ctx, acct, "google_grouped_audience", id, name, "grouped", create, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "audience_resource_id": r.ID, "audience_type": "grouped", "result": parsed}, nil
}

func googleGroupedAudienceDefinition(acct *adAccount, args map[string]any) (map[string]any, error) {
	segments := []any{}
	for _, spec := range []struct{ arg, kind, field, prefix string }{{"user_list_ids", "userList", "userList", "customers/" + acct.NativeAccountID + "/userLists/"}, {"custom_audience_ids", "customAudience", "customAudience", "customers/" + acct.NativeAccountID + "/customAudiences/"}, {"user_interest_ids", "userInterest", "userInterestCategory", "customers/" + acct.NativeAccountID + "/userInterests/"}} {
		for _, raw := range asSlice(args[spec.arg]) {
			id := toString(raw)
			if strings.HasPrefix(id, spec.prefix) {
				id = strings.TrimPrefix(id, spec.prefix)
			}
			if !googleNumericID(id) {
				return nil, fmt.Errorf("%s requires numeric segment IDs belonging to the selected account", spec.arg)
			}
			segments = append(segments, map[string]any{spec.kind: map[string]any{spec.field: spec.prefix + id}})
		}
	}
	if len(segments) == 0 || len(segments) > 100 {
		return nil, fmt.Errorf("grouped audience requires 1–100 typed segments")
	}
	create := map[string]any{"name": firstString(args, "name"), "description": firstString(args, "description"), "scope": "CUSTOMER", "dimensions": []any{map[string]any{"audienceSegments": map[string]any{"segments": segments}}}}
	return create, nil
}
