package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const callStreamLease = 20 * time.Second
const callStreamHeartbeat = 5 * time.Second
const callStreamLimit = 512
const callProjectStreamLimit = 128

type callChangeHub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
	count       int
}

func (h *callChangeHub) subscribe(project string) (<-chan struct{}, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers == nil {
		h.subscribers = map[string]map[chan struct{}]struct{}{}
	}
	group := h.subscribers[project]
	if h.count >= callStreamLimit || len(group) >= callProjectStreamLimit {
		return nil, nil, errors.New("notification capacity reached")
	}
	if group == nil {
		group = map[chan struct{}]struct{}{}
		h.subscribers[project] = group
	}
	ch := make(chan struct{}, 1)
	group[ch] = struct{}{}
	h.count++
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(group, ch)
			h.count--
			if len(group) == 0 {
				delete(h.subscribers, project)
			}
			h.mu.Unlock()
		})
	}, nil
}
func (h *callChangeHub) notify(project string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers[project] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Fingerprint only the visible call/offer state. Unrelated users' changes never
// produce events, and hints carry no call data or global sequence counters.
func (a *App) callNotificationSnapshot(r *http.Request, project string) ([32]byte, error) {
	rows, err := a.recentPhoneCalls(r, project, 100)
	if err != nil {
		return [32]byte{}, err
	}
	type offer struct{ ID, Destination, Kind, Expires string }
	type entry struct {
		ID, Status, Peer, Destination, Media string
		Offers                               []offer
	}
	entries := make([]entry, 0, len(rows))
	for _, row := range rows {
		e := entry{ID: row.ID, Status: row.Status, Peer: row.PeerKind, Destination: row.RoutingDestinationID, Media: row.MediaStatus}
		offers, err := a.db().activeRingOffers(row.ID, project)
		if err != nil {
			return [32]byte{}, err
		}
		p := phoneUserFrom(r)
		for _, o := range offers {
			if p != nil && (!p.Destinations[o.DestinationID] || !a.destinationAllowsIdentity(project, o.DestinationID, p.Identity)) {
				continue
			}
			e.Offers = append(e.Offers, offer{o.ID, o.DestinationID, o.Kind, o.ExpiresAt})
		}
		entries = append(entries, e)
	}
	raw, err := json.Marshal(entries)
	return sha256.Sum256(raw), err
}
func (a *App) handleCallNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", 403)
		return
	}
	changes, close, err := a.callChanges.subscribe(project)
	if err != nil {
		http.Error(w, "notification capacity reached", 503)
		return
	}
	defer close()
	control := http.NewResponseController(w)
	// A slow or abandoned browser must not retain a stream worker indefinitely.
	write := func(payload string) error {
		_ = control.SetWriteDeadline(time.Now().Add(3 * time.Second))
		defer control.SetWriteDeadline(time.Time{})
		if _, e := fmt.Fprint(w, payload); e != nil {
			return e
		}
		return control.Flush()
	}
	send := func(payload string) error { return write("data: " + payload + "\n\n") }
	current, err := a.callNotificationSnapshot(r, project)
	if err != nil {
		http.Error(w, "call state unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if send(`{"type":"calls.changed"}`) != nil {
		return
	}
	heartbeat := time.NewTicker(callStreamHeartbeat)
	defer heartbeat.Stop()
	lease := time.NewTimer(callStreamLease)
	defer lease.Stop()
	for {
		heartbeatDue := false
		select {
		case <-r.Context().Done():
			return
		case <-lease.C:
			return // reconnect through the gateway with current credentials
		case <-changes:
		case <-heartbeat.C:
			heartbeatDue = true
		}
		checked := r
		if original, ok := r.Context().Value(phoneStreamRequestKey{}).(*http.Request); ok {
			var status int
			checked, status, err = a.authenticateApplicationSession(original)
			_ = status
			if err != nil {
				_ = send(`{"type":"access.revoked"}`)
				return
			}
		} else if p := phoneUserFrom(r); p != nil {
			fresh, e := a.phonePrincipal(project, p.Identity)
			if e != nil {
				_ = send(`{"type":"access.revoked"}`)
				return
			}
			checked = withPhonePrincipal(r, fresh)
		}
		snapshot, e := a.callNotificationSnapshot(checked, project)
		if e != nil {
			return
		}
		if snapshot != current {
			current = snapshot
			if send(`{"type":"calls.changed"}`) != nil {
				return
			}
		} else if heartbeatDue {
			if write(": keepalive\n\n") != nil {
				return
			}
		}
	}
}

type phoneStreamRequestKey struct{}

func withPhonePrincipal(r *http.Request, p *phonePrincipal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), phonePrincipalKey{}, p))
}
