package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type localArtifact struct {
	Kind          string
	Path          string
	Language      string
	CaptionSource string
}

func normalizeMetadata(raw map[string]any) sourceMetadata {
	metadata := sourceMetadata{
		ID:              mapString(raw, "id"),
		Title:           mapString(raw, "title"),
		Description:     mapString(raw, "description"),
		Channel:         mapString(raw, "channel"),
		ChannelID:       mapString(raw, "channel_id"),
		Uploader:        mapString(raw, "uploader"),
		UploaderID:      mapString(raw, "uploader_id"),
		WebpageURL:      mapString(raw, "webpage_url"),
		ThumbnailURL:    bestThumbnail(raw),
		UploadDate:      mapString(raw, "upload_date"),
		DurationSeconds: mapFloat(raw, "duration"),
		AgeLimit:        int(mapFloat(raw, "age_limit")),
		LiveStatus:      mapString(raw, "live_status"),
		Extractor:       mapString(raw, "extractor"),
		Tags:            mapStringSlice(raw["tags"]),
		Categories:      mapStringSlice(raw["categories"]),
		CaptionTracks:   availableCaptionTracks(raw),
	}
	if metadata.Channel == "" {
		metadata.Channel = metadata.Uploader
	}
	metadata.PublishDate = normalizePublishDate(metadata.UploadDate)
	if formats, ok := raw["formats"].([]any); ok {
		metadata.FormatCount = len(formats)
	}
	return metadata
}

func normalizePublishDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 8 {
		return value[:4] + "-" + value[4:6] + "-" + value[6:]
	}
	return value
}

func mapStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func availableCaptionTracks(raw map[string]any) []captionTrack {
	tracks := make([]captionTrack, 0)
	tracks = append(tracks, captionTracksFromMap(raw["subtitles"], "manual")...)
	tracks = append(tracks, captionTracksFromMap(raw["automatic_captions"], "automatic")...)
	sort.SliceStable(tracks, func(i, j int) bool {
		if tracks[i].Source != tracks[j].Source {
			return tracks[i].Source == "manual"
		}
		return tracks[i].Language < tracks[j].Language
	})
	return tracks
}

func captionTracksFromMap(value any, source string) []captionTrack {
	values, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	tracks := make([]captionTrack, 0, len(values))
	for language, rawFormats := range values {
		if language == "live_chat" || strings.TrimSpace(language) == "" {
			continue
		}
		name := ""
		if formats, ok := rawFormats.([]any); ok && len(formats) > 0 {
			if first, ok := formats[0].(map[string]any); ok {
				name = mapString(first, "name")
			}
		}
		tracks = append(tracks, captionTrack{Language: language, Name: name, Source: source})
	}
	return tracks
}

func selectCaptionTracks(raw map[string]any, requested []string) []captionTrack {
	all := availableCaptionTracks(raw)
	if len(all) == 0 {
		return nil
	}
	if len(requested) == 0 {
		manual := filterCaptionTracks(all, nil, "manual")
		if len(manual) > 0 {
			return manual
		}
		preferred := []string{mapString(raw, "language"), "en"}
		for _, language := range preferred {
			if selected := filterCaptionTracks(all, []string{language}, "automatic"); len(selected) > 0 {
				return selected[:1]
			}
		}
		return []captionTrack{all[0]}
	}
	selected := make([]captionTrack, 0, len(requested))
	seen := map[string]bool{}
	for _, source := range []string{"manual", "automatic"} {
		for _, track := range filterCaptionTracks(all, requested, source) {
			if !seen[track.Language] {
				selected = append(selected, track)
				seen[track.Language] = true
			}
		}
	}
	return selected
}

func filterCaptionTracks(tracks []captionTrack, requested []string, source string) []captionTrack {
	out := make([]captionTrack, 0)
	for _, track := range tracks {
		if track.Source != source {
			continue
		}
		if len(requested) == 0 || captionLanguageRequested(track.Language, requested) {
			out = append(out, track)
		}
	}
	return out
}

func captionLanguageRequested(language string, requested []string) bool {
	for _, wanted := range requested {
		wanted = strings.TrimSpace(wanted)
		if wanted == "*" || language == wanted || strings.HasPrefix(language, wanted+"-") {
			return true
		}
	}
	return false
}

func writeMetadataArtifact(jobDir string, metadata sourceMetadata) (string, error) {
	body, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(jobDir, "metadata.json")
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func discoverSidecarArtifacts(jobDir, primary, metadataPath string, tracks []captionTrack) ([]localArtifact, error) {
	out := []localArtifact{{Kind: "metadata", Path: metadataPath}}
	err := filepath.WalkDir(jobDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path == primary || path == metadataPath || filepath.Base(path) == "cookies.txt" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".vtt", ".srt", ".ass", ".lrc", ".ttml", ".json3":
			track := captionTrackForPath(path, tracks)
			out = append(out, localArtifact{Kind: "captions", Path: path, Language: track.Language, CaptionSource: track.Source})
		case ".jpg", ".jpeg", ".png", ".webp", ".avif":
			out = append(out, localArtifact{Kind: "thumbnail", Path: path})
		}
		return nil
	})
	return out, err
}

func captionTrackForPath(path string, tracks []captionTrack) captionTrack {
	name := strings.ToLower(filepath.Base(path))
	for _, track := range tracks {
		needle := "." + strings.ToLower(track.Language) + "."
		if strings.Contains(name, needle) {
			return track
		}
	}
	if len(tracks) == 1 {
		return tracks[0]
	}
	return captionTrack{}
}

func artifactContentType(path string) string {
	if value := mime.TypeByExtension(filepath.Ext(path)); value != "" {
		return value
	}
	if strings.EqualFold(filepath.Ext(path), ".vtt") {
		return "text/vtt"
	}
	return "application/octet-stream"
}

func (a *App) transcribeWithMedia(runCtx context.Context, ctx *sdk.AppCtx, fileID int64) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("Media transcription unavailable: platform API is not configured")
	}
	args := storageArgs(ctx.CurrentProject(), map[string]any{"file_id": fmt.Sprint(fileID)})
	indexDeadline := time.Now().Add(time.Duration(configInt(ctx, "ingest_media_index_timeout_seconds", 90)) * time.Second)
	for {
		var result map[string]any
		err := ctx.PlatformAPI().CallAppResult("media", "media_get", args, &result)
		if err == nil && boolValue(result["found"]) {
			break
		}
		if time.Now().After(indexDeadline) {
			if err != nil {
				return nil, fmt.Errorf("wait for Media indexing: %w", err)
			}
			return nil, errors.New("timed out waiting for Media to index the audio file")
		}
		if err := waitContext(runCtx, 2*time.Second); err != nil {
			return nil, err
		}
	}
	var queued map[string]any
	if err := ctx.PlatformAPI().CallAppResult("media", "media_transcribe", args, &queued); err != nil {
		return nil, fmt.Errorf("queue Media transcription: %w", err)
	}
	deadline := time.Now().Add(time.Duration(configInt(ctx, "ingest_transcription_timeout_seconds", 900)) * time.Second)
	for {
		var result map[string]any
		if err := ctx.PlatformAPI().CallAppResult("media", "media_get_transcript", args, &result); err != nil {
			return nil, fmt.Errorf("poll Media transcription: %w", err)
		}
		if boolValue(result["found"]) {
			transcript, _ := result["transcript"].(map[string]any)
			switch mapString(transcript, "status") {
			case "ok":
				return transcript, nil
			case "failed", "skipped":
				message := mapString(transcript, "error")
				if message == "" {
					message = "Media transcription did not complete"
				}
				return nil, errors.New(message)
			}
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for Media transcription")
		}
		if err := waitContext(runCtx, 3*time.Second); err != nil {
			return nil, err
		}
	}
}

func writeTranscriptArtifact(jobDir string, transcript map[string]any) (string, error) {
	body, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(jobDir, "transcript.json")
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *App) completeIngestion(runCtx context.Context, ctx *sdk.AppCtx, id string, req downloadRequest, primaryPath, jobDir, cookieFile string, metadata sourceMetadata) error {
	primary, err := a.uploadArtifactFile(runCtx, ctx, id, req, localArtifact{Kind: req.Mode, Path: primaryPath}, true)
	if err != nil {
		return err
	}
	metadataPath, err := writeMetadataArtifact(jobDir, metadata)
	if err != nil {
		return fmt.Errorf("write normalized metadata: %w", err)
	}
	locals, err := discoverSidecarArtifacts(jobDir, primaryPath, metadataPath, req.CaptionTracks)
	if err != nil {
		return fmt.Errorf("discover ingestion artifacts: %w", err)
	}
	hasCaption, hasThumbnail := false, false
	for _, local := range locals {
		if _, err := a.uploadArtifactFile(runCtx, ctx, id, req, local, false); err != nil {
			return fmt.Errorf("upload %s artifact: %w", local.Kind, err)
		}
		hasCaption = hasCaption || local.Kind == "captions"
		hasThumbnail = hasThumbnail || local.Kind == "thumbnail"
	}
	if !hasThumbnail {
		warning := "source did not provide a downloadable thumbnail"
		_ = addDownloadWarning(context.Background(), ctx.AppDB(), id, warning)
		appendLog(context.Background(), ctx.AppDB(), id, "warning", warning)
	}
	if !hasCaption && req.FallbackTranscribe {
		a.setDownloadStage(ctx, req.ProjectID, id, stageTranscribing, 99)
		audio := primary
		if req.Mode != "audio" {
			audioPath, err := a.downloadFallbackAudio(runCtx, ctx, id, req, jobDir, cookieFile)
			if err != nil {
				return fmt.Errorf("download transcription audio: %w", err)
			}
			audio, err = a.uploadArtifactFile(runCtx, ctx, id, req, localArtifact{Kind: "audio", Path: audioPath}, false)
			if err != nil {
				return fmt.Errorf("upload transcription audio: %w", err)
			}
		}
		transcript, err := a.transcribeWithMedia(runCtx, ctx, audio.StorageFileID)
		if err != nil {
			return fmt.Errorf("source captions unavailable and Media fallback failed: %w", err)
		}
		transcriptPath, err := writeTranscriptArtifact(jobDir, transcript)
		if err != nil {
			return fmt.Errorf("write transcript artifact: %w", err)
		}
		if _, err := a.uploadArtifactFile(runCtx, ctx, id, req, localArtifact{Kind: "transcript", Path: transcriptPath, Language: mapString(transcript, "language"), CaptionSource: "transcription"}, false); err != nil {
			return fmt.Errorf("upload transcript artifact: %w", err)
		}
	}
	if !hasCaption && !req.FallbackTranscribe {
		warning := "source captions unavailable; transcription fallback was disabled"
		_ = addDownloadWarning(context.Background(), ctx.AppDB(), id, warning)
		appendLog(context.Background(), ctx.AppDB(), id, "warning", warning)
	}
	return completeDownload(context.Background(), ctx.AppDB(), id, primary.Name, primary.Bytes, primary.StorageFileID, primary.StorageURL)
}

func (a *App) downloadFallbackAudio(runCtx context.Context, ctx *sdk.AppCtx, id string, req downloadRequest, jobDir, cookieFile string) (string, error) {
	audioDir := filepath.Join(jobDir, "fallback-audio")
	if err := os.MkdirAll(audioDir, 0700); err != nil {
		return "", err
	}
	audioReq := req
	audioReq.Ingest = false
	audioReq.CaptionTracks = nil
	audioReq.Mode = "audio"
	audioReq.Quality = "best"
	audioReq.FormatID = ""
	if audioReq.AudioFormat == "" {
		audioReq.AudioFormat = "mp3"
	}
	printed := make([]string, 0, 1)
	var stderr strings.Builder
	err := a.runner.Run(runCtx, a.ytdlpPath, buildDownloadArgs(audioReq, audioDir, cookieFile), func(line string) {
		line = trimLogLine(line)
		if strings.HasPrefix(line, "__APTEVA_FILE__") {
			printed = append(printed, line)
			return
		}
		appendLog(context.Background(), ctx.AppDB(), id, "stdout", line)
	}, func(line string) {
		line = trimLogLine(line)
		stderr.WriteString(line)
		stderr.WriteByte('\n')
		appendLog(context.Background(), ctx.AppDB(), id, "stderr", line)
	})
	if err != nil {
		if runCtx.Err() != nil {
			return "", runCtx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return findOutputFile(audioDir, printed)
}
