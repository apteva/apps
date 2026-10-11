package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const carrierReplacementStartTimeout = 3 * time.Second

// Missing audio is not sufficient to restart a call. It permits only an
// authenticated, format/identity-verified replacement of a continuous stream.
// Healthy duplicates, intentional hold and discontinuous media retain ownership.
func (b *carrierBridgeLease) expectsContinuousMedia() bool {
	if !b.continuousInput.Load() {
		return false
	}
	if r := b.reception.Load(); r != nil {
		if !r.mu.TryLock() {
			return false
		}
		defer r.mu.Unlock()
		return r.s.ContinuousExpected && !r.paused
	}
	return true
}
func (b *carrierBridgeLease) replaceable(now time.Time) bool {
	last := b.arrival.last.Load()
	return b.ctx.Err() != nil || b.expectsContinuousMedia() && last > 0 && now.Sub(time.Unix(0, last)) >= time.Duration(carrierDeliveryStallMS)*time.Millisecond
}

// Candidate reads never hold the call claim lock or modify the existing bridge.
// A per-generation gate, bounded message count and deadline bound candidate work.
func (a *App) claimJSONCarrierStream(w http.ResponseWriter, r *http.Request, row *callRow, cfg jsonMediaBridgeConfig, proxyID string) (*carrierBridgeLease, net.Conn, net.Conn, error) {
	b, err := a.claimCarrierBridge(row, r.Context(), proxyID)
	if !errors.Is(err, errCarrierBridgeBusy) {
		return b, nil, nil, err
	}
	a.mediaBridges.mu.Lock()
	old := a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if old == nil || !cfg.ContinuousInput || !old.replaceable(time.Now()) || !old.replacementPending.CompareAndSwap(false, true) {
		return nil, nil, nil, errCarrierBridgeBusy
	}
	defer old.replacementPending.Store(false)
	conn, reader, err := upgradeBuffered(w, r)
	if err != nil {
		return nil, nil, nil, err
	}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	defer func() { writer.Stop(); <-writer.done }()
	startTimer := time.AfterFunc(carrierReplacementStartTimeout, func() { _ = conn.Close() })
	defer startTimer.Stop()
	accepted := false
	defer func() {
		if !accepted {
			_ = conn.Close()
		}
	}()
	reject := func(reason string, err error) (*carrierBridgeLease, net.Conn, net.Conn, error) {
		e := normalizeCarrierEvidence(row, carrierBridgeEvidence{Kind: "replacement_rejected", Trigger: reason, Detail: transportEvidenceError(err), ProxyConnectionID: proxyID, BridgeGeneration: old.generation})
		a.carrierTelemetry.enqueue(carrierTelemetryRecord{ID: e.ID, InstallID: a.installID, ProjectID: row.ProjectID, CallID: row.ID, Kind: "event", At: e.At, BridgeGeneration: old.generation, ProxyConnectionID: proxyID, Event: &e})
		_ = writer.write(ws.OpClose, ws.NewCloseFrameBody(ws.StatusPolicyViolation, reason), liveAudioWriteTimeout)
		return nil, conn, reader, errCarrierBridgeBusy
	}
	_ = conn.SetReadDeadline(time.Now().Add(carrierReplacementStartTimeout))
	var replay bytes.Buffer
	mediaStream := ""
	for i := 0; i < 4; i++ {
		data, op, err := readWebSocketData(reader, ws.StateServerSide, writer)
		if err != nil {
			return reject("replacement_start_failed", err)
		}
		if op != ws.OpText || len(data) > 16*1024 {
			return reject("replacement_start_invalid", errors.New("invalid candidate start frame"))
		}
		var f carrierMediaFrame
		if err = json.Unmarshal(data, &f); err != nil {
			return reject("replacement_start_invalid", err)
		}
		if f.Event == "error" {
			return reject("replacement_provider_error", errors.New(carrierFrameError(cfg.Provider, f).Detail))
		}
		if f.Event == "media" {
			sid := frameStreamID(f)
			if sid == "" || mediaStream != "" && mediaStream != sid {
				return reject("replacement_identity_invalid", errors.New("candidate media stream mismatch"))
			}
			mediaStream = sid
		}
		if f.Event != "connected" && f.Event != "start" && f.Event != "media" {
			return reject("replacement_start_required", errors.New("candidate must identify the stream before media"))
		}
		if err = wsutil.WriteMessage(&replay, ws.StateClientSide, op, data); err != nil {
			return reject("replacement_replay_failed", err)
		}
		if f.Event != "start" {
			continue
		}
		if err = validateCarrierStartFormat(cfg, f); err != nil {
			return reject("replacement_format_invalid", err)
		}
		sid := frameStreamID(f)
		if row.CarrierSID == "" || frameCallID(f) != row.CarrierSID || boundedTransportID(sid) == "" || mediaStream != "" && sid != mediaStream {
			return reject("replacement_identity_invalid", errors.New("candidate call or stream identity mismatch"))
		}
		if !startTimer.Stop() {
			return reject("replacement_start_timeout", errors.New("candidate deadline elapsed"))
		}
		if err = conn.SetReadDeadline(time.Time{}); err != nil {
			return reject("replacement_deadline_failed", err)
		}
		fresh, err := a.claimCarrierBridgeVerified(row, r.Context(), &carrierReplacementProof{Generation: old.generation, StreamID: sid}, proxyID)
		if err != nil {
			return reject("replacement_owner_changed", err)
		}
		if !fresh.bind(conn) {
			return reject("replacement_canceled", errors.New("call canceled before replacement binding"))
		}
		fresh.continuousInput.Store(cfg.ContinuousInput)
		accepted = true
		return fresh, conn, hijackedConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(replay.Bytes()), reader)}, nil
	}
	return reject("replacement_start_required", errors.New("candidate start frame limit reached"))
}

func carrierFrameError(provider string, f carrierMediaFrame) carrierBridgeEvidence {
	raw := f.Payload
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw, _ = json.Marshal(f.Error)
	}
	var detail struct {
		Code   int    `json:"code"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &detail)
	message := provider + " reported a media stream error"
	if detail.Code != 0 || detail.Title != "" || detail.Detail != "" {
		message = fmt.Sprintf("%s media error %d %s: %s", provider, detail.Code, detail.Title, detail.Detail)
	} else if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		message += ": " + limitDiagnosticText(string(raw), 512)
	}
	return carrierBridgeEvidence{Kind: "provider_stream_error", Leg: string(mediaCloseLegCarrier), Trigger: "provider_stream_error", ProviderEvent: "error", StreamID: frameStreamID(f), Detail: message, ProviderErrorCode: detail.Code, ProviderErrorTitle: detail.Title, ProviderErrorDetail: detail.Detail}
}
func (b *carrierBridgeLease) recordDecodeError(err error) {
	b.arrival.decodeErrors.Add(1)
	now := time.Now().UnixNano()
	previous := b.decodeEvidenceAt.Load()
	if now-previous < int64(time.Second) || !b.decodeEvidenceAt.CompareAndSwap(previous, now) {
		return
	}
	e := normalizeCarrierEvidence(&b.row, carrierBridgeEvidence{Kind: "frame_decode_error", Leg: string(mediaCloseLegCarrier), Detail: transportEvidenceError(err), BridgeGeneration: b.generation, ProxyConnectionID: b.proxyID})
	b.app.carrierTelemetry.enqueue(carrierTelemetryRecord{ID: e.ID, InstallID: b.app.installID, ProjectID: b.row.ProjectID, CallID: b.row.ID, Kind: "event", At: e.At, BridgeGeneration: b.generation, ProxyConnectionID: b.proxyID, Event: &e})
}
