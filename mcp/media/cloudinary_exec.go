package main

// Cloudinary render backend. Optional — kicks in when an operator
// binds the cloudinary integration to the manifest's render_executor
// role. The flow:
//
//	1. Mint a signed URL for the source file in storage (so Cloudinary
//	   can fetch the bytes; storage stays the source of truth).
//	2. POST to /v1_1/{cloud_name}/video/upload with file=<signedURL>
//	   and eager=<transformation>. Cloudinary ingests + processes
//	   eagerly (eager_async=false) and returns the eager URL in the
//	   response.
//	3. HTTP GET the eager URL.
//	4. Re-upload those bytes back into storage as the render output —
//	   same UploadRender path as the local executor, so the catalog
//	   sees both kinds of outputs identically.
//
// Cloudinary-specific quirks:
//   - We always use resource_type=video. The video endpoint handles
//     audio + still-frame extraction too; image-only sources also
//     work (Cloudinary auto-detects the media type from the URL).
//     Exception: image-only resize/crop could go through `image`,
//     but the unified path keeps the executor simpler and Cloudinary
//     bills identically.
//   - eager strings are slash-separated transformation steps within a
//     chain, pipe-separated across chains. We only ever produce one
//     chain (one output per render), so no pipes here.
//   - Concat + audio_extract aren't modelled cleanly by eager (concat
//     needs splice overlays; audio_extract is technically `f_mp3` on
//     a video resource but the semantics get fiddly). selectExecutor
//     filters those ops out before we get here — they fall back to
//     local ffmpeg. That's a feature: the cloud backend is opt-in
//     for the cases it's good at.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// signedURLTTL is how long the source URL we hand to Cloudinary stays
// valid. 1h covers ingest + eager processing for any reasonable
// source size; we don't need to re-fetch after upload.
const cloudinarySignedURLTTL = 3600

type cloudinaryExecutor struct {
	bound    *sdk.BoundIntegration
	fallback *localExecutor // for outputFolder; we never run ffmpeg here
}

func (e *cloudinaryExecutor) Name() string { return "cloudinary" }

// supports gates which ops the cloud backend will accept. Anything
// returning false stays on local ffmpeg.
func (e *cloudinaryExecutor) supports(op string) bool {
	switch op {
	case "trim", "resize", "transcode", "crop", "extract_frame":
		return true
	}
	return false
}

func (e *cloudinaryExecutor) Execute(ctx context.Context, app *sdk.AppCtx, row *RenderRow) (int64, error) {
	log := app.Logger()
	if e.bound == nil || e.bound.ConnectionID == 0 {
		return 0, errors.New("cloudinary executor: integration not bound")
	}
	if len(row.SourceFileIDs) != 1 {
		return 0, fmt.Errorf("cloudinary executor: %s requires one source, got %d",
			row.Operation, len(row.SourceFileIDs))
	}
	srcID, err := strconv.ParseInt(row.SourceFileIDs[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("source file_id %q not numeric", row.SourceFileIDs[0])
	}

	// Build the eager transformation chain.
	chain, err := buildCloudinaryChain(row.Operation, row.Params, row.OutputName)
	if err != nil {
		return 0, fmt.Errorf("cloudinary chain: %w", err)
	}

	// Reuse the local plan helpers for output filename + content type
	// so storage uploads from either backend look identical to panels.
	plan, err := buildPlan(row.Operation, row.SourceFileIDs, row.Params, row.OutputName)
	if err != nil {
		return 0, fmt.Errorf("plan: %w", err)
	}

	sc := newStorageClient()
	signedURL, err := sc.GetSignedURL(ctx, row.ProjectID, srcID, cloudinarySignedURLTTL)
	if err != nil {
		return 0, fmt.Errorf("mint signed url: %w", err)
	}

	args := map[string]any{
		"resource_type": "video",
		"file":          signedURL,
		"eager":         chain,
		// "eager_async":   false,  (default — synchronous eager)
	}
	log.Info("cloudinary upload",
		"id", row.ID, "op", row.Operation, "eager", chain)

	res, err := app.PlatformAPI().ExecuteIntegrationTool(
		e.bound.ConnectionID, "upload", args)
	if err != nil {
		// Use ctx-cancellation reporting; orchestrator distinguishes
		// these from real failures via ctx.Err().
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("cloudinary upload: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return 0, fmt.Errorf("cloudinary non-2xx: %s", truncate(body, 500))
	}

	eagerURL, err := parseCloudinaryEagerURL(res.Data)
	if err != nil {
		return 0, fmt.Errorf("cloudinary response: %w", err)
	}
	log.Info("cloudinary eager ready",
		"id", row.ID, "url", eagerURL)

	// Stream the produced asset straight into storage's upload —
	// avoids reading the whole render output into memory.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eagerURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build download request: %w", err)
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download eager: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		return 0, fmt.Errorf("download eager: HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	uploaded, err := sc.UploadRender(ctx, row.ProjectID,
		e.fallback.outputFolder, plan.Filename, plan.ContentType, httpResp.Body)
	if err != nil {
		return 0, fmt.Errorf("upload result: %w", err)
	}
	return uploaded, nil
}

// ─── transformation builders ───────────────────────────────────────
//
// One per supported op. Each returns a single eager-chain string —
// comma-separated transformation steps, terminated by an `f_<ext>`
// step that pins the output container.

// buildCloudinaryChain dispatches to the per-op builder and pins the
// output format (so the eager URL has a stable extension). outputName
// is read for its extension only — the actual filename is decided by
// buildPlan, which both backends share.
func buildCloudinaryChain(op string, raw json.RawMessage, outputName string) (string, error) {
	switch op {
	case "trim":
		return buildCldTrim(raw, outputName)
	case "resize":
		return buildCldResize(raw, outputName)
	case "transcode":
		return buildCldTranscode(raw, outputName)
	case "crop":
		return buildCldCrop(raw, outputName)
	case "extract_frame":
		return buildCldExtractFrame(raw, outputName)
	}
	return "", fmt.Errorf("cloudinary: unsupported op %q", op)
}

func buildCldTrim(raw json.RawMessage, outputName string) (string, error) {
	var p trimParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("trim params: %w", err)
	}
	if p.EndMs <= p.StartMs {
		return "", errors.New("trim: end_ms must be > start_ms")
	}
	if p.StartMs < 0 {
		return "", errors.New("trim: start_ms must be >= 0")
	}
	startSec := msToCldFloat(p.StartMs)
	durSec := msToCldFloat(p.EndMs - p.StartMs)
	chain := fmt.Sprintf("so_%s,du_%s", startSec, durSec)
	return appendCldFormat(chain, outputName, "mp4"), nil
}

func buildCldResize(raw json.RawMessage, outputName string) (string, error) {
	var p resizeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("resize params: %w", err)
	}
	if p.Width <= 0 {
		return "", errors.New("resize: width must be > 0")
	}
	if !p.KeepAspect && p.Height <= 0 {
		return "", errors.New("resize: height must be > 0 unless keep_aspect=true")
	}
	var step string
	if p.KeepAspect {
		// c_limit caps width while keeping aspect; height is ignored
		// when omitted, which matches local ffmpeg's `-2` behaviour.
		step = fmt.Sprintf("c_limit,w_%d", p.Width)
	} else {
		step = fmt.Sprintf("c_scale,w_%d,h_%d", p.Width, p.Height)
	}
	return appendCldFormat(step, outputName, "mp4"), nil
}

func buildCldTranscode(raw json.RawMessage, outputName string) (string, error) {
	var p transcodeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("transcode params: %w", err)
	}
	if p.Format == "" {
		return "", errors.New("transcode: format required")
	}
	parts := []string{}
	// Cloudinary's video codec param is vc_<codec>; bitrate is
	// br_<value>. Map only what callers asked for so the chain stays
	// minimal.
	if p.VideoCodec != "" {
		parts = append(parts, "vc_"+normaliseCldVideoCodec(p.VideoCodec))
	}
	if p.Bitrate != "" {
		parts = append(parts, "br_"+strings.TrimSpace(p.Bitrate))
	}
	parts = append(parts, "f_"+strings.ToLower(p.Format))
	return strings.Join(parts, ","), nil
}

func buildCldCrop(raw json.RawMessage, outputName string) (string, error) {
	var p cropParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("crop params: %w", err)
	}
	if p.Width <= 0 || p.Height <= 0 {
		return "", errors.New("crop: width and height must be > 0")
	}
	if p.X < 0 || p.Y < 0 {
		return "", errors.New("crop: x and y must be >= 0")
	}
	step := fmt.Sprintf("c_crop,w_%d,h_%d,x_%d,y_%d", p.Width, p.Height, p.X, p.Y)
	return appendCldFormat(step, outputName, "mp4"), nil
}

func buildCldExtractFrame(raw json.RawMessage, outputName string) (string, error) {
	var p extractFrameParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("extract_frame params: %w", err)
	}
	if p.AtMs < 0 {
		return "", errors.New("extract_frame: at_ms must be >= 0")
	}
	parts := []string{"so_" + msToCldFloat(p.AtMs)}
	if p.Width > 0 {
		parts = append(parts, fmt.Sprintf("c_scale,w_%d", p.Width))
	}
	parts = append(parts, "f_png")
	return strings.Join(parts, ","), nil
}

// ─── helpers ───────────────────────────────────────────────────────

// msToCldFloat formats milliseconds as decimal seconds without
// trailing zeros. Cloudinary accepts both "5" and "5.000"; the trim
// keeps eager URLs short + readable.
func msToCldFloat(ms int64) string {
	if ms%1000 == 0 {
		return strconv.FormatInt(ms/1000, 10)
	}
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

// appendCldFormat tacks an `f_<ext>` step onto chain, deriving ext
// from outputName if explicit, else falling back to def.
func appendCldFormat(chain, outputName, def string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(outputName)), ".")
	if ext == "" {
		ext = def
	}
	return chain + ",f_" + ext
}

// normaliseCldVideoCodec maps the ffmpeg codec names we accept (the
// transcodeParams.VideoCodec field is documented as "libx264|libx265|
// libvpx-vp9|...") to Cloudinary's vc_* shorthand. Unknown values
// pass through unchanged — Cloudinary will reject them with a clear
// error rather than us silently transforming them.
func normaliseCldVideoCodec(c string) string {
	switch strings.ToLower(c) {
	case "libx264", "h264":
		return "h264"
	case "libx265", "hevc", "h265":
		return "h265"
	case "libvpx-vp9", "vp9":
		return "vp9"
	case "libaom-av1", "av1":
		return "av1"
	}
	return strings.ToLower(c)
}

// parseCloudinaryEagerURL pulls the first eager output's secure_url
// from Cloudinary's upload response. Falls back to the asset's
// top-level secure_url when no eager block is present (Cloudinary
// sometimes inlines a single eager into the asset itself, depending
// on the account's plan).
func parseCloudinaryEagerURL(body []byte) (string, error) {
	var resp struct {
		SecureURL string `json:"secure_url"`
		URL       string `json:"url"`
		Eager     []struct {
			SecureURL string `json:"secure_url"`
			URL       string `json:"url"`
		} `json:"eager"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(resp.Eager) > 0 {
		if u := resp.Eager[0].SecureURL; u != "" {
			return u, nil
		}
		if u := resp.Eager[0].URL; u != "" {
			return u, nil
		}
	}
	if resp.SecureURL != "" {
		return resp.SecureURL, nil
	}
	if resp.URL != "" {
		return resp.URL, nil
	}
	return "", errors.New("no eager / secure_url in cloudinary response")
}
