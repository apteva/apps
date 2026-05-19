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
	// Keyframe config for the remote shell. Empty/zero values mean
	// "use the script's defaults" — interval 30s, cap 60, enabled.
	// The local indexer pre-resolves these from app.Config() before
	// dispatch.
	KeyframeIntervalSecs int
	KeyframeMaxCount     int
	KeyframesEnabled     bool
}

// remoteIndexResult is the parsed APTEVA_INDEX marker the script
// emits. ThumbnailFileID / WaveformFileID are populated only when
// the corresponding derivation was actually produced (the script
// branches on probe).
type remoteIndexResult struct {
	ProbeBase64     string             `json:"probe_b64"`
	ThumbnailFileID int64              `json:"thumbnail_file_id"`
	WaveformFileID  int64              `json:"waveform_file_id"`
	// Keyframes is the storyboard set produced by the remote shell.
	// One entry per (position_ms, storage_file_id) pair that uploaded
	// successfully. Failures inside the loop are skipped per-frame
	// (the loop continues), so this can be shorter than the requested
	// position count.
	Keyframes []remoteKeyframe `json:"keyframes,omitempty"`
}

// remoteKeyframe is one entry in the storyboard returned by the
// remote indexer.
type remoteKeyframe struct {
	PositionMs    int64 `json:"position_ms"`
	StorageFileID int64 `json:"storage_file_id"`
}

// runRemoteIndexing executes the whole indexer pipeline on the
// remote host and returns the parsed probe + derivation file_ids
// that storage already has. Caller writes the media row + derivation
// rows using these values (no further upload from media's side).
func runRemoteIndexing(
	ctx context.Context, app *sdk.AppCtx, projectID string,
	params remoteIndexParams,
) (*Probe, int64, int64, []remoteKeyframe, error) {
	publicURL, err := resolvePublicURL(app)
	if err != nil {
		return nil, 0, 0, nil, fmt.Errorf("remote indexing requires a publicly reachable storage URL: %w", err)
	}
	storageToken := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if storageToken == "" {
		storageToken = os.Getenv("APTEVA_APP_TOKEN")
	}
	if storageToken == "" {
		return nil, 0, 0, nil, errors.New("no outbound storage token (APTEVA_OUTBOUND_TOKEN/APP_TOKEN); remote indexing requires it")
	}

	// Pre-flight: ffmpeg + ffprobe present on the remote. Cached after
	// first success — same machinery the render executor uses.
	paths, err := sharedRemoteInstaller().Ensure(ctx, app, params.HostID)
	if err != nil {
		return nil, 0, 0, nil, fmt.Errorf("ffmpeg unavailable on host_id=%d: %w", params.HostID, err)
	}

	script := buildRemoteIndexScript(remoteIndexScriptInputs{
		FFmpeg:                 paths.FFmpeg,
		FFprobe:                paths.FFprobe,
		SignedURL:              params.SignedURL,
		FileID:                 params.FileID,
		ThumbSeek:              params.ThumbSeek,
		ThumbWidth:             params.ThumbWidth,
		WaveW:                  params.WaveW,
		WaveH:                  params.WaveH,
		KeyframeIntervalSecs:   params.KeyframeIntervalSecs,
		KeyframeMaxCount:       params.KeyframeMaxCount,
		KeyframesEnabled:       params.KeyframesEnabled,
		PublicURL:              publicURL,
		StorageToken:           storageToken,
		ProjectID:              projectID,
	})

	out, exit, err := runRemote(ctx, app, params.HostID, script, 600)
	if err != nil {
		// runRemote returns err non-nil when the SSH session itself
		// reports failure (typically a non-zero exit via ssh.ExitError).
		// The captured stdout still carries the script's actual error
		// output — include it so the operator sees the real cause
		// instead of "Process exited with status N" with no context.
		if out != "" {
			return nil, 0, 0, nil, fmt.Errorf("remote index ssh: %w (output: %s)", err, truncate(out, 600))
		}
		return nil, 0, 0, nil, fmt.Errorf("remote index ssh: %w", err)
	}
	if exit != 0 {
		return nil, 0, 0, nil, fmt.Errorf("remote index script exit=%d: %s", exit, truncate(out, 800))
	}

	res, err := parseAptevaIndex(out)
	if err != nil {
		return nil, 0, 0, nil, fmt.Errorf("parse remote index result: %w (output=%s)", err, truncate(out, 400))
	}

	probeBytes, err := base64.StdEncoding.DecodeString(res.ProbeBase64)
	if err != nil {
		return nil, 0, 0, nil, fmt.Errorf("decode probe base64: %w", err)
	}
	probe, err := parseProbeBytes(probeBytes)
	if err != nil {
		return nil, 0, 0, nil, fmt.Errorf("parse probe json: %w", err)
	}
	return probe, res.ThumbnailFileID, res.WaveformFileID, res.Keyframes, nil
}

// ─── script construction ──────────────────────────────────────────

type remoteIndexScriptInputs struct {
	FFmpeg, FFprobe string
	SignedURL       string
	FileID          string
	ThumbSeek       float64
	ThumbWidth      int
	WaveW, WaveH    int
	// Keyframe params: zero/empty means "skip the keyframes block".
	// IntervalSecs default 30; MaxCount default 60. KeyframesEnabled
	// must be set true to emit the block at all.
	KeyframeIntervalSecs int
	KeyframeMaxCount     int
	KeyframesEnabled     bool
	PublicURL            string
	StorageToken         string
	ProjectID            string
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
	// THUMB_SEEK is the user-configured fallback seek (same value the
	// local extractVideoThumbnail uses as its first candidate). The
	// multi-attempt block below tries it FIRST, then progressively
	// later positions if the frame at THUMB_SEEK is too dark.
	fmt.Fprintf(&b, "export THUMB_SEEK=%s\n", shellQuote(strconv.FormatFloat(in.ThumbSeek, 'f', 3, 64)))
	fmt.Fprintf(&b, "export THUMB_WIDTH=%d\n", in.ThumbWidth)

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

	// Smart video thumbnailing — ports the local extractVideoThumbnail
	// strategy (mcp/media/derive.go) to the remote shell:
	//
	//   1. Try the configured THUMB_SEEK first (back-compat + lets users
	//      pin a known-good moment via thumbnail_seek_seconds).
	//   2. Then sample 5/15/30/50/75% of duration, skipping positions
	//      too close to start/end and deduping to 0.1s granularity.
	//   3. For each seek: extract via ffmpeg's `thumbnail=30` filter,
	//      which picks the most representative frame from a 30-frame
	//      window via RGB-histogram scoring (skips uniform / fade-y
	//      frames automatically).
	//   4. Decode the produced JPEG and compute mean luminance via
	//      ffmpeg's signalstats; reject anything below 25/255 (the
	//      same threshold as the local path) and try the next seek.
	//   5. Final attempt's output is kept regardless — better an
	//      under-exposed thumbnail than no thumbnail at all.
	//
	// Pre-this-block the script did one fixed seek with no smart
	// filter and no luma check, so the thumbnail was whatever raw
	// frame happened to land at THUMB_SEEK — typically a studio logo,
	// title card, or black opening. This restores parity with what
	// the local sidecar produces.
	fmt.Fprintf(&b, `if [ "$HAS_VIDEO" -gt 0 ]; then`+"\n")
	fmt.Fprintf(&b, `  if [ "$IS_IMAGE" = "1" ]; then`+"\n")
	fmt.Fprintf(&b, `    "$FFMPEG" -y -loglevel error -i "$SIGNED_URL" -frames:v 1 -vf "scale=$THUMB_WIDTH:-2" -q:v 2 thumb.jpg`+"\n")
	fmt.Fprintf(&b, `  else`+"\n")
	// Parse duration from probe.json — first numeric "duration":"…" we
	// find (the format-level one). Empty → DUR_SEC=0 → only the
	// configured fallback seek is tried.
	b.WriteString(`    DUR_SEC=$(sed -n 's/.*"duration"[[:space:]]*:[[:space:]]*"\([0-9.]*\)".*/\1/p' probe.json | head -1)` + "\n")
	b.WriteString(`    : "${DUR_SEC:=0}"` + "\n")
	// luma_of: mean-Y of a JPEG, via ffmpeg's signalstats. Empty
	// output (parse fail / corrupt JPEG) becomes 0, which is treated
	// as "too dark" and triggers the next attempt.
	b.WriteString(`    luma_of() {` + "\n")
	b.WriteString(`      "$FFMPEG" -hide_banner -nostats -i "$1" -vf "signalstats,metadata=print:file=-" -f null /dev/null 2>&1 \` + "\n")
	b.WriteString(`        | sed -n 's/.*signalstats\.YAVG=\([0-9.]*\).*/\1/p' | head -1` + "\n")
	b.WriteString(`    }` + "\n")
	// Build seek list: configured THUMB_SEEK first, then 5/15/30/50/
	// 75% of duration if duration is known. awk handles float math
	// since busybox sh doesn't.
	b.WriteString(`    SEEKS="$THUMB_SEEK"` + "\n")
	b.WriteString(`    if awk -v d="$DUR_SEC" 'BEGIN{exit !(d > 0)}'; then` + "\n")
	b.WriteString(`      for PCT in 0.05 0.15 0.30 0.50 0.75; do` + "\n")
	b.WriteString(`        CAND=$(awk -v d="$DUR_SEC" -v p="$PCT" 'BEGIN{printf "%.3f", d*p}')` + "\n")
	b.WriteString(`        if awk -v c="$CAND" -v d="$DUR_SEC" 'BEGIN{exit !(c >= 0.5 && c < d - 0.1)}'; then` + "\n")
	b.WriteString(`          SEEKS="$SEEKS $CAND"` + "\n")
	b.WriteString(`        fi` + "\n")
	b.WriteString(`      done` + "\n")
	b.WriteString(`    fi` + "\n")
	// Try each seek; first acceptable luma wins, last attempt's
	// output is kept as fallback. set -e is in effect, so wrap the
	// ffmpeg call with "|| continue" — codec/seek failure on one
	// attempt mustn't abort the whole script.
	b.WriteString(`    for S in $SEEKS; do` + "\n")
	b.WriteString(`      "$FFMPEG" -y -loglevel error -ss "$S" -i "$SIGNED_URL" -vf "thumbnail=30,scale=$THUMB_WIDTH:-2" -frames:v 1 -q:v 3 thumb.jpg 2>/dev/null || continue` + "\n")
	b.WriteString(`      [ ! -s thumb.jpg ] && continue` + "\n")
	b.WriteString(`      L=$(luma_of thumb.jpg)` + "\n")
	b.WriteString(`      [ -z "$L" ] && continue` + "\n")
	b.WriteString(`      if awk -v l="$L" 'BEGIN{exit !(l >= 25)}'; then break; fi` + "\n")
	b.WriteString(`    done` + "\n")
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

	// Keyframes — storyboard frames at regular intervals. Only for
	// video sources (audio-only has no frames; images are single-
	// frame). Each iteration: extract one frame via the same
	// `thumbnail=30` smart-frame filter as the canonical thumbnail,
	// upload, append the resulting file_id to a JSON array under
	// KEYFRAMES_JSON. The array is emitted as part of the
	// APTEVA_INDEX marker below.
	//
	// awk + a small shell loop handles the interval/cap math without
	// requiring python on the remote. First keyframe sits at 1s in
	// (skips splash / black opens, same convention as the canonical
	// thumbnail's default seek).
	if in.KeyframesEnabled {
		interval := in.KeyframeIntervalSecs
		if interval <= 0 {
			interval = defaultKeyframeIntervalSeconds
		}
		maxCount := in.KeyframeMaxCount
		if maxCount <= 0 {
			maxCount = defaultKeyframeMaxCount
		}
		fmt.Fprintf(&b, "export KEYFRAME_INTERVAL_SECS=%d\n", interval)
		fmt.Fprintf(&b, "export KEYFRAME_MAX_COUNT=%d\n", maxCount)
		b.WriteString(`KEYFRAMES_JSON=""` + "\n")
		fmt.Fprintf(&b, `if [ "$HAS_VIDEO" -gt 0 ] && [ "$IS_IMAGE" = "0" ] && awk -v d="$DUR_SEC" 'BEGIN{exit !(d > 0)}'; then`+"\n")
		// Compute effective interval: natural = (dur - 1)/interval.
		// If natural+1 > max, stretch interval = (dur-1)/(max-1).
		b.WriteString(`  EFFECTIVE_INTERVAL=$(awk -v d="$DUR_SEC" -v iv="$KEYFRAME_INTERVAL_SECS" -v mx="$KEYFRAME_MAX_COUNT" 'BEGIN{
    natural=int((d-1)/iv);
    if (natural+1 > mx) {
      printf "%.3f", (d-1)/(mx-1);
    } else {
      printf "%d", iv;
    }
  }')` + "\n")
		b.WriteString(`  KF_INDEX=0` + "\n")
		b.WriteString(`  POS_SEC=1` + "\n")
		// Loop until pos >= dur OR we hit the cap.
		b.WriteString(`  while awk -v p="$POS_SEC" -v d="$DUR_SEC" -v c="$KF_INDEX" -v mx="$KEYFRAME_MAX_COUNT" 'BEGIN{exit !(p < d && c < mx)}'; do` + "\n")
		// Position in milliseconds (integer, for filename + JSON).
		b.WriteString(`    POS_MS=$(awk -v p="$POS_SEC" 'BEGIN{printf "%d", p*1000}')` + "\n")
		b.WriteString(`    KF_FILE="kf-${POS_MS}.jpg"` + "\n")
		// Extract with the smart-frame filter — same as thumbnail. If
		// ffmpeg fails for this position, skip and continue to the
		// next; one failed frame mustn't kill the whole loop.
		b.WriteString(`    "$FFMPEG" -y -loglevel error -ss "$POS_SEC" -i "$SIGNED_URL" -vf "thumbnail=30,scale=$THUMB_WIDTH:-2" -frames:v 1 -q:v 3 "$KF_FILE" 2>/dev/null || { POS_SEC=$(awk -v p="$POS_SEC" -v iv="$EFFECTIVE_INTERVAL" 'BEGIN{printf "%.3f", p+iv}'); continue; }` + "\n")
		b.WriteString(`    [ ! -s "$KF_FILE" ] && { POS_SEC=$(awk -v p="$POS_SEC" -v iv="$EFFECTIVE_INTERVAL" 'BEGIN{printf "%.3f", p+iv}'); continue; }` + "\n")
		// Upload the keyframe to storage under /.media/keyframe/.
		// Position-tagged filename so storage's per-folder uniqueness
		// doesn't clobber prior frames.
		b.WriteString(`    KF_RESP=$(curl -sS --fail -X POST \
      -H "Authorization: Bearer $STORAGE_TOKEN" \
      -F "folder=/.media/keyframe/" \
      -F "visibility=private" \
      -F "source=media-derivation" \
      -F "tags=derivation,keyframe" \
      -F "file=@${KF_FILE};type=image/jpeg;filename=${SRC_ID}-${POS_MS}.jpg" \
      "$STORAGE_BASE/files?project_id=$PROJECT_ID" || true)` + "\n")
		b.WriteString(`    KF_ID=$(echo "$KF_RESP" | sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)` + "\n")
		// Append {position_ms, storage_file_id} to JSON array.
		b.WriteString(`    if [ -n "$KF_ID" ]; then` + "\n")
		b.WriteString(`      if [ -z "$KEYFRAMES_JSON" ]; then` + "\n")
		b.WriteString(`        KEYFRAMES_JSON="{\"position_ms\":$POS_MS,\"storage_file_id\":$KF_ID}"` + "\n")
		b.WriteString(`      else` + "\n")
		b.WriteString(`        KEYFRAMES_JSON="$KEYFRAMES_JSON,{\"position_ms\":$POS_MS,\"storage_file_id\":$KF_ID}"` + "\n")
		b.WriteString(`      fi` + "\n")
		b.WriteString(`      KF_INDEX=$((KF_INDEX+1))` + "\n")
		b.WriteString(`    fi` + "\n")
		// rm the local file before next iteration so /tmp doesn't fill
		// for a long video with 60 keyframes.
		b.WriteString(`    rm -f "$KF_FILE"` + "\n")
		b.WriteString(`    POS_SEC=$(awk -v p="$POS_SEC" -v iv="$EFFECTIVE_INTERVAL" 'BEGIN{printf "%.3f", p+iv}')` + "\n")
		b.WriteString(`  done` + "\n")
		fmt.Fprintf(&b, `fi`+"\n")
	} else {
		b.WriteString(`KEYFRAMES_JSON=""` + "\n")
	}

	// Encode probe.json so the marker line stays single-line + safe
	// to embed inside JSON. `base64 -w0` is GNU; the busybox fallback
	// (`base64 | tr -d '\n'`) covers Alpine.
	b.WriteString(`PROBE_B64=$(base64 -w0 probe.json 2>/dev/null || base64 < probe.json | tr -d '\n')` + "\n")

	// Default empty ids to "0" so the marker line is always valid JSON
	// with numeric fields (avoids "":"" parse on the media side).
	b.WriteString(`: "${THUMB_FILE_ID:=0}"` + "\n")
	b.WriteString(`: "${WAVE_FILE_ID:=0}"` + "\n")

	// APTEVA_INDEX marker: probe_b64 + ids + keyframes array. The
	// keyframes field is an empty array when KEYFRAMES_JSON is unset.
	b.WriteString(`printf 'APTEVA_INDEX:{"probe_b64":"%s","thumbnail_file_id":%s,"waveform_file_id":%s,"keyframes":[%s]}\n' "$PROBE_B64" "$THUMB_FILE_ID" "$WAVE_FILE_ID" "$KEYFRAMES_JSON"` + "\n")
	return b.String()
}

// ─── parsers ──────────────────────────────────────────────────────

// aptevaIndexRE matches the JSON envelope. Pre-v0.13.0 used a
// non-greedy `[^}]+` body — that broke when the marker started
// carrying nested arrays/objects like "keyframes":[{...},{...}]
// because the `[^}]` stops at the first inner `}`. The new regex
// finds the line, grabs everything from `{` to end-of-line, and
// relies on json.Unmarshal to validate the structure.
var aptevaIndexRE = regexp.MustCompile(`(?m)^APTEVA_INDEX:(\{.*)$`)

func parseAptevaIndex(stdout string) (*remoteIndexResult, error) {
	m := aptevaIndexRE.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return nil, errors.New("no APTEVA_INDEX marker in remote output")
	}
	var r remoteIndexResult
	if err := json.Unmarshal([]byte(m[1]), &r); err != nil {
		return nil, fmt.Errorf("decode marker: %w (raw=%s)", err, truncate(m[1], 300))
	}
	if r.ProbeBase64 == "" {
		return nil, errors.New("marker missing probe_b64")
	}
	return &r, nil
}
