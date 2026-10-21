package main

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	sdk "github.com/apteva/app-sdk"
)

const audioRedactionVersion = "media-audio-redact-1"

type audioInterval struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}
type audioSelection struct {
	Text       string `json:"text,omitempty"`
	Occurrence int    `json:"occurrence,omitempty"`
	StartMs    *int64 `json:"start_ms,omitempty"`
	EndMs      *int64 `json:"end_ms,omitempty"`
}
type audioRedactParams struct {
	Selections       []audioSelection    `json:"selections"`
	Method           string              `json:"method"`
	Preview          bool                `json:"preview"`
	PaddingMs        *int64              `json:"padding_ms,omitempty"`
	FadeMs           *int64              `json:"fade_ms,omitempty"`
	RoomTone         *audioInterval      `json:"room_tone,omitempty"`
	Plan             *audioRedactionPlan `json:"_redaction_plan,omitempty"`
	SourceSampleRate int                 `json:"_source_sample_rate,omitempty"`
}
type audioRedactionPlan struct {
	AudioStartMs     int64           `json:"audio_start_ms"`
	AlgorithmVersion string          `json:"algorithm_version"`
	SourceSHA256     string          `json:"source_sha256"`
	SourceDurationMs int64           `json:"source_duration_ms"`
	Method           string          `json:"method"`
	Ranges           []audioInterval `json:"ranges"`
	FadeMs           int64           `json:"fade_ms"`
	Preview          bool            `json:"preview"`
	StartMs          int64           `json:"start_ms"`
	EndMs            int64           `json:"end_ms"`
	RoomTone         *audioInterval  `json:"room_tone,omitempty"`
}

func redactionError(code, message string) error { return fmt.Errorf("%s: %s", code, message) }
func wordTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '\'' && r != '’' })
}

// Never interpolate sentence timing into invented word boundaries.
func resolveAudioSelections(selections []audioSelection, words []TranscriptWord, duration int64) ([]audioInterval, error) {
	if len(selections) == 0 || len(selections) > 32 {
		return nil, redactionError("invalid_audio_selection", "provide 1..32 selections")
	}
	var ranges []audioInterval
	for _, s := range selections {
		var r audioInterval
		if s.Text != "" {
			if s.StartMs != nil || s.EndMs != nil || s.Occurrence < 0 {
				return nil, redactionError("invalid_audio_selection", "use text with optional positive occurrence, or start_ms/end_ms")
			}
			needle := wordTokens(s.Text)
			if len(needle) == 0 || len(needle) > 100 {
				return nil, redactionError("invalid_audio_selection", "text must contain 1..100 words")
			}
			if len(words) == 0 {
				return nil, redactionError("word_timing_unavailable", "transcript has no word timing; retranscribe, import timed words, or provide timestamps")
			}
			var matches []audioInterval
			for i := 0; i+len(needle) <= len(words); i++ {
				match := true
				for j, t := range needle {
					tok := wordTokens(words[i+j].Text)
					if len(tok) != 1 || tok[0] != t {
						match = false
						break
					}
				}
				if match {
					matches = append(matches, audioInterval{words[i].StartMs, words[i+len(needle)-1].EndMs})
				}
			}
			if len(matches) == 0 {
				return nil, redactionError("audio_selection_not_found", "text does not match timed transcript words")
			}
			if s.Occurrence == 0 && len(matches) > 1 {
				return nil, redactionError("ambiguous_audio_selection", "text occurs more than once; specify occurrence (1-based)")
			}
			occurrence := s.Occurrence
			if occurrence == 0 {
				occurrence = 1
			}
			if occurrence > len(matches) {
				return nil, redactionError("audio_selection_not_found", "occurrence exceeds available matches")
			}
			r = matches[occurrence-1]
		} else {
			if s.StartMs == nil || s.EndMs == nil || s.Occurrence != 0 {
				return nil, redactionError("invalid_audio_selection", "provide both start_ms and end_ms")
			}
			r = audioInterval{*s.StartMs, *s.EndMs}
		}
		if r.StartMs < 0 || r.EndMs <= r.StartMs || r.EndMs > duration {
			return nil, redactionError("timestamp_out_of_range", "redaction must be within the source audio duration")
		}
		ranges = append(ranges, r)
	}
	return ranges, nil
}

func makeAudioRedactionPlan(p audioRedactParams, words []TranscriptWord, duration int64, hash string) (*audioRedactionPlan, error) {
	if duration <= 0 || hash == "" {
		return nil, redactionError("audio_source_unverified", "index the audio source before redaction")
	}
	if p.Method == "" {
		p.Method = "room_tone"
	}
	switch p.Method {
	case "room_tone", "mute", "bleep", "muffle":
	default:
		return nil, redactionError("invalid_audio_method", "method must be room_tone, mute, bleep or muffle")
	}
	padding, fade := int64(40), int64(10)
	if p.PaddingMs != nil {
		padding = *p.PaddingMs
	}
	if p.FadeMs != nil {
		fade = *p.FadeMs
	}
	if padding < 0 || padding > 250 || fade < 0 || fade > 50 {
		return nil, redactionError("invalid_audio_selection", "padding_ms must be 0..250; fade_ms must be 0..50")
	}
	ranges, err := resolveAudioSelections(p.Selections, words, duration)
	if err != nil {
		return nil, err
	}
	for i := range ranges {
		ranges[i].StartMs = max(0, ranges[i].StartMs-padding)
		ranges[i].EndMs = min(duration, ranges[i].EndMs+padding)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].StartMs < ranges[j].StartMs })
	merged := []audioInterval{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.StartMs <= last.EndMs+2*fade {
			last.EndMs = max(last.EndMs, r.EndMs)
		} else {
			merged = append(merged, r)
		}
	}
	plan := &audioRedactionPlan{AlgorithmVersion: audioRedactionVersion, SourceSHA256: hash, SourceDurationMs: duration, Method: p.Method, Ranges: merged, FadeMs: fade, Preview: p.Preview, StartMs: 0, EndMs: duration, RoomTone: p.RoomTone}
	if p.Method == "room_tone" {
		r := p.RoomTone
		if r == nil {
			return nil, redactionError("room_tone_required", "provide room_tone {start_ms,end_ms} from a verified background-only passage; speech-free ambience is never guessed")
		}
		if r.StartMs < 0 || r.EndMs > duration || r.EndMs-r.StartMs < 100 || r.EndMs-r.StartMs > 5000 {
			return nil, redactionError("invalid_room_tone", "room_tone must be 100..5000 ms within the source")
		}
		for _, x := range merged {
			if r.StartMs < x.EndMs+fade && r.EndMs > x.StartMs-fade {
				return nil, redactionError("invalid_room_tone", "room_tone overlaps a redacted passage or fade")
			}
		}
	} else if p.RoomTone != nil {
		return nil, redactionError("invalid_room_tone", "room_tone is only used by method=room_tone")
	}
	if p.Preview {
		plan.StartMs = max(0, merged[0].StartMs-1500)
		plan.EndMs = min(duration, merged[len(merged)-1].EndMs+1500)
		if plan.EndMs-plan.StartMs > 30000 {
			return nil, redactionError("preview_too_wide", "preview spans over 30 seconds; preview nearby selections separately")
		}
	}
	return plan, nil
}

func prepareAudioRedactionSubmission(app *sdk.AppCtx, project, op string, sources []string, raw json.RawMessage) (json.RawMessage, error) {
	if op != "redact_audio" {
		return raw, nil
	}
	if len(sources) != 1 {
		return nil, redactionError("invalid_audio_selection", "redact_audio requires one source")
	}
	var p audioRedactParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	media, err := getMedia(app.AppDB(), project, sources[0])
	if err != nil || !media.HasAudio || media.ProbeStatus != "ok" {
		return nil, redactionError("audio_source_unverified", "source must have an indexed audio stream")
	}
	// Provider word timing starts at decoded audio zero. Video-source selections
	// use the source presentation timeline, including any leading audio gap.
	var timeline struct {
		Format struct {
			StartTime string `json:"start_time"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			StartTime string `json:"start_time"`
		} `json:"streams"`
	}
	_ = json.Unmarshal(media.RawProbe, &timeline)
	origin, _ := strconv.ParseFloat(timeline.Format.StartTime, 64)
	if media.HasVideo && math.Abs(origin) > .002 {
		return nil, redactionError("audio_source_unverified", "use a zero-origin source before audio redaction")
	}
	audioStart := int64(0)
	for _, stream := range timeline.Streams {
		if stream.CodecType == "audio" {
			start, _ := strconv.ParseFloat(stream.StartTime, 64)
			audioStart = int64(math.Round((start - origin) * 1000))
			break
		}
	}
	var words []TranscriptWord
	needsWords := false
	for _, s := range p.Selections {
		needsWords = needsWords || s.Text != ""
	}
	if needsWords {
		tr, err := getTranscript(app.AppDB(), project, sources[0])
		if err != nil || tr.Status != "ok" {
			return nil, redactionError("word_timing_unavailable", "a completed transcript with word timing is required")
		}
		if tr.SourceSHA256 == "" || tr.SourceSHA256 != media.SourceSHA256 {
			return nil, redactionError("transcript_source_mismatch", "transcript must match the current source hash")
		}
		if len(tr.Segments) == 0 {
			return nil, redactionError("word_timing_unavailable", "transcript has no timed words; retranscribe or provide timestamps")
		}
		var segments []TranscriptSegment
		if err := json.Unmarshal(tr.Segments, &segments); err != nil {
			return nil, err
		}
		for _, s := range segments {
			words = append(words, s.Words...)
		}
		if tr.Provider == "deepgram" {
			for i := range words {
				words[i].StartMs += audioStart
				words[i].EndMs += audioStart
			}
		}
		if err := validateTranscriptWords(words, media.DurationMs); err != nil {
			return nil, err
		}
	}
	plan, err := makeAudioRedactionPlan(p, words, media.DurationMs, media.SourceSHA256)
	if err != nil {
		return nil, err
	}
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	plan.AudioStartMs = audioStart
	params["_redaction_plan"] = plan
	params["_source_sample_rate"] = media.SampleRate
	return json.Marshal(params)
}

func validateTranscriptWords(words []TranscriptWord, duration int64) error {
	if len(words) > 100000 {
		return redactionError("invalid_word_timing", "too many timed words")
	}
	var previous int64
	for i, w := range words {
		if strings.TrimSpace(w.Text) == "" || w.StartMs < 0 || w.EndMs <= w.StartMs || (duration > 0 && w.EndMs > duration) || (i > 0 && w.StartMs < previous) {
			return redactionError("invalid_word_timing", "words must have nonempty text and ordered, valid source intervals")
		}
		previous = w.StartMs
	}
	return nil
}

func planAudioRedact(sources []string, raw json.RawMessage, outputName, sourceExt string) (*opPlan, error) {
	var p audioRedactParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if len(sources) != 1 || p.Plan == nil {
		return nil, redactionError("audio_source_unverified", "redaction requires a resolved source-bound plan")
	}
	q := p.Plan
	if q.AlgorithmVersion != audioRedactionVersion || q.EndMs <= q.StartMs || len(q.Ranges) == 0 {
		return nil, redactionError("invalid_audio_selection", "invalid resolved plan")
	}
	if p.Preview {
		if outputName == "" {
			outputName = "redact-preview-" + sources[0] + ".m4a"
		}
		if !isAudioExt(strings.ToLower(filepath.Ext(outputName))) {
			return nil, invalidOutputFormat("redaction preview requires an audio output")
		}
	}
	name, ct, audioOnly := audioFilterOutput(outputName, sources[0], sourceExt)
	if outputName == "" {
		name = "redact-" + sources[0] + filepath.Ext(name)
	}
	codec, err := audioFilterCodecForOutput(filepath.Ext(name), audioOnly)
	if err != nil {
		return nil, err
	}
	rate := p.SourceSampleRate
	if rate == 0 {
		rate = 48000
	}
	// All user text has been resolved to numeric geometry before filter assembly.
	masks := []string{}
	for _, r := range q.Ranges {
		a, b := float64(r.StartMs-q.StartMs)/1000, float64(r.EndMs-q.StartMs)/1000
		f := float64(q.FadeMs) / 1000
		if f == 0 {
			masks = append(masks, fmt.Sprintf("between(t,%s,%s)", ffmpegFloat(a), ffmpegFloat(b)))
		} else {
			masks = append(masks, fmt.Sprintf("max(0,min(1,min((t-%s)/%s,(%s-t)/%s)))", ffmpegFloat(a-f), ffmpegFloat(f), ffmpegFloat(b+f), ffmpegFloat(f)))
		}
	}
	mask := masks[0]
	for _, m := range masks[1:] {
		mask = "max(" + mask + "," + m + ")"
	}
	duration := msToSeconds(q.EndMs - q.StartMs)
	args := []string{"-y", "-loglevel", "error", "-progress", "pipe:1"}
	if q.Preview {
		args = append(args, "-ss", msToSeconds(q.StartMs))
	}
	args = append(args, "-i", "{input}")
	if q.Method == "room_tone" {
		args = append(args, "-ss", msToSeconds(q.RoomTone.StartMs), "-t", msToSeconds(q.RoomTone.EndMs-q.RoomTone.StartMs), "-i", "{input}")
	}
	// Represent a genuine leading audio gap as silence from timeline zero.
	// This also prevents AAC priming from pulling a delayed stream earlier.
	sourceChain := "aresample=async=1:first_pts=0,atrim=duration=" + duration
	graph := "[0:a:0]" + sourceChain + ",aeval=exprs='val(ch)*(1-(" + mask + "))':c=same[dry]"
	switch q.Method {
	case "mute":
		graph += ";[dry]anull[out]"
	case "muffle":
		graph += ";[0:a:0]" + sourceChain + ",lowpass=f=250,volume=0.15,aeval=exprs='val(ch)*(" + mask + ")':c=same[wet];[dry][wet]amix=inputs=2:duration=first:normalize=0[out]"
	case "bleep":
		graph += ";[0:a:0]" + sourceChain + ",aeval=exprs='0.12*sin(2*PI*1000*t)*(" + mask + ")':c=same[wet];[dry][wet]amix=inputs=2:duration=first:normalize=0[out]"
	case "room_tone":
		n := (q.RoomTone.EndMs - q.RoomTone.StartMs) * int64(rate) / 1000
		// Fade donor seams to avoid clicks at each repeat.
		graph += ";[1:a:0]aresample=" + fmt.Sprint(rate) + ",asetpts=PTS-STARTPTS,afade=t=in:d=0.005,afade=t=out:st=" + ffmpegFloat(float64(q.RoomTone.EndMs-q.RoomTone.StartMs)/1000-.005) + ":d=0.005,aloop=loop=-1:size=" + fmt.Sprint(n) + ",atrim=duration=" + duration + ",aeval=exprs='val(ch)*(" + mask + ")':c=same[wet];[dry][wet]amix=inputs=2:duration=first:normalize=0[out]"
	default:
		return nil, redactionError("invalid_audio_method", "unsupported resolved method")
	}
	args = append(args, "-filter_complex", graph, "-map", "[out]")
	if audioOnly {
		args = append(args, "-vn")
	} else {
		args = append(args, "-map", "0:v?", "-c:v", "copy")
	}
	args = append(args, "-c:a", codec, "-ar", fmt.Sprint(rate))
	if codec == "aac" {
		args = append(args, "-aac_pns", "0", "-b:a", "192k")
	}
	if q.Preview {
		args = append(args, "-t", duration)
	}
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

func verifyRedactionSource(raw json.RawMessage, hash string) error {
	var p audioRedactParams
	_ = json.Unmarshal(raw, &p)
	if p.Plan == nil || p.Plan.SourceSHA256 == "" || p.Plan.SourceSHA256 != hash {
		return redactionError("audio_source_unverified", "source changed after selection; prepare a new redaction")
	}
	return nil
}
func audioRedactionReceipt(raw json.RawMessage) any {
	var p audioRedactParams
	_ = json.Unmarshal(raw, &p)
	if p.Plan == nil {
		return nil
	}
	return p.Plan
}
func (a *App) audioRedactTool() sdk.Tool {
	return sdk.Tool{
		Name:        "media_redact_audio",
		Description: "Redact words or source-time passages without changing full-output duration or video. Methods: room_tone (default; requires an explicit verified speech-free donor interval), mute, bleep, muffle (an effect, not guaranteed unintelligible). Text requires source-matched recorded word timing; repeated phrases require 1-based occurrence. Preview returns an audio-only excerpt up to 30 seconds. Returns render_id; use media_get_render for progress and output. Original files are retained.",
		InputSchema: schemaObject(map[string]any{
			"file_id": map[string]any{"type": "string"},
			"selections": map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": schemaObject(map[string]any{
				"text": map[string]any{"type": "string"}, "occurrence": map[string]any{"type": "integer", "minimum": 1},
				"start_ms": map[string]any{"type": "integer", "minimum": 0}, "end_ms": map[string]any{"type": "integer", "minimum": 1},
			}, nil)},
			"method":      map[string]any{"type": "string", "enum": []string{"room_tone", "mute", "bleep", "muffle"}, "default": "room_tone"},
			"room_tone":   schemaObject(map[string]any{"start_ms": map[string]any{"type": "integer"}, "end_ms": map[string]any{"type": "integer"}}, []string{"start_ms", "end_ms"}),
			"padding_ms":  map[string]any{"type": "integer", "minimum": 0, "maximum": 250, "default": 40},
			"fade_ms":     map[string]any{"type": "integer", "minimum": 0, "maximum": 50, "default": 10},
			"preview":     map[string]any{"type": "boolean", "default": false},
			"output_name": map[string]any{"type": "string"}, "output_folder": map[string]any{"type": "string"}, "timeout_seconds": timeoutSchema(),
		}, []string{"file_id", "selections"}),
		Handler: a.toolSubmitRender("redact_audio", []string{"selections", "method", "room_tone", "padding_ms", "fade_ms", "preview"}, []string{"file_id"}),
	}
}
