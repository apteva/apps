package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type nativeTransport interface {
	Probe(context.Context, *Instance, string) (nativeProbe, error)
	Prepare(context.Context, *Instance, nativeRecord, string) error
	Start(context.Context, *Instance, nativeRecord, map[string]string) error
	Inspect(context.Context, *Instance, nativeRecord) (nativeObservation, error)
	Stop(context.Context, *Instance, nativeRecord) error
	Delete(context.Context, *Instance, nativeRecord) error
	Logs(context.Context, *Instance, nativeRecord, string, int) ([]string, string, error)
}
type nativeBackend struct {
	db           *sql.DB
	root         string
	installID    int64
	transport    nativeTransport
	tunnels      *tunnelRegistry
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	locks        map[string]*sync.Mutex
	jobs         map[string]context.CancelFunc
	artifactMu   sync.Mutex
	allocationMu sync.Mutex
}

var nativeManagersMu sync.Mutex
var nativeManagers = map[*sql.DB]*nativeBackend{}

func nativeFor(ctx *sdk.AppCtx) (*nativeBackend, error) {
	nativeManagersMu.Lock()
	defer nativeManagersMu.Unlock()
	if b := nativeManagers[ctx.AppDB()]; b != nil {
		return b, nil
	}
	id, e := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	if e != nil || id <= 0 || !filepath.IsAbs(ctx.DataDir()) {
		return nil, errors.New("native backend requires installation identity and absolute data directory")
	}
	root := filepath.Join(ctx.DataDir(), "native-runtime")
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	life, cancel := context.WithCancel(context.Background())
	b := &nativeBackend{db: ctx.AppDB(), root: root, installID: id, transport: nativeSSH{}, tunnels: newTunnelRegistry(dialInstanceLoopback), ctx: life, cancel: cancel, locks: map[string]*sync.Mutex{}, jobs: map[string]context.CancelFunc{}}
	nativeManagers[ctx.AppDB()] = b
	return b, nil
}
func startNativeRuntime(ctx *sdk.AppCtx) error {
	b, e := nativeFor(ctx)
	if e != nil {
		return e
	}
	go func() {
		select {
		case <-ctx.Done():
			b.cancel()
			b.tunnels.closeAll()
		case <-b.ctx.Done():
		}
	}()
	// Reconnect retained active executions; never start a service during recovery.
	go func() {
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for {
			rows, e := b.db.Query(`SELECT record FROM native_deployments WHERE json_extract(record,'$.started')=1 AND json_extract(record,'$.execution.state') NOT IN ('stopped','deleted')`)
			var records []nativeRecord
			if e == nil {
				for rows.Next() {
					var raw string
					var d nativeRecord
					if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &d) == nil && d.Started && d.Execution.State != "stopped" && d.Execution.State != "deleted" {
						records = append(records, d)
					}
				}
				rows.Close()
			}
			for _, d := range records {
				if b.ctx.Err() != nil {
					return
				}
				if inst, e := dbGetInstance(b.db, d.InstanceID); e == nil {
					_ = b.ensureTunnel(&d, inst)
				}
			}
			select {
			case <-b.ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	return nil
}
func stopNativeRuntime(ctx *sdk.AppCtx) {
	nativeManagersMu.Lock()
	b := nativeManagers[ctx.AppDB()]
	delete(nativeManagers, ctx.AppDB())
	nativeManagersMu.Unlock()
	if b != nil {
		b.cancel()
		b.mu.Lock()
		for _, cancel := range b.jobs {
			cancel()
		}
		b.mu.Unlock()
		b.tunnels.closeAll()
	}
}
func (b *nativeBackend) lock(id string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.locks[id] == nil {
		b.locks[id] = &sync.Mutex{}
	}
	return b.locks[id]
}
func (b *nativeBackend) record(id string) (nativeRecord, error) {
	var d nativeRecord
	var raw string
	e := b.db.QueryRow(`SELECT record FROM native_deployments WHERE id=?`, id).Scan(&raw)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal([]byte(raw), &d)
	return d, e
}
func (b *nativeBackend) saveCAS(before string, d nativeRecord) error {
	result, e := b.db.Exec(`UPDATE native_deployments SET record=? WHERE id=? AND record=?`, nativeJSON(d), d.ID, before)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("deployment changed concurrently; inspect before retry")
	}
	return nil
}
func (b *nativeBackend) save(d nativeRecord) error {
	_, e := b.db.Exec(`UPDATE native_deployments SET record=? WHERE id=?`, nativeJSON(d), d.ID)
	return e
}
func nativeEnvelope(r nativeRequest) error {
	if r.Contract != nativeContract || !nativeID(r.DeploymentID) || r.InstallID <= 0 || r.Generation <= 0 || r.OperationID == "" || len(r.OperationID) > 256 || r.IdempotencyKey == "" || len(r.IdempotencyKey) > 512 {
		return errors.New("invalid runtime request identity/contract")
	}
	if r.Target.Type != "instances.instance" {
		return errors.New("unsupported target resource_type")
	}
	return nil
}
func nativeFingerprint(r nativeRequest, o nativeOptions) string {
	return nativeHash(nativeJSON(struct {
		Install, Generation int64
		Project             string
		Target              nativeTarget
		Manifest            sdk.Manifest
		Options             nativeOptions
	}{r.InstallID, r.Generation, r.ProjectID, r.Target, r.Manifest, o}))
}
func (b *nativeBackend) target(r nativeRequest) (*Instance, error) {
	if r.InstallID == b.installID {
		return nil, errors.New("backend cannot manage its own installation")
	}
	if r.Target.InstallID != b.installID {
		return nil, errors.New("target is not owned by this Instances installation")
	}
	id, e := strconv.ParseInt(r.Target.ID, 10, 64)
	if e != nil || id <= 0 {
		return nil, errors.New("native backend supports registered remote Linux hosts only")
	}
	inst, e := dbGetInstance(b.db, id)
	if e != nil || inst.Status != "ready" {
		return nil, errors.New("target instance is unavailable")
	}
	if inst.IsLocal() || inst.Platform != "" && inst.Platform != "linux" {
		return nil, errors.New("unsupported target platform")
	}
	return inst, nil
}
func (b *nativeBackend) validate(r nativeRequest) (*Instance, nativeOptions, nativeArtifact, nativeProbe, error) {
	var o nativeOptions
	var a nativeArtifact
	var p nativeProbe
	if e := nativeEnvelope(r); e != nil {
		return nil, o, a, p, e
	}
	if e := json.Unmarshal([]byte(nativeJSON(r.Options)), &o); e != nil {
		return nil, o, a, p, errors.New("invalid native backend options")
	}
	if o.GatewayURL == "" {
		o.GatewayURL = r.GatewayURL
	}
	if r.GatewayURL != "" && r.GatewayURL != o.GatewayURL {
		return nil, o, a, p, errors.New("gateway handoff conflict")
	}
	if e := nativeGateway(o.GatewayURL); e != nil {
		return nil, o, a, p, e
	}
	if o.ControlPort != 0 && (o.ControlPort < 1024 || o.ControlPort > 65535) || o.MinDiskMB < 0 || o.MinMemoryMB < 0 {
		return nil, o, a, p, errors.New("invalid target resource requirements")
	}
	if r.Manifest.Runtime.Kind != "source" && r.Manifest.Runtime.Kind != "service" || r.Manifest.Runtime.Resources.CPULimit != 0 || r.Manifest.Runtime.Resources.MemoryLimit != 0 || len(r.Manifest.Runtime.Storage) > 0 || r.Manifest.Runtime.Image != "" {
		return nil, o, a, p, errors.New("unsupported runtime kind or resource controls")
	}
	inst, e := b.target(r)
	if e != nil {
		return nil, o, a, p, e
	}
	a, e = b.artifact(o.ArtifactID, true)
	if e != nil {
		return nil, o, a, p, e
	}
	if a.AppName != r.Manifest.Name || a.AppVersion != r.Manifest.Version || r.ArtifactPlatform != "" && r.ArtifactPlatform != a.Platform {
		return nil, o, a, p, errors.New("artifact release/platform mismatch")
	}
	if len(r.Manifest.Runtime.Artifacts) > 0 {
		archive, ok := r.Manifest.Runtime.Artifacts[a.Platform]
		if !ok || r.Package == nil || !nativeDigest(r.Package.ArchiveSHA256) || r.Package.ArchiveSHA256 != strings.ToLower(archive.SHA256) {
			return nil, o, a, p, errors.New("verified archive handoff is missing or differs from release manifest")
		}
	}
	if e = b.verifyArtifact(a); e != nil {
		return nil, o, a, p, e
	}
	c, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()
	p, e = b.transport.Probe(c, inst, o.GatewayURL)
	if e != nil {
		return nil, o, a, p, errors.New("target SSH/systemd/gateway validation failed")
	}
	if r.TargetInfo != nil && (r.TargetInfo.HostKey != p.HostKey || r.TargetInfo.Platform != p.Platform || r.TargetInfo.ABI != p.ABI) {
		return nil, o, a, p, errors.New("target identity/environment changed after inspection")
	}
	if p.Platform != a.Platform || p.HostKey == "" {
		return nil, o, a, p, errors.New("target architecture/identity mismatch")
	}
	if e = b.verifyTargetPackage(r, a, p); e != nil {
		return nil, o, a, p, e
	}
	var size int64
	for _, f := range a.Files {
		size += f.Size
	}
	if p.MemoryMB < max(64, o.MinMemoryMB) || p.DiskMB < max((size*2>>20)+64, o.MinDiskMB) {
		return nil, o, a, p, errors.New("target has insufficient available resources")
	}
	return inst, o, a, p, nil
}
func (b *nativeBackend) create(r nativeRequest, o nativeOptions, a nativeArtifact, p nativeProbe, inst *Instance) (nativeRecord, error) {
	b.allocationMu.Lock()
	defer b.allocationMu.Unlock()
	d, e := b.record(r.DeploymentID)
	if e == nil {
		if d.Fingerprint != nativeFingerprint(r, o) || r.StateRef != "" && r.StateRef != d.Execution.StateRef {
			return d, errors.New("deployment retry conflicts with original request")
		}
		return d, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return d, e
	}
	hash := nativeHash(fmt.Sprintf("%d:%d:%s", b.installID, r.InstallID, r.DeploymentID))[:32]
	base := fmt.Sprintf("/var/lib/apteva-native/%d/inst-%d", b.installID, r.InstallID)
	port := o.ControlPort
	if port == 0 {
		v, _ := strconv.ParseUint(hash[:8], 16, 32)
		port = 50000 + int(v%10000)
	}
	stateRef := fmt.Sprintf("instances-native:%d:%s:%d", b.installID, p.HostKey, r.InstallID)
	if r.StateRef != "" && r.StateRef != stateRef {
		return d, errors.New("persistent state reference belongs to a different target/installation")
	}
	d = nativeRecord{HealthPath: r.Manifest.Runtime.HealthCheck, ID: r.DeploymentID, InstallID: r.InstallID, Generation: r.Generation, InstanceID: inst.ID, Fingerprint: nativeFingerprint(r, o), Options: o, Artifact: a, ReleaseDir: base + "/releases/" + hash, DataDir: base + "/data", Unit: fmt.Sprintf("apteva-native-%d-%s.service", b.installID, hash), ControlPort: port,
		Execution: nativeExecution{Handle: r.DeploymentID, State: "preparing", HostKey: p.HostKey, ReleaseDigest: nativeArtifactDigest(a), Platform: a.Platform, StateRef: stateRef, AppCapabilities: a.AppCapabilities, Ports: []nativePort{{Protocol: "tcp", Start: port, End: port, Exclusive: true}}}}
	if d.HealthPath == "" {
		d.HealthPath = "/health"
	}
	if !strings.HasPrefix(d.HealthPath, "/") || strings.ContainsAny(d.HealthPath, "?\x00\r\n#") || strings.Contains(d.HealthPath, "..") {
		return d, errors.New("invalid application health path")
	}
	seen := map[string]bool{fmt.Sprintf("tcp:%d", port): true}
	for _, v := range r.Manifest.Runtime.Ports {
		if v.HostPort == 0 {
			v.HostPort = v.ContainerPort
		}
		if v.HostPort == 0 || v.ContainerPort > 0 && v.ContainerPort != v.HostPort {
			return d, errors.New("native listeners need a concrete port without container remapping")
		}

		proto := v.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" || v.HostPort < 1 || v.HostPort > 65535 {
			return d, errors.New("invalid declared listener")
		}
		k := fmt.Sprintf("%s:%d", proto, v.HostPort)
		if seen[k] {
			continue
		}
		seen[k] = true
		d.Execution.Ports = append(d.Execution.Ports, nativePort{Protocol: proto, Start: v.HostPort, End: v.HostPort, Exclusive: true})
	}
	// Do not share a tunnel or physical listener with another retained execution.
	for _, port := range d.Execution.Ports {
		var count int
		err := b.db.QueryRow(`SELECT count(*) FROM native_deployments, json_each(native_deployments.record, '$.execution.ports') AS p WHERE json_extract(record,'$.execution.host_key')=? AND json_extract(record,'$.execution.state') NOT IN ('stopped','deleted') AND json_extract(p.value,'$.protocol')=? AND json_extract(p.value,'$.start')<=? AND json_extract(p.value,'$.end')>=?`, d.Execution.HostKey, port.Protocol, port.End, port.Start).Scan(&count)
		if err != nil || count > 0 {
			return d, errors.New("target listener is already reserved by another deployment")
		}
	}
	// Reserve the private tunnel port durably before starting any remote process.
	t, e := b.tunnels.open(inst, port)
	if e != nil {
		return d, errors.New("private control tunnel unavailable")
	}
	d.LocalPort = t.localPort
	d.Execution.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", d.LocalPort)
	_, e = b.db.Exec(`INSERT INTO native_deployments(id,install_id,instance_id,record) VALUES(?,?,?,?)`, d.ID, d.InstallID, d.InstanceID, nativeJSON(d))
	if e != nil {
		b.tunnels.close(inst.ID, port)
	}
	return d, e
}
func (b *nativeBackend) ensureTunnel(d *nativeRecord, inst *Instance) error {
	if d.LocalPort <= 0 {
		return errors.New("missing durable control tunnel allocation")
	}
	_, e := b.tunnels.openAt(inst, d.ControlPort, d.LocalPort)
	return e
}
func (b *nativeBackend) beginPrepare(d nativeRecord, inst *Instance) {
	b.mu.Lock()
	if b.jobs[d.ID] != nil {
		b.mu.Unlock()
		return
	}
	c, cancel := context.WithTimeout(b.ctx, 5*time.Minute)
	b.jobs[d.ID] = cancel
	b.mu.Unlock()
	go func() {
		defer cancel()
		lock := b.lock(d.ID)
		lock.Lock()
		defer lock.Unlock()
		defer func() { b.mu.Lock(); delete(b.jobs, d.ID); b.mu.Unlock() }()
		current, e := b.record(d.ID)
		if e != nil || current.Execution.State == "stopped" || current.Execution.State == "deleted" {
			return
		}
		before := nativeJSON(current)
		e = b.transport.Prepare(c, inst, current, b.artifactDir(current.Artifact.ID))
		if e != nil {
			current.Execution.State = "unknown"
		} else {
			current.Execution.State = "prepared"
		}
		_, _ = b.db.Exec(`UPDATE native_deployments SET record=? WHERE id=? AND record=?`, nativeJSON(current), current.ID, before)
	}()
}
func (b *nativeBackend) inspect(d nativeRecord, inst *Instance) nativeExecution {
	before := nativeJSON(d)
	out := d.Execution
	out.Ready = false
	c, cancel := context.WithTimeout(b.ctx, 12*time.Second)
	defer cancel()
	obs, e := b.transport.Inspect(c, inst, d)
	if e != nil {
		out.State = "unknown"
		out.StoppedConfirmed = false
		return out
	}
	switch {
	case obs.Stopped:
		out.State = "stopped"
		out.StoppedConfirmed = true
	case obs.State == "running":
		out.State = "running"
		out.StoppedConfirmed = false
		if b.ensureTunnel(&d, inst) == nil {
			env, e := b.readEnvironment(d)
			if e == nil {
				req, e := http.NewRequestWithContext(c, http.MethodGet, out.Endpoint+d.HealthPath, nil)
				if e == nil {
					req.Header.Set("Authorization", "Bearer "+env["APTEVA_APP_TOKEN"])
					client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					resp, e := client.Do(req)
					if e == nil {
						resp.Body.Close()
						out.Ready = resp.StatusCode == 200
						if out.Ready {
							manifestReq, _ := http.NewRequestWithContext(c, http.MethodGet, out.Endpoint+"/manifest", nil)
							manifestReq.Header.Set("Authorization", "Bearer "+env["APTEVA_APP_TOKEN"])
							manifestResp, err := client.Do(manifestReq)
							var running struct {
								Name    string `json:"name"`
								Version string `json:"version"`
							}
							if err != nil {
								out.Ready = false
							} else {
								decodeErr := json.NewDecoder(io.LimitReader(manifestResp.Body, 1<<20)).Decode(&running)
								manifestResp.Body.Close()
								if manifestResp.StatusCode != 200 || decodeErr != nil || running.Name != d.Artifact.AppName || running.Version != d.Artifact.AppVersion {
									out.Ready = false
									out.State = "failed"
								}
							}
						}
						if out.Ready && (d.Artifact.AppCapabilities.PassiveStart || d.Artifact.AppCapabilities.Drain || d.Artifact.AppCapabilities.ReplicaSafe || d.Artifact.AppCapabilities.WorkerCoordination) {
							capsReq, _ := http.NewRequestWithContext(c, http.MethodGet, out.Endpoint+"/_apteva/runtime/app/describe", nil)
							capsReq.Header.Set("Authorization", "Bearer "+env["APTEVA_APP_TOKEN"])
							capsResp, err := client.Do(capsReq)
							var actual nativeAppCaps
							if err != nil {
								out.Ready = false
							} else {
								decodeErr := json.NewDecoder(io.LimitReader(capsResp.Body, 16<<10)).Decode(&actual)
								capsResp.Body.Close()
								if capsResp.StatusCode != 200 || decodeErr != nil || actual != d.Artifact.AppCapabilities {
									out.Ready = false
									out.State = "failed"
								}
							}
						}
					}
				}
			}
		}
	case obs.Prepared && !d.Started:
		out.State = "prepared"
	default:
		out.State = obs.State
		if out.State == "" {
			out.State = "unknown"
		}
	}
	d.Execution = out
	if b.saveCAS(before, d) != nil {
		if latest, e := b.record(d.ID); e == nil {
			return latest.Execution
		}
		out.Ready = false
		out.State = "unknown"
	}
	return out
}
func (b *nativeBackend) environment(d nativeRecord, r nativeRequest) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range r.Environment {
		if !nativeEnvKey(k) || strings.ContainsAny(v, "\x00\r\n") {
			return nil, errors.New("invalid process environment")
		}
		env[k] = v
	}
	if env["APTEVA_INSTALL_ID"] != strconv.FormatInt(d.InstallID, 10) || env["APTEVA_DEPLOYMENT_ID"] != d.ID || env["APTEVA_DEPLOYMENT_GENERATION"] != strconv.FormatInt(d.Generation, 10) || env["APTEVA_GATEWAY_URL"] != d.Options.GatewayURL || env["APTEVA_APP_TOKEN"] == "" || env["APTEVA_PROJECT_ID"] != r.ProjectID || d.Artifact.AppCapabilities.PassiveStart && env["APTEVA_RUNTIME_MODE"] != "standby" {
		return nil, errors.New("Server environment identity/gateway handoff mismatch")
	}
	env["APTEVA_APP_PORT"] = strconv.Itoa(d.ControlPort)
	env["APTEVA_BIND_HOST"] = "127.0.0.1"
	env["APTEVA_DATA_DIR"] = d.DataDir
	env["DB_PATH"] = d.DataDir + "/app.db"
	env["APTEVA_MIGRATIONS_DIR"] = d.ReleaseDir + "/migrations"
	env["APTEVA_UI_DIR"] = d.ReleaseDir + "/ui"
	return env, nil
}
func nativeEnvKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for i, c := range k {
		if !(c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func (b *nativeBackend) secretCipher() (cipher.AEAD, error) {
	name := filepath.Join(b.root, "environment.key")
	key, e := os.ReadFile(name)
	if errors.Is(e, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, e
		}
		f, e := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(e, os.ErrExist) {
			key, e = os.ReadFile(name)
		} else if e == nil {
			_, e = f.Write(key)
			if e == nil {
				e = f.Sync()
			}
			f.Close()
		}
		if e != nil {
			return nil, e
		}
	}
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func (b *nativeBackend) secretPath(d nativeRecord) string {
	return filepath.Join(b.root, "environment-"+nativeHash(d.ID))
}
func (b *nativeBackend) saveEnvironment(d nativeRecord, env map[string]string) error {
	a, e := b.secretCipher()
	if e != nil {
		return e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	sealed := a.Seal(nonce, nonce, []byte(nativeJSON(env)), []byte(d.Fingerprint))
	tmp := b.secretPath(d) + ".tmp-" + newRequestID()
	f, e := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer os.Remove(tmp)
	_, e = f.Write(sealed)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, b.secretPath(d))
}
func (b *nativeBackend) readEnvironment(d nativeRecord) (map[string]string, error) {
	raw, e := os.ReadFile(b.secretPath(d))
	if e != nil {
		return nil, e
	}
	a, e := b.secretCipher()
	if e != nil || len(raw) < a.NonceSize() {
		return nil, errors.New("protected environment unavailable")
	}
	plain, e := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(d.Fingerprint))
	if e != nil {
		return nil, e
	}
	var env map[string]string
	e = json.Unmarshal(plain, &env)
	return env, e
}
func (a *App) handleNativeRuntime(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		http.Error(w, "Instances unavailable", 503)
		return
	}
	b, e := nativeFor(globalCtx)
	if e != nil {
		http.Error(w, "native backend unavailable", 503)
		return
	}
	b.serveHTTP(w, r)
}
func (b *nativeBackend) serveHTTP(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, nativePrefix+"/")
	if tail == "describe" && r.Method == http.MethodGet {
		writeNative(w, nativeCapabilities())
		return
	}
	if tail == "artifacts" || strings.HasPrefix(tail, "artifacts/") {
		b.artifactHTTP(w, r, strings.TrimPrefix(strings.TrimPrefix(tail, "artifacts"), "/"))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var req nativeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if e := decoder.Decode(&req); e != nil {
		http.Error(w, "invalid runtime request", 400)
		return
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		http.Error(w, "one JSON request required", 400)
		return
	}
	if e := nativeEnvelope(req); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" && key != req.IdempotencyKey+":"+tail {
		http.Error(w, "idempotency header/body mismatch", 409)
		return
	}
	if tail != "start" && len(req.Environment) > 0 {
		http.Error(w, "environment accepted only by start", 400)
		return
	}
	switch tail {
	case "inspect-target", "validate-target", "prepare", "start", "inspect", "stop", "delete", "logs":
	default:
		http.NotFound(w, r)
		return
	}
	if tail == "inspect-target" {
		info, e := b.inspectTarget(req)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		writeNative(w, info)
		return
	}
	if tail == "validate-target" {
		_, _, a, p, e := b.validate(req)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		writeNative(w, nativeExecution{HostKey: p.HostKey, Platform: a.Platform, ReleaseDigest: nativeArtifactDigest(a), AppCapabilities: a.AppCapabilities})
		return
	}
	lock := b.lock(req.DeploymentID)
	if tail == "stop" || tail == "delete" {
		b.mu.Lock()
		if cancel := b.jobs[req.DeploymentID]; cancel != nil {
			cancel()
		}
		b.mu.Unlock()
	}
	if !lock.TryLock() {
		if tail == "inspect" {
			targetID, parseErr := strconv.ParseInt(req.Target.ID, 10, 64)
			if d, e := b.record(req.DeploymentID); e == nil && parseErr == nil && d.InstallID == req.InstallID && d.Generation == req.Generation && d.InstanceID == targetID && req.Target.InstallID == b.installID {
				writeNative(w, d.Execution)
				return
			}
		}
		w.Header().Set("Retry-After", "1")
		http.Error(w, "deployment operation in progress; inspect/retry", 409)
		return
	}
	defer lock.Unlock()
	var d nativeRecord
	var inst *Instance
	var e error
	if tail == "prepare" {
		var o nativeOptions
		var a nativeArtifact
		var p nativeProbe
		if previous, err := b.record(req.DeploymentID); err == nil && (previous.Execution.State == "stopped" || previous.Execution.State == "deleted") {
			_ = json.Unmarshal([]byte(nativeJSON(req.Options)), &o)
			if o.GatewayURL == "" {
				o.GatewayURL = req.GatewayURL
			}
			if nativeFingerprint(req, o) != previous.Fingerprint {
				http.Error(w, "deployment retry conflicts with original request", 409)
				return
			}
			writeNative(w, previous.Execution)
			return
		}
		inst, o, a, p, e = b.validate(req)
		if e == nil {
			d, e = b.create(req, o, a, p, inst)
		}
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		if d.Execution.State == "preparing" || d.Execution.State == "unknown" && !d.Started {
			b.beginPrepare(d, inst)
		}
		writeNative(w, d.Execution)
		return
	}
	d, e = b.record(req.DeploymentID)
	if e != nil {
		http.Error(w, "deployment not found; ownership cannot be established", 404)
		return
	}
	if d.Execution.State == "deleted" && tail != "logs" {
		var o nativeOptions
		_ = json.Unmarshal([]byte(nativeJSON(req.Options)), &o)
		if o.GatewayURL == "" {
			o.GatewayURL = req.GatewayURL
		}
		if nativeFingerprint(req, o) != d.Fingerprint {
			http.Error(w, "deployment retry conflicts with original request", 409)
			return
		}
		writeNative(w, d.Execution)
		return
	}
	inst, e = b.target(req)
	if e != nil || d.InstallID != req.InstallID || d.Generation != req.Generation || d.InstanceID != instID(inst) {
		http.Error(w, "deployment/target ownership mismatch", 409)
		return
	}
	if d.ID != req.Execution.Handle && req.Execution.Handle != "" || req.Execution.HostKey != "" && req.Execution.HostKey != d.Execution.HostKey || req.Execution.ReleaseDigest != "" && req.Execution.ReleaseDigest != d.Execution.ReleaseDigest {
		http.Error(w, "execution descriptor conflicts with owned deployment", 409)
		return
	}
	if tail != "logs" {
		var o nativeOptions
		_ = json.Unmarshal([]byte(nativeJSON(req.Options)), &o)
		if o.GatewayURL == "" {
			o.GatewayURL = req.GatewayURL
		}
		if nativeFingerprint(req, o) != d.Fingerprint {
			http.Error(w, "deployment retry conflicts with original request", 409)
			return
		}
	}
	c, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()
	switch tail {
	case "inspect":
		if d.Execution.State == "deleted" {
			writeNative(w, d.Execution)
			return
		}
		writeNative(w, b.inspect(d, inst))
	case "start":
		if req.Package != nil {
			_, _, _, probe, err := b.validate(req)
			if err != nil || probe.HostKey != d.Execution.HostKey {
				http.Error(w, "target/package validation changed before start", 409)
				return
			}
		}
		env, e := b.environment(d, req)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		digest := nativeHash(nativeJSON(env))
		if d.Execution.State == "stopped" || d.Execution.State == "deleted" {
			writeNative(w, d.Execution)
			return
		}
		if d.Started {
			if d.EnvDigest != digest {
				http.Error(w, "start retry changed process environment", 409)
				return
			}
			out := b.inspect(d, inst)
			// A verified prepared release with no remote start intent means the crash
			// happened before any start side effect. Retry only after that observation.
			if out.State == "prepared" {
				if e = b.saveEnvironment(d, env); e != nil {
					out.State = "unknown"
					out.Ready = false
					writeNative(w, out)
					return
				}
				if e = b.transport.Start(c, inst, d, env); e == nil {
					out.State = "starting"
					out.Ready = false
					latest, _ := b.record(d.ID)
					before := nativeJSON(latest)
					latest.Execution = out
					_ = b.saveCAS(before, latest)
				} else {
					out.State = "unknown"
					out.Ready = false
				}
			}
			writeNative(w, out)
			return
		}
		if d.Execution.State != "prepared" {
			http.Error(w, "deployment is not prepared", 409)
			return
		}
		before := nativeJSON(d)
		d.Started = true
		d.EnvDigest = digest
		d.Execution.State = "starting"
		if e = b.saveCAS(before, d); e != nil {
			http.Error(w, "start ownership changed; inspect/retry", 409)
			return
		}
		if e = b.saveEnvironment(d, env); e != nil {
			d.Execution.State = "unknown"
			_ = b.save(d)
			http.Error(w, "cannot persist protected environment; inspect/retry", 503)
			return
		}
		if e = b.transport.Start(c, inst, d, env); e != nil {
			d.Execution.State = "unknown"
			_ = b.save(d)
			writeNative(w, d.Execution)
			return
		}
		d.Execution.State = "starting"
		d.Execution.Ready = false
		_ = b.save(d)
		writeNative(w, d.Execution)
	case "stop", "delete":
		if d.Execution.State == "deleted" {
			writeNative(w, d.Execution)
			return
		}
		if e = b.transport.Stop(c, inst, d); e != nil {
			d.Execution.State = "unknown"
			d.Execution.Ready = false
			d.Execution.StoppedConfirmed = false
			_ = b.save(d)
			writeNative(w, d.Execution)
			return
		}
		d.Execution.State = "stopped"
		d.Execution.StoppedConfirmed = true
		d.Execution.Ready = false
		if e = b.save(d); e != nil {
			http.Error(w, "cannot journal confirmed stop", 503)
			return
		}
		b.tunnels.close(d.InstanceID, d.ControlPort)
		if tail == "delete" {
			if e = b.transport.Delete(c, inst, d); e != nil {
				http.Error(w, "deployment cleanup outcome unknown; inspect/retry", 503)
				return
			}
			d.Execution.State = "deleted"
			_ = os.Remove(b.secretPath(d))
			if e = b.save(d); e != nil {
				http.Error(w, "cannot journal deletion", 503)
				return
			}
		}
		writeNative(w, d.Execution)
	case "logs":
		env, e := b.readEnvironment(d)
		if e != nil {
			http.Error(w, "protected log redaction context unavailable", 503)
			return
		}
		cursor, _ := req.Options["cursor"].(string)
		lines, next, e := b.transport.Logs(c, inst, d, cursor, 100)
		if e != nil {
			http.Error(w, "owned service logs unavailable", 503)
			return
		}
		redact := nativeSecrets(env)
		for i, line := range lines {
			for _, secret := range redact {
				line = strings.ReplaceAll(line, secret, "[redacted]")
			}
			if len(line) > 4096 {
				line = line[:4096] + " [truncated]"
			}
			lines[i] = line
		}
		if len(lines) > 100 {
			lines = lines[:100]
		}
		writeNative(w, map[string]any{"lines": lines, "cursor": next})
	}
}
func instID(i *Instance) int64 {
	if i == nil {
		return -1
	}
	return i.ID
}
func nativeSecrets(env map[string]string) []string {
	var secrets []string
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case string:
			if len(v) > 0 {
				secrets = append(secrets, v)
			}
		case map[string]any:
			for _, x := range v {
				collect(x)
			}
		case []any:
			for _, x := range v {
				collect(x)
			}
		}
	}
	for _, v := range env {
		if len(v) > 0 {
			secrets = append(secrets, v)
		}
		var data any
		if json.Unmarshal([]byte(v), &data) == nil {
			collect(data)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

// Host lifecycle operations must not strand an owned deployment or its data.
func nativeHostRetained(db *sql.DB, id int64) error {
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM native_deployments WHERE instance_id=? AND json_extract(record,'$.execution.state')!='deleted'`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("instance has retained native deployments; delete those deployments before destroying/forgetting this host")
	}
	return nil
}
