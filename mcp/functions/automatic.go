package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/apteva/apps/mcp/functions/internal/admission"
)

func automaticError(err error) error {
	var e *admission.Error
	if errors.As(err, &e) {
		return resourceError(e.Code, e.Reason)
	}
	return err
}
func (p *pool) acquireAutomatic(ctx context.Context, fn *Function) (*admission.Permit, error) {
	if p.auto == nil {
		return nil, nil
	} // small unit-test pools have no running scheduler
	class := requestClass(ctx, fn)
	if trace := traceFrom(ctx); trace != nil {
		trace.state("queued")
	}
	started := time.Now()
	permit, err := p.auto.Acquire(ctx, admission.Request{
		Key: fn.ProjectID + ":" + strconv.FormatInt(fn.ID, 10), Operation: fn.InstanceKey + ":" + strconv.FormatInt(automaticVersion(fn), 10),
		Caller: fn.ProjectID, Background: class == "background", Nested: class == "nested",
		MaxConcurrency: policy(fn).Concurrency, MaxQueue: p.settings().MaxQueuePerFunction,
		Wait: time.Duration(policy(fn).QueueMS) * time.Millisecond,
	})
	if trace := traceFrom(ctx); trace != nil {
		trace.mu.Lock()
		trace.AutomaticWaitMS += time.Since(started).Milliseconds()
		trace.mu.Unlock()
	}
	if err != nil {
		return nil, p.reject(automaticError(err))
	}
	return permit, nil
}
func workerCPUSeconds(w *worker) float64 {
	if w == nil || w.cmd == nil || w.cmd.Process == nil {
		return -1
	}
	root := os.Getenv("APTEVA_FUNCTIONS_CGROUP_ROOT")
	if root == "" {
		root = "/sys/fs/cgroup/apteva-functions"
	}
	return admission.ProcessCPU(w.cmd.Process.Pid, filepath.Join(root, fmt.Sprintf("worker-%d", w.cmd.Process.Pid)))
}
func (p *pool) automaticSnapshot(project string) map[string]any {
	if p.auto == nil {
		return map[string]any{"mode": "unavailable"}
	}
	snapshot := p.auto.Snapshot()
	if project != "" {
		rows := []map[string]any{}
		for _, r := range snapshot["operations"].([]map[string]any) {
			if strings.HasPrefix(r["key"].(string), project+":") {
				rows = append(rows, r)
			}
		}
		snapshot["operations"] = rows
	}
	// Destination IDs are shared infrastructure; only publish totals here. The
	// platform-admin gateway API exposes target-level identities after auth.
	if p.autoDownstream != nil {
		d := p.autoDownstream.Snapshot()
		delete(d, "operations")
		snapshot["downstream"] = d
	}
	return snapshot
}
func writeAutomaticOverload(w http.ResponseWriter, err error) bool {
	var e *ResourceError
	if !errors.As(err, &e) || !strings.HasPrefix(e.Code, "adaptive_") {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e.Error(), "error_code": e.Code, "retryable": true, "retry_after_ms": 1000})
	return true
}

func automaticVersion(fn *Function) int64 {
	if fn.ActiveVersionID != nil {
		return *fn.ActiveVersionID
	}
	return 0
}
