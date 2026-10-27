package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in test: high-level app handlers -> the user's existing integration -> Google.
// Delivery fixtures are PAUSED and campaigns/budgets are removed on completion.
// Reusable assets/audiences are retained where Google API v23 provides no removal.
// No activation is attempted.
func TestGoogleAudiencesLive(t *testing.T) {
	if os.Getenv("ADS_GOOGLE_AUDIENCES_LIVE") != "1" {
		t.Skip("set ADS_GOOGLE_AUDIENCES_LIVE=1 to run paused real-provider validation")
	}
	cf := os.Getenv("ADS_LIVE_CONFIG")
	if cf == "" {
		home, _ := os.UserHomeDir()
		cf = filepath.Join(home, ".apteva-prod", "apteva.json")
	}
	raw, err := os.ReadFile(cf)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	key := firstString(config, "api_key", "apiKey")
	if key == "" {
		t.Fatal("missing API key")
	}
	base := os.Getenv("ADS_LIVE_SERVER")
	if base == "" {
		base = "https://agents.schwartzindustries.com"
	}
	connection := os.Getenv("ADS_LIVE_CONNECTION")
	if connection == "" {
		connection = "103"
	}
	customer := os.Getenv("ADS_LIVE_CUSTOMER")
	if customer == "" {
		customer = "1875399872"
	}
	client := &http.Client{Timeout: 60 * time.Second}
	pf := newRecordingPlatform()
	evidence := []any{}
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "custom_audience_mutate" {
			executor := os.Getenv("ADS_LIVE_CUSTOM_EXECUTOR")
			if executor == "" {
				return nil, fmt.Errorf("ADS_LIVE_CUSTOM_EXECUTOR required")
			}
			payload, _ := json.Marshal(input)
			cmd := exec.Command("bun", executor)
			cmd.Stdin = bytes.NewReader(payload)
			cmd.Env = os.Environ()
			body, e := cmd.Output()
			if e != nil {
				return nil, fmt.Errorf("custom integration executor failed: %w", e)
			}
			var envelope struct {
				Success bool            `json:"success"`
				Status  int             `json:"status"`
				Data    json.RawMessage `json:"data"`
			}
			if e = json.Unmarshal(body, &envelope); e != nil {
				return nil, fmt.Errorf("invalid custom integration response")
			}
			evidence = append(evidence, map[string]any{"tool": tool, "input": input, "success": envelope.Success, "status": envelope.Status, "data": envelope.Data})
			return &sdk.ExecuteResult{Success: envelope.Success, Status: envelope.Status, Data: envelope.Data}, nil
		}
		payload, _ := json.Marshal(map[string]any{"tool": tool, "input": input})
		req, err := http.NewRequest("POST", base+"/api/connections/"+connection+"/execute", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Success bool              `json:"success"`
			Status  int               `json:"status"`
			Data    json.RawMessage   `json:"data"`
			Headers map[string]string `json:"headers"`
		}
		if err = json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("integration HTTP %d: %s", resp.StatusCode, body)
		}
		evidence = append(evidence, map[string]any{"tool": tool, "input": input, "success": envelope.Success, "status": envelope.Status, "data": json.RawMessage(envelope.Data), "request_id": envelope.Headers["Request-Id"]})
		return &sdk.ExecuteResult{Success: envelope.Success, Status: envelope.Status, Data: envelope.Data}, nil
	}
	t.Cleanup(func() {
		path := os.Getenv("ADS_LIVE_EVIDENCE")
		if path == "" {
			path = "/private/tmp/ads-google-audiences-live.json"
		}
		data, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Error(err)
		}
		t.Log("Real-provider evidence:", path)
	})
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", customer)
	customerData, customerOut := a.execIntegrationTool(ctx, &adAccount{NativeAccountID: customer, Platform: "google"}, "search", map[string]any{"customer_id": customer, "query": "SELECT customer.id, customer.currency_code, customer.time_zone FROM customer"})
	if customerOut != nil {
		t.Fatal(customerOut)
	}
	customerRows := resultRows(customerData)
	if len(customerRows) != 1 {
		t.Fatal("customer context unavailable")
	}
	nativeCustomer := mapAt(customerRows[0], "customer")
	if _, err := ctx.AppDB().Exec(`UPDATE ad_accounts SET currency=?,timezone_name=? WHERE id=?`, firstString(nativeCustomer, "currencyCode"), firstString(nativeCustomer, "timeZone"), account); err != nil {
		t.Fatal(err)
	}

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		args["ad_account_id"] = account
		for _, tool := range a.MCPTools() {
			if tool.Name == name {
				out, e := tool.Handler(ctx, args)
				if e != nil || mcpResultError(out) != nil {
					t.Fatalf("%s: %#v %v", name, out, e)
				}
				return asMap(out)
			}
		}
		t.Fatalf("missing %s", name)
		return nil
	}
	listed := call("audience_refresh", map[string]any{})
	if len(asMap(listed["refresh_errors"])) > 0 {
		t.Fatalf("refresh failed %#v", listed["refresh_errors"])
	}
	call("targeting_catalog_search", map[string]any{"type": "interest", "query": "Invest", "limit": 5})
	stamp := time.Now().UTC().Format("20060102-150405")
	custom := call("audience_create", map[string]any{"name": "Apteva audience validation " + stamp, "type": "custom_segment", "keywords": []any{"software development", "business software"}, "urls": []any{"https://apteva.ai"}, "apps": []any{"com.google.android.apps.adwords"}, "segment_mode": "interests", "idempotency_key": "aud-custom-" + stamp})
	customDeleted := false
	t.Cleanup(func() {
		if !customDeleted {
			out, e := a.toolAudienceDelete(ctx, map[string]any{"ad_account_id": account, "audience_id": custom["id"]})
			if e != nil || mcpResultError(out) != nil {
				t.Errorf("custom segment cleanup %#v %v", out, e)
			}
		}
	})

	call("audience_update", map[string]any{"audience_id": custom["id"], "description": "Validated structured segment update", "keywords": []any{"automation software"}})
	call("audience_usage_get", map[string]any{"audience_id": custom["id"]})
	call("audience_delete", map[string]any{"audience_id": custom["id"]})
	customDeleted = true
	// Reuse an existing segment: Google Audience resources have no remove API.
	refreshed := call("audience_refresh", map[string]any{})
	var existing any
	var seed any
	for _, row := range refreshed["data"].([]map[string]any) {
		if row["type"] == "custom_segment" {
			existing = row["id"]
		}
		if row["type"] == "website" && row["status"] == "active" {
			seed = row["id"]
		}
	}
	if existing == nil {
		t.Fatal("existing custom segment required for grouped targeting validation")
	}
	var group map[string]any
	if reuse := os.Getenv("ADS_LIVE_GROUP_ID"); reuse != "" {
		for _, row := range refreshed["data"].([]map[string]any) {
			if row["type"] == "grouped" && row["native_id"] == reuse {
				group = row
			}
		}
		if group == nil {
			t.Fatal("ADS_LIVE_GROUP_ID was not discovered in this account")
		}
	} else {
		group = call("audience_create", map[string]any{"name": "Apteva audience group validation " + stamp, "type": "grouped", "segment_audience_ids": []any{existing}, "user_interest_ids": []any{"80001"}, "idempotency_key": "aud-group-" + stamp})
	}
	call("audience_update", map[string]any{"audience_id": group["id"], "description": "Validated grouped audience update", "segment_audience_ids": []any{existing}, "user_interest_ids": []any{"80001"}, "replace_segments": true})
	t.Log("Reusable grouped audience retained (no Google remove API):", group["native_id"])
	campaign := call("campaign_create", map[string]any{"name": "Apteva audience targeting validation " + stamp, "channel_type": "demand_gen", "objective": "traffic", "optimization_goal": "link_clicks", "daily_budget_cents": 500, "status": "PAUSED", "idempotency_key": "aud-campaign-" + stamp})
	cid := firstString(campaign, "id")
	if cid == "" {
		t.Fatal("campaign ID missing")
	}
	t.Cleanup(func() {
		out, e := a.toolCampaignDelete(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid})
		if e != nil || mcpResultError(out) != nil {
			t.Errorf("campaign cleanup %#v %v", out, e)
		} else {
			t.Log("Removed paused campaign", cid)
			budget, e := a.toolBudgetDelete(ctx, map[string]any{"ad_account_id": account, "budget_ids": []any{campaign["budget_id"]}})
			if e != nil || mcpResultError(budget) != nil {
				t.Errorf("budget cleanup %#v %v", budget, e)
			}
		}
	})
	adGroup := call("adset_create", map[string]any{"campaign_id": cid, "name": "Scoped audience targeting", "audience_resource_ids": []any{group["id"]}, "placements": []any{"youtube_in_feed"}, "status": "PAUSED", "idempotency_key": "aud-adgroup-" + stamp})
	call("adset_configuration_get", map[string]any{"adset_id": adGroup["id"]})
	call("audience_usage_get", map[string]any{"audience_id": group["id"]})
	if seed == nil {
		t.Fatal("existing first-party website seed required")
	}
	args := map[string]any{"ad_account_id": account, "name": "Apteva lookalike validation " + stamp, "type": "lookalike", "source_audience_ids": []any{seed}, "country_codes": []any{"ES"}, "expansion_level": "narrow", "idempotency_key": "aud-lookalike-" + stamp}
	result, e := a.toolAudienceCreate(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	if fail := mcpResultError(result); fail != nil {
		if fail["code"] != "audience_ineligible" {
			t.Fatalf("unexpected lookalike failure %#v", fail)
		}
		t.Log("Current account seed eligibility restriction:", fail)
	} else {
		t.Log("Created/reused lookalike:", asMap(result)["native_id"])
		call("audience_get", map[string]any{"audience_id": asMap(result)["id"]})
		before := len(evidence)
		denied, e := a.toolAudienceDelete(ctx, map[string]any{"ad_account_id": account, "audience_id": asMap(result)["id"]})
		if e != nil || mcpResultError(denied) == nil || len(evidence) != before {
			t.Fatalf("lookalike deletion must be blocked locally in v23: %#v %v", denied, e)
		}
		t.Log("Immutable lookalike retained: Google v23 rejects update/removal", asMap(result)["native_id"])
	}
	t.Log("Audience lifecycle/discovery and paused targeting validated; paid delivery was never enabled")
}
