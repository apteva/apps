package main

// Local speech-to-text with NVIDIA Parakeet TDT 0.6B v3 (int8, sherpa-onnx,
// CPU). A transcript whose frozen model is parakeet-* runs here instead of the
// Deepgram integration: on the media executor when offloading is configured,
// otherwise on Media's own host. The runtime and model are provisioned on first
// use under a versioned root, every model file checked against a pinned hash.

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

//go:embed transcript_parakeet_runtime.py
var parakeetRuntime string

//go:embed transcript_parakeet_setup.py
var parakeetSetup string

const (
	parakeetModel          = "parakeet-tdt-0.6b-v3"
	parakeetRuntimeID      = "sherpa-onnx-2.0.2"
	parakeetModelID        = "parakeet-tdt-0.6b-v3-int8"
	parakeetRuntimeVersion = "parakeet-tdt-0.6b-v3-int8-sherpa-2.0.2-1"
	parakeetRemoteRoot     = "/var/tmp/apteva-media-asr/" + parakeetRuntimeVersion
	parakeetRemoteWork     = "/var/tmp/apteva-media-asr-work"
	// First use downloads ~490 MB and installs the runtime.
	parakeetSetupBudget = 900 * time.Second
	parakeetMaxTimeout  = 4 * time.Hour
)

// The 25 European languages Parakeet TDT 0.6B v3 was trained on.
var parakeetLanguages = map[string]bool{
	"bg": true, "hr": true, "cs": true, "da": true, "nl": true, "en": true, "et": true,
	"fi": true, "fr": true, "de": true, "el": true, "hu": true, "it": true, "lv": true,
	"lt": true, "mt": true, "pl": true, "pt": true, "ro": true, "sk": true, "sl": true,
	"es": true, "sv": true, "ru": true, "uk": true,
}

func isParakeetModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "parakeet")
}

func parakeetSupportsLanguage(language string) bool {
	if language == "auto" || language == "multi" {
		return true
	}
	base, _, _ := strings.Cut(strings.ToLower(language), "-")
	return parakeetLanguages[base]
}

// transcribeEngine is deepgram (default), parakeet, or auto.
func transcribeEngine(app *sdk.AppCtx) string {
	switch e := strings.ToLower(strings.TrimSpace(app.Config().Get("transcribe_engine"))); e {
	case "parakeet", "auto":
		return e
	}
	return "deepgram"
}

// defaultTranscriptionModel picks the model new jobs freeze when the request
// names none. auto prefers Parakeet unless the job needs diarization or a
// language it does not cover.
func defaultTranscriptionModel(app *sdk.AppCtx, language string, diarize bool) string {
	configured := firstNonEmpty(strings.TrimSpace(app.Config().Get("transcribe_model")), "nova-3")
	switch transcribeEngine(app) {
	case "parakeet":
		if isParakeetModel(configured) {
			return configured
		}
		return parakeetModel
	case "auto":
		if !diarize && parakeetSupportsLanguage(language) {
			return parakeetModel
		}
		if isParakeetModel(configured) {
			return "nova-3"
		}
	}
	return configured
}

// transcriptionConfigured reports whether automatic transcription can run at
// all: a bound Deepgram integration, or a local engine selected.
func transcriptionConfigured(app *sdk.AppCtx) bool {
	return app.IntegrationFor("transcripts") != nil || transcribeEngine(app) != "deepgram"
}

func parakeetTimeout(app *sdk.AppCtx, durationMs int64) time.Duration {
	configured := time.Duration(parseConfigIntFallback(app.Config().Get("transcribe_timeout_seconds"), 600)) * time.Second
	// Budget for a slow 2 vCPU host (~3x real time) on top of first-use setup.
	budget := parakeetSetupBudget + time.Duration(durationMs/3)*time.Millisecond + 2*time.Minute
	return min(max(configured, budget), parakeetMaxTimeout)
}

func parakeetThreads(app *sdk.AppCtx) int {
	n := parseConfigIntFallback(app.Config().Get("transcribe_parakeet_threads"), 4)
	return min(max(n, 1), 16)
}

type parakeetRequest struct {
	Audio   string  `json:"audio"`
	Threads int     `json:"threads"`
	ChunkS  float64 `json:"chunk_s"`
}

type parakeetResult struct {
	Runtime    string            `json:"runtime"`
	Model      string            `json:"model"`
	DurationMs int64             `json:"duration_ms"`
	Threads    int               `json:"threads"`
	Chunks     int               `json:"chunks"`
	LoadMs     int64             `json:"load_ms"`
	DecodeMs   int64             `json:"decode_ms"`
	Text       string            `json:"text"`
	Words      []json.RawMessage `json:"words"`
}

// runParakeet transcribes the validated transcription audio at audioURL.
func runParakeet(ctx context.Context, app *sdk.AppCtx, projectID, fileID, audioURL string) (*parakeetResult, string, error) {
	app = app.WithProject(projectID)
	ctx, release, err := acquireMediaWork(withMediaAdmissionObserver(ctx, "transcription_parakeet:"+projectID+":"+fileID, nil), app, 2)
	if err != nil {
		return nil, "", err
	}
	defer release()
	req := parakeetRequest{Audio: "audio.wav", Threads: parakeetThreads(app), ChunkS: 30}
	timeout := 60
	if deadline, ok := ctx.Deadline(); ok {
		timeout = max(1, int(math.Ceil(time.Until(deadline).Seconds())))
	}
	if componentExecutionID(app) > 0 {
		paths, err := sharedRemoteInstaller().Ensure(ctx, app, 1)
		if err != nil {
			return nil, "", fmt.Errorf("executor unavailable: %w", err)
		}
		script := buildParakeetRemoteScript(paths.FFmpeg, audioURL, req)
		out, exit, err := runRemote(ctx, app, 1, script, timeout)
		res, perr := parseParakeetOutput(out)
		if err != nil || exit != 0 {
			if perr != nil && ctx.Err() == nil {
				return nil, "executor", fmt.Errorf("parakeet executor exit=%d: %v: %s", exit, perr, transcriptAudioFailureTail(out, 600))
			}
			if err == nil {
				err = fmt.Errorf("exit=%d", exit)
			}
			return nil, "executor", fmt.Errorf("parakeet executor: %w", err)
		}
		return res, "executor", perr
	}
	res, err := runParakeetLocal(ctx, app, audioURL, req)
	return res, "local", err
}

func buildParakeetRemoteScript(ffmpeg, audioURL string, req parakeetRequest) string {
	raw, _ := json.Marshal(req)
	var b strings.Builder
	// The executor weighs plans that report APTEVA_PARAKEET: as two units.
	b.WriteString("set -eu\numask 077\n")
	fmt.Fprintf(&b, "mkdir -p %s\n", shellQuote(parakeetRemoteWork))
	fmt.Fprintf(&b, "WORK=$(mktemp -d %s)\n", shellQuote(parakeetRemoteWork+"/job-XXXXXX"))
	b.WriteString("trap 'cd /tmp && rm -rf \"$WORK\"' EXIT\n")
	for _, f := range []struct {
		name string
		data []byte
	}{{"setup.py", []byte(parakeetSetup)}, {"runtime.py", []byte(parakeetRuntime)}, {"request.json", raw}} {
		fmt.Fprintf(&b, "printf '%%s' %s | base64 -d > \"$WORK/%s\"\n", shellQuote(base64.StdEncoding.EncodeToString(f.data)), f.name)
	}
	fmt.Fprintf(&b, "ROOT=%s\n", shellQuote(parakeetRemoteRoot))
	b.WriteString("export TMPDIR=\"$WORK\" OMP_NUM_THREADS=1\n")
	b.WriteString("python3 \"$WORK/setup.py\" \"$ROOT\"\n")
	fmt.Fprintf(&b, "%s -nostdin -loglevel error -i %s -vn -map 0:a:0 -ac 1 -ar 16000 -c:a pcm_s16le -f wav \"$WORK/audio.wav\" || { printf 'APTEVA_PARAKEET_ERROR:{\"code\":\"parakeet_audio_decode_failed\"}\\n'; exit 1; }\n", shellQuote(ffmpeg), shellQuote(audioURL))
	b.WriteString("\"$ROOT/venv/bin/python\" \"$WORK/runtime.py\" \"$WORK/request.json\" \"$ROOT/model\"\n")
	return b.String()
}

func parakeetLocalRoot(app *sdk.AppCtx) (string, error) {
	if override := strings.TrimSpace(app.Config().Get("transcribe_parakeet_root")); override != "" {
		return override, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "apteva-media-asr", parakeetRuntimeVersion), nil
}

func runParakeetLocal(ctx context.Context, app *sdk.AppCtx, audioURL string, req parakeetRequest) (*parakeetResult, error) {
	root, err := parakeetLocalRoot(app)
	if err != nil {
		return nil, err
	}
	scratch := resolveScratchRoot(app, app.Config().Get("render_scratch_dir"))
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		scratch = os.TempDir()
	}
	work, err := os.MkdirTemp(scratch, "parakeet-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	raw, _ := json.Marshal(req)
	for name, data := range map[string][]byte{"setup.py": []byte(parakeetSetup), "runtime.py": []byte(parakeetRuntime), "request.json": raw} {
		if err := os.WriteFile(filepath.Join(work, name), data, 0o600); err != nil {
			return nil, err
		}
	}
	run := func(name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "TMPDIR="+work, "OMP_NUM_THREADS=1")
		configureRuntimeProcess(cmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("python3", filepath.Join(work, "setup.py"), root); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, perr := parseParakeetOutput(out); perr != nil {
			return nil, perr
		}
		return nil, errors.New("parakeet_runtime_unavailable")
	}
	ffmpeg := firstNonEmpty(strings.TrimSpace(app.Config().Get("ffmpeg_path")), "ffmpeg")
	if out, err := run(ffmpeg, "-nostdin", "-loglevel", "error", "-i", audioURL, "-vn", "-map", "0:a:0", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav", filepath.Join(work, "audio.wav")); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("parakeet_audio_decode_failed: %s", truncate(strings.TrimSpace(out), 300))
	}
	out, err := run(filepath.Join(root, "venv", "bin", "python"), filepath.Join(work, "runtime.py"), filepath.Join(work, "request.json"), filepath.Join(root, "model"))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	res, perr := parseParakeetOutput(out)
	if err != nil && perr == nil {
		perr = fmt.Errorf("parakeet runtime: %w", err)
	}
	return res, perr
}

// parseParakeetOutput finds the result (or error) line in runtime output.
func parseParakeetOutput(out string) (*parakeetResult, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "APTEVA_PARAKEET_ERROR:"); ok {
			var e struct {
				Code string `json:"code"`
			}
			if json.Unmarshal([]byte(rest), &e) == nil && e.Code != "" {
				return nil, errors.New(e.Code)
			}
			return nil, errors.New("parakeet_runtime_failed")
		}
		if rest, ok := strings.CutPrefix(line, "APTEVA_PARAKEET:"); ok {
			var r parakeetResult
			if err := json.Unmarshal([]byte(rest), &r); err != nil {
				return nil, fmt.Errorf("parakeet result: %w", err)
			}
			if r.Runtime != parakeetRuntimeID || r.Model != parakeetModelID {
				return nil, errors.New("parakeet_result_identity_mismatch")
			}
			return &r, nil
		}
	}
	return nil, errors.New("parakeet_result_missing")
}

// parakeetToParsed converts runtime output to Media's transcript shape.
// Segments end at sentence punctuation or a pause over one second.
func parakeetToParsed(r *parakeetResult, language, host string) (*parsedTranscript, error) {
	if r.DurationMs <= 0 {
		return nil, errors.New("parakeet result has no audio duration")
	}
	channel := 0
	words := make([]TranscriptWord, 0, len(r.Words))
	estimated := 0
	for _, raw := range r.Words {
		var tuple []json.RawMessage
		if err := json.Unmarshal(raw, &tuple); err != nil || len(tuple) != 5 {
			return nil, errors.New("invalid parakeet word")
		}
		var w TranscriptWord
		var conf *float64
		var est bool
		if json.Unmarshal(tuple[0], &w.StartMs) != nil || json.Unmarshal(tuple[1], &w.EndMs) != nil || json.Unmarshal(tuple[2], &w.Text) != nil || json.Unmarshal(tuple[3], &conf) != nil || json.Unmarshal(tuple[4], &est) != nil {
			return nil, errors.New("invalid parakeet word")
		}
		w.Confidence = conf
		ch := channel
		w.Channel = &ch
		if est {
			estimated++
		}
		words = append(words, w)
	}
	if err := validateTranscriptWords(words, r.DurationMs); err != nil {
		return nil, err
	}
	var segments []TranscriptSegment
	for i, w := range words {
		if len(segments) == 0 || segmentClosed(words, i) {
			ch := channel
			segments = append(segments, TranscriptSegment{StartMs: w.StartMs, Channel: &ch})
		}
		s := &segments[len(segments)-1]
		s.Words = append(s.Words, w)
		s.EndMs = max(s.EndMs, w.EndMs)
		if s.Text != "" {
			s.Text += " "
		}
		s.Text += w.Text
	}
	out := &parsedTranscript{
		Text:               strings.TrimSpace(r.Text),
		Segments:           segments,
		ProviderDurationMs: r.DurationMs,
		WordCount:          len(words),
		WordSource:         "parakeet_tdt",
	}
	if language != "auto" && language != "multi" {
		out.Language = language
	}
	if out.Text == "" && len(segments) > 0 {
		parts := make([]string, len(segments))
		for i, s := range segments {
			parts[i] = s.Text
		}
		out.Text = strings.Join(parts, " ")
	}
	if len(words) > 0 {
		out.LastWordEndMs = words[len(words)-1].EndMs
		for _, w := range words {
			out.LastWordEndMs = max(out.LastWordEndMs, w.EndMs)
		}
	}
	meta, _ := json.Marshal(map[string]any{
		"engine": "parakeet", "runtime": r.Runtime, "model": r.Model, "host": host,
		"duration": float64(r.DurationMs) / 1000, "threads": r.Threads, "chunks": r.Chunks,
		"load_ms": r.LoadMs, "decode_ms": r.DecodeMs, "estimated_words": estimated,
	})
	out.Metadata = meta
	return out, nil
}

// segmentClosed reports whether word i starts a new segment.
func segmentClosed(words []TranscriptWord, i int) bool {
	if i == 0 {
		return true
	}
	prev := words[i-1]
	if words[i].StartMs-prev.EndMs > 1000 {
		return true
	}
	t := strings.TrimSpace(prev.Text)
	return t != "" && strings.ContainsAny(t[len(t)-1:], ".?!")
}

// transcribeWithParakeet runs the whole Parakeet path for one row and returns
// the parsed transcript plus the validated audio it was computed from.
func transcribeWithParakeet(parent context.Context, app *sdk.AppCtx, sc *storageClient, media *MediaRow, opts *transcriptionOptions) (*parsedTranscript, *transcriptAudioEvidence, error) {
	if opts.Diarize {
		return nil, nil, errors.New("parakeet_diarization_unsupported: use a Deepgram model for speaker labels")
	}
	if !parakeetSupportsLanguage(opts.Language) {
		return nil, nil, fmt.Errorf("parakeet_language_unsupported: %s", opts.Language)
	}
	ctx, cancel := context.WithTimeout(parent, parakeetTimeout(app, media.DurationMs))
	defer cancel()
	audioURL, evidence, err := deepgramInput(ctx, app, sc, media)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare transcript audio: %w", err)
	}
	res, host, err := runParakeet(ctx, app, media.ProjectID, media.FileID, audioURL)
	if err != nil {
		return nil, evidence, err
	}
	parsed, err := parakeetToParsed(res, opts.Language, host)
	if err != nil {
		return nil, evidence, err
	}
	parsed.RequestID = "parakeet-" + media.FileID + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	return parsed, evidence, nil
}
