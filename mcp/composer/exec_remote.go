package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const composerBoundStorageProxyPath = "/api/apps/callback/apps/storage/proxy"

// remoteFFmpegExecutor runs the same ffmpeg command on a host managed
// by the `instances` app. Strategy lifted from media's remote_exec.go:
//
//  1. Pre-flight: ffmpeg + ffprobe installed on the remote (cached
//     after first success).
//  2. Resolve every asset.src to a URL the remote can curl (storage's
//     signed URLs cover the storage:N case; https:// pass-through).
//  3. SSH a single bash script via instances.instance_run_command
//     that downloads the inputs, runs ffmpeg with the same filter
//     graph the local executor builds, then multipart-POSTs the
//     output back to Storage through the binding-gated callback proxy and echoes a result
//     marker the sidecar parses.
//
// Remote renders return a Storage file id directly. We do not stream
// the result through the Composer sidecar; the selected host uploads
// the finished file through the platform callback proxy. The platform
// validates Composer's Storage binding and swaps in Storage's token.
type remoteFFmpegExecutor struct {
	hostID int64
}

func (e *remoteFFmpegExecutor) Name() string { return "remote" }

func (e *remoteFFmpegExecutor) Render(
	ctx context.Context,
	app *sdk.AppCtx,
	edit *Edit,
	output Output,
	projectID string,
) (Result, error) {
	start := time.Now()

	// Pre-flight: instances app must be bound (best-effort check via
	// CallApp dry-run; instances will surface the error if not).
	if err := remotePreflight(app, e.hostID); err != nil {
		return Result{}, fmt.Errorf("remote preflight on host_id=%d: %w", e.hostID, err)
	}

	// Resolve every input to a URL the remote can fetch. storage:N →
	// signed URL via storage.files_get_url; https:// pass-through.
	track := primaryVisualTrack(edit)
	audioClips := audioTimelineClips(edit)
	urls := []string{}
	if track != nil {
		visualRefs := visualClipRefs(edit)
		urls = make([]string, 0, len(visualRefs)+len(audioClips)+1)
		for i, ref := range visualRefs {
			c := ref.clip
			url, err := resolveAssetURL(app, c.Asset.Src)
			if err != nil {
				return Result{}, fmt.Errorf("visual clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
			}
			urls = append(urls, url)
		}
	} else {
		urls = make([]string, 0, len(audioClips)+1)
	}
	remoteAudioCount := 0
	for i, c := range audioClips {
		if clipAssetType(c, "audio") == "silence" {
			continue
		}
		url, err := resolveAssetURL(app, c.Asset.Src)
		if err != nil {
			return Result{}, fmt.Errorf("audio clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		urls = append(urls, url)
		remoteAudioCount++
	}
	if s := edit.Timeline.Soundtrack; s != nil {
		url, err := resolveAssetURL(app, s.Src)
		if err != nil {
			return Result{}, fmt.Errorf("soundtrack resolve %q: %w", s.Src, err)
		}
		urls = append(urls, url)
	}

	// Build the same ffmpeg arg list the local executor uses, but
	// against local-on-remote file paths. We let bash assemble them
	// from the curl outputs by referring to ./in0, ./in1, … below.
	soundtrackIdx := -1
	if edit.Timeline.Soundtrack != nil {
		if track != nil {
			soundtrackIdx = totalVisualClipCount(edit) + remoteAudioCount
		} else {
			soundtrackIdx = remoteAudioCount
		}
	}
	localPaths := make([]string, len(urls))
	for i := range urls {
		localPaths[i] = fmt.Sprintf("./in%d", i)
	}
	var args []string
	if track == nil {
		args = buildLocalAudioFFmpegArgs(edit, output, localPaths, soundtrackIdx, "./out."+output.Format)
	} else {
		args = buildLocalFFmpegArgsWithAudioInfo(edit, output, localPaths, soundtrackIdx, "./out."+output.Format, remoteVisualAudioDefaults(edit))
	}
	fontFaces := composerFontFacesInArgs(args)
	if len(fontFaces) > 0 {
		fontPaths := make(map[string]string, len(fontFaces))
		for _, face := range fontFaces {
			fontPaths[face.ID] = "./" + face.Filename
		}
		args = materializeComposerFontArgs(args, fontPaths)
	}
	cmd := shellEcho("ffmpeg", args)

	publicURL, err := resolveComposerPublicURL(app)
	if err != nil {
		return Result{FFmpegCommand: cmd}, err
	}
	token := outboundToken()
	if token == "" {
		return Result{FFmpegCommand: cmd}, errors.New("remote render requires APTEVA_OUTBOUND_TOKEN or APTEVA_APP_TOKEN for Storage upload")
	}
	filename := fmt.Sprintf("composition-remote-%d.%s", time.Now().UnixNano(), output.Format)
	fontURLs := make(map[string]string, len(fontFaces))
	for _, face := range fontFaces {
		fontURLs[face.ID] = strings.TrimRight(publicURL, "/") + "/api/apps/composer/render-font?project_id=" + url.QueryEscape(projectID) + "&face=" + url.QueryEscape(face.ID)
	}
	script := remoteRenderScript(urls, cmd, output.Format, projectID, publicURL, token, filename, renderContentType(output.Format), fontURLs)

	app.Logger().Info("remote ffmpeg render", "host_id", e.hostID, "inputs", len(urls), "format", output.Format)

	res, err := remoteRunScript(ctx, e.hostID, script)
	if err != nil {
		return Result{FFmpegCommand: cmd}, remoteExecFailure(err, res)
	}

	storageID, parseErr := parseRemoteResult(res)
	if parseErr != nil {
		return Result{FFmpegCommand: cmd}, fmt.Errorf("remote result parse: %w (raw: %s)", parseErr, truncTail(res, 600))
	}

	return Result{
		Sync:          true,
		LocalPath:     fmt.Sprintf("storage://files/%d", storageID),
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: cmd,
	}, nil
}

func remoteExecFailure(err error, output string) error {
	detail := strings.TrimSpace(output)
	if detail == "" {
		return fmt.Errorf("remote exec: %w", err)
	}
	return fmt.Errorf("remote exec: %w\nremote output (last 4KB):\n%s", err, truncTail(detail, 4096))
}

func remoteVisualAudioDefaults(edit *Edit) []bool {
	refs := visualClipRefs(edit)
	if len(refs) == 0 {
		return nil
	}
	out := make([]bool, len(refs))
	for i, ref := range refs {
		out[i] = clipAssetType(ref.clip, "visual") == "video" && visualClipMayUseSourceAudioForLayer(ref.clip, ref.base)
	}
	return out
}

// remotePreflight checks the instances app is reachable and the host
// exists. We trust the selected host has ffmpeg on PATH; operator
// feedback is the remote command's ffmpeg error if not.
func remotePreflight(app *sdk.AppCtx, hostID int64) error {
	if app == nil {
		return errors.New("nil app ctx")
	}
	var probe struct {
		ID       int64 `json:"id"`
		Instance *struct {
			ID int64 `json:"id"`
		} `json:"instance"`
	}
	err := app.PlatformAPI().CallAppResult("instances", "instance_get",
		map[string]any{"id": hostID}, &probe)
	if err != nil {
		return fmt.Errorf("instance_get failed (is instances bound?): %w", err)
	}
	gotID := probe.ID
	if gotID == 0 && probe.Instance != nil {
		gotID = probe.Instance.ID
	}
	if gotID != hostID {
		return fmt.Errorf("instances returned id=%d, want %d", gotID, hostID)
	}
	return nil
}

// remoteRenderScript assembles the bash script the remote runs.
// Convention: input URLs become ./in0, ./in1, … in the working dir,
// the ffmpeg command is appended verbatim, and the output is
// echoed back as APTEVA_RESULT:{...} for the parser.
func remoteRenderScript(urls []string, ffmpegCmd, format, projectID, publicURL, token, filename, contentType string, fontURLs map[string]string) string {
	var b strings.Builder
	b.WriteString("set -eu -o pipefail\n")
	b.WriteString("WORKDIR=$(mktemp -d)\n")
	b.WriteString("trap 'rm -rf \"$WORKDIR\"' EXIT\n")
	b.WriteString("cd \"$WORKDIR\"\n")
	b.WriteString(remoteFFmpegBootstrapScript())
	for _, face := range composerFontFaces {
		fontURL := strings.TrimSpace(fontURLs[face.ID])
		if fontURL == "" {
			continue
		}
		fmt.Fprintf(&b, "curl -fsSL --retry 3 -H %q -o ./%s %q\n", "Authorization: Bearer "+token, face.Filename, fontURL)
	}
	for i, u := range urls {
		fmt.Fprintf(&b, "curl -fsSL --retry 3 -o ./in%d %q\n", i, u)
	}
	b.WriteString(ffmpegCmd)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "BYTES=$(stat -c %%s ./out.%s 2>/dev/null || stat -f %%z ./out.%s)\n", format, format)
	b.WriteString("if command -v sha256sum >/dev/null 2>&1; then\n")
	b.WriteString("  SHA=$(sha256sum ./out.* | awk '{print $1}')\n")
	b.WriteString("elif command -v shasum >/dev/null 2>&1; then\n")
	b.WriteString("  SHA=$(shasum -a 256 ./out.* | awk '{print $1}')\n")
	b.WriteString("else\n")
	b.WriteString("  echo \"missing sha256sum/shasum on remote render host\" >&2\n")
	b.WriteString("  exit 127\n")
	b.WriteString("fi\n")
	fmt.Fprintf(&b, "export STORAGE_TOKEN=%q\n", token)
	fmt.Fprintf(&b, "export STORAGE_BASE=%q\n", strings.TrimRight(publicURL, "/")+composerBoundStorageProxyPath)
	fmt.Fprintf(&b, "export PROJECT_ID=%q\n", projectID)
	b.WriteString("export FOLDER=/.composer/\n")
	fmt.Fprintf(&b, "export NAME=%q\n", shellFormValue(filename))
	fmt.Fprintf(&b, "export CT=%q\n", shellFormValue(contentType))
	fmt.Fprintf(&b, "export OUT=./out.%s\n", format)
	b.WriteString(remoteStorageUploadScriptFragment)
	b.WriteString(`echo "APTEVA_RESULT:{\"storage_id\":${STORAGE_ID},\"bytes\":${BYTES},\"sha256\":\"${SHA}\",\"format\":\"` + format + `\"}"` + "\n")
	return b.String()
}

// remoteStorageUploadScriptFragment uploads $OUT back to the Storage
// app from the remote render host. It mirrors Media's hardened upload
// ladder: direct presigned upload when the backend supports it, chunked
// /uploads fallback for proxy-backed installs, and single multipart
// POST only as a legacy last resort.
const remoteStorageUploadScriptFragment = `CURL_RETRY=(--retry 3 --retry-delay 1 --retry-max-time 120 --retry-connrefused --retry-all-errors)
INIT_BODY_FILE=$(mktemp)
INIT_CODE=$(curl -sS "${CURL_RETRY[@]}" -o "$INIT_BODY_FILE" -w "%{http_code}" \
  -X POST \
  -H "Authorization: Bearer $STORAGE_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$NAME\",\"folder\":\"$FOLDER\",\"content_type\":\"$CT\",\"size_bytes\":$BYTES,\"sha256\":\"$SHA\",\"visibility\":\"private\",\"source\":\"composer-render\",\"tags\":[\"composer\",\"render\"]}" \
  "$STORAGE_BASE/files/init?project_id=$PROJECT_ID" || echo 000)
STORAGE_ID=""
NEED_MULTIPART=1
if [ "$INIT_CODE" = "200" ]; then
  UPLOAD_URL=$(sed -n 's/.*"upload_url":[[:space:]]*"\([^"]*\)".*/\1/p' "$INIT_BODY_FILE")
  UPLOAD_URL=$(printf '%s' "$UPLOAD_URL" | sed -e 's/\\u0026/\&/g' -e 's/\\u003c/</g' -e 's/\\u003e/>/g')
  UPLOAD_ID=$(sed -n 's/.*"upload_id":[[:space:]]*"\([^"]*\)".*/\1/p' "$INIT_BODY_FILE")
  if [ -n "$UPLOAD_URL" ] && [ -n "$UPLOAD_ID" ]; then
    NEED_MULTIPART=0
    curl -sS "${CURL_RETRY[@]}" --fail -o /dev/null -X PUT -H "Content-Type: $CT" --upload-file "$OUT" "$UPLOAD_URL"
    FIN_BODY=$(curl -sS "${CURL_RETRY[@]}" --fail -X POST \
      -H "Authorization: Bearer $STORAGE_TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"sha256\":\"$SHA\"}" \
      "$STORAGE_BASE/files/$UPLOAD_ID/finalize?project_id=$PROJECT_ID")
    STORAGE_ID=$(echo "$FIN_BODY" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' | head -1)
  elif grep -q '"was_existing"[[:space:]]*:[[:space:]]*true' "$INIT_BODY_FILE"; then
    STORAGE_ID=$(sed -n 's/.*"file":[[:space:]]*{[[:space:]]*"id":[[:space:]]*\([0-9]*\).*/\1/p' "$INIT_BODY_FILE")
    if [ -n "$STORAGE_ID" ]; then
      NEED_MULTIPART=0
    else
      echo "STORAGE_INIT_DEDUP_NO_ID body[0:300]=$(head -c 300 "$INIT_BODY_FILE" | tr '\n\r\t' '   ')" >&2
    fi
  else
    echo "STORAGE_INIT_UNPARSEABLE code=$INIT_CODE body[0:300]=$(head -c 300 "$INIT_BODY_FILE" | tr '\n\r\t' '   ')" >&2
  fi
else
  echo "STORAGE_INIT_FAILED code=$INIT_CODE body[0:300]=$(head -c 300 "$INIT_BODY_FILE" | tr '\n\r\t' '   ')" >&2
fi
rm -f "$INIT_BODY_FILE"
if [ "$NEED_MULTIPART" = "1" ]; then
  UPLOADS_INIT_FILE=$(mktemp)
  UPLOADS_INIT_CODE=$(curl -sS "${CURL_RETRY[@]}" -o "$UPLOADS_INIT_FILE" -w "%{http_code}" \
    -X POST \
    -H "Authorization: Bearer $STORAGE_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"filename\":\"$NAME\",\"folder\":\"$FOLDER\",\"content_type\":\"$CT\",\"size\":$BYTES,\"sha256\":\"$SHA\",\"visibility\":\"private\",\"source\":\"composer-render\",\"tags\":[\"composer\",\"render\"]}" \
    "$STORAGE_BASE/uploads?project_id=$PROJECT_ID" || echo 000)
  if [ "$UPLOADS_INIT_CODE" = "200" ]; then
    if grep -q '"was_existing"[[:space:]]*:[[:space:]]*true' "$UPLOADS_INIT_FILE"; then
      STORAGE_ID=$(sed -n 's/.*"file":[[:space:]]*{[[:space:]]*"id":[[:space:]]*\([0-9]*\).*/\1/p' "$UPLOADS_INIT_FILE")
      if [ -n "$STORAGE_ID" ]; then
        NEED_MULTIPART=0
      else
        echo "STORAGE_UPLOADS_DEDUP_NO_ID body[0:300]=$(head -c 300 "$UPLOADS_INIT_FILE" | tr '\n\r\t' '   ')" >&2
      fi
    else
      CHUNK_UPLOAD_ID=$(sed -n 's/.*"upload_id":[[:space:]]*"\([^"]*\)".*/\1/p' "$UPLOADS_INIT_FILE")
      PART_SIZE=$(sed -n 's/.*"part_size":[[:space:]]*\([0-9]*\).*/\1/p' "$UPLOADS_INIT_FILE")
      if [ -n "$CHUNK_UPLOAD_ID" ]; then
        NEED_MULTIPART=0
        PART_SIZE=${PART_SIZE:-5242880}
        PART_FILE=$(mktemp)
        OFFSET=0
        PART=1
        while [ "$OFFSET" -lt "$BYTES" ]; do
          dd if="$OUT" of="$PART_FILE" bs="$PART_SIZE" skip="$OFFSET" count="$PART_SIZE" iflag=skip_bytes,count_bytes status=none
          curl -sS "${CURL_RETRY[@]}" --fail -o /dev/null -X PUT \
            -H "Content-Type: application/octet-stream" \
            --data-binary "@$PART_FILE" \
            "$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/parts/$PART?project_id=$PROJECT_ID"
          OFFSET=$((OFFSET + PART_SIZE))
          PART=$((PART + 1))
        done
        rm -f "$PART_FILE"
        COMPLETE_BODY=$(curl -sS "${CURL_RETRY[@]}" --fail -X POST \
          -H "Authorization: Bearer $STORAGE_TOKEN" \
          -H "Content-Type: application/json" \
          -d "{\"sha256\":\"$SHA\"}" \
          "$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/complete?project_id=$PROJECT_ID")
        STORAGE_ID=$(echo "$COMPLETE_BODY" | sed -n 's/.*"file":[[:space:]]*{[[:space:]]*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)
        if [ -z "$STORAGE_ID" ]; then
          echo "STORAGE_UPLOADS_COMPLETE_NO_ID body[0:300]=$(printf '%s' "$COMPLETE_BODY" | head -c 300 | tr '\n\r\t' '   ')" >&2
          exit 1
        fi
      else
        echo "STORAGE_UPLOADS_INIT_UNPARSEABLE code=$UPLOADS_INIT_CODE body[0:300]=$(head -c 300 "$UPLOADS_INIT_FILE" | tr '\n\r\t' '   ')" >&2
      fi
    fi
  else
    echo "STORAGE_UPLOADS_INIT_FAILED code=$UPLOADS_INIT_CODE body[0:300]=$(head -c 300 "$UPLOADS_INIT_FILE" | tr '\n\r\t' '   ')" >&2
  fi
  rm -f "$UPLOADS_INIT_FILE"
fi
if [ "$NEED_MULTIPART" = "1" ]; then
  LEGACY_BODY_FILE=$(mktemp)
  LEGACY_CODE=$(curl -sS "${CURL_RETRY[@]}" -o "$LEGACY_BODY_FILE" -w "%{http_code}" -X POST \
    -H "Authorization: Bearer $STORAGE_TOKEN" \
    -F "folder=$FOLDER" \
    -F "visibility=private" \
    -F "source=composer-render" \
    -F "tags=composer" \
    -F "tags=render" \
    -F "file=@$OUT;type=$CT;filename=$NAME" \
    "$STORAGE_BASE/files?project_id=$PROJECT_ID" || echo 000)
  if [ "$LEGACY_CODE" -ge 200 ] 2>/dev/null && [ "$LEGACY_CODE" -lt 300 ] 2>/dev/null; then
    STORAGE_ID=$(sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' "$LEGACY_BODY_FILE" | head -1)
  else
    echo "STORAGE_LEGACY_UPLOAD_FAILED code=$LEGACY_CODE body[0:300]=$(head -c 300 "$LEGACY_BODY_FILE" | tr '\n\r\t' '   ')" >&2
  fi
  rm -f "$LEGACY_BODY_FILE"
fi
if [ -z "$STORAGE_ID" ]; then
  echo "STORAGE_UPLOAD_FAILED" >&2
  exit 1
fi
`

func remoteFFmpegBootstrapScript() string {
	return `if ! command -v ffmpeg >/dev/null 2>&1; then
  INSTALL_DIR="$HOME/.apteva-render/ffmpeg-btbn-n7.1"
  if [ ! -x "$INSTALL_DIR/bin/ffmpeg" ]; then
    ARCH="$(uname -m)"
    case "$ARCH" in
      x86_64|amd64) FFMPEG_URL="https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-n7.1-latest-linux64-gpl-7.1.tar.xz" ;;
      aarch64|arm64) FFMPEG_URL="https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-n7.1-latest-linuxarm64-gpl-7.1.tar.xz" ;;
      *) echo "unsupported remote architecture: $ARCH" >&2; exit 127 ;;
    esac
    mkdir -p "$INSTALL_DIR"
    curl -fsSL --retry 3 "$FFMPEG_URL" -o ./ffmpeg.tar.xz
    tar -xJf ./ffmpeg.tar.xz -C "$INSTALL_DIR" --strip-components=1
  fi
  export PATH="$INSTALL_DIR/bin:$PATH"
fi
`
}

// remoteRunScript SSHes via instances.instance_run_command. Returns
// the combined stdout/stderr.
func remoteRunScript(ctx context.Context, hostID int64, script string) (string, error) {
	var out struct {
		Output   string `json:"output"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
		Err      string `json:"error"`
	}
	if err := callComposerInstancesRunCommand(ctx, 1800, map[string]any{
		"id":        hostID,
		"cmd":       script,
		"timeout_s": 1800,
	}, &out); err != nil {
		return out.Output + out.Stdout + out.Stderr, err
	}
	combined := out.Output
	if combined == "" {
		combined = out.Stdout + "\n" + out.Stderr
	}
	if out.Err != "" {
		return combined, errors.New(out.Err)
	}
	if out.ExitCode != 0 {
		return combined, fmt.Errorf("remote exit_code=%d", out.ExitCode)
	}
	return combined, nil
}

// parseRemoteResult pulls the JSON object after the APTEVA_RESULT:
// marker line.
func parseRemoteResult(s string) (int64, error) {
	idx := strings.Index(s, "APTEVA_RESULT:")
	if idx < 0 {
		return 0, errors.New("APTEVA_RESULT marker missing")
	}
	tail := s[idx+len("APTEVA_RESULT:"):]
	end := strings.Index(tail, "\n")
	if end > 0 {
		tail = tail[:end]
	}
	var got struct {
		StorageID int64 `json:"storage_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(tail)), &got); err != nil {
		return 0, err
	}
	if got.StorageID <= 0 {
		return 0, errors.New("remote result did not include storage_id")
	}
	return got.StorageID, nil
}

func shellFormValue(s string) string {
	return strings.ReplaceAll(s, `"`, "")
}

func outboundToken() string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_OUTBOUND_TOKEN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
}

func resolveComposerPublicURL(app *sdk.AppCtx) (string, error) {
	if app != nil {
		if info, err := app.PlatformInfo(); err == nil && info != nil && info.PublicURL != "" {
			return strings.TrimRight(info.PublicURL, "/"), nil
		}
	}
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")), "/"); v != "" {
		return v, nil
	}
	return "", errors.New("APTEVA_PUBLIC_URL not set in platform settings or env")
}

func callComposerInstancesRunCommand(ctx context.Context, timeoutS int, input map[string]any, out any) error {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	token := outboundToken()
	if timeoutS <= 0 {
		timeoutS = 30
	}
	body, err := json.Marshal(map[string]any{
		"tool":  "instance_run_command",
		"input": input,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/apps/callback/apps/instances/call", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: time.Duration(timeoutS)*time.Second + 30*time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, readErr := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		if readErr != nil {
			return fmt.Errorf("instances call: HTTP %d; additionally failed reading body: %w", res.StatusCode, readErr)
		}
		return fmt.Errorf("instances call: HTTP %d: %s", res.StatusCode, truncTail(string(raw), 500))
	}
	if readErr != nil {
		return readErr
	}
	return decodeComposerMCPEnvelope(raw, "instances", "instance_run_command", out)
}

func decodeComposerMCPEnvelope(raw []byte, appName, tool string, out any) error {
	if out == nil {
		return errors.New("decode remote MCP envelope: out is nil")
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s.%s: empty response", appName, tool)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return json.Unmarshal(raw, out)
	}
	if env.Error != nil {
		return fmt.Errorf("%s.%s: %s (code=%d)", appName, tool, env.Error.Message, env.Error.Code)
	}
	if len(env.Result) > 0 {
		if handled, err := decodeComposerMCPContent(env.Result, appName, tool, out); handled || err != nil {
			return err
		}
	}
	if handled, err := decodeComposerMCPContent(raw, appName, tool, out); handled || err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s.%s: response had no content array and direct decode failed: %w", appName, tool, err)
	}
	return nil
}

func decodeComposerMCPContent(raw json.RawMessage, appName, tool string, out any) (bool, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) == 0 {
		return false, nil
	}
	inner := result.Content[0].Text
	if inner == "" {
		return true, fmt.Errorf("%s.%s: empty content text", appName, tool)
	}
	if result.IsError {
		return true, fmt.Errorf("%s.%s: tool returned error: %.200s", appName, tool, inner)
	}
	if err := json.Unmarshal([]byte(inner), out); err != nil {
		return true, fmt.Errorf("%s.%s: decode inner JSON: %w (text: %.200s)", appName, tool, err, inner)
	}
	return true, nil
}
