package main

import (
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	terminationTelephonyRestart = "telephony_restart"
	telephonyRestartReason      = "call ended when Telephony restarted"
)

// Direct SIP dialogs live only in this process's gateway, so a call the
// previous execution left unfinished cannot still be connected: its caller was
// dropped when that process stopped. Carrier-held calls (Twilio, Telnyx call
// control) are not touched; the carrier keeps them alive and media recovery
// reconnects them.
//
// Reconciliation records the restart as the failure, clears the deadlines that
// would otherwise send the expiry worker after a dialog that no longer exists,
// and publishes through the normal lifecycle path. It sends no carrier command.
func (a *App) reconcileAbandonedDirectSIPCalls(ctx *sdk.AppCtx) (int, error) {
	rows, err := a.db().db.Query(`SELECT ` + callSelectColumns + ` FROM calls
        WHERE status NOT IN ('completed','failed','no-answer','busy','canceled')`)
	if err != nil {
		return 0, err
	}
	var abandoned []*callRow
	for rows.Next() {
		row, err := scanCall(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		abandoned = append(abandoned, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	gateway := a.directSIPGateway()
	now := time.Now().UTC()
	reconciled := 0
	for _, row := range abandoned {
		if !a.callUsesDirectSIP(row) {
			continue
		}
		// A managed runtime starts its gateway before it owns the state, and a
		// failed initialization can be retried in the same process. A dialog the
		// gateway holds is live, not abandoned.
		if gateway != nil && gateway.hasCall(row.ID) {
			continue
		}
		if err := a.reconcileAbandonedCall(row, now); err != nil {
			return reconciled, err
		}
		reconciled++
		// The Core thread outlived the call. Ending it is best effort.
		if err := a.killCallThread(ctx, row); err != nil {
			ctx.Logger().Warn("kill thread of call abandoned by restart", "call", row.ID, "err", err)
		}
	}
	return reconciled, nil
}

func (a *App) reconcileAbandonedCall(row *callRow, now time.Time) error {
	unlock := a.softphones.lockClaim(row.ID)
	defer unlock()
	_, err := a.db().updateStatusWithFacts(row.ID, "failed", telephonyRestartReason, lifecycleFacts{
		Source:               "telephony",
		OccurredAt:           now.Format(time.RFC3339Nano),
		TerminationCause:     terminationTelephonyRestart,
		TerminationInitiator: "telephony",
	})
	if err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE calls SET state_expires_at='',deadline_at='',connected_deadline_at='',media_deadline_at=''
        WHERE id=? AND status IN ('completed','failed','no-answer','busy','canceled')`, row.ID)
	return err
}
