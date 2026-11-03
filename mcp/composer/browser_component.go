package main

// Browser captures (V2 browser renders and web-canvas-1 procedures) on the
// renderer component. The plan carries the same document and capture script
// the local renderer uses; asset sources become files fetched by the worker.
import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type rendererInput struct{ Name, URL string }

// rendererInputs names each distinct source once; the plan fetches them all
// before capture.
type rendererInputs struct {
	byURL map[string]string
	order []rendererInput
}

var rendererExtension = regexp.MustCompile(`^\.[a-z0-9]{1,9}$`)

func (in *rendererInputs) add(url, kind string) string {
	if name, ok := in.byURL[url]; ok {
		return name
	}
	ext := procedureInputExtension(url, kind)
	if !rendererExtension.MatchString(ext) {
		ext = ".bin"
	}
	return in.named(fmt.Sprintf("inputs/%d%s", len(in.order), ext), url)
}

func (in *rendererInputs) named(name, url string) string {
	if in.byURL == nil {
		in.byURL = map[string]string{}
	}
	in.byURL[url] = name
	in.order = append(in.order, rendererInput{Name: name, URL: url})
	return name
}

// workerFileURL addresses a plan file from the captured document. The token is
// replaced with the job directory on the worker.
func workerFileURL(name string) string { return "file://" + rendererWorkdirToken + "/" + name }

// browserCaptureTimeout leaves headroom for first-use dependency provisioning
// on the worker plus a generous per-frame budget for small hosts.
func browserCaptureTimeout(frames int) int {
	timeout := 600 + frames*3
	if timeout > 14400 {
		timeout = 14400
	}
	return timeout
}

// capture runs the shared CDP capture script against scene.html in the job
// directory, reporting progress between from and to.
func (s *rendererScript) capture(width, height, fps, frames int, excerptStart, from, to float64) {
	s.run(`PORT=$("$NODE_BIN" -e "const s=require('net').createServer();s.listen(0,'127.0.0.1',()=>{console.log(s.address().port);s.close()})")`)
	s.run(fmt.Sprintf(`COMPOSER_EXCERPT_START=%g COMPOSER_PROGRESS_FROM=%g COMPOSER_PROGRESS_TO=%g "$NODE_BIN" capture.js "$CHROME_BIN" "file://$WORKURL/scene.html" "$WORKDIR/frames" %d %d %d %d "$PORT" "$WORKDIR/chrome-profile"`,
		excerptStart, from, to, width, height, fps, frames))
}

func renderV2BrowserComponent(ctx context.Context, app *sdk.AppCtx, spec *V2Composition, projectID string, excerptStart, excerptEnd float64) (Result, []string, error) {
	start := time.Now()
	g, err := browserFrameGeometry(spec, excerptStart, excerptEnd)
	if err != nil {
		return Result{}, nil, err
	}
	inputs := &rendererInputs{}
	source := func(kind string) func(string) (string, error) {
		return func(src string) (string, error) {
			u, err := resolveAssetURL(app, src)
			if err != nil {
				return "", err
			}
			return inputs.add(u, kind), nil
		}
	}
	visual := source("image")
	renderSpec, err := browserSpecWith(spec, func(src string) (string, error) {
		name, err := visual(src)
		return workerFileURL(name), err
	})
	if err != nil {
		return Result{}, nil, err
	}
	audio := source("audio")
	ffArgs, err := buildV2EncodeArgs(func(src string) (string, error) {
		name, err := audio(src)
		return "./" + name, err
	}, spec, "frames/frame_%06d.jpg", "./out.mp4", g.Duration, g.FPS)
	if err != nil {
		return Result{}, nil, err
	}
	cmd := shellEcho("ffmpeg", ffArgs)
	filename := fmt.Sprintf("composition-v2-%d.mp4", time.Now().UnixNano())
	receipt, err := runRendererJob(ctx, app, projectID, rendererJob{
		Stage:   "browser",
		Timeout: browserCaptureTimeout(g.Frames),
		Weight:  2,
		Needs:   []string{"browser"},
		Build: func(env rendererEnv) (string, error) {
			s := newRendererScript()
			s.workdirFile("scene.html", []byte(browserHTML(renderSpec)))
			s.file("capture.js", []byte(browserCaptureScript()))
			for _, in := range inputs.order {
				s.fetch(in.Name, env.source(in.URL))
			}
			s.progress(0.05)
			s.capture(g.Width, g.Height, g.FPS, g.Frames, excerptStart, 0.05, 0.85)
			s.run(cmd)
			s.progress(0.95)
			s.upload(env, "./out.mp4", filename, "video/mp4")
			return s.String(), nil
		},
	})
	if err != nil {
		return Result{FFmpegCommand: redactSecrets(cmd)}, nil, err
	}
	res, err := parseRendererResult(receipt.Output)
	if err != nil {
		return Result{FFmpegCommand: redactSecrets(cmd)}, nil, fmt.Errorf("renderer result parse: %w (raw: %s)", err, redactSecrets(truncTail(receipt.Output, 600)))
	}
	reportRenderProgress(ctx, RenderProgress{Fraction: 1, OutTimeSeconds: g.Duration, Frame: int64(g.Frames)})
	return Result{
		Sync:          true,
		LocalPath:     fmt.Sprintf("storage://files/%d", res.StorageID),
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: redactSecrets(cmd),
	}, []string{"browser composer/v2 renderer used for CSS scene graph and component motion (renderer component)"}, nil
}

// runProcedureComponent captures a web-canvas-1 procedure on the renderer and
// returns the Storage file it uploaded. The artifact is encoded by Composer's
// own plan, so the duration comes from ffprobe on the worker.
func runProcedureComponent(parent context.Context, appCtx *sdk.AppCtx, revision *ProcedureRevisionRecord, binding *ProceduralAsset, output Output, timeline procedureTimeline) (procedureRunResult, error) {
	result := procedureRunResult{Kind: revision.OutputKind, RuntimeJobID: timeline.JobID}
	if revision.Runtime != webCanvasRuntime {
		return result, fmt.Errorf("runtime %q runs in the Composer sidecar, not on the renderer", revision.Runtime)
	}
	inputs := &rendererInputs{}
	assets := map[string]webCanvasAsset{}
	names := make([]string, 0, len(binding.Inputs))
	for name := range binding.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		input := binding.Inputs[name]
		if err := validateProcedureInputName(name); err != nil {
			return result, err
		}
		if input == nil || strings.TrimSpace(input.Src) == "" {
			return result, fmt.Errorf("procedure input %q has no materialized src", name)
		}
		u, err := resolveAssetURL(appCtx, input.Src)
		if err != nil {
			return result, fmt.Errorf("resolve procedure input %q: %w", name, err)
		}
		ext := procedureInputExtension(u, input.Kind)
		if !rendererExtension.MatchString(ext) {
			ext = ".bin"
		}
		file := inputs.named("assets/"+name+ext, u)
		assets[name] = webCanvasAsset{URL: workerFileURL(file), Kind: input.Kind}
	}
	width, height := resolutionWH(output.Resolution, output.Aspect)
	if width <= 0 || height <= 0 {
		return result, errors.New("web-canvas-1 needs a canvas size")
	}
	if timeline.FPS <= 0 {
		timeline.FPS = 30
	}
	fps := timeline.FPS
	canvas := map[string]any{"width": width, "height": height, "pixel_aspect": 1, "color_space": "srgb"}
	parameters := binding.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	doc, err := buildWebCanvasDocument(revision, webCanvasEnv{Mode: "render", Canvas: canvas, Timeline: timeline, Parameters: parameters, Assets: assets})
	if err != nil {
		return result, err
	}
	frames := 1
	if revision.Target != "still" {
		frames = int(timeline.Duration*float64(fps) + 0.5)
		if frames < 1 {
			return result, errors.New("web-canvas-1 clip duration must be > 0")
		}
	}
	artifact, mime := "out.mp4", "video/mp4"
	encode := []string{"-y", "-loglevel", "error", "-framerate", strconv.Itoa(fps), "-i", "frames/frame_%06d.jpg"}
	if revision.OutputKind == "image" {
		artifact, mime = "out.png", "image/png"
		encode = append(encode, "-frames:v", "1", artifact)
	} else {
		encode = append(encode, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-preset", "medium", "-crf", "18", "-r", strconv.Itoa(fps), "-movflags", "+faststart", artifact)
	}
	filename := fmt.Sprintf("procedure-%d-r%d-%d.%s", revision.ProcedureID, revision.Revision, time.Now().UnixNano(), strings.TrimPrefix(artifact, "out."))
	receipt, err := runRendererJob(parent, appCtx, appCtx.CurrentProject(), rendererJob{
		Stage:   "procedure:" + timeline.JobID,
		Timeout: int(proceduralTimeout(appCtx).Seconds()) + 600,
		Weight:  2,
		Needs:   []string{"browser"},
		Build: func(env rendererEnv) (string, error) {
			s := newRendererScript()
			s.workdirFile("scene.html", []byte(doc))
			s.file("capture.js", []byte(browserCaptureScript()))
			for _, in := range inputs.order {
				s.fetch(in.Name, env.source(in.URL))
			}
			s.capture(width, height, fps, frames, timeline.SourceStart, 0.05, 0.85)
			s.run(shellEcho("ffmpeg", encode))
			s.upload(env, artifact, filename, mime)
			return s.String(), nil
		},
	})
	result.Logs = truncTail(redactSecrets(receipt.Output), procedureLogLimit)
	if err != nil {
		return result, err
	}
	res, err := parseRendererResult(receipt.Output)
	if err != nil {
		return result, fmt.Errorf("renderer result parse: %w", err)
	}
	result.StorageID = res.StorageID
	if result.Kind != "image" {
		result.DurationSeconds = res.Duration
		declared := float64(frames) / float64(fps)
		if result.DurationSeconds <= 0 || math.Abs(result.DurationSeconds-declared) > math.Max(0.25, declared*0.05) {
			return result, fmt.Errorf("procedure artifact duration %.3fs does not match the captured %.3fs", result.DurationSeconds, declared)
		}
	}
	return result, nil
}
