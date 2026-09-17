package main

import (
	"fmt"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// This mirrors scripts/oak_google_campaign.py call for call: campaign -> ad
// group -> 9 keywords -> 24 negatives -> RSA (12 headlines / 4 descriptions)
// -> 4 sitelinks. It is the acceptance path that v0.1.47 could not get past
// step one, so it is pinned here against stubs rather than only run by hand.

func oakKeywords() []any {
	specs := [][2]string{
		{"mirror jewelry cabinet", "EXACT"},
		{"jewelry cabinet with mirror", "EXACT"},
		{"wall mounted jewelry cabinet", "EXACT"},
		{"wall mount jewelry armoire", "PHRASE"},
		{"door mounted jewelry cabinet", "PHRASE"},
		{"over the door jewelry armoire", "PHRASE"},
		{"hanging jewelry cabinet with mirror", "PHRASE"},
		{"lockable jewelry cabinet", "PHRASE"},
		{"mirrored jewelry storage cabinet", "PHRASE"},
	}
	out := make([]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"text": spec[0], "match_type": spec[1], "cpc_bid_cents": 65,
		})
	}
	return out
}

func oakNegatives() []any {
	terms := []string{
		"standing", "floor standing", "free standing", "freestanding", "dresser", "chest",
		"jewelry box", "ring box", "watch box", "display case", "retail",
		"diy", "plans", "how to build", "used", "second hand", "repair", "free", "cheap",
		"wholesale", "amazon", "ikea",
	}
	out := make([]any, 0, len(terms))
	for _, term := range terms {
		out = append(out, map[string]any{"text": term, "match_type": "PHRASE"})
	}
	return out
}

func oakRSACopy() ([]any, []any) {
	headlines := []string{
		"Mirror Jewelry Cabinet", "Wall or Door Mounted", "Lockable Mirrored Door",
		"Hooks, Shelves & Mirror", "Battery LED Lighting", "42.5 in Mirrored Cabinet",
		"Free Shipping in the US", "30-Day Returns", "Jewelry Off the Dresser",
		"Considered Home Storage", "Oak & Vault Organization", "Clear Your Dressing Area",
	}
	descriptions := []string{
		"Mirrored door with lock and key. Hooks, shelves and space for everyday jewelry.",
		"Wall- or door-mounted. 14.3 x 4.8 x 42.5 in. Free shipping to US addresses.",
		"Close the door for a clearer dressing area. Detachable LED lighting included.",
		"Considered home organization from Oak & Vault. 30-day returns on unused items.",
	}
	h := make([]any, 0, len(headlines))
	for _, text := range headlines {
		h = append(h, map[string]any{"text": text})
	}
	d := make([]any, 0, len(descriptions))
	for _, text := range descriptions {
		d = append(d, map[string]any{"text": text})
	}
	return h, d
}

func oakSitelinks() []any {
	return []any{
		map[string]any{"link_text": "Shipping", "final_url": "https://oakandvault.cartkit.co/shipping",
			"description1": "Free US shipping", "description2": "Dispatch in 1-2 business days"},
		map[string]any{"link_text": "Returns", "final_url": "https://oakandvault.cartkit.co/returns",
			"description1": "30-day returns", "description2": "On unused, repackaged items"},
		map[string]any{"link_text": "Organization guide", "final_url": "https://oakandvault.cartkit.co/organization-guide",
			"description1": "Room-by-room guide", "description2": "Start where you use most"},
		map[string]any{"link_text": "Our approach", "final_url": "https://oakandvault.cartkit.co/about",
			"description1": "Considered storage", "description2": "Built for everyday order"},
	}
}

func oakPlatform() *recordingPlatform {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "budget_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1875399872/campaignBudgets/55"}]}`), nil
		case "campaign_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1875399872/campaigns/987"}]}`), nil
		case "ad_group_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1875399872/adGroups/456"}]}`), nil
		case "ad_group_criterion_mutate":
			ops, _ := input["operations"].([]any)
			results := make([]string, 0, len(ops))
			for i := range ops {
				results = append(results, fmt.Sprintf(
					`{"resourceName":"customers/1875399872/adGroupCriteria/456~%d"}`, 1000+i))
			}
			return executeJSON(`{"results":[` + strings.Join(results, ",") + `]}`), nil
		case "ad_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1875399872/adGroupAds/456~777"}]}`), nil
		case "asset_mutate":
			ops, _ := input["operations"].([]any)
			results := make([]string, 0, len(ops))
			for i := range ops {
				results = append(results, fmt.Sprintf(
					`{"resourceName":"customers/1875399872/assets/%d"}`, 20+i))
			}
			return executeJSON(`{"results":[` + strings.Join(results, ",") + `]}`), nil
		case "campaign_asset_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1875399872/campaignAssets/987~20~SITELINK"}]}`), nil
		}
		return executeJSON(`{"results":[]}`), nil
	}
	return pf
}

func TestOakGoogleSearchCampaignEndToEnd(t *testing.T) {
	pf := oakPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1875399872")

	step := "" // set before each call so the checker can name the failing step
	ok := func(result any, err error) map[string]any {
		t.Helper()
		if err != nil {
			t.Fatalf("%s errored: %v", step, err)
		}
		out := asMap(result)
		if out["isError"] == true {
			t.Fatalf("%s failed: %#v", step, out)
		}
		return out
	}

	// 1. Campaign. The script passes the EU declaration through
	// platform_options in snake_case, which must not collide with the
	// camelCase field the app now always sets.
	step = "campaign_create"
	camp := ok(app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Oak & Vault — Mirror Jewelry Cabinet — US Search",
		"objective": "sales", "channel_type": "search", "status": "PAUSED",
		"daily_budget_cents": 2000,
		"platform_options": map[string]any{
			"contains_eu_political_advertising": "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING",
		},
	}))
	campaign := createdGoogleCampaign(t, pf)
	if _, dup := campaign["contains_eu_political_advertising"]; dup {
		t.Fatalf("both spellings of the EU declaration were sent: %#v", campaign)
	}
	if campaign["containsEuPoliticalAdvertising"] != "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING" {
		t.Fatalf("EU declaration missing: %#v", campaign)
	}
	if _, ok := campaign["maximizeConversionValue"]; !ok {
		t.Fatalf("sales objective should bid on conversion value: %#v", campaign)
	}
	if createdBudget(t, pf)["explicitlyShared"] != false {
		t.Fatalf("budget must not be shared alongside an inline strategy")
	}
	// The script chains on this exact expression.
	cid := toString(asMap(camp["campaign"])["id"])
	if cid != "987" {
		t.Fatalf("campaign id not chainable: %#v", camp)
	}

	// 2. Ad group, with no Meta targeting field in sight.
	step = "adset_create"
	ag := ok(app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": cid,
		"name":              "Mirror jewelry cabinet — wall & door mounted",
		"optimization_goal": "conversions", "status": "PAUSED",
	}))
	agid := toString(ag["id"])
	if agid != "456" {
		t.Fatalf("ad group id not chainable: %#v", ag)
	}

	// 3. Keywords.
	step = "keyword_create"
	kw := ok(app.toolKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": agid, "status": "ACTIVE",
		"keywords": oakKeywords(),
	}))
	if kw["requested"] != 9 {
		t.Fatalf("expected 9 keywords, got %v", kw["requested"])
	}

	// 4. Negatives.
	step = "negative_keyword_create"
	neg := ok(app.toolNegativeKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": agid, "keywords": oakNegatives(),
	}))
	if neg["requested"] != 22 {
		t.Fatalf("expected 22 unique negatives, got %v", neg["requested"])
	}

	// 5. Responsive search ad.
	headlines, descriptions := oakRSACopy()
	step = "ad_create"
	ok(app.toolAdCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": agid, "name": "RSA — mirror jewelry cabinet",
		"status": "PAUSED", "final_urls": []any{"https://oakandvault.cartkit.co/products/mirror-jewelry-cabinet"},
		"headlines": headlines, "descriptions": descriptions,
		"path1": "jewelry", "path2": "cabinet",
	}))
	ad := asMap(asMap(asMap(findExecuteCall(t, pf, "ad_mutate").
		Input["operations"].([]any)[0])["create"])["ad"])
	rsa := asMap(ad["responsiveSearchAd"])
	if len(rsa["headlines"].([]any)) != 12 || len(rsa["descriptions"].([]any)) != 4 {
		t.Fatalf("RSA asset counts wrong: %#v", rsa)
	}

	// 6. Sitelinks.
	step = "ad_extension_create"
	ext := ok(app.toolAdExtensionCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": cid,
		"type": "sitelink", "extensions": oakSitelinks(),
	}))
	if ext["created"] != 4 {
		t.Fatalf("expected 4 sitelinks, got %v", ext["created"])
	}

	// Nothing in the run may activate spend.
	if campaign["status"] != "PAUSED" {
		t.Fatalf("campaign should be created paused: %#v", campaign)
	}
}
