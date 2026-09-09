package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Setup is additive to the existing create/register/wait/get contract. A nil
// request preserves provider-only provisioning; an empty object discovers and
// verifies an existing host without installing packages.
type SetupOptions struct {
	Baseline bool `json:"baseline"`
	Docker   bool `json:"docker"`
	Runtimes bool `json:"runtimes"`
}
type HostFacts struct {
	Platform     string `json:"platform"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}
type SetupVerified struct {
	SSH      bool `json:"ssh"`
	Files    bool `json:"files"`
	Metrics  bool `json:"metrics"`
	Docker   bool `json:"docker"`
	Runtimes bool `json:"runtimes"`
}
type InstanceSetup struct {
	Versions  map[string]string `json:"versions,omitempty"`
	Requested SetupOptions      `json:"requested"`
	Status    string            `json:"status"`
	Stage     string            `json:"stage"`
	Completed []string          `json:"completed"`
	Facts     HostFacts         `json:"facts"`
	Verified  SetupVerified     `json:"verified"`
	Error     string            `json:"error,omitempty"`
	UpdatedAt string            `json:"updated_at"`
}

func initialSetupJSON(options *SetupOptions) string {
	if options == nil {
		return "{}"
	}
	data, _ := json.Marshal(InstanceSetup{Requested: *options, Status: "pending", Stage: "Connecting", Completed: []string{}, UpdatedAt: nowUTC()})
	return string(data)
}
func setupSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "description": "Optional host setup. Empty object discovers OS and verifies SSH/files/metrics without installing packages. baseline installs common utilities; docker installs and verifies Docker; runtimes installs distribution-provided Node.js, npm, and Go. Installation is owned by Instances and runs through its SSH implementation. Ubuntu/Debian AMD64/ARM64 for package setup.", "properties": map[string]any{
		"baseline": map[string]any{"type": "boolean"}, "docker": map[string]any{"type": "boolean"}, "runtimes": map[string]any{"type": "boolean"}}}
}
func parseSetup(args map[string]any) (*SetupOptions, error) {
	raw, present := args["setup"]
	if !present {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("setup must be an object")
	}
	var options SetupOptions
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&options); err != nil {
		return nil, fmt.Errorf("invalid setup: %w", err)
	}
	return &options, nil
}

var setupRunSSH = runSSH

// Use the existing collector directly: the public metrics tool correctly
// requires ready, while setup is the operation establishing that state.
var setupCollectMetrics = collectRemoteMetrics
var setupUploadSSH = uploadSSH
var setupDownloadSSH = downloadSSH

func saveSetup(ctx *sdk.AppCtx, inst *Instance) error {
	inst.Setup.UpdatedAt = nowUTC()
	data, err := json.Marshal(inst.Setup)
	if err != nil {
		return err
	}
	result, err := ctx.AppDB().Exec(`UPDATE instances SET setup_json=?, lifecycle_stage=? WHERE id=? AND status='provisioning'`, string(data), inst.Setup.Stage, inst.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrOperationSuperseded
	}
	return nil
}
func setupCommand(ctx *sdk.AppCtx, inst *Instance, script string, timeout time.Duration) (string, error) {
	work := inst.workContext
	if work == nil {
		work = context.Background()
	}
	if work.Err() != nil {
		return "", work.Err()
	}
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return "", err
	}
	if fresh.Status != "provisioning" {
		return "", ErrOperationSuperseded
	}
	type result struct {
		output string
		code   int
		err    error
	}
	done := make(chan result, 1)
	go func() { o, c, e := setupRunSSH(inst, script, timeout); done <- result{o, c, e} }()
	select {
	case <-work.Done():
		return "", work.Err()
	case r := <-done:
		if r.err != nil {
			return "", fmt.Errorf("remote setup: %w", r.err)
		}
		if r.code != 0 {
			return "", fmt.Errorf("remote setup exited %d: %s", r.code, truncateSetupOutput(r.output))
		}
		return r.output, nil
	}
}
func truncateSetupOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		s = s[len(s)-2000:]
	}
	return s
}

func prepareInstance(ctx *sdk.AppCtx, inst *Instance) (err error) {
	inst.workContext = instanceWorkerContext(ctx, inst.ID)
	options := inst.Setup.Requested
	inst.Setup = &InstanceSetup{Requested: options, Status: "running", Stage: "Inspecting", Versions: map[string]string{}, Completed: []string{"Connecting"}, Verified: SetupVerified{SSH: true}}
	defer func() {
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrOperationSuperseded) {
			return
		}
		inst.Setup.Status = "error"
		inst.Setup.Error = err.Error()
		if saveSetup(ctx, inst) == nil {
			_, _, _ = transitionInstanceAndEmit(ctx, inst.ID, []string{"provisioning"}, "error", map[string]any{"error_message": err.Error()})
		}
	}()
	step := func(stage string, run func() error) error {
		inst.Setup.Stage = stage
		if err := saveSetup(ctx, inst); err != nil {
			return err
		}
		if err := run(); err != nil {
			return err
		}
		inst.Setup.Completed = append(inst.Setup.Completed, stage)
		return saveSetup(ctx, inst)
	}
	if err = step("Inspecting", func() error {
		output, e := setupCommand(ctx, inst, hostDiscoveryScript, 30*time.Second)
		if e != nil {
			return e
		}
		facts, e := parseHostFacts(output)
		if e != nil {
			return e
		}
		inst.Setup.Facts = facts
		inst.Platform = facts.Platform
		if options.Baseline || options.Docker || options.Runtimes {
			if facts.Platform != "linux" || (facts.OS != "ubuntu" && facts.OS != "debian") || (facts.Architecture != "amd64" && facts.Architecture != "arm64") {
				return fmt.Errorf("automatic package setup supports Ubuntu/Debian on AMD64/ARM64; detected %s/%s/%s", facts.Platform, facts.OS, facts.Architecture)
			}
		}
		return dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"platform": facts.Platform})
	}); err != nil {
		return err
	}
	if options.Baseline {
		if err = step("Baseline", func() error { _, e := setupCommand(ctx, inst, baselineSetupScript, 10*time.Minute); return e }); err != nil {
			return err
		}
	}
	if options.Docker {
		if err = step("Docker", func() error {
			if _, e := setupCommand(ctx, inst, dockerSetupScript, 10*time.Minute); e != nil {
				return e
			}
			// New command uses a fresh SSH session with updated group membership.
			output, e := setupCommand(ctx, inst, dockerVerifyScript, 3*time.Minute)
			if e != nil {
				return e
			}
			if strings.TrimSpace(output) == "" {
				return errors.New("Docker did not report a version")
			}
			inst.Setup.Verified.Docker = true
			inst.Setup.Versions["docker"] = strings.TrimSpace(output)
			return nil
		}); err != nil {
			return err
		}
	}
	if options.Runtimes {
		if err = step("Language tools", func() error {
			output, e := setupCommand(ctx, inst, runtimesSetupScript, 10*time.Minute)
			if e != nil {
				return e
			}
			for _, line := range strings.Split(output, "\n") {
				key, value, ok := strings.Cut(line, "=")
				if ok && (key == "node" || key == "npm" || key == "go") {
					inst.Setup.Versions[key] = strings.TrimSpace(value)
				}
			}
			for _, key := range []string{"node", "npm", "go"} {
				if inst.Setup.Versions[key] == "" {
					return fmt.Errorf("language tool %s did not report a version", key)
				}
			}
			inst.Setup.Verified.Runtimes = true
			return nil
		}); err != nil {
			return err
		}
	}

	if err = step("Verifying", func() error {
		if _, e := setupCommand(ctx, inst, fileCheckScript, 30*time.Second); e != nil {
			return e
		}
		if e := verifySetupFileTransfer(ctx, inst); e != nil {
			return e
		}
		inst.Setup.Verified.Files = true
		m, e := setupCollectMetrics(inst)
		if e != nil {
			return fmt.Errorf("verify metrics: %w", e)
		}
		if m == nil || m.Mem.TotalBytes == 0 || m.CPU.Cores == 0 {
			return errors.New("host returned incomplete metrics")
		}
		inst.Setup.Verified.Metrics = true
		if inst.Provider == "external" {
			resources, _ := json.Marshal(map[string]any{"cpu": map[string]any{"cores": m.CPU.Cores}, "memory_gb": float64(m.Mem.TotalBytes) / (1 << 30), "disk": m.Disk})
			return dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"resources_json": string(resources)})
		}
		return nil
	}); err != nil {
		return err
	}
	inst.Setup.Status = "ready"
	inst.Setup.Stage = "Ready"
	return saveSetup(ctx, inst)
}
func parseHostFacts(output string) (HostFacts, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 3 {
		return HostFacts{}, errors.New("host discovery returned incomplete output")
	}
	f := HostFacts{Platform: strings.TrimSpace(lines[len(lines)-3]), Architecture: strings.TrimSpace(lines[len(lines)-2]), OS: strings.TrimSpace(lines[len(lines)-1])}
	switch f.Platform {
	case "Linux":
		f.Platform = "linux"
	case "Darwin":
		f.Platform = "macos"
	default:
		return f, fmt.Errorf("unsupported SSH host platform %q", f.Platform)
	}
	switch f.Architecture {
	case "x86_64":
		f.Architecture = "amd64"
	case "aarch64", "arm64":
		f.Architecture = "arm64"
	}
	return f, nil
}

const hostDiscoveryScript = `set -eu
uname -s
uname -m
if [ -r /etc/os-release ]; then . /etc/os-release; printf '%s\n' "$ID"; else printf 'macos\n'; fi
`
const setupPrivilegeScript = `set -eu
if [ "$(id -u)" -eq 0 ]; then SUDO=""; else sudo -n true || { echo 'Passwordless sudo is required for the selected setup' >&2; exit 1; }; SUDO="sudo -n"; fi
`
const baselineSetupScript = setupPrivilegeScript + `
missing=0
for cmd in curl git python3 tar gzip bash base64 ss; do command -v "$cmd" >/dev/null 2>&1 || missing=1; done
if [ "$missing" -eq 1 ] || [ ! -s /etc/ssl/certs/ca-certificates.crt ]; then
 $SUDO apt-get update -qq
 $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl git python3 tar gzip bash coreutils util-linux iproute2
fi
$SUDO install -d -m 0755 -o "$(id -u)" -g "$(id -g)" /var/lib/apteva-instances
`
const fileCheckScript = `set -eu
probe=$(mktemp -d "$HOME/.apteva-instance-check.XXXXXX")
trap 'rm -rf "$probe"' EXIT
printf 'apteva-file-check\n' > "$probe/check"
[ "$(cat "$probe/check")" = 'apteva-file-check' ]
`

func reconcileExternalHosts(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "external", "provisioning")
	if err != nil {
		return
	}
	for _, inst := range rows {
		if inst.SSHHost != "" {
			kickExternalReadiness(ctx, inst.ID)
		}
	}
}
func kickExternalReadiness(ctx *sdk.AppCtx, id int64) {
	probe := probeSSHReadyFn
	startInstanceWorker(ctx, id, func(work context.Context) {
		inst, e := dbGetInstance(ctx.AppDB(), id)
		if e != nil || inst.Status != "provisioning" {
			return
		}
		inst.workContext = work
		// While awaiting the authorization command, a failed SSH attempt is normal.
		// Leave the registration pending; the periodic reconciler will try again.
		if e = probe(inst, 20*time.Second); e != nil {
			if inst.Setup != nil && work.Err() == nil {
				inst.Setup.Status = "pending"
				inst.Setup.Stage = "Connecting"
				inst.Setup.Error = "Waiting for SSH access: " + e.Error()
				_ = saveSetup(ctx, inst)
			}
			return
		}
		if work.Err() != nil {
			return
		}
		_, _, _ = transitionInstanceAndEmit(ctx, id, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC(), "error_message": "", "lifecycle_stage": "Ready"})
	})
}

func retryInstanceSetup(ctx *sdk.AppCtx, id int64, options *SetupOptions) error {

	unlock, err := lockResource(ctx.AppDB(), "instance", id)
	if err != nil {
		return err
	}
	defer unlock()
	workerMutex.Lock()
	busy := instanceWorkers[resourceKey{ctx.AppDB(), "instance", id}] != nil
	workerMutex.Unlock()
	if busy {
		return errors.New("instance setup is already running")
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return err
	}
	if inst.IsLocal() {
		return errors.New("host setup is for remote instances")
	}
	if inst.Status != "ready" && inst.Status != "error" && inst.Status != "provisioning" {
		return ErrOperationSuperseded
	}
	if inst.Status == "error" && (inst.Setup == nil || inst.Setup.Status != "error") {
		return errors.New("resolve the provider provisioning error before retrying host setup")
	}
	if options == nil {
		if inst.Setup != nil {
			options = &inst.Setup.Requested
		} else {
			options = &SetupOptions{}
		}
	}

	// Clear old verification before running repairs; it must never appear ready
	// while a newly requested capability is still being installed.
	ok, err := dbTransitionStatus(ctx.AppDB(), id, []string{inst.Status}, "provisioning", map[string]any{"error_message": "", "ready_at": "", "lifecycle_stage": "Connecting", "setup_json": initialSetupJSON(options)})
	if err != nil || !ok {
		return ErrOperationSuperseded
	}

	if inst.Provider == "external" {
		kickExternalReadiness(ctx, id)
	} else {
		kickReadinessProbe(ctx, id)
	}
	return nil
}

func verifySetupFileTransfer(ctx *sdk.AppCtx, inst *Instance) error {
	token, err := newSSHExitMarker()
	if err != nil {
		return err
	}
	path := "/tmp/.apteva-instance-check-" + token
	payload := base64.StdEncoding.EncodeToString([]byte(token))
	defer setupCommand(ctx, inst, "rm -f -- "+quoteShellArg(path), 10*time.Second)
	if _, err = setupUploadSSH(inst, path, payload); err != nil {
		return fmt.Errorf("verify upload: %w", err)
	}
	got, _, err := setupDownloadSSH(inst, path)
	if err != nil {
		return fmt.Errorf("verify download: %w", err)
	}
	if got != payload {
		return errors.New("file-transfer verification returned different content")
	}
	return nil
}

const dockerSetupScript = setupPrivilegeScript + `
if ! command -v docker >/dev/null 2>&1; then
 $SUDO apt-get update -qq
 $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io
fi
$SUDO systemctl enable --now docker
if [ "$(id -u)" -ne 0 ] && ! docker info >/dev/null 2>&1; then
 $SUDO usermod -aG docker "$(id -un)"
fi
`
const dockerVerifyScript = `set -eu
docker info >/dev/null
docker run --rm --network none hello-world:latest >/dev/null
docker version --format '{{.Server.Version}}'
`
const runtimesSetupScript = setupPrivilegeScript + `
instance_packages=""
command -v node >/dev/null 2>&1 || instance_packages="$instance_packages nodejs"
command -v npm >/dev/null 2>&1 || instance_packages="$instance_packages npm"
command -v go >/dev/null 2>&1 || instance_packages="$instance_packages golang-go"
if [ -n "$instance_packages" ]; then
 $SUDO apt-get update -qq
 $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y $instance_packages
fi
instance_node_version=$(node --version)
instance_npm_version=$(npm --version)
instance_go_version=$(go version)
printf 'node=%s\n' "$instance_node_version"
printf 'npm=%s\n' "$instance_npm_version"
printf 'go=%s\n' "$instance_go_version"
`
