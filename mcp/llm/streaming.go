package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// streamSink proxies provider SSE bytes straight to the caller.
//
// It tracks whether any bytes have reached the client, because once they have
// we can no longer fail over to another provider or change the status code:
// the response line and early chunks are already on the wire.
type streamSink struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	started  bool
	clientUp bool
}

func newStreamSink(w http.ResponseWriter) *streamSink {
	flusher, _ := w.(http.Flusher)
	return &streamSink{w: w, flusher: flusher, clientUp: true}
}

func (s *streamSink) Started() bool { return s.started }

func (s *streamSink) begin() {
	if s.started {
		return
	}
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
	s.flush()
}

func (s *streamSink) write(p []byte) error {
	s.begin()
	if _, err := s.w.Write(p); err != nil {
		s.clientUp = false
		return err
	}
	s.flush()
	return nil
}

func (s *streamSink) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// providerSupportsStreaming reports whether a live SSE proxy is possible for
// this route. Providers reached through a translated or non-SSE surface keep
// using the buffered emitter, which stays byte-compatible with v0.5.
func providerSupportsStreaming(cfg *ProviderConfig, model string) bool {
	if cfg == nil {
		return false
	}
	if cfg.Provider == "openai-codex" {
		return false
	}
	if cfg.Provider == "anthropic" || providerUsesAnthropicMessages(cfg.Provider, model) {
		return false
	}
	return true
}

// approxTokensFromChars is only used when a provider ends a stream without the
// terminal usage chunk, or when the client disconnects mid-stream. It is a
// deliberate approximation and the usage event is marked estimated so billing
// can treat it differently from metered traffic.
func approxTokensFromChars(chars int64) int64 {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// callOpenAICompatibleStream proxies a provider SSE stream to the caller while
// capturing the usage needed to commit the reservation afterwards.
func (a *App) callOpenAICompatibleStream(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any, sink *streamSink) (*chatResult, error) {
	outBody := cloneMap(body)
	delete(outBody, "_llm_request_id")
	delete(outBody, "request_id")
	outBody["model"] = upstreamModel(cfg.Provider, strArg(body, "model"))
	outBody["stream"] = true
	// Ask for the terminal usage chunk so committed usage reflects what the
	// provider actually metered rather than our pre-flight estimate.
	outBody["stream_options"] = map[string]any{"include_usage": true}

	b, _ := json.Marshal(outBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Nothing has been written to the client yet, so a provider error here can
	// still fail over or surface as a normal JSON error.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &chatResult{Status: resp.StatusCode, Body: raw}, providerError(resp.StatusCode, string(raw))
	}

	// A provider may ignore stream:true and answer with a normal completion.
	// Nothing has reached the client yet, so hand the body back unstreamed and
	// let the caller emit it through the buffered path rather than proxying
	// JSON as if it were SSE.
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream") {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if readErr != nil {
			return nil, readErr
		}
		buffered := &chatResult{Status: resp.StatusCode, Body: raw, RequestID: resp.Header.Get("X-Request-Id")}
		metering := parseProviderMetering(raw)
		buffered.RequestTokens = metering.InputTokens
		buffered.ResponseTokens = metering.OutputTokens
		buffered.ProviderCostMicrounits = metering.CostMicrounits
		buffered.ProviderCostCurrency = metering.CostCurrency
		buffered.ProviderCostReported = metering.CostReported
		buffered.UsageDetails = providerCostDetails(metering)
		if buffered.RequestID == "" {
			buffered.RequestID = "llm_" + randomSuffix(16)
		}
		return buffered, nil
	}

	result := &chatResult{Status: http.StatusOK, RequestID: resp.Header.Get("X-Request-Id"), Streamed: true}
	var (
		usageRaw     json.RawMessage
		streamModel  string
		contentChars int64
		clientErr    error
	)

	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if clientErr == nil {
				if werr := sink.write(line); werr != nil {
					// The caller hung up. Stop proxying but keep reading so the
					// usage chunk still lands and we bill what was produced.
					clientErr = werr
				}
			}
			if trimmed := bytes.TrimSpace(line); bytes.HasPrefix(trimmed, []byte("data:")) {
				payload := bytes.TrimSpace(trimmed[len("data:"):])
				if !bytes.Equal(payload, []byte("[DONE]")) {
					var chunk struct {
						Model   string          `json:"model"`
						Usage   json.RawMessage `json:"usage"`
						Choices []struct {
							Delta struct {
								Content string `json:"content"`
							} `json:"delta"`
						} `json:"choices"`
					}
					if json.Unmarshal(payload, &chunk) == nil {
						if chunk.Model != "" {
							streamModel = chunk.Model
						}
						if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
							usageRaw = chunk.Usage
						}
						for _, choice := range chunk.Choices {
							contentChars += int64(len(choice.Delta.Content))
						}
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if sink.Started() {
				// Partial output already reached the client; commit what we saw
				// rather than discarding the request as failed.
				break
			}
			return result, readErr
		}
	}

	// Reuse the non-streaming metering path by reconstructing the shape it
	// expects from the chunks we observed.
	synthetic := map[string]any{"model": firstNonEmpty(streamModel, strArg(body, "model"))}
	if len(usageRaw) > 0 {
		synthetic["usage"] = usageRaw
	}
	raw, _ := json.Marshal(synthetic)
	result.Body = raw

	metering := parseProviderMetering(raw)
	result.RequestTokens = metering.InputTokens
	result.ResponseTokens = metering.OutputTokens
	result.ProviderCostMicrounits = metering.CostMicrounits
	result.ProviderCostCurrency = metering.CostCurrency
	result.ProviderCostReported = metering.CostReported
	result.UsageDetails = providerCostDetails(metering)
	if len(usageRaw) == 0 {
		result.ResponseTokens = approxTokensFromChars(contentChars)
		result.UsageDetails = mergeStreamDetails(result.UsageDetails, map[string]any{
			"stream_usage": "estimated",
		})
	}
	if clientErr != nil {
		result.UsageDetails = mergeStreamDetails(result.UsageDetails, map[string]any{
			"stream_status": "client_disconnected",
		})
	}
	if result.RequestID == "" {
		result.RequestID = "llm_" + randomSuffix(16)
	}
	return result, nil
}

// mergeStreamDetails folds extra keys into the provider cost detail blob
// without disturbing the fields cost reporting already writes.
func mergeStreamDetails(details json.RawMessage, extra map[string]any) json.RawMessage {
	merged := map[string]any{}
	if len(details) > 0 {
		_ = json.Unmarshal(details, &merged)
	}
	for k, v := range extra {
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return details
	}
	return out
}

// writeSSEStreamError ends an already-started stream with an OpenAI-shaped
// error event. The HTTP status is long gone, so this is the only way to tell
// the caller the completion did not finish.
func writeSSEStreamError(sink *streamSink, err error) {
	if sink == nil || !sink.Started() {
		return
	}
	_, typ := errorStatus(err)
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]any{"type": typ, "message": err.Error()},
	})
	_ = sink.write([]byte("data: " + string(payload) + "\n\n"))
	_ = sink.write([]byte("data: [DONE]\n\n"))
}
