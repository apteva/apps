package main

import (
	"regexp"
	"strings"
	"testing"
)

// GAQL rejects a query that filters on a field which is absent from the SELECT
// clause: "'campaign.id' must be present in SELECT clause". Four queries
// shipped with that fault at once, so the rule is checked mechanically against
// every query the app emits rather than re-read by eye.

var gaqlFieldPattern = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\.([a-z][a-z0-9_.]*)`)

func gaqlResources(clause string) map[string]bool {
	found := map[string]bool{}
	for _, match := range gaqlFieldPattern.FindAllStringSubmatch(clause, -1) {
		found[match[1]] = true
	}
	return found
}

// assertGAQLSelectCoversFilters fails when a resource is filtered on but never
// selected.
func assertGAQLSelectCoversFilters(t *testing.T, label, query string) {
	t.Helper()
	upper := strings.ToUpper(query)
	selectStart := strings.Index(upper, "SELECT ")
	fromStart := strings.Index(upper, " FROM ")
	whereStart := strings.Index(upper, " WHERE ")
	if selectStart < 0 || fromStart < 0 {
		t.Fatalf("%s: not a SELECT query: %s", label, query)
	}
	if whereStart < 0 {
		return
	}
	selected := gaqlResources(query[selectStart+len("SELECT ") : fromStart])
	where := query[whereStart+len(" WHERE "):]
	for _, terminator := range []string{" LIMIT ", " ORDER BY "} {
		if idx := strings.Index(strings.ToUpper(where), terminator); idx >= 0 {
			where = where[:idx]
		}
	}
	for resource := range gaqlResources(where) {
		if !selected[resource] {
			t.Errorf("%s: filters on %s.* but never selects it — Google rejects this query.\n  %s",
				label, resource, query)
		}
	}
}

func capturedQuery(t *testing.T, pf *recordingPlatform) string {
	t.Helper()
	return toString(findExecuteCall(t, pf, "search").Input["query"])
}

func TestEveryEmittedGAQLQuerySelectsWhatItFiltersOn(t *testing.T) {
	type listCall struct {
		label string
		args  map[string]any
		call  func(app *App, args map[string]any) (any, error)
	}

	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	calls := []listCall{
		{"keyword_list", map[string]any{"ad_account_id": accountID, "adset_id": "456"},
			func(a *App, args map[string]any) (any, error) { return a.toolKeywordList(ctx, args) }},
		{"negative_keyword_list ad_group", map[string]any{"ad_account_id": accountID, "adset_id": "456"},
			func(a *App, args map[string]any) (any, error) { return a.toolNegativeKeywordList(ctx, args) }},
		{"negative_keyword_list campaign", map[string]any{"ad_account_id": accountID, "level": "campaign", "campaign_id": "987"},
			func(a *App, args map[string]any) (any, error) { return a.toolNegativeKeywordList(ctx, args) }},
		{"ad_extension_list", map[string]any{"ad_account_id": accountID, "campaign_id": "987"},
			func(a *App, args map[string]any) (any, error) { return a.toolAdExtensionList(ctx, args) }},
		{"ad_extension_list filtered", map[string]any{"ad_account_id": accountID, "campaign_id": "987", "type": "sitelink"},
			func(a *App, args map[string]any) (any, error) { return a.toolAdExtensionList(ctx, args) }},
		{"campaign_targeting_list", map[string]any{"ad_account_id": accountID, "campaign_id": "987"},
			func(a *App, args map[string]any) (any, error) { return a.toolCampaignTargetingList(ctx, args) }},
		{"budget_list", map[string]any{"ad_account_id": accountID},
			func(a *App, args map[string]any) (any, error) { return a.toolBudgetList(ctx, args) }},
	}

	for _, c := range calls {
		t.Run(c.label, func(t *testing.T) {
			if _, err := c.call(app, c.args); err != nil {
				t.Fatalf("%s errored: %v", c.label, err)
			}
			assertGAQLSelectCoversFilters(t, c.label, capturedQuery(t, pf))
		})
	}
}

// The checker has to actually catch the shipped fault, not pass on everything.
func TestGAQLInvariantCatchesTheShippedFault(t *testing.T) {
	broken := "SELECT campaign_asset.asset, campaign_asset.field_type FROM campaign_asset " +
		"WHERE campaign.id = 987 AND campaign_asset.status != REMOVED"
	fake := &testing.T{}
	assertGAQLSelectCoversFilters(fake, "broken", broken)
	if !fake.Failed() {
		t.Fatal("the invariant checker did not catch a query missing campaign.id from SELECT")
	}

	fixed := "SELECT campaign.id, campaign_asset.asset FROM campaign_asset WHERE campaign.id = 987"
	clean := &testing.T{}
	assertGAQLSelectCoversFilters(clean, "fixed", fixed)
	if clean.Failed() {
		t.Fatal("the invariant checker rejected a correct query")
	}
}
