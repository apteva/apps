package main

// Backends own their transactions and projection semantics. The runtime only
// carries opaque bounded handles and whitelisted, domain-independent metadata.
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type upstreamCapabilities struct{ KeyBatching, BatchSnapshot, RequestSnapshot bool }
type readSnapshot struct {
	Handle    string    `json:"handle"`
	ExpiresAt time.Time `json:"expires_at"`
}
type upstreamRead struct {
	ID        string         `json:"id"`
	Operation string         `json:"operation"`
	Arguments map[string]any `json:"arguments"`
	Parent    any            `json:"parent,omitempty"`
}
type upstreamResult struct {
	Value    any              `json:"value"`
	Metadata []map[string]any `json:"metadata,omitempty"`
	Error    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
type upstreamAdapter interface {
	Capabilities() upstreamCapabilities
	Open(context.Context, time.Time) (*readSnapshot, error)
	Read(context.Context, *readSnapshot, []upstreamRead) (map[string]upstreamResult, error)
	Close(context.Context, *readSnapshot) error
}
type appUpstream struct {
	app     *App
	project string
	source  sourceRecord
}

func (u *appUpstream) Capabilities() upstreamCapabilities {
	return upstreamCapabilities{KeyBatching: stringValue(u.source.Config["batch_tool"], "") != "", RequestSnapshot: stringValue(u.source.Config["snapshot_open_tool"], "") != ""}
}
func (u *appUpstream) input(ctx context.Context) map[string]any {
	out := map[string]any{"_project_id": u.project, "parameters": u.source.Config["parameters"]}
	if deadline, ok := ctx.Deadline(); ok {
		out["deadline"] = deadline.UTC().Format(time.RFC3339Nano)
	}
	if i := securityIdentity(ctx); i != nil {
		out["principal"] = map[string]any{"subject": i.Subject, "issuer": i.Issuer, "tenant_id": i.Tenant, "project_id": i.Project, "api": i.API, "claims": i.Claims, "permissions": i.Permissions, "authorization_version": i.AuthorizationVersion}
	}
	return out
}
func (u *appUpstream) invoke(ctx context.Context, tool string, input map[string]any, out any) error {
	err := sdk.CallAppResultContext(ctx, u.app.ctx.WithProject(u.project).PlatformAPI(), stringValue(u.source.Config["app"], ""), tool, input, out)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return runtimeError("upstream_call_failed", "upstream adapter call failed")
	}
	return nil
}
func (u *appUpstream) Open(ctx context.Context, deadline time.Time) (*readSnapshot, error) {
	if !u.Capabilities().RequestSnapshot {
		return nil, runtimeError("snapshot_unsupported", "upstream does not support reusable read snapshots")
	}
	input := u.input(ctx)
	input["expires_at"] = deadline.UTC().Format(time.RFC3339Nano)
	var snapshot readSnapshot
	if err := u.invoke(ctx, stringValue(u.source.Config["snapshot_open_tool"], ""), input, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Handle == "" || len(snapshot.Handle) > 4096 || !snapshot.ExpiresAt.After(time.Now()) || snapshot.ExpiresAt.After(deadline) {
		if snapshot.Handle != "" {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = u.Close(cleanup, &snapshot)
			cancel()
		}
		return nil, runtimeError("snapshot_invalid", "upstream returned an invalid or unbounded snapshot")
	}
	return &snapshot, nil
}
func (u *appUpstream) Read(ctx context.Context, snapshot *readSnapshot, reads []upstreamRead) (map[string]upstreamResult, error) {
	if snapshot != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, snapshot.ExpiresAt)
		defer cancel()
		if ctx.Err() != nil {
			return nil, runtimeError("snapshot_expired", "upstream read snapshot expired")
		}
	}
	input := u.input(ctx)
	if snapshot != nil {
		input["snapshot_handle"] = snapshot.Handle
	}
	results := map[string]upstreamResult{}
	if len(reads) > 1 && u.Capabilities().KeyBatching {
		input["reads"] = reads
		var out struct {
			Results map[string]upstreamResult `json:"results"`
		}
		if err := u.invoke(ctx, stringValue(u.source.Config["batch_tool"], ""), input, &out); err != nil {
			return nil, err
		}
		results = out.Results
	} else {
		for _, read := range reads {
			input["operation"], input["arguments"], input["parent"] = read.Operation, read.Arguments, read.Parent
			var out upstreamResult
			if err := u.invoke(ctx, stringValue(u.source.Config["read_tool"], ""), input, &out); err != nil {
				return nil, err
			}
			results[read.ID] = out
		}
	}
	return results, nil
}
func (u *appUpstream) Close(ctx context.Context, snapshot *readSnapshot) error {
	if snapshot == nil {
		return nil
	}
	input := u.input(ctx)
	input["snapshot_handle"] = snapshot.Handle
	var out any
	return u.invoke(ctx, stringValue(u.source.Config["snapshot_close_tool"], ""), input, &out)
}
func validateUpstreamSource(config map[string]any) error {
	for _, key := range []string{"app", "read_tool"} {
		value, ok := config[key].(string)
		if !ok || !identityText(value) || strings.ContainsAny(value, "/ ") {
			return invalid("upstream source requires %s", key)
		}
	}
	for _, key := range []string{"batch_tool", "snapshot_open_tool", "snapshot_close_tool"} {
		if value, ok := config[key]; ok {
			s, ok := value.(string)
			if !ok || !identityText(s) || strings.ContainsAny(s, "/ ") {
				return invalid("invalid upstream %s", key)
			}
		}
	}
	if (config["snapshot_open_tool"] != nil) != (config["snapshot_close_tool"] != nil) {
		return invalid("upstream snapshot open and close tools must be configured together")
	}
	if config["read_only"] != true {
		return invalid("upstream adapters require read_only: true")
	}
	return nil
}

type requestReadContext struct {
	adapter  upstreamAdapter
	snapshot *readSnapshot
}
type requestReads struct {
	mu       sync.Mutex
	contexts map[int64]*requestReadContext
	closed   bool
}

func (r *requestReads) get(ctx context.Context, source sourceRecord, adapter upstreamAdapter, limits releaseLimits) (*requestReadContext, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, runtimeError("execution_cancelled", "request read context is closed")
	}
	if c := r.contexts[source.ID]; c != nil {
		return c, nil
	}
	c := &requestReadContext{adapter: adapter}
	if limits.ReadConsistency == "request" {
		if !adapter.Capabilities().RequestSnapshot {
			return nil, runtimeError("snapshot_unsupported", fmt.Sprintf("source %s does not support request snapshots", source.Name))
		}
		deadline := time.Now().Add(time.Duration(limits.MaxSnapshotMS) * time.Millisecond)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		var err error
		c.snapshot, err = adapter.Open(ctx, deadline)
		if err != nil {
			return nil, err
		}
	}
	if r.contexts == nil {
		r.contexts = map[int64]*requestReadContext{}
	}
	r.contexts[source.ID] = c
	return c, nil
}
func (r *requestReads) close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	contexts := r.contexts
	r.mu.Unlock()
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var first error
	for _, c := range contexts {
		if c.snapshot != nil {
			if err := c.adapter.Close(cleanup, c.snapshot); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// A native Tables batch can bind several reads to one SQLite snapshot. It does
// not advertise request snapshots across multiple resolver levels/chunks.
type tablesUpstream struct {
	app         *App
	project     string
	consistency string
}

func (t *tablesUpstream) Capabilities() upstreamCapabilities {
	return upstreamCapabilities{KeyBatching: true, BatchSnapshot: true}
}
func (t *tablesUpstream) Open(context.Context, time.Time) (*readSnapshot, error) {
	return nil, runtimeError("snapshot_unsupported", "Tables supports batch snapshots, not reusable request snapshots")
}
func (t *tablesUpstream) Close(context.Context, *readSnapshot) error { return nil }
func (t *tablesUpstream) Read(ctx context.Context, _ *readSnapshot, reads []upstreamRead) (map[string]upstreamResult, error) {
	ops := make([]map[string]any, len(reads))
	for i, read := range reads {
		ops[i] = map[string]any{"id": read.ID, "operation": read.Operation, "args": read.Arguments}
	}
	mode := "best_effort"
	if t.consistency == "batch" {
		mode = "read_snapshot"
	}
	var out tablesBatchResult
	if err := sdk.CallAppResultContext(ctx, t.app.ctx.WithProject(t.project).PlatformAPI(), "tables", "tables_batch", map[string]any{"operations": ops, "mode": mode, "_project_id": t.project}, &out); err != nil {
		return nil, err
	}
	results := map[string]upstreamResult{}
	for _, read := range reads {
		entry, ok := out.Results[read.ID]
		if !ok {
			return nil, internal("Tables batch omitted a read")
		}
		result := upstreamResult{Value: entry.Result}
		if entry.Status != "ok" {
			return nil, internal("Tables snapshot batch failed")
		}
		if m, ok := entry.Result.(map[string]any); ok {
			result.Metadata = projectionMetadata(m["projections"])
		}
		results[read.ID] = result
	}
	return results, nil
}

// Metadata is deliberately whitelisted. Opaque handles, arguments, row contents,
// private backend fields and caller identity never enter logs/extensions.
var metadataFields = map[string]bool{"name": true, "version": true, "generation": true, "published_generation": true, "last_full_generation": true, "ready": true, "built": true, "stale": true, "is_current": true, "status": true, "coverage_from": true, "coverage_to": true, "published_change_id": true, "consumed_change_id": true, "latest_relevant_change": true, "lag": true, "last_successful_publication_at": true}

func projectionMetadata(raw any) []map[string]any {
	bytes, err := json.Marshal(raw)
	if err != nil || len(bytes) > 64<<10 {
		return nil
	}
	var values []map[string]any
	if json.Unmarshal(bytes, &values) != nil {
		return nil
	}
	out := []map[string]any{}
	for _, value := range values {
		clean := map[string]any{}
		for k, v := range value {
			if !metadataFields[k] {
				continue
			}
			switch v := v.(type) {
			case string:
				if len(v) <= 1024 {
					clean[k] = v
				}
			case float64, bool, nil:
				clean[k] = v
			}
		}
		if len(clean) > 0 {
			out = append(out, clean)
		}
		if len(out) >= 64 {
			break
		}
	}
	return out
}
func (s *standardRequest) sourceMetadata(source, consistency string, metadata []map[string]any) {
	if len(metadata) == 0 {
		return
	}
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	if s.metadata == nil {
		s.metadata = map[string]any{}
	}
	key := runtimeDigest([]any{source, consistency, metadata})
	if _, exists := s.metadata[key]; exists {
		return
	}
	item := map[string]any{"source": source, "consistency": consistency, "metadata": metadata}
	encoded, _ := json.Marshal(item)
	if len(s.metadata) < 64 && s.metadataBytes+len(encoded) <= 64<<10 {
		s.metadata[key] = item
		s.metadataBytes += len(encoded)
	}
}
