package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RuntimePolicy is persisted independently of immutable source deployments.
// Zero numeric fields inherit operator defaults; max_idle_workers uses a pointer
// so an explicit zero disables warm retention.
type RuntimePolicy struct {
	Class         string `json:"class,omitempty"`
	Concurrency   int    `json:"concurrency,omitempty"`
	MaxIdle       *int   `json:"max_idle_workers,omitempty"`
	IdleMS        int    `json:"idle_timeout_ms,omitempty"`
	QueueMS       int    `json:"queue_timeout_ms,omitempty"`
	AppMS         int    `json:"app_timeout_ms,omitempty"`
	IntegrationMS int    `json:"integration_timeout_ms,omitempty"`
}

func (r RuntimePolicy) validate() error {
	if r.Class != "" && r.Class != "interactive" && r.Class != "background" {
		return errors.New("limits.class must be interactive or background")
	}
	if r.Concurrency < 0 || r.Concurrency > 1024 || r.MaxIdle != nil && (*r.MaxIdle < 0 || *r.MaxIdle > 1024) {
		return errors.New("worker limits must be between 0 and 1024")
	}
	for _, v := range []int{r.IdleMS, r.QueueMS, r.AppMS, r.IntegrationMS} {
		if v < 0 || v > 600000 {
			return errors.New("limit durations must be between 0 and 600000 ms")
		}
	}
	return nil
}
func policy(fn *Function) RuntimePolicy {
	r := fn.Limits
	if r.Class == "" {
		r.Class = "interactive"
	}
	if r.Concurrency == 0 {
		r.Concurrency = 8
	}
	if r.IdleMS == 0 {
		r.IdleMS = 300000
	}
	if r.QueueMS == 0 {
		r.QueueMS = 10000
	}
	if r.AppMS == 0 {
		r.AppMS = envInt("APTEVA_FUNCTIONS_APP_TIMEOUT_MS", 30000, 1, 600000)
	}
	if r.IntegrationMS == 0 {
		r.IntegrationMS = envInt("APTEVA_FUNCTIONS_INTEGRATION_TIMEOUT_MS", 300000, 1, 600000)
	}
	return r
}

type CapacitySettings struct {
	InteractiveQueue      int `json:"interactive_reserved_queue"`
	NestedQueue           int `json:"nested_reserved_queue"`
	AppTimeoutMS          int `json:"app_timeout_ms"`
	IntegrationTimeoutMS  int `json:"integration_timeout_ms"`
	TotalMemoryMB         int `json:"total_memory_mb"`
	MaxWorkers            int `json:"max_workers"`
	MaxWorkerMemoryMB     int `json:"max_worker_memory_mb"`
	InteractiveMemoryMB   int `json:"interactive_reserved_memory_mb"`
	InteractiveWorkers    int `json:"interactive_reserved_workers"`
	NestedMemoryMB        int `json:"nested_reserved_memory_mb"`
	NestedWorkers         int `json:"nested_reserved_workers"`
	MaxDownstream         int `json:"max_downstream_calls"`
	InteractiveDownstream int `json:"interactive_reserved_downstream"`
	NestedDownstream      int `json:"nested_reserved_downstream"`
	MaxQueue              int `json:"max_queue"`
	MaxQueuePerFunction   int `json:"max_queue_per_function"`
	PreparationWorkers    int `json:"max_prepared_idle_workers"`
	HostHeadroomMB        int `json:"host_headroom_mb"`
	MaxNestedDepth        int `json:"max_nested_depth"`
}

func defaultCapacity() CapacitySettings {
	total := envInt("APTEVA_FUNCTIONS_TOTAL_MEMORY_MB", 4096, 16, 1048576)
	workers := envInt("APTEVA_FUNCTIONS_MAX_WORKERS", 32, 1, 1024)
	downstream := envInt("APTEVA_FUNCTIONS_MAX_DOWNSTREAM_TOTAL", 64, 1, 1024)
	return CapacitySettings{InteractiveQueue: envInt("APTEVA_FUNCTIONS_MAX_QUEUE", 256, 1, 10000) / 4, NestedQueue: envInt("APTEVA_FUNCTIONS_MAX_QUEUE", 256, 1, 10000) / 8, AppTimeoutMS: envInt("APTEVA_FUNCTIONS_APP_TIMEOUT_MS", 30000, 1, 600000), IntegrationTimeoutMS: envInt("APTEVA_FUNCTIONS_INTEGRATION_TIMEOUT_MS", 300000, 1, 600000), TotalMemoryMB: total, MaxWorkers: workers, MaxWorkerMemoryMB: envInt("APTEVA_FUNCTIONS_MAX_WORKER_MEMORY_MB", 1024, 16, 65536), InteractiveMemoryMB: total / 4, InteractiveWorkers: workers / 4, NestedMemoryMB: total / 8, NestedWorkers: workers / 8, MaxDownstream: downstream, InteractiveDownstream: downstream / 4, NestedDownstream: downstream / 8, MaxQueue: envInt("APTEVA_FUNCTIONS_MAX_QUEUE", 256, 1, 10000), MaxQueuePerFunction: envInt("APTEVA_FUNCTIONS_MAX_QUEUE_PER_FUNCTION", 64, 1, 10000), PreparationWorkers: 2, HostHeadroomMB: 512, MaxNestedDepth: 4}
}
func (s CapacitySettings) validate() error {
	if s.AppTimeoutMS < 1 || s.AppTimeoutMS > 600000 || s.IntegrationTimeoutMS < 1 || s.IntegrationTimeoutMS > 600000 {
		return errors.New("callback deadlines must be 1..600000 ms")
	}
	if s.TotalMemoryMB < 16 || s.TotalMemoryMB > 1048576 || s.MaxWorkers < 1 || s.MaxWorkers > 1024 || s.MaxWorkerMemoryMB < 16 || s.MaxWorkerMemoryMB > 65536 || s.MaxDownstream < 1 || s.MaxDownstream > 1024 || s.MaxQueue < 1 || s.MaxQueue > 10000 || s.MaxQueuePerFunction < 1 || s.MaxQueuePerFunction > 10000 || s.MaxNestedDepth < 1 || s.MaxNestedDepth > 16 || (s.HostHeadroomMB < 0 || s.HostHeadroomMB > 1048576) {
		return errors.New("invalid capacity settings")
	}
	if s.InteractiveQueue < 0 || s.NestedQueue < 0 || s.InteractiveQueue+s.NestedQueue >= s.MaxQueue {
		return errors.New("reserved queues must leave shared queue capacity")
	}
	if s.InteractiveMemoryMB < 0 || s.NestedMemoryMB < 0 || s.InteractiveMemoryMB+s.NestedMemoryMB >= s.TotalMemoryMB || s.InteractiveWorkers < 0 || s.NestedWorkers < 0 || s.InteractiveWorkers+s.NestedWorkers >= s.MaxWorkers || s.InteractiveDownstream < 0 || s.NestedDownstream < 0 || s.InteractiveDownstream+s.NestedDownstream >= s.MaxDownstream || s.PreparationWorkers < 0 || s.PreparationWorkers > s.MaxWorkers {
		return errors.New("reserved capacity must leave shared capacity; preparation workers must fit the global limit")
	}
	if limit := hostMemoryLimitMB(); limit > 0 && int64(s.TotalMemoryMB+s.HostHeadroomMB+envInt("APTEVA_FUNCTIONS_MAX_BUILDS", 2, 1, 32)*envInt("APTEVA_FUNCTIONS_BUILD_MEMORY_MB", 1024, 64, 8192)+envInt("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", 128, 16, 1024)) > limit {
		return fmt.Errorf("worker budget plus host/build/protocol headroom exceeds effective host limit %d MiB", limit)
	}
	return nil
}
func effectiveMaxMemoryMB() int {
	if p := currentPool(); p != nil {
		return p.settings().MaxWorkerMemoryMB
	}
	return defaultCapacity().MaxWorkerMemoryMB
}
func (p *pool) settings() CapacitySettings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settingsLocked()
}
func (p *pool) settingsLocked() CapacitySettings {
	if p.capacity.MaxWorkers == 0 {
		return defaultCapacity()
	}
	return p.capacity
}
func (p *pool) initCapacity() error {
	p.capacity = defaultCapacity()
	p.liveCalls = map[int64]*callTrace{}
	p.rejections = map[string]int64{}
	p.downstreamClasses = map[string]int{}
	var b string
	err := p.ctx.AppDB().QueryRow("SELECT settings_json FROM function_capacity_settings WHERE id=1").Scan(&b)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(b), &p.capacity); err != nil {
			return err
		}
	}
	// Existing installations continue with explicit warnings if their configured
	// budget exceeds the new host validation. API updates must pass validation.
	if err := p.capacity.validate(); err != nil {
		p.capacityWarning = err.Error()
	}
	return nil
}

type ResourceError struct {
	Code        string `json:"code"`
	Reason      string `json:"reason"`
	RequestedMB int    `json:"requested_memory_mb,omitempty"`
	AvailableMB int    `json:"available_memory_mb,omitempty"`
	Retryable   bool   `json:"retryable"`
}

func (e *ResourceError) Is(target error) bool { return target == errFunctionBusy }
func (e *ResourceError) Error() string        { return e.Code + ": " + e.Reason }
func resourceError(code, reason string) *ResourceError {
	return &ResourceError{Code: code, Reason: reason, Retryable: true}
}
func errorCode(err error) string {
	var e *ResourceError
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.Canceled) {
		return "caller_canceled"
	}
	return ""
}
func (p *pool) reject(err error) error {
	p.mu.Lock()
	if p.rejections == nil {
		p.rejections = map[string]int64{}
	}
	p.rejections[errorCode(err)]++
	p.mu.Unlock()
	return err
}

type traceKey struct{}
type admissionClassKey struct{}

func traceFrom(ctx context.Context) *callTrace { t, _ := ctx.Value(traceKey{}).(*callTrace); return t }
func requestClass(ctx context.Context, fn *Function) string {
	if c, ok := ctx.Value(admissionClassKey{}).(string); ok {
		return c
	}
	if t := traceFrom(ctx); t != nil && t.ParentID != 0 {
		return "nested"
	}
	return policy(fn).Class
}

type CallResources struct {
	ProtocolReservedBytes int64              `json:"downstream_buffer_reserved_bytes"`
	ProtocolPeakBytes     int64              `json:"downstream_buffer_peak_reserved_bytes"`
	InvocationID          int64              `json:"invocation_id"`
	ParentID              int64              `json:"parent_invocation_id,omitempty"`
	FunctionID            int64              `json:"function_id"`
	ProjectID             string             `json:"project_id"`
	FunctionName          string             `json:"function_name"`
	Class                 string             `json:"class"`
	State                 string             `json:"state"`
	ReservedMB            int                `json:"worker_allowance_mb"`
	MemoryStart           *int64             `json:"worker_memory_start_bytes"`
	MemoryCurrent         *int64             `json:"worker_memory_current_bytes"`
	MemoryPeak            *int64             `json:"worker_memory_sampled_peak_bytes"`
	MemorySource          string             `json:"memory_source"`
	ErrorCode             string             `json:"error_code,omitempty"`
	ErrorDetails          *ResourceError     `json:"error_details,omitempty"`
	QueueMS               int64              `json:"capacity_wait_ms"`
	Downstream            []DownstreamRecord `json:"downstream_calls"`
	DownstreamDropped     int                `json:"downstream_records_dropped"`
	StartedAt             time.Time          `json:"started_at"`
	Depth                 int                `json:"depth"`
}
type DownstreamRecord struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Target     string    `json:"target"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	TimeoutMS  int64     `json:"timeout_ms"`
	ErrorCode  string    `json:"error_code,omitempty"`
}
type callTrace struct {
	mu sync.Mutex
	CallResources
	chain []int64
}

func (t *callTrace) snapshot() CallResources {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.CallResources
	c.Downstream = append([]DownstreamRecord{}, t.Downstream...)
	for i := range c.Downstream {
		if c.Downstream[i].State == "running" {
			c.Downstream[i].DurationMS = time.Since(c.Downstream[i].StartedAt).Milliseconds()
		}
	}
	if c.State == "running" {
		for _, d := range c.Downstream {
			if d.State == "running" {
				c.State = "waiting_downstream"
				break
			}
		}
	}
	return c
}
func (t *callTrace) state(state string) {
	if t != nil {
		t.mu.Lock()
		t.State = state
		t.mu.Unlock()
	}
}
func (t *callTrace) sample(w *worker) {
	if t == nil {
		return
	}
	m, source, _ := workerMemory(w)
	t.mu.Lock()
	defer t.mu.Unlock()
	if m != nil {
		v := *m
		t.MemorySource = source
		t.MemoryCurrent = &v
		if t.MemoryStart == nil {
			t.MemoryStart = &v
		}
		if t.MemoryPeak == nil || v > *t.MemoryPeak {
			t.MemoryPeak = &v
		}
	}
}
func (p *pool) newTrace(ctx context.Context, fn *Function, id int64) *callTrace {
	t := &callTrace{CallResources: CallResources{InvocationID: id, FunctionID: fn.ID, FunctionName: fn.Name, ProjectID: fn.ProjectID, Class: policy(fn).Class, State: "preparing", ReservedMB: fn.MaxMemoryMB, MemorySource: "unavailable", StartedAt: time.Now().UTC(), Downstream: []DownstreamRecord{}}, chain: []int64{fn.ID}}
	if parent := traceFrom(ctx); parent != nil {
		t.ParentID = parent.InvocationID
		t.Depth = parent.Depth + 1
		t.chain = append(append([]int64{}, parent.chain...), fn.ID)
		t.Class = "nested"
	}
	p.mu.Lock()
	if p.liveCalls == nil {
		p.liveCalls = map[int64]*callTrace{}
	}
	p.liveCalls[id] = t
	p.mu.Unlock()
	return t
}
func (p *pool) finishTrace(t *callTrace, err error, res *invokeResult) {
	t.mu.Lock()
	t.State = res.Status
	t.ErrorCode = errorCode(err)
	if res.ErrorCode != "" {
		t.ErrorCode = res.ErrorCode
	}
	var detail *ResourceError
	if errors.As(err, &detail) {
		t.ErrorDetails = detail
	}
	t.mu.Unlock()
	b, _ := json.Marshal(t.snapshot())
	if _, err := p.ctx.AppDB().Exec("INSERT INTO function_invocation_resources(invocation_id,resources_json) VALUES (?,?) ON CONFLICT(invocation_id) DO UPDATE SET resources_json=excluded.resources_json", t.InvocationID, string(b)); err != nil {
		p.ctx.Logger().Warn("persist invocation resources", "err", err)
	}
	p.mu.Lock()
	delete(p.liveCalls, t.InvocationID)
	p.mu.Unlock()
}
func (p *pool) capacitySnapshot(pid string) map[string]any {
	p.mu.Lock()
	settings := p.settingsLocked()
	reserved := p.liveMB
	workers := make([]*worker, 0, len(p.all))
	for w := range p.all {
		workers = append(workers, w)
	}
	calls := []*callTrace{}
	for _, t := range p.liveCalls {
		if t.ProjectID == pid {
			calls = append(calls, t)
		}
	}
	reasons := map[string]int64{}
	for k, v := range p.rejections {
		reasons[k] = v
	}
	down := map[string]int{}
	for k, v := range p.downstreamClasses {
		down[k] = v
	}
	queueByClass := map[string]int{}
	for class, n := range p.queueClasses {
		queueByClass[class] = n
	}
	globalQueued := len(p.globalQueue)
	protocolByClass := map[string]int64{}
	for class, n := range p.protocolClasses {
		protocolByClass[class] = n
	}
	classReservations := map[string]map[string]int{}
	for class, u := range p.classReservations {
		classReservations[class] = map[string]int{"reserved_memory_mb": u[0], "workers_including_starting": u[1]}
	}
	starting := len(p.globalSem) - len(p.all)
	warning := p.capacityWarning
	p.mu.Unlock()
	ws := []map[string]any{}
	var actual int64
	measured := 0
	for _, w := range workers {
		p.mu.Lock()
		workerClass := w.capacityClass
		p.mu.Unlock()
		m, source, oom := workerMemory(w)
		if m != nil {
			actual += *m
			measured++
		}
		if w.projectID != pid {
			continue
		}
		ws = append(ws, map[string]any{"function_id": w.fnID, "function_name": w.fnName, "version_id": w.versionID, "pid": w.cmd.Process.Pid, "class": workerClass, "state": w.capacityState.Load(), "invocation_id": w.invocationID.Load(), "reserved_memory_mb": w.memoryMB, "memory_current_bytes": m, "memory_source": source, "oom_kills": oom})
	}
	groups := map[int64]*FunctionCapacity{}
	for _, w := range ws {
		id := w["function_id"].(int64)
		g := groups[id]
		if g == nil {
			g = &FunctionCapacity{FunctionID: id, Name: w["function_name"].(string)}
			groups[id] = g
		}
		g.Workers++
		g.ReservedMB += w["reserved_memory_mb"].(int)
		if w["state"] == "idle" {
			g.Idle++
		} else {
			g.Active++
		}
		if m, ok := w["memory_current_bytes"].(*int64); ok && m != nil {
			if g.ActualBytes == nil {
				g.ActualBytes = new(int64)
			}
			*g.ActualBytes += *m
			g.Measured++
		}
	}
	live := []CallResources{}
	queued := 0
	for _, t := range calls {
		c := t.snapshot()
		if groups[c.FunctionID] == nil {
			groups[c.FunctionID] = &FunctionCapacity{FunctionID: c.FunctionID, Name: c.FunctionName}
		}
		if c.State == "queued" {
			queued++
			if g := groups[c.FunctionID]; g != nil {
				g.Queued++
			}
		}
		live = append(live, c)
	}
	functionGroups := []*FunctionCapacity{}
	for _, g := range groups {
		functionGroups = append(functionGroups, g)
	}
	return map[string]any{"functions": functionGroups, "settings": settings, "protocol_memory_limit_mb": envInt("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", 128, 16, 1024), "protocol_reserved_bytes": protocolBytes.Load(), "effective_host_memory_mb": hostMemoryLimitMB(), "validation_warning": warning, "global": map[string]any{"reserved_by_class": classReservations, "starting_workers": starting, "reserved_memory_mb": reserved, "live_workers": len(workers), "actual_worker_memory_bytes": actual, "measured_workers": measured, "memory_measurement_complete": measured == len(workers), "downstream_by_class": down, "queue_depth": globalQueued, "queued_by_class": queueByClass, "protocol_reserved_by_class": protocolByClass, "rejections": reasons}, "project_id": pid, "workers": ws, "calls": live, "queue_depth": queued, "memory_note": "Current/peak values measure the worker, including its loaded runtime and retained allocations. Peaks are sampled during the call, not exclusive allocations by that call."}
}
func (a *App) handleHTTPCapacity(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "GET required")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	p := currentPool()
	if p == nil {
		httpErr(w, 503, "runtime unavailable")
		return
	}
	httpJSON(w, p.capacitySnapshot(pid))
}
func (a *App) handleHTTPCapacitySettings(w http.ResponseWriter, r *http.Request) {
	p := currentPool()
	if p == nil {
		httpErr(w, 503, "runtime unavailable")
		return
	}
	if r.Method == "GET" {
		httpJSON(w, map[string]any{"settings": p.settings(), "effective_host_memory_mb": hostMemoryLimitMB()})
		return
	}
	if r.Method != "PUT" {
		httpErr(w, 405, "GET or PUT required")
		return
	}
	var s CapacitySettings
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := s.validate(); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	// Settings changes serialize with admission; lowering a budget never kills work.
	p.mu.Lock()
	if s.TotalMemoryMB < p.liveMB || s.MaxWorkers < len(p.globalSem) {
		p.mu.Unlock()
		httpErr(w, 409, "new limits are below current reservations; drain workers first")
		return
	}
	b, _ := json.Marshal(s)
	_, err := p.ctx.AppDB().Exec("INSERT INTO function_capacity_settings(id,settings_json) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET settings_json=excluded.settings_json", string(b))
	if err == nil {
		p.capacity = s
		p.capacityWarning = ""
	}
	p.mu.Unlock()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	p.signal()
	httpJSON(w, map[string]any{"settings": s})
}

// StoredResources supports SQLite TEXT without flattening the API JSON object.
type StoredResources json.RawMessage

func (s *StoredResources) Scan(value any) error {
	switch v := value.(type) {
	case string:
		*s = []byte(v)
	case []byte:
		*s = append((*s)[:0], v...)
	case nil:
		*s = []byte("null")
	default:
		return fmt.Errorf("invalid resource record %T", value)
	}
	return nil
}
func (s StoredResources) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("null"), nil
	}
	return s, nil
}
func readIntFile(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

type FunctionCapacity struct {
	FunctionID  int64  `json:"function_id"`
	Name        string `json:"function_name"`
	Workers     int    `json:"workers"`
	Active      int    `json:"active_workers"`
	Idle        int    `json:"idle_workers"`
	Queued      int    `json:"queued_calls"`
	ReservedMB  int    `json:"reserved_memory_mb"`
	ActualBytes *int64 `json:"actual_memory_bytes"`
	Measured    int    `json:"measured_workers"`
}

func (t *callTrace) protocolReservation(n int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ProtocolReservedBytes += n
	if t.ProtocolReservedBytes > t.ProtocolPeakBytes {
		t.ProtocolPeakBytes = t.ProtocolReservedBytes
	}
}

// Admission and worker reservation are sequential; include failed waits too.
func (t *callTrace) addCapacityWait(elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.QueueMS += elapsed.Milliseconds()
	t.mu.Unlock()
}
