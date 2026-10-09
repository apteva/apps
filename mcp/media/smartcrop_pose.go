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

//go:embed smartcrop_pose_recovery.py
var poseRecoveryRuntime string

//go:embed smartcrop_model/pose_landmarker_full.task
var poseModel []byte

const poseModelSHA256 = "4eaa5eb7a98365221087693fcc286334cf0858e2eb6e15b506aa4a7ecdcec4ad"
const poseRuntimeVersion = "mediapipe-0.10.21-full-1"
const poseHybridRuntimeVersion = "mediapipe-0.10.21-hybrid-1"
const posePersonModelSHA256 = "eb9543a3f625fc19e7d6134cf33e801542a81b34a15aaa9bdc25cdd1ec741309"
const poseRecoveryModelSHA256 = "26f3a19e61304a600dfb82d1001d41d24343b89fc70a33ffc84657e0b0bf2ecf"

type poseRecoveryModelsKey struct{}

func poseRuntimeIdentity(engine string) string {
	if engine == "hybrid" {
		return poseHybridRuntimeVersion
	}
	return poseRuntimeVersion
}
func poseAlgorithmIdentity(engine string) string {
	if engine == "hybrid" {
		return smartCropAlgorithmVersion
	}
	return "media-smartcrop-mediapipe-full-hands-10"
}

func smartCropEngineSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"hybrid", "mediapipe_full", "legacy"}, "description": "Smart Crop engine. hybrid uses MediaPipe Full first, with independent same-frame YOLO Pose/RTMPose recovery on difficult samples. Short unsupported position gaps remain composition-unknown. mediapipe_full retains pure Full; legacy retains saliency/foreground. No implicit padding; sampled coverage needs visual review."}
}
func smartCropFramingSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"upper_body", "widest_valid"}, "description": "Framing preference for pose engines. upper_body keeps tighter head/upper-pose framing (default). widest_valid uses the largest native crop at the requested ratio, positioned to retain supported pose; no padding or automatic scaling. Coverage guards still apply."}
}
func resolveSmartCropFraming(app *sdk.AppCtx, requested string) (string, error) {
	framing := strings.TrimSpace(strings.ToLower(requested))
	if framing == "" && app != nil {
		framing = strings.TrimSpace(strings.ToLower(app.Config().Get("smart_crop_framing")))
	}
	if framing == "" {
		framing = "upper_body"
	}
	if framing != "upper_body" && framing != "widest_valid" {
		return "", &renderInputError{Code: "invalid_smart_crop_framing", Message: "smart_crop_framing must be upper_body or widest_valid."}
	}
	return framing, nil
}
func resolveSmartCropEngine(app *sdk.AppCtx, requested string) (string, error) {
	engine := strings.TrimSpace(strings.ToLower(requested))
	if engine == "" && app != nil {
		engine = strings.TrimSpace(strings.ToLower(app.Config().Get("smart_crop_engine")))
	}
	if engine == "" {
		engine = "hybrid"
	}
	if engine != "hybrid" && engine != "mediapipe_full" && engine != "legacy" {
		return "", &renderInputError{Code: "invalid_smart_crop_engine", Message: "smart_crop_engine must be hybrid, mediapipe_full or legacy."}
	}
	return engine, nil
}
func validateSmartCropEngine(raw []byte) error {
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if p["smart_crop_framing"] != nil {
		value, ok := p["smart_crop_framing"].(string)
		if !ok {
			return &renderInputError{Code: "invalid_smart_crop_framing", Message: "smart_crop_framing must be a string."}
		}
		if _, err := resolveSmartCropFraming(nil, value); err != nil {
			return err
		}
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
	Hybrid       bool    `json:"hybrid"`
	RecoveryRoot string  `json:"recovery_root,omitempty"`
	Framing      string  `json:"framing"`
	Source       string  `json:"source"`
	Model        string  `json:"model"`
	ModelSHA     string  `json:"model_sha256"`
	FFmpeg       string  `json:"ffmpeg"`
	Positions    []int64 `json:"positions"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	RatioW       int     `json:"ratio_w"`
	RatioH       int     `json:"ratio_h"`
	Remaining    float64 `json:"remaining_seconds"`
	Video        bool    `json:"video"`
}
type poseLandmarkEvidence struct {
	Index      int     `json:"index"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Visibility float64 `json:"visibility"`
	Presence   float64 `json:"presence"`
}
type poseSample struct {
	EffectiveStatus      string                 `json:"effective_status,omitempty"`
	Recovery             *poseRecoveryEvidence  `json:"recovery,omitempty"`
	ExtentScope          string                 `json:"extent_scope,omitempty"`
	Landmarks            []poseLandmarkEvidence `json:"landmark_evidence,omitempty"`
	TrackedWristEvidence []poseLandmarkEvidence `json:"tracked_wrist_evidence,omitempty"`
	RefreshedHands       []int                  `json:"refreshed_hand_indices,omitempty"`
	RefreshStatus        string                 `json:"hand_refresh_status,omitempty"`
	InitialStatus        string                 `json:"initial_status,omitempty"`

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
type poseRecoveryPoint struct {
	Index      int     `json:"index"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Confidence float64 `json:"confidence"`
}
type poseRecoveryEvidence struct {
	Status          string              `json:"status"`
	FailureCode     string              `json:"failure_code,omitempty"`
	Mode            string              `json:"mode"`
	InitialStatus   string              `json:"initial_status,omitempty"`
	PersonModel     string              `json:"person_model,omitempty"`
	PersonSHA       string              `json:"person_model_sha256,omitempty"`
	PoseModel       string              `json:"pose_model,omitempty"`
	PoseSHA         string              `json:"pose_model_sha256,omitempty"`
	ConfidenceScope string              `json:"confidence_scope,omitempty"`
	PersonBounds    []float64           `json:"person_bounds,omitempty"`
	PersonScore     float64             `json:"person_score,omitempty"`
	Landmarks       []poseRecoveryPoint `json:"landmarks,omitempty"`
	RecoveredWrists []int               `json:"recovered_wrist_indices,omitempty"`
	ElapsedMs       float64             `json:"elapsed_ms"`
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
	runtimeVersion := poseRuntimeVersion
	setupMode := ""
	if req.Hybrid {
		runtimeVersion = poseHybridRuntimeVersion
		setupMode = " hybrid"
	}
	if host > 0 {
		req.Model = "/tmp/apteva-media-pose/" + runtimeVersion + "/model.task"
		if req.Hybrid {
			req.RecoveryRoot = "/tmp/apteva-media-pose/" + runtimeVersion
		}
		raw, _ := json.Marshal(req)
		work := uniqueRemoteWorkDir(0) + "-pose"
		script := "set -eu\numask 077\nWORK=" + shellQuote(work) + "\nmkdir -p \"$WORK\"\necho $$ > \"$WORK/pid\"\n[ ! -f \"$WORK/cancel.requested\" ] || exit 1\ntrap 'rm -rf \"$WORK\"' EXIT\n"
		// Base64 avoids shell interpolation of private source URLs and request data.
		for _, file := range []struct {
			name string
			data []byte
		}{{"runtime.py", []byte(poseRuntime)}, {"smartcrop_pose_recovery.py", []byte(poseRecoveryRuntime)}, {"setup.py", []byte(poseSetup)}, {"request.json", raw}} {
			script += fmt.Sprintf("printf '%%s' %s | base64 -d > \"$WORK/%s\"\n", shellQuote(base64.StdEncoding.EncodeToString(file.data)), file.name)
		}
		script += "POSE_ROOT=/tmp/apteva-media-pose/" + runtimeVersion + "\npython3 \"$WORK/setup.py\" \"$POSE_ROOT\"" + setupMode + "\n"
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
		root, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(root, "apteva-media-pose", runtimeVersion)
		if req.Hybrid {
			req.RecoveryRoot = root
			if override, ok := ctx.Value(poseRecoveryModelsKey{}).(string); ok {
				req.RecoveryRoot = override
			}
		}
		raw, _ := json.Marshal(req)
		for name, data := range map[string][]byte{"runtime.py": []byte(poseRuntime), "smartcrop_pose_recovery.py": []byte(poseRecoveryRuntime), "setup.py": []byte(poseSetup), "model.task": poseModel, "request.json": raw} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				return nil, err
			}
		}
		python := strings.TrimSpace(app.Config().Get("smart_crop_python"))
		if python == "" {
			args := []string{filepath.Join(dir, "setup.py"), root}
			if req.Hybrid {
				args = append(args, "hybrid")
			}
			setup := exec.CommandContext(ctx, "python3", args...)
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
				if r := s.Recovery; r != nil && (!req.Hybrid || (r.PersonSHA != "" && r.PersonSHA != posePersonModelSHA256) || (r.PoseSHA != "" && r.PoseSHA != poseRecoveryModelSHA256)) {
					return nil, fmt.Errorf("pose_evidence_identity_mismatch")
				}
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("pose_result_missing")
}

func computeSmartCropPose(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project, fid string, rw, rh int, target smartCropTarget, framing string) (*cropWindow, []cropPathPoint, error) {
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
	positions := posePositions(target, row.DurationMs, row.FPS)
	engine := "mediapipe_full"
	if a := cropAudit(ctx); a != nil && a.RequestedEngine == "hybrid" {
		engine = "hybrid"
	}
	recordPoseAttempt(ctx, &poseResult{}, framing)
	if a := cropAudit(ctx); a != nil {
		a.PoseAttempt.Status = "inference"
		a.PoseAttempt.Requested = len(positions)
	}
	result, err := runPose(ctx, app, host, poseRequest{Hybrid: engine == "hybrid", Framing: framing, Source: source, ModelSHA: poseModelSHA256, FFmpeg: ffmpeg, Positions: positions, Width: row.Width, Height: row.Height, RatioW: rw, RatioH: rh, Video: !row.IsImage})
	if err != nil {
		if a := cropAudit(ctx); a != nil {
			a.PoseAttempt.Status = "inference_failed"
			a.PoseAttempt.FailureCode = poseFailureReason(err)
		}
		if failure, ok := err.(*poseRuntimeFailure); ok {
			if a := cropAudit(ctx); a != nil {
				a.PoseFailure = failure
			}
		}
		return nil, nil, err
	}
	recordPoseAttempt(ctx, result, framing)
	win, path, err := planPoseSamples(result.Samples, row.Width, row.Height, rw, rh)
	var gaps []posePathGap
	if engine == "hybrid" {
		win, path, gaps, err = planHybridPoseSamples(result.Samples, row.Width, row.Height, rw, rh)
	}
	if err != nil {
		if a := cropAudit(ctx); a != nil {
			a.PoseAttempt.Status = "planning_failed"
			a.PoseAttempt.FailureCode = poseFailureReason(err)
		}
		return nil, nil, err
	}
	if a := cropAudit(ctx); a != nil {
		a.PoseAttempt.Status = "planned"
	}
	if a := cropAudit(ctx); a != nil {
		a.AlgorithmVersion = poseAlgorithmIdentity(engine)
		a.Method = engine + ":exact_source_pose"
		a.EffectiveEngine = engine
		a.ModelSHA = poseModelSHA256
		a.RuntimeVersion = poseRuntimeIdentity(engine)
		a.PosePathGaps = gaps
		a.Coverage = "sampled_extent_fits"
		a.PoseSamples = result.Samples
		a.PoseLimitations = []string{"Head/hair and hand bounds are estimated from landmarks; pose confidence is not visual approval.", "Upper-body portrait policy does not require full legs; wider actions may not fit.", "Video coverage is sampled (up to 256 frames); extraction timestamps identify requested FFmpeg seeks and can differ from picture presentation by one frame. End-of-source samples leave two nominal frame intervals to avoid empty seeks."}
		if engine == "hybrid" {
			a.PoseLimitations = append(a.PoseLimitations, "Recovery models have different confidence meanings. Coarse person boxes and interpolated crop positions do not verify head/hand coverage; gaps remain unknown.")
		}
		for i, s := range result.Samples {
			recordSmartCropEvidence(ctx, "native_pose", s.AtMs, "")
			b := poseBoundsWindow(s.Bounds, row.Width, row.Height)
			status := s.Status
			if engine == "hybrid" && status == "upper_pose_exceeds_crop" && len(s.WeakWrists) == 0 && s.ExtentScope == "" && i < len(path) && b.W <= win.W && b.H <= win.H && path[i].X <= b.X && path[i].X+win.W >= b.X+b.W && win.Y <= b.Y && win.Y+win.H >= b.Y+b.H {
				status = "fits_detected_upper_pose"
				a.PoseSamples[i].EffectiveStatus = "fits_supported_extent_after_reposition"
			}
			a.Extents = append(a.Extents, smartCropExtentEvidence{AtMs: s.AtMs, Bounds: auditCropWindow(b), Support: s.Status})
			if s.Recovery != nil && (s.Recovery.Status == "runtime_unavailable" || s.Recovery.Status == "budget_exhausted") {
				recordSmartCropFallback(ctx, "hybrid_recovery_"+s.Recovery.Status)
			}
			if b.W > win.W && s.ExtentScope != "full_person_recovery" {
				a.Coverage = "exceeds_crop_width"
				a.Recommendation = "Use a wider ratio or explicitly request fit_mode: contain to preserve the full action."
			} else if status != "fits_detected_upper_pose" && a.Coverage != "exceeds_crop_width" {
				a.Coverage = "unknown"
				a.Recommendation = "Pose evidence is incomplete; review the source and crop."
			}
		}
		if len(gaps) > 0 && a.Coverage != "exceeds_crop_width" {
			a.Coverage = "unknown"
			a.Recommendation = "Short detection gaps use interpolated crop positions only. Review these timestamps; head and hand coverage is unverified."
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

type poseFailedSample struct {
	AtMs     int64  `json:"at_ms"`
	Category string `json:"category"`
}
type poseAttemptAudit struct {
	CountScope  string             `json:"count_scope"`
	Requested   int                `json:"requested_samples"`
	Analysed    int                `json:"analysed_samples"`
	Engine      string             `json:"engine"`
	Algorithm   string             `json:"algorithm_version"`
	ModelSHA    string             `json:"model_sha256"`
	Runtime     string             `json:"runtime_version"`
	Framing     string             `json:"framing"`
	Status      string             `json:"status"`
	FailureCode string             `json:"failure_code,omitempty"`
	Valid       int                `json:"valid_samples"`
	Invalid     int                `json:"invalid_samples"`
	Uncertain   int                `json:"uncertain_samples"`
	Failed      []poseFailedSample `json:"failed_samples"`
}

type posePathGap struct {
	AtMs     int64  `json:"at_ms"`
	BeforeMs int64  `json:"before_ms"`
	AfterMs  int64  `json:"after_ms"`
	Method   string `json:"method"`
}

func recordPoseAttempt(ctx context.Context, result *poseResult, framing string) {
	a := cropAudit(ctx)
	if a == nil {
		return
	}
	a.PoseSamples = result.Samples
	engine := "mediapipe_full"
	if a.RequestedEngine == "hybrid" {
		engine = "hybrid"
	}
	attempt := &poseAttemptAudit{Engine: engine, Algorithm: poseAlgorithmIdentity(engine), ModelSHA: poseModelSHA256, Runtime: poseRuntimeIdentity(engine), Framing: framing, CountScope: "Valid/invalid counts describe usable positioning geometry, including conservative recovery boxes; they do not establish hand coverage, crop fit or visual approval. Missing samples retain their original status; position interpolation never increases confidence.", Requested: len(result.Samples), Analysed: len(result.Samples), Status: "planning", Failed: []poseFailedSample{}}
	for _, s := range result.Samples {
		if poseSampleHasGeometry(s, a.SourceWidth, a.SourceHeight) {
			attempt.Valid++
		} else {
			attempt.Invalid++
		}
		if s.Status == "uncertain_hand_evidence" || s.Status == "uncertain_person_extent" {
			attempt.Uncertain++
		}
		if s.Status != "fits_detected_upper_pose" {
			attempt.Failed = append(attempt.Failed, poseFailedSample{AtMs: s.AtMs, Category: s.Status})
		}
	}
	a.PoseAttempt = attempt
}

// Preserve good native geometry when bounded gaps prevent a complete pose
// estimate. Only the position is interpolated; samples/confidence remain intact.
// Strict composition policy sees unknown, never an invented fit.
func planHybridPoseSamples(samples []poseSample, w, h, rw, rh int) (*cropWindow, []cropPathPoint, []posePathGap, error) {
	valid := []poseSample{}
	for i, s := range samples {
		if i > 0 && s.AtMs <= samples[i-1].AtMs {
			return nil, nil, nil, fmt.Errorf("pose_timestamp_identity_mismatch")
		}
		if poseSampleHasGeometry(s, w, h) {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 || len(valid)*4 < len(samples)*3 {
		return nil, nil, nil, fmt.Errorf("pose_insufficient_evidence")
	}
	win, supported, err := planPoseSamples(valid, w, h, rw, rh)
	if err != nil {
		return nil, nil, nil, err
	}
	// Chroma alignment need not discard a feasible edge strip merely to keep
	// an exact integer ratio unit (e.g. 594x1056 versus 606x1080). Expand before
	// declaring an action too wide, preserving a fixed size throughout a reel.
	maxW, maxH := cropDimsForRatio(w, h, rw, rh)
	neededW, top, bottom := 0, h, 0
	for _, s := range valid {
		b := poseBoundsWindow(s.Bounds, w, h)
		neededW = max(neededW, b.W)
		top = min(top, b.Y)
		bottom = max(bottom, b.Y+b.H)
	}
	if neededW > win.W || bottom-top > win.H {
		oldW := win.W
		win.H = min(maxH, max(win.H, (int(math.Ceil(float64(min(neededW, maxW))*float64(rh)/float64(rw)))+1)&^1, bottom-top))
		win.H = (win.H + 1) &^ 1
		win.W = min(maxW, (int(math.Ceil(float64(win.H)*float64(rw)/float64(rh)))+1)&^1)
		win.Y = clampInt(top-int(float64(win.H)*.055), max(0, bottom-win.H), min(h-win.H, top))
		for i, s := range valid {
			b := poseBoundsWindow(s.Bounds, w, h)
			x := supported[i].X + (oldW-win.W)/2
			if b.W <= win.W {
				x = clampInt(x, max(0, b.X+b.W-win.W), min(w-win.W, b.X))
			}
			supported[i].X = clampInt(x, 0, w-win.W)
			supported[i].Y = win.Y
		}
	}
	// A too-wide action should still keep its supported visible head in-frame.
	for i, s := range valid {
		if len(s.Head) == 4 && poseBoundsWindow(s.Bounds, w, h).W > win.W {
			head := poseBoundsWindow(s.Head, w, h)
			if head.W > 0 && head.W <= win.W {
				supported[i].X = clampInt(supported[i].X, max(0, head.X+head.W-win.W), min(w-win.W, head.X))
			}
		}
	}
	path := make([]cropPathPoint, 0, len(samples))
	gaps := []posePathGap{}
	for _, s := range samples {
		i := sort.Search(len(supported), func(i int) bool { return supported[i].AtMs >= s.AtMs })
		if i < len(supported) && supported[i].AtMs == s.AtMs {
			path = append(path, supported[i])
			continue
		}
		left, right := max(0, i-1), min(len(supported)-1, i)
		a, b := supported[left], supported[right]
		if a.AtMs == b.AtMs {
			if absInt64(s.AtMs-a.AtMs) > 1000 {
				return nil, nil, nil, fmt.Errorf("pose_insufficient_evidence")
			}
		} else if b.AtMs-a.AtMs > 2500 {
			return nil, nil, nil, fmt.Errorf("pose_insufficient_evidence")
		}
		x := a.X
		if a.AtMs != b.AtMs {
			x = int(math.Round(float64(a.X) + float64(b.X-a.X)*float64(s.AtMs-a.AtMs)/float64(b.AtMs-a.AtMs)))
		}
		path = append(path, cropPathPoint{AtMs: s.AtMs, X: clampInt(x, 0, w-win.W), Y: win.Y})
		gaps = append(gaps, posePathGap{AtMs: s.AtMs, BeforeMs: a.AtMs, AfterMs: b.AtMs, Method: "interpolated_position_only"})
	}
	win.X = path[0].X
	return win, path, gaps, nil
}

// Expanding a legacy fallback retains its original rectangle at each sample.
// Existing unknown/too-wide coverage remains guarded; this is a preference,
// never an automatic composition approval or a padding request.
func expandSmartCropFraming(raw []byte, a *smartCropAudit, rw, rh int) []byte {
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil || a.SourceWidth <= 0 || a.SourceHeight <= 0 {
		return raw
	}
	ow, oh := int(int64FromJSONValue(p["crop_w"])), int(int64FromJSONValue(p["crop_h"]))
	if ow <= 0 || oh <= 0 {
		return raw
	}
	w, h := cropDimsForRatio(a.SourceWidth, a.SourceHeight, rw, rh)
	if w < ow || h < oh {
		return raw
	}
	position := func(x, y int) (int, int) {
		return clampInt(x+ow/2-w/2, max(0, x+ow-w), min(x, a.SourceWidth-w)), clampInt(y+oh/2-h/2, max(0, y+oh-h), min(y, a.SourceHeight-h))
	}
	x, y := position(int(int64FromJSONValue(p["crop_x"])), int(int64FromJSONValue(p["crop_y"])))
	p["crop_x"] = x
	p["crop_y"] = y
	p["crop_w"] = w
	p["crop_h"] = h
	if value, ok := p["crop_path"]; ok {
		encoded, _ := json.Marshal(value)
		var path []cropPathPoint
		if json.Unmarshal(encoded, &path) == nil {
			for i := range path {
				path[i].X, path[i].Y = position(path[i].X, path[i].Y)
			}
			p["crop_path"] = path
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return raw
	}
	return encoded
}
func poseBoundsWindow(b []float64, w, h int) cropWindow {
	if len(b) != 4 {
		return cropWindow{}
	}
	x, y := max(0, int(math.Floor(b[0]))), max(0, int(math.Floor(b[1])))
	return cropWindow{X: x, Y: y, W: min(w, int(math.Ceil(b[2]))) - x, H: min(h, int(math.Ceil(b[3]))) - y}
}
func poseSampleHasGeometry(s poseSample, w, h int) bool {
	if len(s.Bounds) != 4 || s.Crop.Width <= 0 || s.Crop.Height <= 0 || s.Crop.Width > w || s.Crop.Height > h {
		return false
	}
	for _, v := range s.Bounds {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return s.Bounds[0] >= 0 && s.Bounds[1] >= 0 && s.Bounds[2] <= float64(w) && s.Bounds[3] <= float64(h) && s.Bounds[2] > s.Bounds[0] && s.Bounds[3] > s.Bounds[1]
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
		if !poseSampleHasGeometry(s, w, h) {
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
