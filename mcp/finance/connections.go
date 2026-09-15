package main

import (
	"errors"
	"fmt"
	"sort"

	sdk "github.com/apteva/app-sdk"
)

// Keep the existing account-data role key so its single-ID bindings continue
// to work when upgraded to multiple selections. Every financial operation now
// uses this one role; provider credentials remain in the platform.
const financeConnectionRole = "open_banking"

type financeBoundConnection struct {
	sdk.PlatformConnection
	Default bool
}

func financeConnections(ctx *sdk.AppCtx) ([]financeBoundConnection, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("platform connections are not available")
	}
	out := []financeBoundConnection{}
	seen := map[int64]bool{}
	for _, bound := range ctx.IntegrationsFor(financeConnectionRole) {
		if bound == nil || bound.ConnectionID <= 0 || seen[bound.ConnectionID] {
			continue
		}
		conn, err := ctx.PlatformAPI().GetConnection(bound.ConnectionID)
		if err != nil {
			return nil, err
		}
		if conn == nil || conn.ID != bound.ConnectionID {
			continue
		}
		if conn.ProjectID != "" && conn.ProjectID != projectID(ctx) {
			continue
		}
		if !isBankingProvider(conn.AppSlug) && conn.AppSlug != "trading212" {
			continue
		}
		seen[conn.ID] = true
		out = append(out, financeBoundConnection{PlatformConnection: *conn, Default: bound.IsDefault})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Default && !out[j].Default })
	return out, nil
}
func financeConnection(ctx *sdk.AppCtx, provider string, id int64) (sdk.PlatformConnection, error) {
	conns, err := financeConnections(ctx)
	if err != nil {
		return sdk.PlatformConnection{}, err
	}
	for _, conn := range conns {
		if id != 0 && conn.ID != id {
			continue
		}
		if provider != "" && conn.AppSlug != provider {
			continue
		}
		if conn.Status != "" && conn.Status != "active" && conn.Status != "connected" {
			if id != 0 {
				return sdk.PlatformConnection{}, errors.New("connection is not active")
			}
			continue
		}
		return conn.PlatformConnection, nil
	}
	return sdk.PlatformConnection{}, fmt.Errorf("no active %s connection selected in Finance's Financial connections (connection_id=%d)", provider, id)
}
