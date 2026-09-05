package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestScalewayBucketNames(t *testing.T) {
	body := `<?xml version="1.0"?><ListAllMyBucketsResult><Buckets><Bucket><Name>alpha</Name></Bucket><Bucket><Name>beta</Name></Bucket></Buckets></ListAllMyBucketsResult>`
	encoded, _ := json.Marshal(body)
	if got, want := scalewayBucketNames(encoded), []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scalewayBucketNames()=%v, want %v", got, want)
	}
}

type hetznerReconcilePlatform struct {
	tk.BasePlatformClient
	connectionID int64
	args         map[string]any
	response     json.RawMessage
	calls        int
}

func (p *hetznerReconcilePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *hetznerReconcilePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "hetzner"}, nil
}

func (p *hetznerReconcilePlatform) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls++
	p.connectionID = connectionID
	p.args = input
	if tool != "server_get" {
		return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"unexpected tool"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: p.response}, nil
}

func TestCompareInstanceProviderRepairsScientificHetznerID(t *testing.T) {
	platform := &hetznerReconcilePlatform{response: json.RawMessage(`{
		"server": {
			"id": 132664013,
			"name": "processing",
			"status": "running",
			"public_net": {"ipv4": {"ip": "91.99.2.117"}, "ipv6": {"ip": "2a01:4f8::1"}}
		}
	}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "processing", Provider: "hetzner", ProviderConnectionID: 7,
		ProviderID: "1.32664013e+08", PublicIPv4: "91.99.2.117", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{
		"status": "error", "lifecycle_stage": "Reconcile",
		"primary_error": "provider reconciliation: resource no longer exists upstream",
		"error_message": "provider reconciliation: resource no longer exists upstream",
	}); err != nil {
		t.Fatal(err)
	}
	inst, _ = dbGetInstance(ctx.AppDB(), inst.ID)

	comparison, err := compareInstanceProvider(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	if platform.connectionID != 7 || platform.args["id"] != int64(132664013) {
		t.Fatalf("server_get connection/id = %d/%#v, want 7/132664013", platform.connectionID, platform.args["id"])
	}
	if !comparison.ProviderExists || comparison.ProviderIPv4 != "91.99.2.117" {
		t.Fatalf("comparison = %#v", comparison)
	}
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ProviderID != "132664013" || fresh.Status != "ready" || fresh.LifecycleStage != "Ready" || fresh.ErrorMessage != "" || fresh.PrimaryError != "" {
		t.Fatalf("repaired instance = %#v", fresh)
	}
}

func TestCompareInstanceProviderRejectsInvalidStoredHetznerID(t *testing.T) {
	platform := &hetznerReconcilePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "broken", Provider: "hetzner", ProviderConnectionID: 7,
		ProviderID: "not-an-id", PublicIPv4: "192.0.2.10", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compareInstanceProvider(ctx, inst)
	if err == nil || !strings.Contains(err.Error(), "invalid stored provider ID") {
		t.Fatalf("error = %v, want invalid stored provider ID", err)
	}
	if platform.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", platform.calls)
	}
}

func TestReconcileTrackedProviderStateLabelsInvalidHetznerID(t *testing.T) {
	platform := &hetznerReconcilePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "broken", Provider: "hetzner", ProviderConnectionID: 7,
		ProviderID: "not-an-id", PublicIPv4: "192.0.2.10", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	reconcileTrackedProviderState(ctx)
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "error" || fresh.LifecycleStage != "Reconcile" || !strings.Contains(fresh.ErrorMessage, "invalid stored provider ID") {
		t.Fatalf("reconciled instance = %#v", fresh)
	}
	if strings.Contains(fresh.ErrorMessage, "resource no longer exists") {
		t.Fatalf("invalid ID was misclassified as missing: %q", fresh.ErrorMessage)
	}
	if platform.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", platform.calls)
	}
}

func TestCompareInstanceProviderRefusesUnverifiedHetznerRepair(t *testing.T) {
	platform := &hetznerReconcilePlatform{response: json.RawMessage(`{
		"server": {"id":132664013,"name":"different","status":"running","public_net":{"ipv4":{"ip":"91.99.2.117"}}}
	}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "processing", Provider: "hetzner", ProviderConnectionID: 7,
		ProviderID: "1.32664013e+08", PublicIPv4: "91.99.2.117", Status: "error",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compareInstanceProvider(ctx, inst)
	if err == nil || !strings.Contains(err.Error(), "refusing Hetzner provider ID repair") {
		t.Fatalf("error = %v, want guarded repair refusal", err)
	}
	fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if fresh.ProviderID != "1.32664013e+08" || fresh.Status != "error" {
		t.Fatalf("unverified row was modified: %#v", fresh)
	}
}
