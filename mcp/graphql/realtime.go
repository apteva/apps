package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

type subscription struct {
	project string
	topic   string
	events  chan map[string]any
	done    chan struct{}
}

type subscriptionHub struct {
	mu     sync.Mutex
	closed bool
	subs   map[string]map[*subscription]struct{}
}

func newSubscriptionHub() *subscriptionHub {
	return &subscriptionHub{subs: map[string]map[*subscription]struct{}{}}
}

func (h *subscriptionHub) key(project, topic string) string { return project + "\x00" + topic }

func (h *subscriptionHub) subscribe(project, topic string) (*subscription, func()) {
	s := &subscription{project: project, topic: topic, events: make(chan map[string]any, 16), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(s.done)
		return s, func() {}
	}
	key := h.key(project, topic)
	if h.subs[key] == nil {
		h.subs[key] = map[*subscription]struct{}{}
	}
	h.subs[key][s] = struct{}{}
	h.mu.Unlock()
	return s, func() { h.unsubscribe(s) }
}

func (h *subscriptionHub) unsubscribe(s *subscription) {
	h.mu.Lock()
	key := h.key(s.project, s.topic)
	if group := h.subs[key]; group != nil {
		delete(group, s)
		if len(group) == 0 {
			delete(h.subs, key)
		}
	}
	h.mu.Unlock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

func (h *subscriptionHub) publish(project, topic string, payload map[string]any) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0
	}
	count := 0
	for sub := range h.subs[h.key(project, topic)] {
		select {
		case sub.events <- payload:
			count++
		default:
			// A slow client must not block unrelated realtime subscribers.
		}
	}
	return count
}

func (h *subscriptionHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, group := range h.subs {
		for sub := range group {
			close(sub.done)
		}
	}
	h.subs = map[string]map[*subscription]struct{}{}
}

var websocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 16 << 10,
	CheckOrigin: func(r *http.Request) bool {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" || strings.EqualFold(origin, "null") {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && strings.EqualFold(u.Host, r.Host)
	},
}

type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (a *App) handleRealtime(w http.ResponseWriter, r *http.Request) {
	project, err := a.projectFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
		return
	}
	conn, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var writeMu sync.Mutex
	write := func(message wsMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(message)
	}
	var activeCancel func()
	defer func() {
		if activeCancel != nil {
			activeCancel()
		}
	}()
	for {
		var message wsMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		switch message.Type {
		case "connection_init":
			if err := write(wsMessage{Type: "connection_ack"}); err != nil {
				return
			}
		case "ping":
			if err := write(wsMessage{Type: "pong", Payload: message.Payload}); err != nil {
				return
			}
		case "subscribe":
			if activeCancel != nil {
				activeCancel()
			}
			var req graphqlRequest
			if err := json.Unmarshal(message.Payload, &req); err != nil {
				_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": "invalid subscribe payload"}})})
				continue
			}
			result, execErr := a.execute(r.Context(), project, normalizeEnvironment(req.Environment), req)
			if execErr != nil {
				_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": execErr.Error()}})})
				continue
			}
			_ = write(wsMessage{ID: message.ID, Type: "next", Payload: mustJSON(map[string]any{"data": result.Data, "errors": result.Errors})})
			topic := subscriptionTopic(a, project, req.Query, message.Payload)
			if topic == "" {
				_ = write(wsMessage{ID: message.ID, Type: "complete"})
				continue
			}
			sub, cancel := a.hub.subscribe(project, topic)
			activeCancel = cancel
			go func(id, topic string, sub *subscription, request graphqlRequest) {
				for {
					select {
					case <-sub.done:
						return
					case <-r.Context().Done():
						return
					case <-sub.events:
						next, err := a.execute(r.Context(), project, normalizeEnvironment(request.Environment), request)
						if err != nil {
							_ = write(wsMessage{ID: id, Type: "error", Payload: mustJSON([]map[string]any{{"message": err.Error()}})})
							continue
						}
						_ = write(wsMessage{ID: id, Type: "next", Payload: mustJSON(map[string]any{"data": next.Data, "errors": next.Errors})})
					}
				}
			}(message.ID, topic, sub, req)
		case "complete":
			if activeCancel != nil {
				activeCancel()
				activeCancel = nil
			}
		default:
			_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": "unsupported realtime message type"}})})
		}
	}
}

func (a *App) handleSourceEvent(_ *sdk.AppCtx, event sdk.Event) error {
	if a.hub == nil || event.ProjectID == "" {
		return nil
	}
	topic := event.Name()
	if table, ok := event.Data["table"].(string); ok && strings.TrimSpace(table) != "" {
		topic = "tables." + table + "." + topic
	}
	a.hub.publish(event.ProjectID, topic, event.Data)
	a.hub.publish(event.ProjectID, event.Name(), event.Data)
	return nil
}

func subscriptionTopic(a *App, project, query string, payload json.RawMessage) string {
	// The protocol permits an explicit topic for source adapters and tests.
	var envelope map[string]any
	if json.Unmarshal(payload, &envelope) == nil {
		if value, ok := envelope["topic"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	// Resolver configuration is the source of truth for normal subscriptions.
	doc, err := parseQueryOnly(query)
	if err != nil || len(doc.Operations) == 0 || len(doc.Operations[0].SelectionSet) == 0 {
		return ""
	}
	field, ok := doc.Operations[0].SelectionSet[0].(*ast.Field)
	if !ok {
		return ""
	}
	resolver, _ := getResolver(a.ctx.AppReadDB(), project, "Subscription", field.Name)
	if resolver == nil {
		return ""
	}
	if topic, ok := resolver.Config["topic"].(string); ok {
		return strings.TrimSpace(topic)
	}
	return ""
}

func parseQueryOnly(query string) (*ast.QueryDocument, error) {
	return parser.ParseQuery(&ast.Source{Name: "subscription.graphql", Input: query})
}

func mustJSON(value any) json.RawMessage {
	b, _ := json.Marshal(value)
	return b
}
