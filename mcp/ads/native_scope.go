package main

import "fmt"

// Native ad operations remain an escape hatch for fields, not for the identity
// or operation selected by the caller. A single public call tracks one entity.
func validateGoogleAdOperations(acct *adAccount, args map[string]any, ops []any, kind string) map[string]any {
	if len(ops) != 1 {
		return mcpError("supply exactly one native ad operation per call")
	}
	op := asMap(ops[0])
	payload := asMap(op[kind])
	if len(payload) == 0 || op["remove"] != nil || (kind == "create" && op["update"] != nil) || (kind == "update" && op["create"] != nil) {
		return mcpError("native operation must match ad_" + kind)
	}
	if kind == "create" {
		parent := stringArgAny(args, "adset_id")
		if !googleNumericID(parent) || firstString(payload, "adGroup", "ad_group") != googleAdGroupResource(acct.NativeAccountID, parent) || payload["resourceName"] != nil || payload["resource_name"] != nil {
			return mcpError("native ad create must use the selected adset_id")
		}
	} else {
		parent, id := stringArgAny(args, "adset_id"), stringArgAny(args, "ad_id")
		resource := id
		if googleNumericID(id) && googleNumericID(parent) {
			resource = fmt.Sprintf("customers/%s/adGroupAds/%s~%s", acct.NativeAccountID, parent, id)
		}
		if resource == "" || firstString(payload, "resourceName", "resource_name") != resource || payload["adGroup"] != nil || payload["ad_group"] != nil {
			return mcpError("native ad update must target the selected ad_id and cannot change its parent")
		}
	}
	// Reject alternate JSON spellings that could disagree after provider parsing.
	if (payload["resourceName"] != nil && payload["resource_name"] != nil) || (payload["adGroup"] != nil && payload["ad_group"] != nil) {
		return mcpError("use one spelling for each native resource field")
	}
	return nil
}
