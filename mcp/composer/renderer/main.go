package main

// The owning Composer release supplies trusted render plans, not end-user
// commands. The component has no app bindings, provider secrets or parent token.
// All infrastructure placement is handled by Server's runtime controller.
import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

//go:embed apteva.yaml
var manifestBytes []byte

const protocol = "apteva.composer-renderer/v1"
const outputLimit = 10 << 20
const progressMarker = "APTEVA_PROGRESS:"

var jobPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`)

type request struct {
	JobID   string   `json:"job_id"`
	Command string   `json:"command"`
	Timeout int      `json:"timeout_s"`
	Weight  int      `json:"weight"`
	Control bool     `json:"control"`
	Needs   []string `json:"needs,omitempty"`
}
type receipt struct {
	JobID        string    `json:"job_id"`
	Project      string    `json:"project_id"`
	Fingerprint  string    `json:"fingerprint"`
	Status       string    `json:"status"`
	Progress     float64   `json:"progress,omitempty"`
	Output       string    `json:"output,omitempty"`
	Exit         int       `json:"exit_code"`
	Error        string    `json:"error,omitempty"`
	PID          int       `json:"pid,omitempty"`
	ProcessStart string    `json:"process_start,omitempty"`
	Stopped      bool      `json:"stopped_confirmed"`
	CreatedAt    time.Time `json:"created_at"`
	Deadline     time.Time `json:"deadline"`
}
type job struct {
	receipt
	cancel context.CancelFunc
	done   chan struct{}
}
type RendererApp struct {
	mu             sync.Mutex
	root           string
	active         bool
	jobs           map[string]*job
	capacity       int
	used           int
	waiters        []*struct{ marker byte }
	control        chan struct{}
	slots          chan struct{}
	changed        chan struct{}
	life           context.Context
	stop           context.CancelFunc
	ffmpeg         ffmpegDeps
	browser        browserDeps
	deploymentID   string
	generation     int64
	lifecycleState string
}

func (*RendererApp) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		panic(err)
	}
	return *m
}
func (*RendererApp) ComponentProtocol() string         { return protocol }
func (*RendererApp) Channels() []sdk.ChannelFactory    { return nil }
func (*RendererApp) Workers() []sdk.Worker             { return nil }
func (*RendererApp) EventHandlers() []sdk.EventHandler { return nil }
func (a *RendererApp) OnMount(app *sdk.AppCtx) error {
	a.deploymentID = os.Getenv("APTEVA_DEPLOYMENT_ID")
	if raw := os.Getenv("APTEVA_DEPLOYMENT_GENERATION"); a.deploymentID != "" || raw != "" {
		var err error
		a.generation, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || a.generation <= 0 || a.deploymentID == "" {
			return errors.New("invalid managed deployment identity")
		}
	}
	a.lifecycleState = "standby"
	a.root = filepath.Join(app.DataDir(), "renderer")
	a.capacity = positive(app.Config().Get("work_capacity"), 2)
	if a.capacity > 16 {
		return errors.New("work_capacity exceeds 16")
	}
	a.browser.chromeConfig = strings.TrimSpace(app.Config().Get("chrome_path"))
	a.browser.nodeConfig = strings.TrimSpace(app.Config().Get("node_path"))
	a.browser.prepare = make(chan struct{}, 1)
	a.life, a.stop = context.WithCancel(context.Background())
	if err := os.MkdirAll(a.root, 0700); err != nil {
		return err
	}
	provision, err := a.ffmpeg.resolve(a.root, app.Config().Get("ffmpeg_path"), app.Config().Get("ffprobe_path"))
	if err != nil {
		return err
	}
	a.jobs = map[string]*job{}
	a.changed = make(chan struct{})
	a.control = make(chan struct{}, 4)
	a.slots = make(chan struct{}, 64)
	if err := os.MkdirAll(filepath.Join(a.root, "journal"), 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(a.root, "journal"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.root, "journal", e.Name()))
		if err != nil {
			return err
		}
		var r receipt
		if json.Unmarshal(raw, &r) != nil || !jobPattern.MatchString(r.JobID) {
			return errors.New("invalid renderer journal")
		}
		j := &job{receipt: r, done: make(chan struct{})}
		if !terminal(j.Status) {
			j.Stopped = j.PID == 0
			if j.Status == "starting" && j.PID == 0 {
				j.Stopped = false
			}
			if j.PID > 0 {
				j.Stopped = stopRecorded(j.PID, j.ProcessStart) == nil
			}
			j.Status = "failed"
			j.Error = "renderer_restarted: attempt interrupted; not replayed"
			if !j.Stopped {
				j.Error = "renderer_restarted: previous process termination unconfirmed"
			}
			if err := a.save(j); err != nil {
				return err
			}
		}
		if !j.Stopped {
			return errors.New("previous renderer process termination unconfirmed; backend cleanup is required")
		}
		close(j.done)
		a.jobs[j.JobID] = j
	}
	if provision {
		go func() {
			ctx, cancel := context.WithTimeout(a.life, 30*time.Minute)
			defer cancel()
			_, _, _ = a.ffmpeg.ensure(ctx, a.root)
		}()
	}
	if browserAtMount {
		// Missing system libraries or fonts show in capabilities right after
		// deploy instead of failing the first browser render.
		go func() {
			ctx, cancel := context.WithTimeout(a.life, 30*time.Minute)
			defer cancel()
			_, _ = a.browser.ensure(ctx, a.root)
		}()
	}
	return nil // Always passive until Server activates this generation.
}

// browserAtMount is disabled by tests that must not download Chrome.
var browserAtMount = true
func positive(raw string, fallback int) int {
	n, e := strconv.Atoi(raw)
	if e != nil || n < 1 {
		return fallback
	}
	return n
}
func executable(raw, fallback string) string {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	if raw == "" {
		return ""
	}
	path, err := exec.LookPath(raw)
	if err != nil {
		return ""
	}
	return path
}
func terminal(s string) bool   { return s == "succeeded" || s == "failed" || s == "cancelled" }
func (a *RendererApp) signal() { close(a.changed); a.changed = make(chan struct{}) }
func (a *RendererApp) save(j *job) error {
	raw, e := json.Marshal(j.receipt)
	if e != nil {
		return e
	}
	path := filepath.Join(a.root, "journal", j.JobID+".json")
	f, e := os.CreateTemp(filepath.Dir(path), ".receipt-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e == nil {
		e = dir.Sync()
		dir.Close()
	}
	return e
}
func (a *RendererApp) MCPTools() []sdk.Tool {
	schema := map[string]any{"type": "object", "properties": map[string]any{"job_id": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "timeout_s": map[string]any{"type": "integer"}, "weight": map[string]any{"type": "integer"}, "control": map[string]any{"type": "boolean"}, "needs": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"browser"}}}}}
	return []sdk.Tool{
		{Name: "capabilities", InputSchema: schema, HandlerCtx: a.capabilities},
		{Name: "submit", InputSchema: schema, HandlerCtx: a.submit},
		{Name: "get", InputSchema: schema, HandlerCtx: a.get},
		{Name: "cancel", InputSchema: schema, HandlerCtx: a.cancelJob},
		{Name: "acknowledge", InputSchema: schema, HandlerCtx: a.acknowledge},
	}
}
func (a *RendererApp) capabilities(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	// The gateway URL is where this worker reaches the platform. The parent
	// addresses its job-scoped transfer relay through it; it is not a credential.
	// "browser" is kept for Composer 0.11.2 parents, which read that key.
	ffmpeg, ffprobe := a.ffmpeg.paths()
	browser := a.browser.status()
	return map[string]any{"protocol": protocol, "ffmpeg": ffmpeg, "ffprobe": ffprobe, "ffmpeg_status": a.ffmpeg.status(), "browser_status": browser, "browser": browser,
		"capacity": a.capacity, "platform": executionPlatform(), "gateway_url": strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")}, nil
}
func project(app *sdk.AppCtx) string { return app.CurrentProject() }
func (a *RendererApp) submit(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	raw, _ := json.Marshal(args)
	var req request
	if e := json.Unmarshal(raw, &req); e != nil {
		return nil, e
	}
	if !jobPattern.MatchString(req.JobID) || req.Command == "" || len(req.Command) > 8<<20 || req.Timeout < 1 || req.Timeout > 14400 || req.Weight < 0 || req.Weight > 16 {
		return nil, errors.New("invalid render plan")
	}
	for _, need := range req.Needs {
		if need != "browser" {
			return nil, errors.New("unsupported render dependency")
		}
	}
	if !req.Control && req.Weight < 1 {
		return nil, errors.New("render weight is required")
	}
	if req.Weight > a.capacity {
		req.Weight = a.capacity
	}
	fingerprint := sha256.Sum256(rawCanonical(req))
	a.mu.Lock()
	defer a.mu.Unlock()
	if j := a.jobs[req.JobID]; j != nil {
		if j.Project != project(app) || j.Fingerprint != hex.EncodeToString(fingerprint[:]) {
			return nil, errors.New("job identity conflict")
		}
		return j.receipt, nil
	}
	if !a.active {
		return nil, errors.New("renderer is passive or draining")
	}
	select {
	case a.slots <- struct{}{}:
	default:
		return nil, errors.New("renderer queue full")
	}
	created := time.Now().UTC()
	runCtx, cancel := context.WithDeadline(a.life, created.Add(time.Duration(req.Timeout)*time.Second))
	j := &job{receipt: receipt{JobID: req.JobID, Project: project(app), Fingerprint: hex.EncodeToString(fingerprint[:]), Status: "queued", Exit: -1, CreatedAt: created, Deadline: created.Add(time.Duration(req.Timeout) * time.Second)}, done: make(chan struct{}), cancel: cancel}
	if e := a.save(j); e != nil {
		cancel()
		<-a.slots
		return nil, e
	}
	a.jobs[j.JobID] = j
	go a.run(runCtx, j, req)
	return j.receipt, nil
}
func rawCanonical(req request) []byte { raw, _ := json.Marshal(req); return raw }
func (a *RendererApp) lookup(app *sdk.AppCtx, args map[string]any) (*job, error) {
	id, _ := args["job_id"].(string)
	j := a.jobs[id]
	if j == nil || j.Project != project(app) {
		return nil, errors.New("job not found")
	}
	return j, nil
}
func (a *RendererApp) get(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	j, e := a.lookup(app, args)
	if e != nil {
		return nil, e
	}
	return j.receipt, nil
}
func (a *RendererApp) acknowledge(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	j, err := a.lookup(app, args)
	if err != nil {
		return nil, err
	}
	if !terminal(j.Status) || !j.Stopped {
		return nil, errors.New("attempt is not terminal")
	}
	j.Output = ""
	if err = a.save(j); err != nil {
		return nil, err
	}
	return map[string]any{"acknowledged": true}, nil
}
func (a *RendererApp) cancelJob(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	j, e := a.lookup(app, args)
	if e != nil {
		a.mu.Unlock()
		return nil, e
	}
	if j.cancel != nil && !terminal(j.Status) {
		j.cancel()
	}
	done := j.done
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !j.Stopped {
		return nil, errors.New("renderer cancellation unconfirmed")
	}
	return j.receipt, nil
}
func (a *RendererApp) admit(ctx context.Context, req request) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Control {
		select {
		case a.control <- struct{}{}:
			return func() { <-a.control }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	waiter := &struct{ marker byte }{}
	a.mu.Lock()
	a.waiters = append(a.waiters, waiter)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, w := range a.waiters {
			if w == waiter {
				a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
				a.signal()
				break
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.waiters[0] == waiter && a.used+req.Weight <= a.capacity {
			a.waiters = a.waiters[1:]
			a.signal()
			a.used += req.Weight
			a.mu.Unlock()
			return func() { a.mu.Lock(); a.used -= req.Weight; a.signal(); a.mu.Unlock() }, nil
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// boundedOutput keeps the output tail and reports APTEVA_PROGRESS:<fraction>
// lines so a polling parent can show render progress before the job ends.
type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	line     []byte
	overflow bool
	progress func(float64)
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > outputLimit {
		b.overflow = true
		b.data = append([]byte{}, b.data[len(b.data)-outputLimit:]...)
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(b.line) < 256 {
				b.line = append(b.line, p...)
			}
			break
		}
		line := append(b.line, p[:i]...)
		b.line = b.line[:0]
		p = p[i+1:]
		if at := bytes.Index(line, []byte(progressMarker)); at >= 0 && b.progress != nil {
			if v, err := strconv.ParseFloat(strings.TrimSpace(string(line[at+len(progressMarker):])), 64); err == nil && v >= 0 && v <= 1 {
				b.progress(v)
			}
		}
	}
	return n, nil
}
func (a *RendererApp) run(ctx context.Context, j *job, req request) {
	defer func() { j.cancel(); <-a.slots; close(j.done) }()
	release, err := a.admit(ctx, req)
	if err == nil {
		defer release()
	}
	output := boundedOutput{progress: func(v float64) { a.mu.Lock(); j.Progress = v; a.mu.Unlock() }}
	exit := -1
	stopped := true
	failure := "render failed"
	var ffmpeg, ffprobe string
	if err == nil {
		if ffmpeg, ffprobe, err = a.ffmpeg.ensure(ctx, a.root); err != nil {
			failure = "FFmpeg unavailable: " + err.Error()
		}
	}
	var browser browserPaths
	if err == nil && len(req.Needs) > 0 {
		// Waits for the attempt mount started, or retries a failed one.
		if browser, err = a.browser.ensure(ctx, a.root); err != nil {
			failure = "browser dependencies unavailable: " + err.Error()
		}
	}
	if err == nil {
		command := strings.TrimPrefix(req.Command, "setsid ")
		if strings.HasPrefix(command, "bash -c ") {
			command = "exec " + command
		}
		cmd := exec.Command("bash", "-c", command)
		cmd.Dir = a.root
		cmd.Env = workerEnv(a.root, ffmpeg, ffprobe, browser)
		cmd.Stdout = &output
		cmd.Stderr = &output
		configureProcess(cmd)
		a.mu.Lock()
		j.Status = "starting"
		err = a.save(j)
		a.mu.Unlock()
		if err == nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = cmd.Start()
			}
		}
		if err == nil {
			a.mu.Lock()
			j.PID = cmd.Process.Pid
			j.ProcessStart = processStart(j.PID)
			j.Status = "running"
			saveErr := a.save(j)
			a.mu.Unlock()
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			if saveErr != nil {
				err = saveErr
				_ = stopRecorded(j.PID, j.ProcessStart)
				<-waited
			} else {
				select {
				case err = <-waited:
				case <-ctx.Done():
					err = ctx.Err()
					stopErr := stopRecorded(j.PID, j.ProcessStart)
					<-waited
					if stopErr != nil {
						err = stopErr
						stopped = false
					}
				}
			}
			// Also kill descendants (Chrome, FFmpeg) left behind after the main shell exits.
			if stopErr := stopRecorded(j.PID, j.ProcessStart); stopErr != nil {
				err = stopErr
				stopped = false
			}
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	j.Output = string(output.data)
	j.Exit = exit
	j.Stopped = stopped
	j.Status = "succeeded"
	if err != nil {
		j.Status = "failed"
		j.Error = failure
	}
	if ctx.Err() != nil {
		j.Status = "cancelled"
		j.Error = ctx.Err().Error()
	}
	if !stopped {
		j.Status = "failed"
		j.Error = "process termination unconfirmed"
	}
	if output.overflow {
		j.Status = "failed"
		j.Error = "renderer output limit exceeded"
	}
	if j.Status == "succeeded" {
		j.Progress = 1
	}
	if e := a.save(j); e != nil {
		j.Status = "failed"
		j.Error = "renderer journal write failed"
	}
}
func workerEnv(root, ffmpeg, ffprobe string, browser browserPaths) []string {
	// Do not expose the worker installation token or gateway credentials to plans.
	path := os.Getenv("PATH")
	if ffmpeg != "" {
		path = filepath.Dir(ffmpeg) + ":" + path
	}
	env := []string{"HOME=" + root, "TMPDIR=" + root, "PATH=" + path, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "FFMPEG=" + ffmpeg, "FFPROBE=" + ffprobe}
	ownFonts := browser.Chrome != "" && browser.FontConfig != ""
	if browser.Chrome != "" {
		env = append(env, "CHROME_BIN="+browser.Chrome, "NODE_BIN="+browser.Node, "CHROME_EXTRA_ARGS="+strings.Join(browser.ExtraArgs, " "))
	}
	if ownFonts {
		// Browser captures use the renderer's own fonts, not the host's config.
		env = append(env, "FONTCONFIG_FILE="+browser.FontConfig)
	}
	for _, key := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "SYSTEMROOT", "FONTCONFIG_FILE", "FONTCONFIG_PATH"} {
		if ownFonts && strings.HasPrefix(key, "FONTCONFIG_") {
			continue
		}
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}
func (a *RendererApp) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "GET", Pattern: "/_apteva/runtime/app/status", Handler: a.status},
		{Method: "GET", Pattern: "/_apteva/runtime/app/describe", Handler: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"passive_start": true, "drain": true, "replica_safe": false, "worker_coordination": false, "routing": "key"})
		}},
		{Method: "POST", Pattern: "/_apteva/runtime/app/activate", Handler: func(w http.ResponseWriter, r *http.Request) {
			if !a.lifecycleIdentity(w, r) {
				return
			}
			a.mu.Lock()
			if a.lifecycleState == "draining" {
				a.mu.Unlock()
				http.Error(w, "renderer is draining", http.StatusConflict)
				return
			}
			a.active = true
			a.lifecycleState = "active"
			a.mu.Unlock()
			writeJSON(w, map[string]any{"active": true})
		}},
		{Method: "POST", Pattern: "/_apteva/runtime/app/drain", Handler: a.drain},
	}
}
func (a *RendererApp) lifecycleIdentity(w http.ResponseWriter, r *http.Request) bool {
	if a.deploymentID == "" {
		return true // Standalone SDK development; no managed identity to claim.
	}
	var identity struct {
		DeploymentID string `json:"deployment_id"`
		Generation   int64  `json:"generation"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if decoder.Decode(&identity) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "deployment identity required", http.StatusBadRequest)
		return false
	}
	if identity.DeploymentID != a.deploymentID || identity.Generation != a.generation {
		http.Error(w, "deployment identity mismatch", http.StatusConflict)
		return false
	}
	return true
}
func (a *RendererApp) status(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, map[string]any{"deployment_id": a.deploymentID, "generation": a.generation,
		"state": a.lifecycleState, "admitting": a.active, "load": float64(a.used) / float64(a.capacity)})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *RendererApp) drain(w http.ResponseWriter, r *http.Request) {
	if !a.lifecycleIdentity(w, r) {
		return
	}
	a.mu.Lock()
	a.active = false
	a.lifecycleState = "draining"
	running := []chan struct{}{}
	for _, j := range a.jobs {
		if !terminal(j.Status) {
			running = append(running, j.done)
		}
	}
	a.mu.Unlock()
	for _, done := range running {
		select {
		case <-done:
		case <-r.Context().Done():
			http.Error(w, "drain incomplete", http.StatusConflict)
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range a.jobs {
		if !j.Stopped {
			http.Error(w, "termination unconfirmed", http.StatusConflict)
			return
		}
	}
	a.lifecycleState = "inactive"
	writeJSON(w, map[string]any{"drained": true})
}
func (a *RendererApp) OnUnmount(app *sdk.AppCtx) error {
	a.mu.Lock()
	a.active = false
	a.stop()
	jobs := []*job{}
	for _, j := range a.jobs {
		jobs = append(jobs, j)
	}
	a.mu.Unlock()
	for _, j := range jobs {
		<-j.done
		if !j.Stopped {
			return errors.New("worker shutdown unconfirmed")
		}
	}
	return nil
}

var _ io.Writer = (*boundedOutput)(nil)

func main() { sdk.Run(&RendererApp{}) }
