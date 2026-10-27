package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Real provider reads, local fixture annotations only. This test cannot attest
// a business mapping such as HGV: that requires checking source configuration.
func TestAudienceProvenanceLiveReadback(t *testing.T) {
	if os.Getenv("ADS_AUDIENCE_PROVENANCE_LIVE") != "1" {
		t.Skip("set ADS_AUDIENCE_PROVENANCE_LIVE=1 for read-only real provider validation")
	}
	home, _ := os.UserHomeDir()
	path := os.Getenv("ADS_LIVE_CONFIG")
	if path == "" {
		path = filepath.Join(home, ".apteva-prod", "apteva.json")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var config map[string]any
	if e = json.Unmarshal(raw, &config); e != nil {
		t.Fatal(e)
	}
	key := firstString(config, "api_key", "apiKey")
	if key == "" {
		t.Fatal("missing API key")
	}
	base := os.Getenv("ADS_LIVE_SERVER")
	if base == "" {
		base = "https://agents.schwartzindustries.com"
	}
	conn := os.Getenv("ADS_LIVE_CONNECTION")
	if conn == "" {
		conn = "103"
	}
	customer := os.Getenv("ADS_LIVE_CUSTOMER")
	if customer == "" {
		customer = "1875399872"
	}
	requests := 0
	pf := newRecordingPlatform()
	client := &http.Client{Timeout: 45 * time.Second}
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "search" {
			t.Fatalf("live provenance test attempted non-read tool %s", tool)
		}
		requests++
		payload, _ := json.Marshal(map[string]any{"tool": tool, "input": input})
		req, e := http.NewRequest("POST", base+"/api/connections/"+conn+"/execute", bytes.NewReader(payload))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		body, e := io.ReadAll(resp.Body)
		if e != nil {
			return nil, e
		}
		var result sdk.ExecuteResult
		if e = json.Unmarshal(body, &result); e != nil {
			return nil, e
		}
		return &result, nil
	}
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", customer)
	acct := &adAccount{ID: id, ProjectID: "test-proj", Platform: "google", NativeAccountID: customer}
	first, e := a.toolAudienceRefresh(ctx, map[string]any{"ad_account_id": id})
	if e != nil || len(asMap(asMap(first)["refresh_errors"])) > 0 {
		t.Fatalf("discovery failed %#v %v", first, e)
	}
	var selected int64
	neutral := 0
	for _, r := range asMap(first)["data"].([]map[string]any) {
		if r["type"] == "rule_based" {
			neutral++
			selected = int64(intArg(r, "id", 0))
			if asMap(r["provenance"])["status"] != "unknown" {
				t.Fatal("provider name inferred a verified source")
			}
		}
	}
	if selected == 0 {
		t.Fatal("no real rule-based list available for regression validation")
	}
	before := requests
	// This is deliberately a test-fixture association in an isolated test DB.
	// No source claim is made about the real user's channel or business.
	out, e := a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": id, "audience_id": selected, "confirmed": true, "provenance": map[string]any{"source_platform": "validation_fixture", "source_asset_id": "isolated-local-test", "interaction_type": "storage_regression", "verification_evidence": "Synthetic local test association for persistence validation, not a business/channel assertion."}})
	if e != nil || mcpResultError(out) != nil || requests != before {
		t.Fatalf("local-only annotation failed %#v %v", out, e)
	}
	second, e := a.toolAudienceRefresh(ctx, map[string]any{"ad_account_id": id})
	if e != nil || len(asMap(asMap(second)["refresh_errors"])) > 0 {
		t.Fatal("second real refresh failed")
	}
	a = &App{}
	saved, e := a.getResource(ctx, acct, selected)
	if e != nil || verifiedAudienceProvenance(*saved)["source_asset_id"] != "isolated-local-test" {
		t.Fatal("real refresh overwrote local provenance")
	}
	filtered, e := a.toolAudienceList(ctx, map[string]any{"ad_account_id": id, "refresh": false, "source_asset_id": "isolated-local-test", "provenance_status": "verified"})
	if e != nil || len(asMap(filtered)["data"].([]map[string]any)) != 1 {
		t.Fatal("verified source filter failed")
	}
	cleared, e := a.toolAudienceProvenanceSet(ctx, map[string]any{"ad_account_id": id, "audience_id": selected, "confirmed": true, "clear": true})
	if e != nil || asMap(asMap(cleared)["provenance"])["status"] != "unknown" {
		t.Fatal("clear failed")
	}
	t.Logf("PASS: %d real read requests; %d rule-based lists labelled neutrally; isolated local provenance survived refresh/new App, filtered and cleared; no provider mutation", requests, neutral)
}
