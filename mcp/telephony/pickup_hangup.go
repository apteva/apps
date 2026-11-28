package main

import (
	"database/sql"
	"net/http"
	"time"
)

// A browser claim must open its media socket within this window, or the call
// goes back to routing. A pick-up that starts automatically has its audio
// prepared during the ring; a manual one may wait on a microphone prompt.
// Both stay under a ring-group claim's former 30 s expiry, which ended the
// call instead of re-offering it.
var (
	automaticAnswerAttachWindow = 10 * time.Second
	manualAnswerAttachWindow    = 25 * time.Second
)

// hangupRequest is the optional body of POST /calls/:id/hangup. A browser
// that is picking up the call sends the session token it was issued.
type hangupRequest struct {
	SessionToken string `json:"session_token,omitempty"`
}

// phoneHangupBeforeAnswer handles a phone user's hang-up of an inbound human
// call that is ringing or being picked up. Such a request only ever concerns
// that user: it declines their ringing offers, and while the call connects
// only the session that claimed it may end it. handled=false hands an allowed
// request back to the reject path.
//
// ownSession is true when the request carried a valid media session of this
// call for this user: the claiming browser.
func (a *App) phoneHangupBeforeAnswer(r *http.Request, row *callRow, p *phonePrincipal, sentToken, ownSession bool, actor hangupActor) (response map[string]any, handled bool, err error) {
	requestedAt := time.Now()
	if row.Status == "answering" {
		if ownSession {
			return nil, false, nil
		}
		reason := "answering_elsewhere"
		if !sentToken {
			reason = "no_session_token"
		}
		a.recordHangupNotEnded(r, row, p, actor, reason, requestedAt)
		return ignoredHangup(row, reason), true, nil
	}
	declined, err := a.declinePhoneOffers(row, p)
	if err != nil {
		return nil, true, err
	}
	if declined > 0 {
		a.recordHangupNotEnded(r, row, p, actor, "offer_declined", requestedAt)
		return map[string]any{"ok": true, "call_id": row.ID, "status": row.Status, "offer_declined": true}, true, nil
	}
	a.recordHangupNotEnded(r, row, p, actor, "no_active_offer", requestedAt)
	return ignoredHangup(row, "no_active_offer"), true, nil
}

func ignoredHangup(row *callRow, reason string) map[string]any {
	return map[string]any{"ok": true, "call_id": row.ID, "status": row.Status, "ignored": true, "reason": reason}
}

// recordHangupNotEnded logs and audits a hang-up that left the call running,
// so a client that keeps sending them can be found.
func (a *App) recordHangupNotEnded(r *http.Request, row *callRow, p *phonePrincipal, actor hangupActor, reason string, requestedAt time.Time) {
	logSoftphone("telephony hangup did not end call",
		"request_id", r.Header.Get("X-Request-ID"), "call_id", row.ID, "status", row.Status,
		"principal", phoneHash(p.Identity.key())[:16], "session_generation", actor.Session, "reason", reason)
	actor.Action = "hangup_requested"
	a.auditHangup(row, actor, row.Status, reason, "", requestedAt)
}

// declinePhoneOffers declines the user's ringing browser offers for the call,
// so the ring group moves on, exactly as the offer decline endpoint does.
func (a *App) declinePhoneOffers(row *callRow, p *phonePrincipal) (int, error) {
	offers, err := a.db().activeRingOffers(row.ID, row.ProjectID)
	if err != nil {
		return 0, err
	}
	var mine []ringOffer
	for _, o := range offers {
		if o.Kind == "browser" && p.Destinations[o.DestinationID] && a.destinationAllowsIdentity(row.ProjectID, o.DestinationID, p.Identity) {
			mine = append(mine, o)
		}
	}
	if len(mine) == 0 {
		return 0, nil
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	declined, runs := 0, map[string]bool{}
	for _, o := range mine {
		ok, err := declineRingOfferTx(tx, row.ID, o.ID, time.Now())
		if err != nil {
			return 0, err
		}
		if ok {
			declined++
			runs[o.RunID] = true
		}
	}
	for run := range runs {
		if err = advanceRingRunTx(tx, run, time.Now()); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if declined > 0 {
		a.routingCommitted(row.ProjectID)
	}
	return declined, nil
}

// declineRingOfferTx declines one still-ringing offer of a pending call.
func declineRingOfferTx(tx *sql.Tx, callID, offerID string, at time.Time) (bool, error) {
	now := ringTime(at)
	res, err := tx.Exec(`UPDATE call_offers SET status='declined',declined_at=? WHERE id=? AND call_id=? AND status='offered' AND expires_at>? AND EXISTS(SELECT 1 FROM calls WHERE id=? AND status='pending')`, now, offerID, callID, now, callID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// releaseUnattachedClaim gives an answering call back to routing when the
// claiming browser never opened its media socket: closed tab, crash or lost
// network. resetAnswerClaim touches only an unanswered call without media, so
// a call that connected meanwhile is never rolled back. handled=false leaves
// the expiry to the normal no-answer path.
func (a *App) releaseUnattachedClaim(row *callRow) (handled bool, err error) {
	if row.Status != "answering" || row.Direction != "inbound" || row.PeerKind != peerKindHuman || row.PeerToken == "" || row.MediaConnectedAt != "" {
		return false, nil
	}
	if hub := a.softphones.lookup(row.ID); hub != nil && hub.browserWriter() != nil {
		// The browser is attached and the carrier answer is under way; its own
		// activation deadline applies. Look again shortly.
		return true, a.db().setStateExpiry(row.ID, time.Now().Add(automaticAnswerAttachWindow))
	}
	if pending, err := a.carrierCommandPending(row.ID); err != nil || pending {
		if err == nil {
			err = a.db().setStateExpiry(row.ID, time.Now().Add(automaticAnswerAttachWindow))
		}
		return true, err
	}
	if err = a.db().resetAnswerClaim(row.ID, row.PeerToken); err != nil {
		// Media arrived meanwhile, or the store failed: never end the call here.
		return true, err
	}
	logSoftphone("softphone answer claim released", "call_id", row.ID, "reason", "browser_not_attached")
	return true, nil
}
