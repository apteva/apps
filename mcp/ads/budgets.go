package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Campaign budgets are created as a side effect of campaign_create but were
// otherwise invisible through this app: nothing could list them and nothing
// could remove them. A campaign mutate that fails after its budget lands
// strands that budget, so the cleanup path needs to be reachable by a caller.

// resolveGoogleAccount gates an account-level Google-only surface. Unlike
// resolveGoogleTarget it does not require a parent campaign or ad group.
func (a *App) resolveGoogleAccount(
	ctx *sdk.AppCtx, args map[string]any, operation string,
) (*adAccount, map[string]any) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return nil, errOut
	}
	if acct.Platform != "google" {
		return nil, googleOnlyError(acct.Platform, operation)
	}
	return acct, nil
}

func (a *App) toolBudgetList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, errOut := a.resolveGoogleAccount(ctx, args, "budget_list")
	if errOut != nil {
		return errOut, nil
	}
	limit := intArg(args, "limit", 200)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := fmt.Sprintf(
		"SELECT campaign_budget.id, campaign_budget.name, campaign_budget.amount_micros, "+
			"campaign_budget.explicitly_shared, campaign_budget.status, campaign_budget.reference_count, "+
			"campaign_budget.delivery_method "+
			"FROM campaign_budget WHERE campaign_budget.status != REMOVED LIMIT %d", limit,
	)
	rows, errOut := a.googleSearchRows(ctx, acct, query)
	if errOut != nil {
		return errOut, nil
	}
	onlyOrphaned := boolArgDefault(args, "only_orphaned", false)
	budgets := make([]map[string]any, 0, len(rows))
	orphans := 0
	for _, row := range rows {
		item := mapAt(row, "campaignBudget")
		if len(item) == 0 {
			item = mapAt(row, "campaign_budget")
		}
		id := firstString(item, "id")
		if id == "" {
			continue
		}
		// reference_count is the number of campaigns using the budget. Zero
		// means nothing points at it, which is what a stranded budget looks like.
		references := intArg(item, "referenceCount", intArg(item, "reference_count", 0))
		orphaned := references == 0
		if orphaned {
			orphans++
		}
		if onlyOrphaned && !orphaned {
			continue
		}
		budgets = append(budgets, map[string]any{
			"id":                id,
			"resource_name":     fmt.Sprintf("customers/%s/campaignBudgets/%s", acct.NativeAccountID, id),
			"name":              firstString(item, "name"),
			"amount_micros":     firstString(item, "amountMicros", "amount_micros"),
			"explicitly_shared": googleBool(item["explicitlyShared"]) || googleBool(item["explicitly_shared"]),
			"delivery_method":   firstString(item, "deliveryMethod", "delivery_method"),
			"status":            firstString(item, "status"),
			"reference_count":   references,
			"orphaned":          orphaned,
		})
	}
	return map[string]any{
		"count": len(budgets), "orphaned_count": orphans, "budgets": budgets,
	}, nil
}

func (a *App) toolBudgetDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, errOut := a.resolveGoogleAccount(ctx, args, "budget_delete")
	if errOut != nil {
		return errOut, nil
	}
	raw, ok := args["budget_ids"].([]any)
	if !ok || len(raw) == 0 {
		return mcpError("budget_ids must contain at least one budget id from budget_list"), nil
	}
	if len(raw) > 100 {
		return mcpError("budget_ids supports at most 100 ids"), nil
	}
	seen := make(map[string]bool, len(raw))
	operations := make([]any, 0, len(raw))
	ids := make([]string, 0, len(raw))
	for i, entry := range raw {
		id := strings.TrimSpace(toString(entry))
		if !googleNumericID(id) {
			return mcpError(fmt.Sprintf("budget_ids[%d] must be a numeric budget id", i)), nil
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		operations = append(operations, map[string]any{
			"remove": fmt.Sprintf("customers/%s/campaignBudgets/%s", acct.NativeAccountID, id),
		})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, "budget_mutate", map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		// A budget still referenced by a campaign cannot be removed; say so
		// rather than leaving the caller with a bare provider error.
		errOut["hint"] = "a budget in use by a campaign cannot be removed; check reference_count in budget_list"
		return errOut, nil
	}
	return map[string]any{"removed": len(ids), "budget_ids": ids, "result": out}, nil
}
