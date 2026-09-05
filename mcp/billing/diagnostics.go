package main

import (
	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolReconciliationStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppReadDB().Query(`SELECT id,COALESCE(invoice_id,0),kind,state,error,updated_at FROM billing_provider_operations WHERE project_id=? AND state<>'completed' ORDER BY updated_at LIMIT 100`, pid)
	if err != nil {
		return nil, err
	}
	var operations []map[string]any
	for rows.Next() {
		var id, kind, state, message string
		var iid, updated int64
		if err = rows.Scan(&id, &iid, &kind, &state, &message, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		operations = append(operations, map[string]any{"operation_id": id, "invoice_id": iid, "kind": kind, "state": state, "error": message, "updated_at": updated})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var pendingEvents int
	if err = ctx.AppReadDB().QueryRow(`SELECT count(*) FROM billing_outbox WHERE project_id=? AND delivered_at IS NULL`, pid).Scan(&pendingEvents); err != nil {
		return nil, err
	}
	return map[string]any{"operations": operations, "pending_events": pendingEvents}, nil
}
