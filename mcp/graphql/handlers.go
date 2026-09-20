package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST required", "method_not_allowed")
		return
	}
	project, err := a.projectFromRequest(r)
	if err != nil {
		writeGraphQLError(w, http.StatusBadRequest, err)
		return
	}
	apiSlug := apiSlugFromPath(r.URL.Path)
	apiLookupStart := time.Now()
	api, err := a.cachedAPI(project, apiSlug)
	if err != nil {
		writeGraphQLError(w, http.StatusNotFound, err)
		return
	}
	apiLookupDuration := time.Since(apiLookupStart)
	var req graphqlRequest
	reader := io.Reader(http.MaxBytesReader(w, r.Body, int64(maxRequestBytes(a.ctx))))
	if r.Method == http.MethodGet {
		if len(r.URL.RawQuery) > maxRequestBytes(a.ctx) {
			writeGraphQLError(w, 400, invalid("request exceeds size limit"))
			return
		}
		params := r.URL.Query()
		body := map[string]any{"query": params.Get("query"), "operationName": params.Get("operationName")}
		for _, key := range []string{"variables", "extensions"} {
			if value := params.Get(key); value != "" {
				var v map[string]any
				d := json.NewDecoder(strings.NewReader(value))
				d.UseNumber()
				if err := d.Decode(&v); err != nil {
					writeGraphQLError(w, 400, invalid("invalid %s", key))
					return
				}
				var trailing any
				if err := d.Decode(&trailing); err != io.EOF {
					writeGraphQLError(w, 400, invalid("invalid %s", key))
					return
				}
				body[key] = v
			}
		}
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeGraphQLError(w, http.StatusBadRequest, invalid("invalid GraphQL request: %v", err))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeGraphQLError(w, 400, invalid("request must contain one JSON object"))
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
	result, executeErr := a.execute(context.WithValue(r.Context(), requestMethodKey{}, r.Method), project, api.Slug, environment, req)
	w.Header().Add("Server-Timing", fmt.Sprintf("graphql_api;dur=%.3f, graphql_config;dur=%.3f, graphql_prepare;dur=%.3f, graphql_plan;dur=%.3f, graphql_source;dur=%.3f, graphql_execute;dur=%.3f, graphql_fast;desc=%q",
		milliseconds(apiLookupDuration), milliseconds(result.Timings.Config), milliseconds(result.Timings.Prepare), milliseconds(result.Timings.Plan), milliseconds(result.Timings.Source), milliseconds(result.Timings.Execute), fmt.Sprint(result.Timings.Fast)))
	status := http.StatusOK
	if executeErr != nil {
		status = http.StatusBadRequest
		if errorCode(executeErr) == "unauthenticated" {
			status = http.StatusUnauthorized
		}
		if errorCode(executeErr) == "permission_denied" {
			status = http.StatusForbidden
		}
		if errorCode(executeErr) == "method_not_allowed" {
			status = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "POST")
		}
		result.Errors = []map[string]any{{"message": executeErr.Error(), "extensions": map[string]any{"code": errorCode(executeErr)}}}
	}
	if len(result.Errors) > 0 && status == http.StatusOK {
		if !result.HasData {
			status = http.StatusBadRequest
		}
		for _, item := range result.Errors {
			extensions, _ := item["extensions"].(map[string]any)
			if extensions["code"] == "unauthenticated" {
				status = http.StatusUnauthorized
				break
			}
			if extensions["code"] == "permission_denied" {
				status = http.StatusForbidden
			}
		}
	}
	response := map[string]any{}
	if result.HasData || result.Data != nil {
		response["data"] = result.Data
	}
	if len(result.Errors) > 0 {
		response["errors"] = result.Errors
	}
	encoded, _ := json.Marshal(response)
	limits := defaultReleaseLimits()
	if release, _ := a.cachedActiveRelease(project, api.Slug, environment); release != nil {
		limits = release.Limits
	}
	if len(encoded) > limits.MaxResponseBytes {
		status = http.StatusRequestEntityTooLarge
		result.Errors = []map[string]any{{"message": "response exceeds configured size limit", "extensions": map[string]any{"code": "response_size_exceeded"}}}
		response = map[string]any{"errors": result.Errors}
		encoded, _ = json.Marshal(response)
	}
	requestID := r.Header.Get("X-Request-ID")
	if identity := securityIdentity(r.Context()); identity != nil {
		requestID = identity.RequestID
	}
	_ = a.logRequest(project, api.Slug, result, requestID, status, time.Since(start), len(encoded))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(encoded, '\n'))
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
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
			rows, err := listGraphQLAPIs(a.ctx.AppDB(), project)
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
		a.invalidateRuntime(project, row.Slug)
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
	if path == "security/validate" && r.Method == http.MethodPost {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := a.validateSecurity(ctx, project, api.Slug)
		if err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		writeJSON(w, result)
		return
	}
	if path == "security" {
		if r.Method == http.MethodGet {
			policy, err := getSecurity(a.ctx.AppReadDB(), project, api.Slug)
			if err != nil {
				writeJSONError(w, 500, "security unavailable", "storage_error")
				return
			}
			writeJSON(w, map[string]any{"security": policy, "endpoint": "/public/graphql/" + api.Slug})
			return
		}
		if r.Method == http.MethodPut {
			var body struct {
				Security json.RawMessage `json:"security"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			d.DisallowUnknownFields()
			if d.Decode(&body) != nil {
				writeJSONError(w, 400, "invalid security request", "invalid_request")
				return
			}
			policy, err := setSecurity(a.ctx.AppDB(), project, api.Slug, body.Security)
			if err != nil {
				writeJSONError(w, 400, err.Error(), errorCode(err))
				return
			}
			a.invalidateRuntime(project, api.Slug)
			writeJSON(w, map[string]any{"security": policy})
			return
		}
		writeJSONError(w, 405, "GET or PUT required", "method_not_allowed")
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
	if path == "modules" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		if r.Method == http.MethodGet {
			rows, err := listResolverModulesForAPI(a.ctx.AppReadDB(), project, api.Slug)
			if err != nil {
				writeJSONError(w, 500, err.Error(), "storage_error")
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, publicResolverModule(row))
			}
			writeJSON(w, map[string]any{"modules": out, "count": len(out)})
			return
		}
		var body struct {
			Name             string         `json:"name"`
			Version          int            `json:"version"`
			Description      string         `json:"description"`
			Inputs           map[string]any `json:"inputs"`
			OutputType       string         `json:"output_type"`
			Definition       map[string]any `json:"definition"`
			Deterministic    *bool          `json:"deterministic"`
			NullBehavior     string         `json:"null_behavior"`
			DecimalPrecision int            `json:"decimal_precision"`
			DecimalScale     int            `json:"decimal_scale"`
			RoundingMode     string         `json:"rounding_mode"`
			Timezone         string         `json:"timezone"`
			Completeness     string         `json:"completeness"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			writeJSONError(w, 400, err.Error(), "invalid_request")
			return
		}
		deterministic := true
		if body.Deterministic != nil {
			deterministic = *body.Deterministic
		}
		row, err := createResolverModuleForAPI(a.ctx.AppDB(), project, api.Slug, body.Name, body.Description, body.OutputType, body.Inputs, body.Definition, body.Version, deterministic)
		if err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		metadataArgs := map[string]any{}
		if body.NullBehavior != "" {
			metadataArgs["null_behavior"] = body.NullBehavior
		}
		if body.DecimalPrecision != 0 {
			metadataArgs["decimal_precision"] = body.DecimalPrecision
		}
		if body.DecimalScale != 0 {
			metadataArgs["decimal_scale"] = body.DecimalScale
		}
		if body.RoundingMode != "" {
			metadataArgs["rounding_mode"] = body.RoundingMode
		}
		if body.Timezone != "" {
			metadataArgs["timezone"] = body.Timezone
		}
		if body.Completeness != "" {
			metadataArgs["completeness"] = body.Completeness
		}
		metadata, err := parseModuleMetadata(metadataArgs)
		if err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		if err := setResolverModuleMetadata(a.ctx.AppDB(), row.ID, metadata); err != nil {
			writeJSONError(w, 500, err.Error(), "storage_error")
			return
		}
		row, _ = getResolverModuleForAPI(a.ctx.AppReadDB(), project, api.Slug, row.Name, row.Version, false)
		a.invalidateRuntime(project, api.Slug)
		writeJSON(w, map[string]any{"module": publicResolverModule(*row)})
		return
	}
	if path == "modules/validate" && r.Method == http.MethodPost {
		var body struct {
			Inputs     map[string]any `json:"inputs"`
			Definition map[string]any `json:"definition"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			writeJSONError(w, 400, err.Error(), "invalid_request")
			return
		}
		problems := validateResolverModuleDefinition(body.Inputs, body.Definition)
		writeJSON(w, map[string]any{"valid": len(problems) == 0, "validation_errors": problems, "dependencies": moduleCallDependencies(body.Definition)})
		return
	}
	if path == "modules/test" && r.Method == http.MethodPost {
		var body struct {
			Name    string         `json:"name"`
			Version int            `json:"version"`
			Inputs  map[string]any `json:"inputs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			writeJSONError(w, 400, err.Error(), "invalid_request")
			return
		}
		row, err := getResolverModuleForAPI(a.ctx.AppReadDB(), project, api.Slug, body.Name, body.Version, false)
		if err != nil || row == nil {
			writeJSONError(w, 404, "resolver module not found", "not_found")
			return
		}
		rows, err := listResolverModulesForAPI(a.ctx.AppReadDB(), project, api.Slug)
		if err != nil {
			writeJSONError(w, 500, err.Error(), "storage_error")
			return
		}
		modules := map[string]resolverModule{}
		for _, module := range rows {
			modules[moduleKey(module.Name, module.Version)] = module
		}
		value, err := (moduleRuntime{modules: modules}).evaluate(*row, body.Inputs)
		if err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		writeJSON(w, map[string]any{"value": value, "module": row.Name, "version": row.Version})
		return
	}
	if path == "modules/test-batch" && r.Method == http.MethodPost {
		var body struct {
			Name    string           `json:"name"`
			Version int              `json:"version"`
			Batch   []map[string]any `json:"batch"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil || len(body.Batch) > 10000 {
			writeJSONError(w, 400, "invalid module batch", "invalid_request")
			return
		}
		row, err := getResolverModuleForAPI(a.ctx.AppReadDB(), project, api.Slug, body.Name, body.Version, false)
		if err != nil || row == nil {
			writeJSONError(w, 404, "resolver module not found", "not_found")
			return
		}
		rows, err := listResolverModulesForAPI(a.ctx.AppReadDB(), project, api.Slug)
		if err != nil {
			writeJSONError(w, 500, err.Error(), "storage_error")
			return
		}
		modules := map[string]resolverModule{}
		for _, module := range rows {
			modules[moduleKey(module.Name, module.Version)] = module
		}
		values := make([]any, len(body.Batch))
		runtime := moduleRuntime{modules: modules}
		for i, input := range body.Batch {
			values[i], err = runtime.evaluate(*row, input)
			if err != nil {
				writeJSONError(w, 400, err.Error(), errorCode(err))
				return
			}
		}
		writeJSON(w, map[string]any{"values": values, "count": len(values), "module": publicResolverModule(*row)})
		return
	}
	if path == "modules/publish" && r.Method == http.MethodPost {
		var body struct {
			Name    string `json:"name"`
			Version int    `json:"version"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSONError(w, 400, err.Error(), "invalid_request")
			return
		}
		row, err := publishResolverModuleForAPI(a.ctx.AppDB(), project, api.Slug, body.Name, body.Version)
		if err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		a.invalidateRuntime(project, api.Slug)
		writeJSON(w, map[string]any{"module": publicResolverModule(*row), "published": true})
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
		a.invalidateRuntime(project, api.Slug)
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
		a.invalidateRuntime(project, api.Slug)
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
			Limits  any `json:"limits"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		if err := a.verifyPublishSecurity(r.Context(), project, api.Slug); err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		row, err := publishAPIRelease(a.ctx.AppDB(), project, api.Slug, normalizeEnvironment(r.URL.Query().Get("environment")), body.Version, body.Limits)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error(), errorCode(err))
			return
		}
		a.invalidateRuntime(project, api.Slug)
		writeJSON(w, map[string]any{"release": publicAPIRelease(row, true), "deployed": true})
		return
	}
	if path == "releases" && r.Method == http.MethodGet {
		rows, err := listAPIReleases(a.ctx.AppReadDB(), project, api.Slug, environment)
		if err != nil {
			writeJSONError(w, 500, err.Error(), "storage_error")
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for i := range rows {
			out = append(out, publicAPIRelease(&rows[i], false))
		}
		writeJSON(w, map[string]any{"releases": out, "count": len(out)})
		return
	}
	if path == "release" && r.Method == http.MethodGet {
		version, _ := strconv.Atoi(r.URL.Query().Get("version"))
		row, err := getAPIRelease(a.ctx.AppReadDB(), project, api.Slug, environment, version, version == 0)
		if err != nil || row == nil {
			writeJSONError(w, 404, "release not found", "not_found")
			return
		}
		writeJSON(w, map[string]any{"release": publicAPIRelease(row, true)})
		return
	}
	if path == "release/rollback" && r.Method == http.MethodPost {
		var body struct {
			Version int `json:"version"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil {
			writeJSONError(w, 400, "invalid rollback request", "invalid_request")
			return
		}
		if err := a.verifyPublishSecurity(r.Context(), project, api.Slug); err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		row, err := rollbackAPIRelease(a.ctx.AppDB(), project, api.Slug, environment, body.Version)
		if err != nil {
			writeJSONError(w, 400, err.Error(), errorCode(err))
			return
		}
		a.invalidateRuntime(project, api.Slug)
		writeJSON(w, map[string]any{"release": publicAPIRelease(row, true), "rolled_back": true})
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

func (a *App) logRequest(project, apiSlug string, result executeResult, requestID string, status int, duration time.Duration, responseBytes int) error {
	message := ""
	codes := []string{}
	if len(result.Errors) > 0 {
		if value, ok := result.Errors[0]["message"].(string); ok {
			message = value
		}
		for _, item := range result.Errors {
			if ext, ok := item["extensions"].(map[string]any); ok {
				if code, ok := ext["code"].(string); ok {
					codes = append(codes, code)
				}
			}
		}
	}
	timings, _ := json.Marshal(result.SourceTimings)
	encodedCodes, _ := json.Marshal(uniqueStrings(codes))
	a.enqueueRequestLog(requestLogEntry{
		projectID:     storageProject(project, apiSlug),
		operationName: result.OperationName,
		operationType: result.OperationType,
		status:        status,
		durationMS:    duration.Milliseconds(),
		errorMessage:  message,
		createdAt:     nowUTC(),
		operationHash: result.OperationHash, apiRelease: result.Release, responseBytes: responseBytes, rowCount: result.Rows, resolverCount: result.Resolvers, sourceTimings: string(timings), errorCodes: string(encodedCodes), authorizationScope: result.AuthScope, requestID: requestID,
	})
	return nil
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
