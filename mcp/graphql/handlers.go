package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) projectFromRequest(r *http.Request) (string, error) {
	project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if project == "" {
		project = strings.TrimSpace(r.URL.Query().Get("project_id"))
	}
	if a.ctx != nil && a.ctx.CurrentProject() != "" {
		if project != "" && project != a.ctx.CurrentProject() {
			return "", forbidden("project override is not allowed")
		}
		return a.ctx.CurrentProject(), nil
	}
	if project == "" {
		return "", invalid("project_id is required")
	}
	return project, nil
}

func (a *App) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required", "method_not_allowed")
		return
	}
	project, err := a.projectFromRequest(r)
	if err != nil {
		writeGraphQLError(w, http.StatusBadRequest, err)
		return
	}
	apiSlug := apiSlugFromPath(r.URL.Path)
	api, err := resolveGraphQLAPI(a.ctx.AppDB(), project, apiSlug)
	if err != nil {
		writeGraphQLError(w, http.StatusNotFound, err)
		return
	}
	var req graphqlRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(maxRequestBytes(a.ctx))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeGraphQLError(w, http.StatusBadRequest, invalid("invalid GraphQL request: %v", err))
		return
	}
	if queryProject := strings.TrimSpace(r.URL.Query().Get("project_id")); queryProject != "" && queryProject != project {
		writeGraphQLError(w, http.StatusForbidden, forbidden("project override is not allowed"))
		return
	}
	environment := normalizeEnvironment(req.Environment)
	if queryEnv := strings.TrimSpace(r.URL.Query().Get("environment")); queryEnv != "" {
		environment = normalizeEnvironment(queryEnv)
	}
	if headerEnv := strings.TrimSpace(r.Header.Get("X-GraphQL-Environment")); headerEnv != "" {
		environment = normalizeEnvironment(headerEnv)
	}
	start := time.Now()
	result, executeErr := a.execute(r.Context(), project, api.Slug, environment, req)
	status := http.StatusOK
	if executeErr != nil {
		status = http.StatusBadRequest
		result.Errors = []map[string]any{{"message": executeErr.Error(), "extensions": map[string]any{"code": errorCode(executeErr)}}}
	}
	if len(result.Errors) > 0 && status == http.StatusOK {
		status = http.StatusBadRequest
	}
	_ = a.logRequest(project, api.Slug, result.OperationName, result.OperationType, status, time.Since(start), result.Errors)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	response := map[string]any{}
	if result.Data != nil {
		response["data"] = result.Data
	}
	if len(result.Errors) > 0 {
		response["errors"] = result.Errors
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (a *App) handleAdminHTTP(w http.ResponseWriter, r *http.Request) {
	project, err := a.projectFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	environment := normalizeEnvironment(r.URL.Query().Get("environment"))
	if path == "apis" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		if r.Method == http.MethodGet {
			rows, err := listGraphQLAPIs(a.ctx.AppReadDB(), project)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error(), "storage_error")
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, publicAPI(row))
			}
			writeJSON(w, map[string]any{"apis": out, "count": len(out)})
			return
		}
		var body struct {
			Slug        string `json:"slug"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		row, err := createGraphQLAPI(a.ctx.AppDB(), project, body.Slug, body.Name, body.Description)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
			return
		}
		writeJSON(w, map[string]any{"api": publicAPI(*row)})
		return
	}
	apiSlug := r.URL.Query().Get("api_slug")
	if apiSlug == "" {
		apiSlug = r.URL.Query().Get("api")
	}
	api, err := resolveGraphQLAPI(a.ctx.AppDB(), project, apiSlug)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error(), errorCode(err))
		return
	}
	if path == "schemas" && r.Method == http.MethodGet {
		rows, err := listSchemasForAPI(a.ctx.AppReadDB(), project, api.Slug, environment)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error(), "storage_error")
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for i := range rows {
			out = append(out, publicSchema(&rows[i]))
		}
		writeJSON(w, map[string]any{"schemas": out, "count": len(out)})
		return
	}
	if path == "schema/validate" && r.Method == http.MethodPost {
		var body struct {
			SDL string `json:"sdl"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		_, validationErrors := validateSDL(body.SDL)
		writeJSON(w, map[string]any{"valid": len(validationErrors) == 0, "validation_errors": validationErrors})
		return
	}
	if path == "sources" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		if r.Method == http.MethodGet {
			rows, err := listSourcesForAPI(a.ctx.AppReadDB(), project, api.Slug)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error(), "storage_error")
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, publicSource(row))
			}
			writeJSON(w, map[string]any{"sources": out, "count": len(out)})
			return
		}
		var body struct {
			Name   string         `json:"name"`
			Kind   string         `json:"kind"`
			Config map[string]any `json:"config"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		row, err := createSourceForAPI(a.ctx.AppDB(), project, api.Slug, body.Name, body.Kind, body.Config)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
			return
		}
		writeJSON(w, map[string]any{"source": publicSource(*row)})
		return
	}
	if path == "resolvers" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		if r.Method == http.MethodGet {
			rows, err := listResolversForAPI(a.ctx.AppReadDB(), project, api.Slug)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error(), "storage_error")
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				source, _ := getSourceForAPI(a.ctx.AppReadDB(), project, api.Slug, row.SourceID, "")
				out = append(out, publicResolver(row, source))
			}
			writeJSON(w, map[string]any{"resolvers": out, "count": len(out)})
			return
		}
		var body struct {
			ParentType string         `json:"parent_type"`
			FieldName  string         `json:"field_name"`
			SourceID   int64          `json:"source_id"`
			Source     string         `json:"source"`
			Operation  string         `json:"operation"`
			Config     map[string]any `json:"config"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		if body.SourceID == 0 && strings.TrimSpace(body.Source) != "" {
			source, findErr := getSourceForAPI(a.ctx.AppReadDB(), project, api.Slug, 0, strings.TrimSpace(body.Source))
			if findErr != nil {
				writeJSONError(w, http.StatusInternalServerError, findErr.Error(), "storage_error")
				return
			}
			if source == nil {
				writeJSONError(w, http.StatusBadRequest, "source not found", "invalid_request")
				return
			}
			body.SourceID = source.ID
		}
		row, err := upsertResolverForAPI(a.ctx.AppDB(), project, api.Slug, body.ParentType, body.FieldName, body.Operation, body.SourceID, body.Config)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
			return
		}
		source, _ := getSourceForAPI(a.ctx.AppReadDB(), project, api.Slug, row.SourceID, "")
		writeJSON(w, map[string]any{"resolver": publicResolver(*row, source)})
		return
	}
	if path == "schema" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		if r.Method == http.MethodGet {
			row, err := getSchemaForAPI(a.ctx.AppReadDB(), project, api.Slug, environment, 0, false)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error(), "storage_error")
				return
			}
			if row == nil {
				writeJSONError(w, http.StatusNotFound, "published schema not found", "not_found")
				return
			}
			writeJSON(w, map[string]any{"schema": publicSchema(row)})
			return
		}
		var body struct {
			SDL string `json:"sdl"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		row, validationErrors, createErr := createSchemaForAPI(a.ctx.AppDB(), project, api.Slug, environment, body.SDL, 0)
		if createErr != nil && row == nil && len(validationErrors) == 0 {
			writeJSONError(w, http.StatusBadRequest, createErr.Error(), errorCode(createErr))
			return
		}
		writeJSON(w, map[string]any{"schema": publicSchema(row), "valid": len(validationErrors) == 0, "validation_errors": validationErrors})
		return
	}
	if path == "schema/publish" && r.Method == http.MethodPost {
		var body struct {
			Version int `json:"version"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		row, err := publishSchemaForAPI(a.ctx.AppDB(), project, api.Slug, normalizeEnvironment(r.URL.Query().Get("environment")), body.Version)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
			return
		}
		writeJSON(w, map[string]any{"schema": publicSchema(row), "deployed": true})
		return
	}
	if path == "logs" && r.Method == http.MethodGet {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := publicLogs(a.ctx.AppReadDB(), storageProject(project, api.Slug), limit)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error(), "storage_error")
			return
		}
		writeJSON(w, map[string]any{"logs": rows, "count": len(rows)})
		return
	}
	if path == "events" && r.Method == http.MethodPost {
		var body struct {
			Topic   string         `json:"topic"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		if strings.TrimSpace(body.Topic) == "" {
			writeJSONError(w, http.StatusBadRequest, "topic is required", "invalid_request")
			return
		}
		count := a.hub.publishForAPI(project, api.Slug, body.Topic, body.Payload)
		writeJSON(w, map[string]any{"published": true, "topic": body.Topic, "delivered": count})
		return
	}
	writeJSONError(w, http.StatusNotFound, "not found", "not_found")
}

func (a *App) logRequest(project, apiSlug, operationName, operationType string, status int, duration time.Duration, errors []map[string]any) error {
	message := ""
	if len(errors) > 0 {
		if value, ok := errors[0]["message"].(string); ok {
			message = value
		}
	}
	_, err := a.ctx.AppDB().Exec(`INSERT INTO graphql_request_logs(project_id,operation_name,operation_type,status_code,duration_ms,error,created_at) VALUES(?,?,?,?,?,?,?)`, storageProject(project, apiSlug), operationName, operationType, status, duration.Milliseconds(), message, nowUTC())
	return err
}

func writeGraphQLError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": err.Error(), "extensions": map[string]any{"code": errorCode(err)}}}})
}

func writeJSONError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "code": code})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func maxRequestBytes(ctx *sdk.AppCtx) int {
	if ctx != nil {
		if value := ctx.Config()["max_request_bytes"]; value != "" {
			if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 16<<20 {
				return n
			}
		}
	}
	return 1 << 20
}

func statusCodeFor(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return http.StatusBadRequest
}

func formatContextError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("request canceled: %w", ctx.Err())
	}
	return err
}
