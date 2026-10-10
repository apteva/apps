package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Admission, cold preparation and cleanup have separate headroom. Inference
// remains serial and bounded; extending its deadline never increases workers.
func poseAnalysisBudget(samples int, hybrid bool) time.Duration {
	seconds := max(60, 30+4*min(256, max(1, samples)))
	if hybrid {
		seconds += 60
	}
	return time.Duration(seconds) * time.Second
}

func smartCropTimeout(app *sdk.AppCtx, project, op, fid string, raw []byte) time.Duration {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	samples := 1
	if row, err := getMedia(app.AppDB(), project, fid); err == nil && row != nil {
		samples = len(posePositions(smartCropFocus(op, p), row.DurationMs, row.FPS))
	}
	engine, _ := resolveSmartCropEngine(app, stringJSONValue(p["smart_crop_engine"]))
	capSeconds := min(1800, max(30, parseConfigIntFallback(app.Config().Get("smart_crop_timeout_seconds"), 900)))
	return min(time.Duration(capSeconds)*time.Second, 330*time.Second+poseAnalysisBudget(samples, engine == "hybrid"))
}

type cropStageTiming struct {
	Stage     string  `json:"stage"`
	ElapsedMs float64 `json:"elapsed_ms"`
	Status    string  `json:"status"`
}

func cropStage(ctx context.Context, stage string) func() {
	start := time.Now()
	return func() {
		if a := cropAudit(ctx); a != nil {
			a.StageTimings = append(a.StageTimings, cropStageTiming{Stage: stage, ElapsedMs: float64(time.Since(start).Microseconds()) / 1000, Status: "completed"})
		}
	}
}

// Only our numeric stage markers enter persisted diagnostics. Never retain
// arbitrary subprocess output, request data, source paths or signed URLs.
func recordPoseStages(ctx context.Context, output string) {
	a := cropAudit(ctx)
	if a == nil {
		return
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "REMOTE_SOURCE_CACHE_HIT ") {
			a.SourceCache = "hit"
		}
		if strings.HasPrefix(line, "REMOTE_SOURCE_CACHE_MISS ") {
			a.SourceCache = "miss"
		}
		if !strings.HasPrefix(line, "APTEVA_POSE_STAGE:") {
			continue
		}
		var timing cropStageTiming
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "APTEVA_POSE_STAGE:")), &timing) != nil || math.IsNaN(timing.ElapsedMs) || math.IsInf(timing.ElapsedMs, 0) || timing.ElapsedMs < 0 {
			continue
		}
		if timing.Stage != "source_materialization" && timing.Stage != "runtime_setup" {
			continue
		}
		if timing.Status != "completed" && timing.Status != "started" {
			continue
		}
		found := false
		for i := range a.StageTimings {
			if a.StageTimings[i].Stage == timing.Stage {
				a.StageTimings[i] = timing
				found = true
				break
			}
		}
		if !found {
			a.StageTimings = append(a.StageTimings, timing)
		}
	}
}

func poseSourcePreparation(req poseRequest, originalSource, work string) string {
	if req.SourceFile == nil {
		return ""
	}
	f := req.SourceFile
	if req.SourceCacheMaxBytes <= 0 {
		req.SourceCacheMaxBytes = remoteSourceCacheDefaultMaxBytes
	}
	cache := remoteSourceCacheName(fmt.Sprint(f.ID), sanitizeFilename(f.Name), f.SHA256, f.SizeBytes)
	cleanup := `rm -f "$POSE_SOURCE_CACHE.tmp.$$" "$POSE_SOURCE_CACHE.tmp.$$.verified"; if [ -f "$POSE_SOURCE_CACHE.lock/pid" ] && [ "$(cat "$POSE_SOURCE_CACHE.lock/pid")" = "$$" ]; then rm -rf "$POSE_SOURCE_CACHE.lock"; fi`
	return fmt.Sprintf("SOURCE_CACHE_ROOT=%s\nSOURCE_CACHE_MAX_BYTES=%d\nPOSE_SOURCE_CACHE=%s\ntrap %s EXIT\nmkdir -p \"$SOURCE_CACHE_ROOT\"\n", shellQuote(remoteSourceCacheRoot), req.SourceCacheMaxBytes, shellQuote(remoteSourceCacheRoot+"/"+cache), shellQuote(cleanup)) +
		"curl_retry() { curl -sS --connect-timeout 15 --max-time 180 --retry 2 --retry-delay 1 --retry-max-time 180 --retry-connrefused \"$@\"; }\n" + remoteSourceCacheScriptFragment +
		fmt.Sprintf("materialize_source %s %s %s %s %d %s\n", shellQuote(fmt.Sprint(f.ID)), shellQuote(originalSource), shellQuote(work+"/source"), shellQuote(cache), f.SizeBytes, shellQuote(f.SHA256))
}

func buildPoseRemoteScript(req poseRequest, root, work, setupMode string) string {
	source := req.Source
	if req.SourceFile != nil {
		req.Source = work + "/source"
	}
	raw, _ := json.Marshal(req)
	script := "set -eu\numask 077\nWORK=" + shellQuote(work) + "\nmkdir -p \"$WORK\"\necho $$ > \"$WORK/pid\"\n[ ! -f \"$WORK/cancel.requested\" ] || exit 1\n"
	cleanup := "rm -rf \"$WORK\""
	if f := req.SourceFile; f != nil {
		cache := remoteSourceCacheRoot + "/" + remoteSourceCacheName(fmt.Sprint(f.ID), sanitizeFilename(f.Name), f.SHA256, f.SizeBytes)
		script += "POSE_SOURCE_CACHE=" + shellQuote(cache) + "\n"
		cleanup += "; rm -f \"$POSE_SOURCE_CACHE.tmp.$$\" \"$POSE_SOURCE_CACHE.tmp.$$.verified\"; if [ -f \"$POSE_SOURCE_CACHE.lock/pid\" ] && [ \"$(cat \"$POSE_SOURCE_CACHE.lock/pid\")\" = \"$$\" ]; then rm -rf \"$POSE_SOURCE_CACHE.lock\"; fi"
	}
	script += "trap " + shellQuote(cleanup) + " EXIT\n"
	for _, file := range []struct {
		name string
		data []byte
	}{{"runtime.py", []byte(poseRuntime)}, {"smartcrop_pose_recovery.py", []byte(poseRecoveryRuntime)}, {"setup.py", []byte(poseSetup)}, {"request.json", raw}} {
		script += fmt.Sprintf("printf '%%s' %s | base64 -d > \"$WORK/%s\"\n", shellQuote(base64.StdEncoding.EncodeToString(file.data)), file.name)
	}
	script += "export TMPDIR=\"$WORK\" OMP_NUM_THREADS=1 OPENBLAS_NUM_THREADS=1 MKL_NUM_THREADS=1\n"
	stage := func(name, body, failureCode string) string {
		return fmt.Sprintf("printf 'APTEVA_POSE_STAGE:{\"stage\":\"%s\",\"elapsed_ms\":0,\"status\":\"started\"}\\n'\nPOSE_STAGE_START=$(python3 -c 'import time; print(time.monotonic_ns())')\nPOSE_STAGE_STATUS=0\n env WORK=\"$WORK\" POSE_ROOT=\"${POSE_ROOT:-}\" bash -eu -c %s || POSE_STAGE_STATUS=$?\nif [ \"$POSE_STAGE_STATUS\" -ne 0 ]; then if [ \"$POSE_STAGE_STATUS\" -eq 137 ]; then printf 'APTEVA_POSE_ERROR:{\"code\":\"media_resource_exhausted\"}\\n'; exit 137; fi; printf 'APTEVA_POSE_ERROR:{\"code\":\"%s\"}\\n'; exit 1; fi\nprintf 'APTEVA_POSE_STAGE:{\"stage\":\"%s\",\"elapsed_ms\":%%d,\"status\":\"completed\"}\\n' \"$(( ($(python3 -c 'import time; print(time.monotonic_ns())') - POSE_STAGE_START) / 1000000 ))\"\n", name, shellQuote(body), failureCode, name)
	}
	if req.SourceFile != nil {
		script += stage("source_materialization", poseSourcePreparation(req, source, work), "pose_source_preparation_failed")
	}
	script += "POSE_ROOT=" + shellQuote(root) + "\n"
	script += stage("runtime_setup", "python3 \"$WORK/setup.py\" \"$POSE_ROOT\""+setupMode, "pose_runtime_unavailable")
	script += "\"$POSE_ROOT/venv/bin/python\" \"$WORK/runtime.py\" \"$WORK/request.json\""
	return script
}

// Partial evidence is diagnostic only. Its model and timestamp identities must
// pass the same checks as a complete result, and it can never form a crop plan.
func verifiedPartialPose(result *poseResult, req poseRequest) *poseResult {
	if result == nil || result.ModelSHA != poseModelSHA256 || result.Runtime != "mediapipe-0.10.21" || len(result.Samples) > len(req.Positions) {
		return nil
	}
	for i, s := range result.Samples {
		if s.AtMs != req.Positions[i] {
			return nil
		}
		if r := s.Recovery; r != nil && (!req.Hybrid || (r.PersonSHA != "" && r.PersonSHA != posePersonModelSHA256) || (r.PoseSHA != "" && r.PoseSHA != poseRecoveryModelSHA256)) {
			return nil
		}
	}
	return result
}

// Known preparation/runtime timeouts are terminal, rather than launching
// more analysis under the same exhausted budget or returning a symbolic crop.
func poseTimeout(err error) bool {
	if err == nil {
		return false
	}
	code := poseFailureReason(err)
	return code == "analysis_timeout" || code == "pose_source_read_timeout" || code == "pose_analysis_timeout"
}
