package main

import (
	"encoding/json"

	sdk "github.com/apteva/app-sdk"
)

func boundIntegrationsFor(ctx *sdk.AppCtx, role string) []*sdk.BoundIntegration {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil || id.Bindings == nil {
		return nil
	}
	ids, defaultID := bindingIDsForRole(id.Bindings[role])
	if len(ids) == 0 {
		if b := ctx.IntegrationFor(role); b != nil {
			return []*sdk.BoundIntegration{b}
		}
		return nil
	}
	out := make([]*sdk.BoundIntegration, 0, len(ids))
	for _, connID := range ids {
		if connID <= 0 {
			continue
		}
		bound := &sdk.BoundIntegration{
			Role:         role,
			Kind:         "integration",
			ConnectionID: connID,
			ToolFor:      toolsForRole(role),
		}
		if conn, err := ctx.PlatformAPI().GetConnection(connID); err == nil && conn != nil {
			bound.AppSlug = conn.AppSlug
		}
		if defaultID != 0 && connID == defaultID {
			out = append([]*sdk.BoundIntegration{bound}, out...)
		} else {
			out = append(out, bound)
		}
	}
	return out
}

func toolsForRole(role string) func(string) string {
	manifest := (&App{}).Manifest()
	for _, dep := range manifest.Requires.Integrations {
		if dep.Role != role {
			continue
		}
		tools := dep.Tools
		return func(capability string) string {
			if tools == nil {
				return capability
			}
			if tool, ok := tools[capability]; ok {
				return tool
			}
			return capability
		}
	}
	return func(capability string) string { return capability }
}

func bindingIDsForRole(raw any) ([]int64, int64) {
	if id, ok := bindingID(raw); ok && id > 0 {
		return []int64{id}, id
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, 0
	}
	ids := make([]int64, 0)
	seen := map[int64]bool{}
	if arr, ok := m["ids"].([]any); ok {
		for _, item := range arr {
			if id, ok := bindingID(item); ok && id > 0 && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	defaultID := int64(0)
	if id, ok := bindingID(m["default_id"]); ok && seen[id] {
		defaultID = id
	}
	return ids, defaultID
}

func bindingID(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n > 0
	case json.Number:
		i, err := n.Int64()
		return i, err == nil && i > 0
	}
	return 0, false
}
