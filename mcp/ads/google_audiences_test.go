package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func TestGoogleCustomSegmentsUseDedicatedServiceAndDurableRequest(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[{"customAudience":{"id":"45","type":"SEARCH","members":[{"memberType":"KEYWORD","keyword":"running shoes"},{"memberType":"URL","url":"https://external-update.example"},{"memberType":"APP","app":"com.example.app"}]}}]}`)
	pf.executeResponses["custom_audience_mutate"] = executeJSON(`{"results":[{"resourceName":"customers/123/customAudiences/45"}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	args := map[string]any{"ad_account_id": id, "name": "Interests", "type": "custom_segment", "keywords": []any{"running shoes"}, "urls": []any{"https://example.com"}, "apps": []any{"com.example.app"}, "segment_mode": "search_terms", "idempotency_key": "custom-1"}
	out, err := app.toolAudienceCreate(ctx, args)
	if err != nil || mcpResultError(out) != nil {
		t.Fatalf("%#v %v", out, err)
	}
	result := asMap(out)
	if result["type"] != "custom_segment" || result["native_id"] != "45" {
		t.Fatalf("registry response %#v", result)
	}
	call := findExecuteCall(t, pf, "custom_audience_mutate")
	create := asMap(asMap(asSlice(call.Input["operations"])[0])["create"])
	if call.Input["customer_id"] != "123" || create["type"] != "SEARCH" || len(asSlice(create["members"])) != 3 {
		t.Fatalf("wrong native segment %#v", call)
	}
	before := len(pf.executeCalls)
	again, err := app.toolAudienceCreate(ctx, args)
	if err != nil || mcpResultError(again) != nil || len(pf.executeCalls) != before {
		t.Fatalf("duplicate dispatch %#v %v", again, err)
	}
	// Updating one collection preserves the other members and the SEARCH mode.
	updated, err := app.toolAudienceUpdate(ctx, map[string]any{"ad_account_id": id, "audience_id": result["id"], "keywords": []any{"trail running"}})
	if err != nil || mcpResultError(updated) != nil {
		t.Fatalf("update %#v %v", updated, err)
	}
	call = findExecuteCall(t, pf, "custom_audience_mutate")
	update := asMap(asMap(asSlice(call.Input["operations"])[0])["update"])
	if update["type"] != "SEARCH" || len(asSlice(update["members"])) != 3 {
		t.Fatalf("update dropped members/mode %#v", update)
	}
	if !containsExactString(update, "https://external-update.example") {
		t.Fatalf("partial edit overwrote external provider changes %#v", update)
	}
}

func TestGoogleCustomSegmentValidationBlocksDispatch(t *testing.T) {
	for _, patch := range []map[string]any{{}, {"keywords": []any{strings.Repeat("long ", 11)}}, {"urls": []any{"https://user:pass@example.com"}}, {"urls": []any{"javascript:alert(1)"}}, {"apps": []any{"https://play.google.com"}}, {"keywords": []any{"valid"}, "segment_mode": "PURCHASE_INTENT"}, {"keywords": []any{"valid"}, "platform_options": map[string]any{"x": true}}} {
		pf := newRecordingPlatform()
		ctx := newAdsCtx(t, pf)
		app := &App{}
		id := seedResourceTestAccount(t, ctx, "google", "123")
		args := map[string]any{"ad_account_id": id, "type": "custom_segment", "name": "Invalid"}
		for k, v := range patch {
			args[k] = v
		}
		out, err := app.toolAudienceCreate(ctx, args)
		if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) > 0 {
			t.Fatalf("unsafe %#v => %#v %v", patch, out, err)
		}
	}
}

func TestGoogleLookalikeResolvesScopedSeedsAndExpansion(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[]}`)
	pf.executeResponses["user_list_mutate"] = executeJSON(`{"results":[{"resourceName":"customers/123/userLists/88"}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	seed := seedAudience(t, app, ctx, id, "google", "77")
	out, err := app.toolAudienceCreateLookalike(ctx, map[string]any{"ad_account_id": id, "name": "Similar", "source_audience_ids": []any{seed.ID}, "country_codes": []any{"es", "US", "ES"}, "expansion_level": "balanced"})
	if err != nil || mcpResultError(out) != nil {
		t.Fatalf("%#v %v", out, err)
	}
	call := findExecuteCall(t, pf, "user_list_mutate")
	info := asMap(asMap(asMap(asSlice(call.Input["operations"])[0])["create"])["lookalikeUserList"])
	if firstString(info, "expansionLevel") != "BALANCED" || strings.Join(info["seedUserListIds"].([]string), ",") != "77" || strings.Join(info["countryCodes"].([]string), ",") != "ES,US" {
		t.Fatalf("native lookalike %#v", info)
	}
	meta := asMap(asMap(out)["metadata"])
	if meta["readiness"] != "unknown" {
		t.Fatalf("fabricated readiness %#v", out)
	}
	for _, args := range []map[string]any{{"source_audience_ids": []any{99999}}, {"source_audience_ids": []any{seed.ID}, "ratio": 0.01}, {"source_audience_ids": []any{seed.ID}, "country_codes": []any{"USA"}}} {
		args["ad_account_id"] = id
		args["type"] = "lookalike"
		args["name"] = "Bad"
		if args["country_codes"] == nil {
			args["country_codes"] = []any{"US"}
		}
		args["expansion_level"] = "narrow"
		before := len(pf.executeCalls)
		out, err = app.toolAudienceCreate(ctx, args)
		if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) != before {
			t.Fatalf("unsafe seed %#v %v", out, err)
		}
	}
}

func TestGoogleLookalikeDuplicateReused(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[{"userList":{"id":"99","name":"Existing","lookalikeUserList":{"seedUserListIds":["77"],"countryCodes":["US"],"expansionLevel":"BROAD"}}}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	seed := seedAudience(t, app, ctx, id, "google", "77")
	out, err := app.toolAudienceCreate(ctx, map[string]any{"ad_account_id": id, "name": "Same definition", "type": "lookalike", "source_audience_ids": []any{seed.ID}, "country_codes": []any{"US"}, "expansion_level": "broad"})
	if err != nil || asMap(out)["reused"] != true || asMap(out)["native_id"] != "99" {
		t.Fatalf("%#v %v", out, err)
	}
	for _, call := range pf.executeCalls {
		if integrationToolMutates(call.Tool) {
			t.Fatalf("duplicate mutation %#v", call)
		}
	}
}

func TestGoogleAudienceDiscoveryPreservesTypesReadOnlyAndOverlappingIDs(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		q := firstString(input, "query")
		switch {
		case strings.Contains(q, "FROM custom_audience"):
			return executeJSON(`{"results":[{"customAudience":{"id":"77","name":"Topics","type":"SEARCH","status":"ENABLED","members":[{"keyword":"running","memberType":"KEYWORD"}]}}]}`), nil
		case strings.Contains(q, "FROM audience"):
			return executeJSON(`{"results":[{"audience":{"id":"77","name":"Group","status":"ENABLED","dimensions":[]}}]}`), nil
		default:
			return executeJSON(`{"results":[{"userList":{"id":"77","name":"Viewers","type":"REMARKETING","membershipStatus":"OPEN","readOnly":true}},{"userList":{"id":"88","name":"Owned","type":"CRM_BASED","membershipStatus":"OPEN"}}]}`), nil
		}
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	out, err := app.toolAudienceRefresh(ctx, map[string]any{"ad_account_id": id})
	if err != nil || mcpResultError(out) != nil {
		t.Fatalf("%#v %v", out, err)
	}
	rows := asMap(out)["data"].([]map[string]any)
	if len(rows) != 4 {
		t.Fatalf("colliding native IDs %#v", out)
	}
	for _, row := range rows {
		if row["type"] == "remarketing" {
			for _, op := range []string{"update", "delete", "members_add"} {
				if containsString(row["capabilities"].([]string), op) {
					t.Fatalf("read-only audience %#v", row)
				}
			}
			if asMap(row["metadata"])["engagement_source"] != "unknown" {
				t.Fatalf("fabricated channel classification %#v", row)
			}
		}
	}
}

func TestGoogleAudienceUsageIncludesNestedGroupAndSeedDependencies(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		q := firstString(input, "query")
		if strings.Contains(q, "FROM audience") {
			return executeJSON(`{"results":[{"audience":{"id":"30","name":"Saved group","dimensions":[{"audienceSegments":{"segments":[{"userList":{"userList":"customers/123/userLists/77"}}]}}]}}]}`), nil
		}
		if strings.Contains(q, "FROM user_list") {
			return executeJSON(`{"results":[{"userList":{"id":"88","name":"Lookalike","lookalikeUserList":{"seedUserListIds":["77"]}}}]}`), nil
		}
		return executeJSON(`{"results":[]}`), nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	seed := seedAudience(t, app, ctx, id, "google", "77")
	out, err := app.toolAudienceDelete(ctx, map[string]any{"ad_account_id": id, "audience_id": seed.ID})
	if err != nil || mcpResultError(out) == nil || !strings.Contains(mcpErrorMessage(asMap(out)), "used") {
		t.Fatalf("unsafe delete %#v %v", out, err)
	}
	queried := false
	for _, call := range pf.executeCalls {
		if strings.Contains(firstString(call.Input, "query"), "ad_group_criterion.audience.audience") {
			queried = true
		}
		if integrationToolMutates(call.Tool) {
			t.Fatalf("unsafe delete mutation %#v", call)
		}
	}
	if !queried {
		t.Fatal("group targeting usage not queried")
	}
}

func TestGoogleGroupLocalIDsStayAccountScoped(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	acct := &adAccount{ID: id, ProjectID: "test-proj", Platform: "google", NativeAccountID: "123"}
	segment, err := app.saveGoogleAudience(ctx, acct, "google_custom_segment", "44", "Segment", "custom_segment", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	native, err := app.googleGroupedSegments(ctx, acct, map[string]any{"segment_audience_ids": []any{segment.ID}})
	if err != nil || toString(asSlice(native["custom_audience_ids"])[0]) != "44" {
		t.Fatalf("%#v %v", native, err)
	}
	_, err = app.googleGroupedSegments(ctx, acct, map[string]any{"segment_audience_ids": []any{9999}})
	if err == nil {
		t.Fatal("foreign segment accepted")
	}
	group, _ := app.saveGoogleAudience(ctx, acct, "google_grouped_audience", "55", "Group", "grouped", map[string]any{}, true)
	target, fail := app.resolveGoogleAudienceTargeting(ctx, acct, map[string]any{"audience_resource_ids": []any{group.ID}})
	if fail != nil || toString(asSlice(target["audience_ids"])[0]) != "55" {
		t.Fatalf("%#v %#v", target, fail)
	}
	_, fail = app.resolveGoogleAudienceTargeting(ctx, acct, map[string]any{"audience_resource_ids": []any{segment.ID}})
	if fail == nil {
		t.Fatal("custom segment used as grouped Audience")
	}
}

func TestGoogleInterestDiscoveryAndYouTubeLimit(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[{"userInterest":{"userInterestId":"80001","name":"Investors","taxonomyType":"AFFINITY"}}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	out, err := app.toolTargetingCatalogSearch(ctx, map[string]any{"ad_account_id": id, "type": "interest", "query": "Investor"})
	if err != nil || mcpResultError(out) != nil {
		t.Fatalf("%#v %v", out, err)
	}
	rows := asMap(out)["data"].([]map[string]any)
	if rows[0]["id"] != "80001" {
		t.Fatalf("unusable interest ID %#v", out)
	}
	out, err = app.toolAudienceCreate(ctx, map[string]any{"ad_account_id": id, "type": "engagement", "name": "Subscribers"})
	if err != nil || !strings.Contains(mcpErrorMessage(asMap(out)), "cannot create") {
		t.Fatalf("false YouTube creation %#v %v", out, err)
	}
}

func TestGoogleAudienceSchemasExposeStructuredFields(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	found := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "audience_create" || tool.Name == "audience_create_lookalike" {
			p := asMap(tool.InputSchema["properties"])
			for _, key := range []string{"keywords", "urls", "apps", "source_audience_ids", "segment_audience_ids", "country_codes", "expansion_level", "idempotency_key"} {
				if p[key] == nil {
					t.Fatalf("%s missing %s", tool.Name, key)
				}
			}
			found[tool.Name] = true
		}
	}
	raw, _ := json.Marshal(found)
	if len(found) != 2 {
		t.Fatalf("schemas %s", raw)
	}
}

func TestGoogleCanonicalDomainSignalsRemainEditable(t *testing.T) {
	spec, err := googleCustomSegment(map[string]any{"urls": []any{"apteva.ai/path"}})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(asSlice(spec["members"])[0])["url"] != "https://apteva.ai/path" {
		t.Fatalf("canonical URL lost %#v", spec)
	}
}

func TestGoogleLookalikeMutationsAreBlockedDespiteReadOnlyFalse(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	acct := &adAccount{ID: id, ProjectID: "test-proj", NativeAccountID: "123", Platform: "google"}
	r, err := a.saveGoogleAudience(ctx, acct, "google_user_list", "88", "Test lookalike", "lookalike", map[string]any{"readOnly": false}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Persisted pre-upgrade capabilities must not re-enable unsupported mutations.
	_, err = ctx.AppDB().Exec(`UPDATE ad_resources SET capabilities_json='["target","get","update","delete","usage"]' WHERE id=?`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.toolAudienceDelete(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID})
	if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) > 0 {
		t.Fatalf("unsafe deletion %#v %v", out, err)
	}
	out, err = a.toolAudienceUpdate(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID, "name": "Changed"})
	if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) > 0 {
		t.Fatalf("unsafe update %#v %v", out, err)
	}
}

func TestGoogleReadOnlyAndNonCustomerListsRejectMemberUploads(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	acct := &adAccount{ID: id, ProjectID: "test-proj", NativeAccountID: "123", Platform: "google"}
	r, err := a.saveGoogleAudience(ctx, acct, "google_custom_segment", "88", "Topics", "custom_segment", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.toolAudienceMembersSync(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID, "operation": "add", "source": map[string]any{"kind": "storage", "ref": "a.csv"}, "idempotency_key": "bad-upload"})
	if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) > 0 {
		t.Fatalf("unsafe member upload %#v %v", out, err)
	}
}
