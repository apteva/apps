package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	avatarCreatePollInterval    = 20 * time.Second
	maxAvatarCreatePollAttempts = 180 // 180 × 20s = 60 minutes
)

type avatarCreateJob struct {
	ID               int64  `json:"id"`
	ProjectID        string `json:"project_id"`
	Provider         string `json:"provider"`
	SourceType       string `json:"source_type"`
	Name             string `json:"name"`
	ProviderJobID    string `json:"provider_job_id"`
	ProviderAvatarID string `json:"provider_avatar_id"`
	ProviderGroupID  string `json:"provider_group_id"`
	SourceRef        string `json:"source_ref"`
	ConsentRef       string `json:"consent_ref"`
	Status           string `json:"status"`
	Error            string `json:"error"`
	Attempts         int    `json:"attempts"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type avatarCreateResult struct {
	ProviderJobID    string
	ProviderAvatarID string
	ProviderGroupID  string
	Status           string
	Error            string
}

func (a *App) toolMediaAvatarCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	if ctx == nil {
		return nil, errors.New("app not mounted")
	}
	pid := projectScopeFromArgs(ctx, args)
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	name := strings.TrimSpace(strArg(args, "name", ""))
	if name == "" {
		return nil, errors.New("name required")
	}
	sourceType := strings.TrimSpace(strArg(args, "source_type", "photo"))
	if sourceType == "" {
		sourceType = "photo"
	}
	if sourceType != "photo" && sourceType != "prompt" && sourceType != "video" {
		return mcpError("unsupported source_type: " + sourceType), nil
	}
	bound := ctx.IntegrationFor("avatar_provider")
	if bound == nil {
		return mcpError("no avatar_provider bound — pick HeyGen or Tavus in app settings"), nil
	}
	prepared := copyMap(args)
	prepared["source_type"] = sourceType
	prepared["name"] = name
	sourceRef := ""
	consentRef := safeAvatarSourceRef(strings.TrimSpace(strArg(args, "consent_video", "")))
	if sourceType == "photo" {
		rawSource := strings.TrimSpace(strArg(args, "source_image", ""))
		if rawSource == "" {
			return nil, errors.New("source_image required for source_type=photo")
		}
		resolved, err := resolveSourceImage(ctx, rawSource)
		if err != nil {
			return mcpError("source_image: " + err.Error()), nil
		}
		sourceRef = safeAvatarSourceRef(rawSource)
		prepared["source_image"] = resolved
		prepared["_source_ref"] = sourceRef
	} else if sourceType == "prompt" {
		if strings.TrimSpace(strArg(args, "prompt", "")) == "" {
			return nil, errors.New("prompt required for source_type=prompt")
		}
	} else if sourceType == "video" {
		rawSource := strings.TrimSpace(strArg(args, "source_video", ""))
		if rawSource == "" {
			return nil, errors.New("source_video required for source_type=video")
		}
		sourceRef = safeAvatarSourceRef(rawSource)
		prepared["_source_ref"] = sourceRef
	}

	providerArgs, err := buildAvatarCreateArgs(prepared, bound.AppSlug)
	if err != nil {
		return mcpError("build args: " + err.Error()), nil
	}
	tool := avatarCreateToolForSlug(bound.AppSlug)
	if tool == "" {
		return mcpError("avatar creation is not wired for provider " + bound.AppSlug), nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, providerArgs)
	if err != nil {
		return mcpError("provider call failed: " + err.Error()), nil
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return mcpError("provider returned non-2xx: " + body), nil
	}
	normalized, err := normalizeAvatarCreateResponse(bound.AppSlug, res.Data)
	if err != nil {
		return mcpError("provider response parse: " + err.Error()), nil
	}
	if normalized.Status == "" {
		normalized.Status = "training"
	}
	job := avatarCreateJob{
		ProjectID:        pid,
		Provider:         bound.AppSlug,
		SourceType:       sourceType,
		Name:             name,
		ProviderJobID:    normalized.ProviderJobID,
		ProviderAvatarID: normalized.ProviderAvatarID,
		ProviderGroupID:  normalized.ProviderGroupID,
		SourceRef:        sourceRef,
		ConsentRef:       consentRef,
		Status:           normalized.Status,
		Error:            normalized.Error,
	}
	jobID, err := insertAvatarCreateJob(ctx, job, sanitizedAvatarCreateJSON(args))
	if err != nil {
		return mcpError("avatar created at provider but local tracking row failed: " + err.Error()), nil
	}
	job.ID = jobID
	if _, err := upsertMediaIdentityFromAvatarJob(ctx, job); err != nil {
		ctx.Logger().Warn("avatar identity upsert failed", "job_id", jobID, "err", err)
	}
	eventTopic := "avatar.create_queued"
	if normalized.Status == "completed" {
		eventTopic = "avatar.created"
	}
	ctx.EmitWithProject(eventTopic, pid, map[string]any{
		"job_id": jobID, "provider": bound.AppSlug, "avatar_id": normalized.ProviderAvatarID,
	})
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": fmt.Sprintf("Avatar creation started via %s. Job #%d (%s).", bound.AppSlug, jobID, normalized.Status)},
		},
		"_meta": map[string]any{
			"kind":               "avatar_create",
			"status":             normalized.Status,
			"job_id":             jobID,
			"provider":           bound.AppSlug,
			"source_type":        sourceType,
			"provider_job_id":    normalized.ProviderJobID,
			"provider_avatar_id": normalized.ProviderAvatarID,
			"provider_group_id":  normalized.ProviderGroupID,
		},
	}, nil
}

func avatarCreateToolForSlug(slug string) string {
	switch slug {
	case "heygen":
		return "create_avatar"
	case "tavus":
		return "create_replica"
	default:
		return ""
	}
}

func buildAvatarCreateArgs(args map[string]any, providerSlug string) (map[string]any, error) {
	switch providerSlug {
	case "heygen":
		return buildHeyGenAvatarCreateArgs(args)
	case "tavus":
		return buildTavusReplicaCreateArgs(args)
	default:
		return nil, fmt.Errorf("unsupported avatar provider slug: %q", providerSlug)
	}
}

func buildHeyGenAvatarCreateArgs(args map[string]any) (map[string]any, error) {
	sourceType := strArg(args, "source_type", "photo")
	name := strings.TrimSpace(strArg(args, "name", ""))
	out := map[string]any{"type": sourceType, "name": name}
	opts, _ := args["options"].(map[string]any)
	switch sourceType {
	case "photo":
		src := strings.TrimSpace(strArg(args, "source_image", ""))
		if src == "" {
			return nil, errors.New("source_image required")
		}
		out["file"] = heygenAssetInput(src)
	case "prompt":
		prompt := strings.TrimSpace(strArg(args, "prompt", ""))
		if prompt == "" {
			return nil, errors.New("prompt required")
		}
		out["prompt"] = prompt
		if opts != nil {
			if v, ok := opts["reference_images"]; ok {
				out["reference_images"] = v
			}
			if v := strArg(opts, "avatar_group_id", ""); v != "" {
				out["avatar_group_id"] = v
			}
		}
	case "video":
		return nil, errors.New("heygen video/digital-twin creation is account-gated and not enabled in Media Studio yet")
	default:
		return nil, fmt.Errorf("unsupported source_type %q", sourceType)
	}
	if opts != nil {
		for _, k := range []string{"avatar_group_id"} {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out, nil
}

func buildTavusReplicaCreateArgs(args map[string]any) (map[string]any, error) {
	sourceType := strArg(args, "source_type", "photo")
	name := strings.TrimSpace(strArg(args, "name", ""))
	out := map[string]any{"replica_name": name}
	opts, _ := args["options"].(map[string]any)
	switch sourceType {
	case "photo":
		src := strings.TrimSpace(strArg(args, "source_image", ""))
		if !looksLikeHTTPURL(src) {
			return nil, errors.New("Tavus image training requires source_image as a direct HTTPS URL")
		}
		out["train_image_url"] = src
		voiceName := strArg(args, "voice", "")
		if voiceName == "" && opts != nil {
			voiceName = strArg(opts, "voice_name", "")
		}
		if voiceName == "" {
			return nil, errors.New("Tavus image training requires voice or options.voice_name")
		}
		out["voice_name"] = voiceName
	case "video":
		src := strings.TrimSpace(strArg(args, "source_video", ""))
		if !looksLikeHTTPURL(src) {
			return nil, errors.New("Tavus video training requires source_video as a direct HTTPS URL")
		}
		out["train_video_url"] = src
		if consent := strings.TrimSpace(strArg(args, "consent_video", "")); consent != "" {
			if !looksLikeHTTPURL(consent) {
				return nil, errors.New("Tavus consent_video must be a direct HTTPS URL")
			}
			out["consent_video_url"] = consent
		}
	case "prompt":
		return nil, errors.New("Tavus does not support prompt avatar creation")
	}
	if opts != nil {
		for _, k := range []string{"callback_url", "model_name", "auto_fix_training_image"} {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out, nil
}

func normalizeAvatarCreateResponse(slug string, raw json.RawMessage) (avatarCreateResult, error) {
	switch slug {
	case "heygen":
		var body struct {
			Data struct {
				AvatarItem struct {
					ID      string `json:"id"`
					GroupID string `json:"group_id"`
					Status  string `json:"status"`
					Error   struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"avatar_item"`
				AvatarGroup struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Error  struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"avatar_group"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return avatarCreateResult{}, err
		}
		status := normalizeAvatarCreateStatus(firstNonEmpty(body.Data.AvatarItem.Status, body.Data.AvatarGroup.Status))
		errMsg := firstNonEmpty(body.Data.AvatarItem.Error.Message, body.Data.AvatarGroup.Error.Message)
		return avatarCreateResult{
			ProviderJobID:    firstNonEmpty(body.Data.AvatarItem.ID, body.Data.AvatarGroup.ID),
			ProviderAvatarID: body.Data.AvatarItem.ID,
			ProviderGroupID:  firstNonEmpty(body.Data.AvatarItem.GroupID, body.Data.AvatarGroup.ID),
			Status:           status,
			Error:            errMsg,
		}, nil
	case "tavus":
		var body struct {
			ReplicaID string `json:"replica_id"`
			Status    string `json:"status"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return avatarCreateResult{}, err
		}
		if body.ReplicaID == "" {
			return avatarCreateResult{}, fmt.Errorf("tavus create response missing replica_id: %s", truncate(string(raw), 200))
		}
		return avatarCreateResult{
			ProviderJobID:    body.ReplicaID,
			ProviderAvatarID: body.ReplicaID,
			Status:           normalizeAvatarCreateStatus(body.Status),
			Error:            body.Error,
		}, nil
	}
	return avatarCreateResult{}, fmt.Errorf("unsupported avatar provider slug: %q", slug)
}

func normalizeAvatarCreateStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "ready", "success":
		return "completed"
	case "failed", "failure", "error", "rejected":
		return "failed"
	case "queued", "pending", "started", "processing", "training", "":
		return "training"
	default:
		return "training"
	}
}

func (a *App) handleAvatarCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	bound := globalCtx.IntegrationFor("avatar_provider")
	resp := map[string]any{"bound": false, "source_types": []string{}}
	if bound != nil {
		resp["bound"] = true
		resp["provider"] = bound.AppSlug
		switch bound.AppSlug {
		case "heygen":
			resp["source_types"] = []string{"photo", "prompt"}
			resp["notes"] = "Digital Twin/video creation is account-gated; photo avatar creation is available here."
		case "tavus":
			resp["source_types"] = []string{"photo", "video"}
			resp["notes"] = "Photo creation requires a direct image URL and voice_name; video creation requires direct video URLs and consent handling."
		}
	}
	writeJSON(w, resp, nil)
}

func (a *App) handleAvatarCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" && projectArg(body) == "" {
		body["project_id"] = pid
	}
	pid := projectScopeFromArgs(globalCtx, body)
	if pid == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	body["_project_id"] = pid
	out, err := a.toolMediaAvatarCreate(globalCtx.WithProject(pid), body)
	writeJSON(w, out, err)
}

func (a *App) handleListAvatarCreateJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jobs, err := queryAvatarCreateJobs(globalCtx, pid, r.URL.Query().Get("status"))
	writeJSON(w, map[string]any{"jobs": jobs}, err)
}

func insertAvatarCreateJob(ctx *sdk.AppCtx, j avatarCreateJob, requestJSON string) (int64, error) {
	res, err := ctx.AppDB().Exec(
		`INSERT INTO avatar_create_jobs
		 (project_id, provider, source_type, name, provider_job_id,
		  provider_avatar_id, provider_group_id, source_ref, consent_ref,
		  request_json, status, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ProjectID, j.Provider, j.SourceType, j.Name, j.ProviderJobID,
		j.ProviderAvatarID, j.ProviderGroupID, j.SourceRef, j.ConsentRef,
		requestJSON, j.Status, j.Error,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func queryAvatarCreateJobs(ctx *sdk.AppCtx, pid, statusFilter string) ([]avatarCreateJob, error) {
	q := `SELECT id, project_id, provider, source_type, name, provider_job_id,
	             provider_avatar_id, provider_group_id, source_ref, consent_ref,
	             status, error, attempts, created_at, updated_at
	      FROM avatar_create_jobs
	      WHERE project_id = ?`
	args := []any{pid}
	if statusFilter != "" {
		q += ` AND status = ?`
		args = append(args, statusFilter)
	} else {
		q += ` AND (status IN ('queued','training','failed') OR updated_at > datetime('now','-1 day'))`
	}
	q += ` ORDER BY id DESC LIMIT 100`
	rows, err := ctx.AppDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []avatarCreateJob{}
	for rows.Next() {
		var j avatarCreateJob
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.Provider, &j.SourceType, &j.Name,
			&j.ProviderJobID, &j.ProviderAvatarID, &j.ProviderGroupID, &j.SourceRef,
			&j.ConsentRef, &j.Status, &j.Error, &j.Attempts, &j.CreatedAt, &j.UpdatedAt); err != nil {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func (a *App) avatarCreatePollWorker(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(
		`SELECT id, project_id, provider, source_type, name, provider_job_id,
		        provider_avatar_id, provider_group_id, attempts
		 FROM avatar_create_jobs
		 WHERE status IN ('queued', 'training')
		 ORDER BY id ASC`,
	)
	if err != nil {
		return err
	}
	var jobs []avatarCreateJob
	for rows.Next() {
		var j avatarCreateJob
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.Provider, &j.SourceType, &j.Name,
			&j.ProviderJobID, &j.ProviderAvatarID, &j.ProviderGroupID, &j.Attempts); err != nil {
			continue
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	for _, j := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bound := app.IntegrationFor("avatar_provider")
		if bound == nil || bound.AppSlug != j.Provider {
			app.Logger().Warn("avatar create poll: bound provider mismatch", "job_id", j.ID, "provider", j.Provider)
			continue
		}
		a.pollOneAvatarCreateJob(app, bound, j)
	}
	return nil
}

func (a *App) pollOneAvatarCreateJob(app *sdk.AppCtx, bound *sdk.BoundIntegration, j avatarCreateJob) {
	attempts := j.Attempts + 1
	if attempts > maxAvatarCreatePollAttempts {
		avatarCreateJobUpdate(app, j, "failed", fmt.Sprintf("gave up after %d polls (~%s)", maxAvatarCreatePollAttempts, time.Duration(maxAvatarCreatePollAttempts)*avatarCreatePollInterval), attempts)
		return
	}
	var result avatarCreateResult
	var err error
	switch j.Provider {
	case "heygen":
		result, err = pollHeyGenAvatarCreate(app, bound, j)
	case "tavus":
		result, err = pollTavusAvatarCreate(app, bound, j)
	default:
		err = fmt.Errorf("provider %s not wired", j.Provider)
	}
	if err != nil {
		avatarCreateJobBump(app, j.ID, attempts)
		app.Logger().Warn("avatar create poll transient error", "id", j.ID, "err", err)
		return
	}
	status := result.Status
	if status == "" {
		status = "training"
	}
	j.ProviderJobID = firstNonEmpty(result.ProviderJobID, j.ProviderJobID)
	j.ProviderAvatarID = firstNonEmpty(result.ProviderAvatarID, j.ProviderAvatarID)
	j.ProviderGroupID = firstNonEmpty(result.ProviderGroupID, j.ProviderGroupID)
	j.Status = status
	j.Error = result.Error
	if _, err := upsertMediaIdentityFromAvatarJob(app, j); err != nil {
		app.Logger().Warn("avatar identity poll upsert failed", "job_id", j.ID, "err", err)
	}
	avatarCreateJobUpdate(app, j, status, result.Error, attempts)
	if status == "completed" {
		app.EmitWithProject("avatar.created", j.ProjectID, map[string]any{
			"job_id": j.ID, "provider": j.Provider, "avatar_id": firstNonEmpty(result.ProviderAvatarID, j.ProviderAvatarID),
		})
	} else if status == "failed" {
		app.EmitWithProject("avatar.create_failed", j.ProjectID, map[string]any{
			"job_id": j.ID, "provider": j.Provider, "error": result.Error,
		})
	}
}

func pollHeyGenAvatarCreate(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, j avatarCreateJob) (avatarCreateResult, error) {
	if j.ProviderAvatarID == "" {
		return avatarCreateResult{Status: "training"}, nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_avatar_look", map[string]any{"look_id": j.ProviderAvatarID})
	if err != nil {
		return avatarCreateResult{}, err
	}
	if res == nil || !res.Success {
		return avatarCreateResult{}, errors.New("heygen get_avatar_look non-2xx")
	}
	var body struct {
		Data struct {
			ID      string `json:"id"`
			GroupID string `json:"group_id"`
			Status  string `json:"status"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		return avatarCreateResult{}, err
	}
	return avatarCreateResult{
		ProviderJobID:    firstNonEmpty(body.Data.ID, j.ProviderJobID),
		ProviderAvatarID: firstNonEmpty(body.Data.ID, j.ProviderAvatarID),
		ProviderGroupID:  firstNonEmpty(body.Data.GroupID, j.ProviderGroupID),
		Status:           normalizeAvatarCreateStatus(body.Data.Status),
		Error:            body.Data.Error.Message,
	}, nil
}

func pollTavusAvatarCreate(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, j avatarCreateJob) (avatarCreateResult, error) {
	if j.ProviderAvatarID == "" {
		return avatarCreateResult{Status: "training"}, nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_replica", map[string]any{"replica_id": j.ProviderAvatarID})
	if err != nil {
		return avatarCreateResult{}, err
	}
	if res == nil || !res.Success {
		return avatarCreateResult{}, errors.New("tavus get_replica non-2xx")
	}
	var body struct {
		ReplicaID string `json:"replica_id"`
		Status    string `json:"status"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		return avatarCreateResult{}, err
	}
	return avatarCreateResult{
		ProviderJobID:    firstNonEmpty(body.ReplicaID, j.ProviderJobID),
		ProviderAvatarID: firstNonEmpty(body.ReplicaID, j.ProviderAvatarID),
		Status:           normalizeAvatarCreateStatus(body.Status),
		Error:            body.Error,
	}, nil
}

func avatarCreateJobBump(ctx *sdk.AppCtx, id int64, attempts int) {
	_, err := ctx.AppDB().Exec(
		`UPDATE avatar_create_jobs
		 SET attempts=?, last_poll_at=?, updated_at=?, status='training'
		 WHERE id=?`,
		attempts, time.Now(), time.Now(), id,
	)
	if err != nil {
		ctx.Logger().Warn("avatar_create_jobs bump failed", "id", id, "err", err)
	}
}

func avatarCreateJobUpdate(ctx *sdk.AppCtx, j avatarCreateJob, status, errMsg string, attempts int) {
	_, err := ctx.AppDB().Exec(
		`UPDATE avatar_create_jobs
		 SET status=?, error=?, provider_job_id=?, provider_avatar_id=?,
		     provider_group_id=?, attempts=?, last_poll_at=?, updated_at=?
		 WHERE id=?`,
		status, errMsg, j.ProviderJobID, j.ProviderAvatarID, j.ProviderGroupID,
		attempts, time.Now(), time.Now(), j.ID,
	)
	if err != nil {
		ctx.Logger().Warn("avatar_create_jobs update failed", "id", j.ID, "err", err)
	}
}

func sanitizedAvatarCreateJSON(args map[string]any) string {
	cp := copyMap(args)
	for _, k := range []string{"source_image", "source_video", "consent_video"} {
		if v, ok := cp[k].(string); ok && len(v) > 160 {
			cp[k] = v[:80] + "…" + v[len(v)-40:]
		}
	}
	delete(cp, "_source_ref")
	b, err := json.Marshal(cp)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func safeAvatarSourceRef(s string) string {
	if len(s) <= 180 {
		return s
	}
	if strings.HasPrefix(s, "data:") {
		return s[:64] + "…"
	}
	return s[:100] + "…" + s[len(s)-40:]
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func looksLikeHTTPURL(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
