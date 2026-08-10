package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxVoiceCloneSamples = 10

func selectVoiceIdentityProvider(ctx *sdk.AppCtx, args map[string]any, sourceType string) (*sdk.BoundIntegration, error) {
	provider := strings.TrimSpace(strArg(args, "provider", ""))
	bounds := boundIntegrationsFor(ctx, "audio_provider")
	for _, bound := range bounds {
		if bound == nil || !audioProviderSupports(bound.AppSlug, "voice.create") {
			continue
		}
		if provider != "" && bound.AppSlug != provider {
			continue
		}
		if sourceType == "prompt" && bound.AppSlug != "elevenlabs" && bound.AppSlug != "minimax-audio" {
			continue
		}
		return bound, nil
	}
	if provider != "" {
		return nil, fmt.Errorf("audio provider %s is not bound or does not support %s voice creation", provider, sourceType)
	}
	return nil, nil
}

func (a *App) createAudioCloneIdentity(ctx *sdk.AppCtx, pid string, bound *sdk.BoundIntegration, args map[string]any) (any, error) {
	name := strings.TrimSpace(strArg(args, "name", ""))
	samples, filename, originalRefs, err := resolveVoiceAudioSamples(ctx, args)
	if err != nil {
		return mcpError("source_audio: " + err.Error()), nil
	}
	description := strings.TrimSpace(firstNonEmpty(strArg(args, "description", ""), strArg(args, "voice_description", ""), strArg(args, "prompt", "")))

	if (bound.AppSlug == "cartesia" || bound.AppSlug == "minimax-audio") && len(samples) != 1 {
		return mcpError(bound.AppSlug + " voice cloning requires exactly one source_audio sample"), nil
	}

	var tool string
	var providerArgs map[string]any
	var res *sdk.ExecuteResult
	switch bound.AppSlug {
	case "fish-audio":
		tool = "create_voice_model"
		providerArgs = buildFishAudioCloneArgs(args, name, description, samples, filename)
	case "elevenlabs":
		tool = "create_ivc_voice"
		providerArgs = buildElevenLabsCloneArgs(args, name, description, samples, filename)
	case "cartesia":
		tool = "clone_voice"
		providerArgs = buildCartesiaCloneArgs(args, name, description, samples[0], filename)
	case "minimax-audio":
		var providerVoiceID string
		res, providerVoiceID, err = executeMiniMaxClone(ctx, bound, args, name, samples[0], filename)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		providerArgs = map[string]any{"voice_id": providerVoiceID}
	default:
		return mcpError("audio cloning is not wired for provider " + bound.AppSlug), nil
	}

	if res == nil {
		res, err = ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, providerArgs)
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
	}

	var identity mediaIdentity
	switch bound.AppSlug {
	case "fish-audio":
		identity, err = normalizeFishAudioCreatedVoice(res.Data)
	case "elevenlabs":
		identity, err = normalizeElevenLabsCreatedVoice(res.Data)
	case "cartesia":
		identity, err = normalizeCartesiaCreatedVoice(res.Data)
	case "minimax-audio":
		identity, err = normalizeMiniMaxClonedVoice(res.Data, fmt.Sprint(providerArgs["voice_id"]), name)
	}
	if err != nil {
		return mcpError("provider response parse: " + err.Error()), nil
	}
	if identity.ProviderIdentityID == "" {
		return mcpError("provider response missing voice id"), nil
	}
	identity.ProjectID = pid
	identity.Kind = identityKindVoice
	identity.Provider = bound.AppSlug
	identity.Name = firstNonEmpty(identity.Name, name)
	identity.SourceType = "audio"
	identity.SourceRef = strings.Join(persistedMediaRefs(originalRefs), ",")
	identity.Prompt = description
	if identity.Status == "" {
		identity.Status = "ready"
	}
	identity.MetadataJSON = compactJSON(map[string]any{
		"request":               json.RawMessage(sanitizedIdentityCreateJSON(args)),
		"sample_count":          len(samples),
		"provider_response_raw": json.RawMessage(res.Data),
	})
	id, err := upsertMediaIdentity(ctx, identity)
	if err != nil {
		return mcpError("voice created at provider but local identity row failed: " + err.Error()), nil
	}
	identity.ID = id
	ctx.EmitWithProject("identity.created", pid, map[string]any{
		"id": id, "kind": identity.Kind, "provider": identity.Provider, "provider_identity_id": identity.ProviderIdentityID,
	})
	return identityCreateMCPResult(identityCreateResult{Identity: identity}), nil
}

func buildFishAudioCloneArgs(args map[string]any, name, description string, samples []string, filename string) map[string]any {
	out := map[string]any{
		"type": "tts", "title": name, "train_mode": "fast", "voices": samples,
		"visibility": "private", "enhance_audio_quality": true, "generate_sample": true,
	}
	if description != "" {
		out["description"] = description
	}
	if filename != "" {
		out["voices_filename"] = filename
	}
	if transcripts := stringListArg(args, "transcripts"); len(transcripts) > 0 {
		out["texts"] = transcripts
	}
	if labels, ok := args["labels"].(map[string]any); ok {
		if tags := stringListValue(labels["tags"]); len(tags) > 0 {
			out["tags"] = tags
		}
	}
	copyVoiceCloneOptions(out, args, []string{"visibility", "texts", "tags", "enhance_audio_quality", "generate_sample"})
	return out
}

func buildElevenLabsCloneArgs(args map[string]any, name, description string, samples []string, filename string) map[string]any {
	out := map[string]any{"name": name, "files": samples}
	if description != "" {
		out["description"] = description
	}
	if filename != "" {
		out["files_filename"] = filename
	}
	if labels, ok := args["labels"]; ok {
		out["labels"] = labels
	}
	copyVoiceCloneOptions(out, args, []string{"remove_background_noise"})
	return out
}

func buildCartesiaCloneArgs(args map[string]any, name, description, sample, filename string) map[string]any {
	language := firstNonEmpty(
		strArg(args, "language", ""),
		optionString(args, "language"),
		labelString(args, "language"),
		"en",
	)
	out := map[string]any{
		"clip": sample, "name": name, "language": language,
	}
	if filename != "" {
		out["clip_filename"] = filename
	}
	if description != "" {
		out["description"] = description
	}
	copyVoiceCloneOptions(out, args, []string{"base_voice_id"})
	return out
}

func executeMiniMaxClone(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, args map[string]any, name, sample, filename string) (*sdk.ExecuteResult, string, error) {
	if filename == "" {
		filename = voiceSampleFilename(sample)
	}
	upload, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "upload_file", map[string]any{
		"purpose": "voice_clone", "file": sample, "file_filename": filename,
	})
	if err != nil {
		return nil, "", fmt.Errorf("provider upload failed: %w", err)
	}
	if upload == nil || !upload.Success {
		body := ""
		if upload != nil {
			body = string(upload.Data)
		}
		return nil, "", fmt.Errorf("provider upload returned non-2xx: %s", body)
	}
	fileID, err := normalizeMiniMaxUploadFileID(upload.Data)
	if err != nil {
		return nil, "", fmt.Errorf("provider upload response parse: %w", err)
	}
	voiceID, err := miniMaxVoiceID(args, name)
	if err != nil {
		return nil, "", err
	}
	cloneArgs := buildMiniMaxCloneArgs(args, fileID, voiceID)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "clone_voice", cloneArgs)
	if err != nil {
		return nil, "", fmt.Errorf("provider call failed: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, "", fmt.Errorf("provider returned non-2xx: %s", body)
	}
	return res, voiceID, nil
}

func buildMiniMaxCloneArgs(args map[string]any, fileID int64, voiceID string) map[string]any {
	out := map[string]any{"file_id": fileID, "voice_id": voiceID}
	copyVoiceCloneOptions(out, args, []string{
		"clone_prompt", "text", "model", "language_boost", "text_validation", "accuracy",
		"need_noise_reduction", "need_volume_normalization", "aigc_watermark",
	})
	if transcripts := stringListArg(args, "transcripts"); len(transcripts) > 0 {
		if _, exists := out["text_validation"]; !exists {
			out["text_validation"] = transcripts[0]
		}
	}
	if previewText := firstNonEmpty(strArg(args, "preview_text", ""), optionString(args, "preview_text")); previewText != "" {
		out["text"] = previewText
		if _, exists := out["model"]; !exists {
			out["model"] = "speech-2.8-hd"
		}
	}
	return out
}

func normalizeMiniMaxUploadFileID(raw json.RawMessage) (int64, error) {
	var body struct {
		File struct {
			FileID int64 `json:"file_id"`
		} `json:"file"`
		BaseResp miniMaxBaseResponse `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return 0, err
	}
	if err := body.BaseResp.Err(); err != nil {
		return 0, err
	}
	if body.File.FileID == 0 {
		return 0, errors.New("missing file.file_id")
	}
	return body.File.FileID, nil
}

func normalizeCartesiaCreatedVoice(raw json.RawMessage) (mediaIdentity, error) {
	var body struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return mediaIdentity{}, err
	}
	return mediaIdentity{
		ProviderIdentityID: body.ID,
		Name:               body.Name,
		Status:             "ready",
		MetadataJSON:       compactJSON(map[string]any{"language": body.Language}),
	}, nil
}

func normalizeMiniMaxClonedVoice(raw json.RawMessage, voiceID, name string) (mediaIdentity, error) {
	var body struct {
		DemoAudio string              `json:"demo_audio"`
		BaseResp  miniMaxBaseResponse `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return mediaIdentity{}, err
	}
	if err := body.BaseResp.Err(); err != nil {
		return mediaIdentity{}, err
	}
	return mediaIdentity{
		ProviderIdentityID: voiceID,
		Name:               name,
		PreviewURL:         body.DemoAudio,
		Status:             "ready",
	}, nil
}

type miniMaxBaseResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

func (r miniMaxBaseResponse) Err() error {
	if r.StatusCode == 0 {
		return nil
	}
	return fmt.Errorf("MiniMax status %d: %s", r.StatusCode, r.StatusMsg)
}

func miniMaxVoiceID(args map[string]any, name string) (string, error) {
	voiceID := firstNonEmpty(
		strArg(args, "provider_voice_id", ""),
		strArg(args, "voice_id", ""),
		optionString(args, "voice_id"),
	)
	if voiceID == "" {
		stem := strings.ToLower(strings.TrimSpace(name))
		var cleaned strings.Builder
		for _, r := range stem {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				cleaned.WriteRune(r)
			case cleaned.Len() > 0 && !strings.HasSuffix(cleaned.String(), "-"):
				cleaned.WriteByte('-')
			}
		}
		stem = strings.Trim(cleaned.String(), "-_")
		if stem == "" || stem[0] < 'a' || stem[0] > 'z' {
			stem = "voice-" + stem
		}
		if len(stem) > 200 {
			stem = strings.TrimRight(stem[:200], "-_")
		}
		voiceID = fmt.Sprintf("%s-%x", stem, time.Now().UnixNano())
	}
	if len(voiceID) < 8 || len(voiceID) > 256 || voiceID[0] < 'A' ||
		(voiceID[0] > 'Z' && voiceID[0] < 'a') || voiceID[0] > 'z' ||
		strings.HasSuffix(voiceID, "-") || strings.HasSuffix(voiceID, "_") {
		return "", errors.New("MiniMax voice_id must be 8-256 characters, start with a letter, and not end with '-' or '_'")
	}
	for _, r := range voiceID {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", errors.New("MiniMax voice_id may contain only letters, digits, '-' and '_'")
		}
	}
	return voiceID, nil
}

func voiceSampleFilename(sample string) string {
	lower := strings.ToLower(sample)
	switch {
	case strings.HasPrefix(lower, "data:audio/mpeg"), strings.HasPrefix(lower, "data:audio/mp3"):
		return "voice-sample.mp3"
	case strings.HasPrefix(lower, "data:audio/mp4"), strings.HasPrefix(lower, "data:audio/m4a"):
		return "voice-sample.m4a"
	default:
		return "voice-sample.wav"
	}
}

func optionString(args map[string]any, key string) string {
	if opts, ok := args["options"].(map[string]any); ok {
		if value, exists := opts[key]; exists && value != nil {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func labelString(args map[string]any, key string) string {
	if labels, ok := args["labels"].(map[string]any); ok {
		if value, exists := labels[key]; exists && value != nil {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func copyVoiceCloneOptions(out, args map[string]any, keys []string) {
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range keys {
			if value, exists := opts[key]; exists {
				out[key] = value
			}
		}
	}
}

func normalizeFishAudioCreatedVoice(raw json.RawMessage) (mediaIdentity, error) {
	var body struct {
		ID        string   `json:"_id"`
		Title     string   `json:"title"`
		State     string   `json:"state"`
		Languages []string `json:"languages"`
		Samples   []struct {
			Audio string `json:"audio"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return mediaIdentity{}, err
	}
	status := "ready"
	if strings.EqualFold(body.State, "failed") {
		status = "failed"
	}
	preview := ""
	if len(body.Samples) > 0 {
		preview = body.Samples[0].Audio
	}
	return mediaIdentity{
		ProviderIdentityID: body.ID, Name: body.Title, Status: status, PreviewURL: preview,
	}, nil
}

func resolveVoiceAudioSamples(ctx *sdk.AppCtx, args map[string]any) ([]string, string, []string, error) {
	refs := sourceAudioRefs(args)
	if len(refs) == 0 {
		return nil, "", nil, errors.New("at least one source_audio or source_audios value is required")
	}
	if len(refs) > maxVoiceCloneSamples {
		return nil, "", nil, fmt.Errorf("at most %d audio samples are supported", maxVoiceCloneSamples)
	}
	filename := strings.TrimSpace(strArg(args, "source_audio_filename", ""))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		resolved, discoveredName, err := resolveVoiceAudioSample(ctx, ref)
		if err != nil {
			return nil, "", refs, err
		}
		if filename == "" && discoveredName != "" {
			filename = discoveredName
		}
		out = append(out, resolved)
	}
	return out, filename, refs, nil
}

func sourceAudioRefs(args map[string]any) []string {
	out := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	add(strArg(args, "source_audio", ""))
	for _, value := range stringListArg(args, "source_audios") {
		add(value)
	}
	return out
}

func resolveVoiceAudioSample(ctx *sdk.AppCtx, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "storage:") {
		id, err := parseInt64(strings.TrimPrefix(ref, "storage:"))
		if err != nil {
			return "", "", errors.New("malformed storage handle: " + ref)
		}
		var got struct {
			Name          string `json:"name"`
			ContentType   string `json:"content_type"`
			ContentBase64 string `json:"content_base64"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content", storageArgs(ctx, map[string]any{"id": id}), &got); err != nil {
			return "", "", errors.New("storage fetch failed: " + err.Error())
		}
		if got.ContentBase64 == "" {
			return "", "", errors.New("storage returned empty content")
		}
		return dataURL(firstNonEmpty(got.ContentType, "application/octet-stream"), got.ContentBase64), got.Name, nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return fetchVoiceAudioURL(ref)
	}
	return ref, "", nil
}

func fetchVoiceAudioURL(rawURL string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Apteva media-studio voice clone)")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("audio URL returned status %d", resp.StatusCode)
	}
	data, err := readLimitedMedia(resp.Body, resp.ContentLength, 25<<20)
	if err != nil {
		return "", "", err
	}
	parsed, _ := url.Parse(rawURL)
	filename := path.Base(parsed.Path)
	if filename == "." || filename == "/" {
		filename = "voice-sample.bin"
	}
	return dataURL(firstNonEmpty(resp.Header.Get("Content-Type"), "application/octet-stream"), base64.StdEncoding.EncodeToString(data)), filename, nil
}

func dataURL(contentType, base64Value string) string {
	return "data:" + strings.TrimSpace(strings.Split(contentType, ";")[0]) + ";base64," + base64Value
}

func stringListArg(args map[string]any, key string) []string {
	return stringListValue(args[key])
}

func stringListValue(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(values) != "" {
			return []string{values}
		}
	}
	return nil
}
