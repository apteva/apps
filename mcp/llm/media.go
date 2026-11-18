package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// OpenAI-compatible audio and image endpoints. They go through the same
// admission, limits, reservation, and settlement path as chat. Quantities
// that are not tokens (audio seconds, characters, images) are recorded in
// usage_details.units and passed to the settle hook as `units`.

const (
	maxAudioUploadBytes  = 25 << 20
	maxMediaResponseSize = 64 << 20
)

func providerSupportsMedia(cfg *ProviderConfig) error {
	if cfg.Provider == "anthropic" || cfg.Provider == "openai-codex" {
		return typedErr{status: http.StatusBadRequest, typ: "invalid_request_error", msg: fmt.Sprintf("provider %s does not offer this endpoint", cfg.Provider)}
	}
	return nil
}

func (a *App) postProvider(ctx context.Context, cfg *ProviderConfig, apiKey, path, contentType string, payload []byte) (*chatResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaResponseSize))
	if err != nil {
		return nil, err
	}
	res := &chatResult{Status: resp.StatusCode, Body: raw, ContentType: resp.Header.Get("Content-Type"),
		RequestID: firstNonEmpty(resp.Header.Get("X-Request-Id"), "llm_"+randomSuffix(16))}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, providerError(resp.StatusCode, string(raw))
	}
	if strings.Contains(strings.ToLower(res.ContentType), "json") {
		metering := parseProviderMetering(raw)
		res.RequestTokens = metering.InputTokens
		res.ResponseTokens = metering.OutputTokens
		res.ProviderCostMicrounits = metering.CostMicrounits
		res.ProviderCostCurrency = metering.CostCurrency
		res.ProviderCostReported = metering.CostReported
		res.UsageDetails = providerCostDetails(metering)
	}
	return res, nil
}

func writeMediaResult(w http.ResponseWriter, res *chatResult) {
	if res.ContentType != "" {
		w.Header().Set("Content-Type", res.ContentType)
	}
	if res.RequestID != "" {
		w.Header().Set("X-Request-Id", res.RequestID)
	}
	costHeaders(w, res.UsageEvent)
	writeAdmissionHeaders(w, res.admission)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// ---- /v1/audio/transcriptions (multipart) ----

func (a *App) handleV1Transcriptions(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "expected multipart/form-data with a file field: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "file is required")
		return
	}
	audio, err := io.ReadAll(io.LimitReader(file, maxAudioUploadBytes+1))
	file.Close()
	if err != nil || len(audio) > maxAudioUploadBytes {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("file must be at most %d MB", maxAudioUploadBytes>>20))
		return
	}
	fields := map[string][]string{}
	for k, v := range r.MultipartForm.Value {
		fields[k] = v
	}
	model := strings.TrimSpace(firstValue(fields["model"]))
	delete(fields, "model")
	requestID := requestIDFor(r, map[string]any{"request_id": firstValue(fields["request_id"])})
	delete(fields, "request_id")
	adm, err := a.admit(r.Context(), ctx, ident, gatewayRequest{Endpoint: "audio.transcriptions", Model: model, RequestID: requestID})
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	result, used, callErr := adm.run(nil, func(attempt providerAttempt, cfg *ProviderConfig, key string, _ map[string]any) (*chatResult, error) {
		if err := providerSupportsMedia(cfg); err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("model", upstreamModel(cfg.Provider, attempt.Model))
		for k, values := range fields {
			for _, v := range values {
				_ = mw.WriteField(k, v)
			}
		}
		part, err := mw.CreateFormFile("file", firstNonEmpty(header.Filename, "audio"))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(audio); err != nil {
			return nil, err
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
		res, err := a.postProvider(r.Context(), cfg, key, "/audio/transcriptions", mw.FormDataContentType(), buf.Bytes())
		if err == nil {
			res.Units = transcriptionUnits(res.Body, len(audio))
		}
		return res, err
	})
	if _, err := adm.complete(used, result, callErr); err != nil {
		writeGatewayError(w, err)
		return
	}
	result.admission = adm
	writeMediaResult(w, result)
}

func transcriptionUnits(raw []byte, audioBytes int) map[string]any {
	units := map[string]any{"audio_bytes": audioBytes}
	var body struct {
		Duration float64 `json:"duration"`
		Usage    struct {
			Type    string  `json:"type"`
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if body.Usage.Seconds > 0 {
			units["audio_seconds"] = body.Usage.Seconds
		} else if body.Duration > 0 {
			units["audio_seconds"] = body.Duration
		}
	}
	return units
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// ---- /v1/audio/speech (JSON in, audio out) ----

func (a *App) handleV1Speech(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	input, _ := body["input"].(string)
	if strings.TrimSpace(input) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}
	adm, err := a.admit(r.Context(), ctx, ident, gatewayRequest{
		Endpoint:       "audio.speech",
		Model:          strArg(body, "model"),
		EstimatedInput: estimateOutputTokens(int64(len(input))),
		RequestID:      requestIDFor(r, body),
	})
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	result, used, callErr := adm.run(body, func(attempt providerAttempt, cfg *ProviderConfig, key string, attemptBody map[string]any) (*chatResult, error) {
		if err := providerSupportsMedia(cfg); err != nil {
			return nil, err
		}
		delete(attemptBody, "request_id")
		attemptBody["model"] = upstreamModel(cfg.Provider, attempt.Model)
		payload, _ := json.Marshal(attemptBody)
		res, err := a.postProvider(r.Context(), cfg, key, "/audio/speech", "application/json", payload)
		if err == nil {
			res.Units = map[string]any{"characters": len([]rune(input)), "audio_bytes": len(res.Body)}
			if res.RequestTokens == 0 {
				res.UsageEstimated = true
			}
		}
		return res, err
	})
	if _, err := adm.complete(used, result, callErr); err != nil {
		writeGatewayError(w, err)
		return
	}
	result.admission = adm
	writeMediaResult(w, result)
}

// ---- /v1/images/generations ----

func (a *App) handleV1Images(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	prompt, _ := body["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	requested := int64Arg(body, "n")
	if requested <= 0 {
		requested = 1
	}
	adm, err := a.admit(r.Context(), ctx, ident, gatewayRequest{
		Endpoint:       "images.generations",
		Model:          strArg(body, "model"),
		EstimatedInput: estimateOutputTokens(int64(len(prompt))),
		RequestID:      requestIDFor(r, body),
	})
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	result, used, callErr := adm.run(body, func(attempt providerAttempt, cfg *ProviderConfig, key string, attemptBody map[string]any) (*chatResult, error) {
		if err := providerSupportsMedia(cfg); err != nil {
			return nil, err
		}
		delete(attemptBody, "request_id")
		attemptBody["model"] = upstreamModel(cfg.Provider, attempt.Model)
		payload, _ := json.Marshal(attemptBody)
		res, err := a.postProvider(r.Context(), cfg, key, "/images/generations", "application/json", payload)
		if err == nil {
			var out struct {
				Data []json.RawMessage `json:"data"`
			}
			_ = json.Unmarshal(res.Body, &out)
			if len(out.Data) == 0 {
				return res, providerError(http.StatusBadGateway, "provider response contains no images")
			}
			res.Units = map[string]any{"images": len(out.Data), "images_requested": requested}
			if size := strArg(body, "size"); size != "" {
				res.Units["size"] = size
			}
			if q := strArg(body, "quality"); q != "" {
				res.Units["quality"] = q
			}
		}
		return res, err
	})
	if _, err := adm.complete(used, result, callErr); err != nil {
		writeGatewayError(w, err)
		return
	}
	result.admission = adm
	writeMediaResult(w, result)
}
