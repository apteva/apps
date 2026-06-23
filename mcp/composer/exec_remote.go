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
//     output back to storage's /files endpoint and echoes a result
//     marker the sidecar parses.
//
// Remote renders return a Storage file id directly. We do not stream
// the result through the Composer sidecar; the selected host uploads
// the finished file to Storage with Composer's outbound token.
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
		urls = make([]string, 0, len(track.Clips)+len(audioClips)+1)
		for i, c := range track.Clips {
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
			soundtrackIdx = len(track.Clips) + remoteAudioCount
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
		args = buildLocalFFmpegArgsWithAudioInfo(edit, output, localPaths, soundtrackIdx, "./out."+output.Format, remoteVisualAudioDefaults(track))
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
	script := remoteRenderScript(urls, cmd, output.Format, projectID, publicURL, token, filename, renderContentType(output.Format))

	app.Logger().Info("remote ffmpeg render", "host_id", e.hostID, "inputs", len(urls), "format", output.Format)

	res, err := remoteRunScript(ctx, e.hostID, script)
	if err != nil {
		return Result{FFmpegCommand: cmd}, fmt.Errorf("remote exec: %w", err)
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

func remoteVisualAudioDefaults(track *Track) []bool {
	if track == nil {
		return nil
	}
	out := make([]bool, len(track.Clips))
	for i, c := range track.Clips {
		out[i] = clipAssetType(c, "visual") == "video" && visualClipMayUseSourceAudio(c)
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
		ID int64 `json:"id"`
	}
	err := app.PlatformAPI().CallAppResult("instances", "instance_get",
		map[string]any{"id": hostID}, &probe)
	if err != nil {
		return fmt.Errorf("instance_get failed (is instances bound?): %w", err)
	}
	if probe.ID != hostID {
		return fmt.Errorf("instances returned id=%d, want %d", probe.ID, hostID)
	}
	return nil
}

// remoteRenderScript assembles the bash script the remote runs.
// Convention: input URLs become ./in0, ./in1, … in the working dir,
// the ffmpeg command is appended verbatim, and the output is
// echoed back as APTEVA_RESULT:{...} for the parser.
func remoteRenderScript(urls []string, ffmpegCmd, format, projectID, publicURL, token, filename, contentType string) string {
	var b strings.Builder
	b.WriteString("set -eu -o pipefail\n")
	b.WriteString("WORKDIR=$(mktemp -d)\n")
	b.WriteString("trap 'rm -rf \"$WORKDIR\"' EXIT\n")
	b.WriteString("cd \"$WORKDIR\"\n")
	b.WriteString(remoteFFmpegBootstrapScript())
	for i, u := range urls {
		fmt.Fprintf(&b, "curl -fsSL --retry 3 -o ./in%d %q\n", i, u)
	}
	b.WriteString(ffmpegCmd)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "BYTES=$(stat -c %%s ./out.%s 2>/dev/null || stat -f %%z ./out.%s)\n", format, format)
	b.WriteString("SHA=$(shasum -a 256 ./out.* | awk '{print $1}')\n")
	uploadURL := strings.TrimRight(publicURL, "/") + "/api/apps/storage/files"
	if projectID != "" {
		uploadURL += "?project_id=" + url.QueryEscape(projectID)
	}
	fmt.Fprintf(&b, "UPLOAD_URL=%q\n", uploadURL)
	fmt.Fprintf(&b, "UPLOAD_TOKEN=%q\n", token)
	fmt.Fprintf(&b, "UPLOAD_RESP=$(curl -fsS --retry 3 -X POST \"$UPLOAD_URL\" -H \"Authorization: Bearer $UPLOAD_TOKEN\" -F folder=/.composer/ -F visibility=private -F source=composer-render -F tags=composer -F tags=render -F file=@./out.%s\\;filename=%s\\;type=%s)\n", format, shellFormValue(filename), shellFormValue(contentType))
	b.WriteString("STORAGE_ID=$(printf '%s' \"$UPLOAD_RESP\" | sed -n 's/.*\"id\"[[:space:]]*:[[:space:]]*\\([0-9][0-9]*\\).*/\\1/p' | head -1)\n")
	b.WriteString("test -n \"$STORAGE_ID\"\n")
	b.WriteString(`echo "APTEVA_RESULT:{\"storage_id\":${STORAGE_ID},\"bytes\":${BYTES},\"sha256\":\"${SHA}\",\"format\":\"` + format + `\"}"` + "\n")
	return b.String()
}

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
