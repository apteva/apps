package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type prospectingCapabilities struct {
	Web bool `json:"web"`
	CRM bool `json:"crm"`
}

func capabilitiesFor(ctx *sdk.AppCtx) prospectingCapabilities {
	capabilities := prospectingCapabilities{}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return capabilities
	}
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || identity == nil {
		return capabilities
	}
	capabilities.Web = appBindingPresent(identity.Bindings["web"])
	capabilities.CRM = appBindingPresent(identity.Bindings["crm"])
	return capabilities
}

func appBindingPresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case int:
		return typed > 0
	case int64:
		return typed > 0
	case float64:
		return typed > 0
	case json.Number:
		value, _ := typed.Int64()
		return value > 0
	case string:
		value, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return value > 0
	case map[string]any:
		if appBindingPresent(typed["default_id"]) {
			return true
		}
		if values, ok := typed["ids"].([]any); ok {
			for _, item := range values {
				if appBindingPresent(item) {
					return true
				}
			}
		}
	case []any:
		for _, item := range typed {
			if appBindingPresent(item) {
				return true
			}
		}
	}
	return false
}

func requireOptionalApp(ctx *sdk.AppCtx, name string) error {
	capabilities := capabilitiesFor(ctx)
	available := (name == "web" && capabilities.Web) || (name == "crm" && capabilities.CRM)
	if available {
		return nil
	}
	display := strings.ToUpper(name)
	if name == "web" {
		display = "Web"
	}
	return fmt.Errorf("%s integration unavailable: connect the optional %s app to enable this action", display, display)
}

func (a *App) toolCapabilities(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return capabilitiesFor(ctx), nil
}

func (a *App) toolCandidatesImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	profile, createdProfile, err := resolveSeedProfile(ctx, int64Arg(args, "profile_id"))
	if err != nil {
		return nil, err
	}
	rows, err := candidateImportRows(args)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("no candidate rows found")
	}
	if len(rows) > 1000 {
		return nil, errors.New("imports are limited to 1000 candidates per request")
	}
	imported, duplicates, skipped := 0, 0, 0
	rowErrors := []map[string]any{}
	for index, row := range rows {
		input, inputErr := candidateInputFromArgs(row)
		if inputErr != nil {
			skipped++
			if len(rowErrors) < 25 {
				rowErrors = append(rowErrors, map[string]any{"row": index + 1, "error": inputErr.Error()})
			}
			continue
		}
		input.ProfileID = profile.ID
		input.Source = "import"
		candidate, created, insertErr := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), input, profile)
		if insertErr != nil {
			skipped++
			if len(rowErrors) < 25 {
				rowErrors = append(rowErrors, map[string]any{"row": index + 1, "error": insertErr.Error()})
			}
			continue
		}
		if !created {
			duplicates++
			continue
		}
		imported++
		ctx.EmitWithProject("prospecting.candidate.created", candidate.ProjectID, candidateEvent(candidate))
	}
	return map[string]any{
		"profile": profile, "profile_created": createdProfile, "rows": len(rows),
		"imported": imported, "duplicates": duplicates, "skipped": skipped, "errors": rowErrors,
	}, nil
}

func (a *App) toolCandidatesExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := clamp(intArg(args, "limit", 200), 1, 200)
	candidates, total, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{
		ProfileID: int64Arg(args, "profile_id"), Status: stringArg(args, "status"), Q: stringArg(args, "q"), Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"candidates": candidates, "count": len(candidates), "total": total, "truncated": len(candidates) < total}, nil
}

func resolveSeedProfile(ctx *sdk.AppCtx, profileID int64) (*TargetProfile, bool, error) {
	if profileID > 0 {
		profile, err := getProfile(ctx.AppDB(), ctx.CurrentProject(), profileID)
		if err != nil {
			return nil, false, err
		}
		if profile == nil {
			return nil, false, errors.New("target profile not found")
		}
		return profile, false, nil
	}
	profiles, err := listProfiles(ctx.AppDB(), ctx.CurrentProject(), "active")
	if err != nil {
		return nil, false, err
	}
	if len(profiles) > 0 {
		return &profiles[0], false, nil
	}
	profile, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{
		"name": "Imported leads", "description": "Standalone leads seeded manually or by import.",
	})
	return profile, err == nil, err
}

func candidateImportRows(args map[string]any) ([]map[string]any, error) {
	if raw, ok := args["candidates"]; ok && raw != nil {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		if err := json.Unmarshal(encoded, &rows); err != nil {
			return nil, errors.New("candidates must be an array of objects")
		}
		return rows, nil
	}
	data := strings.TrimSpace(stringArg(args, "data"))
	if data == "" {
		return nil, errors.New("data or candidates is required")
	}
	format := strings.ToLower(stringArg(args, "format"))
	if format == "" || format == "auto" {
		if strings.HasPrefix(data, "[") || strings.HasPrefix(data, "{") {
			format = "json"
		} else {
			format = "csv"
		}
	}
	switch format {
	case "json":
		var rows []map[string]any
		if strings.HasPrefix(data, "{") {
			var envelope struct {
				Candidates []map[string]any `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(data), &envelope); err != nil {
				return nil, fmt.Errorf("parse JSON: %w", err)
			}
			rows = envelope.Candidates
		} else if err := json.Unmarshal([]byte(data), &rows); err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
		}
		return rows, nil
	case "csv":
		return parseCandidateCSV(data)
	default:
		return nil, errors.New("format must be auto, csv, or json")
	}
}

func parseCandidateCSV(data string) ([]map[string]any, error) {
	reader := csv.NewReader(strings.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header: %w", err)
	}
	for i := range header {
		header[i] = normalizeImportHeader(strings.TrimPrefix(header[i], "\ufeff"))
	}
	rows := []map[string]any{}
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read CSV row %d: %w", len(rows)+2, readErr)
		}
		row := map[string]any{}
		for index, value := range record {
			if index >= len(header) || header[index] == "" {
				continue
			}
			row[header[index]] = strings.TrimSpace(value)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func normalizeImportHeader(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(value)
	aliases := map[string]string{
		"company": "company_name", "name": "company_name", "domain": "company_domain", "url": "website",
		"first_name": "person_first_name", "last_name": "person_last_name", "contact": "person_display_name",
		"contact_name": "person_display_name", "person_name": "person_display_name", "title": "job_title",
		"notes": "summary", "source": "source_url",
	}
	if canonical := aliases[value]; canonical != "" {
		return canonical
	}
	return value
}

func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.toolCapabilities(requestCtx(r), nil)
	respond(w, out, err)
}

func (a *App) handleCandidateImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	args, err := decodeBody(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	out, err := a.toolCandidatesImport(requestCtx(r), args)
	respond(w, out, err)
}

func (a *App) handleCandidateExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx := requestCtx(r)
	filter := candidateFilter{
		ProfileID: queryInt64(r, "profile_id"), Status: r.URL.Query().Get("status"), Q: r.URL.Query().Get("q"), Limit: 200,
	}
	candidates := []Candidate{}
	for {
		filter.Offset = len(candidates)
		page, total, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), filter)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		candidates = append(candidates, page...)
		if len(candidates) >= total || len(candidates) >= 10000 {
			break
		}
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="prospecting-leads.json"`)
		_ = json.NewEncoder(w).Encode(candidates)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="prospecting-leads.csv"`)
	writer := csv.NewWriter(w)
	header := []string{"id", "profile_id", "company_name", "company_domain", "website", "person_first_name", "person_last_name", "person_display_name", "job_title", "email", "phone", "location", "summary", "fit_score", "confidence_score", "status", "source", "source_url", "created_at", "updated_at"}
	_ = writer.Write(header)
	for _, candidate := range candidates {
		_ = writer.Write([]string{
			strconv.FormatInt(candidate.ID, 10), strconv.FormatInt(candidate.ProfileID, 10), candidate.CompanyName, candidate.CompanyDomain,
			candidate.Website, candidate.PersonFirstName, candidate.PersonLastName, candidate.PersonDisplayName, candidate.JobTitle,
			candidate.Email, candidate.Phone, candidate.Location, candidate.Summary, strconv.Itoa(candidate.FitScore),
			strconv.Itoa(candidate.ConfidenceScore), candidate.Status, candidate.Source, candidate.SourceURL, candidate.CreatedAt, candidate.UpdatedAt,
		})
	}
	writer.Flush()
}
