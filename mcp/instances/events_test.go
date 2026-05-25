package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type failingHetznerPlatform struct {
	tk.BasePlatformClient
}

func (failingHetznerPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (failingHetznerPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "hetzner"}, nil
}

func (failingHetznerPlatform) ExecuteIntegrationTool(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
	return &sdk.ExecuteResult{
		Success: false,
		Status:  502,
		Data:    json.RawMessage(`{"error":"upstream unavailable"}`),
	}, nil
}

func TestHetznerProvision_EmitsCreatedProvisioningAndError(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("project-1"),
		tk.WithEmitter(rec),
		tk.WithPlatform(failingHetznerPlatform{}),
	)

	_, err := hetznerProvision(ctx, CreateInstanceInput{Name: "test-1"})
	if err == nil {
		t.Fatal("hetznerProvision succeeded, want upstream error")
	}

	events := rec.Events()
	wantTopics := []string{instanceCreatedTopic, instanceProvisioningTopic, instanceErrorTopic}
	if len(events) != len(wantTopics) {
		t.Fatalf("got %d events, want %d: %#v", len(events), len(wantTopics), events)
	}
	for i, want := range wantTopics {
		if events[i].Topic != want {
			t.Fatalf("event[%d].Topic=%q, want %q", i, events[i].Topic, want)
		}
		if events[i].ProjectID != "project-1" {
			t.Fatalf("event[%d].ProjectID=%q, want project-1", i, events[i].ProjectID)
		}
	}
	payload, ok := events[2].Data.(map[string]any)
	if !ok {
		t.Fatalf("error event payload type %T, want map[string]any", events[2].Data)
	}
	if payload["status"] != "error" {
		t.Fatalf("error payload status=%v, want error", payload["status"])
	}
	if payload["ssh_private_key"] != nil || payload["ssh_public_key"] != nil {
		t.Fatalf("event payload leaked SSH key material: %#v", payload)
	}
}

func TestUpdateInstanceAndEmit_Ready(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"), tk.WithEmitter(rec))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "test-1", Provider: "hetzner", ProviderID: "42", Status: "provisioning",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := updateInstanceAndEmit(ctx, inst.ID, map[string]any{"status": "ready", "ready_at": "2026-05-25T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	events := rec.EventsByTopic(instanceReadyTopic)
	if len(events) != 1 {
		t.Fatalf("ready events=%d, want 1", len(events))
	}
	payload := events[0].Data.(map[string]any)
	if payload["id"] != inst.ID || payload["status"] != "ready" {
		t.Fatalf("ready payload = %#v", payload)
	}
}

func TestDeleteInstanceAndEmit_Destroyed(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"), tk.WithEmitter(rec))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "test-1", Provider: "hetzner", ProviderID: "42", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := deleteInstanceAndEmit(ctx, inst); err != nil {
		t.Fatal(err)
	}

	events := rec.EventsByTopic(instanceDestroyedTopic)
	if len(events) != 1 {
		t.Fatalf("destroyed events=%d, want 1", len(events))
	}
	payload := events[0].Data.(map[string]any)
	if payload["id"] != inst.ID || payload["status"] != "destroyed" || payload["provider_id"] != "42" {
		t.Fatalf("destroyed payload = %#v", payload)
	}
	if _, err := dbGetInstance(ctx.AppDB(), inst.ID); err != ErrInstanceNotFound {
		t.Fatalf("dbGetInstance after delete = %v, want ErrInstanceNotFound", err)
	}
}
