package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// eventTrigger manages SSE subscriptions to other apps' event
// streams and dispatches matching workflow runs. One SSE connection
// per (source_app, project_id) lane shared by all workflows that
// listen on it; reconnect-with-since handles transient drops.
//
// Deliberately lighter than the platform's AppEventDispatcher
// (which is agent-shaped, persists per-row last_seq, etc.). Here
// the source of truth is the workflows table itself (trigger_kind=
// event), the lane set rebuilds from a single SELECT on reconcile,
// and the cursor is in-memory — a sidecar restart re-subscribes
// fresh and the bus's ring-buffer + since=<seq> recovery covers
// most gaps. Anything older than the bus ring loses one delivery,
// which is acceptable for a v0.3 cut.

const (
	// eventReconnectDelay backs off briefly after a stream drops
	// before the lane re-subscribes with since=<lastSeq>.
	eventReconnectDelay = 2 * time.Second
	// eventReconcilePeriod is a safety-net rescan. Reconcile is
	// otherwise called explicitly from workflow CRUD via Kick().
	eventReconcilePeriod = 60 * time.Second
)

type eventTrigger struct {
	ctx            *sdk.AppCtx
	gatewayURL     string
	token          string
	client         *http.Client
	reconnectDelay time.Duration
	runSlots       chan struct{}

	mu                 sync.Mutex
	lanes              map[laneKey]*eventLane
	started            bool
	configured         bool
	missingConfig      []string
	lastReconcileAt    string
	lastReconcileError string

	stop     chan struct{}
	done     chan struct{}
	kick     chan struct{}
	stopOnce sync.Once
	laneWG   sync.WaitGroup
}

type laneKey struct {
	source    string
	projectID string
}

// eventLane is one SSE subscription + the workflows interested in
// it. workflows is the snapshot from the latest reconcile;
// reconcile rewrites it under ln.mu without disturbing the running
// stream.
type eventLane struct {
	key    laneKey
	cancel context.CancelFunc

	mu             sync.Mutex
	workflows      []*Workflow
	lastSeq        uint64
	state          string
	connectedAt    string
	lastEventAt    string
	lastError      string
	lastErrorAt    string
	reconnectCount int
}

func newEventTrigger(ctx *sdk.AppCtx) *eventTrigger {
	gatewayURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("APTEVA_GATEWAY_URL")), "/")
	token := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	missing := make([]string, 0, 2)
	if gatewayURL == "" {
		missing = append(missing, "APTEVA_GATEWAY_URL")
	}
	if token == "" {
		missing = append(missing, "APTEVA_APP_TOKEN")
	}
	return &eventTrigger{
		ctx:        ctx,
		gatewayURL: gatewayURL,
		token:      token,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 15 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		}, // no whole-request timeout — a healthy SSE stream stays open
		reconnectDelay: eventReconnectDelay,
		runSlots:       make(chan struct{}, 32),
		lanes:          map[laneKey]*eventLane{},
		configured:     len(missing) == 0,
		missingConfig:  missing,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		kick:           make(chan struct{}, 1),
	}
}

// Start spawns the reconcile loop. Safe to call when there's nothing
// to subscribe to; it'll come back on the next CRUD-triggered Kick.
func (m *eventTrigger) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	go func() {
		defer close(m.done)
		m.reconcileLoop()
	}()
	m.Kick()
}

// Stop tears down every lane and ends the reconcile loop.
func (m *eventTrigger) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if started {
		<-m.done
	} else {
		m.shutdownAll()
	}
	m.laneWG.Wait()
}

// Kick requests an out-of-band reconcile. Called from workflow CRUD
// so an added/removed event-trigger goes live without waiting for
// the periodic rescan.
func (m *eventTrigger) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *eventTrigger) reconcileLoop() {
	t := time.NewTicker(eventReconcilePeriod)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			m.shutdownAll()
			return
		case <-m.kick:
			m.reconcile()
		case <-t.C:
			m.reconcile()
		}
	}
}

// reconcile rebuilds the desired (source, project) lane set from the
// workflows table and brings the in-memory map into line: starts new
// lanes, stops orphan ones, refreshes the workflow snapshot on lanes
// that already exist.
func (m *eventTrigger) reconcile() {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	m.mu.Lock()
	m.lastReconcileAt = now
	if !m.configured {
		m.lastReconcileError = "missing required subscriber configuration: " + strings.Join(m.missingConfig, ", ")
		errText := m.lastReconcileError
		m.mu.Unlock()
		log.Printf("[WF-EVENT] degraded: %s", errText)
		return
	}
	m.mu.Unlock()

	workflows, err := dbListEventTriggeredWorkflowsAll(m.ctx.AppDB())
	if err != nil {
		m.mu.Lock()
		m.lastReconcileError = err.Error()
		m.mu.Unlock()
		log.Printf("[WF-EVENT] reconcile list failed: %v", err)
		return
	}

	desired := map[laneKey][]*Workflow{}
	for _, wf := range workflows {
		trig, ok := workflowTrigger(wf)
		if !ok || trig.Kind != "event" || trig.Source == "" {
			continue
		}
		k := laneKey{source: trig.Source, projectID: wf.ProjectID}
		desired[k] = append(desired[k], wf)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastReconcileError = ""

	// Drop lanes that no longer have any workflows.
	for k, ln := range m.lanes {
		if _, want := desired[k]; !want {
			ln.cancel()
			delete(m.lanes, k)
			log.Printf("[WF-EVENT] lane stopped source=%s project=%s", k.source, k.projectID)
		}
	}

	// Refresh existing + start new.
	for k, wfs := range desired {
		if existing, ok := m.lanes[k]; ok {
			existing.mu.Lock()
			existing.workflows = wfs
			existing.mu.Unlock()
			continue
		}
		ln := &eventLane{key: k, workflows: wfs, state: "connecting"}
		ctx, cancel := context.WithCancel(context.Background())
		ln.cancel = cancel
		m.lanes[k] = ln
		m.laneWG.Add(1)
		go func() {
			defer m.laneWG.Done()
			m.runLane(ctx, ln)
		}()
		log.Printf("[WF-EVENT] lane started source=%s project=%s workflows=%d",
			k.source, k.projectID, len(wfs))
	}
}

func (m *eventTrigger) shutdownAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, ln := range m.lanes {
		ln.cancel()
		delete(m.lanes, k)
	}
}

// runLane is the per-lane goroutine: open the SSE stream, parse +
// dispatch events, reconnect on every disconnect until cancelled.
func (m *eventTrigger) runLane(ctx context.Context, ln *eventLane) {
	for {
		if ctx.Err() != nil {
			m.setLaneState(ln, "stopped", nil)
			return
		}
		err := m.streamLane(ctx, ln)
		if ctx.Err() != nil {
			m.setLaneState(ln, "stopped", nil)
			return
		}
		if err != nil {
			m.setLaneState(ln, "reconnecting", err)
			log.Printf("[WF-EVENT] lane source=%s project=%s stream err: %v",
				ln.key.source, ln.key.projectID, err)
		}
		delay := m.reconnectDelay
		if delay <= 0 {
			delay = eventReconnectDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			m.setLaneState(ln, "stopped", nil)
			return
		case <-timer.C:
		}
	}
}

// streamLane opens one SSE connection and reads it until EOF or
// error. Each completed event frame is dispatched to dispatchFrame.
func (m *eventTrigger) streamLane(ctx context.Context, ln *eventLane) error {
	ln.mu.Lock()
	since := ln.lastSeq
	ln.mu.Unlock()

	u := fmt.Sprintf("%s/api/app-events/%s?project_id=%s",
		m.gatewayURL,
		url.PathEscape(ln.key.source),
		url.QueryEscape(ln.key.projectID),
	)
	if since > 0 {
		u += fmt.Sprintf("&since=%d", since)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, detail)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "text/event-stream") {
		return fmt.Errorf("unexpected content type %q", ct)
	}
	m.setLaneState(ln, "connected", nil)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 8<<20) // match server's frame cap
	var dataBuf, idBuf string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if dataBuf != "" {
				m.dispatchFrame(ln, idBuf, dataBuf)
			}
			dataBuf, idBuf = "", ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment / keepalive — ignore.
			continue
		}
		switch {
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(payload, " ") {
				payload = payload[1:]
			}
			if dataBuf != "" {
				dataBuf += "\n"
			}
			dataBuf += payload
		case strings.HasPrefix(line, "id:"):
			idBuf = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

// dispatchFrame parses one event and dispatches RunWorkflow on each
// matching workflow.
func (m *eventTrigger) dispatchFrame(ln *eventLane, idStr, dataStr string) {
	var ev struct {
		Topic     string          `json:"topic"`
		App       string          `json:"app"`
		ProjectID string          `json:"project_id"`
		Seq       uint64          `json:"seq"`
		Time      string          `json:"time"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(dataStr), &ev); err != nil {
		m.setLaneState(ln, "connected", fmt.Errorf("invalid event JSON: %w", err))
		return
	}
	if ev.Seq == 0 && idStr != "" {
		ev.Seq, _ = strconv.ParseUint(idStr, 10, 64)
	}
	if ev.App != "" && ev.App != ln.key.source {
		m.setLaneState(ln, "connected", fmt.Errorf("event source %q does not match lane %q", ev.App, ln.key.source))
		return
	}
	if ev.ProjectID != "" && ev.ProjectID != ln.key.projectID {
		m.setLaneState(ln, "connected", fmt.Errorf("event project %q does not match lane %q", ev.ProjectID, ln.key.projectID))
		return
	}

	ln.mu.Lock()
	if ev.Seq > 0 && ev.Seq <= ln.lastSeq {
		ln.mu.Unlock()
		return
	}
	if ev.Seq > ln.lastSeq {
		ln.lastSeq = ev.Seq
	}
	ln.lastEventAt = time.Now().UTC().Format(time.RFC3339Nano)
	workflows := append([]*Workflow{}, ln.workflows...)
	ln.mu.Unlock()

	var data any
	if len(ev.Data) > 0 {
		_ = json.Unmarshal(ev.Data, &data)
	}
	input := map[string]any{
		"topic":      ev.Topic,
		"source":     ev.App,
		"project_id": ev.ProjectID,
		"time":       ev.Time,
		"data":       data,
	}

	for _, wf := range workflows {
		trig, ok := workflowTrigger(wf)
		if !ok {
			continue
		}
		if !topicMatches(trig.Topic, ev.Topic) {
			continue
		}
		m.runMatched(wf, input)
	}
}

// runMatched fires off RunWorkflow in a goroutine so the read loop
// stays free for the next event. The workflow's project pins the
// run context; the trigger kind is recorded as "event" in the runs
// table.
func (m *eventTrigger) runMatched(wf *Workflow, input map[string]any) {
	runCtx := m.ctx
	if wf.ProjectID != "" {
		runCtx = m.ctx.WithProject(wf.ProjectID)
	}
	select {
	case m.runSlots <- struct{}{}:
	case <-m.stop:
		return
	}
	go func() {
		defer func() { <-m.runSlots }()
		if _, err := RunWorkflow(context.Background(), runCtx, wf.ProjectID, wf, input,
			runOptions{triggerKind: "event"}); err != nil {
			log.Printf("[WF-EVENT] run workflow=%s err: %v", wf.Name, err)
		}
	}()
}

func (m *eventTrigger) setLaneState(ln *eventLane, state string, err error) {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	ln.state = state
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if state == "connected" && ln.connectedAt == "" {
		ln.connectedAt = now
	}
	if err != nil {
		ln.lastError = err.Error()
		ln.lastErrorAt = now
		if state == "reconnecting" {
			ln.reconnectCount++
		}
	}
}

type eventSubscriberLaneStatus struct {
	Source         string `json:"source"`
	ProjectID      string `json:"project_id"`
	WorkflowCount  int    `json:"workflow_count"`
	State          string `json:"state"`
	ConnectedAt    string `json:"connected_at,omitempty"`
	LastEventAt    string `json:"last_event_at,omitempty"`
	LastEventSeq   uint64 `json:"last_event_seq,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	LastErrorAt    string `json:"last_error_at,omitempty"`
	ReconnectCount int    `json:"reconnect_count"`
}

type eventSubscriberStatus struct {
	Healthy            bool                        `json:"healthy"`
	State              string                      `json:"state"`
	Configured         bool                        `json:"configured"`
	MissingConfig      []string                    `json:"missing_configuration,omitempty"`
	LastReconcileAt    string                      `json:"last_reconcile_at,omitempty"`
	LastReconcileError string                      `json:"last_reconcile_error,omitempty"`
	LaneCount          int                         `json:"lane_count"`
	WorkflowCount      int                         `json:"workflow_count"`
	Lanes              []eventSubscriberLaneStatus `json:"lanes"`
}

func (m *eventTrigger) Status() eventSubscriberStatus {
	m.mu.Lock()
	status := eventSubscriberStatus{
		Configured:         m.configured,
		MissingConfig:      append([]string(nil), m.missingConfig...),
		LastReconcileAt:    m.lastReconcileAt,
		LastReconcileError: m.lastReconcileError,
		LaneCount:          len(m.lanes),
		Lanes:              make([]eventSubscriberLaneStatus, 0, len(m.lanes)),
	}
	lanes := make([]*eventLane, 0, len(m.lanes))
	for _, ln := range m.lanes {
		lanes = append(lanes, ln)
	}
	m.mu.Unlock()

	allConnected := true
	for _, ln := range lanes {
		ln.mu.Lock()
		laneStatus := eventSubscriberLaneStatus{
			Source:         ln.key.source,
			ProjectID:      ln.key.projectID,
			WorkflowCount:  len(ln.workflows),
			State:          ln.state,
			ConnectedAt:    ln.connectedAt,
			LastEventAt:    ln.lastEventAt,
			LastEventSeq:   ln.lastSeq,
			LastError:      ln.lastError,
			LastErrorAt:    ln.lastErrorAt,
			ReconnectCount: ln.reconnectCount,
		}
		ln.mu.Unlock()
		status.WorkflowCount += laneStatus.WorkflowCount
		if laneStatus.State != "connected" {
			allConnected = false
		}
		status.Lanes = append(status.Lanes, laneStatus)
	}

	switch {
	case !status.Configured || status.LastReconcileError != "":
		status.State = "degraded"
	case status.LaneCount == 0:
		status.State = "idle"
		status.Healthy = true
	case allConnected:
		status.State = "connected"
		status.Healthy = true
	default:
		status.State = "reconnecting"
	}
	return status
}

func (a *App) handleSubscriberStatus(w http.ResponseWriter, _ *http.Request) {
	if globalEventTrigger == nil {
		httpJSON(w, eventSubscriberStatus{
			Healthy: false,
			State:   "stopped",
			Lanes:   []eventSubscriberLaneStatus{},
		})
		return
	}
	httpJSON(w, globalEventTrigger.Status())
}

func (a *App) handleSubscriberHealth(w http.ResponseWriter, _ *http.Request) {
	status := eventSubscriberStatus{
		Healthy: false,
		State:   "starting",
		Lanes:   []eventSubscriberLaneStatus{},
	}
	if globalEventTrigger != nil {
		status = globalEventTrigger.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	if !status.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(status)
}

// topicMatches: exact match, "*" wildcard for "every topic", or
// "<prefix>.*" for one-level wildcard (e.g. "row.*" matches
// "row.inserted" and "row.deleted"). Anything richer is the
// workflow author's job inside a branch step.
func topicMatches(pattern, topic string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == topic {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// kickEventTrigger is a nil-safe shorthand for the CRUD paths. The
// manager itself is created in OnMount; calls before / outside of
// that (e.g. unit tests of the store layer) silently no-op.
func kickEventTrigger() {
	if globalEventTrigger != nil {
		globalEventTrigger.Kick()
	}
}

// workflowTrigger parses the trigger out of a workflow. Prefers the
// denormalised TriggerJSON column (cheap), falls back to parsing
// Source (covers rows created before the denormalisation landed).
func workflowTrigger(wf *Workflow) (TriggerDef, bool) {
	if strings.TrimSpace(wf.TriggerJSON) != "" {
		var t TriggerDef
		if err := json.Unmarshal([]byte(wf.TriggerJSON), &t); err == nil {
			return t, true
		}
	}
	if wf.Source != "" {
		if def, err := ParseDefinition([]byte(wf.Source)); err == nil {
			return def.Trigger, true
		}
	}
	return TriggerDef{}, false
}
