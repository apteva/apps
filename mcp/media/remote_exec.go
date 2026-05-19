package main

// Remote render executor — runs ffmpeg on a host managed by the
// `instances` app. Selected when render_host_id config > 0 and the
// instances app is installed.
//
// Flow (per render):
//
//  1. Pre-flight ensures ffmpeg/ffprobe are installed on the remote
//     (cached after first success — see remote_ffmpeg_install.go).
//  2. For each source file_id, mint a time-limited signed URL from
//     storage. The remote pulls the bytes itself; media's process
//     never sees them.
//  3. SSH a single bash script via instances.instance_run_command:
//       - curl signed URLs → local files
//       - (concat only) write the demuxer list file
//       - run ffmpeg with the same args local execution uses, but
//         pointing at the downloaded local files
//       - stat + sha256 the output
//       - multipart POST it back to storage's /files endpoint with
//         media's outbound bearer token
//       - echo a single APTEVA_RESULT:{...} JSON marker line that
//         media parses out of stdout
//  4. Parse the marker, return the file_id storage assigned.
//
// Why upload via curl-from-remote instead of streaming bytes back
// through media's process: a 4 GiB transcode shouldn't double-hop
// through media's small container. The remote already has internet
// egress (instances declares net.egress) and storage is reachable
// at APTEVA_PUBLIC_URL.
//
// Why not the presigned-PUT protocol (storage's /files/init): it
// only works on S3-backed installs (disk backends return 501). The
// multipart POST path works everywhere. Presigned PUT is a worthwhile
// follow-up for S3 installs but not v1.
//
// Cancellation: the script writes $$ → pid before ffmpeg starts.
// On ctx-cancel, registerRemoteKill best-effort SSHes a SIGTERM to
// that pid. The trap on EXIT also rm -rfs the workdir so we don't
// leave scratch around on aborts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// remoteExecutor is constructed once per worker and re-used across
// renders. The installer cache is shared so concurrent first-renders
// to the same host don't race each other on the install probe.
type remoteExecutor struct {
	hostID       int64
	installer    *remoteFFmpegInstaller
	outputFolder string

	// Outbound storage credentials. storageToken is per-install +
	// process-stable (set by the SDK at boot), so caching it on the
	// struct is fine. publicURL is operator-mutable via settings, so
	// it's resolved per-Execute via app.PlatformInfo() — SDK-side 60s
	// cache keeps this cheap.
	storageToken string

	// fallback gives us defaults (output folder) and lets the executor
	// share the local executor's notion of "where renders land". We
	// never invoke fallback.Execute — selectExecutor handles fallthrough
	// when the remote one declines or fails.
	fallback *localExecutor
}

func (e *remoteExecutor) Name() string { return "remote-instance" }

// newRemoteExecutor constructs the executor from worker-level config.
// Returns nil + nil when the feature isn't configured (host_id <= 0)
// so callers can use a clean nil check.
//
// Pre-v0.11.7 this captured APTEVA_PUBLIC_URL into a struct field at
// worker startup; operators who later changed public_url in settings
// had to restart media for it to take effect. v0.11.7 drops the
// cached field — Execute resolves the URL fresh per render via
// app.PlatformInfo() (SDK-side 60s cache so we don't hammer the
// server), so public_url changes propagate within a minute without
// a sidecar restart.
func newRemoteExecutor(hostID int64, installer *remoteFFmpegInstaller, local *localExecutor) (*remoteExecutor, error) {
	if hostID <= 0 {
		return nil, nil
	}
	tok := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if tok == "" {
		tok = os.Getenv("APTEVA_APP_TOKEN")
	}
	if tok == "" {
		return nil, errors.New("render_host_id is set but no outbound storage token in env (APTEVA_OUTBOUND_TOKEN / APTEVA_APP_TOKEN)")
	}
	return &remoteExecutor{
		hostID:       hostID,
		installer:    installer,
		outputFolder: local.outputFolder,
		storageToken: tok,
		fallback:     local,
	}, nil
}

func (e *remoteExecutor) Execute(ctx context.Context, app *sdk.AppCtx, row *RenderRow) (int64, error) {
	log := app.Logger()

	publicURL, err := resolvePublicURL(app)
	if err != nil {
		return 0, fmt.Errorf("remote render needs a publicly reachable storage URL: %w", err)
	}

	paths, err := e.installer.Ensure(ctx, app, e.hostID)
	if err != nil {
		return 0, fmt.Errorf("remote ffmpeg unavailable on host_id=%d: %w", e.hostID, err)
	}

	// Mint signed source URLs. Use a generous TTL because download +
	// render + upload can take a while for big inputs, and a re-mint
	// halfway through would require keeping more state on media's side.
	sc := newStorageClient()

	// Subject-aware crop pre-pass. We hoist this above buildPlan so
	// the planner sees concrete crop_w/h/x/y instead of the symbolic
	// iw/ih expression. preprocessSmartCrop reaches into storage to
	// download the cached thumbnail and into the media DB for source
	// dimensions, so it can only run after sc exists. No-op for ops
	// that don't crop (trim, concat, audio_extract, …).
	row.Params = preprocessSmartCrop(ctx, app, sc, row.ProjectID, row.Operation, row.SourceFileIDs, row.Params)

	plan, err := buildPlan(row.Operation, row.SourceFileIDs, row.Params, row.OutputName)
	if err != nil {
		return 0, fmt.Errorf("build plan: %w", err)
	}
	signedURLs := make([]string, 0, len(row.SourceFileIDs))
	sourceNames := make([]string, 0, len(row.SourceFileIDs))
	for _, fidStr := range row.SourceFileIDs {
		fid, parseErr := strconv.ParseInt(fidStr, 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("source file_id %q not numeric", fidStr)
		}
		meta, getErr := sc.GetFile(ctx, row.ProjectID, fid)
		if getErr != nil {
			return 0, fmt.Errorf("source lookup %d: %w", fid, getErr)
		}
		url, urlErr := sc.GetSignedURL(ctx, row.ProjectID, fid, 3600)
		if urlErr != nil {
			return 0, fmt.Errorf("sign source %d: %w", fid, urlErr)
		}
		signedURLs = append(signedURLs, url)
		sourceNames = append(sourceNames, sanitizeFilename(meta.Name))
	}

	folder := row.OutputFolder
	if folder == "" {
		folder = e.outputFolder
	}

	script, err := e.buildScript(row, plan, paths.FFmpeg, signedURLs, sourceNames, folder, publicURL)
	if err != nil {
		return 0, fmt.Errorf("build remote script: %w", err)
	}

	log.Info("remote render starting",
		"id", row.ID, "op", row.Operation, "host_id", e.hostID,
		"sources", len(row.SourceFileIDs), "output_folder", folder)

	// Per-render best-effort kill on ctx-cancel. Registered before the
	// long-running call so a cancel mid-encode hits the remote PID.
	registerRemoteKill(ctx, app, e.hostID, row.ID)

	// Live progress: poll the remote progress.log every few seconds
	// while ffmpeg runs. Bump the row to 50 % on first activity —
	// same coarse-but-useful signal the local executor surfaces via
	// the -progress pipe:1 stream. Stops when Execute returns.
	progressDone := make(chan struct{})
	defer close(progressDone)
	go pollRemoteProgress(ctx, progressDone, app, e.hostID, row.ID)

	// timeout_s on the run-command call gets the row's wall-clock cap
	// minus a small safety margin (the install-time pre-flight already
	// consumed some of it). We pass through ctx.Deadline if present.
	timeoutS := 1800
	if dl, ok := ctx.Deadline(); ok {
		if rem := int(dl.Sub(time.Now()).Seconds()) - 5; rem > 30 {
			timeoutS = rem
		}
	}

	out, exit, runErr := runRemote(ctx, app, e.hostID, script, timeoutS)
	if runErr != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// SSH-reported non-zero exit lands here with runErr set;
		// preserve the captured script stdout so the operator sees
		// the actual cause (ffmpeg error, curl HTTP code, etc.)
		// rather than just "Process exited with status N".
		if out != "" {
			return 0, fmt.Errorf("remote render: %w (output: %s)", runErr, truncate(out, 1000))
		}
		return 0, fmt.Errorf("remote render: %w", runErr)
	}
	if exit != 0 {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("remote render exit=%d: %s", exit, truncate(out, 1500))
	}

	res, err := parseAptevaResult(out)
	if err != nil {
		return 0, fmt.Errorf("parse remote result: %w (output=%s)", err, truncate(out, 500))
	}
	log.Info("remote render complete",
		"id", row.ID, "file_id", res.FileID, "size", res.Size, "sha256", res.SHA256)
	return res.FileID, nil
}

// ─── script construction ───────────────────────────────────────────

// buildScript renders the bash program the remote executes. Layout
// matches the file header's flow comment.
func (e *remoteExecutor) buildScript(
	row *RenderRow, plan *opPlan,
	ffmpegPath string,
	signedURLs []string, sourceNames []string,
	folder string, publicURL string,
) (string, error) {
	workDir := fmt.Sprintf("/tmp/apteva-render-%d", row.ID)

	// Local-on-remote source paths. Match the local executor's
	// "src-<fid>.<ext>" shape so debugging an ssh-in mirrors local.
	srcPaths := make([]string, 0, len(signedURLs))
	for i, fidStr := range row.SourceFileIDs {
		ext := filepath.Ext(sourceNames[i])
		srcPaths = append(srcPaths, fmt.Sprintf("src-%s%s", fidStr, ext))
	}

	args, err := materialiseRemoteArgs(plan.Args, srcPaths)
	if err != nil {
		return "", err
	}
	args = append(args, plan.Filename)

	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "WORK=%s\n", shellQuote(workDir))
	b.WriteString(`mkdir -p "$WORK"` + "\n")
	b.WriteString(`cd "$WORK"` + "\n")
	b.WriteString("echo $$ > pid\n")
	// Always-rm cleanup. Runs on any exit including non-zero/abort.
	b.WriteString(`trap 'cd /tmp && rm -rf "$WORK"' EXIT` + "\n")

	// Download every source. Signed URLs are time-limited; curl --fail
	// turns HTTP errors into non-zero exits so the script aborts.
	for i, url := range signedURLs {
		fmt.Fprintf(&b, "curl -sS --fail -L -o %s %s\n",
			shellQuote(srcPaths[i]), shellQuote(url))
	}

	// Concat list, written here from media's side rather than via the
	// pool helper. The {concat_list} token in plan.Args has already
	// been replaced with the literal "concat.txt" by materialiseRemoteArgs.
	if row.Operation == "concat" {
		b.WriteString("cat > concat.txt <<'__CONCAT_LIST_EOF__'\n")
		for _, sp := range srcPaths {
			fmt.Fprintf(&b, "file '%s'\n", sp)
		}
		b.WriteString("__CONCAT_LIST_EOF__\n")
	}

	// Run ffmpeg.
	fmt.Fprintf(&b, "%s", shellQuote(ffmpegPath))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	b.WriteString("\n")

	// Stat + hash output before upload.
	fmt.Fprintf(&b, "OUT=%s\n", shellQuote(plan.Filename))
	b.WriteString(`SIZE=$(stat -c%s "$OUT" 2>/dev/null || stat -f%z "$OUT")` + "\n")
	b.WriteString(`SHA=$(sha256sum "$OUT" | awk '{print $1}')` + "\n")

	// Upload. We try storage's presigned-PUT protocol first (works
	// on S3-backed installs — bytes go remote→S3 directly, never
	// proxying through the storage container). On disk-backed
	// installs storage returns 501; we fall back to a single
	// multipart POST through storage. Both paths write FILE_ID for
	// the closing marker line.
	//
	// Env vars carry the inputs so they don't appear in `ps` output
	// and so the inline JSON / curl args stay readable.
	fmt.Fprintf(&b, "export STORAGE_TOKEN=%s\n", shellQuote(e.storageToken))
	fmt.Fprintf(&b, "export STORAGE_BASE=%s\n", shellQuote(publicURL+"/api/apps/storage"))
	fmt.Fprintf(&b, "export PROJECT_ID=%s\n", shellQuote(row.ProjectID))
	fmt.Fprintf(&b, "export FOLDER=%s\n", shellQuote(folder))
	fmt.Fprintf(&b, "export NAME=%s\n", shellQuote(plan.Filename))
	fmt.Fprintf(&b, "export CT=%s\n", shellQuote(plan.ContentType))
	b.WriteString(uploadScriptFragment)

	// Final marker — media's parser looks for the APTEVA_RESULT: prefix.
	b.WriteString(`printf 'APTEVA_RESULT:{"file_id":%s,"size":%s,"sha256":"%s"}\n' "$FILE_ID" "$SIZE" "$SHA"` + "\n")
	return b.String(), nil
}

// uploadScriptFragment is the bash block that uploads $OUT back to
// storage. Pinned as a const because it's pure literal — no values
// from media's side are interpolated. Reads env vars set just above
// it: STORAGE_TOKEN, STORAGE_BASE, PROJECT_ID, FOLDER, NAME, CT,
// SIZE, SHA, OUT. Writes FILE_ID for the marker line.
//
// Presigned path: POST /files/init with name+size+sha256, PUT bytes
// to the returned signed S3 URL, then POST /files/<id>/finalize.
// Multipart fallback: single POST to /files with the file as a part.
const uploadScriptFragment = `INIT_BODY_FILE=$(mktemp)
INIT_CODE=$(curl -sS -o "$INIT_BODY_FILE" -w "%{http_code}" \
  -X POST \
  -H "Authorization: Bearer $STORAGE_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$NAME\",\"folder\":\"$FOLDER\",\"content_type\":\"$CT\",\"size_bytes\":$SIZE,\"sha256\":\"$SHA\",\"visibility\":\"private\",\"source\":\"media-render\",\"tags\":[\"render\"]}" \
  "$STORAGE_BASE/files/init?project_id=$PROJECT_ID" || echo 000)
if [ "$INIT_CODE" = "200" ]; then
  UPLOAD_URL=$(sed -n 's/.*"upload_url":[[:space:]]*"\([^"]*\)".*/\1/p' "$INIT_BODY_FILE")
  # Decode JSON-escaped ampersands. Go's encoding/json escapes & as
  # & by default ("safe" HTML output); our raw sed extraction
  # keeps the literal "&" in the URL, which Hetzner / S3 sees
  # as part of the query-string value, garbling the signature and
  # 403'ing the PUT. One sed pass fixes it. < / > aren't
  # used in S3 URLs but cost nothing to handle defensively.
  UPLOAD_URL=$(printf '%s' "$UPLOAD_URL" | sed -e 's/\\u0026/\&/g' -e 's/\\u003c/</g' -e 's/\\u003e/>/g')
  UPLOAD_ID=$(sed -n 's/.*"upload_id":[[:space:]]*"\([^"]*\)".*/\1/p' "$INIT_BODY_FILE")
  rm -f "$INIT_BODY_FILE"
  if [ -z "$UPLOAD_URL" ] || [ -z "$UPLOAD_ID" ]; then
    echo "STORAGE_INIT_PARSE_FAILED" >&2; exit 1
  fi
  curl -sS --fail -X PUT -H "Content-Type: $CT" --upload-file "$OUT" "$UPLOAD_URL"
  FIN_BODY=$(curl -sS --fail -X POST \
    -H "Authorization: Bearer $STORAGE_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"sha256\":\"$SHA\"}" \
    "$STORAGE_BASE/files/$UPLOAD_ID/finalize?project_id=$PROJECT_ID")
  FILE_ID=$(echo "$FIN_BODY" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' | head -1)
else
  rm -f "$INIT_BODY_FILE"
  RESP=$(curl -sS --fail -X POST \
    -H "Authorization: Bearer $STORAGE_TOKEN" \
    -F "folder=$FOLDER" \
    -F "visibility=private" \
    -F "source=media-render" \
    -F "tags=render" \
    -F "file=@$OUT;type=$CT;filename=$NAME" \
    "$STORAGE_BASE/files?project_id=$PROJECT_ID")
  FILE_ID=$(echo "$RESP" | sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)
fi
if [ -z "$FILE_ID" ]; then
  echo "STORAGE_UPLOAD_FAILED" >&2; exit 1
fi
`

// materialiseRemoteArgs is the remote analogue of materialiseArgs in
// renderexec.go. Two substitutions beyond the placeholder swap:
//
//   - {input}        → src-<fid>.<ext> (local-on-remote path)
//   - {concat_list}  → concat.txt (the script writes the heredoc)
//
// Plus: the per-op planners always emit `-progress pipe:1` to drive
// local progress forwarding. On the remote there's no pipe back to
// media, so we redirect ffmpeg's progress stream to a local file
// (progress.log) that media polls via a separate SSH command.
func materialiseRemoteArgs(template, srcPaths []string) ([]string, error) {
	out := make([]string, 0, len(template))
	for _, a := range template {
		switch a {
		case "{input}":
			if len(srcPaths) != 1 {
				return nil, fmt.Errorf("{input} needs exactly 1 source, got %d", len(srcPaths))
			}
			out = append(out, srcPaths[0])
		case "{concat_list}":
			out = append(out, "concat.txt")
		default:
			out = append(out, a)
		}
	}
	// Post-pass: redirect -progress to a local file.
	for i := 1; i < len(out); i++ {
		if out[i] == "pipe:1" && out[i-1] == "-progress" {
			out[i] = remoteProgressFilename
		}
	}
	return out, nil
}

// remoteProgressFilename is the per-render progress-file name written
// by ffmpeg (-progress <name>) and tailed by media's progress poller.
// Local to the script's $WORK directory.
const remoteProgressFilename = "progress.log"

// ─── result parsing ────────────────────────────────────────────────

type remoteRenderResult struct {
	FileID int64  `json:"file_id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// aptevaResultRE finds the marker line the script always prints on
// success: APTEVA_RESULT:<json>. ffmpeg + curl can be chatty on
// stderr/stdout; we extract the one line we control.
var aptevaResultRE = regexp.MustCompile(`(?m)^APTEVA_RESULT:(\{[^}]+\})\s*$`)

func parseAptevaResult(stdout string) (*remoteRenderResult, error) {
	m := aptevaResultRE.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return nil, errors.New("no APTEVA_RESULT marker in remote output")
	}
	var r remoteRenderResult
	if err := json.Unmarshal([]byte(m[1]), &r); err != nil {
		return nil, fmt.Errorf("decode marker: %w", err)
	}
	if r.FileID == 0 {
		return nil, errors.New("marker had file_id=0")
	}
	return &r, nil
}

// ─── cancellation ──────────────────────────────────────────────────

// pollRemoteProgress tails progress.log on the remote until either
// the per-render done channel closes (Execute returned) or ctx
// cancels. On the first sighting of `out_time_ms=` (ffmpeg is
// actively encoding) it bumps the row's progress_pct to 50. We
// don't compute a finer-grained percentage because the local
// executor doesn't either — without an ffprobe-derived total
// duration the percentage would be a lie. 100 % gets written by
// runOneRender on terminal status.
//
// Poll interval is intentionally coarse (5s). Faster would burn
// SSH connections on every render with no user-visible benefit.
func pollRemoteProgress(ctx context.Context, done <-chan struct{}, app *sdk.AppCtx, hostID, renderID int64) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	bumped := false
	cmd := fmt.Sprintf(
		`tail -n 5 /tmp/apteva-render-%d/progress.log 2>/dev/null | grep -F 'out_time_ms=' | head -1`,
		renderID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if bumped {
				continue
			}
			out, _, err := runRemote(context.Background(), app, hostID, cmd, 8)
			if err != nil {
				// Network blips, partial install, etc. Don't spam logs —
				// next tick will re-attempt. Worst case: progress stays
				// at 0 until terminal.
				continue
			}
			if !strings.Contains(out, "out_time_ms=") {
				continue
			}
			if updErr := renderUpdateProgress(app.AppDB(), renderID, 50); updErr != nil {
				app.Logger().Warn("remote progress bump failed", "id", renderID, "err", updErr)
			}
			bumped = true
		}
	}
}

// registerRemoteKill spawns a tiny goroutine that, on ctx-cancel,
// best-effort kills the remote bash by reading its captured pid and
// SSHing a SIGTERM. Fire-and-forget — the trap in the script cleans
// the workdir; this just makes the abort prompt instead of waiting
// for the run-command timeout.
func registerRemoteKill(ctx context.Context, app *sdk.AppCtx, hostID, renderID int64) {
	go func() {
		<-ctx.Done()
		log := app.Logger()
		killCmd := fmt.Sprintf(
			`PID=$(cat /tmp/apteva-render-%d/pid 2>/dev/null || true); `+
				`if [ -n "$PID" ]; then kill -TERM "$PID" 2>/dev/null || true; fi`,
			renderID)
		// Detached background; run with its own short timeout so a
		// dead host doesn't pin this goroutine.
		_, _, err := runRemote(context.Background(), app, hostID, killCmd, 10)
		if err != nil {
			log.Warn("remote render kill failed", "id", renderID, "host_id", hostID, "err", err)
		}
	}()
}

// ─── helpers ───────────────────────────────────────────────────────

// shellQuote wraps s in single quotes, escaping any embedded single
// quotes. Safe for any string in a POSIX shell argument position.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sanitizeFilename strips any path separator that snuck into a
// storage file's Name. The remote uses this only for choosing the
// .ext we save into; the actual content is keyed by file_id.
func sanitizeFilename(s string) string {
	s = filepath.Base(s)
	if s == "." || s == "/" || s == "" {
		return "file"
	}
	return s
}

// resolvePublicURL returns the platform's externally-reachable base
// URL. Used by every code path that hands a storage URL to a remote
// Hetzner box for direct curl access (signed-URL sources for renders
// + indexer; multipart upload destination; presigned-PUT init+finalize).
//
// Prefers the SDK's hot-cached PlatformInfo() (60s freshness — picks
// up operator settings changes without a sidecar restart). Falls back
// to the legacy APTEVA_PUBLIC_URL env so this works against older
// apteva-server versions that don't yet expose /platform-info. Returns
// "trimmed; trailing slash removed" so callers can `publicURL + "/api/..."`
// safely.
func resolvePublicURL(app *sdk.AppCtx) (string, error) {
	if app != nil {
		if info, err := app.PlatformInfo(); err == nil && info != nil && info.PublicURL != "" {
			return strings.TrimRight(info.PublicURL, "/"), nil
		}
	}
	if v := strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/"); v != "" {
		return v, nil
	}
	return "", errors.New("APTEVA_PUBLIC_URL not set in platform settings or env")
}
