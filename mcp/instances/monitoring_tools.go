package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
)

func monitoringTools() []sdk.Tool {
	id := map[string]any{"type": "integer"}
	return []sdk.Tool{
		{Name: "instance_metrics_history", Description: "Query bounded monitoring history with CPU averages, original 250ms peaks and timestamps, busiest core, memory, network/disk I/O, thresholds and coverage. Default last hour, at most 600 buckets; max_points ≤3600. from/to are RFC3339. Auto resolution preserves peaks; missing intervals remain gaps.", InputSchema: schemaObject(map[string]any{"id": id, "from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}, "resolution": map[string]any{"type": "string", "enum": []string{"auto", "1s", "1m", "5m", "1h", "1d"}}, "max_points": map[string]any{"type": "integer", "minimum": 1, "maximum": 3600}}, []string{"id"}), Handler: monitoringHistory},
		{Name: "instance_metrics_incidents", Description: "List recent CPU/core/pressure incidents, or retrieve one bounded 250ms recording and best-effort process attribution by incident_id. Detail expires after 30 days or budget eviction; metadata is capped at 200 per host.", InputSchema: schemaObject(map[string]any{"id": id, "incident_id": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, []string{"id"}), Handler: monitoringIncidents},
		{Name: "instance_monitoring", Description: "Read collector installation/version/health, freshness and storage budget. Set enabled to enable or disable background monitoring; disabling stops the remote service asynchronously and reports stop failures. Linux/macOS AMD64/ARM64 collectors install and upgrade automatically on ready SSH hosts.", InputSchema: schemaObject(map[string]any{"id": id, "enabled": map[string]any{"type": "boolean"}}, []string{"id"}), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if value, present := args["enabled"]; present {
				enabled, ok := value.(bool)
				if !ok {
					return nil, errors.New("enabled must be a boolean")
				}
				return configureMonitoring(ctx, int64Arg(args, "id"), enabled)
			}
			return liveMonitoring(ctx, int64Arg(args, "id"))
		}},
	}
}
