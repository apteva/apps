package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Real streaming: chunks are relayed to the client as the provider produces
// them. Usage comes from the provider's final usage message; when a stream
// is cut short the output is estimated from the text actually sent. Failover
// is only possible until the first chunk reaches the client.

type chunkEmitter func(chunk map[string]any) error

// sseWriter writes headers lazily, so errors that happen before the first
// chunk can still be returned as a normal JSON error with a status code.
type sseWriter struct {
	w       http.ResponseWriter
	adm     *gatewayAdmission
	started bool
	failed  bool
}

func (s *sseWriter) start() {
	if s.started {
		return
	}
	s.started = true
	s.adm.streamStarted = true
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Request-Id", s.adm.req.RequestID)
	writeAdmissionHeaders(s.w, s.adm)
	s.w.WriteHeader(http.StatusOK)
}

func (s *sseWriter) data(value any) error {
	s.start()
	if s.failed {
		return errClientGone
	}
	b, _ := json.Marshal(value)
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		s.failed = true
		return errClientGone
	}
	s.flush()
	return nil
}

func (s *sseWriter) event(name string, value any) error {
	s.start()
	if s.failed {
		return errClientGone
	}
	b, _ := json.Marshal(value)
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		s.failed = true
		return errClientGone
	}
	s.flush()
	return nil
}

func (s *sseWriter) done() {
	if s.failed {
		return
	}
	s.start()
	_, _ = io.WriteString(s.w, "data: [DONE]\n\n")
	s.flush()
}

func (s *sseWriter) flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

var errClientGone = errors.New("client disconnected")

func chunkHasContent(chunk map[string]any) bool {
	choices, _ := chunk["choices"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if s, _ := delta["content"].(string); s != "" {
			return true
		}
		if calls, _ := delta["tool_calls"].([]any); len(calls) > 0 {
			return true
		}
		for _, k := range []string{"reasoning_content", "reasoning"} {
			if s, _ := delta[k].(string); s != "" {
				return true
			}
		}
	}
	return false
}

// chunkOutputChars counts generated characters for the fallback estimate.
func chunkOutputChars(chunk map[string]any) int64 {
	var n int64
	choices, _ := chunk["choices"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		for _, k := range []string{"content", "reasoning_content", "reasoning"} {
			if s, ok := delta[k].(string); ok {
				n += int64(len(s))
			}
		}
		calls, _ := delta["tool_calls"].([]any)
		for _, raw := range calls {
			call, _ := raw.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			if s, ok := fn["name"].(string); ok {
				n += int64(len(s))
			}
			if s, ok := fn["arguments"].(string); ok {
				n += int64(len(s))
			}
		}
	}
	return n
}

func estimateOutputTokens(chars int64) int64 {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// streamClient has no overall timeout: a long generation is not a hung
// request. Header timeouts and the request context still apply.
func (a *App) streamClient() *http.Client {
	return &http.Client{Transport: a.httpClient.Transport}
}

func (a *App) streamChatAttempt(reqCtx context.Context, appCtx *sdk.AppCtx, cfg *ProviderConfig, apiKey string, body map[string]any, emit chunkEmitter) (*chatResult, error) {
	switch {
	case cfg.Provider == "openai-codex":
		res, err := a.callOpenAICodexIntegration(reqCtx, appCtx, cfg, body)
		if err == nil {
			err = validateChatCompletion(res.Body)
		}
		if err != nil {
			return res, err
		}
		return res, emitBufferedChat(res.Body, emit)
	case cfg.Provider == "anthropic" || providerUsesAnthropicMessages(cfg.Provider, strArg(body, "model")):
		return a.streamAnthropic(reqCtx, cfg, apiKey, body, emit)
	default:
		return a.streamOpenAICompatible(reqCtx, cfg, apiKey, body, emit, true)
	}
}

// emitBufferedChat replays a complete chat response as stream chunks, for
// providers that cannot stream.
func emitBufferedChat(raw []byte, emit chunkEmitter) error {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return providerError(http.StatusBadGateway, "provider returned an invalid chat response")
	}
	id := firstNonEmpty(strAny(response["id"]), "chatcmpl_"+randomSuffix(12))
	model := strAny(response["model"])
	created := int64FromAny(response["created"])
	if created == 0 {
		created = time.Now().Unix()
	}
	choices, _ := response["choices"].([]any)
	if len(choices) == 0 {
		return providerError(http.StatusBadGateway, "provider response contains no completion choices")
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	delta := map[string]any{"role": "assistant"}
	if content, exists := message["content"]; exists {
		delta["content"] = content
	}
	if calls, ok := message["tool_calls"].([]any); ok {
		indexed := make([]any, 0, len(calls))
		for i, raw := range calls {
			call, _ := raw.(map[string]any)
			c := cloneMap(call)
			if c == nil {
				c = map[string]any{}
			}
			c["index"] = i
			indexed = append(indexed, c)
		}
		delta["tool_calls"] = indexed
	}
	if err := emit(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}); err != nil {
		return err
	}
	return emit(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": choice["finish_reason"]}}})
}

type sseEvent struct {
	Event string
	Data  string
}

// readSSE calls fn for each server-sent event until EOF or fn returns false.
func readSSE(r io.Reader, fn func(sseEvent) (bool, error)) error {
	reader := bufio.NewReaderSize(r, 64<<10)
	var ev sseEvent
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(data) > 0 || ev.Event != "" {
					ev.Data = strings.Join(data, "\n")
					cont, ferr := fn(ev)
					if ferr != nil || !cont {
						return ferr
					}
				}
				ev, data = sseEvent{}, nil
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event:"):
				ev.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err != nil {
			if err == io.EOF {
				if len(data) > 0 {
					ev.Data = strings.Join(data, "\n")
					_, ferr := fn(ev)
					return ferr
				}
				return nil
			}
			return err
		}
	}
}

func (a *App) streamOpenAICompatible(reqCtx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any, emit chunkEmitter, withUsageOption bool) (*chatResult, error) {
	outBody := cloneMap(body)
	delete(outBody, "_llm_request_id")
	delete(outBody, "request_id")
	outBody["model"] = upstreamModel(cfg.Provider, strArg(body, "model"))
	outBody["stream"] = true
	if withUsageOption {
		outBody["stream_options"] = map[string]any{"include_usage": true}
	} else {
		delete(outBody, "stream_options")
	}
	b, _ := json.Marshal(outBody)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := a.streamClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	requestID := firstNonEmpty(resp.Header.Get("X-Request-Id"), "llm_"+randomSuffix(16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if withUsageOption && resp.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(string(raw)), "stream_options") {
			return a.streamOpenAICompatible(reqCtx, cfg, apiKey, body, emit, false)
		}
		return &chatResult{Status: resp.StatusCode, Body: raw, RequestID: requestID}, providerError(resp.StatusCode, string(raw))
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream") {
		// The provider ignored stream=true and answered in one piece.
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if err := validateChatCompletion(raw); err != nil {
			return nil, err
		}
		metering := parseProviderMetering(raw)
		res := &chatResult{Status: resp.StatusCode, Body: raw, RequestID: requestID, RequestTokens: metering.InputTokens, ResponseTokens: metering.OutputTokens,
			ProviderCostMicrounits: metering.CostMicrounits, ProviderCostCurrency: metering.CostCurrency, ProviderCostReported: metering.CostReported,
			UsageDetails: providerCostDetails(metering)}
		return res, emitBufferedChat(raw, emit)
	}

	res := &chatResult{Status: resp.StatusCode, RequestID: requestID}
	var usageChunk []byte
	var outputChars int64
	sawChunk := false
	readErr := readSSE(resp.Body, func(ev sseEvent) (bool, error) {
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return true, nil
		}
		if data == "[DONE]" {
			return false, nil
		}
		var chunk map[string]any
		dec := json.NewDecoder(strings.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&chunk); err != nil {
			return false, providerError(http.StatusBadGateway, "provider sent a malformed stream chunk")
		}
		if errObj, ok := chunk["error"]; ok && errObj != nil {
			b, _ := json.Marshal(errObj)
			return false, providerError(http.StatusBadGateway, string(b))
		}
		sawChunk = true
		if usage, ok := chunk["usage"]; ok && usage != nil {
			usageChunk = []byte(data)
		}
		delete(chunk, "usage")
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			return true, nil
		}
		outputChars += chunkOutputChars(chunk)
		if err := emit(chunk); err != nil {
			return false, err
		}
		return true, nil
	})
	finishStreamResult(res, usageChunk, outputChars)
	if readErr != nil {
		if reqCtx.Err() != nil || errors.Is(readErr, errClientGone) {
			res.ClientDisconnected = true
			return res, context.Canceled
		}
		return res, readErr
	}
	if !sawChunk {
		return res, providerError(http.StatusBadGateway, "provider stream ended without any data")
	}
	return res, nil
}

func finishStreamResult(res *chatResult, usageJSON []byte, outputChars int64) {
	metering := parseProviderMetering(usageJSON)
	res.RequestTokens = metering.InputTokens
	res.ResponseTokens = metering.OutputTokens
	res.ProviderCostMicrounits = metering.CostMicrounits
	res.ProviderCostCurrency = metering.CostCurrency
	res.ProviderCostReported = metering.CostReported
	res.UsageDetails = providerCostDetails(metering)
	if metering.InputTokens == 0 && metering.OutputTokens == 0 {
		res.UsageEstimated = true
		res.ResponseTokens = estimateOutputTokens(outputChars)
	}
}

func (a *App) streamAnthropic(reqCtx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any, emit chunkEmitter) (*chatResult, error) {
	anthropicReq, err := openAIChatToAnthropic(cfg.Provider, body)
	if err != nil {
		return nil, err
	}
	anthropicReq["stream"] = true
	b, _ := json.Marshal(anthropicReq)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/messages", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Api-Key", apiKey)
	if cfg.Provider != "anthropic" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := a.streamClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	requestID := firstNonEmpty(resp.Header.Get("Request-Id"), "llm_"+randomSuffix(16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &chatResult{Status: resp.StatusCode, Body: raw, RequestID: requestID}, providerError(resp.StatusCode, string(raw))
	}
	model := strArg(body, "model")
	id := "chatcmpl_" + randomSuffix(12)
	created := time.Now().Unix()
	usage := map[string]any{}
	toolIndex := map[int64]int{}
	finish := "stop"
	var outputChars int64
	sawMessage := false
	chunk := func(delta map[string]any, finishReason any) map[string]any {
		return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}}}
	}
	mergeUsage := func(u map[string]any) {
		for k, v := range u {
			if v != nil {
				usage[k] = v
			}
		}
	}
	readErr := readSSE(resp.Body, func(ev sseEvent) (bool, error) {
		if strings.TrimSpace(ev.Data) == "" {
			return true, nil
		}
		var payload map[string]any
		dec := json.NewDecoder(strings.NewReader(ev.Data))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			return false, providerError(http.StatusBadGateway, "provider sent a malformed stream event")
		}
		typ := firstNonEmpty(strAny(payload["type"]), ev.Event)
		switch typ {
		case "message_start":
			sawMessage = true
			msg, _ := payload["message"].(map[string]any)
			if mid := strAny(msg["id"]); mid != "" {
				id = mid
			}
			if u, ok := msg["usage"].(map[string]any); ok {
				mergeUsage(u)
			}
			return true, emit(chunk(map[string]any{"role": "assistant", "content": ""}, nil))
		case "content_block_start":
			block, _ := payload["content_block"].(map[string]any)
			if strAny(block["type"]) == "tool_use" {
				idx := int64FromAny(payload["index"])
				n := len(toolIndex)
				toolIndex[idx] = n
				name := strAny(block["name"])
				outputChars += int64(len(name))
				return true, emit(chunk(map[string]any{"tool_calls": []any{map[string]any{
					"index": n, "id": strAny(block["id"]), "type": "function",
					"function": map[string]any{"name": name, "arguments": ""},
				}}}, nil))
			}
		case "content_block_delta":
			delta, _ := payload["delta"].(map[string]any)
			switch strAny(delta["type"]) {
			case "text_delta":
				text := rawStr(delta["text"])
				outputChars += int64(len(text))
				return true, emit(chunk(map[string]any{"content": text}, nil))
			case "input_json_delta":
				part := rawStr(delta["partial_json"])
				outputChars += int64(len(part))
				n := toolIndex[int64FromAny(payload["index"])]
				return true, emit(chunk(map[string]any{"tool_calls": []any{map[string]any{
					"index": n, "function": map[string]any{"arguments": part},
				}}}, nil))
			case "thinking_delta":
				outputChars += int64(len(rawStr(delta["thinking"])))
			}
		case "message_delta":
			delta, _ := payload["delta"].(map[string]any)
			switch strAny(delta["stop_reason"]) {
			case "tool_use":
				finish = "tool_calls"
			case "max_tokens":
				finish = "length"
			}
			if u, ok := payload["usage"].(map[string]any); ok {
				mergeUsage(u)
			}
		case "message_stop":
			return false, emit(chunk(map[string]any{}, finish))
		case "error":
			b, _ := json.Marshal(payload["error"])
			return false, providerError(http.StatusBadGateway, string(b))
		}
		return true, nil
	})
	usageJSON, _ := json.Marshal(map[string]any{"usage": usage})
	res := &chatResult{Status: resp.StatusCode, RequestID: requestID}
	finishStreamResult(res, usageJSON, outputChars)
	if readErr != nil {
		if reqCtx.Err() != nil || errors.Is(readErr, errClientGone) {
			res.ClientDisconnected = true
			return res, context.Canceled
		}
		return res, readErr
	}
	if !sawMessage {
		return res, providerError(http.StatusBadGateway, "provider stream ended without any data")
	}
	return res, nil
}

// streamChat serves /v1/chat/completions with stream=true.
func (a *App) streamChat(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, ident *TokenIdentity, body map[string]any) {
	includeUsage := false
	if opts, ok := body["stream_options"].(map[string]any); ok {
		includeUsage = boolArg(opts, "include_usage")
	}
	delete(body, "stream")
	delete(body, "stream_options")
	adm, err := a.admitChat(r.Context(), ctx, ident, body, true)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	out := &sseWriter{w: w, adm: adm}
	var lastChunk map[string]any
	emit := func(chunk map[string]any) error {
		if chunkHasContent(chunk) {
			adm.markFirstToken()
		}
		if adm.viaPublic() {
			if _, ok := chunk["model"]; ok {
				chunk["model"] = adm.public.Name
			}
		}
		lastChunk = chunk
		return out.data(chunk)
	}
	result, used, callErr := adm.run(body, func(attempt providerAttempt, cfg *ProviderConfig, key string, attemptBody map[string]any) (*chatResult, error) {
		return a.streamChatAttempt(r.Context(), ctx, cfg, key, attemptBody, emit)
	})
	ev, _ := adm.complete(used, result, callErr)
	clientErr := adm.clientError(callErr)
	if callErr != nil && !out.started {
		writeGatewayError(w, clientErr)
		return
	}
	if callErr != nil && !errors.Is(callErr, context.Canceled) {
		_, typ := errorStatus(clientErr)
		if !adm.viaPublic() {
			typ = "provider_error"
		}
		_ = out.data(map[string]any{"error": map[string]any{"message": clientErr.Error(), "type": typ}})
	}
	if includeUsage && ev != nil {
		final := map[string]any{"object": "chat.completion.chunk", "choices": []any{}, "usage": map[string]any{
			"prompt_tokens": ev.RequestTokens, "completion_tokens": ev.ResponseTokens, "total_tokens": ev.TotalTokens,
		}}
		if lastChunk != nil {
			final["id"], final["created"], final["model"] = lastChunk["id"], lastChunk["created"], lastChunk["model"]
		}
		_ = out.data(final)
	}
	out.done()
}

// streamResponses serves /v1/responses with stream=true by translating chat
// chunks into Responses API events as they arrive.
func (a *App) streamResponses(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, ident *TokenIdentity, chatReq map[string]any) {
	adm, err := a.admitChat(r.Context(), ctx, ident, chatReq, true)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	out := &sseWriter{w: w, adm: adm}
	responseID := "resp_" + randomSuffix(12)
	createdAt := time.Now().Unix()
	model := strArg(chatReq, "model")
	var text strings.Builder
	type toolCall struct{ id, name, args string }
	var calls []*toolCall
	opened := false
	open := func() error {
		if opened {
			return nil
		}
		opened = true
		return out.event("response.created", map[string]any{"type": "response.created", "response": map[string]any{
			"id": responseID, "object": "response", "created_at": createdAt, "model": model, "status": "in_progress", "output": []any{},
		}})
	}
	emit := func(chunk map[string]any) error {
		if err := open(); err != nil {
			return err
		}
		if chunkHasContent(chunk) {
			adm.markFirstToken()
		}
		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			choice, _ := c.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if s, _ := delta["content"].(string); s != "" {
				text.WriteString(s)
				if err := out.event("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": s, "output_index": 0, "content_index": 0}); err != nil {
					return err
				}
			}
			deltaCalls, _ := delta["tool_calls"].([]any)
			for _, raw := range deltaCalls {
				call, _ := raw.(map[string]any)
				idx := int(int64FromAny(call["index"]))
				for len(calls) <= idx {
					calls = append(calls, &toolCall{})
				}
				tc := calls[idx]
				if id := strAny(call["id"]); id != "" {
					tc.id = id
				}
				fn, _ := call["function"].(map[string]any)
				if name := strAny(fn["name"]); name != "" {
					tc.name = name
				}
				tc.args += rawStr(fn["arguments"])
			}
		}
		return nil
	}
	result, used, callErr := adm.run(chatReq, func(attempt providerAttempt, cfg *ProviderConfig, key string, attemptBody map[string]any) (*chatResult, error) {
		return a.streamChatAttempt(r.Context(), ctx, cfg, key, attemptBody, emit)
	})
	ev, _ := adm.complete(used, result, callErr)
	clientErr := adm.clientError(callErr)
	if callErr != nil && !out.started {
		writeGatewayError(w, clientErr)
		return
	}
	output := []any{}
	if text.Len() > 0 {
		output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text.String()}}})
	}
	for _, tc := range calls {
		item := map[string]any{"type": "function_call", "call_id": tc.id, "name": tc.name, "arguments": tc.args}
		output = append(output, item)
		_ = out.event("response.output_item.done", map[string]any{"type": "response.output_item.done", "item": item})
	}
	response := map[string]any{"id": responseID, "object": "response", "created_at": createdAt, "model": model, "output": output}
	if ev != nil {
		response["usage"] = map[string]any{"input_tokens": ev.RequestTokens, "output_tokens": ev.ResponseTokens, "total_tokens": ev.TotalTokens}
	}
	if callErr != nil {
		response["status"] = "incomplete"
		if !errors.Is(callErr, context.Canceled) {
			_, typ := errorStatus(clientErr)
			if !adm.viaPublic() {
				typ = "provider_error"
			}
			response["error"] = map[string]any{"message": clientErr.Error(), "type": typ}
		}
		_ = out.event("response.failed", map[string]any{"type": "response.failed", "response": response})
		return
	}
	response["status"] = "completed"
	_ = out.event("response.completed", map[string]any{"type": "response.completed", "response": response})
}

// rawStr returns a string value without trimming; streamed text deltas are
// whitespace-significant.
func rawStr(v any) string {
	s, _ := v.(string)
	return s
}
