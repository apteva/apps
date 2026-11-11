package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func transcriptImportFromArgs(ctx *sdk.AppCtx, pid, fid string, args map[string]any) (*TranscriptRow, error) {
	media, mediaErr := getMedia(ctx.AppDB(), pid, fid)
	expected, _ := args["source_sha256"].(string)
	if expected != "" && (mediaErr != nil || media.SourceSHA256 != expected) {
		return nil, errors.New("transcript_source_mismatch")
	}
	t := &TranscriptRow{FileID: fid, ProjectID: pid, Status: "ok", SourceKind: "imported", Provider: "imported"}
	if mediaErr == nil {
		t.DurationMs = media.DurationMs
		t.SourceSHA256 = media.SourceSHA256
		t.RequireSourceMatch = true
	}
	if raw, ok := args["deepgram_response"]; ok {
		if expected == "" || mediaErr != nil {
			return nil, errors.New("saved Deepgram import requires a current source_sha256")
		}
		audioID, err := transcriptInteger(args["transcription_audio_file_id"], "transcription_audio_file_id")
		if err != nil || audioID <= 0 {
			return nil, errors.New("transcription_audio_file_id required")
		}
		var evidenceRaw string
		err = ctx.AppDB().QueryRow(`SELECT evidence FROM transcript_audio_checks WHERE project_id=? AND file_id=? AND kind=? AND storage_file_id=?`, pid, fid, transcriptAudioKind(expected), audioID).Scan(&evidenceRaw)
		if err != nil {
			return nil, errors.New("source-matched validated transcription audio not found")
		}
		var evidence transcriptAudioEvidence
		if json.Unmarshal([]byte(evidenceRaw), &evidence) != nil || evidence.StorageFileID != audioID {
			return nil, errors.New("invalid transcription audio evidence")
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		parsed, err := parseDeepgramResponse(data)
		if err != nil {
			return nil, err
		}
		timing, err := buildTranscriptTiming(context.Background(), ctx, newStorageClient(), media, &evidence, parsed)
		if err != nil {
			return nil, err
		}
		var metadata struct {
			SHA256 string `json:"sha256"`
		}
		_ = json.Unmarshal(parsed.Metadata, &metadata)
		if metadata.SHA256 == "" || !strings.EqualFold(metadata.SHA256, timing.AudioSHA256) {
			return nil, errors.New("provider_audio_identity_unverified: regenerate through media_transcribe")
		}
		if parsed.ProviderDurationMs <= 0 || absInt64(parsed.ProviderDurationMs-timing.AudioDurationMs) > int64(transcriptAudioDurationTolerance(timing.AudioDurationMs)) {
			return nil, errors.New("provider audio duration mismatch")
		}
		if parsed.WordCount == 0 {
			return nil, errors.New("word_timing_unavailable")
		}
		t.Text = parsed.Text
		t.Language = parsed.Language
		t.Provider = "deepgram"
		t.Segments, err = formatSegments(parsed.Segments)
		if err != nil {
			return nil, err
		}
		t.Diagnostics, _ = json.Marshal(map[string]any{"timing": timing, "provider_metadata": parsed.Metadata, "word_count": parsed.WordCount, "last_word_end_ms": parsed.LastWordEndMs, "speech_coverage": "unverified", "imported_saved_response": true})
		return t, nil
	}
	t.Text, _ = args["text"].(string)
	if strings.TrimSpace(t.Text) == "" {
		return nil, errors.New("text required")
	}
	t.Language, _ = args["language"].(string)
	if provider, ok := args["provider"].(string); ok && provider != "" {
		t.Provider = provider
	}
	if raw, ok := args["segments"]; ok && raw != nil {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var segments []TranscriptSegment
		if err = json.Unmarshal(data, &segments); err != nil {
			return nil, fmt.Errorf("invalid transcript segments: %w", err)
		}
		for _, s := range segments {
			if s.StartMs < 0 || s.EndMs < s.StartMs || (t.DurationMs > 0 && s.EndMs > t.DurationMs) {
				return nil, errors.New("invalid transcript segment interval")
			}
		}
		if err = validateTranscriptWords(transcriptStoredWords(segments), t.DurationMs); err != nil {
			return nil, err
		}
		t.Segments, err = formatSegments(segments)
		if err != nil {
			return nil, err
		}
	}
	return t, nil
}
