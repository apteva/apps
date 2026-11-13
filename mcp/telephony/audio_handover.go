package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pion/webrtc/v4"
)

// Audio handover lets a browser replace its connection while the old one still
// carries audio, for when signaling drops but RTP does not. It is off unless
// softphone_webrtc_handover is set, and costs nothing until signaling fails.
//
// Authorization never gets looser. A candidate holds a separate grant that only
// handleSoftphoneMedia reads. Issuing any ordinary session clears it, so a
// takeover or a newer attach fences every candidate, and the candidate replaces
// the active grant only after its own audio path has connected.

func softphoneHandoverEnabled(project string) bool {
	if globalCtx == nil {
		return false
	}
	config := globalCtx.WithProject(project).Config()
	return config["softphone_webrtc_enabled"] == "true" && config["softphone_webrtc_handover"] == "true"
}

func (a *App) issuePendingPhoneSession(ctx context.Context, row *callRow, p *phonePrincipal, d audioSessionCorrelation, g *phoneAttachmentGuard) (*softphoneSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !g.valid() || g.Expected == "" {
		return nil, errPhoneSessionChanged
	}
	principal := ""
	var identity phoneIdentity
	if p != nil {
		fresh, err := a.phonePrincipal(row.ProjectID, p.Identity)
		if err != nil {
			return nil, err
		}
		if _, _, err = a.phoneOwner(row.ID); err != nil {
			return nil, err
		}
		if !a.phoneCallAllowed(fresh.forAction("call.attach"), row, false) {
			return nil, errPhoneAccessDenied
		}
		principal = p.Identity.key()
		identity = p.Identity
	}
	s := normalizeAudioSessionCorrelation(d)
	s.SessionID = newAudioSessionID()
	s.PreviousSessionID = g.Expected
	s.IssuedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(s)
	token := newSecret()
	now := time.Now().Unix()
	// Each attempt replaces the last, so at most one candidate can win.
	res, err := a.db().db.ExecContext(ctx, `UPDATE telephony_media_sessions SET pending_token_hash=?,pending_expires_at=?,pending_diagnostic_json=? WHERE call_id=? AND project_id=? AND principal=? AND expires_at>? AND COALESCE(json_extract(CASE WHEN json_valid(diagnostic_json) THEN diagnostic_json ELSE '{}' END,'$.session_id'),'')=?`,
		phoneHash(token), now+phoneLeaseSeconds, string(raw), row.ID, row.ProjectID, principal, now, g.Expected)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errPhoneSessionChanged
	}
	a.enqueueHandoverEvent(audioNetworkEvent{CallID: row.ID, ProjectID: row.ProjectID, AdviserIdentity: identity, IdentitySource: "authorized_http_session", Session: s, Retention: 7 * 24 * time.Hour}, "softphone.media.handover_pending", "")
	return &softphoneSession{CallID: row.ID, MediaURL: a.softphoneMediaURL(row.ID, token), SessionToken: token, SessionGeneration: s.SessionID, To: row.ToNumber, From: row.FromNumber, LeaseSeconds: phoneLeaseSeconds, AttachmentGuardVersion: 1, HandoverPending: true}, nil
}

// Reports whether the token is the current candidate grant. The reason covers the
// same permission checks as an active session, so a revoked user cannot connect.
func (a *App) phoneMediaPendingCheck(row *callRow, token string) (reason string, expiry int64, identity phoneIdentity, session audioSessionCorrelation, pending bool) {
	var hash, principal, raw string
	err := a.db().db.QueryRow(`SELECT pending_token_hash,principal,pending_expires_at,pending_diagnostic_json FROM telephony_media_sessions WHERE call_id=? AND project_id=?`, row.ID, row.ProjectID).Scan(&hash, &principal, &expiry, &raw)
	if err != nil || hash == "" || expiry <= time.Now().Unix() || !secureEqual(phoneHash(token), hash) {
		return "", 0, phoneIdentity{}, audioSessionCorrelation{}, false
	}
	_ = json.Unmarshal([]byte(raw), &session)
	reason, identity = a.phoneMediaPrincipalReason(row, principal)
	return reason, expiry, identity, session, true
}

// Atomic: the candidate replaces the active grant only if it is still the
// candidate. Zero rows means a takeover or a newer attach got there first.
func (a *App) commitPendingPhoneSession(row *callRow, token string) (bool, error) {
	res, err := a.db().db.Exec(`UPDATE telephony_media_sessions SET token_hash=pending_token_hash,expires_at=pending_expires_at,diagnostic_json=json_set(pending_diagnostic_json,'$.previous_session_id',COALESCE(json_extract(CASE WHEN json_valid(diagnostic_json) THEN diagnostic_json ELSE '{}' END,'$.session_id'),'')),pending_token_hash='',pending_expires_at=0,pending_diagnostic_json='{}' WHERE call_id=? AND project_id=? AND pending_token_hash=? AND pending_expires_at>?`,
		row.ID, row.ProjectID, phoneHash(token), time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (a *App) enqueueHandoverEvent(network audioNetworkEvent, event, reason string) {
	now := time.Now().UTC()
	network.ID = newAudioConnectionID() + ":handover"
	network.Event = event
	network.Action = "handover"
	network.Reason = reason
	network.OccurredAt = now.Format(time.RFC3339Nano)
	network.ExpiresAt = now.Add(network.Retention).Format(time.RFC3339Nano)
	a.audioNetworks.enqueue(network)
}

// Variables so tests need not wait out the production limits.
var (
	rtcHandoverSample    = 500 * time.Millisecond
	rtcHandoverHoldLimit = 20 * time.Second
	rtcHandoverQuiet     = 3 * time.Second
)

// Called after signaling failed. Returns when the connection should stop: at once
// unless RTP is demonstrably still arriving, otherwise when it is replaced, when
// RTP stops, or at the limit. It extends nothing: the media watchdog keeps checking
// revocation, takeover and lease expiry, and the lease needs a renewal over HTTP.
func holdRTCForHandover(pc *webrtc.PeerConnection, stats *rtcMediaStats, done <-chan struct{}, holding func()) {
	if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		return
	}
	before := stats.ingress.packets.Load()
	sample := time.NewTimer(rtcHandoverSample)
	defer sample.Stop()
	select {
	case <-done:
		return
	case <-sample.C:
	}
	last := stats.ingress.packets.Load()
	if last == before || pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		return
	}
	holding()
	limit := time.NewTimer(rtcHandoverHoldLimit)
	defer limit.Stop()
	tick := time.NewTicker(max(rtcHandoverQuiet/3, time.Millisecond))
	defer tick.Stop()
	quietSince := time.Now()
	for {
		select {
		case <-done:
			return
		case <-limit.C:
			return
		case now := <-tick.C:
			if n := stats.ingress.packets.Load(); n != last {
				last, quietSince = n, now
			} else if now.Sub(quietSince) >= rtcHandoverQuiet {
				return
			}
		}
	}
}

var errHandoverNotCommitted = errors.New("handover candidate was fenced")

// Runs under the call's claim lock, after the candidate's own audio path connected.
// On error the predecessor is untouched and keeps carrying the call.
func (a *App) commitHandover(row *callRow, token string, network audioNetworkEvent) (int64, phoneIdentity, error) {
	reason, expiry, identity, session, pending := a.phoneMediaPendingCheck(row, token)
	if !pending || reason != "" {
		if reason == "" {
			reason = "candidate_fenced"
		}
		a.enqueueHandoverEvent(network, "softphone.media.handover_failed", reason)
		return 0, phoneIdentity{}, errHandoverNotCommitted
	}
	committed, err := a.commitPendingPhoneSession(row, token)
	if err != nil || !committed {
		a.enqueueHandoverEvent(network, "softphone.media.handover_failed", "candidate_fenced")
		return 0, phoneIdentity{}, errHandoverNotCommitted
	}
	if h := a.softphones.lookup(row.ID); h != nil {
		h.invalidateCoaching()
		h.telemetry.sessionIssued(row, session, identity, a.audioNetworks.enqueue)
	}
	a.enqueueHandoverEvent(network, "softphone.media.handover_committed", "")
	return expiry, identity, nil
}
