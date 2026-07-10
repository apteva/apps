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
		if sourceType == "prompt" && bound.AppSlug != "elevenlabs" {
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

	var tool string
	var providerArgs map[string]any
	switch bound.AppSlug {
	case "fish-audio":
		tool = "create_voice_model"
		providerArgs = buildFishAudioCloneArgs(args, name, description, samples, filename)
	case "elevenlabs":
		tool = "create_ivc_voice"
		providerArgs = buildElevenLabsCloneArgs(args, name, description, samples, filename)
	default:
		return mcpError("audio cloning is not wired for provider " + bound.AppSlug), nil
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

	var identity mediaIdentity
	if bound.AppSlug == "fish-audio" {
		identity, err = normalizeFishAudioCreatedVoice(res.Data)
	} else {
		identity, err = normalizeElevenLabsCreatedVoice(res.Data)
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
