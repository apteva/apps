package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed smartcrop_pose_runtime.py
var poseRuntime string

//go:embed smartcrop_pose_setup.py
var poseSetup string

//go:embed smartcrop_model/pose_landmarker_full.task
var poseModel []byte

const poseModelSHA256 = "4eaa5eb7a98365221087693fcc286334cf0858e2eb6e15b506aa4a7ecdcec4ad"
const poseRuntimeVersion = "mediapipe-0.10.21-full-1"

func smartCropEngineSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"mediapipe_full", "legacy"}, "description": "Smart Crop engine. Defaults to app configuration (MediaPipe Pose Full). legacy retains the previous saliency/foreground engine. Runtime or insufficient pose evidence falls back visibly to legacy; sampled coverage requires visual review."}
}
func resolveSmartCropEngine(app *sdk.AppCtx, requested string) (string, error) {
	engine := strings.TrimSpace(strings.ToLower(requested))
	if engine == "" && app != nil {
		engine = strings.TrimSpace(strings.ToLower(app.Config().Get("smart_crop_engine")))
	}
	if engine == "" {
		engine = "mediapipe_full"
	}
	if engine != "mediapipe_full" && engine != "legacy" {
		return "", &renderInputError{Code: "invalid_smart_crop_engine", Message: "smart_crop_engine must be mediapipe_full or legacy."}
	}
	return engine, nil
}
func validateSmartCropEngine(raw []byte) error {
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if p["smart_crop_engine"] == nil {
		return nil
	}
	value, ok := p["smart_crop_engine"].(string)
	if !ok {
		return &renderInputError{Code: "invalid_smart_crop_engine", Message: "smart_crop_engine must be a string."}
	}
	_, err := resolveSmartCropEngine(nil, value)
	return err
}

type poseRequest struct {
	Source    string  `json:"source"`
	Model     string  `json:"model"`
	ModelSHA  string  `json:"model_sha256"`
	FFmpeg    string  `json:"ffmpeg"`
	Positions []int64 `json:"positions"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	RatioW    int     `json:"ratio_w"`
	RatioH    int     `json:"ratio_h"`
	Remaining float64 `json:"remaining_seconds"`
	Video     bool    `json:"video"`
}
type poseSample struct {
	ExtractionAttempts int    `json:"extraction_attempts,omitempty"`
	WeakWrists         []int  `json:"low_confidence_wrist_indices,omitempty"`
	SupportedIndices   []int  `json:"required_landmark_indices,omitempty"`
	SourceClipped      []bool `json:"subject_extent_clipped_by_source,omitempty"`

	AtMs   int64  `json:"at_ms"`
	Status string `json:"status"`
	Crop   struct {
		X      int `json:"x"`
		Y      int `json:"y"`
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"crop"`
	Bounds      []float64 `json:"required_bounds"`
	Head        []float64 `json:"head_bounds_estimate"`
	InferenceMs float64   `json:"inference_ms"`
}
type poseResult struct {
	Samples  []poseSample `json:"samples"`
	Runtime  string       `json:"runtime"`
	ModelSHA string       `json:"model_sha256"`
}

type poseRuntimeFailure struct {
	Code     string `json:"code"`
	AtMs     *int64 `json:"at_ms,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}

func (e *poseRuntimeFailure) Error() string { return e.Code }

func parsePoseRuntimeFailure(output string) *poseRuntimeFailure {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "APTEVA_POSE_ERROR:") {
			continue
		}
		var failure poseRuntimeFailure
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "APTEVA_POSE_ERROR:")), &failure) != nil {
			continue
		}
		switch failure.Code {
		case "pose_source_frame_unavailable", "pose_source_read_timeout", "pose_runtime_version_mismatch", "pose_model_hash_mismatch", "pose_sample_budget_exceeded", "pose_source_geometry_mismatch", "pose_inference_failed":
		default:
			continue
		}
		if failure.Attempts < 0 || failure.Attempts > 3 || (failure.AtMs != nil && *failure.AtMs < 0) {
			continue
		}
		return &failure
	}
	return nil
}

func posePositions(target smartCropTarget, duration int64, fps float64) []int64 {
	if !target.HasRange() {
		return []int64{target.FocusMs}
	}
	end := target.EndMs - 1
	if duration > 0 {
		// Duration includes the last picture's display interval. FFmpeg seeks
		// forward, so duration-1 can yield no picture. Leave two nominal
		// frame intervals at the source edge and retain the actual seek in
		// evidence. VFR or inaccurate duration can still fail visibly.
		if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) {
			fps = 25
		}
		end = min(end, max(target.StartMs, duration-int64(math.Ceil(2000/fps))))
	}
	count := min(256, max(2, int((end-target.StartMs)/500)+2))
	out := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, target.StartMs+int64(i)*(end-target.StartMs)/int64(count-1))
	}
	return uniqueSortedSmartCropPositions(out)
}
func runPose(ctx context.Context, app *sdk.AppCtx, host int64, req poseRequest) (*poseResult, error) {
	// One deadline includes provisioning, extraction and inference.
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	req.Remaining = 120
	if deadline, ok := ctx.Deadline(); ok {
		req.Remaining = min(120, time.Until(deadline).Seconds())
	}
	var output string
	if host > 0 {
		req.Model = "/tmp/apteva-media-pose/" + poseRuntimeVersion + "/model.task"
		raw, _ := json.Marshal(req)
		work := uniqueRemoteWorkDir(0) + "-pose"
		script := "set -eu\numask 077\nWORK=" + shellQuote(work) + "\nmkdir -p \"$WORK\"\necho $$ > \"$WORK/pid\"\n[ ! -f \"$WORK/cancel.requested\" ] || exit 1\ntrap 'rm -rf \"$WORK\"' EXIT\n"
		// Base64 avoids shell interpolation of private source URLs and request data.
		for _, file := range []struct {
			name string
			data []byte
		}{{"runtime.py", []byte(poseRuntime)}, {"setup.py", []byte(poseSetup)}, {"request.json", raw}} {
			script += fmt.Sprintf("printf '%%s' %s | base64 -d > \"$WORK/%s\"\n", shellQuote(base64.StdEncoding.EncodeToString(file.data)), file.name)
		}
		script += "POSE_ROOT=/tmp/apteva-media-pose/" + poseRuntimeVersion + "\npython3 \"$WORK/setup.py\" \"$POSE_ROOT\"\n"
		// The model is verified in the persistent runtime directory.
		script += "\"$POSE_ROOT/venv/bin/python\" \"$WORK/runtime.py\" \"$WORK/request.json\""
		finish := registerRemoteKill(ctx, app, host, 0, work)
		timeout := 300
		if deadline, ok := ctx.Deadline(); ok {
			timeout = max(1, int(math.Ceil(time.Until(deadline).Seconds())))
		}
		out, code, err := runRemote(ctx, app, host, "setsid bash -c "+shellQuote(script), timeout)
		if stopErr := finish(); stopErr != nil {
			return nil, fmt.Errorf("pose_cancellation_failed")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || code != 0 {
			if failure := parsePoseRuntimeFailure(out); failure != nil {
				return nil, failure
			}
			return nil, fmt.Errorf("pose_runtime_unavailable")
		}
		output = out
	} else {
		dir, err := os.MkdirTemp("", "apteva-pose-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		req.Model = filepath.Join(dir, "model.task")
		raw, _ := json.Marshal(req)
		for name, data := range map[string][]byte{"runtime.py": []byte(poseRuntime), "setup.py": []byte(poseSetup), "model.task": poseModel, "request.json": raw} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				return nil, err
			}
		}
		root, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(root, "apteva-media-pose", poseRuntimeVersion)
		python := strings.TrimSpace(app.Config().Get("smart_crop_python"))
		if python == "" {
			setup := exec.CommandContext(ctx, "python3", filepath.Join(dir, "setup.py"), root)
			configureRuntimeProcess(setup)
			if err := setup.Run(); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, fmt.Errorf("pose_runtime_unavailable")
			}
			python = filepath.Join(root, "venv", "bin", "python")
		}
		cmd := exec.CommandContext(ctx, python, filepath.Join(dir, "runtime.py"), filepath.Join(dir, "request.json"))
		configureRuntimeProcess(cmd)
		out, err := cmd.Output()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if failure := parsePoseRuntimeFailure(string(out)); failure != nil {
				return nil, failure
			}
			return nil, fmt.Errorf("pose_inference_failed")
		}
		output = string(out)
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "APTEVA_POSE:") {
			var result poseResult
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "APTEVA_POSE:")), &result); err != nil {
				return nil, err
			}
			if result.ModelSHA != poseModelSHA256 || result.Runtime != "mediapipe-0.10.21" || len(result.Samples) != len(req.Positions) {
				return nil, fmt.Errorf("pose_evidence_identity_mismatch")
			}
			for i, s := range result.Samples {
				if s.AtMs != req.Positions[i] {
					return nil, fmt.Errorf("pose_timestamp_identity_mismatch")
				}
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("pose_result_missing")
}

func computeSmartCropPose(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project, fid string, rw, rh int, target smartCropTarget) (*cropWindow, []cropPathPoint, error) {
	ctx, release, err := acquireMediaWork(ctx, app, 1)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	row, err := getMedia(app.AppDB(), project, fid)
	if err != nil || row == nil || row.Width <= 0 || row.Height <= 0 {
		return nil, nil, fmt.Errorf("pose_source_geometry_unavailable")
	}
	host := remoteIndexerHostID(app)
	source := ""
	if paths, ok := ctx.Value(renderSourcesKey{}).(map[string]string); ok && paths[fid] != "" {
		source = paths[fid]
		host = 0
	}
	if source == "" {
		id, _ := strconv.ParseInt(fid, 10, 64)
		source, err = sc.GetSignedURL(ctx, project, id, 900)
		if err != nil {
			return nil, nil, fmt.Errorf("pose_source_unavailable")
		}
		if host > 0 {
			if meta, e := sc.GetFile(ctx, project, id); e == nil && len(meta.SHA256) == 64 {
				cached := filepath.Join(remoteSourceCacheRoot, remoteSourceCacheName(fid, sanitizeFilename(meta.Name), meta.SHA256, meta.SizeBytes))
				check := remoteSourceCacheScriptFragment + fmt.Sprintf("\nif cache_valid %s %d %s; then echo CACHE_READY; fi\n", shellQuote(cached), meta.SizeBytes, shellQuote(meta.SHA256))
				if out, _, e := runRemote(ctx, app, host, check, 10); e == nil && strings.Contains(out, "CACHE_READY") {
					source = cached
				}
			}
		}
	}
	ffmpeg := strings.TrimSpace(app.Config().Get("ffmpeg_path"))
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if host > 0 {
		paths, e := sharedRemoteInstaller().Ensure(ctx, app, host)
		if e != nil {
			return nil, nil, fmt.Errorf("pose_ffmpeg_unavailable")
		}
		ffmpeg = paths.FFmpeg
	}
	result, err := runPose(ctx, app, host, poseRequest{Source: source, ModelSHA: poseModelSHA256, FFmpeg: ffmpeg, Positions: posePositions(target, row.DurationMs, row.FPS), Width: row.Width, Height: row.Height, RatioW: rw, RatioH: rh, Video: !row.IsImage})
	if err != nil {
		if failure, ok := err.(*poseRuntimeFailure); ok {
			if a := cropAudit(ctx); a != nil {
				a.PoseFailure = failure
			}
		}
		return nil, nil, err
	}
	win, path, err := planPoseSamples(result.Samples, row.Width, row.Height, rw, rh)
	if err != nil {
		return nil, nil, err
	}
	if a := cropAudit(ctx); a != nil {
		a.AlgorithmVersion = smartCropAlgorithmVersion
		a.Method = "mediapipe_full:exact_source_pose"
		a.EffectiveEngine = "mediapipe_full"
		a.ModelSHA = poseModelSHA256
		a.RuntimeVersion = poseRuntimeVersion
		a.Coverage = "sampled_extent_fits"
		a.PoseSamples = result.Samples
		a.PoseLimitations = []string{"Head/hair and hand bounds are estimated from landmarks; pose confidence is not visual approval.", "Upper-body portrait policy does not require full legs; wider actions may not fit.", "Video coverage is sampled (up to 256 frames); extraction timestamps identify requested FFmpeg seeks and can differ from picture presentation by one frame. End-of-source samples leave two nominal frame intervals to avoid empty seeks."}
		for _, s := range result.Samples {
			recordSmartCropEvidence(ctx, "native_pose", s.AtMs, "")
			b := poseBoundsWindow(s.Bounds, row.Width, row.Height)
			a.Extents = append(a.Extents, smartCropExtentEvidence{AtMs: s.AtMs, Bounds: auditCropWindow(b), Support: s.Status})
			if b.W > win.W {
				a.Coverage = "exceeds_crop_width"
				a.Recommendation = "Use a wider ratio or explicitly request fit_mode: contain to preserve the full action."
			} else if s.Status != "fits_detected_upper_pose" && a.Coverage != "exceeds_crop_width" {
				a.Coverage = "unknown"
				a.Recommendation = "Pose evidence is incomplete; review the source and crop."
			}
		}
		if a.Coverage == "sampled_extent_fits" {
			a.Effective = func() *smartCropAuditWindow { v := auditCropWindow(*win); return &v }()
			a.Path = path
			if !cropRetainsSampledExtents(a) {
				a.Coverage = "unknown"
				a.Recommendation = "The movement envelope does not fit; review the source or explicitly request contain."
			}
		}
	}
	return win, path, nil
}
func poseBoundsWindow(b []float64, w, h int) cropWindow {
	if len(b) != 4 {
		return cropWindow{}
	}
	x, y := max(0, int(math.Floor(b[0]))), max(0, int(math.Floor(b[1])))
	return cropWindow{X: x, Y: y, W: min(w, int(math.Ceil(b[2]))) - x, H: min(h, int(math.Ceil(b[3]))) - y}
}
func planPoseSamples(samples []poseSample, w, h, rw, rh int) (*cropWindow, []cropPathPoint, error) {
	if len(samples) == 0 {
		return nil, nil, fmt.Errorf("pose_no_samples")
	}
	cw, ch := 0, 0
	top, bottom := h, 0
	for _, s := range samples {
		if len(s.Bounds) != 4 || s.Crop.Width <= 0 || s.Crop.Height <= 0 {
			return nil, nil, fmt.Errorf("pose_insufficient_evidence")
		}
		if s.Bounds[0] < 0 || s.Bounds[1] < 0 || s.Bounds[2] > float64(w) || s.Bounds[3] > float64(h) || s.Bounds[2] <= s.Bounds[0] || s.Bounds[3] <= s.Bounds[1] || s.Crop.Width > w || s.Crop.Height > h {
			return nil, nil, fmt.Errorf("pose_invalid_geometry")
		}
		cw = max(cw, s.Crop.Width)
		ch = max(ch, s.Crop.Height)
		b := poseBoundsWindow(s.Bounds, w, h)
		top = min(top, b.Y)
		bottom = max(bottom, b.Y+b.H)
	}
	// Fixed Y and size retain the vertical movement envelope. Only X tracks;
	// existing renderers and strict preflight share these exact semantics.
	needed := max(ch, bottom-top)
	maxW, maxH := cropDimsForRatio(w, h, rw, rh)
	ch = min(maxH, (needed+1)&^1)
	cw = min(maxW, (int(math.Ceil(float64(ch)*float64(rw)/float64(rh)))+1)&^1)
	y := clampInt(top-int(float64(ch)*.055), max(0, bottom-ch), min(h-ch, top))
	if bottom-top > ch {
		y = clampInt(top, 0, h-ch)
	}
	path := make([]cropPathPoint, len(samples))
	for i, s := range samples {
		b := poseBoundsWindow(s.Bounds, w, h)
		// Median smoothing reduces landmark jitter; project back into feasible
		// subject bounds so smoothing cannot displace a fitting pose.
		xs := []int{}
		for j := max(0, i-2); j <= min(len(samples)-1, i+2); j++ {
			xs = append(xs, samples[j].Crop.X+samples[j].Crop.Width/2)
		}
		sort.Ints(xs)
		x := xs[len(xs)/2] - cw/2
		if b.W <= cw {
			x = clampInt(x, max(0, b.X+b.W-cw), min(w-cw, b.X))
		}
		path[i] = cropPathPoint{AtMs: s.AtMs, X: clampInt(x, 0, w-cw), Y: y}
	}
	return &cropWindow{W: cw, H: ch, X: path[0].X, Y: y}, path, nil
}

// Save stable failure codes only; filesystem paths/provider URLs are private.
func poseFailureReason(err error) string {
	if err == nil {
		return "pose_analysis_unavailable"
	}
	if err == context.Canceled {
		return "analysis_cancelled"
	}
	if err == context.DeadlineExceeded {
		return "analysis_timeout"
	}
	if failure, ok := err.(*poseRuntimeFailure); ok {
		return failure.Code
	}
	switch err.Error() {
	case "pose_source_geometry_unavailable", "pose_source_unavailable", "pose_ffmpeg_unavailable", "pose_runtime_unavailable", "pose_inference_failed", "pose_cancellation_failed", "pose_evidence_identity_mismatch", "pose_timestamp_identity_mismatch", "pose_result_missing", "pose_no_samples", "pose_insufficient_evidence", "pose_invalid_geometry":
		return err.Error()
	}
	return "pose_analysis_unavailable"
}
