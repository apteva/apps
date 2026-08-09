package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func seedSelectedAccount(t *testing.T, ctx *sdk.AppCtx, nativeID string) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, management_mode)
		 VALUES ('test-proj','meta',7,?,'Selected account','selected')`,
		nativeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func campaignScopePlatform() *recordingPlatform {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		data := `{"data":[]}`
		if tool == "campaign_list" {
			data = `{"data":[
				{"id":"100","name":"Brand search","status":"ACTIVE","objective":"traffic"},
				{"id":"200","name":"Prospecting","status":"PAUSED","objective":"sales"}
			]}`
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(data)}, nil
	}
	return pf
}

func TestCampaignImportCandidatesAreTransientUntilSelected(t *testing.T) {
	pf := campaignScopePlatform()
	ctx := newAdsCtx(t, pf)
	accountID := seedSelectedAccount(t, ctx, "act_scope")
	app := &App{}

	out, err := app.toolCampaignImportCandidatesList(ctx, map[string]any{"ad_account_id": accountID})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.(map[string]any)["data"].([]map[string]any)
	if len(rows) != 2 || rows[0]["managed"] != false {
		t.Fatalf("candidates=%#v", rows)
	}
	var stored int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_entities WHERE ad_account_id=?`, accountID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("candidate discovery persisted %d entities", stored)
	}
}

func TestCampaignImportScopesListsAndMutations(t *testing.T) {
	pf := campaignScopePlatform()
	ctx := newAdsCtx(t, pf)
	accountID := seedSelectedAccount(t, ctx, "act_scope")
	app := &App{}

	if out, err := app.toolCampaignImport(ctx, map[string]any{"ad_account_id": accountID, "campaign_ids": []any{"100"}}); err != nil || mcpResultError(out) != nil {
		t.Fatalf("import out=%#v err=%v", out, err)
	}
	listed, err := app.toolCampaignList(ctx, map[string]any{"ad_account_id": accountID})
	if err != nil {
		t.Fatal(err)
	}
	rows := listed.(map[string]any)["data"].([]map[string]any)
	if len(rows) != 1 || rows[0]["id"] != "100" {
		t.Fatalf("scoped campaigns=%#v", rows)
	}

	pf.mu.Lock()
	before := len(pf.executeCalls)
	pf.mu.Unlock()
	rejected, err := app.toolCampaignPause(ctx, map[string]any{"ad_account_id": accountID, "campaign_id": "200"})
	if err != nil {
		t.Fatal(err)
	}
	if mcpResultError(rejected) == nil || !strings.Contains(mcpErrorTextValue(rejected.(map[string]any)), "import it first") {
		t.Fatalf("unmanaged mutation result=%#v", rejected)
	}
	pf.mu.Lock()
	after := len(pf.executeCalls)
	pf.mu.Unlock()
	if after != before {
		t.Fatalf("unmanaged mutation reached provider: before=%d after=%d", before, after)
	}
}

func TestCampaignRemoveOnlyClearsProjectState(t *testing.T) {
	pf := campaignScopePlatform()
	ctx := newAdsCtx(t, pf)
	accountID := seedSelectedAccount(t, ctx, "act_scope")
	app := &App{}
	if out, _ := app.toolCampaignImport(ctx, map[string]any{"ad_account_id": accountID, "campaign_ids": []any{"100"}}); mcpResultError(out) != nil {
		t.Fatalf("import=%#v", out)
	}
	pf.mu.Lock()
	before := len(pf.executeCalls)
	pf.mu.Unlock()

	out, err := app.toolCampaignRemoveFromProject(ctx, map[string]any{"ad_account_id": accountID, "campaign_id": "100"})
	if err != nil || out.(map[string]any)["upstream_deleted"] != false {
		t.Fatalf("remove out=%#v err=%v", out, err)
	}
	pf.mu.Lock()
	after := len(pf.executeCalls)
	pf.mu.Unlock()
	if after != before {
		t.Fatalf("remove-from-project called provider: before=%d after=%d", before, after)
	}
	var stored int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_entities WHERE ad_account_id=?`, accountID).Scan(&stored)
	if stored != 0 {
		t.Fatalf("campaign state remains after removal: %d", stored)
	}
}

func TestCampaignImportRejectsExclusiveClaimFromAnotherProject(t *testing.T) {
	pf := campaignScopePlatform()
	ctx := newAdsCtx(t, pf)
	accountID := seedSelectedAccount(t, ctx, "act_shared")
	other, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, management_mode)
		 VALUES ('other-project','meta',7,'act_shared','Other','selected')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := other.LastInsertId()
	_, err = ctx.AppDB().Exec(
		`INSERT INTO ad_entities (project_id, ad_account_id, platform, level, native_entity_id, campaign_id, is_managed, managed_source, last_seen_at)
		 VALUES ('other-project',?,'meta','campaign','100','100',1,'imported',CURRENT_TIMESTAMP)`,
		otherID,
	)
	if err != nil {
		t.Fatal(err)
	}

	out, err := (&App{}).toolCampaignImport(ctx, map[string]any{"ad_account_id": accountID, "campaign_ids": []any{"100"}})
	if err != nil {
		t.Fatal(err)
	}
	if mcpResultError(out) == nil || !strings.Contains(mcpErrorTextValue(out.(map[string]any)), "another project") {
		t.Fatalf("exclusive claim result=%#v", out)
	}
}

func TestSelectedPerformanceAlwaysFiltersManagedCampaigns(t *testing.T) {
	pf := campaignScopePlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "insights_get" {
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[]}`)}, nil
		}
		filtering := toString(input["filtering"])
		if !strings.Contains(filtering, `"100"`) || strings.Contains(filtering, `"200"`) {
			t.Fatalf("unscoped performance filter=%s", filtering)
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[]}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := seedSelectedAccount(t, ctx, "act_scope")
	acct, _, errOut := (&App{}).resolveAdAccount(ctx, map[string]any{"ad_account_id": accountID})
	if errOut != nil {
		t.Fatal(mcpErrorTextValue(errOut))
	}
	if err := (&App{}).upsertManagedCampaign(ctx, acct, map[string]any{"id": "100", "name": "Managed"}, "imported"); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolPerformanceGet(ctx, map[string]any{
		"ad_account_id": accountID, "level": "campaign", "date_from": "2026-08-01", "date_to": "2026-08-02",
	})
	if err != nil || mcpResultError(out) != nil {
		t.Fatalf("performance out=%#v err=%v", out, err)
	}
}
