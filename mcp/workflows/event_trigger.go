package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
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
	ctx        *sdk.AppCtx
	gatewayURL string
	token      string
	client     *http.Client

	mu    sync.Mutex
	lanes map[laneKey]*eventLane

	stop chan struct{}
	kick chan struct{}
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

	mu        sync.Mutex
	workflows []*Workflow
	lastSeq   uint64
}

func newEventTrigger(ctx *sdk.AppCtx) *eventTrigger {
	return &eventTrigger{
		ctx:        ctx,
		gatewayURL: strings.TrimSuffix(os.Getenv("APTEVA_GATEWAY_URL"), "/"),
		token:      os.Getenv("APTEVA_APP_TOKEN"),
		client:     &http.Client{}, // no timeout — SSE stays open
		lanes:      map[laneKey]*eventLane{},
		stop:       make(chan struct{}),
		kick:       make(chan struct{}, 1),
	}
}

// Start spawns the reconcile loop. Safe to call when there's nothing
// to subscribe to; it'll come back on the next CRUD-triggered Kick.
func (m *eventTrigger) Start() {
	go m.reconcileLoop()
	m.Kick()
}

// Stop tears down every lane and ends the reconcile loop.
func (m *eventTrigger) Stop() {
	select {
	case <-m.stop:
		return // already stopped
	default:
	}
	close(m.stop)
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
	if m.gatewayURL == "" || m.token == "" {
		// Not configured — tests / dev runs that don't talk to a real
		// gateway. No-op; come back on the next Kick or tick.
		return
	}

	workflows, err := dbListEventTriggeredWorkflowsAll(m.ctx.AppDB())
	if err != nil {
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
		ln := &eventLane{key: k, workflows: wfs}
		ctx, cancel := context.WithCancel(context.Background())
		ln.cancel = cancel
		m.lanes[k] = ln
		go m.runLane(ctx, ln)
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
			return
		}
		err := m.streamLane(ctx, ln)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("[WF-EVENT] lane source=%s project=%s stream err: %v",
				ln.key.source, ln.key.projectID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(eventReconnectDelay):
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
		return fmt.Errorf("status %d", resp.StatusCode)
	}

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
			dataBuf = payload
		case strings.HasPrefix(line, "id:"):
			idBuf = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
	}
	return scanner.Err()
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
		return
	}

	ln.mu.Lock()
	if ev.Seq > ln.lastSeq {
		ln.lastSeq = ev.Seq
	}
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
	go func() {
		if _, err := RunWorkflow(context.Background(), runCtx, wf.ProjectID, wf, input,
			runOptions{triggerKind: "event"}); err != nil {
			log.Printf("[WF-EVENT] run workflow=%s err: %v", wf.Name, err)
		}
	}()
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
