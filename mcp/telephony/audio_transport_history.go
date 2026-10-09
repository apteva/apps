package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Browser-supplied measurements never carry authority or raw RTC reports.
// History lives outside calls so list/SSE projections remain small.
type browserTransportSample struct {
	Complete              bool               `json:"complete"`
	PartIndex             int                `json:"part_index"`
	PartCount             int                `json:"part_count"`
	Parts                 map[string]bool    `json:"parts,omitempty"`
	ID                    string             `json:"id"`
	Timestamp             string             `json:"timestamp"`
	Transport             string             `json:"transport"`
	Reason                string             `json:"reason"`
	WindowMS              float64            `json:"window_ms"`
	Metrics               map[string]float64 `json:"metrics"`
	States                map[string]string  `json:"states"`
	ConnectionID          string             `json:"connection_id,omitempty"`
	ReceivingConnectionID string             `json:"receiving_connection_id,omitempty"`
	ReceivedAt            string             `json:"received_at,omitempty"`
	CallID                string             `json:"-"`
	ProjectID             string             `json:"-"`
	ExpiresAt             string             `json:"-"`
}

var browserTransportMetricKeys = []string{"rtt_ms", "queue_ms", "target_ms", "buffered_bytes", "underruns", "dropped_ms", "capture_sequence_gaps", "playback_sequence_gaps", "main_thread_max_pause_ms", "worker_max_tick_gap_ms", "capture_sent_ms", "capture_muted_ms", "capture_dropped_ms", "playback_ingress_ms", "playback_transport_dropped_ms", "playback_source_dropped_ms", "clock_uncertainty_ms", "clock_sample_age_ms", "stats_errors", "stats_duration_ms", "send_bitrate_bps", "receive_bitrate_bps", "receive_gap_ms", "capture_age_ms", "transit_ms", "delivery_excess_ms", "server_queue_ms", "receiver_bytesReceived", "receiver_packetsReceived", "receiver_packetsLost", "receiver_jitter", "receiver_packetsDiscarded", "receiver_concealedSamples", "receiver_silentConcealedSamples", "receiver_concealmentEvents", "receiver_insertedSamplesForDeceleration", "receiver_removedSamplesForAcceleration", "receiver_totalSamplesReceived", "receiver_audioLevel", "receiver_totalAudioEnergy", "receiver_totalSamplesDuration", "receiver_nackCount", "receiver_fecPacketsReceived", "receiver_fecPacketsDiscarded", "receiver_jitter_buffer_interval_ms", "receiver_jitter_target_interval_ms", "receiver_jitter_minimum_interval_ms", "receiver_processing_interval_ms", "receiver_last_packet_age_ms", "sender_bytesSent", "sender_packetsSent", "sender_headerBytesSent", "sender_retransmittedPacketsSent", "sender_retransmittedBytesSent", "sender_nackCount", "sender_targetBitrate", "sender_send_delay_interval_ms", "remote_receiver_packetsLost", "remote_receiver_fractionLost", "remote_receiver_jitter", "remote_receiver_roundTripTime", "remote_receiver_totalRoundTripTime", "remote_receiver_roundTripTimeMeasurements", "remote_sender_packetsSent", "remote_sender_bytesSent", "pair_availableOutgoingBitrate", "pair_availableIncomingBitrate", "pair_currentRoundTripTime", "pair_totalRoundTripTime", "pair_bytesSent", "pair_bytesReceived", "pair_requestsSent", "pair_requestsReceived", "pair_responsesSent", "pair_responsesReceived", "pair_consentRequestsSent", "path_revision", "codec_clock_rate", "codec_channels"}
var browserTransportStateKeys = map[string][]string{"connection": {"connected", "reconnecting", "closed"}, "context": {"running", "suspended", "interrupted", "closed"}, "muted": {"true", "false"}, "device_muted": {"true", "false"}, "track": {"live", "ended"}, "ice": {"new", "checking", "connected", "completed", "disconnected", "failed", "closed"}, "dtls": {"new", "connecting", "connected", "closed", "failed"}, "pair": {"frozen", "waiting", "in-progress", "failed", "succeeded"}, "local_candidate": {"host", "srflx", "prflx", "relay"}, "remote_candidate": {"host", "srflx", "prflx", "relay"}, "protocol": {"udp", "tcp"}, "relay_protocol": {"udp", "tcp", "tls"}, "codec": {"audio/opus", "audio/PCMU", "audio/PCMA", "pcm16"}}

func normalizeTransportSamples(samples []browserTransportSample, now time.Time) []browserTransportSample {
	out := make([]browserTransportSample, 0, min(len(samples), 4))
	for _, s := range samples[:min(len(samples), 4)] {
		at, err := time.Parse(time.RFC3339Nano, s.Timestamp)
		if err != nil || at.After(now.Add(time.Minute)) || at.Before(now.Add(-24*time.Hour)) || len(s.ID) == 0 || len(s.ID) > 64 {
			continue
		}
		validID := true
		for _, c := range s.ID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':') {
				validID = false
				break
			}
		}
		if !validID || s.Transport != "websocket" && s.Transport != "webrtc" {
			continue
		}
		switch s.Reason {
		case "periodic", "incident", "incident_context":
		default:
			continue
		}
		if s.PartCount == 0 {
			s.PartCount = 1
			s.PartIndex = 0
		}
		if s.PartCount < 1 || s.PartCount > 16 || s.PartIndex < 0 || s.PartIndex >= s.PartCount {
			continue
		}
		s.Parts = map[string]bool{fmt.Sprint(s.PartIndex): true}
		s.Timestamp = audioNetworkTimestamp(at)
		s.ReceivedAt = audioNetworkTimestamp(now)
		s.Complete = false
		s.ConnectionID = ""
		s.ReceivingConnectionID = ""
		s.CallID = ""
		s.ProjectID = ""
		s.ExpiresAt = ""
		if math.IsNaN(s.WindowMS) || math.IsInf(s.WindowMS, 0) {
			s.WindowMS = 0
		}
		s.WindowMS = math.Max(0, math.Min(s.WindowMS, 60000))
		values := map[string]float64{}
		for _, k := range browserTransportMetricKeys {
			if n, ok := s.Metrics[k]; ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
				values[k] = math.Max(0, math.Min(n, 1e12))
			}
		}
		s.Metrics = values
		labels := map[string]string{}
		for k, allowed := range browserTransportStateKeys {
			for _, v := range allowed {
				if s.States[k] == v {
					labels[k] = v
					break
				}
			}
		}
		s.States = labels
		out = append(out, s)
	}
	return out
}

// Media only attempts a bounded append. Flushes, failures and retention work
// happen in the existing background worker, never under the media lock.
type audioTransportCollector struct {
	mu          sync.Mutex
	flushMu     sync.Mutex
	pending     []browserTransportSample
	dropped     atomic.Uint64
	lastCleanup time.Time
}

func (c *audioTransportCollector) enqueue(samples []browserTransportSample, row *callRow) {
	if len(samples) == 0 {
		return
	}
	if !c.mu.TryLock() {
		c.dropped.Add(uint64(len(samples)))
		return
	}
	defer c.mu.Unlock()
	for _, s := range samples {
		if len(c.pending) >= 1024 {
			c.dropped.Add(1)
			continue
		}
		s.CallID = row.ID
		s.ProjectID = row.ProjectID
		c.pending = append(c.pending, s)
	}
}
func (c *audioTransportCollector) flush(ctx context.Context, db *callsDB, now time.Time) error {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.mu.Lock()
	n := min(100, len(c.pending))
	batch := append([]browserTransportSample(nil), c.pending[:n]...)
	c.mu.Unlock()
	if n == 0 && !c.lastCleanup.IsZero() && now.Sub(c.lastCleanup) < time.Minute {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := map[[2]string]bool{}
	for _, s := range batch {
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		kind := "incident"
		if s.Reason == "periodic" {
			kind = "periodic"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO telephony_browser_transport_samples(id,call_id,project_id,kind,occurred_at,expires_at,sample_json) SELECT ?,id,project_id,?,?,?,? FROM calls WHERE id=? AND project_id=? ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,sample_json=json_patch(telephony_browser_transport_samples.sample_json,excluded.sample_json) WHERE telephony_browser_transport_samples.call_id=excluded.call_id AND telephony_browser_transport_samples.project_id=excluded.project_id AND telephony_browser_transport_samples.occurred_at=excluded.occurred_at AND json_extract(telephony_browser_transport_samples.sample_json,'$.part_count')=json_extract(excluded.sample_json,'$.part_count')`, s.ID, kind, s.Timestamp, s.ExpiresAt, string(raw), s.CallID, s.ProjectID); err != nil {
			return err
		}
		changed[[2]string{s.ProjectID, s.CallID}] = true
	}
	for key := range changed {
		for _, kind := range []string{"periodic", "incident"} {
			limit := 96
			if kind == "incident" {
				limit = 32
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_browser_transport_samples WHERE id IN (SELECT id FROM telephony_browser_transport_samples WHERE project_id=? AND call_id=? AND kind=? ORDER BY occurred_at DESC,id DESC LIMIT -1 OFFSET ?)`, key[0], key[1], kind, limit); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_browser_transport_samples WHERE id IN (SELECT id FROM telephony_browser_transport_samples WHERE expires_at<=? ORDER BY expires_at LIMIT 100)`, audioNetworkTimestamp(now)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c.lastCleanup = now
	c.mu.Lock()
	remaining := copy(c.pending, c.pending[n:])
	clear(c.pending[remaining:])
	c.pending = c.pending[:remaining]
	c.mu.Unlock()
	return nil
}
func (c *callsDB) browserTransportSamples(ctx context.Context, project, call string, now time.Time) ([]browserTransportSample, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := c.db.QueryContext(ctx, `SELECT sample_json FROM telephony_browser_transport_samples WHERE project_id=? AND call_id=? AND expires_at>? ORDER BY occurred_at DESC,id DESC LIMIT 128`, project, call, audioNetworkTimestamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []browserTransportSample{}
	for rows.Next() {
		var raw string
		var s browserTransportSample
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, err
		}
		s.Complete = s.PartCount > 0 && len(s.Parts) == s.PartCount
		out = append(out, s)
	}
	return out, rows.Err()
}
func transportSampleID(connection, epoch, id string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{connection, epoch, id}, "\x00")))
	return fmt.Sprintf("transport-%x", digest[:16])
}
