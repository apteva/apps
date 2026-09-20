package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

type subscription struct {
	project string
	api     string
	topic   string
	events  chan map[string]any
	done    chan struct{}
	slow    chan struct{}
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

func (h *subscriptionHub) apiKey(project, api, topic string) string {
	return project + "\x00" + normalizeAPISlug(api) + "\x00" + topic
}

func (h *subscriptionHub) subscribe(project, topic string) (*subscription, func()) {
	return h.subscribeForAPI(project, "default", topic)
}

func (h *subscriptionHub) subscribeForAPI(project, api, topic string) (*subscription, func()) {
	s := &subscription{project: project, api: normalizeAPISlug(api), topic: topic, events: make(chan map[string]any, 16), done: make(chan struct{}), slow: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(s.done)
		return s, func() {}
	}
	key := h.apiKey(project, api, topic)
	if h.subs[key] == nil {
		h.subs[key] = map[*subscription]struct{}{}
	}
	h.subs[key][s] = struct{}{}
	h.mu.Unlock()
	return s, func() { h.unsubscribe(s) }
}

func (h *subscriptionHub) unsubscribe(s *subscription) {
	h.mu.Lock()
	key := h.apiKey(s.project, s.api, s.topic)
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
	return h.publishForAPI(project, "default", topic, payload)
}

func (h *subscriptionHub) publishForAPI(project, api, topic string, payload map[string]any) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0
	}
	count := 0
	for sub := range h.subs[h.apiKey(project, api, topic)] {
		select {
		case sub.events <- payload:
			count++
		default:
			select {
			case <-sub.slow:
			default:
				close(sub.slow)
			}
		}
	}
	return count
}

func (h *subscriptionHub) publishAllAPIs(project, topic string, payload map[string]any) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0
	}
	prefix := project + "\x00"
	suffix := "\x00" + topic
	count := 0
	for key, group := range h.subs {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
			continue
		}
		for sub := range group {
			select {
			case sub.events <- payload:
				count++
			default:
				select {
				case <-sub.slow:
				default:
					close(sub.slow)
				}
			}
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
	public := strings.HasPrefix(r.URL.Path, "/public/realtime/")
	project := a.ctx.CurrentProject()
	var err error
	if !public {
		project, err = a.projectFromRequest(r)
	}
	if err != nil || project == "" {
		writeJSONError(w, http.StatusBadRequest, "project is required", "invalid_request")
		return
	}
	slug := realtimeAPISlugFromPath(r.URL.Path)
	if public {
		slug = strings.TrimPrefix(r.URL.Path, "/public/realtime/")
	}
	api, err := a.cachedAPI(project, slug)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error(), errorCode(err))
		return
	}
	release, err := getActiveAPIRelease(a.ctx.AppReadDB(), project, api.Slug)
	if err != nil {
		writeJSONError(w, 500, "release unavailable", "storage_error")
		return
	}
	policy, err := a.cachedSecurity(project, api.Slug)
	if release != nil {
		policy = release.Security
		err = nil
	}
	if err != nil || (public && policy.Mode != "auth") || (!public && policy.Mode != "platform") {
		writeJSONError(w, 403, "subscription endpoint is not enabled", "permission_denied")
		return
	}
	header := http.Header{}
	if strings.Contains(r.Header.Get("Sec-WebSocket-Protocol"), "graphql-transport-ws") {
		header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	}
	conn, err := websocketUpgrader.Upgrade(w, r, header)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(2 * time.Minute)) })
	var writeMu sync.Mutex
	write := func(message wsMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(message)
	}
	ctx, cancelConn := context.WithCancel(r.Context())
	defer cancelConn()
	operations := map[string]context.CancelFunc{}
	var operationsMu sync.Mutex
	defer func() {
		operationsMu.Lock()
		for _, cancel := range operations {
			cancel()
		}
		operationsMu.Unlock()
	}()
	initialized := false
	for {
		var message wsMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		switch message.Type {
		case "connection_init":
			if initialized {
				_ = write(wsMessage{Type: "error", Payload: mustJSON([]map[string]any{{"message": "too many initialization requests"}})})
				return
			}
			if public {
				var params map[string]any
				_ = json.Unmarshal(message.Payload, &params)
				token, _ := params["Authorization"].(string)
				if token == "" {
					token, _ = params["authorization"].(string)
				}
				authReq := r.Clone(ctx)
				authReq.Header = r.Header.Clone()
				authReq.Header.Set("Authorization", token)
				identity, authErr := a.authenticateGraphQL(authReq, project, api.Slug, policy)
				if authErr != nil {
					_ = write(wsMessage{Type: "error", Payload: mustJSON([]map[string]any{{"message": authErr.Error(), "extensions": map[string]any{"code": errorCode(authErr)}}})})
					return
				}
				ctx = context.WithValue(ctx, identityKey{}, identity)
				time.AfterFunc(time.Until(identity.Expires), cancelConn)
			}
			initialized = true
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
			if err := write(wsMessage{Type: "connection_ack"}); err != nil {
				return
			}
		case "ping":
			if err := write(wsMessage{Type: "pong", Payload: message.Payload}); err != nil {
				return
			}
		case "subscribe":
			if !initialized {
				_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": "connection is not initialized"}})})
				continue
			}
			if message.ID == "" {
				continue
			}
			operationsMu.Lock()
			_, exists := operations[message.ID]
			operationsMu.Unlock()
			if exists {
				_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": "operation id is already active"}})})
				continue
			}
			var req graphqlRequest
			if err := json.Unmarshal(message.Payload, &req); err != nil {
				_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": "invalid subscribe payload"}})})
				continue
			}
			environment := normalizeEnvironment(req.Environment)
			if policy.Mode == "auth" {
				environment = policy.Environment
			}
			opCtx, opCancel := context.WithCancel(ctx)
			result, execErr := a.execute(opCtx, project, api.Slug, environment, req)
			if execErr != nil {
				opCancel()
				_ = write(wsMessage{ID: message.ID, Type: "error", Payload: mustJSON([]map[string]any{{"message": execErr.Error()}})})
				continue
			}
			_ = write(wsMessage{ID: message.ID, Type: "next", Payload: mustJSON(map[string]any{"data": result.Data, "errors": result.Errors})})
			topic := subscriptionTopic(a, project, api.Slug, req.Query, message.Payload)
			if topic == "" {
				opCancel()
				_ = write(wsMessage{ID: message.ID, Type: "complete"})
				continue
			}
			sub, cancelSubscription := a.hub.subscribeForAPI(project, api.Slug, topic)
			operationsMu.Lock()
			operations[message.ID] = func() { opCancel(); cancelSubscription() }
			operationsMu.Unlock()
			go func(id, topic, environment string, sub *subscription, request graphqlRequest) {
				defer func() { operationsMu.Lock(); delete(operations, id); operationsMu.Unlock(); cancelSubscription() }()
				for {
					select {
					case <-sub.slow:
						_ = write(wsMessage{ID: id, Type: "error", Payload: mustJSON([]map[string]any{{"message": "subscription consumer is too slow", "extensions": map[string]any{"code": "backpressure_exceeded"}}})})
						return
					case <-sub.done:
						return
					case <-opCtx.Done():
						return
					case <-sub.events:
						next, err := a.execute(opCtx, project, api.Slug, environment, request)
						if err != nil {
							_ = write(wsMessage{ID: id, Type: "error", Payload: mustJSON([]map[string]any{{"message": err.Error()}})})
							continue
						}
						_ = write(wsMessage{ID: id, Type: "next", Payload: mustJSON(map[string]any{"data": next.Data, "errors": next.Errors})})
					}
				}
			}(message.ID, topic, environment, sub, req)
		case "complete":
			operationsMu.Lock()
			cancel := operations[message.ID]
			delete(operations, message.ID)
			operationsMu.Unlock()
			if cancel != nil {
				cancel()
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
	a.hub.publishAllAPIs(event.ProjectID, topic, event.Data)
	a.hub.publishAllAPIs(event.ProjectID, event.Name(), event.Data)
	return nil
}

func subscriptionTopic(a *App, project, apiSlug, query string, payload json.RawMessage) string {
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
	resolver, _ := getResolverForAPI(a.ctx.AppReadDB(), project, apiSlug, "Subscription", field.Name)
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
