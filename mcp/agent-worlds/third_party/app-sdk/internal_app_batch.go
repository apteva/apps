package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxInternalAppBatchCalls       = 32
	maxInternalAppBatchConcurrency = 8
	maxInternalAppCallResultBytes  = 16 << 20
	maxInternalAppBatchResultBytes = 32 << 20
)

type internalAppBatchHandler struct {
	mcp *mcpHandler
}

func newInternalAppBatchHandler(mcp *mcpHandler) http.Handler {
	return &internalAppBatchHandler{mcp: mcp}
}

func (h *internalAppBatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	w.Header().Set(HeaderInternalAppBatchVersion, InternalAppBatchVersion)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// The framework bearer-token middleware authenticates the target install.
	// Requiring server-minted caller identity as well keeps this transport from
	// becoming a second, agent-facing tool endpoint.
	if strings.TrimSpace(r.Header.Get(HeaderBoundCallerInstallID)) == "" || strings.TrimSpace(r.Header.Get(HeaderBoundCallerAppName)) == "" {
		http.Error(w, "bound app caller required", http.StatusForbidden)
		return
	}
	if raw := r.Header.Get(HeaderAppCallDeadline); raw != "" {
		millis, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid app deadline", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), time.UnixMilli(millis))
		defer cancel()
		r = r.WithContext(ctx)
		if ctx.Err() != nil {
			http.Error(w, "app deadline exceeded", http.StatusGatewayTimeout)
			return
		}
	}

	var request InternalAppBatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "batch must contain exactly one JSON object within 8 MiB", http.StatusBadRequest)
		return
	}
	workers, err := internalBatchWorkers(request.AppBatchOptions)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(request.Calls) == 0 || len(request.Calls) > maxInternalAppBatchCalls {
		http.Error(w, fmt.Sprintf("calls must contain 1-%d items", maxInternalAppBatchCalls), http.StatusBadRequest)
		return
	}
	for i := range request.Calls {
		if strings.TrimSpace(request.Calls[i].Tool) == "" {
			http.Error(w, fmt.Sprintf("calls[%d].tool required", i), http.StatusBadRequest)
			return
		}
		if request.Calls[i].ID == "" {
			request.Calls[i].ID = strconv.Itoa(i + 1)
		}
		if len(request.Calls[i].Input) == 0 {
			request.Calls[i].Input = json.RawMessage(`{}`)
		}
		if !json.Valid(request.Calls[i].Input) {
			http.Error(w, fmt.Sprintf("calls[%d].input must be valid JSON", i), http.StatusBadRequest)
			return
		}
	}

	response := InternalAppBatchResponse{Results: make([]AppCallResult, len(request.Calls))}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	caller := h.mcp.buildCaller(r)
	for i := range response.Results {
		response.Results[i] = AppCallResult{ID: request.Calls[i].ID, Status: 499, Error: &AppCallError{Message: "batch canceled before dispatch"}}
	}
	var next atomic.Int64
	var resultBytes atomic.Int64
	var tooLarge atomic.Bool
	var wg sync.WaitGroup
	run := func() {
		defer wg.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			index := int(next.Add(1) - 1)
			if index >= len(request.Calls) || ctx.Err() != nil {
				return
			}
			call := request.Calls[index]
			result := AppCallResult{ID: call.ID, Status: http.StatusOK}
			args, decodeErr := h.decodeInternalArgs(call.Input)
			if decodeErr != nil {
				result.Error = &AppCallError{Code: -32602, Message: decodeErr.Error()}
				response.Results[index] = result
				continue
			}
			body, callErr := h.mcp.callTool(ctx, caller, true, call.Tool, args)
			if callErr != nil {
				result.Error = &AppCallError{Code: callErr.Code, Message: callErr.Message}
			} else if len(body) > maxInternalAppCallResultBytes {
				result.Status = http.StatusBadGateway
				result.Error = &AppCallError{Message: "app response too large"}
			} else if resultBytes.Add(int64(len(body))) > maxInternalAppBatchResultBytes {
				tooLarge.Store(true)
				cancel()
				return
			} else {
				result.Result = json.RawMessage(body)
				result.Format = "json"
			}
			response.Results[index] = result
		}
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go run()
	}
	wg.Wait()
	if tooLarge.Load() {
		http.Error(w, "batch response too large", http.StatusBadGateway)
		return
	}
	handlerDone := time.Now()
	encoded, err := marshalJSONNoHTMLEscape(response)
	if err != nil || len(encoded) > maxInternalAppBatchResultBytes {
		http.Error(w, "batch response too large or invalid", http.StatusBadGateway)
		return
	}
	w.Header().Set("Server-Timing", fmt.Sprintf("apteva_target_handler;dur=%.1f, apteva_target_encode;dur=%.1f", handlerDone.Sub(started).Seconds()*1000, time.Since(handlerDone).Seconds()*1000))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}

func internalBatchWorkers(options AppBatchOptions) (int, error) {
	switch options.Execution {
	case "", "sequential":
		if options.Concurrency != 0 && options.Concurrency != 1 {
			return 0, errors.New("concurrency requires parallel_independent")
		}
		return 1, nil
	case ParallelIndependent:
		workers := options.Concurrency
		if workers == 0 {
			workers = 4
		}
		if workers < 1 || workers > maxInternalAppBatchConcurrency {
			return 0, fmt.Errorf("concurrency must be between 1 and %d", maxInternalAppBatchConcurrency)
		}
		return workers, nil
	default:
		return 0, errors.New("unknown batch execution mode")
	}
}

func (h *internalAppBatchHandler) decodeInternalArgs(raw json.RawMessage) (map[string]any, error) {
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if policy, ok := h.mcp.app.(JSONNumberPreservingApp); ok && policy.PreserveJSONNumbers() {
		decoder.UseNumber()
	}
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("decode arguments: %w", err)
	}
	if args == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	return args, nil
}
