package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const rtcSendSampleLimit = 64
const rtcSendIncidentLimit = 16
const rtcPathSampleLimit = 8
const rtcSlowSendThreshold = 10 * time.Millisecond

type rtcSendEvent struct {
	At           time.Time `json:"at"`
	ConnectionID string    `json:"connection_id"`
	PathRevision uint64    `json:"path_revision"`
	SSRC         uint32    `json:"ssrc"`
	Sequence     uint16    `json:"sequence"`
	RTPTimestamp uint32    `json:"rtp_timestamp"`
	PayloadBytes int       `json:"payload_bytes"`
	WriteUS      int64     `json:"write_us"`
	Outcome      string    `json:"outcome"`
}
type rtcPathEvent struct {
	At              time.Time `json:"at"`
	ConnectionID    string    `json:"connection_id"`
	Revision        uint64    `json:"revision"`
	Protocol        string    `json:"protocol"`
	LocalCandidate  string    `json:"local_candidate"`
	RemoteCandidate string    `json:"remote_candidate"`
}

// Addresses are kept only in the existing operator-only, expiring network store.
// An ICE remote may be a TURN relay; it is not necessarily the browser's public IP.
type rtcMediaEndpoint struct {
	rtcPathEvent
	LocalIP              string `json:"local_ip,omitempty"`
	LocalPort            uint16 `json:"local_port"`
	RemoteIP             string `json:"remote_ip,omitempty"`
	RemotePort           uint16 `json:"remote_port"`
	RemoteClassification string `json:"remote_network_classification"`
	AddressSource        string `json:"address_source"`
}
type rtcSendSnapshot struct {
	SamplesUnavailable      bool           `json:"samples_unavailable,omitempty"`
	Attempts                uint64         `json:"attempts"`
	Errors                  uint64         `json:"errors"`
	MaxWriteUS              int64          `json:"max_write_us"`
	TotalWriteUS            int64          `json:"total_write_us"`
	PathChanges             uint64         `json:"path_changes"`
	PathObservationsSkipped uint64         `json:"path_observations_skipped"`
	ObservationsSkipped     uint64         `json:"observations_skipped"`
	Recent                  []rtcSendEvent `json:"recent,omitempty"`
	Incidents               []rtcSendEvent `json:"incidents,omitempty"`
	Paths                   []rtcPathEvent `json:"paths,omitempty"`
}

// A fixed ring, no audio payloads, database, logging or formatting on RTP writes.
// A concurrent snapshot must never make a media sender wait for telemetry.
type rtcSendTelemetry struct {
	connectionID              string
	ssrc                      uint32
	attempts                  atomic.Uint64
	errors                    atomic.Uint64
	maxWriteUS                atomic.Int64
	totalWriteUS              atomic.Int64
	skipped                   atomic.Uint64
	pathRevision              atomic.Uint64
	pathSkipped               atomic.Uint64
	mu                        sync.Mutex
	recent                    [rtcSendSampleLimit]rtcSendEvent
	incidents                 [rtcSendIncidentLimit]rtcSendEvent
	paths                     [rtcPathSampleLimit]rtcPathEvent
	recentN, incidentN, pathN uint64
}

func (d *rtcSendTelemetry) observe(p *rtp.Packet, at time.Time, elapsed time.Duration, revision uint64, err error) {
	d.attempts.Add(1)
	us := max(int64(0), elapsed.Microseconds())
	d.totalWriteUS.Add(us)
	for old := d.maxWriteUS.Load(); us > old; old = d.maxWriteUS.Load() {
		if d.maxWriteUS.CompareAndSwap(old, us) {
			break
		}
	}
	if err != nil {
		d.errors.Add(1)
	}
	if !d.mu.TryLock() {
		d.skipped.Add(1)
		return
	}
	defer d.mu.Unlock()
	outcome := "sent"
	if err != nil {
		outcome = "send_error"
		var n net.Error
		if errors.Is(err, net.ErrClosed) {
			outcome = "connection_closed"
		} else if errors.As(err, &n) && n.Timeout() {
			outcome = "send_timeout"
		}
	}
	e := rtcSendEvent{At: at.UTC(), ConnectionID: d.connectionID, PathRevision: revision, SSRC: d.ssrc, Sequence: p.SequenceNumber, RTPTimestamp: p.Timestamp, PayloadBytes: len(p.Payload), WriteUS: us, Outcome: outcome}
	d.recent[d.recentN%rtcSendSampleLimit] = e
	d.recentN++
	if err != nil || elapsed >= rtcSlowSendThreshold {
		d.incidents[d.incidentN%rtcSendIncidentLimit] = e
		d.incidentN++
	}
}
func chronologicalRing[T any](ring []T, n uint64) []T {
	size := min(n, uint64(len(ring)))
	out := make([]T, 0, int(size))
	for i := n - size; i < n; i++ {
		out = append(out, ring[i%uint64(len(ring))])
	}
	return out
}
func (d *rtcSendTelemetry) snapshot() rtcSendSnapshot {
	s := rtcSendSnapshot{}
	if d.mu.TryLock() {
		s = rtcSendSnapshot{Recent: chronologicalRing(d.recent[:], d.recentN), Incidents: chronologicalRing(d.incidents[:], d.incidentN), Paths: chronologicalRing(d.paths[:], d.pathN)}
		d.mu.Unlock()
	} else {
		s.SamplesUnavailable = true
	}
	s.Attempts = d.attempts.Load()
	s.Errors = d.errors.Load()
	s.MaxWriteUS = d.maxWriteUS.Load()
	s.TotalWriteUS = d.totalWriteUS.Load()
	s.ObservationsSkipped = d.skipped.Load()
	s.PathChanges = d.pathRevision.Load()
	s.PathObservationsSkipped = d.pathSkipped.Load()
	return s
}
func mergeRTCSends(a, b rtcSendSnapshot) rtcSendSnapshot {
	a.SamplesUnavailable = a.SamplesUnavailable || b.SamplesUnavailable
	a.Attempts += b.Attempts
	a.Errors += b.Errors
	a.TotalWriteUS += b.TotalWriteUS
	a.MaxWriteUS = max(a.MaxWriteUS, b.MaxWriteUS)
	a.ObservationsSkipped += b.ObservationsSkipped
	a.PathChanges += b.PathChanges
	a.PathObservationsSkipped += b.PathObservationsSkipped
	a.Recent = mergeBoundedRTPEvidence(a.Recent, b.Recent, rtcSendSampleLimit)
	a.Incidents = mergeBoundedRTPEvidence(a.Incidents, b.Incidents, rtcSendIncidentLimit)
	a.Paths = mergeBoundedRTPEvidence(a.Paths, b.Paths, rtcPathSampleLimit)
	return a
}

// Reconnect summaries must not alias buffers a concurrent JSON reader holds.
func mergeBoundedRTPEvidence[T any](a, b []T, limit int) []T {
	n := min(limit, len(a)+len(b))
	if n == 0 {
		return nil
	}
	out := make([]T, 0, n)
	if len(b) < n {
		out = append(out, a[len(a)-(n-len(b)):]...)
	}
	return append(out, b[max(0, len(b)-n):]...)
}

func canonicalRTCIP(s string) string {
	if a, e := netip.ParseAddr(s); e == nil {
		return a.Unmap().String()
	}
	return ""
}
func (d *rtcSendTelemetry) selectedPath(pair *webrtc.ICECandidatePair, base audioNetworkEvent, knownExits string, collect func(audioNetworkEvent)) {
	if pair == nil || pair.Local == nil || pair.Remote == nil {
		return
	}
	at := time.Now().UTC()
	e := rtcPathEvent{At: at, ConnectionID: d.connectionID, Revision: d.pathRevision.Add(1), Protocol: pair.Local.Protocol.String(), LocalCandidate: pair.Local.Typ.String(), RemoteCandidate: pair.Remote.Typ.String()}
	if d.mu.TryLock() {
		d.paths[d.pathN%rtcPathSampleLimit] = e
		d.pathN++
		d.mu.Unlock()
	} else {
		d.pathSkipped.Add(1)
	}
	if collect == nil {
		return
	}
	remote := canonicalRTCIP(pair.Remote.Address)
	ip, _ := netip.ParseAddr(remote)
	base.ConnectionID = d.connectionID
	base.ID = fmt.Sprintf("%s:rtc-path:%d", d.connectionID, e.Revision)
	base.Event = "softphone.browser.media_path_selected"
	base.Action = "media_path_selected"
	base.OccurredAt = audioNetworkTimestamp(at)
	base.ExpiresAt = audioNetworkTimestamp(at.Add(base.Retention))
	base.MediaPath = &rtcMediaEndpoint{rtcPathEvent: e, LocalIP: canonicalRTCIP(pair.Local.Address), LocalPort: pair.Local.Port, RemoteIP: remote, RemotePort: pair.Remote.Port, RemoteClassification: audioNetworkClassification(ip, knownExits), AddressSource: "ice_selected_remote"}
	collect(base)
}

// Write success means acceptance by Pion's send stack, not browser reception.
func writeObservedRTP(write func(*rtp.Packet) error, p *rtp.Packet, d *rtcSendTelemetry) error {
	revision := d.pathRevision.Load()
	started := time.Now()
	err := write(p)
	d.observe(p, started, time.Since(started), revision, err)
	return err
}
