package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

type transcriptTiming struct {
	SourceSHA256    string  `json:"source_sha256"`
	AudioFileID     int64   `json:"audio_file_id"`
	AudioSHA256     string  `json:"audio_sha256"`
	Recipe          string  `json:"recipe"`
	AudioDurationMs int64   `json:"audio_duration_ms"`
	Timebase        string  `json:"timebase"`
	SourceOffsetMs  int64   `json:"source_offset_ms"`
	Scale           float64 `json:"scale"`
	Validated       bool    `json:"validated"`
	WordSource      string  `json:"word_source,omitempty"`
}

func sourceAudioOffset(media *MediaRow) (int64, error) {
	var probe struct {
		Format struct {
			Start string `json:"start_time"`
		} `json:"format"`
		Streams []struct {
			Type  string `json:"codec_type"`
			Start string `json:"start_time"`
		} `json:"streams"`
	}
	if len(media.RawProbe) > 0 && json.Unmarshal(media.RawProbe, &probe) != nil {
		return 0, errors.New("invalid source timeline")
	}
	parse := func(s string) (float64, error) {
		if s == "" || s == "N/A" {
			return 0, nil
		}
		n, e := strconv.ParseFloat(s, 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, errors.New("invalid audio origin")
		}
		return n, nil
	}
	origin, err := parse(probe.Format.Start)
	if err != nil {
		return 0, err
	}
	if media.HasVideo && math.Abs(origin) > .002 {
		return 0, errors.New("audio_source_unverified: use a zero-origin source")
	}
	for _, stream := range probe.Streams {
		if stream.Type == "audio" {
			start, e := parse(stream.Start)
			if e != nil {
				return 0, e
			}
			offset := int64(math.Round((start - origin) * 1000))
			if offset < 0 {
				return 0, errors.New("negative audio timeline offset unsupported")
			}
			return offset, nil
		}
	}
	return 0, nil
}

// The normalized proxy starts at decoded audio zero, not necessarily video zero.
func transcriptionAudioExpectedDuration(media *MediaRow) (int64, error) {
	offset, err := sourceAudioOffset(media)
	if err != nil {
		return 0, err
	}
	expected := media.DurationMs - offset
	var probe struct {
		Streams []struct {
			Type     string `json:"codec_type"`
			Duration string `json:"duration"`
		} `json:"streams"`
	}
	if len(media.RawProbe) > 0 && json.Unmarshal(media.RawProbe, &probe) != nil {
		return 0, errors.New("invalid audio duration evidence")
	}
	for _, stream := range probe.Streams {
		if stream.Type == "audio" && stream.Duration != "" && stream.Duration != "N/A" {
			seconds, err := strconv.ParseFloat(stream.Duration, 64)
			if err != nil {
				return 0, errors.New("invalid audio duration")
			}
			duration, err := providerMilliseconds(seconds)
			if err != nil || duration <= 0 {
				return 0, errors.New("invalid audio duration")
			}
			if duration > expected+int64(transcriptAudioDurationTolerance(expected)) {
				return 0, errors.New("audio duration exceeds source presentation")
			}
			expected = duration
			break
		}
	}
	if expected <= 0 {
		return 0, errors.New("audio duration unavailable")
	}
	return expected, nil
}

func buildTranscriptTiming(ctx context.Context, app *sdk.AppCtx, sc *storageClient, media *MediaRow, e *transcriptAudioEvidence, parsed *parsedTranscript) (*transcriptTiming, error) {
	if media.SourceSHA256 == "" {
		return nil, errors.New("transcript source hash missing")
	}
	expectedDuration, err := transcriptionAudioExpectedDuration(media)
	if err != nil {
		return nil, err
	}
	if e == nil || e.Validation == nil || e.Validation.check(expectedDuration) != nil {
		return nil, errors.New("validated transcription audio required")
	}
	files, err := sc.ResolveFiles(ctx, media.ProjectID, []string{strconv.FormatInt(e.StorageFileID, 10)})
	if err != nil {
		return nil, err
	}
	audio, ok := files[strconv.FormatInt(e.StorageFileID, 10)]
	if !ok || audio.SHA256 == "" {
		return nil, errors.New("transcription audio identity unavailable")
	}
	var providerMetadata struct {
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(parsed.Metadata, &providerMetadata) == nil && providerMetadata.SHA256 != "" && providerMetadata.SHA256 != audio.SHA256 {
		return nil, errors.New("provider audio hash does not match transcription audio")
	}
	offset, err := sourceAudioOffset(media)
	if err != nil {
		return nil, err
	}
	timing := &transcriptTiming{SourceSHA256: media.SourceSHA256, AudioFileID: e.StorageFileID, AudioSHA256: audio.SHA256, Recipe: e.Validation.Recipe, AudioDurationMs: e.Validation.DurationMs, Timebase: "transcript_audio", SourceOffsetMs: offset, Scale: 1, Validated: true, WordSource: parsed.WordSource}
	words := transcriptWords(parsed.Segments)
	if err = validateTranscriptWords(words, timing.AudioDurationMs); err != nil {
		return nil, err
	}
	for _, w := range words {
		if w.EndMs+offset > media.DurationMs {
			return nil, errors.New("word timing exceeds source audio presentation bounds")
		}
	}
	return timing, nil
}

func transcriptTimingOf(t *TranscriptRow) *transcriptTiming {
	var d struct {
		Timing *transcriptTiming `json:"timing"`
	}
	_ = json.Unmarshal(t.Diagnostics, &d)
	return d.Timing
}

func transcriptStoredWords(segments []TranscriptSegment) []TranscriptWord {
	var words []TranscriptWord
	for _, s := range segments {
		words = append(words, s.Words...)
	}
	return words
}

func transcriptWords(segments []TranscriptSegment) []TranscriptWord {
	var words []TranscriptWord
	for _, s := range segments {
		words = append(words, s.Words...)
	}
	sort.SliceStable(words, func(i, j int) bool {
		if words[i].StartMs == words[j].StartMs {
			return transcriptChannel(words[i]) < transcriptChannel(words[j])
		}
		return words[i].StartMs < words[j].StartMs
	})
	return words
}
func transcriptChannel(w TranscriptWord) int {
	if w.Channel != nil {
		return *w.Channel
	}
	return 0
}

func transcriptSourceWords(t *TranscriptRow, media *MediaRow) ([]TranscriptWord, error) {
	if t.SourceSHA256 == "" || t.SourceSHA256 != media.SourceSHA256 {
		return nil, redactionError("transcript_source_mismatch", "transcript must match the current source hash")
	}
	var segments []TranscriptSegment
	if err := json.Unmarshal(t.Segments, &segments); err != nil {
		return nil, err
	}
	words := transcriptStoredWords(segments)
	if len(words) == 0 {
		return nil, redactionError("word_timing_unavailable", "transcript has no word timestamps; request media_transcribe with require_word_timings=true")
	}
	if timing := transcriptTimingOf(t); timing != nil {
		if !timing.Validated || timing.SourceSHA256 != media.SourceSHA256 || timing.Timebase != "transcript_audio" || timing.Scale != 1 || timing.AudioFileID <= 0 || timing.AudioSHA256 == "" || timing.SourceOffsetMs < 0 || timing.AudioDurationMs <= 0 {
			return nil, errors.New("transcript_audio_identity_unverified")
		}
		if err := validateTranscriptWords(words, timing.AudioDurationMs); err != nil {
			return nil, err
		}
		for i := range words {
			words[i].StartMs += timing.SourceOffsetMs
			words[i].EndMs += timing.SourceOffsetMs
		}
	} else if t.Provider == "deepgram" {
		// Historical imported words have no explicit mapping. Preserve the existing
		// zero-origin mapping, but do not claim validated proxy identity in summaries.
		offset, err := sourceAudioOffset(media)
		if err != nil {
			return nil, err
		}
		for i := range words {
			words[i].StartMs += offset
			words[i].EndMs += offset
		}
	}
	if err := validateTranscriptWords(words, media.DurationMs); err != nil {
		return nil, err
	}
	sort.SliceStable(words, func(i, j int) bool {
		if words[i].StartMs == words[j].StartMs {
			return transcriptChannel(words[i]) < transcriptChannel(words[j])
		}
		return words[i].StartMs < words[j].StartMs
	})
	return words, nil
}
