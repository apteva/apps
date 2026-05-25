package main

import (
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	instanceCreatedTopic      = "instance.created"
	instanceProvisioningTopic = "instance.provisioning"
	instanceReadyTopic        = "instance.ready"
	instanceErrorTopic        = "instance.error"
	instanceDestroyedTopic    = "instance.destroyed"
)

func appCtxForRequest(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return globalCtx
	}
	return globalCtx.WithProject(projectID)
}

func emitInstanceCreated(ctx *sdk.AppCtx, inst *Instance) {
	emitInstanceEvent(ctx, instanceCreatedTopic, inst)
}

func emitInstanceStatus(ctx *sdk.AppCtx, inst *Instance) {
	if inst == nil {
		return
	}
	switch inst.Status {
	case "provisioning":
		emitInstanceEvent(ctx, instanceProvisioningTopic, inst)
	case "ready":
		emitInstanceEvent(ctx, instanceReadyTopic, inst)
	case "error":
		emitInstanceEvent(ctx, instanceErrorTopic, inst)
	}
}

func emitInstanceDestroyed(ctx *sdk.AppCtx, inst *Instance) {
	if inst == nil {
		return
	}
	c := *inst
	c.Status = "destroyed"
	if c.DestroyedAt == "" {
		c.DestroyedAt = nowUTC()
	}
	emitInstanceEvent(ctx, instanceDestroyedTopic, &c)
}

func emitInstanceEvent(ctx *sdk.AppCtx, topic string, inst *Instance) {
	if ctx == nil || inst == nil {
		return
	}
	ctx.Emit(topic, instanceEventPayload(inst))
}

func instanceEventPayload(inst *Instance) map[string]any {
	out := map[string]any{
		"id":       inst.ID,
		"name":     inst.Name,
		"provider": inst.Provider,
		"status":   inst.Status,
	}
	if inst.ProviderID != "" {
		out["provider_id"] = inst.ProviderID
	}
	if inst.PublicIPv4 != "" {
		out["public_ipv4"] = inst.PublicIPv4
	}
	if inst.PublicIPv6 != "" {
		out["public_ipv6"] = inst.PublicIPv6
	}
	if inst.Region != "" {
		out["region"] = inst.Region
	}
	if inst.Size != "" {
		out["size"] = inst.Size
	}
	if inst.Image != "" {
		out["image"] = inst.Image
	}
	if inst.MonthlyCostCents > 0 {
		out["monthly_cost_cents"] = inst.MonthlyCostCents
	}
	if inst.ErrorMessage != "" {
		out["error"] = inst.ErrorMessage
	}
	if inst.CreatedAt != "" {
		out["created_at"] = inst.CreatedAt
	}
	if inst.ReadyAt != "" {
		out["ready_at"] = inst.ReadyAt
	}
	if inst.DestroyedAt != "" {
		out["destroyed_at"] = inst.DestroyedAt
	}
	return out
}

func updateInstanceAndEmit(ctx *sdk.AppCtx, id int64, fields map[string]any) (*Instance, error) {
	before, _ := dbGetInstance(ctx.AppDB(), id)
	if err := dbUpdateInstance(ctx.AppDB(), id, fields); err != nil {
		return nil, err
	}
	after, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if status, ok := fields["status"].(string); ok && status != "" && (before == nil || before.Status != status) {
		emitInstanceStatus(ctx, after)
	}
	return after, nil
}

func deleteInstanceAndEmit(ctx *sdk.AppCtx, inst *Instance) error {
	if err := dbDeleteInstance(ctx.AppDB(), inst.ID); err != nil {
		return err
	}
	emitInstanceDestroyed(ctx, inst)
	return nil
}
