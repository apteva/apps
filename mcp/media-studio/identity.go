package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	identityKindVoice  = "voice"
	identityKindAvatar = "avatar"
)

type mediaIdentity struct {
	ID                 int64  `json:"id"`
	ProjectID          string `json:"project_id"`
	Kind               string `json:"kind"`
	Provider           string `json:"provider"`
	Name               string `json:"name"`
	SourceType         string `json:"source_type"`
	ProviderIdentityID string `json:"provider_identity_id"`
	ProviderJobID      string `json:"provider_job_id"`
	ProviderGroupID    string `json:"provider_group_id"`
	SourceRef          string `json:"source_ref"`
	Prompt             string `json:"prompt"`
	PreviewURL         string `json:"preview_url"`
	Status             string `json:"status"`
	Error              string `json:"error"`
	MetadataJSON       string `json:"metadata_json"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

type identityCreateResult struct {
	Identity mediaIdentity  `json:"identity"`
	Previews []voicePreview `json:"previews,omitempty"`
}

type voicePreview struct {
	AudioBase64      string  `json:"audio_base_64"`
	GeneratedVoiceID string  `json:"generated_voice_id"`
	MediaType        string  `json:"media_type"`
	DurationSeconds  float64 `json:"duration_secs"`
	Language         string  `json:"language"`
	PreviewStreamURL string  `json:"preview_stream_url,omitempty"`
}

func (a *App) toolMediaIdentityCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	if ctx == nil {
		return nil, errors.New("app not mounted")
	}
	pid := projectScopeFromArgs(ctx, args)
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	kind := strings.TrimSpace(strArg(args, "kind", identityKindVoice))
	switch kind {
	case identityKindVoice:
		return a.createVoiceIdentity(ctx, pid, args)
	case identityKindAvatar:
		out, err := a.toolMediaAvatarCreate(ctx, args)
		if err != nil {
			return out, err
		}
		return out, nil
	default:
		return mcpError("unsupported identity kind: " + kind), nil
	}
}

func (a *App) toolMediaVoiceCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cp := copyMap(args)
	cp["kind"] = identityKindVoice
	return a.toolMediaIdentityCreate(ctx, cp)
}

func (a *App) createVoiceIdentity(ctx *sdk.AppCtx, pid string, args map[string]any) (any, error) {
	name := strings.TrimSpace(strArg(args, "name", ""))
	if name == "" {
		return nil, errors.New("name required")
	}
	sourceType := strings.TrimSpace(strArg(args, "source_type", "prompt"))
	if sourceType == "" {
		sourceType = "prompt"
	}
	if sourceType != "prompt" && sourceType != "audio" {
		return mcpError("unsupported voice source_type: " + sourceType), nil
	}
	bound, err := selectVoiceIdentityProvider(ctx, args, sourceType)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if bound == nil {
		return mcpError("no compatible audio_provider bound — bind ElevenLabs and/or Fish Audio in app settings"), nil
	}
	if sourceType == "audio" {
		return a.createAudioCloneIdentity(ctx, pid, bound, args)
	}
	if bound.AppSlug != "elevenlabs" {
		return mcpError("prompt-based voice design is supported by ElevenLabs; use source_type=audio for Fish Audio"), nil
	}

	description := strings.TrimSpace(firstNonEmpty(
		strArg(args, "voice_description", ""),
		strArg(args, "prompt", ""),
	))
	if description == "" {
		return nil, errors.New("prompt or voice_description required")
	}

	generatedVoiceID := strings.TrimSpace(strArg(args, "generated_voice_id", ""))
	previews := []voicePreview{}
	if generatedVoiceID == "" {
		designArgs := buildElevenLabsDesignVoiceArgs(args, description)
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "design_voice", designArgs)
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
		var usedText string
		var errParse error
		previews, usedText, errParse = normalizeElevenLabsVoicePreviews(res.Data)
		if errParse != nil {
			return mcpError("provider response parse: " + errParse.Error()), nil
		}
		if len(previews) == 0 {
			return mcpError("provider returned no voice previews"), nil
		}
		idx := intArg(args, "preview_index", 0)
		if idx < 0 || idx >= len(previews) {
			idx = 0
		}
		generatedVoiceID = previews[idx].GeneratedVoiceID
		if generatedVoiceID == "" {
			return mcpError("selected voice preview missing generated_voice_id"), nil
		}
		if usedText != "" {
			for i := range previews {
				previews[i].PreviewStreamURL = "/api/apps/media-studio/voice-preview/" + previews[i].GeneratedVoiceID
			}
		}
	}

	saveArgs := buildElevenLabsCreateVoiceArgs(args, name, description, generatedVoiceID)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "create_voice_from_preview", saveArgs)
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
	identity, err := normalizeElevenLabsCreatedVoice(res.Data)
	if err != nil {
		return mcpError("provider response parse: " + err.Error()), nil
	}
	identity.ProjectID = pid
	identity.Kind = identityKindVoice
	identity.Provider = bound.AppSlug
	identity.Name = firstNonEmpty(identity.Name, name)
	identity.SourceType = sourceType
	identity.ProviderJobID = generatedVoiceID
	identity.Prompt = description
	identity.Status = "ready"
	identity.MetadataJSON = compactJSON(map[string]any{
		"request":               sanitizedIdentityCreateJSON(args),
		"generated_voice_id":    generatedVoiceID,
		"provider_response_raw": json.RawMessage(res.Data),
	})
	if identity.ProviderIdentityID == "" {
		return mcpError("provider response missing voice_id"), nil
	}
	id, err := upsertMediaIdentity(ctx, identity)
	if err != nil {
		return mcpError("voice created at provider but local identity row failed: " + err.Error()), nil
	}
	identity.ID = id
	ctx.EmitWithProject("identity.created", pid, map[string]any{
		"id": id, "kind": identity.Kind, "provider": identity.Provider, "provider_identity_id": identity.ProviderIdentityID,
	})
	return identityCreateMCPResult(identityCreateResult{Identity: identity, Previews: previews}), nil
}

func buildElevenLabsDesignVoiceArgs(args map[string]any, description string) map[string]any {
	out := map[string]any{"voice_description": description}
	for _, k := range []string{"model_id", "text", "auto_generate_text", "loudness", "quality", "seed", "guidance_scale", "should_enhance", "output_format"} {
		if v, ok := args[k]; ok {
			out[k] = v
		}
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, k := range []string{"model_id", "text", "auto_generate_text", "loudness", "quality", "seed", "guidance_scale", "should_enhance", "output_format"} {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out
}

func buildElevenLabsCreateVoiceArgs(args map[string]any, name, description, generatedVoiceID string) map[string]any {
	out := map[string]any{
		"voice_name":         name,
		"voice_description":  description,
		"generated_voice_id": generatedVoiceID,
	}
	if v, ok := args["labels"]; ok {
		out["labels"] = v
	}
	if v, ok := args["played_not_selected_voice_ids"]; ok {
		out["played_not_selected_voice_ids"] = v
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, k := range []string{"labels", "played_not_selected_voice_ids"} {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out
}

func normalizeElevenLabsVoicePreviews(raw json.RawMessage) ([]voicePreview, string, error) {
	var body struct {
		Previews []voicePreview `json:"previews"`
		Text     string         `json:"text"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", err
	}
	return body.Previews, body.Text, nil
}

func normalizeElevenLabsCreatedVoice(raw json.RawMessage) (mediaIdentity, error) {
	var body struct {
		VoiceID    string         `json:"voice_id"`
		Name       string         `json:"name"`
		PreviewURL string         `json:"preview_url"`
		Category   string         `json:"category"`
		Labels     map[string]any `json:"labels"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return mediaIdentity{}, err
	}
	meta := compactJSON(map[string]any{"category": body.Category, "labels": body.Labels})
	return mediaIdentity{
		ProviderIdentityID: body.VoiceID,
		Name:               body.Name,
		PreviewURL:         body.PreviewURL,
		MetadataJSON:       meta,
	}, nil
}

func identityCreateMCPResult(result identityCreateResult) map[string]any {
	identity := result.Identity
	text := fmt.Sprintf("Created %s identity #%d via %s: %s (%s)",
		identity.Kind, identity.ID, identity.Provider, identity.Name, identity.ProviderIdentityID)
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"_meta": map[string]any{
			"kind":                 identity.Kind,
			"identity_id":          identity.ID,
			"provider":             identity.Provider,
			"provider_identity_id": identity.ProviderIdentityID,
			"provider_job_id":      identity.ProviderJobID,
			"status":               identity.Status,
			"identity":             identity,
			"previews":             result.Previews,
		},
	}
}

func (a *App) toolMediaIdentityList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	if ctx == nil {
		return nil, errors.New("app not mounted")
	}
	pid := projectScopeFromArgs(ctx, args)
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	kind := strings.TrimSpace(strArg(args, "kind", ""))
	items, err := queryMediaIdentities(ctx, pid, kind, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"identities": items}, nil
}

func (a *App) toolMediaVoiceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cp := copyMap(args)
	cp["kind"] = identityKindVoice
	out, err := a.toolMediaIdentityList(ctx, cp)
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	ctx = withProjectScope(ctx, args)
	if ctx != nil {
		voices, providers, providerErrors := listAudioVoicesForAllProviders(ctx)
		m["providers"] = providers
		m["provider"] = strings.Join(providers, ",")
		m["provider_voices"] = voices
		if len(providerErrors) > 0 {
			m["provider_errors"] = providerErrors
		}
	}
	return m, nil
}

func (a *App) toolMediaAvatarList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cp := copyMap(args)
	cp["kind"] = identityKindAvatar
	out, err := a.toolMediaIdentityList(ctx, cp)
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	ctx = withProjectScope(ctx, args)
	if ctx != nil {
		if bound := ctx.IntegrationFor("avatar_provider"); bound != nil {
			if avatars, err := listAvatarsFor(ctx, bound); err == nil {
				m["provider"] = bound.AppSlug
				m["provider_avatars"] = avatars
			} else {
				m["provider_error"] = err.Error()
			}
		}
	}
	return m, nil
}

func (a *App) toolMediaIdentityGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	if ctx == nil {
		return nil, errors.New("app not mounted")
	}
	pid := projectScopeFromArgs(ctx, args)
	id := int64Arg(args, "id", 0)
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	if id == 0 {
		return nil, errors.New("id required")
	}
	identity, err := queryMediaIdentityByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"identity": identity}, nil
}

func upsertMediaIdentityFromAvatarJob(ctx *sdk.AppCtx, j avatarCreateJob) (int64, error) {
	status := "training"
	switch normalizeAvatarCreateStatus(j.Status) {
	case "completed":
		status = "ready"
	case "failed":
		status = "failed"
	default:
		status = "training"
	}
	providerIdentityID := j.ProviderAvatarID
	if providerIdentityID == "" && j.Provider == "tavus" {
		providerIdentityID = j.ProviderJobID
	}
	return upsertMediaIdentity(ctx, mediaIdentity{
		ProjectID:          j.ProjectID,
		Kind:               identityKindAvatar,
		Provider:           j.Provider,
		Name:               j.Name,
		SourceType:         j.SourceType,
		ProviderIdentityID: providerIdentityID,
		ProviderJobID:      j.ProviderJobID,
		ProviderGroupID:    j.ProviderGroupID,
		SourceRef:          j.SourceRef,
		Status:             status,
		Error:              j.Error,
		MetadataJSON: compactJSON(map[string]any{
			"avatar_create_job_id": j.ID,
			"consent_ref":          j.ConsentRef,
		}),
	})
}

func upsertMediaIdentity(ctx *sdk.AppCtx, identity mediaIdentity) (int64, error) {
	if identity.MetadataJSON == "" {
		identity.MetadataJSON = "{}"
	}
	if identity.Status == "" {
		identity.Status = "ready"
	}
	if identity.ProviderIdentityID != "" {
		var existingID int64
		err := ctx.AppDB().QueryRow(
			`SELECT id FROM media_identities
			 WHERE project_id=? AND kind=? AND provider=? AND provider_identity_id=?
			 ORDER BY id DESC LIMIT 1`,
			identity.ProjectID, identity.Kind, identity.Provider, identity.ProviderIdentityID,
		).Scan(&existingID)
		if err == nil && existingID > 0 {
			_, err = ctx.AppDB().Exec(
				`UPDATE media_identities
				 SET name=?, source_type=?, provider_job_id=?, provider_group_id=?,
				     source_ref=?, prompt=?, preview_url=?, status=?, error=?,
				     metadata_json=?, updated_at=?
				 WHERE id=?`,
				identity.Name, identity.SourceType, identity.ProviderJobID, identity.ProviderGroupID,
				identity.SourceRef, identity.Prompt, identity.PreviewURL, identity.Status, identity.Error,
				identity.MetadataJSON, time.Now(), existingID,
			)
			return existingID, err
		}
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO media_identities
		 (project_id, kind, provider, name, source_type, provider_identity_id,
		  provider_job_id, provider_group_id, source_ref, prompt, preview_url,
		  status, error, metadata_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		identity.ProjectID, identity.Kind, identity.Provider, identity.Name, identity.SourceType,
		identity.ProviderIdentityID, identity.ProviderJobID, identity.ProviderGroupID,
		identity.SourceRef, identity.Prompt, identity.PreviewURL, identity.Status,
		identity.Error, identity.MetadataJSON,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func queryMediaIdentities(ctx *sdk.AppCtx, pid, kind string, limit int) ([]mediaIdentity, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, kind, provider, name, source_type,
		        provider_identity_id, provider_job_id, provider_group_id,
		        source_ref, prompt, preview_url, status, error, metadata_json,
		        created_at, updated_at
		 FROM media_identities
		 WHERE project_id = ? AND (? = '' OR kind = ?)
		 ORDER BY id DESC LIMIT ?`,
		pid, kind, kind, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mediaIdentity{}
	for rows.Next() {
		var x mediaIdentity
		if err := rows.Scan(&x.ID, &x.ProjectID, &x.Kind, &x.Provider, &x.Name,
			&x.SourceType, &x.ProviderIdentityID, &x.ProviderJobID, &x.ProviderGroupID,
			&x.SourceRef, &x.Prompt, &x.PreviewURL, &x.Status, &x.Error,
			&x.MetadataJSON, &x.CreatedAt, &x.UpdatedAt); err != nil {
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

func queryMediaIdentityByID(ctx *sdk.AppCtx, pid string, id int64) (mediaIdentity, error) {
	var x mediaIdentity
	err := ctx.AppDB().QueryRow(
		`SELECT id, project_id, kind, provider, name, source_type,
		        provider_identity_id, provider_job_id, provider_group_id,
		        source_ref, prompt, preview_url, status, error, metadata_json,
		        created_at, updated_at
		 FROM media_identities
		 WHERE project_id = ? AND id = ?`,
		pid, id,
	).Scan(&x.ID, &x.ProjectID, &x.Kind, &x.Provider, &x.Name,
		&x.SourceType, &x.ProviderIdentityID, &x.ProviderJobID, &x.ProviderGroupID,
		&x.SourceRef, &x.Prompt, &x.PreviewURL, &x.Status, &x.Error,
		&x.MetadataJSON, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (a *App) handleIdentityCreate(w http.ResponseWriter, r *http.Request) {
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
	out, err := a.toolMediaIdentityCreate(globalCtx.WithProject(pid), body)
	writeJSON(w, out, err)
}

func (a *App) handleListIdentities(w http.ResponseWriter, r *http.Request) {
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
	limit := intQuery(r, "limit", 100)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items, err := queryMediaIdentities(globalCtx.WithProject(pid), pid, r.URL.Query().Get("kind"), limit)
	writeJSON(w, map[string]any{"identities": items}, err)
}

func sanitizedIdentityCreateJSON(args map[string]any) string {
	cp := copyMap(args)
	for key, value := range cp {
		if isMediaReferenceKey(key) {
			cp[key] = persistedRequestValue(key, value)
		}
	}
	b, err := json.Marshal(cp)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
