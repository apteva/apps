package sdk

// HTTP-based Emitter — POSTs to apteva-server's
// /api/app-events/internal/emit using the sidecar's APTEVA_APP_TOKEN.
//
// Fire-and-forget by design. The platform's bus is in-memory only;
// a missed event is recovered by the dashboard reconnecting with
// since=<lastSeq>, and the app's own DB is the durable source. So
// if the publish fails (network blip, server restarting), losing
// the UI fanout is the right trade vs. blocking the caller.
//
// Each call runs in its own goroutine with a hard 100ms timeout so
// pathological cases (server hung, DNS slow) never stretch a tool-
// call response.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const emitTimeout = 100 * time.Millisecond

type httpEmitter struct {
	gatewayURL string
	token      string
	logger     Logger
	client     *http.Client
}

func newHTTPEmitter(gatewayURL, token string, logger Logger) *httpEmitter {
	if logger == nil {
		logger = silentLogger{}
	}
	gatewayURL = strings.TrimSuffix(gatewayURL, "/")
	return &httpEmitter{
		gatewayURL: gatewayURL,
		token:      token,
		logger:     logger,
		client: &http.Client{
			Timeout: emitTimeout,
		},
	}
}

// EmitWithProject sends an event whose project_id is set explicitly
// on the wire. The server validates it against the install's scope
// (project-scoped install → overridden to pinned project; global
// install → validated against the install's owner). Passing "" emits
// a wildcard-only event.
func (e *httpEmitter) EmitWithProject(topic, projectID string, data any) {
	// Skip silently when not configured — tests, manifests with no
	// platform, dev runs against a stubbed harness.
	if e == nil || e.gatewayURL == "" || e.token == "" {
		return
	}
	if strings.TrimSpace(topic) == "" {
		return
	}
	go e.send(topic, projectID, data)
}

func (e *httpEmitter) send(topic, projectID string, data any) {
	body := struct {
		Topic     string `json:"topic"`
		ProjectID string `json:"project_id,omitempty"`
		Data      any    `json:"data,omitempty"`
	}{Topic: topic, ProjectID: projectID, Data: data}
	buf, err := json.Marshal(body)
	if err != nil {
		e.logger.Warn("emit: marshal failed", "topic", topic, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), emitTimeout)
	defer cancel()
	url := e.gatewayURL + "/api/app-events/internal/emit"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	if err != nil {
		e.logger.Warn("emit: build request failed", "topic", topic, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := e.client.Do(req)
	if err != nil {
		// Logged at debug-ish level so a flaky platform doesn't spam
		// the sidecar's stderr; the dashboard recovers via reconnect.
		e.logger.Warn("emit: post failed", "topic", topic, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.logger.Warn("emit: non-2xx", "topic", topic, "status", resp.StatusCode)
	}
}

// EmitWithProjectAck waits for the platform to accept an event. Durable app
// outboxes should retry errors with a stable event_id in the payload.
func (e *httpEmitter) EmitWithProjectAck(ctx context.Context, topic, projectID string, data any) error {
	if e == nil || e.gatewayURL == "" || e.token == "" {
		return fmt.Errorf("event gateway not configured")
	}
	body, err := json.Marshal(map[string]any{"topic": topic, "project_id": projectID, "data": data})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", e.gatewayURL+"/api/app-events/internal/emit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("event gateway HTTP %d", resp.StatusCode)
	}
	return nil
}
