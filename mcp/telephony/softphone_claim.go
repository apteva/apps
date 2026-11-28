package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// softphoneClaim is a committed browser answer claim.
type softphoneClaim struct {
	row       *callRow
	peerToken string
	session   *softphoneSession // application users only
}

var (
	errClaimOwnership          = errors.New("ownership unavailable")
	errClaimSession            = errors.New("media session unavailable")
	errClaimDestinationChanged = errors.New("claimed destination changed")
)

// claimSoftphoneAnswer claims a pending inbound call for a browser in one
// transaction: the claim, the other offers, the attachment, the owner and the
// media grant. Telephony's database has a single connection, so this is one
// turn on it instead of five. Everything read before the writes is read first,
// because the transaction holds that connection. claimed is false when the
// call is no longer available to claim.
func (a *App) claimSoftphoneAnswer(row *callRow, p *phonePrincipal, dest string, correlation audioSessionCorrelation, attachWindow time.Duration) (claim *softphoneClaim, claimed bool, err error) {
	var capacity destinationCapacity
	principal, revision := "", int64(0)
	if p != nil {
		fresh, e := a.phonePrincipal(row.ProjectID, p.Identity)
		if e != nil {
			return nil, false, e
		}
		if capacity, e = a.capacityForIdentity(row.ProjectID, p.Identity); e != nil {
			return nil, false, fmt.Errorf("%w: %v", errClaimOwnership, e)
		}
		if dest != "" && !p.Supervisor && !a.destinationAllowsIdentity(row.ProjectID, dest, p.Identity) {
			return nil, false, fmt.Errorf("%w: destination is assigned to another identity", errClaimOwnership)
		}
		// The new owner attaches through this destination, so it must still be
		// allowed to: the same check a separately issued media grant makes.
		if !fresh.forAction("call.attach").Destinations[dest] {
			return nil, false, fmt.Errorf("call not owned by user: %w", errPhoneAccessDenied)
		}
		principal, revision = p.Identity.key(), fresh.Revision
	}
	peerToken := newSecret()
	bridgeURL := a.peerLoopbackURL(row)
	db := a.db()
	tx, err := db.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if claimed, err = claimPendingCallForHumanTx(tx, row.ID, row.ProjectID, dest); err != nil || !claimed {
		return nil, false, err
	}
	if err = settleHumanOffersOn(tx, row.ID, row.ProjectID); err != nil {
		return nil, false, err
	}
	current, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, row.ID))
	if err != nil {
		return nil, false, err
	}
	if p != nil && current.RoutingDestinationID != dest {
		return nil, false, errClaimDestinationChanged
	}
	if err = attachHumanCallOn(tx, row.ID, bridgeURL, peerToken); err != nil {
		return nil, false, err
	}
	// The claiming browser must attach within the window; the lifecycle worker
	// otherwise returns the call to routing (releaseUnattachedClaim).
	expires := time.Now().Add(attachWindow).UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`UPDATE calls SET state_expires_at=? WHERE id=? AND status='answering'`, expires, row.ID); err != nil {
		return nil, false, err
	}
	current.PeerToken, current.AudioBridgeURL, current.StateExpiresAt = peerToken, bridgeURL, expires
	var token string
	var issued audioSessionCorrelation
	if p != nil {
		if err = setPhoneOwnerTx(tx, current, p, current.RoutingDestinationID, capacity); err != nil {
			return nil, false, fmt.Errorf("%w: %v", errClaimOwnership, err)
		}
		var query string
		var args []any
		token, query, args, issued = phoneMediaSessionInsert(current, principal, revision, correlation)
		var saved string
		if err = tx.QueryRow(query+` RETURNING diagnostic_json`, args...).Scan(&saved); err != nil {
			return nil, false, fmt.Errorf("%w: %v", errClaimSession, err)
		}
		_ = json.Unmarshal([]byte(saved), &issued)
	}
	if err = db.commitCall(tx, row.ID); err != nil {
		return nil, false, err
	}
	claim = &softphoneClaim{row: current, peerToken: peerToken}
	if p != nil {
		claim.session = a.phoneSessionIssued(current, p, token, issued)
	}
	return claim, true, nil
}

// pickupTiming follows one browser pick-up from the answer request until its
// audio is live, on the server's clock, for the "softphone answer timing" line.
type pickupTiming struct {
	received, decided time.Time
	returned          atomic.Int64 // UnixNano when the session was sent back
}

// pickupMedia is the browser audio side of a pick-up. A connection prepared
// during the ring attached before the answer request.
type pickupMedia struct {
	attached, rtcReady, carrierAnswered, mediaStarted time.Time
	transport                                         string
	standby                                           bool
}

var pickupTimingWrites atomic.Uint64

func (a *App) recordPickupClaim(callID string, received, decided time.Time) *pickupTiming {
	t := &pickupTiming{received: received, decided: decided}
	a.pickupTimings.Store(callID, t)
	// A claim whose browser never attaches leaves its entry behind.
	if pickupTimingWrites.Add(1)%64 == 0 {
		cutoff := time.Now().Add(-2 * time.Minute)
		a.pickupTimings.Range(func(key, value any) bool {
			if value.(*pickupTiming).received.Before(cutoff) {
				a.pickupTimings.CompareAndDelete(key, value)
			}
			return true
		})
	}
	return t
}

func (t *pickupTiming) sessionReturned(at time.Time) { t.returned.Store(at.UnixNano()) }

// logPickupTiming writes one line per answered pick-up once its audio is live.
// Every value is milliseconds after the answer request arrived; a stage the
// browser completed during the ring counts as 0.
func (a *App) logPickupTiming(callID string, media pickupMedia, ready time.Time) {
	value, ok := a.pickupTimings.LoadAndDelete(callID)
	if !ok {
		return
	}
	t := value.(*pickupTiming)
	ms := func(at time.Time) int64 {
		if at.IsZero() {
			return -1
		}
		return max(at.Sub(t.received).Milliseconds(), 0)
	}
	returned := time.Time{}
	if n := t.returned.Load(); n != 0 {
		returned = time.Unix(0, n)
	}
	fields := []any{"call", callID,
		"claim_ms", ms(t.decided), "session_returned_ms", ms(returned),
		"browser_attach_ms", ms(media.attached), "rtc_ready_ms", ms(media.rtcReady),
		"carrier_answer_ms", ms(media.carrierAnswered), "media_start_ms", ms(media.mediaStarted),
		"total_ms", ms(ready), "transport", media.transport, "standby", media.standby}
	if pickupTimingHook != nil {
		pickupTimingHook(fields)
	}
	logSoftphone("softphone answer timing", fields...)
}

// pickupTimingHook receives each timing line, in tests only.
var pickupTimingHook func(fields []any)
