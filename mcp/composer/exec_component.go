package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The renderer reaches Storage through Composer's job-scoped relay, which
// forwards to this binding-gated callback proxy with Composer's own token.
const composerBoundStorageProxyPath = "/api/apps/callback/apps/storage/proxy"

// componentFFmpegExecutor runs the same ffmpeg command the local executor
// builds on Composer's renderer component:
//
//  1. Resolve every asset.src to a URL the worker can fetch (Storage signed
//     URLs, rebased onto the gateway the worker reaches; https pass-through).
//  2. Submit one bounded plan that downloads the inputs, writes the bundled
//     fonts, runs ffmpeg and uploads the output to Storage through the
//     job-scoped relay, echoing a result marker the sidecar parses.
//
// The output never streams through the Composer sidecar; Render returns the
// Storage file id directly.
type componentFFmpegExecutor struct{}

func (e *componentFFmpegExecutor) Name() string { return "component" }

func (e *componentFFmpegExecutor) Render(
	ctx context.Context,
	app *sdk.AppCtx,
	edit *Edit,
	output Output,
	projectID string,
) (Result, error) {
	start := time.Now()
	if err := validateEditOutput(edit, output); err != nil {
		return Result{}, err
	}
	// Resolve every input to a URL the worker can fetch. storage:N →
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

	// Build the same ffmpeg arg list the local executor uses, but against
	// worker-local file paths: the plan downloads input i to ./in<i>.
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
		args = buildLocalFFmpegArgsWithAudioInfo(edit, output, localPaths, soundtrackIdx, "./out."+output.Format, componentVisualAudioDefaults(edit))
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
	filename := fmt.Sprintf("composition-%d.%s", time.Now().UnixNano(), output.Format)
	app.Logger().Info("component ffmpeg render", "inputs", len(urls), "format", output.Format)
	receipt, err := runRendererJob(ctx, app, projectID, rendererJob{
		Stage:   "ffmpeg",
		Timeout: 1800,
		Weight:  1,
		Build: func(env rendererEnv) (string, error) {
			s := newRendererScript()
			for _, face := range fontFaces {
				s.file(face.Filename, face.Data)
			}
			for i, u := range urls {
				s.fetch(fmt.Sprintf("in%d", i), env.source(u))
			}
			for _, idx := range componentAudioInputIndices(edit) {
				s.run(componentEnsureAudioScript(fmt.Sprintf("./in%d", idx)))
			}
			s.progress(0.1)
			s.run(cmd)
			s.progress(0.9)
			s.upload(env, "./out."+output.Format, filename, renderContentType(output.Format))
			return s.String(), nil
		},
	})
	if err != nil {
		return Result{FFmpegCommand: cmd}, err
	}
	res, err := parseRendererResult(receipt.Output)
	if err != nil {
		return Result{FFmpegCommand: cmd}, fmt.Errorf("renderer result parse: %w (raw: %s)", err, redactSecrets(truncTail(receipt.Output, 600)))
	}
	return Result{
		Sync:          true,
		LocalPath:     fmt.Sprintf("storage://files/%d", res.StorageID),
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: cmd,
	}, nil
}

func componentVisualAudioDefaults(edit *Edit) []bool {
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

func componentAudioInputIndices(edit *Edit) []int {
	var out []int
	for i, enabled := range componentVisualAudioDefaults(edit) {
		if enabled {
			out = append(out, i)
		}
	}
	return out
}

func componentEnsureAudioScript(path string) string {
	p := shellQuote(path)
	return "if [ -z \"$(ffprobe -v error -select_streams a:0 -show_entries stream=index -of csv=p=0 " + p + ")\" ]; then\n" +
		" ffmpeg -nostdin -y -v error -i " + p + " -f lavfi -i anullsrc=r=48000:cl=stereo -map 0:v:0 -map 1:a:0 -c:v copy -c:a aac -shortest -f matroska " + shellQuote(path+".audio") + "\n mv " + shellQuote(path+".audio") + " " + p + "\nfi\n"
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

// remoteStorageUploadScriptFragment uploads $OUT back to Storage from the
// renderer. It mirrors Media's hardened upload ladder: direct presigned upload
// when the backend supports it, chunked /uploads fallback for proxy-backed
// installs (base64 parts when APTEVA_TRANSFER_ENCODING=base64), and single
// multipart POST only as a legacy last resort.
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
          dd if="$OUT" of="$PART_FILE" bs="$PART_SIZE" skip="$((PART - 1))" count=1 2>/dev/null
          SEND_FILE="$PART_FILE"
          TRANSFER_HEADERS=(-H "Content-Type: application/octet-stream")
          if [ "${APTEVA_TRANSFER_ENCODING:-}" = "base64" ]; then
            base64 < "$PART_FILE" > "$PART_FILE.b64"
            SEND_FILE="$PART_FILE.b64"
            TRANSFER_HEADERS=(-H "Content-Type: text/plain" -H "X-Apteva-Transfer-Encoding: base64")
          fi
          curl -sS "${CURL_RETRY[@]}" --fail -o /dev/null -X PUT \
            -H "Authorization: Bearer $STORAGE_TOKEN" \
            "${TRANSFER_HEADERS[@]}" \
            --data-binary "@$SEND_FILE" \
            "$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/parts/$PART?project_id=$PROJECT_ID"
          rm -f "$PART_FILE.b64"
          OFFSET=$((OFFSET + PART_SIZE))
          PART=$((PART + 1))
        done
        rm -f "$PART_FILE" "$PART_FILE.b64"
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
