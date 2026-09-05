package main

import (
	"database/sql"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func recipientStatuses(db *sql.DB, ids []int64) map[int64]map[string]string {
	out := map[int64]map[string]string{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT message_id,recipient,status FROM recipient_delivery_status WHERE message_id IN (`+strings.TrimRight(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var recipient, status string
		if rows.Scan(&id, &recipient, &status) == nil {
			if out[id] == nil {
				out[id] = map[string]string{}
			}
			out[id][recipient] = status
		}
	}
	return out
}

func (a *App) refreshSendersInBackground(ctx *sdk.AppCtx, pid string) {
	a.syncMu.Lock()
	if a.syncNext == nil {
		a.syncNext = map[string]time.Time{}
	}
	if a.syncRunning == nil {
		a.syncRunning = map[string]bool{}
	}
	if a.syncRunning[pid] || time.Now().Before(a.syncNext[pid]) {
		a.syncMu.Unlock()
		return
	}
	a.syncRunning[pid] = true
	a.syncMu.Unlock()
	go func() {
		err := a.refreshSendersFromProviders(ctx, pid)
		a.syncMu.Lock()
		a.syncRunning[pid] = false
		a.syncNext[pid] = time.Now().Add(time.Minute)
		a.syncMu.Unlock()
		if err != nil {
			ctx.Logger().Warn("senders background refresh", "err", err)
			_, _ = ctx.AppDB().Exec(`UPDATE senders SET last_sync_error=? WHERE project_id=?`, truncate(err.Error(), 500), pid)
		}
	}()
}
