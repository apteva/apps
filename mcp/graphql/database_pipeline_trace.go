package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gql "github.com/graphql-go/graphql"
)

// Measurement of one database pipeline call. It only observes: nothing here
// decides what a pipeline reads, which filter it sends or what a client sees.
// The one thing it adds to a request is `diagnostics: true` on count steps,
// which Database removes from the response before a client could see it (see
// takeDatabaseStatistics) and which never changes what is counted.
//
// A trace is stored on the request log (runtime_metrics_json.pipelines), never
// in a response or a Server-Timing header, because a filter hash and a plan
// describe the caller's permissions.

const (
	maxPipelineTraces          = 32
	maxPipelineTraceOperations = 16
	maxDatabaseStatisticsBytes = 4 << 10
	databaseStatisticsRecheck  = 5 * time.Minute
)

const (
	pipelineStageResolution = "resolution"
	pipelineStageAccess     = "access"
	pipelineStageBatch      = "database_batch"
	pipelineStageAssemble   = "assemble"
)

const (
	pipelineStatisticsNotRequested = "not_requested" // nothing counted in Database
	pipelineStatisticsRequested    = "requested"     // asked, batch still running
	pipelineStatisticsReported     = "reported"
	pipelineStatisticsMissing      = "missing"     // asked and accepted, but not returned
	pipelineStatisticsUnavailable  = "unavailable" // this Database version refuses the option
)

type pipelineTrace struct {
	Field     string  `json:"field,omitempty"`
	Outcome   string  `json:"outcome"`         // ok, error or in_flight
	Stage     string  `json:"stage,omitempty"` // where a failed or unfinished call stopped
	ErrorCode string  `json:"error_code,omitempty"`
	TotalMS   float64 `json:"total_ms"`
	// FilterHash identifies the resolved filter of the page (or the first
	// filtered step). Requests with the same hash asked Database the same thing.
	FilterHash   string              `json:"filter_hash,omitempty"`
	Access       pipelineAccessTrace `json:"access,omitzero"`
	ResolutionMS float64             `json:"resolution_ms"`
	Batch        *pipelineBatchTrace `json:"database_batch,omitempty"`
	AssembleMS   float64             `json:"assemble_ms"`
}

type pipelineAccessTrace struct {
	MS              float64           `json:"ms"`
	Batches         int               `json:"batches"`
	Reads           int               `json:"reads"`
	AnsweredLocally int               `json:"answered_locally,omitempty"`
	Wire            pipelineWireTrace `json:"wire,omitzero"`
}

type pipelineBatchTrace struct {
	WallMS float64           `json:"wall_ms"`
	Wire   pipelineWireTrace `json:"wire"`
	// DatabaseReportedMS is the longest execution Database reported. Reads in
	// a batch may overlap, so it is a floor for Database's share, not a sum.
	DatabaseReportedMS float64 `json:"database_reported_ms"`
	// UnattributedServerWaitMS is the server wait Database did not account for:
	// the gateway, Database's queue and snapshot, and every read it reports no
	// time for (finds, today). It is not a measurement of the gateway alone.
	UnattributedServerWaitMS float64                  `json:"unattributed_server_wait_ms"`
	Statistics               string                   `json:"statistics"`
	RetriedWithoutStatistics bool                     `json:"retried_without_statistics,omitempty"`
	Operations               []pipelineOperationTrace `json:"operations"`
	OperationsOmitted        int                      `json:"operations_omitted,omitempty"`
}

type pipelineOperationTrace struct {
	Step            string         `json:"step,omitempty"`
	Op              string         `json:"op"`
	CountOf         string         `json:"count_of,omitempty"`
	FilterHash      string         `json:"filter_hash,omitempty"`
	AnsweredLocally bool           `json:"answered_locally,omitempty"`
	Rows            *int           `json:"rows,omitempty"`
	HasMore         bool           `json:"has_more,omitempty"`
	Count           any            `json:"count,omitempty"`
	ExecutionMS     *float64       `json:"execution_ms,omitempty"`
	Statistics      map[string]any `json:"statistics,omitempty"`
}

// pipelineWireTrace splits one call by what the SDK's HTTP client saw. Every
// stage but server_wait_ms is this process or its connection; server_wait_ms
// runs from the request being written to the first response byte, which is the
// gateway and Database together.
type pipelineWireTrace struct {
	PrepareMS        float64 `json:"prepare_ms"`
	ConnectionWaitMS float64 `json:"connection_wait_ms"`
	RequestWriteMS   float64 `json:"request_write_ms"`
	ServerWaitMS     float64 `json:"server_wait_ms"`
	ResponseReadMS   float64 `json:"response_read_ms"`
	DecodeMS         float64 `json:"decode_ms"`
	ResponseBytes    int64   `json:"response_bytes"`
	ReusedConnection bool    `json:"reused_connection,omitempty"`
}

// traceMS keeps a stored timing to the microsecond.
func traceMS(value float64) float64 { return math.Round(value*1000) / 1000 }

func (w pipelineWireTrace) rounded() pipelineWireTrace {
	w.PrepareMS = traceMS(w.PrepareMS)
	w.ConnectionWaitMS = traceMS(w.ConnectionWaitMS)
	w.RequestWriteMS = traceMS(w.RequestWriteMS)
	w.ServerWaitMS = traceMS(w.ServerWaitMS)
	w.ResponseReadMS = traceMS(w.ResponseReadMS)
	w.DecodeMS = traceMS(w.DecodeMS)
	return w
}

func (w *pipelineWireTrace) add(other pipelineWireTrace) {
	w.PrepareMS += other.PrepareMS
	w.ConnectionWaitMS += other.ConnectionWaitMS
	w.RequestWriteMS += other.RequestWriteMS
	w.ServerWaitMS += other.ServerWaitMS
	w.ResponseReadMS += other.ResponseReadMS
	w.DecodeMS += other.DecodeMS
	w.ResponseBytes += other.ResponseBytes
	w.ReusedConnection = w.ReusedConnection || other.ReusedConnection
}

// pipelineWire marks one db_batch call. httptrace hooks can fire on other
// goroutines and after a cancelled call has returned, hence the atomics. Marks
// are nanoseconds after start, and zero means not reached.
type pipelineWire struct {
	started                                   time.Time
	getConn, gotConn, wrote, firstByte        atomic.Int64
	decodeStarted, decodeFinished, returnedAt atomic.Int64
	responseBytes                             atomic.Int64
	reused                                    atomic.Bool
}

func (w *pipelineWire) mark(target *atomic.Int64) {
	target.Store(max(int64(time.Since(w.started)), 1))
}

func (w *pipelineWire) attach(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { w.mark(&w.getConn) },
		GotConn: func(info httptrace.GotConnInfo) {
			w.mark(&w.gotConn)
			w.reused.Store(info.Reused)
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { w.mark(&w.wrote) },
		GotFirstResponseByte: func() { w.mark(&w.firstByte) },
	})
}

func (w *pipelineWire) decoding(size int) {
	if w != nil {
		w.responseBytes.Store(int64(size))
		w.mark(&w.decodeStarted)
	}
}

func (w *pipelineWire) decoded() {
	if w != nil {
		w.mark(&w.decodeFinished)
	}
}

func (w *pipelineWire) returned() {
	if w != nil {
		w.mark(&w.returnedAt)
	}
}

// report reads the marks at now; a stage still running extends to now, so a
// call abandoned at the deadline shows the stage it was stuck in.
func (w *pipelineWire) report(now time.Time) pipelineWireTrace {
	if w == nil {
		return pipelineWireTrace{}
	}
	end := w.returnedAt.Load()
	if end == 0 {
		end = max(int64(now.Sub(w.started)), 1)
	}
	span := func(from, to int64) float64 {
		if to == 0 {
			to = end
		}
		return milliseconds(time.Duration(max(to-from, 0)))
	}
	getConn, gotConn, wrote, firstByte := w.getConn.Load(), w.gotConn.Load(), w.wrote.Load(), w.firstByte.Load()
	decodeStarted, decodeFinished := w.decodeStarted.Load(), w.decodeFinished.Load()
	out := pipelineWireTrace{PrepareMS: span(0, getConn), ResponseBytes: w.responseBytes.Load(), ReusedConnection: w.reused.Load()}
	if getConn > 0 {
		out.ConnectionWaitMS = span(getConn, gotConn)
	}
	if gotConn > 0 {
		out.RequestWriteMS = span(gotConn, wrote)
	}
	if wrote > 0 {
		out.ServerWaitMS = span(wrote, firstByte)
	}
	if firstByte > 0 {
		out.ResponseReadMS = span(firstByte, decodeStarted)
	}
	if decodeStarted > 0 {
		out.DecodeMS = span(decodeStarted, decodeFinished)
	}
	return out
}

// databaseBatchResponse decodes db_batch's inner result like a plain struct
// would. The method exists to time the decode, which happens inside the SDK.
type databaseBatchResponse struct {
	Results []any
	wire    *pipelineWire
}

func (r *databaseBatchResponse) UnmarshalJSON(data []byte) error {
	r.wire.decoding(len(data))
	defer r.wire.decoded()
	var plain struct {
		Results []any `json:"results"`
	}
	if err := json.Unmarshal(data, &plain); err != nil {
		return err
	}
	r.Results = plain.Results
	return nil
}

type pipelineFieldKey struct{}

// withPipelineField names the GraphQL field a pipeline resolves for its trace.
func withPipelineField(ctx context.Context, p gql.ResolveParams) context.Context {
	if p.Info.ParentType == nil {
		return ctx
	}
	field := p.Info.ParentType.Name() + "." + p.Info.FieldName
	if len(field) > 512 {
		field = "[long field name]"
	}
	return context.WithValue(ctx, pipelineFieldKey{}, field)
}

// pipelineTraceRecorder is shared with the request's result: graphql-go returns
// as soon as the deadline passes, while the resolver is still unwinding, so the
// request log reads a trace by snapshot rather than waiting for it to finish.
// All methods accept a nil receiver, for the publish-time probe that has none.
type pipelineTraceRecorder struct {
	mu           sync.Mutex
	started      time.Time
	finished     time.Time
	trace        pipelineTrace
	stage        string
	accessWall   time.Duration
	accessWire   pipelineWireTrace
	batchWall    time.Duration
	openStage    string
	openStarted  time.Time
	openWire     *pipelineWire
	assembleFrom time.Time
	sentCount    bool
}

func startPipelineTrace(ctx context.Context) *pipelineTraceRecorder {
	r := &pipelineTraceRecorder{started: time.Now(), stage: pipelineStageResolution}
	r.trace.Field, _ = ctx.Value(pipelineFieldKey{}).(string)
	if state, _ := ctx.Value(standardRequestKey{}).(*standardRequest); state != nil {
		state.timingMu.Lock()
		if len(state.pipelineTraces) < maxPipelineTraces {
			state.pipelineTraces = append(state.pipelineTraces, r)
		} else {
			state.metrics.PipelinesOmitted++
		}
		state.timingMu.Unlock()
	}
	return r
}

// pipelineFilterHash is a truncated digest of the filter exactly as sent, so a
// log can show two reads shared a filter without storing the caller's scope.
func pipelineFilterHash(where any) string {
	if where == nil {
		return ""
	}
	encoded, err := json.Marshal(where) // map keys marshal sorted
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8])
}

// beginBatch opens a batch. The hashes are taken before the clock starts, so
// they count as resolution rather than as time spent in Database.
func (r *pipelineTraceRecorder) beginBatch(stage string, steps []databasePipelineStep, operations []any) {
	if r == nil {
		return
	}
	var traced []pipelineOperationTrace
	omitted, first, page := 0, "", ""
	if stage == pipelineStageBatch {
		for i, raw := range operations {
			op, _ := raw.(map[string]any)
			args, _ := op["args"].(map[string]any)
			name, _ := op["op"].(string)
			hash := pipelineFilterHash(args["where"])
			if first == "" {
				first = hash
			}
			if page == "" && i < len(steps) && steps[i].Page != nil {
				page = hash
			}
			if len(traced) == maxPipelineTraceOperations {
				omitted++
				continue
			}
			entry := pipelineOperationTrace{Op: name, FilterHash: hash}
			if i < len(steps) {
				entry.Step, entry.CountOf = steps[i].ID, steps[i].CountOf
			}
			traced = append(traced, entry)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stage, r.openStage, r.openStarted, r.openWire = stage, stage, time.Now(), nil
	if stage == pipelineStageAccess {
		r.trace.Access.Batches++
		r.trace.Access.Reads += len(operations)
		return
	}
	r.trace.FilterHash = first
	if page != "" {
		r.trace.FilterHash = page
	}
	r.trace.Batch = &pipelineBatchTrace{Statistics: pipelineStatisticsNotRequested, Operations: traced, OperationsOmitted: omitted}
}

func (r *pipelineTraceRecorder) answeredLocally(index int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.openStage == pipelineStageAccess {
		r.trace.Access.AnsweredLocally++
	} else if r.trace.Batch != nil && index < len(r.trace.Batch.Operations) {
		r.trace.Batch.Operations[index].AnsweredLocally = true
	}
}

// attachWire starts one call's marks. A retried call replaces the marks; its
// wall time stays in the batch's.
func (r *pipelineTraceRecorder) attachWire(ctx context.Context) (context.Context, *pipelineWire) {
	if r == nil {
		return ctx, nil
	}
	wire := &pipelineWire{started: time.Now()}
	r.mu.Lock()
	r.openWire = wire
	r.mu.Unlock()
	return wire.attach(ctx), wire
}

// noteStatistics records what the batch being sent asked of Database.
func (r *pipelineTraceRecorder) noteStatistics(operations []any) {
	if r == nil {
		return
	}
	counts, requested := false, false
	for _, raw := range operations {
		op, _ := raw.(map[string]any)
		args, _ := op["args"].(map[string]any)
		counts = counts || op["op"] == "count"
		requested = requested || args["diagnostics"] == true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.trace.Batch == nil || r.openStage != pipelineStageBatch {
		return
	}
	switch {
	case requested:
		r.trace.Batch.Statistics = pipelineStatisticsRequested
	case counts:
		r.trace.Batch.Statistics = pipelineStatisticsUnavailable
	}
}

func (r *pipelineTraceRecorder) statisticsRefused() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.trace.Batch != nil {
		r.trace.Batch.RetriedWithoutStatistics = true
		r.trace.Batch.Statistics = pipelineStatisticsUnavailable
	}
}

// endBatch closes the open batch. results is nil when the batch failed, and
// statistics holds what takeDatabaseStatistics took out of each result.
func (r *pipelineTraceRecorder) endBatch(results []any, statistics []map[string]any) {
	if r == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	wall := now.Sub(r.openStarted)
	wire := r.openWire.report(now)
	if r.openStage == pipelineStageAccess {
		r.accessWall += wall
		r.accessWire.add(wire)
		r.openStage, r.openWire = "", nil
		if results != nil {
			r.stage = pipelineStageResolution
		}
		return
	}
	r.batchWall = wall
	batch := r.trace.Batch
	batch.Wire = wire
	r.openStage, r.openWire = "", nil
	if results == nil {
		if batch.Statistics == pipelineStatisticsRequested {
			batch.Statistics = pipelineStatisticsMissing
		}
		return
	}
	reported := false
	for i := range batch.Operations {
		op := &batch.Operations[i]
		if i >= len(results) {
			break
		}
		result, _ := results[i].(map[string]any)
		switch op.Op {
		case "find":
			records, _ := result["records"].([]any)
			rows := len(records)
			op.Rows, op.HasMore = &rows, result["hasMore"] == true
		case "aggregate":
			records, _ := result["rows"].([]any)
			rows := len(records)
			op.Rows = &rows
		case "count":
			op.Count = result["count"]
		}
		if i < len(statistics) && statistics[i] != nil {
			reported = true
			op.Statistics = boundedDatabaseStatistics(statistics[i])
			if value, ok := statistics[i]["executionMs"].(float64); ok {
				op.ExecutionMS = &value
				batch.DatabaseReportedMS = max(batch.DatabaseReportedMS, value)
			}
		}
	}
	if batch.Statistics == pipelineStatisticsRequested {
		batch.Statistics = pipelineStatisticsMissing
		if reported {
			batch.Statistics = pipelineStatisticsReported
		}
	}
	r.stage, r.assembleFrom = pipelineStageAssemble, now
}

func (r *pipelineTraceRecorder) finish(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = time.Now()
	r.trace.Outcome = "ok"
	if err != nil {
		r.trace.Outcome = "error"
		r.trace.Stage = r.stage
		r.trace.ErrorCode = errorCode(err)
	}
}

// snapshot reports the call as of now, whether it has finished or not.
func (r *pipelineTraceRecorder) snapshot() pipelineTrace {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.finished
	out := r.trace
	if now.IsZero() {
		now = time.Now()
		out.Outcome = "in_flight"
		out.Stage = r.stage
	}
	access, accessWire := r.accessWall, r.accessWire
	batchWall := r.batchWall
	if out.Batch != nil {
		copied := *out.Batch
		copied.Operations = append([]pipelineOperationTrace(nil), out.Batch.Operations...)
		out.Batch = &copied
	}
	switch r.openStage {
	case pipelineStageAccess:
		access += now.Sub(r.openStarted)
		accessWire.add(r.openWire.report(now))
	case pipelineStageBatch:
		batchWall = now.Sub(r.openStarted)
		out.Batch.Wire = r.openWire.report(now)
	}
	var assemble time.Duration
	if !r.assembleFrom.IsZero() {
		assemble = now.Sub(r.assembleFrom)
	}
	total := now.Sub(r.started)
	out.TotalMS = traceMS(milliseconds(total))
	out.Access.MS = traceMS(milliseconds(access))
	out.Access.Wire = accessWire.rounded()
	out.AssembleMS = traceMS(milliseconds(assemble))
	out.ResolutionMS = traceMS(milliseconds(max(total-access-batchWall-assemble, 0)))
	if out.Batch != nil {
		out.Batch.WallMS = traceMS(milliseconds(batchWall))
		out.Batch.UnattributedServerWaitMS = traceMS(max(out.Batch.Wire.ServerWaitMS-out.Batch.DatabaseReportedMS, 0))
		out.Batch.Wire = out.Batch.Wire.rounded()
	}
	return out
}

// takeDatabaseStatistics removes count diagnostics from each result before
// anything can decode it, so they can't reach a resolver or a client.
func takeDatabaseStatistics(results []any) []map[string]any {
	var out []map[string]any
	for i, raw := range results {
		result, _ := raw.(map[string]any)
		statistics, ok := result["diagnostics"].(map[string]any)
		delete(result, "diagnostics")
		if !ok {
			continue
		}
		if out == nil {
			out = make([]map[string]any, len(results))
		}
		out[i] = statistics
	}
	return out
}

// boundedDatabaseStatistics keeps counters and names. Lists are dropped: a
// statistic is never a list of values, and the log must not grow with a plan.
func boundedDatabaseStatistics(raw map[string]any) map[string]any {
	out := boundedStatisticsMap(raw, 0)
	if encoded, _ := json.Marshal(out); len(encoded) > maxDatabaseStatisticsBytes {
		for key, value := range out {
			if _, nested := value.(map[string]any); nested {
				delete(out, key)
			}
		}
		out["truncated"] = true
	}
	return out
}

func boundedStatisticsMap(raw map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		switch typed := value.(type) {
		case bool, float64:
			out[key] = typed
		case string:
			if len(typed) <= 128 {
				out[key] = typed
			}
		case map[string]any:
			if depth < 3 {
				out[key] = boundedStatisticsMap(typed, depth+1)
			}
		}
	}
	return out
}

func operationsRequestStatistics(operations []any) bool {
	for _, raw := range operations {
		op, _ := raw.(map[string]any)
		if args, _ := op["args"].(map[string]any); args["diagnostics"] == true {
			return true
		}
	}
	return false
}

// withoutStatistics copies a batch minus the option, for a Database that
// refuses it. Filters and everything else are the same values.
func withoutStatistics(operations []any) []any {
	out := make([]any, len(operations))
	for i, raw := range operations {
		out[i] = raw
		op, _ := raw.(map[string]any)
		args, _ := op["args"].(map[string]any)
		if args["diagnostics"] != true {
			continue
		}
		copied := make(map[string]any, len(args))
		for key, value := range args {
			if key != "diagnostics" {
				copied[key] = value
			}
		}
		out[i] = map[string]any{"op": op["op"], "args": copied}
	}
	return out
}

// databaseRefusedStatistics recognises Database's strict decoder rejecting the
// option. That happens while it reads the request, before any read runs, so
// resending the same batch without it can't run a count twice.
func databaseRefusedStatistics(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "unknown field") && strings.Contains(message, "diagnostics")
}

// wantsDatabaseStatistics is false for a while after a project's Database
// refuses them, so that version isn't sent a doomed batch on every request. It
// asks again later because Database can be upgraded underneath a running app.
func (a *App) wantsDatabaseStatistics(project string) bool {
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	refused, known := a.statsRefused[project]
	return !known || time.Since(refused) >= databaseStatisticsRecheck
}

func (a *App) rememberStatisticsRefused(project string) {
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	if a.statsRefused == nil {
		a.statsRefused = map[string]time.Time{}
	}
	a.statsRefused[project] = time.Now()
}
