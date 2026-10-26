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
	segments := []any{}
	for _, spec := range []struct{ arg, kind, field, prefix string }{{"user_list_ids", "userList", "userList", "customers/" + acct.NativeAccountID + "/userLists/"}, {"custom_audience_ids", "customAudience", "customAudience", "customers/" + acct.NativeAccountID + "/customAudiences/"}, {"user_interest_ids", "userInterest", "userInterestCategory", "customers/" + acct.NativeAccountID + "/userInterests/"}} {
		for _, raw := range asSlice(args[spec.arg]) {
			id := toString(raw)
			if strings.HasPrefix(id, spec.prefix) {
				id = strings.TrimPrefix(id, spec.prefix)
			}
			if !googleNumericID(id) {
				return mcpError(fmt.Sprintf("%s requires numeric segment IDs belonging to the selected account", spec.arg)), nil
			}
			segments = append(segments, map[string]any{spec.kind: map[string]any{spec.field: spec.prefix + id}})
		}
	}
	if len(segments) == 0 || len(segments) > 100 {
		return mcpError("grouped audience requires 1–100 typed segments"), nil
	}
	create := map[string]any{"name": name, "description": firstString(args, "description"), "scope": "CUSTOMER", "dimensions": []any{map[string]any{"audienceSegments": map[string]any{"segments": segments}}}}
	parsed, out := a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": []any{map[string]any{"audienceOperation": map[string]any{"create": create}}}, "partialFailure": false})
	if out != nil {
		return out, nil
	}
	return map[string]any{"id": googleMutateID(parsed, "audienceResult"), "audience_type": "grouped", "result": parsed}, nil
}
