package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

var resultCacheLocks keyedGate

type renderSourceMetadataKey struct{}

const renderAlgorithmVersion = "media-audit-1"

// Crop decisions have their own revision so a tracking fix cannot reuse an old
// path, while unrelated render-result caches remain useful. Both decision and
// pre-analysis request caches need it; resolved local plans already include the
// changed coordinates in their result-cache key.
const legacySmartCropAlgorithmVersion = "media-smartcrop-exact-frame-pose-8"
const smartCropAlgorithmVersion = "media-smartcrop-hybrid-grounded-12"

// Remote binaries/provider settings are not immutable. Restrict reuse to this
// process lifetime as well as host/connection identity until they expose a
// trustworthy revision. Local binaries additionally use their file identity.
var executorCacheSession = func() string {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", nonce)
}()

func executableIdentity(binary string) string {
	if binary == "" {
		binary = "ffmpeg"
	}
	if resolved, err := exec.LookPath(binary); err == nil {
		binary = resolved
	}
	identity := binary
	if info, err := os.Stat(binary); err == nil {
		identity += fmt.Sprint(info.Size(), info.ModTime().UnixNano())
	}
	return identity
}

func localRenderCacheKey(ctx context.Context, app *sdk.AppCtx, sc *storageClient, row *RenderRow, plan *opPlan, folder, binary string) string {
	hashes := []string{}
	for _, fid := range row.SourceFileIDs {
		metadata, _ := ctx.Value(renderSourceMetadataKey{}).(map[string]*StorageFile)
		f := metadata[fid]
		if f == nil || f.SHA256 == "" {
			return ""
		}
		hashes = append(hashes, fid+":"+f.SHA256)
	}

	// Include executable identity so replacing FFmpeg invalidates old results.
	identity := executableIdentity(binary)
	raw, _ := json.Marshal([]any{renderAlgorithmVersion, sc.base, row.ProjectID, hashes, plan, folder, identity, app.Config().Get("render_encoder_threads")})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func findCachedRender(ctx context.Context, app *sdk.AppCtx, sc *storageClient, key, project, folder, name string) int64 {
	if key == "" {
		return 0
	}
	var id, size int64
	var sha string
	if app.AppDB().QueryRow(`SELECT storage_file_id,sha256,size_bytes FROM render_result_cache WHERE cache_key=? AND project_id=?`, key, project).Scan(&id, &sha, &size) != nil {
		return 0
	}
	f, err := sc.GetFile(ctx, project, id)
	if err != nil || f.SHA256 != sha || f.SizeBytes != size || validateRenderUploadDestination(f, folder, name) != nil {
		return 0
	}
	return id
}
func saveCachedRender(ctx context.Context, app *sdk.AppCtx, sc *storageClient, key, project string, id int64) {
	if key == "" {
		return
	}
	f, err := sc.GetFile(ctx, project, id)
	if err != nil || f.SHA256 == "" {
		return
	}
	_, _ = app.AppDB().Exec(`INSERT OR REPLACE INTO render_result_cache(cache_key,project_id,storage_file_id,sha256,size_bytes) VALUES(?,?,?,?,?)`, key, project, id, f.SHA256, f.SizeBytes)
	_, _ = app.AppDB().Exec(`DELETE FROM render_result_cache WHERE cache_key IN (SELECT cache_key FROM render_result_cache ORDER BY created_at DESC LIMIT -1 OFFSET 1000)`)
}

type cropPlanCacheKey struct{}
type cropPlanCacheOutcome struct{ Hit bool }
type cropPlanAdmissionKey struct{}
type cropPlanWaits struct{ AdmissionMs, PlanMs float64 }

var cropPlanLocks keyedGate

func preprocessSmartCrop(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project, op string, sources []string, params []byte) []byte {
	if app == nil || ctx == nil || sc == nil || len(sources) != 1 {
		return preprocessSmartCropUncached(ctx, app, sc, project, op, sources, params)
	}
	if ctx.Err() != nil {
		return cropWorkError(params, ctx.Err())
	}
	row, err := getMedia(app.AppDB(), project, sources[0])
	if err != nil || row == nil || row.SourceSHA256 == "" {
		return preprocessSmartCropUncached(ctx, app, sc, project, op, sources, params)
	}
	var parsed map[string]any
	if json.Unmarshal(params, &parsed) != nil {
		return params
	}
	if _, explicit := parsed["crop_w"]; explicit {
		return params
	}
	target := smartCropFocus(op, parsed)
	ratio := stringJSONValue(parsed["target_ratio"])
	if ratio == "" && op == "extract_reel" {
		ratio = "9:16"
	}
	mode := stringJSONValue(parsed["crop_mode"])
	if mode == "" {
		mode = "smart"
	}
	engine, _ := resolveSmartCropEngine(app, stringJSONValue(parsed["smart_crop_engine"]))
	framing, _ := resolveSmartCropFraming(app, stringJSONValue(parsed["smart_crop_framing"]))
	var derivationIdentity, sceneIdentity any
	if engine == "legacy" {
		derivationIdentity = row.Derivations
		sceneIdentity = nativeSmartCropSceneCacheIdentity(app, project, row)
	}
	// Native pose plans depend on source/model identity, not unrelated index
	// thumbnails. Legacy fallback plans are deliberately not cached for ML.
	raw, _ := json.Marshal([]any{framing, engine, poseRuntimeIdentity(engine), poseModelSHA256, posePersonModelSHA256, poseRecoveryModelSHA256, poseAlgorithmIdentity(engine), app.Manifest().Version, sc.base, project, op, sources, row.SourceSHA256, row.Width, row.Height, row.Rotation, row.FPS, row.DurationMs, derivationIdentity, sceneIdentity, target, ratio, mode, parsed["fit_mode"], app.Config().Get("render_host_id")})
	digest := sha256.Sum256(raw)
	key := fmt.Sprintf("%x", digest)
	load := func() []byte {
		var cached string
		if app.AppDB().QueryRow(`SELECT params FROM smartcrop_cache WHERE cache_key=?`, key).Scan(&cached) == nil {
			var crop map[string]any
			if json.Unmarshal([]byte(cached), &crop) == nil {
				for k, v := range crop {
					parsed[k] = v
				}
				if out, err := json.Marshal(parsed); err == nil {
					if state, ok := ctx.Value(cropPlanCacheKey{}).(*cropPlanCacheOutcome); ok {
						state.Hit = true
					}
					return out
				}
			}
		}
		return nil
	}
	// Cache hits need no host work. Misses always admit before taking the
	// plan gate, including render/HTTP callers with existing leases. Reversing
	// this order deadlocks a preview against an admitted heavy render.
	if cached := load(); cached != nil {
		return cached
	}
	start := time.Now()
	ctx, releaseAdmission, err := acquireMediaWork(ctx, app, 1)
	if err != nil {
		return cropWorkError(params, err)
	}
	defer releaseAdmission()
	waits := cropPlanWaits{AdmissionMs: float64(time.Since(start).Microseconds()) / 1000}
	start = time.Now()
	release, err := cropPlanLocks.acquire(ctx, digest[0])
	if err != nil {
		return cropWorkError(params, err)
	}
	defer release()
	waits.PlanMs = float64(time.Since(start).Microseconds()) / 1000
	if cached := load(); cached != nil {
		return cached
	}
	ctx = context.WithValue(ctx, cropPlanAdmissionKey{}, waits)
	out := preprocessSmartCropUncached(ctx, app, sc, project, op, sources, params)
	var resolved map[string]any
	if ctx.Err() == nil && json.Unmarshal(out, &resolved) == nil && (resolved["crop_version"] == "v2" || resolved["crop_version"] == "pose_full" || resolved["crop_version"] == "pose_hybrid") {
		if engine == "mediapipe_full" && resolved["crop_version"] != "pose_full" {
			return out
		}
		if engine == "hybrid" {
			if resolved["crop_version"] != "pose_hybrid" {
				return out
			}
			var p struct {
				Audit smartCropAudit `json:"crop_diagnostics"`
			}
			json.Unmarshal(out, &p)
			for _, s := range p.Audit.PoseSamples {
				if s.Recovery != nil && (s.Recovery.Status == "runtime_unavailable" || s.Recovery.Status == "budget_exhausted") {
					return out
				}
			}
		}
		crop := map[string]any{}
		for _, k := range []string{"crop_w", "crop_h", "crop_x", "crop_y", "crop_path", "crop_mode", "crop_version", "crop_diagnostics", "smart_crop_engine", "smart_crop_framing"} {
			if v, ok := resolved[k]; ok {
				crop[k] = v
			}
		}
		encoded, _ := json.Marshal(crop)
		_, _ = app.AppDB().Exec(`INSERT OR REPLACE INTO smartcrop_cache(cache_key,params) VALUES(?,?)`, key, string(encoded))
		_, _ = app.AppDB().Exec(`DELETE FROM smartcrop_cache WHERE cache_key IN (SELECT cache_key FROM smartcrop_cache ORDER BY created_at DESC LIMIT -1 OFFSET 1000)`)
	}

	return out
}

var requestCacheLocks keyedGate

// Request-level reuse applies to all executors, before download or analysis.
// Source and output permissions are rechecked through Storage on every hit.
func requestRenderCacheKey(ctx context.Context, app *sdk.AppCtx, sc *storageClient, row *RenderRow, executor renderExecutor) (key, folder, name string) {
	var identity any
	switch e := executor.(type) {
	case *localExecutor:
		identity = []any{executableIdentity(e.ffmpegPath), app.Config().Get("render_encoder_threads")}
	case *remoteExecutor:
		identity = []any{executorCacheSession, e.hostID, ffmpegVersion, e.encoderThreads}
	case *cloudinaryExecutor:
		if e.bound == nil {
			return "", "", ""
		}
		identity = []any{executorCacheSession, e.bound.ConnectionID, e.bound.AppSlug}
	default:
		return "", "", ""
	}
	sources := []any{}
	sourceExt := ""
	for i, fid := range row.SourceFileIDs {
		id, err := strconv.ParseInt(fid, 10, 64)
		if err != nil {
			return "", "", ""
		}
		file, err := sc.GetFile(ctx, row.ProjectID, id)
		if err != nil || len(file.SHA256) != 64 {
			return "", "", ""
		}
		if i == 0 {
			sourceExt = filepath.Ext(file.Name)
		}
		var evidence any
		if media, err := getMedia(app.AppDB(), row.ProjectID, fid); err == nil {
			evidence = []any{media.Width, media.Height, media.Rotation, media.Derivations, nativeSmartCropSceneCacheIdentity(app, row.ProjectID, media)}
		}
		sources = append(sources, []any{fid, file.SHA256, file.SizeBytes, evidence})
	}
	plan, err := buildPlan(row.Operation, row.SourceFileIDs, row.Params, row.OutputName, sourceExt)
	if err != nil {
		return "", "", ""
	}
	folder = row.OutputFolder
	if folder == "" {
		folder = app.Config().Get("render_output_folder")
	}
	if folder == "" {
		folder = "/renders/"
	}
	revision := renderAlgorithmVersion
	switch row.Operation {
	case "audio_filter":
		revision += ":" + audioNormalizationVersion
	case "trim":
		revision += ":" + trimAlgorithmVersion
	case "crop", "extract_frame", "extract_reel":
		// Request-cache hits skip analysis entirely. Invalidating only the
		// decision cache would still return an earlier incorrectly cropped file.
		var cropParams map[string]any
		json.Unmarshal(row.Params, &cropParams)
		engine, _ := resolveSmartCropEngine(app, stringJSONValue(cropParams["smart_crop_engine"]))
		framing, _ := resolveSmartCropFraming(app, stringJSONValue(cropParams["smart_crop_framing"]))
		revision += ":" + framing + ":" + smartCropAlgorithmVersion + ":" + app.Manifest().Version + ":" + engine + ":" + poseRuntimeVersion
	}
	raw, _ := json.Marshal([]any{revision, sc.base, row.ProjectID, executor.Name(), identity, row.Operation, row.Params, sources, folder, plan.Filename})
	return fmt.Sprintf("%x", sha256.Sum256(raw)), folder, plan.Filename
}

// A transient pose failure must not freeze a legacy output under the default
// engine's request key. Coordinate-specific local reuse still follows analysis.
func smartCropUsedEngineFallback(raw []byte) bool {
	var p struct {
		Audit *smartCropAudit `json:"crop_diagnostics"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Audit == nil {
		return false
	}
	return (p.Audit.RequestedEngine == "mediapipe_full" || p.Audit.RequestedEngine == "hybrid") && p.Audit.EffectiveEngine != p.Audit.RequestedEngine
}
