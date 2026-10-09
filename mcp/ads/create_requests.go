package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"sync/atomic"
)

// A lost response after creation is ambiguous. Keep a durable reservation
// instead of retrying a non-idempotent provider create across process restarts.
func (a *App) withCreateRequest(ctx *sdk.AppCtx, args map[string]any, operation string, run func(*sdk.AppCtx, map[string]any) (any, error)) (any, error) {
	key := strings.TrimSpace(stringArgAny(args, "idempotency_key"))
	if key == "" {
		return run(ctx, args)
	}
	if len(key) > 200 {
		return mcpError("idempotency_key must be at most 200 characters"), nil
	}
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	public := mobilePublicArgs(args)
	delete(public, "idempotency_key")
	raw, err := json.Marshal(public)
	if err != nil {
		return mcpError("invalid campaign arguments"), nil
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	inserted, err := ctx.AppDB().Exec(`INSERT INTO ad_create_requests(project_id,ad_account_id,request_key,request_hash,status) VALUES(?,?,?,?, 'dispatching') ON CONFLICT DO NOTHING`, acct.ProjectID, acct.ID, operation+":"+key, hash)
	if err != nil {
		return nil, err
	}
	n, _ := inserted.RowsAffected()
	if n != 1 {
		var storedHash, status, result string
		err = ctx.AppDB().QueryRow(`SELECT request_hash,status,result_json FROM ad_create_requests WHERE project_id=? AND ad_account_id=? AND request_key=?`, acct.ProjectID, acct.ID, operation+":"+key).Scan(&storedHash, &status, &result)
		if err != nil {
			return nil, err
		}
		if storedHash != hash {
			return mcpError("idempotency_key was used with different arguments"), nil
		}
		if status == "complete" {
			var parsed any
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				return nil, err
			}
			return parsed, nil
		}
		out := mcpError("campaign creation is in progress or its outcome is unknown; reconcile before submitting a new key")
		out["code"] = "creation_outcome_unknown"
		out["idempotency_key"] = key
		return out, nil
	}
	dispatch := &createDispatch{}
	runCtx := ctx.WithProject(acct.ProjectID)
	a.createDispatches.Store(runCtx, dispatch)
	defer a.createDispatches.Delete(runCtx)
	resultOut, createErr := run(runCtx, args)
	if !dispatch.started.Load() && (createErr != nil || mcpResultError(resultOut) != nil) {
		// Preflight validation/read failures have no provider-side creation to reconcile.
		_, err := ctx.AppDB().Exec(`DELETE FROM ad_create_requests WHERE project_id=? AND ad_account_id=? AND request_key=?`, acct.ProjectID, acct.ID, operation+":"+key)
		if err != nil {
			return nil, err
		}
	}
	if createErr != nil {
		return nil, createErr
	}
	if failure := mcpResultError(resultOut); failure != nil {
		return failure, nil
	}
	encoded, err := json.Marshal(resultOut)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`UPDATE ad_create_requests SET status='complete',result_json=? WHERE project_id=? AND ad_account_id=? AND request_key=?`, string(encoded), acct.ProjectID, acct.ID, operation+":"+key)
	if err != nil {
		return nil, err
	}
	return resultOut, nil
}

func (a *App) toolCampaignCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.withCreateRequest(ctx, args, "campaign", a.toolCampaignCreateOnce)
}
func (a *App) toolAdSetCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.withCreateRequest(ctx, args, "ad_group", a.toolAdSetCreateOnce)
}
func (a *App) toolCreativeCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.withCreateRequest(ctx, args, "creative", a.toolCreativeCreateOnce)
}
func (a *App) toolAdCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.withCreateRequest(ctx, args, "ad", a.toolAdCreateOnce)
}

type createDispatch struct{ started atomic.Bool }

func integrationToolMutates(tool string) bool {
	for _, prefix := range []string{"get_", "list_", "search", "validate_"} {
		if strings.HasPrefix(tool, prefix) {
			return false
		}
	}
	for _, suffix := range []string{"_get", "_list", "_search", "_status", "_report"} {
		if strings.HasSuffix(tool, suffix) {
			return false
		}
	}
	return true
}
