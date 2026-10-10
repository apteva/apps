package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const audioNetworkQueueLimit = 1024

// Diagnostic IDs confer no authority. No random source or external operation is
// required to collect a connection event.
var audioNetworkEpoch = time.Now().UnixNano()
var audioNetworkConnectionSequence atomic.Uint64

func newAudioConnectionID() string {
	return fmt.Sprintf("browser-%x-%x", audioNetworkEpoch, audioNetworkConnectionSequence.Add(1))
}

// Fixed-width UTC timestamps keep indexed SQLite ordering correct even at exact seconds.
func audioNetworkTimestamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// No token, URL, authorization header or carrier control is accepted here.
type audioNetworkEvent struct {
	HTTPStatus            int            `json:"http_status,omitempty"`
	SessionIssuerIdentity *phoneIdentity `json:"session_issuer_identity,omitempty"`
	lastBrowser           audioDisconnectInfo
	ReplacedBy            *audioSessionCorrelation `json:"replaced_by,omitempty"`
	Disconnect            *audioDisconnectInfo     `json:"disconnect,omitempty"`
	Session               audioSessionCorrelation  `json:"session,omitempty"`
	CurrentSessionID      string                   `json:"current_session_id,omitempty"`
	MediaPath             *rtcMediaEndpoint        `json:"media_path,omitempty"`
	ShutdownIntent        string                   `json:"shutdown_intent,omitempty"`
	ID                    string                   `json:"id"`
	Event                 string                   `json:"event"`
	Action                string                   `json:"action"`
	CallID                string                   `json:"call_id"`
	ProjectID             string                   `json:"project_id"`
	ConnectionID          string                   `json:"connection_id"`
	AdviserIdentity       phoneIdentity            `json:"adviser_identity"`
	IdentitySource        string                   `json:"identity_source"`
	ClientIP              string                   `json:"client_ip,omitempty"`
	SocketPeerIP          string                   `json:"socket_peer_ip,omitempty"`
	AddressSource         string                   `json:"address_source"`
	Classification        string                   `json:"network_classification"`
	OccurredAt            string                   `json:"occurred_at"`
	Reason                string                   `json:"reason,omitempty"`
	CloseCode             int                      `json:"close_code,omitempty"`
	ExpiresAt             string                   `json:"-"`
	Retention             time.Duration            `json:"-"`
}

// Socket handlers only attempt a bounded in-memory append. A slow database or
// competing collector cannot delay a connection or its media loop.
type audioNetworkCollector struct {
	mu      sync.Mutex
	flushMu sync.Mutex
	pending []audioNetworkEvent
	dropped atomic.Uint64
}

func (c *audioNetworkCollector) enqueue(e audioNetworkEvent) {
	if !c.mu.TryLock() {
		c.dropped.Add(1)
		return
	}
	defer c.mu.Unlock()
	if len(c.pending) >= audioNetworkQueueLimit {
		c.dropped.Add(1)
		return
	}
	c.pending = append(c.pending, e)
}

func audioNetworkClassification(addr netip.Addr, exits string) string {
	if !addr.IsValid() {
		return "unknown"
	}
	// Configurable exact exits and CIDRs, with bounded parsing. This is a label,
	// never an access decision. A malformed entry is ignored.
	if len(exits) > 8192 {
		exits = exits[:8192]
	}
	for i, raw := range strings.Split(exits, ",") {
		if i >= 128 {
			break
		}
		raw = strings.TrimSpace(raw)
		if ip, err := netip.ParseAddr(raw); err == nil && ip.Unmap() == addr.Unmap() {
			return "known_vpn_exit"
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Bits() > 0 && prefix.Contains(addr.Unmap()) {
			return "known_vpn_exit"
		}
	}
	return "unknown"
}
func newAudioNetworkContext(row *callRow, identity phoneIdentity, r *http.Request, config map[string]string) audioNetworkEvent {
	address, _ := resolveAudioNetworkAddress(r)
	return newAudioNetworkContextWithAddress(row, identity, address, config)
}

type audioNetworkAddress struct {
	Client     netip.Addr
	SocketPeer netip.Addr
	Source     string
}

// Resolve once at the authenticated handshake, before upgrading or rewriting
// the request. Missing/invalid platform metadata falls back only to the socket
// peer; unsigned forwarded headers cannot override a rejected assertion.
func resolveAudioNetworkAddress(r *http.Request) (audioNetworkAddress, error) {
	peer, source := audioPeerAddress(r, "")
	address := audioNetworkAddress{Client: peer, SocketPeer: peer, Source: source}
	ip, err := sdk.ClientIPFromRequest(r)
	if err == nil && ip != "" {
		address.Client, _ = netip.ParseAddr(ip)
		address.Source = "trusted_proxy"
	}
	return address, err
}

func newAudioNetworkContextWithAddress(row *callRow, identity phoneIdentity, address audioNetworkAddress, config map[string]string) audioNetworkEvent {
	ip, peer := "", ""
	if address.Client.IsValid() {
		ip = address.Client.String()
	}
	if address.SocketPeer.IsValid() {
		peer = address.SocketPeer.String()
	}
	identitySource := "unattributed"
	if identity.valid() {
		identitySource = "validated_media_session"
	}
	return audioNetworkEvent{CallID: row.ID, ProjectID: row.ProjectID, AdviserIdentity: identity,
		IdentitySource: identitySource, ClientIP: ip, SocketPeerIP: peer, AddressSource: address.Source,
		Classification: audioNetworkClassification(address.Client, config["audio_telemetry_known_vpn_exits"]),
		Retention:      time.Duration(durationSetting(config, "audio_telemetry_network_retention_days", 7, 1, 31)) * 24 * time.Hour}
}

// Persist idempotently in the background, retaining the batch on failure. The
// database transaction never holds the collection lock. Expiry runs in bounded
// batches and expired records are excluded from reads immediately.
func (c *audioNetworkCollector) flush(ctx context.Context, db *callsDB, now time.Time) (map[string][]string, error) {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.mu.Lock()
	n := min(100, len(c.pending))
	batch := append([]audioNetworkEvent(nil), c.pending[:n]...)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	changed := map[string][]string{}
	for _, e := range batch {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO telephony_browser_network_events(id,call_id,project_id,occurred_at,expires_at,event_json) SELECT ?,id,project_id,?,?,? FROM calls WHERE id=? AND project_id=?`, e.ID, networkDatabaseTime(e.OccurredAt), networkDatabaseTime(e.ExpiresAt), string(raw), e.CallID, e.ProjectID)
		if err != nil {
			return nil, err
		}
		if inserted, _ := result.RowsAffected(); inserted > 0 {
			changed[e.ProjectID] = append(changed[e.ProjectID], e.CallID)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_browser_network_events WHERE id IN (SELECT id FROM telephony_browser_network_events WHERE expires_at<=? ORDER BY expires_at LIMIT 100)`, audioNetworkTimestamp(now)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	remaining := copy(c.pending, c.pending[n:])
	clear(c.pending[remaining:])
	c.pending = c.pending[:remaining]
	c.mu.Unlock()
	return changed, nil
}
func (c *callsDB) browserNetworkEvents(ctx context.Context, project, call string, now time.Time) ([]audioNetworkEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := c.db.QueryContext(ctx, `SELECT event_json FROM telephony_browser_network_events WHERE project_id=? AND call_id=? AND expires_at>? ORDER BY occurred_at DESC LIMIT 100`, project, call, audioNetworkTimestamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []audioNetworkEvent{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e audioNetworkEvent
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func networkDatabaseTime(value string) string {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return audioNetworkTimestamp(at)
}

// Normalized server reason codes only. Free-form peer close text may contain
// secrets or URLs, so it is not copied into the observational store.
func audioNetworkCloseReason(reason string) string {
	switch reason {
	case "handler_closed", "session_replaced", "transport_read_error", "peer_close", "media_session_missing", "media_session_lookup_failed", "media_lease_expired", "media_token_replaced", "media_principal_invalid", "user_access_revoked", "call_ownership_changed", "call_permission_revoked", "policy_lookup_failed", "owner_lookup_failed":
		return reason
	default:
		return "handler_closed"
	}
}
