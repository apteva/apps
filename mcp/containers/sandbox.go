package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Network modes for a workload. "default" is the per-workload Docker bridge
// network with normal internet access (the only mode before 0.6.0).
const (
	NetworkDefault      = "default"
	NetworkNone         = "none"
	NetworkRegistryOnly = "registry_only"
)

const (
	defaultPidsLimit  = 512
	hardenedPidsLimit = 256
	minPidsLimit      = 16
	maxPidsLimit      = 4096
	maxTmpfsMounts    = 8
	defaultTmpfsMB    = 64
	autoTmpTmpfsMB    = 256
	maxTmpfsMB        = 4096
	maxEgressAllow    = 32

	// The egress proxy is a pinned Alpine + tinyproxy image committed once per
	// host. It runs unprivileged with a read-only root and only its config in tmpfs.
	egressImage     = "apteva-containers-egress:alpine3.20-tinyproxy1.11-1"
	egressBaseImage = "alpine:3.20"
	egressAlias     = "apteva-egress"
	egressPort      = 8888
)

var defaultEgressAllow = []string{"registry.npmjs.org"}

type TmpfsSpec struct {
	MountPath string `json:"mount_path"`
	SizeMB    int    `json:"size_mb,omitempty"`
}

var egressEntryRE = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(:[0-9]{1,5})?$`)

func normalizeNetworkMode(mode string, allow []string) (string, []string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = NetworkDefault
	}
	switch mode {
	case NetworkDefault, NetworkNone:
		if len(allow) > 0 {
			return mode, nil, errors.New("egress_allow is only valid with network_mode registry_only")
		}
		return mode, nil, nil
	case NetworkRegistryOnly:
	default:
		return mode, nil, fmt.Errorf("unsupported network_mode %q (use default, none, or registry_only)", mode)
	}
	if len(allow) == 0 {
		allow = defaultEgressAllow
	}
	if len(allow) > maxEgressAllow {
		return mode, nil, fmt.Errorf("egress_allow supports at most %d entries", maxEgressAllow)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(allow))
	for i, raw := range allow {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if len(entry) > 253 || !egressEntryRE.MatchString(entry) {
			return mode, nil, fmt.Errorf("egress_allow[%d] must be a host name, *.domain, or host:port", i)
		}
		if _, port, ok := strings.Cut(entry, ":"); ok {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return mode, nil, fmt.Errorf("egress_allow[%d] has an invalid port", i)
			}
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return mode, out, nil
}

// normalizeSandboxSpec validates the 0.6.0 isolation options. It runs after
// volumes and files are normalized so it can cross-check mount paths.
func normalizeSandboxSpec(in *RunSpec) error {
	mode, allow, err := normalizeNetworkMode(in.NetworkMode, in.EgressAllow)
	if err != nil {
		return err
	}
	in.NetworkMode, in.EgressAllow = mode, allow
	if mode != NetworkDefault && len(in.Ports) > 0 {
		return errors.New("published ports require network_mode default")
	}
	if in.Hardened {
		in.ReadOnlyRootfs = true
		if err := requireNonRootUser(in.User); err != nil {
			return err
		}
	}
	if in.PidsLimit == 0 {
		in.PidsLimit = defaultPidsLimit
		if in.Hardened {
			in.PidsLimit = hardenedPidsLimit
		}
	}
	if in.PidsLimit < minPidsLimit || in.PidsLimit > maxPidsLimit {
		return fmt.Errorf("pids_limit must be between %d and %d", minPidsLimit, maxPidsLimit)
	}
	mounts := map[string]bool{}
	for i := range in.Volumes {
		v := &in.Volumes[i]
		mounts[v.MountPath] = true
		v.SharedFrom = strings.TrimSpace(v.SharedFrom)
		if v.SharedFrom == "" {
			continue
		}
		if v.RetainedFrom != "" {
			return fmt.Errorf("volumes[%d] cannot set both retained_from and shared_from", i)
		}
		if !v.ReadOnly {
			return fmt.Errorf("volumes[%d].shared_from requires read_only: true", i)
		}
	}
	if len(in.Tmpfs) > maxTmpfsMounts {
		return fmt.Errorf("tmpfs supports at most %d mounts", maxTmpfsMounts)
	}
	for i := range in.Tmpfs {
		t := &in.Tmpfs[i]
		raw := strings.TrimSpace(t.MountPath)
		if !strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, ":,\x00") {
			return fmt.Errorf("tmpfs[%d].mount_path must be absolute without ':' or ','", i)
		}
		t.MountPath = path.Clean(raw)
		if t.MountPath == "/" {
			return fmt.Errorf("tmpfs[%d].mount_path cannot be /", i)
		}
		if mounts[t.MountPath] {
			return fmt.Errorf("tmpfs[%d].mount_path duplicates another mount at %q", i, t.MountPath)
		}
		mounts[t.MountPath] = true
		if t.SizeMB == 0 {
			t.SizeMB = defaultTmpfsMB
		}
		if t.SizeMB < 1 || t.SizeMB > maxTmpfsMB {
			return fmt.Errorf("tmpfs[%d].size_mb must be between 1 and %d", i, maxTmpfsMB)
		}
	}
	// Executions keep their control files under /tmp, so a read-only root needs
	// a writable /tmp. Add one unless the caller already mounted something there.
	if in.ReadOnlyRootfs && !mounts["/tmp"] {
		in.Tmpfs = append(in.Tmpfs, TmpfsSpec{MountPath: "/tmp", SizeMB: autoTmpTmpfsMB})
	}
	if err := checkNoPlatformSecrets(in.Env, in.Hardened); err != nil {
		return err
	}
	for i, f := range in.Files {
		v, _, err := resolveVolumeFileTarget(in.Volumes, f.Path)
		if err == nil && v.ReadOnly {
			return fmt.Errorf("files[%d].path targets read-only volume %q", i, v.Name)
		}
		content, err := fileContentBytes(f)
		if err == nil && containsPlatformToken(string(content)) {
			return fmt.Errorf("files[%d] contains the platform app token", i)
		}
	}
	return nil
}

func requireNonRootUser(user string) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return errors.New("hardened workloads require a non-root user (for example \"1000:1000\")")
	}
	name, _, _ := strings.Cut(user, ":")
	if name == "root" || name == "0" {
		return errors.New("hardened workloads cannot run as root")
	}
	return nil
}

// platformToken is the sidecar's own credential. It must never be copied into
// a workload: anything inside the container could call the platform as us.
func platformToken() string {
	return strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
}

func containsPlatformToken(value string) bool {
	token := platformToken()
	return len(token) >= 8 && strings.Contains(value, token)
}

func checkNoPlatformSecrets(env map[string]string, hardened bool) error {
	for _, key := range envKeys(env) {
		if containsPlatformToken(env[key]) {
			return fmt.Errorf("env %q contains the platform app token", key)
		}
		if hardened && (key == "DOCKER_HOST" || strings.HasPrefix(key, "APTEVA_")) {
			return fmt.Errorf("hardened workloads cannot receive env %q", key)
		}
	}
	return nil
}

func proxyEnv() map[string]string {
	url := fmt.Sprintf("http://%s:%d", egressAlias, egressPort)
	return map[string]string{
		"HTTP_PROXY": url, "HTTPS_PROXY": url, "http_proxy": url, "https_proxy": url,
		"npm_config_proxy": url, "npm_config_https_proxy": url,
		"NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1",
	}
}

// sandboxRunArgs returns the extra docker run/create flags for the isolation
// options. Volumes are rendered here so read-only mounts get :ro.
func sandboxRunArgs(spec RunSpec) ([]string, error) {
	var args []string
	for _, v := range spec.Volumes {
		// A source containing "/" would make Docker bind-mount a host path (for
		// example the Docker socket). Workload mounts are always named volumes.
		if v.DockerVolumeName == "" || strings.ContainsAny(v.DockerVolumeName, "/:\x00") || strings.HasPrefix(v.DockerVolumeName, ".") {
			return nil, fmt.Errorf("volume %q has an invalid docker volume name", v.Name)
		}
		mount := v.DockerVolumeName + ":" + v.MountPath
		if v.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	for _, t := range spec.Tmpfs {
		size := t.SizeMB
		if size <= 0 {
			size = defaultTmpfsMB
		}
		args = append(args, "--tmpfs", fmt.Sprintf("%s:rw,nosuid,nodev,size=%dm", t.MountPath, size))
	}
	if spec.ReadOnlyRootfs || spec.Hardened {
		args = append(args, "--read-only")
	}
	if spec.Hardened {
		args = append(args, "--cap-drop", "ALL", "--security-opt", "no-new-privileges")
	}
	if spec.NetworkMode == NetworkRegistryOnly {
		env := proxyEnv()
		for _, key := range envKeys(env) {
			if _, set := spec.Env[key]; !set {
				args = append(args, "-e", key+"="+env[key])
			}
		}
	}
	pids := spec.PidsLimit
	if pids <= 0 {
		pids = defaultPidsLimit
	}
	args = append(args, "--pids-limit", strconv.Itoa(pids))
	return args, nil
}

// --- Docker plumbing shared by local and remote backends ---

type dockerCmdFunc func(ctx context.Context, args ...string) (string, error)

// sandboxRuntime is implemented by backends that can run the non-default
// network modes. Test fakes that do not implement it only support "default".
type sandboxRuntime interface {
	sandboxDocker() dockerCmdFunc
}

func (d LocalDocker) sandboxDocker() dockerCmdFunc { return docker }

func (d *RemoteDocker) sandboxDocker() dockerCmdFunc {
	return func(ctx context.Context, args ...string) (string, error) {
		return d.remoteDocker(ctx, 300, args...)
	}
}

func sandboxNames(containerName, networkName string) (internalNetwork, proxyName string) {
	return networkName + "-int", containerName + "-egress"
}

func dockerMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such") || strings.Contains(msg, "not found") || strings.Contains(msg, "is not connected")
}

func ensureInternalNetwork(ctx context.Context, run dockerCmdFunc, name, workloadID string) error {
	if _, err := run(ctx, "network", "inspect", name); err == nil {
		out, err := run(ctx, "network", "inspect", "--format", "{{.Internal}}", name)
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "true" {
			return fmt.Errorf("network %s exists but is not internal", name)
		}
		return nil
	}
	_, err := run(ctx, "network", "create", "--internal", "--label", "com.apteva.workload-id="+workloadID, name)
	return err
}

func ensureEgressImage(ctx context.Context, run dockerCmdFunc) error {
	if _, err := run(ctx, "image", "inspect", egressImage); err == nil {
		return nil
	}
	builder := "containers-egress-build-" + strings.TrimPrefix(newWorkloadID(), "wrk_")
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = run(c, "rm", "-f", builder)
	}()
	if _, err := run(ctx, "run", "--name", builder, "--pids-limit", "64", "--memory", "256m", egressBaseImage,
		"sh", "-c", "apk add --no-cache 'tinyproxy~=1.11' && tinyproxy -v"); err != nil {
		return fmt.Errorf("build egress proxy image: %w", err)
	}
	if _, err := run(ctx, "commit", "--change", "USER 65534:65534", "--change", `CMD ["tinyproxy","-v"]`, builder, egressImage); err != nil {
		return fmt.Errorf("commit egress proxy image: %w", err)
	}
	return nil
}

func egressFilter(allow []string) string {
	lines := make([]string, 0, len(allow))
	for _, entry := range allow {
		host, _, _ := strings.Cut(entry, ":")
		wildcard := strings.HasPrefix(host, "*.")
		host = strings.TrimPrefix(host, "*.")
		quoted := strings.ReplaceAll(host, ".", `\.`)
		if wildcard {
			lines = append(lines, `^([a-z0-9-]+\.)+`+quoted+`$`)
		} else {
			lines = append(lines, `^`+quoted+`$`)
		}
	}
	return strings.Join(lines, "\n")
}

func egressConfig(allow []string) string {
	ports := map[int]bool{443: true}
	for _, entry := range allow {
		if _, port, ok := strings.Cut(entry, ":"); ok {
			n, _ := strconv.Atoi(port)
			ports[n] = true
		}
	}
	sorted := make([]int, 0, len(ports))
	for p := range ports {
		sorted = append(sorted, p)
	}
	sort.Ints(sorted)
	lines := []string{
		fmt.Sprintf("Port %d", egressPort), "Listen 0.0.0.0", "Timeout 600", "MaxClients 128",
		"LogLevel Notice", `Filter "/tmp/filter"`, "FilterType ere", "FilterDefaultDeny Yes",
		"FilterURLs Off", "FilterCaseSensitive Off", "DisableViaHeader Yes",
	}
	for _, p := range sorted {
		lines = append(lines, fmt.Sprintf("ConnectPort %d", p))
	}
	return strings.Join(lines, "\n")
}

const egressProxyScript = `printf '%s\n' "$EGRESS_CONF" > /tmp/tinyproxy.conf && printf '%s\n' "$EGRESS_FILTER" > /tmp/filter && exec tinyproxy -d -c /tmp/tinyproxy.conf`

// startEgressProxy (re)creates the allow-list proxy for one workload. It sits
// on the workload's normal network (its way out) and on the internal network
// under the apteva-egress alias (the workload's only way out).
func startEgressProxy(ctx context.Context, run dockerCmdFunc, workloadID, proxyName, egressNetwork, internalNetwork string, allow []string) error {
	if err := removeOwnedContainer(ctx, run, proxyName, workloadID); err != nil {
		return err
	}
	if err := ensureEgressImage(ctx, run); err != nil {
		return err
	}
	if _, err := run(ctx, "create", "--name", proxyName,
		"--label", "com.apteva.workload-id="+workloadID, "--label", "com.apteva.role=egress-proxy",
		"--restart", "unless-stopped", "--network", egressNetwork,
		"--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=1m",
		"--memory", "64m", "--cpus", "0.5", "--pids-limit", "64",
		"--log-driver", "local", "--log-opt", "max-size=1m", "--log-opt", "max-file=2",
		"-e", "EGRESS_CONF="+egressConfig(allow), "-e", "EGRESS_FILTER="+egressFilter(allow),
		"--", egressImage, "sh", "-c", egressProxyScript); err != nil {
		return fmt.Errorf("create egress proxy: %w", err)
	}
	if _, err := run(ctx, "network", "connect", "--alias", egressAlias, internalNetwork, proxyName); err != nil {
		return fmt.Errorf("attach egress proxy: %w", err)
	}
	if _, err := run(ctx, "start", proxyName); err != nil {
		return fmt.Errorf("start egress proxy: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := run(ctx, "exec", proxyName, "nc", "-z", "127.0.0.1", strconv.Itoa(egressPort)); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			logs, _ := run(ctx, "logs", "--tail", "20", proxyName)
			return fmt.Errorf("egress proxy did not start listening: %s", strings.TrimSpace(logs))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func removeOwnedContainer(ctx context.Context, run dockerCmdFunc, name, workloadID string) error {
	owner, err := run(ctx, "container", "inspect", "--format", `{{ index .Config.Labels "com.apteva.workload-id" }}`, name)
	if dockerMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(owner) != workloadID {
		return fmt.Errorf("container %s is not owned by workload %s", name, workloadID)
	}
	if _, err := run(ctx, "rm", "-f", name); err != nil && !dockerMissing(err) {
		return err
	}
	return nil
}

// removeSandboxRuntime deletes the egress proxy and internal network. Missing
// resources are fine: most workloads never had them.
func removeSandboxRuntime(ctx context.Context, backend DockerBackend, workloadID, containerName, networkName string) error {
	sb, ok := backend.(sandboxRuntime)
	if !ok {
		return nil
	}
	run := sb.sandboxDocker()
	internal, proxy := sandboxNames(containerName, networkName)
	var errs []error
	if err := removeOwnedContainer(ctx, run, proxy, workloadID); err != nil {
		errs = append(errs, fmt.Errorf("remove egress proxy: %w", err))
	}
	if _, err := run(ctx, "network", "rm", internal); err != nil && !dockerMissing(err) {
		errs = append(errs, fmt.Errorf("remove internal network: %w", err))
	}
	return errors.Join(errs...)
}

func containerNetworks(ctx context.Context, run dockerCmdFunc, container string) ([]string, error) {
	out, err := run(ctx, "container", "inspect", "--format", `{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}`, container)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// applyNetworkMode moves a container between modes. It disconnects before it
// connects, so a switch to a stricter mode never has a window with both paths.
func applyNetworkMode(ctx context.Context, run dockerCmdFunc, workloadID, containerName, networkName, mode string, allow []string) error {
	internal, proxy := sandboxNames(containerName, networkName)
	target := ""
	switch mode {
	case NetworkDefault:
		target = networkName
	case NetworkRegistryOnly:
		target = internal
		if err := ensureInternalNetwork(ctx, run, internal, workloadID); err != nil {
			return err
		}
		if err := startEgressProxy(ctx, run, workloadID, proxy, networkName, internal, allow); err != nil {
			return err
		}
	case NetworkNone:
	default:
		return fmt.Errorf("unsupported network_mode %q", mode)
	}
	current, err := containerNetworks(ctx, run, containerName)
	if err != nil {
		return err
	}
	attached := false
	for _, name := range current {
		if name == target {
			attached = true
			continue
		}
		if _, err := run(ctx, "network", "disconnect", "--force", name, containerName); err != nil && !dockerMissing(err) {
			return fmt.Errorf("disconnect %s: %w", name, err)
		}
	}
	if target != "" && !attached {
		if _, err := run(ctx, "network", "connect", target, containerName); err != nil {
			return fmt.Errorf("connect %s: %w", target, err)
		}
	}
	if mode != NetworkRegistryOnly {
		if err := removeOwnedContainer(ctx, run, proxy, workloadID); err != nil {
			return err
		}
		if _, err := run(ctx, "network", "rm", internal); err != nil && !dockerMissing(err) {
			return err
		}
	}
	return nil
}

// runSandboxed creates a workload whose network mode is not "default". The
// container is created stopped, placed on its target network, then started,
// so its entrypoint never runs with more network access than requested.
func runSandboxed(ctx context.Context, backend DockerBackend, spec RunSpec, containerName, networkName string) (string, error) {
	sb, ok := backend.(sandboxRuntime)
	if !ok {
		return "", fmt.Errorf("network_mode %s is not supported by this container backend", spec.NetworkMode)
	}
	run := sb.sandboxDocker()
	createNetwork := networkName
	internal, proxy := sandboxNames(containerName, networkName)
	if spec.NetworkMode == NetworkRegistryOnly {
		if err := ensureInternalNetwork(ctx, run, internal, spec.runtimeWorkloadID); err != nil {
			return "", err
		}
		if err := startEgressProxy(ctx, run, spec.runtimeWorkloadID, proxy, networkName, internal, spec.EgressAllow); err != nil {
			return "", err
		}
		createNetwork = internal
	}
	args, err := dockerRunArgs(spec, containerName, createNetwork)
	if err != nil {
		return "", err
	}
	args = append([]string{"create"}, args[2:]...) // drop "run", "-d"
	out, err := run(ctx, args...)
	if err != nil {
		return "", err
	}
	if spec.NetworkMode == NetworkNone {
		if _, err := run(ctx, "network", "disconnect", "--force", networkName, containerName); err != nil {
			return "", err
		}
	}
	if _, err := run(ctx, "start", containerName); err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// --- network mode switching (containers_network_set) ---

func (a *App) setNetworkMode(ctx context.Context, appCtx *sdk.AppCtx, db *sql.DB, id, mode string, allow []string) (*Workload, error) {
	mode, allow, err := normalizeNetworkMode(mode, allow)
	if err != nil {
		return nil, typedInputError(err)
	}
	ctx, unlock, err := a.lockWorkload(ctx, id, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	w, err := requireWorkload(db, id)
	if err != nil {
		return nil, err
	}
	if w.Status != StatusRunning && w.Status != StatusStopped && w.Status != StatusUnhealthy {
		return nil, fmt.Errorf("%w: workload is %s", errConflict, w.Status)
	}
	if mode != NetworkDefault && len(w.Ports) > 0 {
		return nil, typedInputError(errors.New("workloads with published ports must stay on network_mode default"))
	}
	backend, err := a.backendForWorkload(appCtx, w)
	if err != nil {
		return nil, err
	}
	sb, ok := backend.(sandboxRuntime)
	if !ok {
		return nil, errors.New("network modes are not supported by this container backend")
	}
	previous := w.NetworkMode
	if err := applyNetworkMode(ctx, sb.sandboxDocker(), w.ID, w.ContainerName, w.NetworkName, mode, allow); err != nil {
		_ = recordEvent(db, id, "network_mode_failed", "tool", map[string]any{"mode": mode, "error": err.Error()})
		return nil, err
	}
	var stored RunSpec
	_ = jsonUnmarshal(w.ConfigJSON, &stored)
	stored.NetworkMode, stored.EgressAllow = mode, allow
	if err := updateWorkload(db, id, map[string]any{"config_json": encodeJSON(stored), "updated_at": nowUTC()}); err != nil {
		return nil, err
	}
	_ = recordEvent(db, id, "network_mode", "tool", map[string]any{"from": previous, "to": mode, "egress_allow": allow})
	return getWorkload(db, id)
}

func networkSetSchema() map[string]any {
	return schemaObject(map[string]any{
		"workload_id":  map[string]any{"type": "string"},
		"mode":         map[string]any{"type": "string", "enum": []string{NetworkDefault, NetworkNone, NetworkRegistryOnly}},
		"egress_allow": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": maxEgressAllow},
	}, []string{"workload_id", "mode"})
}

func (a *App) toolNetworkSet(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := requireAppOwner(callCtx, app)
	if err != nil {
		return nil, err
	}
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), owner)
	if err != nil {
		return nil, err
	}
	var allow []string
	if raw, ok := args["egress_allow"].([]any); ok {
		for _, item := range raw {
			s, ok := item.(string)
			if !ok {
				return nil, typedInputError(errors.New("egress_allow must be an array of strings"))
			}
			allow = append(allow, s)
		}
	} else if args["egress_allow"] != nil {
		return nil, typedInputError(errors.New("egress_allow must be an array of strings"))
	}
	ctx, cancel := context.WithTimeout(callCtx, 3*time.Minute)
	defer cancel()
	workload, err = a.setNetworkMode(ctx, app, app.AppDB(), workload.ID, getStr(args, "mode"), allow)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": workload}, nil
}

// --- shared volumes ---

func sharedVolume(db *sql.DB, sourceID, name string, owner ownerIdentity, targetID int64) (string, error) {
	var source *Workload
	var err error
	if owner.InstallID == 0 {
		source, err = getWorkload(db, sourceID)
		if err == nil && (source == nil || source.ProjectID != owner.ProjectID) {
			err = errWorkloadNotFound
		}
	} else {
		source, err = requireOwnedWorkload(db, sourceID, owner)
	}
	if err != nil {
		return "", err
	}
	if source.Status == StatusDestroyed || source.Status == StatusDestroying || workloadTargetID(source) != targetID {
		return "", fmt.Errorf("%w: shared volume must belong to a live workload on the same host", errConflict)
	}
	v, err := findWorkloadVolume(source, name)
	if err != nil {
		return "", err
	}
	return v.DockerVolumeName, nil
}
