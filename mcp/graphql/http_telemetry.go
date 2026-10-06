package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type requestTelemetryKey struct{}
type requestTelemetry struct {
	result      executeResult
	hasResult   bool
	environment string
	phases      map[string]float64
}

func requestTelemetryFrom(r *http.Request) *requestTelemetry {
	t, _ := r.Context().Value(requestTelemetryKey{}).(*requestTelemetry)
	return t
}

type telemetryWriter struct {
	http.ResponseWriter
	telemetry     *requestTelemetry
	status, bytes int
	earlyBody     bytes.Buffer
}

func (w *telemetryWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *telemetryWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if !w.telemetry.hasResult && w.earlyBody.Len()+len(body) <= 64*1024 {
		_, _ = w.earlyBody.Write(body)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}
func (w *telemetryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// One observation spans authentication, decoding, execution and response writing.
// Nested calls from the public route reuse it; no request bodies or tokens are stored.
func (a *App) observeGraphQL(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	start := time.Now()
	telemetry := &requestTelemetry{phases: map[string]float64{}}
	r = r.WithContext(context.WithValue(r.Context(), requestTelemetryKey{}, telemetry))
	writer := &telemetryWriter{ResponseWriter: w, telemetry: telemetry}
	requestID := uuid.NewString()
	w.Header().Set("X-Request-ID", requestID)
	defer func() {
		panicValue := recover()
		defer func() {
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		project, _ := a.projectFromRequest(r)
		if project == "" && a.ctx != nil {
			project = a.ctx.CurrentProject()
		}
		if project == "" {
			return
		}
		slug := apiSlugFromPath(r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/public/graphql/") {
			slug = strings.TrimPrefix(r.URL.Path, "/public/graphql/")
		}
		if validateAPISlug(slug) != nil {
			slug = "default"
		}
		result := telemetry.result
		if !telemetry.hasResult {
			var response struct {
				Errors []map[string]any `json:"errors"`
				Error  string           `json:"error"`
				Code   string           `json:"code"`
			}
			_ = json.Unmarshal(writer.earlyBody.Bytes(), &response)
			result.Errors = response.Errors
			if len(result.Errors) == 0 && response.Error != "" {
				result.Errors = []map[string]any{{"message": response.Error, "extensions": map[string]any{"code": response.Code}}}
			}
		}
		if panicValue != nil {
			writer.status = http.StatusInternalServerError
			result.Errors = append(result.Errors, map[string]any{"message": "internal GraphQL handler failure", "extensions": map[string]any{"code": "internal_error"}})
		}
		if writer.status == 0 {
			writer.status = http.StatusOK
		}
		telemetry.phases["total"] = milliseconds(time.Since(start))
		// Reuse the standard error/code extraction and then enrich the queued entry.
		entry := makeRequestLog(project, slug, result, w.Header().Get("X-Request-ID"), writer.status, time.Since(start), writer.bytes)
		entry.environment = telemetry.environment
		phases, _ := json.Marshal(telemetry.phases)
		entry.timings = string(phases)
		a.enqueueRequestLog(entry)
	}()
	next(writer, r)
}
