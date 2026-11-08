package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
)

func (a *App) ingressPolicyTools() []sdk.Tool {
	return []sdk.Tool{
		tool("devices_ingest_credential_create", "Issue a write-only HTTP credential for one device.", schema(map[string]any{"device_id": stringProp(), "label": stringProp(), "expires_in_seconds": intProp(), "idempotency_key": stringProp()}, []string{"device_id", "idempotency_key"}), func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.issueHTTPCredential(args) }),
		tool("devices_ingest_credentials_list", "List HTTP credential metadata without secrets.", deviceOnlySchema(), func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			return a.listHTTPCredentials(str(args, "device_id"))
		}),
		tool("devices_ingest_credential_revoke", "Revoke one HTTP write credential.", schema(map[string]any{"device_id": stringProp(), "credential_id": stringProp()}, []string{"device_id", "credential_id"}), func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			return a.revokeHTTPCredential(str(args, "device_id"), str(args, "credential_id"))
		}),
		tool("devices_usage_summary", "Get exact usage totals in [from,to) and current scoped resource counts.", schema(map[string]any{"from": stringProp(), "to": stringProp(), "measurement": stringProp()}, nil), func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.usageSummary(args) }),
		tool("devices_policy_get", "Get effective policy revision, limits and over-limit state.", schema(map[string]any{}, nil), func(_ *sdk.AppCtx, _ map[string]any) (any, error) { return a.policySnapshot() }),
		{Name: "devices_policy_set", Description: "Apply a revision-checked live operational policy; only the configured bound controller app may write.", InputSchema: schema(map[string]any{"expected_revision": intProp(), "request_id": stringProp(), "limits": objectProp()}, []string{"expected_revision", "request_id", "limits"}), HandlerCtx: func(call context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
			if err := a.policyWriter(call); err != nil {
				return nil, err
			}
			return a.applyPolicy(args)
		}},
	}
}
