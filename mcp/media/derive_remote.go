package main

// Remote indexer — when render_host_id > 0, runs probe + thumbnail +
// waveform on the bound Instances host instead of downloading the
// source to the Apteva machine and processing locally. The win is
// bandwidth + disk: a 50 GB video stays on the storage backend
// (R2/S3/Hetzner OS), the host reads only the kilobytes ffmpeg
// actually needs via HTTP range requests, and the Apteva machine
// sees zero bytes.
//
// Single SSH round-trip per indexed file: one bash script does
// probe → conditional thumbnail/waveform → uploads back to storage
// → echoes an APTEVA_INDEX:{...} marker that media parses to get
// the probe metadata + derivation file_ids.
//
// Auto-fallback: any error from the remote path is logged and the
// caller falls back to the local indexer. So a flaky Hetzner network
// degrades to local indexing instead of blocking the worker.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// remoteIndexParams carries everything the indexing script needs.
// Mirrors what the local processOne reads from indexerConfig, plus
// the host_id + naming context.
type remoteIndexParams struct {
	HostID     int64
	SignedURL  string // source file, with HTTP range support
	ThumbSeek  float64
	ThumbWidth int
	WaveW      int
	WaveH      int
	FileID     string // source file_id (string), used to name derivations
}

// remoteIndexResult is the parsed APTEVA_INDEX marker the script
// emits. ThumbnailFileID / WaveformFileID are populated only when
// the corresponding derivation was actually produced (the script
// branches on probe).
type remoteIndexResult struct {
	ProbeBase64     string `json:"probe_b64"`
	ThumbnailFileID int64  `json:"thumbnail_file_id"`
	WaveformFileID  int64  `json:"waveform_file_id"`
}

// runRemoteIndexing executes the whole indexer pipeline on the
// remote host and returns the parsed probe + derivation file_ids
// that storage already has. Caller writes the media row + derivation
// rows using these values (no further upload from media's side).
func runRemoteIndexing(
	ctx context.Context, app *sdk.AppCtx, projectID string,
	params remoteIndexParams,
) (*Probe, int64, int64, error) {
	publicURL, err := resolvePublicURL(app)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("remote indexing requires a publicly reachable storage URL: %w", err)
	}
	storageToken := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if storageToken == "" {
		storageToken = os.Getenv("APTEVA_APP_TOKEN")
	}
	if storageToken == "" {
		return nil, 0, 0, errors.New("no outbound storage token (APTEVA_OUTBOUND_TOKEN/APP_TOKEN); remote indexing requires it")
	}

	// Pre-flight: ffmpeg + ffprobe present on the remote. Cached after
	// first success — same machinery the render executor uses.
	paths, err := sharedRemoteInstaller().Ensure(ctx, app, params.HostID)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("ffmpeg unavailable on host_id=%d: %w", params.HostID, err)
	}

	script := buildRemoteIndexScript(remoteIndexScriptInputs{
		FFmpeg:       paths.FFmpeg,
		FFprobe:      paths.FFprobe,
		SignedURL:    params.SignedURL,
		FileID:       params.FileID,
		ThumbSeek:    params.ThumbSeek,
		ThumbWidth:   params.ThumbWidth,
		WaveW:        params.WaveW,
		WaveH:        params.WaveH,
		PublicURL:    publicURL,
		StorageToken: storageToken,
		ProjectID:    projectID,
	})

	out, exit, err := runRemote(ctx, app, params.HostID, script, 120)
	if err != nil {
		// runRemote returns err non-nil when the SSH session itself
		// reports failure (typically a non-zero exit via ssh.ExitError).
		// The captured stdout still carries the script's actual error
		// output — include it so the operator sees the real cause
		// instead of "Process exited with status N" with no context.
		if out != "" {
			return nil, 0, 0, fmt.Errorf("remote index ssh: %w (output: %s)", err, truncate(out, 600))
		}
		return nil, 0, 0, fmt.Errorf("remote index ssh: %w", err)
	}
	if exit != 0 {
		return nil, 0, 0, fmt.Errorf("remote index script exit=%d: %s", exit, truncate(out, 800))
	}

	res, err := parseAptevaIndex(out)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("parse remote index result: %w (output=%s)", err, truncate(out, 400))
	}

	probeBytes, err := base64.StdEncoding.DecodeString(res.ProbeBase64)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode probe base64: %w", err)
	}
	probe, err := parseProbeBytes(probeBytes)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("parse probe json: %w", err)
	}
	return probe, res.ThumbnailFileID, res.WaveformFileID, nil
}

// ─── script construction ──────────────────────────────────────────

type remoteIndexScriptInputs struct {
	FFmpeg, FFprobe string
	SignedURL       string
	FileID          string
	ThumbSeek       float64
	ThumbWidth      int
	WaveW, WaveH    int
	PublicURL       string
	StorageToken    string
	ProjectID       string
}

// buildRemoteIndexScript returns the bash program executed on the
// remote. Layout:
//
//  1. Set up scratch workdir + EXIT trap cleanup
//  2. ffprobe via signed URL → probe.json (header reads only, cheap)
//  3. Detect HAS_VIDEO / HAS_AUDIO from the probe JSON (loose sed
//     match — we already have full JSON, but bash needs cheap branches)
//  4. If video → ffmpeg seek + extract → upload to /.media/thumbnail/
//  5. If audio-only → ffmpeg waveform filter → upload to /.media/waveform/
//  6. base64-encode probe.json (avoids JSON-in-JSON escaping pain)
//  7. printf the APTEVA_INDEX marker with all three fields
//
// Uploads go through storage's standard /files multipart endpoint
// (works on any backend, no presigned-PUT branching needed for these
// kilobyte-sized derivations — the latency win wouldn't matter).
func buildRemoteIndexScript(in remoteIndexScriptInputs) string {
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "WORK=%s\n", shellQuote(fmt.Sprintf("/tmp/apteva-media-index-%s", in.FileID)))
	b.WriteString(`mkdir -p "$WORK"; cd "$WORK"` + "\n")
	b.WriteString(`trap 'cd /tmp && rm -rf "$WORK"' EXIT` + "\n")

	// Export config + secrets as env so curl args stay clean and the
	// token doesn't appear in `ps` per process.
	fmt.Fprintf(&b, "export STORAGE_TOKEN=%s\n", shellQuote(in.StorageToken))
	fmt.Fprintf(&b, "export STORAGE_BASE=%s\n", shellQuote(in.PublicURL+"/api/apps/storage"))
	fmt.Fprintf(&b, "export PROJECT_ID=%s\n", shellQuote(in.ProjectID))
	fmt.Fprintf(&b, "export SRC_ID=%s\n", shellQuote(in.FileID))
	fmt.Fprintf(&b, "export SIGNED_URL=%s\n", shellQuote(in.SignedURL))
	fmt.Fprintf(&b, "export FFMPEG=%s\n", shellQuote(in.FFmpeg))
	fmt.Fprintf(&b, "export FFPROBE=%s\n", shellQuote(in.FFprobe))

	// Probe — same flags as the local runProbe, against the signed URL.
	b.WriteString(`"$FFPROBE" -v quiet -print_format json -show_format -show_streams "$SIGNED_URL" > probe.json` + "\n")
	b.WriteString(`HAS_VIDEO=$(grep -c '"codec_type": *"video"' probe.json || true)` + "\n")
	b.WriteString(`HAS_AUDIO=$(grep -c '"codec_type": *"audio"' probe.json || true)` + "\n")

	// Initialise output ids — empty until we successfully upload.
	b.WriteString(`THUMB_FILE_ID=""` + "\n")
	b.WriteString(`WAVE_FILE_ID=""` + "\n")

	// Image vs video distinction. ffmpeg's container-level seek (`-ss`)
	// only makes sense for sources with a timeline; on a single-frame
	// image (JPEG/PNG/GIF/single-frame video) `-ss 1.0` seeks past EOF
	// and ffmpeg exits 0 with no output file — which then breaks curl's
	// upload (exit 26 "read error") and the whole script aborts.
	//
	// We mirror the local extractImageThumbnail / extractVideoThumbnail
	// split: an image-codec stream (mjpeg/png/gif/webp/bmp/tiff) or a
	// container with no format.duration is treated as an image and gets
	// no seek; everything else gets the seek.
	b.WriteString(`IS_IMAGE=0` + "\n")
	b.WriteString(`if grep -qE '"codec_name": *"(mjpeg|png|gif|webp|bmp|tiff)"' probe.json; then IS_IMAGE=1; fi` + "\n")
	b.WriteString(`if ! grep -q '"duration":' probe.json; then IS_IMAGE=1; fi` + "\n")

	fmt.Fprintf(&b, `if [ "$HAS_VIDEO" -gt 0 ]; then`+"\n")
	fmt.Fprintf(&b, `  if [ "$IS_IMAGE" = "1" ]; then`+"\n")
	fmt.Fprintf(&b, `    "$FFMPEG" -y -loglevel error -i "$SIGNED_URL" -frames:v 1 -vf scale=%d:-2 -q:v 2 thumb.jpg`+"\n",
		in.ThumbWidth)
	fmt.Fprintf(&b, `  else`+"\n")
	fmt.Fprintf(&b, `    "$FFMPEG" -y -loglevel error -ss %s -i "$SIGNED_URL" -frames:v 1 -vf scale=%d:-2 -q:v 2 thumb.jpg`+"\n",
		strconv.FormatFloat(in.ThumbSeek, 'f', 3, 64), in.ThumbWidth)
	fmt.Fprintf(&b, `  fi`+"\n")
	// Belt + suspenders: if ffmpeg silently produced nothing (some
	// codec edge cases exit 0 without writing the output), don't
	// hand curl an empty path — fail loudly here so the surfaced
	// error names the actual problem.
	b.WriteString(`  if [ ! -s thumb.jpg ]; then echo "ffmpeg produced no thumbnail output" >&2; exit 1; fi` + "\n")
	b.WriteString(`  RESP=$(curl -sS --fail -X POST \
    -H "Authorization: Bearer $STORAGE_TOKEN" \
    -F "folder=/.media/thumbnail/" \
    -F "visibility=private" \
    -F "source=media-derivation" \
    -F "tags=derivation" \
    -F "file=@thumb.jpg;type=image/jpeg;filename=$SRC_ID.jpg" \
    "$STORAGE_BASE/files?project_id=$PROJECT_ID")` + "\n")
	b.WriteString(`  THUMB_FILE_ID=$(echo "$RESP" | sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)` + "\n")
	b.WriteString(`fi` + "\n")

	// Waveform for audio-only sources (skip if there's a video stream
	// — that gets the thumbnail above).
	fmt.Fprintf(&b, `if [ "$HAS_AUDIO" -gt 0 ] && [ "$HAS_VIDEO" -eq 0 ]; then`+"\n")
	fmt.Fprintf(&b, `  "$FFMPEG" -y -loglevel error -i "$SIGNED_URL" -filter_complex "showwavespic=s=%dx%d:colors=#7f7f7f" -frames:v 1 waveform.png`+"\n",
		in.WaveW, in.WaveH)
	// Same guard as the thumbnail branch — empty output would crash
	// curl's -F upload with the unhelpful exit 26 "read error".
	b.WriteString(`  if [ ! -s waveform.png ]; then echo "ffmpeg produced no waveform output" >&2; exit 1; fi` + "\n")
	b.WriteString(`  RESP=$(curl -sS --fail -X POST \
    -H "Authorization: Bearer $STORAGE_TOKEN" \
    -F "folder=/.media/waveform/" \
    -F "visibility=private" \
    -F "source=media-derivation" \
    -F "tags=derivation" \
    -F "file=@waveform.png;type=image/png;filename=$SRC_ID.png" \
    "$STORAGE_BASE/files?project_id=$PROJECT_ID")` + "\n")
	b.WriteString(`  WAVE_FILE_ID=$(echo "$RESP" | sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)` + "\n")
	b.WriteString(`fi` + "\n")

	// Encode probe.json so the marker line stays single-line + safe
	// to embed inside JSON. `base64 -w0` is GNU; the busybox fallback
	// (`base64 | tr -d '\n'`) covers Alpine.
	b.WriteString(`PROBE_B64=$(base64 -w0 probe.json 2>/dev/null || base64 < probe.json | tr -d '\n')` + "\n")

	// Default empty ids to "0" so the marker line is always valid JSON
	// with numeric fields (avoids "":"" parse on the media side).
	b.WriteString(`: "${THUMB_FILE_ID:=0}"` + "\n")
	b.WriteString(`: "${WAVE_FILE_ID:=0}"` + "\n")

	b.WriteString(`printf 'APTEVA_INDEX:{"probe_b64":"%s","thumbnail_file_id":%s,"waveform_file_id":%s}\n' "$PROBE_B64" "$THUMB_FILE_ID" "$WAVE_FILE_ID"` + "\n")
	return b.String()
}

// ─── parsers ──────────────────────────────────────────────────────

var aptevaIndexRE = regexp.MustCompile(`(?m)^APTEVA_INDEX:(\{[^}]+\})\s*$`)

func parseAptevaIndex(stdout string) (*remoteIndexResult, error) {
	m := aptevaIndexRE.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return nil, errors.New("no APTEVA_INDEX marker in remote output")
	}
	var r remoteIndexResult
	if err := json.Unmarshal([]byte(m[1]), &r); err != nil {
		return nil, fmt.Errorf("decode marker: %w", err)
	}
	if r.ProbeBase64 == "" {
		return nil, errors.New("marker missing probe_b64")
	}
	return &r, nil
}
