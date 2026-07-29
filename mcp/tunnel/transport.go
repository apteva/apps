package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apteva/apps/mcp/tunnel/protocol"
	"github.com/gorilla/websocket"
)

var requestSequence atomic.Uint64
var errConnectorBusy = errors.New("connector request limit reached")

type connectorManager struct {
	mu   sync.RWMutex
	byID map[string]*connector
}

func newConnectorManager() *connectorManager {
	return &connectorManager{byID: map[string]*connector{}}
}

func (m *connectorManager) attach(id string, conn *websocket.Conn, maxConcurrent int) *connector {
	next := &connector{
		id:      id,
		conn:    conn,
		closed:  make(chan struct{}),
		pending: map[string]chan protocol.Message{},
		slots:   make(chan struct{}, maxConcurrent),
	}
	m.mu.Lock()
	previous := m.byID[id]
	m.byID[id] = next
	m.mu.Unlock()
	if previous != nil {
		previous.close(websocket.ClosePolicyViolation, "replaced by a newer connector")
	}
	return next
}

func (m *connectorManager) detach(item *connector) {
	m.mu.Lock()
	if m.byID[item.id] == item {
		delete(m.byID, item.id)
	}
	m.mu.Unlock()
	item.close(websocket.CloseNormalClosure, "connector closed")
}

func (m *connectorManager) get(id string) *connector {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byID[id]
}

func (m *connectorManager) connected(id string) bool {
	return m.get(id) != nil
}

func (m *connectorManager) disconnect(id, reason string) {
	if item := m.get(id); item != nil {
		item.close(websocket.ClosePolicyViolation, reason)
	}
}

func (m *connectorManager) closeAll() {
	m.mu.RLock()
	items := make([]*connector, 0, len(m.byID))
	for _, item := range m.byID {
		items = append(items, item)
	}
	m.mu.RUnlock()
	for _, item := range items {
		item.close(websocket.CloseGoingAway, "server shutting down")
	}
}

type connector struct {
	id        string
	conn      *websocket.Conn
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan protocol.Message
	closeOnce sync.Once
	closed    chan struct{}
	slots     chan struct{}
}

func (c *connector) close(code int, reason string) {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.writeMu.Lock()
		_ = c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason),
			time.Now().Add(time.Second),
		)
		_ = c.conn.Close()
		c.writeMu.Unlock()

		c.pendingMu.Lock()
		for id, waiter := range c.pending {
			delete(c.pending, id)
			close(waiter)
		}
		c.pendingMu.Unlock()
	})
}

func (c *connector) readLoop(maxBytes int64) error {
	c.conn.SetReadLimit(maxBytes*2 + (1 << 20))
	_ = c.conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	})
	for {
		var message protocol.Message
		if err := c.conn.ReadJSON(&message); err != nil {
			return err
		}
		if message.Type != protocol.TypeResponse || message.ID == "" {
			continue
		}
		if int64(len(message.Body)) > maxBytes {
			message.Body = nil
			message.Error = "connector response exceeds configured body limit"
		}
		c.pendingMu.Lock()
		waiter := c.pending[message.ID]
		if waiter != nil {
			delete(c.pending, message.ID)
		}
		c.pendingMu.Unlock()
		if waiter != nil {
			waiter <- message
			close(waiter)
		}
	}
}

func (c *connector) pingLoop() {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.writeMu.Lock()
			err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				c.close(websocket.CloseAbnormalClosure, "ping failed")
				return
			}
		case <-c.closed:
			return
		}
	}
}

func (c *connector) roundTrip(ctx context.Context, message protocol.Message) (protocol.Message, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return protocol.Message{}, errConnectorBusy
	}
	message.Type = protocol.TypeRequest
	message.ID = fmt.Sprintf("req_%x_%x", time.Now().UnixNano(), requestSequence.Add(1))
	waiter := make(chan protocol.Message, 1)
	c.pendingMu.Lock()
	c.pending[message.ID] = waiter
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err := c.conn.WriteJSON(message)
	c.writeMu.Unlock()
	if err != nil {
		c.removeWaiter(message.ID)
		return protocol.Message{}, fmt.Errorf("send request to connector: %w", err)
	}

	select {
	case response, ok := <-waiter:
		if !ok {
			return protocol.Message{}, errors.New("connector disconnected")
		}
		if response.Error != "" {
			return protocol.Message{}, errors.New(response.Error)
		}
		if response.StatusCode < 100 || response.StatusCode > 599 {
			return protocol.Message{}, errors.New("connector returned an invalid status code")
		}
		return response, nil
	case <-ctx.Done():
		c.removeWaiter(message.ID)
		return protocol.Message{}, ctx.Err()
	case <-c.closed:
		c.removeWaiter(message.ID)
		return protocol.Message{}, errors.New("connector disconnected")
	}
}

func (c *connector) removeWaiter(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func copyHeaders(input http.Header) map[string][]string {
	output := make(map[string][]string, len(input))
	for key, values := range input {
		if isHopHeader(key) {
			continue
		}
		output[key] = append([]string(nil), values...)
	}
	return output
}

func publicForwardHeaders(r *http.Request) map[string][]string {
	output := copyHeaders(r.Header)
	// Server-native app-token ingress authenticates the host router to the
	// sidecar with Authorization. The visitor's original value is carried in
	// this internal header and must be restored before sending the request to
	// the local target. Never leak platform credentials or routing headers.
	visitorAuthorization := r.Header.Get("X-Apteva-Original-Authorization")
	delete(output, "Authorization")
	delete(output, "X-Apteva-Original-Authorization")
	delete(output, "X-Apteva-App-Token")
	delete(output, "X-Apteva-App-Install-Id")
	delete(output, "X-Apteva-Project-Id")
	if visitorAuthorization != "" {
		output["Authorization"] = []string{visitorAuthorization}
	}
	return output
}

func isHopHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
