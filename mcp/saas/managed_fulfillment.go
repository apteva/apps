package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// toolConnectionEnsure is the one-time secret-ingestion boundary for SaaS.
// Raw fields go straight to Server's encrypted managed-connection store. When
// an account is supplied, only the numeric reference is persisted in metadata.
func (a *App) toolConnectionEnsure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	var acct *Account
	if accountID := strings.TrimSpace(strArg(args, "account_id")); accountID != "" {
		acct, err = dbAccountGet(ctx.AppDB(), pid, accountID)
		if err != nil || acct == nil {
			return nil, firstErr(err, errors.New("account not found"))
		}
	}
	fields, err := managedCredentialFields(args["fields"])
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(strArg(args, "key"))
	appSlug := strings.TrimSpace(strArg(args, "app_slug"))
	if key == "" || appSlug == "" {
		return nil, errors.New("key and app_slug are required")
	}
	ownerKey := "project:" + pid
	defaultName := appSlug + " for project " + pid
	metadataKey := ""
	if acct != nil {
		ownerKey = "account:" + acct.ID
		defaultName = appSlug + " for " + acct.Slug
		metadataKey = normalizeStoreTarget(firstNonEmpty(strArg(args, "metadata_key"), "connections."+key))
		if metadataKey == "" {
			return nil, errors.New("metadata_key is invalid")
		}
	}
	connection, err := sdk.EnsureManagedConnection(ctx.PlatformAPI(), sdk.ManagedConnectionRequest{
		Key:       "saas:" + ownerKey + ":" + key,
		AppSlug:   appSlug,
		Name:      firstNonEmpty(strArg(args, "name"), defaultName),
		AuthType:  strArg(args, "auth_type"),
		ProjectID: pid,
		Fields:    fields,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure managed connection: %w", err)
	}
	if connection == nil || connection.ID <= 0 {
		return nil, errors.New("ensure managed connection returned no connection")
	}
	if acct != nil {
		metadata := mapFromAny(acct.Metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		setPathValue(metadata, strings.Split(metadataKey, "."), connection.ID)
		if err := dbAccountSetMetadata(ctx.AppDB(), pid, acct.ID, metadata); err != nil {
			return nil, err
		}
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "connection.ensured", "saas", map[string]any{
			"connection_id": connection.ID,
			"app_slug":      connection.AppSlug,
			"metadata_key":  metadataKey,
		})
	}
	return map[string]any{"connection": connection, "metadata_key": metadataKey}, nil
}

func managedCredentialFields(raw any) (map[string]string, error) {
	values := mapFromAny(raw)
	if len(values) == 0 {
		return nil, errors.New("fields are required")
	}
	fields := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		text, ok := value.(string)
		if key == "" || !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("credential field %q must be a non-empty string", key)
		}
		fields[key] = text
	}
	return fields, nil
}

func (a *App) executeFulfillmentAction(ctx *sdk.AppCtx, pid string, acct *Account, action PlanAction, run *FulfillmentRun, args map[string]any) (map[string]any, error) {
	switch firstNonEmpty(action.ExecutionKind, "app") {
	case "app":
		var out map[string]any
		err := ctx.PlatformAPI().CallAppResult(action.AppName, action.ToolName, args, &out)
		return out, err
	case "integration_execute":
		return a.executeIntegrationFulfillment(ctx, pid, acct, action, run, args)
	default:
		return nil, fmt.Errorf("unsupported execution_kind %q", action.ExecutionKind)
	}
}

func (a *App) executeIntegrationFulfillment(ctx *sdk.AppCtx, pid string, acct *Account, action PlanAction, run *FulfillmentRun, args map[string]any) (map[string]any, error) {
	connectionID := int64Arg(args, "connection_id")
	if connectionID <= 0 {
		return nil, errors.New("integration_execute requires connection_id")
	}
	input := mapFromAny(args["input"])
	if input == nil {
		input = copyMap(args)
		for _, key := range []string{"connection_id", "managed", "idempotency_key", "_project_id"} {
			delete(input, key)
		}
	}
	if managed := mapFromAny(args["managed"]); len(managed) != 0 {
		var err error
		input, err = prepareManagedProvisioning(ctx, pid, acct, run, args, managed)
		if err != nil {
			return nil, err
		}
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, action.ToolName, input)
	if err != nil {
		return nil, fmt.Errorf("execute integration %s: %w", action.ToolName, err)
	}
	if result == nil || !result.Success || result.Status < 200 || result.Status >= 300 {
		status := 0
		if result != nil {
			status = result.Status
		}
		return nil, fmt.Errorf("integration %s returned status %d", action.ToolName, status)
	}
	out := map[string]any{}
	if len(result.Data) != 0 && string(result.Data) != "null" {
		if err := json.Unmarshal(result.Data, &out); err != nil {
			out["data"] = json.RawMessage(result.Data)
		}
	}
	out["integration_status"] = result.Status
	return out, nil
}

func prepareManagedProvisioning(ctx *sdk.AppCtx, pid string, acct *Account, run *FulfillmentRun, actionArgs, managed map[string]any) (map[string]any, error) {
	manager, err := sdk.ManagedTenants(ctx.PlatformAPI())
	if err != nil {
		return nil, err
	}
	tenantID := firstNonEmpty(strArg(managed, "tenant_id"), acct.ID)
	if _, err := manager.EnsureManagedTenant(sdk.ManagedTenantRequest{TenantID: tenantID, AccountID: acct.ID}); err != nil {
		return nil, fmt.Errorf("ensure managed tenant: %w", err)
	}
	deliveries := make([]sdk.ManagedConnectionGrantDelivery, 0)
	for index, raw := range sliceFromAny(managed["grants"]) {
		grantMap := mapFromAny(raw)
		if grantMap == nil {
			return nil, fmt.Errorf("managed.grants[%d] must be an object", index)
		}
		if grantMap["connection_id"] == nil {
			grantMap["connection_id"] = grantMap["provider_connection_id"]
		}
		grantMap["tenant_id"] = tenantID
		var request sdk.ManagedConnectionGrantRequest
		if err := decodeJSONValue(grantMap, &request); err != nil {
			return nil, fmt.Errorf("decode managed.grants[%d]: %w", index, err)
		}
		if _, err := manager.EnsureManagedConnectionGrant(request); err != nil {
			return nil, fmt.Errorf("ensure grant %s: %w", request.GrantID, err)
		}
		delivery, err := manager.GetManagedConnectionGrantDelivery(tenantID, request.GrantID)
		if err != nil {
			return nil, fmt.Errorf("deliver grant %s: %w", request.GrantID, err)
		}
		deliveries = append(deliveries, *delivery)
	}
	revoked := make([]string, 0)
	for _, value := range sliceFromAny(managed["revoked_grant_ids"]) {
		if grantID := strings.TrimSpace(strFromAny(value)); grantID != "" {
			revoked = append(revoked, grantID)
		}
	}
	var bundle *sdk.ManagedTenantBundle
	if rawBundle := mapFromAny(managed["bundle"]); rawBundle != nil {
		bundle = &sdk.ManagedTenantBundle{}
		if err := decodeJSONValue(rawBundle, bundle); err != nil {
			return nil, fmt.Errorf("decode managed.bundle: %w", err)
		}
		bundle.TenantID = tenantID
		bundle.Revision = run.ID
		bundle.Status = ""
		bundle.LastError = ""
		if bundle.BundleID == "" {
			return nil, errors.New("managed.bundle.bundle_id is required")
		}
	}
	if len(deliveries) == 0 && len(revoked) == 0 && bundle == nil {
		return nil, errors.New("managed provisioning requires grants, revoked_grant_ids, or bundle")
	}
	request := sdk.ManagedProvisioningApplyRequest{
		RequestID:       managedProvisioningRequestID(strArg(actionArgs, "idempotency_key"), acct.ID, run.ID),
		TenantID:        tenantID,
		Grants:          deliveries,
		RevokedGrantIDs: revoked,
		Bundle:          bundle,
	}
	var out map[string]any
	if err := decodeJSONValue(request, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func managedProvisioningRequestID(idempotencyKey, accountID string, runID int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", idempotencyKey, accountID, runID)))
	return "saas_" + hex.EncodeToString(sum[:16])
}

func decodeJSONValue(value, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
