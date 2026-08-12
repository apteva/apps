package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultEventCoalesce  = 200 * time.Millisecond
	eventHeartbeat        = 15 * time.Second
	eventReplayCapacity   = 256
	maxEventMatchValues   = 64
	maxPendingProjections = 256
)

type appEventsConfig struct {
	Topics     []string       `json:"topics"`
	Match      map[string]any `json:"match,omitempty"`
	Output     map[string]any `json:"output"`
	CoalesceMS int            `json:"coalesce_ms,omitempty"`
}

type appBusEvent struct {
	Topic     string          `json:"topic"`
	App       string          `json:"app"`
	ProjectID string          `json:"project_id"`
	InstallID int64           `json:"install_id"`
	Seq       uint64          `json:"seq"`
	Time      string          `json:"time"`
	Data      json.RawMessage `json:"data"`
}

func parseAppEventsConfig(raw string) (*appEventsConfig, error) {
	var cfg appEventsConfig
	if err := json.Unmarshal([]byte(defaultJSON(raw)), &cfg); err != nil {
		return nil, errors.New("events must be a JSON object")
	}
	if len(cfg.Topics) == 0 || len(cfg.Topics) > 32 {
		return nil, errors.New("events.topics must contain 1 to 32 topic patterns")
	}
	for _, topic := range cfg.Topics {
		if !validEventTopicPattern(topic) {
			return nil, fmt.Errorf("invalid event topic pattern %q", topic)
		}
	}
	if len(cfg.Match) > 16 {
		return nil, errors.New("events.match supports at most 16 fields")
	}
	for path, value := range cfg.Match {
		if !strings.HasPrefix(path, "data.") || len(strings.TrimPrefix(path, "data.")) == 0 {
			return nil, fmt.Errorf("events.match path %q must start with data.", path)
		}
		if err := validateEventMatchCondition(path, value); err != nil {
			return nil, err
		}
	}
	if len(cfg.Output) == 0 {
		return nil, errors.New("events.output must be a non-empty object")
	}
	if encoded, err := json.Marshal(cfg.Output); err != nil || len(encoded) > 8*1024 {
		return nil, errors.New("events.output must be valid JSON no larger than 8 KiB")
	}
	if err := validateEventOutput(cfg.Output, cfg.Match, nil); err != nil {
		return nil, err
	}
	if cfg.CoalesceMS == 0 {
		cfg.CoalesceMS = int(defaultEventCoalesce / time.Millisecond)
	}
	if cfg.CoalesceMS < 0 || cfg.CoalesceMS > 5000 {
		return nil, errors.New("events.coalesce_ms must be between 0 and 5000")
	}
	return &cfg, nil
}

func validateEventMatchCondition(path string, condition any) error {
	if isJSONScalar(condition) {
		return nil
	}
	operator, ok := condition.(map[string]any)
	if !ok || len(operator) != 1 {
		return fmt.Errorf("events.match value for %q must be a JSON scalar or an in operator", path)
	}
	rawValues, ok := operator["in"]
	if !ok {
		return fmt.Errorf("events.match value for %q only supports the in operator", path)
	}
	values, ok := rawValues.([]any)
	if !ok || len(values) == 0 || len(values) > maxEventMatchValues {
		return fmt.Errorf("events.match in list for %q must contain 1 to %d values", path, maxEventMatchValues)
	}
	for _, value := range values {
		if !isJSONScalar(value) {
			return fmt.Errorf("events.match in list for %q must contain only JSON scalars", path)
		}
	}
	return nil
}

func validateEventOutput(value any, match map[string]any, outputPath []string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if err := validateEventOutput(child, match, append(outputPath, key)); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := validateEventOutput(child, match, append(outputPath, strconv.Itoa(i))); err != nil {
				return err
			}
		}
	case string:
		path, projected := eventProjectionPath(typed)
		if !projected {
			return nil
		}
		if len(outputPath) == 1 && outputPath[0] == "type" {
			return errors.New("events.output.type must be static")
		}
		if _, constrained := match[path]; !constrained {
			return fmt.Errorf("events.output projection %q must reference a field constrained by events.match", typed)
		}
	}
	return nil
}

func eventProjectionPath(value string) (string, bool) {
	if !strings.HasPrefix(value, "$data.") || len(strings.TrimPrefix(value, "$data.")) == 0 {
		return "", false
	}
	return strings.TrimPrefix(value, "$"), true
}

func validEventTopicPattern(topic string) bool {
	topic = strings.TrimSpace(topic)
	if topic == "*" {
		return true
	}
	if strings.HasSuffix(topic, ".*") {
		topic = strings.TrimSuffix(topic, ".*")
	}
	if topic == "" || strings.ContainsAny(topic, " \t\r\n/*") {
		return false
	}
	for _, part := range strings.Split(topic, ".") {
		if part == "" {
			return false
		}
	}
	return true
}

func isJSONScalar(v any) bool {
	switch v.(type) {
	case nil, bool, float64, string:
		return true
	default:
		return false
	}
}

func eventMatches(cfg *appEventsConfig, ev appBusEvent) bool {
	_, ok := projectAppEvent(cfg, ev)
	return ok
}

func projectAppEvent(cfg *appEventsConfig, ev appBusEvent) ([]byte, bool) {
	topicOK := false
	for _, pattern := range cfg.Topics {
		if pattern == "*" || pattern == ev.Topic ||
			(strings.HasSuffix(pattern, ".*") && strings.HasPrefix(ev.Topic, strings.TrimSuffix(pattern, "*"))) {
			topicOK = true
			break
		}
	}
	if !topicOK {
		return nil, false
	}
	var data any
	if len(cfg.Match) > 0 || outputHasProjection(cfg.Output) {
		if json.Unmarshal(ev.Data, &data) != nil {
			return nil, false
		}
	}
	for path, want := range cfg.Match {
		got, ok := lookupJSONPath(data, strings.Split(strings.TrimPrefix(path, "data."), "."))
		if !ok || !eventMatchConditionMatches(got, want) {
			return nil, false
		}
	}
	projected, ok := projectEventOutput(cfg.Output, data)
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(projected)
	if err != nil || len(encoded) > 8*1024 {
		return nil, false
	}
	return encoded, true
}

func eventMatchConditionMatches(got, condition any) bool {
	if isJSONScalar(condition) {
		return reflect.DeepEqual(got, condition)
	}
	operator, ok := condition.(map[string]any)
	if !ok {
		return false
	}
	values, ok := operator["in"].([]any)
	if !ok {
		return false
	}
	for _, want := range values {
		if reflect.DeepEqual(got, want) {
			return true
		}
	}
	return false
}

func outputHasProjection(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if outputHasProjection(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if outputHasProjection(child) {
				return true
			}
		}
	case string:
		_, ok := eventProjectionPath(typed)
		return ok
	}
	return false
}

func projectEventOutput(value, data any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			projected, ok := projectEventOutput(child, data)
			if !ok {
				return nil, false
			}
			out[key] = projected
		}
		return out, true
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			projected, ok := projectEventOutput(child, data)
			if !ok {
				return nil, false
			}
			out[i] = projected
		}
		return out, true
	case string:
		path, projected := eventProjectionPath(typed)
		if !projected {
			return typed, true
		}
		return lookupJSONPath(data, strings.Split(strings.TrimPrefix(path, "data."), "."))
	default:
		return value, true
	}
}

func lookupJSONPath(value any, path []string) (any, bool) {
	cur := value
	for _, part := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

type appEventHubManager struct {
	mu     sync.Mutex
	hubs   map[string]*appEventHub
	base   string
	token  string
	client *http.Client
	closed bool
}

type appEventHub struct {
	manager   *appEventHubManager
	key       string
	projectID string
	sourceApp string
	ctx       context.Context
	cancel    context.CancelFunc

	mu          sync.Mutex
	subscribers map[uint64]chan appBusEvent
	nextSubID   uint64
	ring        []appBusEvent
	lastSeq     uint64
	firstResult chan error
	firstOnce   sync.Once
}

func newAppEventHubManager(base, token string, client *http.Client) *appEventHubManager {
	return &appEventHubManager{
		hubs:   make(map[string]*appEventHub),
		base:   strings.TrimRight(base, "/"),
		token:  strings.TrimSpace(token),
		client: client,
	}
}

func (m *appEventHubManager) subscribe(ctx context.Context, projectID, sourceApp string, since uint64) (<-chan appBusEvent, []appBusEvent, func(), error) {
	if m == nil || m.base == "" || m.token == "" {
		return nil, nil, nil, errors.New("APTEVA_GATEWAY_URL/APTEVA_OUTBOUND_TOKEN required for app_events targets")
	}
	key := projectID + "\x00" + sourceApp
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, nil, errors.New("app event hub is closed")
	}
	h := m.hubs[key]
	created := false
	if h == nil {
		hubCtx, cancel := context.WithCancel(context.Background())
		h = &appEventHub{
			manager: m, key: key, projectID: projectID, sourceApp: sourceApp,
			ctx: hubCtx, cancel: cancel, subscribers: make(map[uint64]chan appBusEvent),
			firstResult: make(chan error, 1), lastSeq: since,
		}
		m.hubs[key] = h
		created = true
	}
	h.mu.Lock()
	h.nextSubID++
	subID := h.nextSubID
	ch := make(chan appBusEvent, 64)
	h.subscribers[subID] = ch
	replay := make([]appBusEvent, 0)
	if since > 0 {
		for _, ev := range h.ring {
			if ev.Seq > since {
				replay = append(replay, ev)
			}
		}
	}
	h.mu.Unlock()
	m.mu.Unlock()

	if created {
		go h.run()
		select {
		case err := <-h.firstResult:
			if err != nil {
				h.unsubscribe(subID)
				return nil, nil, nil, err
			}
		case <-ctx.Done():
			h.unsubscribe(subID)
			return nil, nil, nil, ctx.Err()
		}
	}
	var once sync.Once
	return ch, replay, func() { once.Do(func() { h.unsubscribe(subID) }) }, nil
}

func (m *appEventHubManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	hubs := make([]*appEventHub, 0, len(m.hubs))
	for _, h := range m.hubs {
		hubs = append(hubs, h)
	}
	m.hubs = make(map[string]*appEventHub)
	m.mu.Unlock()
	for _, h := range hubs {
		h.shutdown()
	}
}

func (h *appEventHub) shutdown() {
	h.cancel()
	h.mu.Lock()
	for id, ch := range h.subscribers {
		delete(h.subscribers, id)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *appEventHub) unsubscribe(id uint64) {
	h.mu.Lock()
	ch, ok := h.subscribers[id]
	if ok {
		delete(h.subscribers, id)
		close(ch)
	}
	empty := len(h.subscribers) == 0
	h.mu.Unlock()
	if !empty {
		return
	}
	h.manager.mu.Lock()
	if h.manager.hubs[h.key] == h {
		delete(h.manager.hubs, h.key)
		h.cancel()
	}
	h.manager.mu.Unlock()
}

func (h *appEventHub) run() {
	first := true
	for {
		resp, err := h.openStream()
		if first {
			h.firstOnce.Do(func() { h.firstResult <- err })
			first = false
		}
		if err == nil {
			err = h.readStream(resp.Body)
			_ = resp.Body.Close()
		}
		if h.ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-h.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (h *appEventHub) openStream() (*http.Response, error) {
	h.mu.Lock()
	since := h.lastSeq
	h.mu.Unlock()
	values := url.Values{"project_id": []string{h.projectID}}
	if since > 0 {
		values.Set("since", strconv.FormatUint(since, 10))
	}
	target := h.manager.base + "/api/app-events/" + url.PathEscape(h.sourceApp) + "?" + values.Encode()
	req, err := http.NewRequestWithContext(h.ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.manager.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.manager.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("AppBus subscription rejected: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func (h *appEventHub) readStream(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				var ev appBusEvent
				if json.Unmarshal([]byte(data.String()), &ev) == nil && ev.App == h.sourceApp && ev.ProjectID == h.projectID {
					h.broadcast(ev)
				}
				data.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return scanner.Err()
}

func (h *appEventHub) broadcast(ev appBusEvent) {
	h.mu.Lock()
	if ev.Seq <= h.lastSeq {
		h.mu.Unlock()
		return
	}
	h.lastSeq = ev.Seq
	h.ring = append(h.ring, ev)
	if len(h.ring) > eventReplayCapacity {
		h.ring = append([]appBusEvent(nil), h.ring[len(h.ring)-eventReplayCapacity:]...)
	}
	for _, ch := range h.subscribers {
		select {
		case ch <- ev:
		default:
			// Invalidations are level-triggered. A slow consumer will still
			// receive a later event or reconnect and perform its initial reload.
		}
	}
	h.mu.Unlock()
}

func (a *App) dispatchAppEvents(w http.ResponseWriter, r *http.Request, api *API, route *APIRoute) (int, error) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "app_events routes require GET")
		return http.StatusMethodNotAllowed, errors.New("app_events routes require GET")
	}
	cfg, err := parseAppEventsConfig(route.EventsJSON)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, http.StatusInternalServerError, "streaming unsupported")
		return http.StatusInternalServerError, errors.New("streaming unsupported")
	}
	since := parseEventCursor(r)
	ch, replay, unsubscribe, err := a.eventHubs.subscribe(r.Context(), api.ProjectID, route.TargetRef, since)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: ready\ndata: {\"type\":\"ready\",\"revalidate\":true}\n\n")
	flusher.Flush()

	eventName := "invalidate"
	if name, _ := cfg.Output["type"].(string); validSSEEventName(name) {
		eventName = name
	}
	coalesce := time.Duration(cfg.CoalesceMS) * time.Millisecond
	type pendingProjection struct {
		output []byte
		seq    uint64
	}
	pending := make(map[string]pendingProjection)
	var timer *time.Timer
	var timerC <-chan time.Time
	flushPending := func() error {
		batch := make([]pendingProjection, 0, len(pending))
		for _, item := range pending {
			batch = append(batch, item)
		}
		sort.Slice(batch, func(i, j int) bool {
			if batch[i].seq == batch[j].seq {
				return string(batch[i].output) < string(batch[j].output)
			}
			return batch[i].seq < batch[j].seq
		})
		for _, item := range batch {
			if err := writeProjectedEvent(w, flusher, eventName, item.output, item.seq); err != nil {
				return err
			}
		}
		clear(pending)
		return nil
	}
	queue := func(ev appBusEvent) error {
		output, matches := projectAppEvent(cfg, ev)
		if !matches {
			return nil
		}
		if coalesce == 0 {
			return writeProjectedEvent(w, flusher, eventName, output, ev.Seq)
		}
		key := string(output)
		if current, ok := pending[key]; !ok || ev.Seq > current.seq {
			pending[key] = pendingProjection{output: output, seq: ev.Seq}
		}
		if len(pending) >= maxPendingProjections {
			if timer != nil {
				timer.Stop()
				timer = nil
				timerC = nil
			}
			return flushPending()
		}
		if timer == nil {
			timer = time.NewTimer(coalesce)
			timerC = timer.C
		}
		return nil
	}
	for _, ev := range replay {
		if err := queue(ev); err != nil {
			return http.StatusOK, err
		}
	}
	heartbeat := time.NewTicker(eventHeartbeat)
	defer heartbeat.Stop()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-r.Context().Done():
			return http.StatusOK, nil
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return http.StatusOK, err
			}
			flusher.Flush()
		case <-timerC:
			timer = nil
			timerC = nil
			if err := flushPending(); err != nil {
				return http.StatusOK, err
			}
		case ev, ok := <-ch:
			if !ok {
				return http.StatusOK, nil
			}
			if err := queue(ev); err != nil {
				return http.StatusOK, err
			}
		}
	}
}

func parseEventCursor(r *http.Request) uint64 {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	cursor, _ := strconv.ParseUint(value, 10, 64)
	return cursor
}

func validSSEEventName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func writeProjectedEvent(w io.Writer, flusher http.Flusher, eventName string, output []byte, seq uint64) error {
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, eventName, output); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
