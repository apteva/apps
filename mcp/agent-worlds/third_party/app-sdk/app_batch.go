package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// AppBatchOptions defaults to sequential execution. ParallelIndependent is an
// explicit assertion that no call depends on another call's effects. Parallel
// batches default to four workers and are limited to eight by the server.
type AppBatchOptions struct {
	Execution   string `json:"execution,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	// ResultMode "json" negotiates direct tool JSON; the default preserves MCP.
	ResultMode string `json:"result_mode,omitempty"`
}

const ParallelIndependent = "parallel_independent"

// InternalAppBatchPath is the SDK-owned target-side transport used by the
// platform for authenticated app-to-app batches. It is deliberately separate
// from /mcp: external MCP clients keep the standard JSON-RPC surface, while a
// new platform can negotiate this endpoint and safely fall back for older
// sidecars.
const InternalAppBatchPath = "/_apteva/internal/app-calls"

// HeaderInternalAppBatchVersion is returned by the SDK-owned batch endpoint.
// The platform must only treat a response as the internal protocol when this
// marker is present; a custom application route at the same path cannot be
// mistaken for a successful batch response.
const HeaderInternalAppBatchVersion = "X-Apteva-Internal-App-Batch-Version"
const InternalAppBatchVersion = "1"

// InternalAppCall and InternalAppBatchRequest are transport contracts between
// apteva-server and SDK sidecars. Inputs stay as raw JSON across the server so
// internal dispatch does not add an MCP envelope or decode application data.
// App developers should continue to use AppCall and CallAppBatchContext.
type InternalAppCall struct {
	ID    string          `json:"id,omitempty"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`
}

type InternalAppBatchRequest struct {
	Calls []InternalAppCall `json:"calls"`
	AppBatchOptions
}

type InternalAppBatchResponse struct {
	Results []AppCallResult `json:"results"`
}

// HeaderAppCallDeadline carries the caller's absolute Unix-millisecond
// deadline across HTTP hops. Cancellation also propagates via request context.
const HeaderAppCallDeadline = "X-Apteva-App-Deadline-Ms"

func setAppCallDeadline(req *http.Request) {
	if deadline, ok := req.Context().Deadline(); ok {
		req.Header.Set(HeaderAppCallDeadline, strconv.FormatInt(deadline.UnixMilli(), 10))
	}
}

// AppContextClient is optional so existing PlatformClient implementations and
// test doubles remain source compatible. No fallback silently drops context.
type AppContextClient interface {
	CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error)
	CallAppResultContext(context.Context, string, string, map[string]any, any) error
	CallAppBatchContext(context.Context, string, []AppCall, AppBatchOptions) ([]AppCallResult, error)
}

func CallAppContext(ctx context.Context, client PlatformClient, app, tool string, input map[string]any) (json.RawMessage, error) {
	if api, ok := client.(AppContextClient); ok {
		return api.CallAppContext(ctx, app, tool, input)
	}
	return nil, errors.New("context-aware app API unavailable")
}

func CallAppResultContext(ctx context.Context, client PlatformClient, app, tool string, input map[string]any, out any) error {
	if api, ok := client.(AppContextClient); ok {
		return api.CallAppResultContext(ctx, app, tool, input, out)
	}
	return errors.New("context-aware app API unavailable")
}

func CallAppBatchContext(ctx context.Context, client PlatformClient, app string, calls []AppCall, options AppBatchOptions) ([]AppCallResult, error) {
	if api, ok := client.(AppContextClient); ok {
		return api.CallAppBatchContext(ctx, app, calls, options)
	}
	return nil, errors.New("context-aware app batch API unavailable")
}

// AppCall is one sibling-app MCP tool invocation in a batch. IDs are opaque
// to the platform and are echoed in the corresponding AppCallResult.
type AppCall struct {
	ID    string         `json:"id,omitempty"`
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input,omitempty"`
}

// AppCallError describes a child failure without failing the whole batch.
type AppCallError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message"`
}

// AppCallResult is the raw target response for one child call. Result keeps
// the target's MCP response envelope so callers that need the legacy shape
// can decode it with CallAppResult's existing rules. Error is populated for a
// child that could not be dispatched or whose target returned a non-2xx code.
type AppCallResult struct {
	ID     string          `json:"id"`
	Status int             `json:"status,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *AppCallError   `json:"error,omitempty"`
	// Format is "json" only when Result is direct tool data, never an envelope.
	Format string `json:"format,omitempty"`
}

// Decode handles both negotiated JSON and legacy MCP results. Batch callers
// should check/decode each child even when the outer HTTP request succeeded.
func (r AppCallResult) Decode(out any) error {
	if r.Error != nil {
		return fmt.Errorf("app call %s: %s (code=%d)", r.ID, r.Error.Message, r.Error.Code)
	}
	if r.Format == "json" {
		return json.Unmarshal(r.Result, out)
	}
	return decodeMCPEnvelope(r.Result, "batch", r.ID, out)
}

// AppBatchClient is an optional PlatformClient extension. Keeping it
// separate preserves source compatibility for custom clients and test stubs.
type AppBatchClient interface {
	CallAppBatch(appName string, calls []AppCall) ([]AppCallResult, error)
}

// CallAppBatch invokes several tools on one sibling app with one outer
// authorization decision. It returns one result per child in input order.
// Clients talking to an older server receive a clear capability error.
func CallAppBatch(client PlatformClient, appName string, calls []AppCall) ([]AppCallResult, error) {
	if client == nil {
		return nil, errors.New("app batch API unavailable")
	}
	if api, ok := client.(AppBatchClient); ok {
		return api.CallAppBatch(appName, calls)
	}
	return nil, errors.New("app batch API unavailable")
}
