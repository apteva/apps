package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Status polling only reads provider records. It never submits payments or
// creates ledger entries, and also checks settled transfers for later returns.
func (a *App) paymentStatusWorker(c context.Context, ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT project_id,id FROM bank_payments WHERE provider_id!='' AND state NOT IN ('draft','cancelled','cancelling','continuing') AND created_at>=datetime('now','-90 days') ORDER BY last_checked_at,id LIMIT 100`)
	if err != nil {
		return err
	}
	type item struct{ project, id string }
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.project, &i.id); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, i := range items {
		if c.Err() != nil {
			return c.Err()
		}
		pc := ctx.WithProject(i.project)
		if _, err := a.toolBankingPaymentGet(pc, map[string]any{"id": i.id, "refresh": true}); err != nil {
			failures = append(failures, fmt.Errorf("payment %s: %w", i.id, err))
		}
		// Rotate failed checks too so a disabled connection cannot starve others.
		if _, err := pc.AppDB().Exec(`UPDATE bank_payments SET last_checked_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, i.id, i.project); err != nil {
			failures = append(failures, err)
		}
	}
	rows, err = ctx.AppDB().Query(`SELECT DISTINCT project_id,json_extract(request_json,'$.connection_id') FROM bank_payments WHERE json_extract(request_json,'$.provider')='plaid' AND json_extract(request_json,'$.mode')='ach'`)
	if err != nil {
		return err
	}
	type source struct {
		project string
		id      int64
	}
	sources := []source{}
	for rows.Next() {
		var s source
		if err = rows.Scan(&s.project, &s.id); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range sources {
		if c.Err() != nil {
			return c.Err()
		}
		if _, err := a.syncPlaidPaymentEvents(ctx.WithProject(s.project), s.id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (a *App) syncPlaidPaymentEvents(ctx *sdk.AppCtx, cid int64) (any, error) {
	_, provider, err := paymentConnection(ctx, cid)
	if err != nil {
		return nil, err
	}
	if provider != "plaid" {
		return nil, errors.New("Transfer events require a Plaid connection")
	}
	if _, err = ctx.AppDB().Exec(`INSERT INTO bank_payment_event_cursors(project_id,connection_id) VALUES(?,?) ON CONFLICT DO NOTHING`, projectID(ctx), cid); err != nil {
		return nil, err
	}
	var cursor int64
	if err = ctx.AppDB().QueryRow(`SELECT after_id FROM bank_payment_event_cursors WHERE project_id=? AND connection_id=?`, projectID(ctx), cid).Scan(&cursor); err != nil {
		return nil, err
	}
	processed := 0
	for page := 0; page < 10; page++ {
		var raw map[string]any
		if err = executeIntegrationJSON(ctx, cid, "sync_transfer_events", map[string]any{"after_id": cursor, "count": 100}, &raw); err != nil {
			return nil, err
		}
		events := flattenItems(raw["transfer_events"])
		next := cursor
		for _, event := range events {
			eid, e := exactPositiveInteger(event["event_id"])
			if e != nil || eid <= cursor {
				return nil, errors.New("invalid or non-advancing Plaid event ID")
			}
			if eid > next {
				next = eid
			}
			id := firstString(event, "transfer_id")
			if id == "" {
				continue // Ledger sweep events have no bank transfer to reconcile.
			}
			rows, e := ctx.AppDB().Query(`SELECT id FROM bank_payments WHERE project_id=? AND json_extract(request_json,'$.connection_id')=? AND provider_id=? AND json_extract(request_json,'$.mode')='ach'`, projectID(ctx), cid, id)
			if e != nil {
				return nil, e
			}
			ids := []string{}
			for rows.Next() {
				var pid string
				if e = rows.Scan(&pid); e != nil {
					rows.Close()
					return nil, e
				}
				ids = append(ids, pid)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, e
			}
			for _, pid := range ids {
				if _, e = a.toolBankingPaymentGet(ctx, map[string]any{"id": pid, "refresh": true}); e != nil {
					return nil, e
				}
			}
			processed++
		}
		// A crash before this checkpoint repeats harmless authoritative status reads.
		// MAX prevents concurrent workers moving a cursor backward.
		if _, err = ctx.AppDB().Exec(`UPDATE bank_payment_event_cursors SET after_id=MAX(after_id,?) WHERE project_id=? AND connection_id=?`, next, projectID(ctx), cid); err != nil {
			return nil, err
		}
		cursor = next
		if len(events) < 100 {
			return map[string]any{"after_id": cursor, "events": processed, "more": false}, nil
		}
	}
	return map[string]any{"after_id": cursor, "events": processed, "more": true}, nil
}
func (a *App) toolBankingPaymentEvents(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := exactPositiveInteger(args["connection_id"])
	if err != nil {
		return nil, err
	}
	return a.syncPlaidPaymentEvents(ctx, id)
}
