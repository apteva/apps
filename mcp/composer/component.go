package main

// Composer offloads renders to its private "renderer" owned component. Server
// places that worker (locally or on an execution backend such as Instances);
// Composer only submits bounded plans, polls receipts and records them
// durably. It holds no infrastructure dependency and never sends its own
// platform token: plans carry a job-scoped transfer grant for the relay in
// component_transfer.go.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const rendererComponent = "renderer"
const rendererProtocol = "apteva.composer-renderer/v1"
const rendererOutputFolder = "/.composer/"

// Half of the grant budget is reserved for declared output sizes (4 GiB).
const rendererTransferMaxBytes = int64(8) << 30

var errRendererUnresolved = errors.New("renderer outcome unresolved")

type componentOwnerKey struct{}

// withComponentOwner names the durable owner of component calls made under
// ctx (e.g. "render:12"). A resumed render re-attaches to its in-flight or
// finished worker job instead of starting a second execution.
func withComponentOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, componentOwnerKey{}, owner)
}

func componentOwner(ctx context.Context, stage string) string {
	owner, _ := ctx.Value(componentOwnerKey{}).(string)
	if owner == "" {
		return ""
	}
	return owner + ":" + stage
}

// componentRenderingEnabled reports whether renders go to the renderer
// component. Installs that configured the removed Instances host keep
// offloading: previously offloaded rendering must never silently consume
// local CPU. With no renderer deployed those renders fail explicitly.
func componentRenderingEnabled(app *sdk.AppCtx) bool {
	if app == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(app.Config().Get("execution_mode"))) {
	case "component":
		return true
	case "local":
		return false
	}
	legacy := strings.TrimSpace(app.Config().Get("render_host_id"))
	if legacy == "" {
		legacy = strings.TrimSpace(os.Getenv("RENDER_HOST_ID"))
	}
	n, _ := strconv.ParseInt(legacy, 10, 64)
	return n > 0
}

type rendererPlan struct {
	JobID   string   `json:"job_id"`
	Command string   `json:"command"`
	Timeout int      `json:"timeout_s"`
	Weight  int      `json:"weight"`
	Control bool     `json:"control"`
	Needs   []string `json:"needs,omitempty"`
}

type rendererReceipt struct {
	JobID    string  `json:"job_id"`
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	Output   string  `json:"output"`
	Exit     int     `json:"exit_code"`
	Error    string  `json:"error"`
	Stopped  bool    `json:"stopped_confirmed"`
}

type rendererCapabilities struct {
	Protocol   string         `json:"protocol"`
	GatewayURL string         `json:"gateway_url"`
	Platform   string         `json:"platform"`
	FFmpeg     string         `json:"ffmpeg"`
	Capacity   int            `json:"capacity"`
	Browser    map[string]any `json:"browser"`
}

// rendererEnv is what a plan builder needs to address the platform from the
// worker: its gateway, the job-scoped Storage relay and the grant for it.
type rendererEnv struct {
	Base       string
	Relay      string
	Grant      string
	ProjectID  string
	publicURLs []string
}

// source rebases platform-minted URLs (signed Storage links) onto the gateway
// the worker actually reaches. External URLs pass through unchanged.
func (e rendererEnv) source(u string) string {
	for _, prefix := range e.publicURLs {
		if prefix != "" && prefix != e.Base && strings.HasPrefix(u, prefix+"/") {
			return e.Base + strings.TrimPrefix(u, prefix)
		}
	}
	return u
}

type rendererJob struct {
	Stage   string
	Timeout int
	Weight  int
	Needs   []string
	Build   func(rendererEnv) (string, error)
}

func rendererCall(ctx context.Context, app *sdk.AppCtx, tool string, input map[string]any, out any) error {
	return app.Components().Call(ctx, rendererComponent, tool, input, out)
}

func rendererCaps(ctx context.Context, app *sdk.AppCtx) (rendererCapabilities, error) {
	var caps rendererCapabilities
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := rendererCall(callCtx, app, "capabilities", map[string]any{}, &caps); err != nil {
		return caps, fmt.Errorf("renderer component unavailable (deploy it with POST /api/apps/installs/<composer install>/components/renderer, or set execution_mode=local): %w", err)
	}
	if caps.Protocol != rendererProtocol {
		return caps, fmt.Errorf("renderer component speaks %q, want %q", caps.Protocol, rendererProtocol)
	}
	caps.GatewayURL = strings.TrimRight(caps.GatewayURL, "/")
	if !strings.HasPrefix(caps.GatewayURL, "https://") && !strings.HasPrefix(caps.GatewayURL, "http://") {
		return caps, errors.New("renderer component reported no gateway URL")
	}
	return caps, nil
}

func randomComponentID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// runRendererJob submits one plan and waits for its terminal receipt. The plan
// and its job ID are persisted before submission; retries and resumes reuse
// them, so an ambiguous response never mints a second execution.
func runRendererJob(ctx context.Context, app *sdk.AppCtx, projectID string, job rendererJob) (rendererReceipt, error) {
	if err := ctx.Err(); err != nil {
		return rendererReceipt{}, err
	}
	app = app.WithProject(projectID)
	if job.Timeout < 30 {
		job.Timeout = 30
	}
	if job.Timeout > 14400 {
		job.Timeout = 14400
	}
	if job.Weight < 1 {
		job.Weight = 1
	}
	ctx, stopCall := context.WithTimeout(ctx, time.Duration(job.Timeout+30)*time.Second)
	defer stopCall()
	owner := componentOwner(ctx, job.Stage)
	var plan rendererPlan
	if owner != "" {
		var raw string
		var saved sql.NullString
		err := app.AppDB().QueryRow(`SELECT request_json,receipt_json FROM renderer_calls WHERE project_id=? AND owner_key=?`, projectID, owner).Scan(&raw, &saved)
		if err == nil {
			if saved.Valid {
				var r rendererReceipt
				if json.Unmarshal([]byte(saved.String), &r) != nil {
					return r, errors.New("invalid persisted renderer receipt")
				}
				return r, rendererResultErr(r)
			}
			if json.Unmarshal([]byte(raw), &plan) != nil {
				return rendererReceipt{}, errors.New("invalid persisted renderer request")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return rendererReceipt{}, err
		}
	}
	if plan.JobID == "" {
		caps, err := rendererCaps(ctx, app)
		if err != nil {
			return rendererReceipt{}, err
		}
		grant, grantHash, err := newRendererGrant(app, projectID, job.Timeout)
		if err != nil {
			return rendererReceipt{}, err
		}
		installID := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
		if installID == "" {
			return rendererReceipt{}, errors.New("renderer transfers require APTEVA_INSTALL_ID")
		}
		env := rendererEnv{
			Base:      caps.GatewayURL,
			Relay:     caps.GatewayURL + "/api/apps/composer/_install/" + installID + "/renderer-io" + composerBoundStorageProxyPath,
			Grant:     grant,
			ProjectID: projectID,
		}
		if public, err := resolveComposerPublicURL(app); err == nil {
			env.publicURLs = append(env.publicURLs, public)
		}
		env.publicURLs = append(env.publicURLs, strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"))
		command, err := job.Build(env)
		if err != nil {
			return rendererReceipt{}, err
		}
		plan = rendererPlan{JobID: randomComponentID(), Command: command, Timeout: job.Timeout, Weight: job.Weight, Needs: job.Needs}
		raw, _ := json.Marshal(plan)
		var ownerValue any
		if owner != "" {
			ownerValue = owner
		}
		if _, err := app.AppDB().Exec(`INSERT INTO renderer_calls(job_id,project_id,owner_key,request_json,grant_hash,created_at) VALUES(?,?,?,?,?,?)`,
			plan.JobID, projectID, ownerValue, string(raw), grantHash, time.Now().Unix()); err != nil {
			return rendererReceipt{}, err
		}
	}
	raw, _ := json.Marshal(plan)
	input := map[string]any{}
	_ = json.Unmarshal(raw, &input)
	var r rendererReceipt
	if err := rendererCall(ctx, app, "submit", input, &r); err != nil {
		// A response may be lost after acceptance. Inspect the exact job before
		// surfacing an unresolved outcome; never create another attempt.
		if inspectErr := rendererCall(ctx, app, "get", map[string]any{"job_id": plan.JobID}, &r); inspectErr != nil {
			return r, fmt.Errorf("%w: renderer submission failed (job_id=%s): %v", errRendererUnresolved, plan.JobID, err)
		}
	}
	if r.JobID != plan.JobID {
		return r, errors.New("renderer receipt identity mismatch")
	}
	lastProgress := -1.0
	for !rendererTerminal(r.Status) {
		if r.Progress > lastProgress {
			lastProgress = r.Progress
			reportRenderProgress(ctx, RenderProgress{Fraction: r.Progress})
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := rendererCall(stopCtx, app, "cancel", map[string]any{"job_id": plan.JobID}, &r); err != nil || r.JobID != plan.JobID || !r.Stopped {
				return r, fmt.Errorf("%w: renderer cancellation unconfirmed (job_id=%s)", errRendererUnresolved, plan.JobID)
			}
			if err := persistRendererReceipt(app, plan.JobID, r); err != nil {
				return r, err
			}
			return r, ctx.Err()
		case <-timer.C:
		}
		var next rendererReceipt
		if err := rendererCall(ctx, app, "get", map[string]any{"job_id": plan.JobID}, &next); err != nil {
			continue // Polling is read-only and remains bounded by the job deadline.
		}
		if next.JobID != plan.JobID {
			return r, errors.New("renderer receipt identity mismatch")
		}
		r = next
	}
	if err := persistRendererReceipt(app, plan.JobID, r); err != nil {
		return r, err
	}
	return r, rendererResultErr(r)
}

func rendererTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func rendererResultErr(r rendererReceipt) error {
	if !r.Stopped {
		return fmt.Errorf("%w: renderer job_id=%s", errRendererUnresolved, r.JobID)
	}
	if r.Status != "succeeded" {
		detail := strings.TrimSpace(redactSecrets(r.Output))
		if detail == "" {
			return fmt.Errorf("renderer job %s %s: %s", r.JobID, r.Status, r.Error)
		}
		return fmt.Errorf("renderer job %s %s: %s\nrenderer output (last 4KB):\n%s", r.JobID, r.Status, r.Error, truncTail(detail, 4096))
	}
	return nil
}

// persistRendererReceipt commits the receipt, revokes the job's transfer grant
// and only then acknowledges, so the worker may compact its copy.
func persistRendererReceipt(app *sdk.AppCtx, id string, r rendererReceipt) error {
	raw, _ := json.Marshal(r)
	tx, err := app.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM renderer_uploads WHERE token_hash=(SELECT grant_hash FROM renderer_calls WHERE job_id=?)`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM renderer_grants WHERE token_hash=(SELECT grant_hash FROM renderer_calls WHERE job_id=?)`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE renderer_calls SET receipt_json=?,request_json='{}' WHERE job_id=?`, string(raw), id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A failed ack retains the full worker receipt and is harmless to correctness.
	var ack map[string]any
	_ = rendererCall(ackCtx, app, "acknowledge", map[string]any{"job_id": id}, &ack)
	return nil
}

func newRendererGrant(app *sdk.AppCtx, projectID string, timeout int) (string, string, error) {
	token := randomComponentID()
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	folders, _ := json.Marshal([]string{rendererOutputFolder})
	if _, err := app.AppDB().Exec(`INSERT INTO renderer_grants(token_hash,project_id,expires_at,max_bytes,folders) VALUES(?,?,?,?,?)`,
		hash, projectID, time.Now().Add(time.Duration(timeout+120)*time.Second).Unix(), rendererTransferMaxBytes, string(folders)); err != nil {
		return "", "", err
	}
	return token, hash, nil
}

// rendererScript assembles a bash plan executed in a fresh directory on the
// worker. Files are embedded base64 so arbitrary content needs no quoting.
type rendererScript struct{ b strings.Builder }

func newRendererScript() *rendererScript {
	s := &rendererScript{}
	s.b.WriteString("set -eu -o pipefail\n")
	s.b.WriteString("WORKDIR=$(mktemp -d)\n")
	s.b.WriteString("trap 'rm -rf \"$WORKDIR\"' EXIT\n")
	s.b.WriteString("cd \"$WORKDIR\"\n")
	s.b.WriteString("WORKURL=$(printf '%s' \"$WORKDIR\" | sed 's/ /%20/g')\n")
	return s
}

// The delimiter cannot occur in base64 output (no underscores).
const rendererHeredoc = "APTEVA_RENDERER_FILE"

func (s *rendererScript) file(name string, data []byte) {
	fmt.Fprintf(&s.b, "mkdir -p \"$(dirname %s)\"\nbase64 -d > %s <<'%s'\n", shellQuote(name), shellQuote(name), rendererHeredoc)
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		s.b.WriteString(enc[:76])
		s.b.WriteByte('\n')
		enc = enc[76:]
	}
	s.b.WriteString(enc)
	s.b.WriteString("\n" + rendererHeredoc + "\n")
}

// workdirFile is like file but substitutes rendererWorkdirToken with the job
// directory's file URL path, so documents can reference absolute file:// URLs.
func (s *rendererScript) workdirFile(name string, data []byte) {
	s.file(name, data)
	fmt.Fprintf(&s.b, "sed -i.orig \"s#%s#${WORKURL}#g\" %s && rm -f %s\n", rendererWorkdirToken, shellQuote(name), shellQuote(name+".orig"))
}

const rendererWorkdirToken = "__APTEVA_RENDERER_WORKDIR__"

func (s *rendererScript) fetch(name, url string) {
	fmt.Fprintf(&s.b, "mkdir -p \"$(dirname %s)\"\ncurl -fsSL --retry 3 --retry-delay 1 --retry-connrefused -o %s %s\n", shellQuote(name), shellQuote(name), shellQuote(url))
}

func (s *rendererScript) run(line string) { s.b.WriteString(line); s.b.WriteByte('\n') }

func (s *rendererScript) progress(f float64) {
	fmt.Fprintf(&s.b, "echo %s%.3f\n", "APTEVA_PROGRESS:", f)
}

// upload sends out to Storage through the job-scoped relay and prints the
// APTEVA_RESULT marker parseRendererResult reads.
func (s *rendererScript) upload(env rendererEnv, out, filename, contentType string) {
	q := shellQuote(out)
	fmt.Fprintf(&s.b, "BYTES=$(stat -c %%s %s 2>/dev/null || stat -f %%z %s)\n", q, q)
	fmt.Fprintf(&s.b, "if command -v sha256sum >/dev/null 2>&1; then SHA=$(sha256sum %s | awk '{print $1}'); else SHA=$(shasum -a 256 %s | awk '{print $1}'); fi\n", q, q)
	fmt.Fprintf(&s.b, "DURATION=$(\"${FFPROBE:-ffprobe}\" -v error -show_entries format=duration -of csv=p=0 %s 2>/dev/null | head -1 || true)\n", q)
	// Public gateways can reject arbitrary binary request bodies before they
	// reach the relay; only the relay decodes this encoding.
	s.b.WriteString("export APTEVA_TRANSFER_ENCODING=base64\n")
	fmt.Fprintf(&s.b, "export STORAGE_TOKEN=%s\n", shellQuote(env.Grant))
	fmt.Fprintf(&s.b, "export STORAGE_BASE=%s\n", shellQuote(env.Relay))
	fmt.Fprintf(&s.b, "export PROJECT_ID=%s\n", shellQuote(env.ProjectID))
	fmt.Fprintf(&s.b, "export FOLDER=%s\n", shellQuote(rendererOutputFolder))
	fmt.Fprintf(&s.b, "export NAME=%s\n", shellQuote(shellFormValue(filename)))
	fmt.Fprintf(&s.b, "export CT=%s\n", shellQuote(shellFormValue(contentType)))
	fmt.Fprintf(&s.b, "export OUT=%s\n", q)
	s.b.WriteString(remoteStorageUploadScriptFragment)
	s.b.WriteString(`echo "APTEVA_RESULT:{\"storage_id\":${STORAGE_ID},\"bytes\":${BYTES},\"sha256\":\"${SHA}\",\"duration\":\"${DURATION}\"}"` + "\n")
}

func (s *rendererScript) String() string { return s.b.String() }

type rendererResult struct {
	StorageID int64   `json:"storage_id"`
	Bytes     int64   `json:"bytes"`
	SHA256    string  `json:"sha256"`
	Duration  float64 `json:"-"`
}

// parseRendererResult pulls the JSON object after the APTEVA_RESULT: marker.
func parseRendererResult(s string) (rendererResult, error) {
	var got rendererResult
	idx := strings.LastIndex(s, "APTEVA_RESULT:")
	if idx < 0 {
		return got, errors.New("APTEVA_RESULT marker missing")
	}
	tail := s[idx+len("APTEVA_RESULT:"):]
	if end := strings.Index(tail, "\n"); end > 0 {
		tail = tail[:end]
	}
	var raw struct {
		rendererResult
		Duration string `json:"duration"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(tail)), &raw); err != nil {
		return got, err
	}
	got = raw.rendererResult
	got.Duration, _ = strconv.ParseFloat(strings.TrimSpace(raw.Duration), 64)
	if got.StorageID <= 0 {
		return got, errors.New("renderer result did not include storage_id")
	}
	return got, nil
}

// componentStorageResult reports the Storage file a component render uploaded
// (Result.LocalPath "storage://files/<id>").
func componentStorageResult(localPath string) (int64, bool) {
	if !strings.HasPrefix(localPath, "storage://files/") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(localPath, "storage://files/"), 10, 64)
	return id, err == nil && id > 0
}

// rendererStatus summarizes the renderer for the panel and composer_status.
func rendererStatus(ctx context.Context, app *sdk.AppCtx) map[string]any {
	out := map[string]any{"execution_mode": "local", "deployed": false}
	if componentRenderingEnabled(app) {
		out["execution_mode"] = "component"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	caps, err := rendererCaps(ctx, app)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["deployed"] = true
	out["platform"] = caps.Platform
	out["gateway_url"] = caps.GatewayURL
	out["capacity"] = caps.Capacity
	out["ffmpeg"] = caps.FFmpeg != ""
	out["browser"] = caps.Browser
	return out
}
