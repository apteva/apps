package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func provenanceFixture() map[string]any {
	return map[string]any{"source_platform": "youtube", "source_channel_id": "UC_fixture", "interaction_type": "video_view", "verification_evidence": "Checked the exact list ID and selected channel in source configuration.", "verification_reference": "test screenshot reference"}
}

func TestAudienceProvenanceSurvivesRefreshUpsertAndNewApp(t *testing.T) {
	for _, platform := range []string{"google", "meta", "x", "reddit"} {
		t.Run(platform, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newAdsCtx(t, pf)
			a := &App{}
			id := seedResourceTestAccount(t, ctx, platform, "123")
			r := seedAudience(t, a, ctx, id, platform, "77")
			acct := &adAccount{ID: id, ProjectID: "test-proj", Platform: platform, NativeAccountID: "123"}
			out, e := a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID, "confirmed": true, "provenance": provenanceFixture()})
			if e != nil || mcpResultError(out) != nil {
				t.Fatalf("%#v %v", out, e)
			}
			before := asMap(asMap(out)["provenance"])
			if _, e = time.Parse(time.RFC3339, firstString(before, "verified_at")); e != nil {
				t.Fatal(e)
			}
			// Refresh replaces provider metadata and changes the provider's name. Local
			// verification must remain tied to the same scoped resource identity.
			refreshed := discoveredResource{Kind: resourceAudience, ProviderType: r.ProviderType, NativeID: r.NativeID, DisplayName: "Renamed upstream", Status: "active", Capabilities: r.Capabilities, Metadata: map[string]any{"type": "customer_list", "description": "Fresh provider metadata"}}
			if e = a.replaceResources(ctx, acct, resourceAudience, nil); e != nil {
				t.Fatal(e)
			}
			if e = a.replaceResources(ctx, acct, resourceAudience, []discoveredResource{refreshed}); e != nil {
				t.Fatal(e)
			}
			if _, e = a.upsertResource(ctx, acct, refreshed); e != nil {
				t.Fatal(e)
			}
			a = &App{}
			got, e := a.toolAudienceGet(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID})
			if e != nil {
				t.Fatal(e)
			}
			p := asMap(asMap(got)["provenance"])
			if p["verified_at"] != before["verified_at"] || p["source_channel_id"] != "UC_fixture" || p["status"] != "verified" {
				t.Fatalf("refresh lost association %#v", got)
			}
			if asMap(got)["name"] != "Renamed upstream" {
				t.Fatalf("provider metadata did not refresh %#v", got)
			}
			resources, e := a.toolResourceList(ctx, map[string]any{"ad_account_id": id, "kind": resourceAudience})
			if e != nil {
				t.Fatal(e)
			}
			rows := asMap(resources)["data"].([]map[string]any)
			if asMap(rows[0]["provenance"])["source_channel_id"] != "UC_fixture" {
				t.Fatalf("generic resource read missing provenance %#v", resources)
			}
			if len(pf.executeCalls) != 0 {
				t.Fatal("local provenance dispatched a provider mutation")
			}
		})
	}
}

func TestProvenanceRequiresAttestationEvidenceAndScope(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	other := seedResourceTestAccount(t, ctx, "google", "456")
	r := seedAudience(t, a, ctx, id, "google", "77")
	cases := []map[string]any{{"provenance": provenanceFixture()}, {"confirmed": true, "provenance": map[string]any{"source_platform": "youtube", "source_channel_id": "UC_fixture", "interaction_type": "video_view"}}, {"confirmed": true, "provenance": map[string]any{"source_platform": "youtube", "interaction_type": "video_view", "verification_evidence": "Checked source"}}, {"confirmed": true, "clear": true, "provenance": provenanceFixture()}}
	for _, args := range cases {
		args["ad_account_id"] = id
		args["audience_id"] = r.ID
		out, e := a.toolAudienceProvenanceSet(ctx, args)
		if e != nil || mcpResultError(out) == nil {
			t.Fatalf("unsafe attestation %#v %v", out, e)
		}
	}
	out, e := a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": other, "audience_id": r.ID, "confirmed": true, "provenance": provenanceFixture()})
	if e != nil || mcpResultError(out) == nil {
		t.Fatalf("cross-account provenance %#v %v", out, e)
	}
	out, e = a.toolAudienceProvenanceSet(ctx.WithProject("other-project"), map[string]any{"ad_account_id": id, "audience_id": r.ID, "confirmed": true, "provenance": provenanceFixture()})
	if e != nil || mcpResultError(out) == nil {
		t.Fatalf("cross-project provenance %#v %v", out, e)
	}
	raw := provenanceFixture()
	raw["status"] = "provider_verified"
	raw["verification_method"] = "provider"
	raw["verified_at"] = "1999-01-01T00:00:00Z"
	out, e = a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID, "confirmed": true, "provenance": raw})
	if e != nil || mcpResultError(out) != nil {
		t.Fatalf("%#v %v", out, e)
	}
	p := asMap(asMap(out)["provenance"])
	if p["verification_method"] != "manual_attestation" || p["status"] != "verified" || p["verified_at"] == raw["verified_at"] {
		t.Fatalf("forged verification metadata %#v", p)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("provider call during local attestation")
	}
}

func TestProvenanceWorksForReadOnlyAndClearIsDurable(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	acct := &adAccount{ID: id, ProjectID: "test-proj", Platform: "google"}
	r, e := a.upsertResource(ctx, acct, discoveredResource{Kind: resourceAudience, ProviderType: "google_user_list", NativeID: "77", Status: "active", Metadata: map[string]any{"type": "rule_based", "readOnly": true}, Capabilities: []string{"get", "target", "usage"}})
	if e != nil {
		t.Fatal(e)
	}
	args := map[string]any{"ad_account_id": id, "audience_id": r.ID, "confirmed": true, "provenance": provenanceFixture()}
	out, e := a.toolAudienceProvenanceSet(ctx, args)
	if e != nil || mcpResultError(out) != nil {
		t.Fatalf("read-only audience could not be annotated %#v %v", out, e)
	}
	out, e = a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": id, "audience_id": r.ID, "confirmed": true, "clear": true})
	if e != nil || asMap(asMap(out)["provenance"])["status"] != "unknown" {
		t.Fatalf("clear %#v %v", out, e)
	}
	fetched, _ := a.getResource(ctx, acct, r.ID)
	if verifiedAudienceProvenance(*fetched)["status"] != "unknown" {
		t.Fatal("clear not persisted")
	}
	var managed int
	_ = ctx.AppDB().QueryRow(`SELECT managed_by_app FROM ad_resources WHERE id=?`, r.ID).Scan(&managed)
	if managed != 0 {
		t.Fatal("local annotation claimed management of provider audience")
	}
}

func TestRuleBasedDiscoveryAndCachedLabelsDoNotInventSource(t *testing.T) {
	for _, metadata := range []map[string]any{{"type": "website", "native_type": "RULE_BASED"}, {"type": "RULE_BASED"}, {"type": "rule_based"}} {
		r := adResource{ProviderType: "google_user_list", DisplayName: "HGV YouTube viewers", Metadata: metadata}
		out := normalizedAudience("google", r)
		if asMap(out["metadata"])["type"] != "rule_based" {
			t.Fatalf("stale metadata still claims website %#v", out)
		}
		if out["type"] != "rule_based" || out["source_label"] != "Source unverified" || asMap(out["provenance"])["status"] != "unknown" {
			t.Fatalf("source inferred from name/type %#v", out)
		}
	}
	if normalizedAudience("meta", adResource{Metadata: map[string]any{"subtype": "WEBSITE"}})["type"] != "website" {
		t.Fatal("explicit Meta website classification regressed")
	}
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, _ string, args map[string]any) (*sdk.ExecuteResult, error) {
		if strings.Contains(firstString(args, "query"), "FROM user_list") {
			return executeJSON(`{"results":[{"userList":{"id":"77","type":"RULE_BASED","name":"Youtube list","membershipStatus":"OPEN"}}]}`), nil
		}
		return executeJSON(`{"results":[]}`), nil
	}
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	out, e := a.toolAudienceRefresh(ctx, map[string]any{"ad_account_id": id})
	if e != nil {
		t.Fatal(e)
	}
	rows := asMap(out)["data"].([]map[string]any)
	if len(rows) != 1 || rows[0]["type"] != "rule_based" {
		t.Fatalf("bad discovery %#v", out)
	}
	// The neutral type remains a valid first-party seed; no audience names are used.
	_, e = a.googleLookalikeInfo(ctx, &adAccount{ID: id, ProjectID: "test-proj", Platform: "google", NativeAccountID: "123"}, map[string]any{"source_audience_ids": []any{rows[0]["id"]}, "country_codes": []any{"US"}, "expansion_level": "narrow"})
	if e != nil {
		t.Fatalf("neutral type broke lookalike eligibility: %v", e)
	}
}

func TestAudienceProvenanceFilteringIgnoresUnverifiedNames(t *testing.T) {
	ctx := newAdsCtx(t, newRecordingPlatform())
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "meta", "act_123")
	verified := seedAudience(t, a, ctx, id, "meta", "77")
	seedAudience(t, a, ctx, id, "meta", "88")
	_, e := a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": id, "audience_id": verified.ID, "confirmed": true, "provenance": provenanceFixture()})
	if e != nil {
		t.Fatal(e)
	}
	for _, filter := range []map[string]any{{"source_channel_id": "UC_fixture"}, {"source_platform": "YOUTUBE", "interaction_type": "video_view"}, {"provenance_status": "unknown"}} {
		filter["ad_account_id"] = id
		filter["refresh"] = false
		out, e := a.toolAudienceList(ctx, filter)
		if e != nil {
			t.Fatal(e)
		}
		rows := asMap(out)["data"].([]map[string]any)
		if len(rows) != 1 {
			t.Fatalf("wrong filtered audiences %#v", out)
		}
	}
	r := adResource{Metadata: map[string]any{"provenance": map[string]any{"status": "verified", "source_channel_id": "UC_fixture"}}, DisplayName: "UC_fixture viewers"}
	if audienceMatchesProvenance(r, map[string]any{"source_channel_id": "UC_fixture"}) {
		t.Fatal("provider metadata/name spoofed local verification")
	}
}

func TestProvenanceToolRegisteredInManifestAndSchema(t *testing.T) {
	a := &App{}
	found := false
	for _, tool := range a.MCPTools() {
		if tool.Name == "audience_provenance_set" {
			found = true
			raw, _ := json.Marshal(tool.InputSchema)
			for _, key := range []string{"verification_evidence", "confirmed", "source_channel_id", "clear"} {
				if !strings.Contains(string(raw), key) {
					t.Fatalf("missing field %s", key)
				}
			}
		}
	}
	if !found || !strings.Contains(manifestYAML, "audience_provenance_set") {
		t.Fatal("provenance not exposed")
	}
}

func TestLegacyGroupListCannotSilentlyIgnoreProvenanceFilter(t *testing.T) {
	a := &App{}
	out, e := a.toolAudienceList(newAdsCtx(t, newRecordingPlatform()), map[string]any{"audience_type": "grouped", "source_channel_id": "UC_fixture"})
	if e != nil || mcpResultError(out) == nil {
		t.Fatalf("filter silently ignored %#v %v", out, e)
	}
}
