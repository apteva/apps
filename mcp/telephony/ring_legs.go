package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
)

const peerKindExternal = "external"

// Outbound legs use the existing carrier adapters and the same 24kHz PCM
// bridge as browser calls. Only the claimed leg is connected to the caller.
func (a *App) startRingLeg(ctx *sdk.AppCtx, parent *callRow, offer ringOffer) error {
	var config map[string]any
	if err := json.Unmarshal([]byte(offer.ConfigJSON), &config); err != nil {
		return err
	}
	to := routingConfigString(config, "phone_number")
	if offer.Kind == "sip" {
		to = routingConfigString(config, "uri")
	}
	if offer.Kind == "pstn" && !validE164(to) {
		return errors.New("invalid ring destination phone number")
	}
	if offer.Kind == "sip" && !validRingSIPURI(to) {
		return errors.New("invalid ring destination SIP URI")
	}
	// Use the inbound number's provider and owned caller ID. A team member cannot
	// substitute another project's integration or an arbitrary caller ID.
	fields := map[string]string{}
	if parent.CarrierSlug == "telnyx" {
		route, err := a.db().findRoute(parent.RouteID)
		if err != nil || route == nil {
			return firstError(err, errors.New("ring route unavailable"))
		}
		var saved telnyxRouteConfig
		if err = json.Unmarshal([]byte(route.PreviousVoiceURL), &saved); err != nil {
			return err
		}
		fields["connection_id"] = saved.ApplicationID
	}
	carrier, err := a.carrierForSlug(parent.CarrierSlug, parent.CarrierConnectionID, fields)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expires, err := time.Parse(time.RFC3339Nano, offer.ExpiresAt)
	if err != nil {
		return err
	}
	timeout := int(time.Until(expires).Seconds())
	if timeout < 1 {
		return nil
	}
	childID := "leg_" + newCallID()
	// Insert once before the provider request. A lost response/restart does not
	// make another paid call: callbacks correlate through this durable child ID.
	res, err := ctx.AppDB().Exec(`INSERT OR IGNORE INTO call_legs(id,call_id,project_id,destination_id,provider,direction,kind,status,started_at,offer_id) SELECT ?,?,?,?,?, 'outbound',?,'placing',?,? WHERE EXISTS(SELECT 1 FROM call_offers o JOIN calls c ON c.id=o.call_id WHERE o.id=? AND o.status='offered' AND o.expires_at>? AND c.status='pending')`, childID, parent.ID, parent.ProjectID, offer.DestinationID, parent.CarrierSlug, offer.Kind, ringTime(now), offer.ID, offer.ID, ringTime(now))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	child := callRow{ID: childID, ThreadID: "external-" + childID, Direction: "outbound", AgentID: 0, CarrierSlug: parent.CarrierSlug, CarrierConnectionID: parent.CarrierConnectionID, CallbackSecret: newSecret(), ToNumber: to, FromNumber: parent.ToNumber, IngressPath: "ring_group", Status: "initiated", PlacedAt: now.Format(time.RFC3339), ProjectID: parent.ProjectID, StateExpiresAt: expires.Format(time.RFC3339), DeadlineAt: parent.DeadlineAt, PeerKind: peerKindExternal, RecordingMode: recordingModeOff, RecordingChannels: "dual", RecordingStorageMode: recordingStorageCopy, IdempotencyKey: "ring:" + offer.ID}
	child.AudioBridgeURL = a.peerLoopbackURL(&child)
	if err = a.placeOutboundLeg(ctx, carrier, &child, timeout, 3600, nil); err != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE call_legs SET status='failed',error_message=? WHERE id=?`, err.Error(), childID)
		_, _ = ctx.AppDB().Exec(`UPDATE call_offers SET status='failed',last_error=? WHERE id=? AND status='offered'`, err.Error(), offer.ID)
		return err
	}
	_, err = ctx.AppDB().Exec(`UPDATE call_legs SET status='ringing',provider_call_id=? WHERE id=? AND status='placing'`, child.CarrierSID, childID)
	return err
}

func validRingSIPURI(uri string) bool {
	if len(uri) > 512 || strings.ContainsAny(uri, "\r\n\t ") {
		return false
	}
	return (strings.HasPrefix(uri, "sip:") || strings.HasPrefix(uri, "sips:")) && strings.Contains(uri, "@")
}

func (a *App) runRingLegTick(_ context.Context, ctx *sdk.AppCtx) error {
	if ctx.CurrentProject() == "" {
		return nil
	}
	// Reconcile in stable order; claims are atomic even if callbacks or another
	// worker observes two answers at the same instant.
	// Failed answer setup can reactivate sibling offers after their phone legs
	// were already canceled. Reconcile those terminal legs too so the offer
	// fails promptly instead of waiting for a phone that is no longer ringing.
	// A canceled leg without a provider ID has nothing left to reconcile until
	// its late callback supplies that ID. Exclude it from the bounded work batch
	// so uncertain requests cannot starve newly answered calls. An in-flight
	// placement is likewise picked up once its child call record exists.
	rows, err := ctx.AppDB().Query(`SELECT l.id,l.call_id,l.offer_id FROM call_legs l JOIN calls child ON child.id=l.id JOIN call_offers o ON o.id=l.offer_id WHERE l.project_id=? AND (l.status NOT IN ('finished','canceled') OR o.status='offered') AND (l.status<>'cancel_pending' OR child.carrier_sid<>'' OR o.status='offered') ORDER BY COALESCE(NULLIF(child.answered_at,''),'9999'),l.started_at LIMIT 100`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	type leg struct{ id, parent, offer string }
	legs := []leg{}
	for rows.Next() {
		var l leg
		if err = rows.Scan(&l.id, &l.parent, &l.offer); err != nil {
			rows.Close()
			return err
		}
		legs = append(legs, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, l := range legs {
		if err := a.reconcileRingLeg(ctx, l.id, l.parent, l.offer); err != nil {
			ctx.Logger().Warn("reconcile ring leg", "call", l.parent, "leg", l.id, "err", err)
		}
	}
	return nil
}

func (a *App) reconcileRingLeg(ctx *sdk.AppCtx, childID, parentID, offerID string) error {
	parent, err := a.db().findCall(parentID)
	if err != nil || parent == nil {
		return err
	}
	child, err := a.db().findCall(childID)
	if err != nil {
		return err
	}
	var offerStatus, destination string
	if err = ctx.AppDB().QueryRow(`SELECT status,destination_id FROM call_offers WHERE id=?`, offerID).Scan(&offerStatus, &destination); err != nil {
		return err
	}
	if child == nil {
		return nil
	} // In-flight placement; never retry an ambiguous dial.
	if isTerminalStatus(parent.Status) || offerStatus == "canceled" || offerStatus == "expired" || offerStatus == "declined" || offerStatus == "failed" {
		if child.CarrierSID != "" { // Even a local failed placement may receive a late provider ID.
			var finished string
			_ = ctx.AppDB().QueryRow(`SELECT status FROM call_legs WHERE id=?`, childID).Scan(&finished)
			if finished != "canceled" && finished != "finished" {
				carrier, err := a.carrierForRow(ctx, nil, child)
				if err != nil {
					return err
				}
				if err = carrier.Hangup(ctx, child); err != nil {
					return err
				}
			}
		}
		if !isTerminalStatus(child.Status) {
			if err = a.db().updateStatus(child.ID, "canceled", "another destination answered or ringing ended"); err != nil {
				return err
			}
		}
		status := "canceled"
		if child.CarrierSID == "" {
			status = "cancel_pending"
		}
		_, err = ctx.AppDB().Exec(`UPDATE call_legs SET status=?,ended_at=? WHERE id=?`, status, ringTime(time.Now()), childID)
		return err
	}
	if isTerminalStatus(child.Status) {
		_, err = ctx.AppDB().Exec(`UPDATE call_offers SET status='failed',last_error=? WHERE id=? AND status='offered'`, child.Status, offerID)
		if err != nil {
			return err
		}
		if offerStatus == "claimed" && !isTerminalStatus(parent.Status) {
			if message := a.hangupCall(ctx, parent.ID, 0, parent.ProjectID); message != "" {
				return errors.New(message)
			}
		}
		_, err = ctx.AppDB().Exec(`UPDATE call_legs SET status='finished',ended_at=? WHERE id=?`, ringTime(time.Now()), childID)
		return err
	}
	if child.Status != "answered" && child.Status != "in-progress" {
		return nil
	}
	if offerStatus == "offered" {
		grouped, won, err := a.db().claimRingOffer(parentID, parent.ProjectID, destination, "external", 0)
		if err != nil {
			return err
		}
		if !grouped || !won {
			return nil
		}
	}
	parent, err = a.db().findCall(parentID)
	if err != nil {
		return err
	}
	if parent.RoutingDestinationID != destination || parent.PeerKind != peerKindExternal {
		return nil
	}
	if parent.Status == "answering" {
		parent.AudioBridgeURL = a.peerLoopbackURL(parent)
		parent.ThreadID = "external-" + parent.ID
		if _, err = ctx.AppDB().Exec(`UPDATE calls SET audio_bridge_url=?,thread_id=? WHERE id=? AND status='answering'`, parent.AudioBridgeURL, parent.ThreadID, parent.ID); err != nil {
			return err
		}
		if err = a.answerInboundCarrierCall(ctx, parent); err != nil {
			return err
		}
		if err = a.db().updateStatus(parent.ID, "answered", ""); err != nil {
			return err
		}
		a.softphones.updateCallState(parent.ID, parent.Direction, "answered")
		if a.callUsesDirectSIP(parent) {
			if gateway := a.directSIPGateway(); gateway != nil {
				if err = gateway.StartMedia(parent); err != nil {
					return err
				}
			}
		}
	}
	_, err = ctx.AppDB().Exec(`UPDATE call_legs SET status='connected',answered_at=CASE WHEN answered_at='' THEN ? ELSE answered_at END WHERE id=?`, ringTime(time.Now()), childID)
	return err
}

func (a *App) startOfferedRingLegs(ctx *sdk.AppCtx, parent *callRow, offers []ringOffer) {
	// Provider calls have independent latency. Start a simultaneous tier with
	// bounded concurrency, rather than waiting for each remote dial in turn.
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, o := range offers {
		if o.Kind != "pstn" && o.Kind != "sip" {
			continue
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(o ringOffer) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := a.startRingLeg(ctx, parent, o); err != nil {
				ctx.Logger().Warn("start ring leg", "call", parent.ID, "destination", o.DestinationID, "err", err)
			}
		}(o)
	}
	wg.Wait()
}

// Both sides are trusted carrier bridges authenticated with independent call
// secrets. A non-winning child never attaches to the shared hub.
func (a *App) handleRingPeerSocket(w http.ResponseWriter, r *http.Request, row *callRow, token string) {
	if !secureEqual(token, row.CallbackSecret) || isTerminalStatus(row.Status) {
		http.Error(w, "forbidden", 403)
		return
	}
	parentID := row.ID
	childSide := false
	var offerID string
	err := a.db().db.QueryRow(`SELECT call_id,offer_id FROM call_legs WHERE id=? AND offer_id<>''`, row.ID).Scan(&parentID, &offerID)
	if err == nil {
		childSide = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "load call leg", 500)
		return
	}
	// Early-media sockets may precede the answer callback (notably Telnyx).
	// Keep their own socket open while discarding pre-answer audio, then attach
	// only when the scheduler has selected this leg.
	conn, readConn, err := upgradeBuffered(w, r)
	if err != nil {
		return
	}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	closer := newGracefulWebSocket(conn, writer)
	defer closer.Close(ws.StatusNormalClosure, "ring leg closed")
	var hub *softphoneHub
	attached := false
	stopped := false
	var attachMu sync.Mutex
	done := make(chan struct{})
	defer close(done)
	attach := func() bool {
		attachMu.Lock()
		defer attachMu.Unlock()
		if stopped {
			return false
		}
		if attached {
			return true
		}
		parent, err := a.db().findCall(parentID)
		if err != nil || parent == nil || isTerminalStatus(parent.Status) {
			return false
		}
		if childSide {
			var status string
			_ = a.db().db.QueryRow(`SELECT status FROM call_offers WHERE id=?`, offerID).Scan(&status)
			if status != "claimed" {
				return false
			}
		}
		if parent.PeerKind != peerKindExternal || (parent.Status != "answered" && parent.Status != "in-progress") {
			return false
		}
		hub = a.softphones.hubFor(parentID)
		hub.setCallState(parent.Direction, parent.Status)
		var old *websocketWriterPump
		if childSide {
			old = hub.setBrowser(writer)
		} else {
			old = hub.setPeer(writer)
		}
		if old != nil {
			old.Stop()
			_ = old.conn.Close()
		}
		attached = true
		return true
	}
	defer func() {
		attachMu.Lock()
		defer attachMu.Unlock()
		stopped = true
		if attached {
			if childSide {
				hub.clearBrowser(writer)
			} else {
				hub.clearPeer(writer)
			}
			a.softphones.dropIfEmpty(parentID, hub)
		}
	}()
	// This also attaches a silent answered caller, and closes losing sockets even
	// when their peer is silent and the read below is blocked.
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				current, err := a.db().findCall(row.ID)
				if err != nil || current == nil || isTerminalStatus(current.Status) {
					_ = conn.Close()
					return
				}
				if childSide {
					var status string
					_ = a.db().db.QueryRow(`SELECT status FROM call_offers WHERE id=?`, offerID).Scan(&status)
					if status != "offered" && status != "claimed" {
						_ = conn.Close()
						return
					}
				}
				if attach() {
					ticker.Reset(time.Second)
				}
			}
		}
	}()
	for {
		data, op, err := readWebSocketData(readConn, ws.StateServerSide, writer)
		if err != nil {
			return
		}
		if op != ws.OpBinary || !attach() {
			continue
		}
		attachMu.Lock()
		activeHub := hub
		attachMu.Unlock()
		if childSide {
			activeHub.mu.Lock()
			target := activeHub.peer
			activeHub.mu.Unlock()
			if target != nil {
				target.QueueAudio(data)
			}
		} else {
			activeHub.toBrowser(ws.OpBinary, data)
		}
	}
}

// Called at the authenticated answer callback before slower event publication.
// The first accepted answer claims immediately, rather than whichever dial was
// created first when a worker later observes several answered legs.
func (a *App) claimAnsweredRingLeg(childID string) error {
	child, err := a.db().findCall(childID)
	if err != nil || child == nil {
		return err
	}
	if child.Status != "answered" && child.Status != "in-progress" {
		return nil
	}
	var parent, destination string
	err = a.db().db.QueryRow(`SELECT call_id,destination_id FROM call_legs WHERE id=? AND offer_id<>''`, childID).Scan(&parent, &destination)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, _, err = a.db().claimRingOffer(parent, child.ProjectID, destination, "external", 0)
	return err
}
