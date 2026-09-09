package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type InstanceSource struct {
	Method   string   `json:"method,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Identity string   `json:"identity,omitempty"` // hash of binding, instance id and creation time
}

func (s InstanceSource) Value() (driver.Value, error) { b, e := json.Marshal(s); return string(b), e }
func (s *InstanceSource) Scan(v any) error {
	switch v := v.(type) {
	case string:
		return json.Unmarshal([]byte(v), s)
	case []byte:
		return json.Unmarshal(v, s)
	default:
		return errors.New("invalid source configuration")
	}
}

type InstanceRestore struct {
	InstanceID int64
	Path       string
}
type instanceRecord struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	Capabilities struct {
		Run    bool `json:"run"`
		Upload bool `json:"upload"`
		Tunnel bool `json:"tunnel"`
	} `json:"capabilities"`
}

func validInstancePath(p string) bool {
	return len(p) > 1 && len(p) <= 4096 && strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n") && p != "/dev" && p != "/proc" && p != "/sys" && !strings.HasPrefix(p, "/dev/") && !strings.HasPrefix(p, "/proc/") && !strings.HasPrefix(p, "/sys/")
}
func validateInstanceScope(s Scope) error {
	id, e := strconv.ParseInt(s.ID, 10, 64)
	if e != nil || id <= 0 || strconv.FormatInt(id, 10) != s.ID || s.SourceApp != "instances" {
		return errors.New("instance scope requires a positive registered instance id and source_app=instances")
	}
	if s.Config.Method != "folders" {
		return errors.New("only the folders backup method is supported")
	}
	if len(s.Config.Paths) == 0 || len(s.Config.Paths) > 32 {
		return errors.New("choose between 1 and 32 source folders")
	}
	for i, p := range s.Config.Paths {
		if !validInstancePath(p) {
			return fmt.Errorf("invalid source folder %q", p)
		}
		for _, other := range s.Config.Paths[:i] {
			if p == other || strings.HasPrefix(p, other+"/") || strings.HasPrefix(other, p+"/") {
				return errors.New("source folders must not overlap")
			}
		}
	}
	return nil
}
func instancesBound(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.PlatformAPI() == nil || ctx.IntegrationFor("instances_provider") == nil {
		return errors.New("bind the optional Instances app to use instance folders")
	}
	return nil
}
func callInstances(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	if e := instancesBound(ctx); e != nil {
		return e
	}
	return ctx.PlatformAPI().CallAppResult("instances", tool, args, out)
}
func getBackupInstance(ctx *sdk.AppCtx, id int64) (instanceRecord, string, error) {
	var result struct {
		Instance instanceRecord `json:"instance"`
	}
	if id <= 0 {
		return result.Instance, "", errors.New("select a registered remote instance")
	}
	if e := callInstances(ctx, "instance_get", map[string]any{"id": id}, &result); e != nil {
		return result.Instance, "", e
	}
	inst := result.Instance
	if inst.ID != id || inst.Status != "ready" || inst.CreatedAt == "" || !inst.Capabilities.Run || !inst.Capabilities.Upload || !inst.Capabilities.Tunnel {
		return inst, "", errors.New("instance must be ready and support SSH commands and tunnels")
	}
	binding := ctx.IntegrationFor("instances_provider")
	identity := sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%s", binding.InstallID, id, inst.CreatedAt)))
	return inst, hex.EncodeToString(identity[:16]), nil
}
func instanceCommand(ctx *sdk.AppCtx, id int64, cmd string) (string, error) {
	var out struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if e := callInstances(ctx, "instance_run_command", map[string]any{"id": id, "cmd": cmd, "timeout_s": 30}, &out); e != nil {
		return "", e
	}
	if out.ExitCode != 0 || out.Error != "" {
		return "", errors.New("instance command failed; verify SSH access and host capabilities")
	}
	if len(out.Output) > 64<<10 {
		return "", errors.New("instance control response exceeds limit")
	}
	return out.Output, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func probeInstance(ctx *sdk.AppCtx, id int64, paths []string) (map[string]any, error) {
	helper, e := ensureInstanceHelper(ctx, id)
	if e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(paths)
	output, e := instanceCommand(ctx, id, shellQuote(helper)+" probe "+shellQuote(string(raw)))
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if e = json.Unmarshal([]byte(output), &result); e != nil {
		return nil, errors.New("invalid instance capability response")
	}
	if result["supported"] != true {
		return result, fmt.Errorf("unsupported folder backup: %v", result["reason"])
	}
	return result, nil
}
func resolveInstanceScope(ctx *sdk.AppCtx, s *Scope) error {
	if e := validateScope(*s); e != nil {
		return e
	}
	if s.Kind != "instance" {
		return nil
	}
	id, _ := strconv.ParseInt(s.ID, 10, 64)
	_, identity, e := getBackupInstance(ctx, id)
	if e != nil {
		return e
	}
	if s.Config.Identity != "" && s.Config.Identity != identity {
		return errors.New("instance identity changed; select the registered host again")
	}
	s.Config.Identity = identity
	return nil
}
func prepareScope(ctx *sdk.AppCtx, s *Scope) error {
	if e := resolveInstanceScope(ctx, s); e != nil {
		return e
	}
	if s.Kind != "instance" {
		return nil
	}
	id, _ := strconv.ParseInt(s.ID, 10, 64)
	_, e := probeInstance(ctx, id, s.Config.Paths)
	return e
}
func addInstanceScopes(ctx *sdk.AppCtx, out map[string]any) {
	out["instances_bound"] = false
	out["instances"] = []any{}
	if instancesBound(ctx) != nil {
		return
	}
	out["instances_bound"] = true
	var list struct {
		Instances []instanceRecord `json:"instances"`
	}
	if e := callInstances(ctx, "instance_list", map[string]any{}, &list); e != nil {
		out["instances_error"] = e.Error()
		return
	}
	result := []map[string]any{}
	for _, i := range list.Instances {
		if i.ID <= 0 {
			continue
		}
		eligible := i.Status == "ready" && i.Capabilities.Run && i.Capabilities.Upload && i.Capabilities.Tunnel
		reason := ""
		if !eligible {
			reason = "Requires ready SSH command and tunnel capabilities"
		}
		result = append(result, map[string]any{"id": i.ID, "name": i.Name, "eligible": eligible, "reason": reason})
	}
	out["instances"] = result
}
func (a *App) handleInstanceCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		httpErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		ID    int64    `json:"id"`
		Paths []string `json:"paths"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body) != nil {
		httpErr(w, 400, "invalid json")
		return
	}
	ctx := getAppCtx(r)
	if _, _, e := getBackupInstance(ctx, body.ID); e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	if len(body.Paths) > 0 {
		if e := validateInstanceScope(Scope{Kind: "instance", ID: strconv.FormatInt(body.ID, 10), SourceApp: "instances", Config: InstanceSource{Method: "folders", Paths: body.Paths}}); e != nil {
			httpErr(w, 400, e.Error())
			return
		}
	}
	out, e := probeInstance(ctx, body.ID, body.Paths)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, out)
}

type instanceOperation struct {
	ID, Kind, Token, Config, State string
	InstanceID                     int64
	TargetPort                     int
}

func randomOperationID() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func loadInstanceOperation(ctx *sdk.AppCtx, runID int64, kind string, instanceID int64, config map[string]any) (*instanceOperation, error) {
	raw, e := json.Marshal(config)
	if e != nil {
		return nil, e
	}
	op := &instanceOperation{ID: randomOperationID(), Kind: kind, Token: randomOperationID(), InstanceID: instanceID, Config: string(raw)}
	_, e = ctx.AppDB().Exec(`INSERT OR IGNORE INTO instance_operations(id,run_id,kind,instance_id,config,token) VALUES(?,?,?,?,?,?)`, op.ID, runID, kind, instanceID, op.Config, op.Token)
	if e != nil {
		return nil, e
	}
	e = ctx.AppDB().QueryRow(`SELECT id,instance_id,config,token,state FROM instance_operations WHERE run_id=? AND kind=?`, runID, kind).Scan(&op.ID, &op.InstanceID, &op.Config, &op.Token, &op.State)
	if e != nil {
		return nil, e
	}
	if op.InstanceID != instanceID || op.Config != string(raw) {
		return nil, errors.New("operation configuration changed during retry")
	}
	return op, nil
}

// Install the small Backup-owned worker via generic SSH execution. Control
// responses contain only endpoint/status metadata; archives use the tunnel.
func openInstanceOperation(ctx *sdk.AppCtx, op *instanceOperation) (string, error) {
	var cfg map[string]any
	if e := json.Unmarshal([]byte(op.Config), &cfg); e != nil {
		return "", e
	}
	cfg["operation_id"] = op.ID
	cfg["token"] = op.Token
	raw, _ := json.Marshal(cfg)
	helper, e := ensureInstanceHelper(ctx, op.InstanceID)
	if e != nil {
		return "", e
	}
	output, e := instanceCommand(ctx, op.InstanceID, shellQuote(helper)+" start "+shellQuote(base64.StdEncoding.EncodeToString(raw)))
	if e != nil {
		return "", e
	}
	var endpoint struct {
		Port int `json:"port"`
	}
	if json.Unmarshal([]byte(output), &endpoint) != nil || endpoint.Port < 1 || endpoint.Port > 65535 {
		return "", errors.New("invalid worker endpoint")
	}
	var tunnel struct {
		Host string `json:"local_host"`
		Port int    `json:"local_port"`
	}
	if e = callInstances(ctx, "instance_open_tunnel", map[string]any{"id": op.InstanceID, "target_port": endpoint.Port}, &tunnel); e != nil {
		return "", e
	}
	if tunnel.Host != "127.0.0.1" || tunnel.Port < 1 || tunnel.Port > 65535 {
		return "", errors.New("invalid Instances tunnel endpoint")
	}
	op.TargetPort = endpoint.Port
	if _, e = ctx.AppDB().Exec(`UPDATE instance_operations SET target_port=? WHERE id=?`, endpoint.Port, op.ID); e != nil {
		return "", e
	}
	return fmt.Sprintf("http://127.0.0.1:%d", tunnel.Port), nil
}
func instanceRequest(ctx context.Context, op *instanceOperation, method, url string, body io.Reader, size int64) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, method, url, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+op.Token)
	if body != nil {
		req.ContentLength = size
	}
	client := *platformTransferClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return nil, errors.New("instance transfer interrupted or tunnel unavailable; retry to reconnect")
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("instance transfer returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func waitInstanceOperation(opCtx context.Context, ctx *sdk.AppCtx, op *instanceOperation, base string, runID int64, wanted string) (map[string]any, error) {
	for {
		status, e := readInstanceStatus(opCtx, op, base)
		if e != nil {
			return nil, e
		}
		stage, _ := status["stage"].(string)
		_, e = ctx.AppDB().Exec(`UPDATE instance_operations SET state=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, stage, op.ID)
		if e != nil {
			return nil, e
		}
		display := "instance " + stage
		if files, ok := status["files"].(float64); ok {
			display += fmt.Sprintf(" (%d files)", int64(files))
		}
		if strings.HasPrefix(op.Kind, "restore-") {
			_, e = ctx.AppDB().Exec(`UPDATE runs SET stage=? WHERE id=? AND status='success'`, "instance restore "+stage, runID)
		} else {
			e = dbUpdateRunStage(ctx.AppDB(), runID, display)
		}
		if e != nil {
			return nil, e
		}
		if stage == wanted {
			return status, nil
		}
		if stage == "failed" {
			return status, fmt.Errorf("instance operation failed: %v", status["error"])
		}
		select {
		case <-opCtx.Done():
			return nil, opCtx.Err()
		case <-time.After(time.Second):
		}
	}
}
func streamInstanceSnapshot(opCtx context.Context, ctx *sdk.AppCtx, dst io.Writer, run *Run) (int64, error) {
	scope := run.Scope
	if e := resolveInstanceScope(ctx, &scope); e != nil {
		return 0, e
	}
	id, _ := strconv.ParseInt(scope.ID, 10, 64)
	op, e := loadInstanceOperation(ctx, run.ID, "backup", id, map[string]any{"kind": "backup", "instance_identity": scope.Config.Identity, "paths": scope.Config.Paths})
	if e != nil {
		return 0, e
	}
	base, e := openInstanceOperation(ctx, op)
	if e != nil {
		return 0, e
	}
	defer closeInstanceTunnel(ctx, op)
	status, e := waitInstanceOperation(opCtx, ctx, op, base, run.ID, "ready")
	if e != nil {
		return 0, e
	}
	resp, e := instanceRequest(opCtx, op, "GET", base+"/archive", nil, 0)
	if e != nil {
		return 0, e
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(dst, h), resp.Body)
	if e != nil {
		return n, e
	}
	if hex.EncodeToString(h.Sum(nil)) != status["sha256"] {
		return n, errors.New("instance archive integrity mismatch")
	}
	return n, nil
}
func cleanupInstanceOperation(ctx *sdk.AppCtx, runID int64, kind string) {
	var op instanceOperation
	if ctx.AppDB().QueryRow(`SELECT id,instance_id,config,token,state,target_port FROM instance_operations WHERE run_id=? AND kind=?`, runID, kind).Scan(&op.ID, &op.InstanceID, &op.Config, &op.Token, &op.State, &op.TargetPort) != nil {
		return
	}
	if op.TargetPort < 1 {
		return
	}
	var tunnel struct {
		Host string `json:"local_host"`
		Port int    `json:"local_port"`
	}
	if e := callInstances(ctx, "instance_open_tunnel", map[string]any{"id": op.InstanceID, "target_port": op.TargetPort}, &tunnel); e != nil || tunnel.Host != "127.0.0.1" || tunnel.Port <= 0 || tunnel.Port > 65535 {
		return
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", tunnel.Port)
	defer closeInstanceTunnel(ctx, &op)
	short, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if resp, e := instanceRequest(short, &op, "POST", base+"/cleanup", nil, 0); e == nil {
		resp.Body.Close()
	}
}
func restoreInstanceRun(opCtx context.Context, ctx *sdk.AppCtx, run *Run, body io.Reader, options InstanceRestore) (map[string]any, error) {
	if run.SHA256 == "" {
		return nil, errors.New("instance recovery point has no integrity digest")
	}
	if !validInstancePath(options.Path) {
		return nil, errors.New("instance restore requires target_path: a new absolute folder whose parent exists")
	}
	target := options.InstanceID
	if target == 0 {
		target, _ = strconv.ParseInt(run.Scope.ID, 10, 64)
	}
	_, identity, e := getBackupInstance(ctx, target)
	if e != nil {
		return nil, e
	}
	if options.InstanceID == 0 && identity != run.Scope.Config.Identity {
		return nil, errors.New("original instance identity changed; explicitly select a compatible replacement")
	}
	if _, e = probeInstance(ctx, target, nil); e != nil {
		return nil, e
	}
	file, e := os.CreateTemp("", "apteva-instance-restore-*")
	if e != nil {
		return nil, e
	}
	defer os.Remove(file.Name())
	defer file.Close()
	h := sha256.New()
	size, e := io.Copy(io.MultiWriter(file, h), contextReader{ctx: opCtx, r: body})
	if e != nil {
		return nil, e
	}
	if _, e = validateSnapshotArchive(file.Name()); e != nil {
		return nil, e
	}
	digest := hex.EncodeToString(h.Sum(nil))
	key := sha256.Sum256([]byte(identity + "/" + options.Path))
	kind := "restore-" + hex.EncodeToString(key[:16])
	cfg := map[string]any{"kind": "restore", "instance_identity": run.Scope.Config.Identity, "target_identity": identity, "target_path": options.Path, "sha256": digest, "archive_bytes": size}
	op, e := loadInstanceOperation(ctx, run.ID, kind, target, cfg)
	if e != nil {
		return nil, e
	}
	if op.State == "restored" {
		return map[string]any{"restored": true, "target_path": options.Path, "target_instance_id": target, "already_completed": true}, nil
	}
	base, e := openInstanceOperation(ctx, op)
	if e != nil {
		return nil, e
	}
	defer closeInstanceTunnel(ctx, op)
	initial, e := readInstanceStatus(opCtx, op, base)
	if e != nil {
		return nil, e
	}
	if initial["stage"] != "restoring" && initial["stage"] != "restored" && initial["stage"] != "uploaded" {
		if _, e = file.Seek(0, io.SeekStart); e != nil {
			return nil, e
		}
		resp, e := instanceRequest(opCtx, op, "POST", base+"/restore", file, size)
		if e != nil {
			return nil, e
		}
		resp.Body.Close()
	}
	status, e := waitInstanceOperation(opCtx, ctx, op, base, run.ID, "restored")
	if e != nil {
		return status, e
	}
	status["restored"] = true
	status["target_instance_id"] = target
	cleanupInstanceOperation(ctx, run.ID, kind)
	return status, nil
}

func closeInstanceTunnel(ctx *sdk.AppCtx, op *instanceOperation) {
	if op.TargetPort > 0 {
		var out map[string]any
		_ = callInstances(ctx, "instance_close_tunnel", map[string]any{"id": op.InstanceID, "target_port": op.TargetPort}, &out)
	}
}

func retryInstanceRun(ctx *sdk.AppCtx, id int64) (*Run, error) {
	release, e := acquireOperation("retry")
	if e != nil {
		return nil, e
	}
	defer release()
	run, e := dbGetRun(ctx.AppDB(), id)
	if e != nil {
		return nil, e
	}
	if run.Scope.Kind != "instance" || run.Status != "failed" {
		return nil, errors.New("only failed instance backups can be retried")
	}
	if e = resolveInstanceScope(ctx, &run.Scope); e != nil {
		return nil, e
	}
	dest, e := dbGetDestination(ctx.AppDB(), run.DestinationID)
	if e != nil || !dest.Enabled {
		return nil, errors.New("backup destination is unavailable")
	}
	policy := &Policy{}
	if run.PolicyID != 0 {
		policy, e = dbGetPolicy(ctx.AppDB(), run.PolicyID)
		if e != nil {
			return nil, e
		}
	}
	p, _ := json.Marshal(policy)
	d, _ := json.Marshal(dest)
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT OR REPLACE INTO backup_queue(run_id,policy_json,destination_json) VALUES(?,?,?)`, id, string(p), string(d)); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(`UPDATE runs SET status='queued',stage='queued for retry',finished_at=NULL,error='' WHERE id=?`, id); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return dbGetRun(ctx.AppDB(), id)
}
func (a *App) handleInstanceRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		httpErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		RunID int64 `json:"run_id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		httpErr(w, 400, "invalid json")
		return
	}
	run, e := retryInstanceRun(getAppCtx(r), body.RunID)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, map[string]any{"run": run})
}

func readInstanceStatus(ctx context.Context, op *instanceOperation, base string) (map[string]any, error) {
	resp, e := instanceRequest(ctx, op, "GET", base+"/status", nil, 0)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	var status map[string]any
	if e != nil || len(raw) > 65536 || json.Unmarshal(raw, &status) != nil {
		return nil, errors.New("invalid worker status")
	}
	return status, nil
}
