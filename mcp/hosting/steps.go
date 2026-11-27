package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// stepEnv is everything a step may touch. Commands only ever go to the
// Instances host of a registered pool machine or of an instance Hosting
// created for this tenant; no tool accepts a raw instance ID or command.
type stepEnv struct {
	op        *Operation
	tenant    *Tenant
	machine   *Machine
	pool      *Pool
	profile   *Profile
	product   *Product
	instance  int64
	platform  string
	serial    string
	mgmt      string
	customer  string
	projectID string
}

func (a *App) loadStepEnv(ctx *sdk.AppCtx, op *Operation) (*stepEnv, error) {
	db := ctx.AppDB()
	env := &stepEnv{op: op, projectID: ctx.CurrentProject(), serial: "-", mgmt: "-"}
	var err error
	if op.TenantID != "" {
		if env.tenant, err = dbTenantGet(db, op.TenantID); err != nil {
			return nil, err
		}
		if env.tenant == nil {
			return nil, fmt.Errorf("tenant %s no longer exists", op.TenantID)
		}
		env.customer = env.tenant.SSHUser
		if env.profile, err = requireProfile(db, env.tenant.ProfileKey); err != nil {
			return nil, err
		}
		if env.product, err = dbProductGet(db, env.profile.ProductKey); err != nil {
			return nil, err
		}
		env.instance = env.tenant.InstanceID
		env.platform = firstNonEmpty(strArg(mapFromAny(env.tenant.Metadata), "platform"), env.profile.onDemand().Platform)
	}
	if op.MachineID > 0 {
		if env.machine, err = requireMachine(db, op.MachineID); err != nil {
			return nil, err
		}
		if env.machine.State == MachineQuarantined && op.Kind != OpInspect && op.Kind != OpClean {
			return nil, errors.New("machine was quarantined by an operator")
		}
		if env.pool, err = requirePool(db, env.machine.PoolKey); err != nil {
			return nil, err
		}
		env.instance = env.machine.InstanceID
		env.platform = env.pool.Platform
		env.serial = firstNonEmpty(env.machine.Serial, "-")
		env.mgmt = firstNonEmpty(env.machine.ManagementUser, "-")
	}
	if env.customer == "" {
		// Inspect and clean check for any hosted account; the access script
		// still needs a well-formed name.
		env.customer = customerUser("inspection:" + op.ID)
	}
	if env.platform == "" {
		env.platform = "linux"
	}
	return env, nil
}

// customerUser is the per-tenant account name on a machine: hst_ + 20 hex.
func customerUser(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "hst_" + hex.EncodeToString(sum[:])[:20]
}

func (a *App) runStep(ctx *sdk.AppCtx, op *Operation, step string) error {
	env, err := a.loadStepEnv(ctx, op)
	if err != nil {
		return err
	}
	if env.pool != nil && !env.pool.ExecutionEnabled && step != "create_instance" {
		return pending("execution is switched off for pool " + env.pool.Key)
	}
	switch step {
	case "create_instance":
		return a.stepCreateInstance(ctx, env)
	case "wait_ready":
		return a.stepWaitReady(ctx, env)
	case "destroy_instance":
		return a.stepDestroyInstance(ctx, env)
	case "prepare_ssh", "preflight", "grant", "verify_access", "revoke", "verify_revoked", "check_clean":
		return a.runAccessStep(ctx, env, step)
	case "check_os":
		return a.stepCheckOS(ctx, env)
	case "revoke_quiet":
		// Releasing an on-demand machine: cut access if the host still
		// answers, but never block destroying it.
		if err := a.runAccessStep(ctx, env, "revoke"); err != nil {
			ctx.Logger().Info("hosting revoke before destroy skipped", "operation_id", op.ID, "err", err)
		}
		return nil
	case "wait_access":
		if err := a.runAccessStep(ctx, env, "verify_access"); err != nil {
			return pending("waiting for the machine to come back: " + err.Error())
		}
		return nil
	case "reboot":
		return a.stepReboot(ctx, env)
	case "install_reset_scripts":
		return a.stepInstallResetScripts(ctx, env)
	case "wipe", "reinstall", "verify_clean":
		return a.stepReset(ctx, env, step)
	case "device_check", "device_open", "device_stop", "device_close", "device_baseline", "device_baseline_capture", "device_clean", "device_verify_clean":
		return a.runDeviceStep(ctx, env, step)
	case "run_workload":
		return a.stepRunWorkload(ctx, env)
	case "stop_workload", "start_workload", "destroy_workload":
		return a.stepWorkload(ctx, env, step)
	default:
		return fmt.Errorf("unknown step %q", step)
	}
}

// ─── Instances calls ─────────────────────────────────────────────────────

type instanceInfo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Platform    string `json:"platform"`
	PublicIPv4  string `json:"public_ipv4"`
	SSHHost     string `json:"ssh_host"`
	SSHPort     int    `json:"ssh_port"`
	DeletableAt string `json:"deletable_at"`
	Error       string `json:"error"`
	// Connected is reported by Instances versions that track whether the
	// host's agent is connected (a switched-off Mac stays "ready").
	Connected  *bool  `json:"connected,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	// ConnectMode is direct, vpn, reverse_tunnel or local (Instances 0.18+).
	ConnectMode string `json:"connect_mode,omitempty"`
	// PublicSSH is the public port customers use to reach a reverse-tunnel
	// machine (Instances 0.19+).
	PublicSSH *publicSSHInfo `json:"public_ssh,omitempty"`
}

type publicSSHInfo struct {
	Enabled        bool     `json:"enabled"`
	Host           string   `json:"host"`
	Port           int      `json:"port"`
	AllowedCIDRs   []string `json:"allowed_cidrs,omitempty"`
	MaxConnections int      `json:"max_connections,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// customerEndpoint is where a customer's SSH client reaches the machine.
// Instances' own ssh_host is how the server reaches it, which for a
// reverse tunnel is 127.0.0.1 and for a VPN machine a private VPN address.
// issue is set (and host empty) when customers have no way in.
func customerEndpoint(inst *instanceInfo) (host string, port int, issue string) {
	switch inst.ConnectMode {
	case "reverse_tunnel":
		ps := inst.PublicSSH
		switch {
		case ps == nil || !ps.Enabled:
			return "", 0, fmt.Sprintf("no public SSH: run instance_public_ssh_set(id=%d, enabled=true) in Instances", inst.ID)
		case ps.Error != "":
			return "", 0, "public SSH error: " + ps.Error
		case ps.Host == "" || ps.Port <= 0:
			return "", 0, "public SSH has no host or port yet"
		}
		return ps.Host, ps.Port, ""
	case "vpn":
		if inst.PublicIPv4 == "" {
			return "", 0, "VPN machine has no public address; set public_host and public_port by hand"
		}
		return inst.PublicIPv4, inst.port(), ""
	}
	host = firstNonEmpty(inst.PublicIPv4, inst.SSHHost)
	if host == "" {
		return "", 0, "Instances reports no address for this machine"
	}
	if isLoopback(host) {
		return "", 0, "Instances reaches this machine through " + host + "; set public_host and public_port by hand"
	}
	return host, inst.port(), ""
}

func isLoopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1"
}

// online uses Instances' connected flag when it has one, else status ready.
func (i *instanceInfo) online() bool {
	if i.Connected != nil {
		return *i.Connected && i.Status == "ready"
	}
	return i.Status == "ready"
}

func (i *instanceInfo) port() int {
	if i.SSHPort > 0 {
		return i.SSHPort
	}
	return 22
}

func instanceGet(ctx *sdk.AppCtx, projectID string, id int64) (*instanceInfo, error) {
	var out struct {
		Instance instanceInfo `json:"instance"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_get", withProject(projectID, map[string]any{"id": id}), &out); err != nil {
		return nil, err
	}
	if out.Instance.ID == 0 && id != 0 {
		return nil, fmt.Errorf("instance %d not found", id)
	}
	return &out.Instance, nil
}

type commandResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error"`
}

func runCommand(ctx *sdk.AppCtx, env *stepEnv, cmd string, timeoutS int) (*commandResult, error) {
	if env.instance <= 0 {
		return nil, errors.New("no machine to run on")
	}
	var out commandResult
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_run_command", withProject(env.projectID, map[string]any{
		"id": env.instance, "cmd": cmd, "timeout_s": timeoutS,
	}), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func uploadFile(ctx *sdk.AppCtx, env *stepEnv, path string, content []byte) error {
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("instances", "instance_upload_file", withProject(env.projectID, map[string]any{
		"id": env.instance, "path": path, "content_b64": base64.StdEncoding.EncodeToString(content),
	}), &out)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// asRoot runs a script as root whether the Instances SSH user is root
// (typical on fresh VPSes) or a management user with passwordless sudo.
func asRoot(script string) string {
	q := shQuote(script)
	return `if [ "$(id -u)" = 0 ]; then /bin/sh -c ` + q + `; else sudo -n /bin/sh -c ` + q + `; fi`
}

func secureDir(platform string) string {
	if platform == "macos" {
		return "/var/root/.apteva-hosting"
	}
	return "/root/.apteva-hosting"
}

func shaCommand(platform, path string) string {
	if platform == "macos" {
		return "/usr/bin/shasum -a 256 " + path + " | /usr/bin/awk '{print $1}'"
	}
	return "sha256sum " + path + " | awk '{print $1}'"
}

func marker(opID, step string) string { return "HOSTING_OK:" + opID + ":" + step }

// runAccessStep uploads the platform's access script, moves it into a
// root-owned directory, checks its hash and runs one step of it.
func (a *App) runAccessStep(ctx *sdk.AppCtx, env *stepEnv, step string) error {
	script, err := accessScript(env.platform)
	if err != nil {
		return err
	}
	keys := "-"
	if step == "grant" {
		if env.tenant == nil || len(env.tenant.AccessKeys) == 0 {
			return errors.New("no SSH keys to grant")
		}
		keys = base64.StdEncoding.EncodeToString([]byte(strings.Join(env.tenant.AccessKeys, "\n") + "\n"))
	}
	tmp := fmt.Sprintf("/tmp/.apteva-hosting-%s-%d.sh", env.op.ID, env.op.Step)
	if err := uploadFile(ctx, env, tmp, script); err != nil {
		return fmt.Errorf("upload access script: %w", err)
	}
	dir := secureDir(env.platform)
	pinned := dir + "/" + env.op.ID + ".sh"
	scriptStep := step
	if step == "prepare_ssh" {
		scriptStep = "prepare"
	}
	args := strings.Join([]string{shQuote(scriptStep), shQuote(env.customer), shQuote(env.op.ID), shQuote(env.serial), shQuote(keys), shQuote(env.mgmt)}, " ")
	cmd := asRoot(fmt.Sprintf(`set -eu; test ! -L %[1]s; install -d -m 700 %[1]s; cp %[2]s %[3]s; rm -f %[2]s; chmod 600 %[3]s; test "$(%[4]s)" = %[5]s; /bin/sh %[3]s %[6]s`,
		dir, shQuote(tmp), pinned, shaCommand(env.platform, pinned), sha256Hex(script), args))
	res, err := runCommand(ctx, env, cmd, 60)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, marker(env.op.ID, scriptStep)) {
		return fmt.Errorf("%s: %s (exit %d): %s", step, accessExitReason(res.ExitCode), res.ExitCode, tail(firstNonEmpty(res.Output, res.Error), 300))
	}
	return nil
}

// accessExitReason explains the exit codes of scripts/<platform>/customer-access.sh.
func accessExitReason(code int) string {
	if r, ok := map[int]string{
		10:  "the script must run as root",
		11:  "invalid customer account name",
		12:  "invalid customer account name",
		13:  "the customer account would be the management account",
		14:  "the machine's serial number does not match the machine record",
		15:  "the management account is missing or is root",
		16:  "the SSH key folder is a symlink",
		17:  "the customer key file is a symlink",
		20:  "no SSH keys given",
		21:  "the customer account has a system user id",
		22:  "the SSH keys could not be decoded",
		24:  "sshd_config does not include sshd_config.d",
		25:  "the Hosting SSH profile is a symlink",
		26:  "the Hosting SSH profile breaks the SSH configuration (removed)",
		27:  "a customer home folder is still on the machine",
		28:  "a customer SSH key is still on the machine",
		30:  "unknown step",
		31:  "the customer account is disabled",
		32:  "the customer SSH key file is missing or has the wrong owner or mode",
		33:  "customer processes are still running",
		34:  "a customer account is still on the machine",
		255: "sshd rejected the SSH configuration",
	}[code]; ok {
		return r
	}
	return "unexpected failure"
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func (a *App) stepReboot(ctx *sdk.AppCtx, env *stepEnv) error {
	m := marker(env.op.ID, "reboot")
	var script string
	if env.platform == "macos" {
		script = fmt.Sprintf(`/bin/launchctl submit -l com.apteva.hosting.restart.%s -- /bin/sh -c 'sleep 5; /sbin/shutdown -r now'; printf '%s\n'`, env.op.ID, m)
	} else {
		script = fmt.Sprintf(`nohup /bin/sh -c 'sleep 5; shutdown -r now' >/dev/null 2>&1 & printf '%s\n'`, m)
	}
	res, err := runCommand(ctx, env, asRoot(script), 30)
	if err != nil {
		return err
	}
	if !strings.Contains(res.Output, m) {
		return fmt.Errorf("reboot was not acknowledged: %s", tail(res.Output, 300))
	}
	return nil
}

// stepInstallResetScripts installs the shipped macOS reset helpers as
// root:700 and checks their hashes on the machine.
func (a *App) stepInstallResetScripts(ctx *sdk.AppCtx, env *stepEnv) error {
	if env.platform != "macos" {
		return nil // Linux pools have no reset script set yet.
	}
	for _, name := range []string{"wipe", "reinstall", "verify-clean"} {
		body, err := resetScript(name)
		if err != nil {
			return err
		}
		tmp := fmt.Sprintf("/tmp/.apteva-hosting-%s-%s", env.op.ID, name)
		if err := uploadFile(ctx, env, tmp, body); err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
		dest := resetScriptDir + "/" + name
		m := marker(env.op.ID, "install_"+name)
		cmd := asRoot(fmt.Sprintf(`set -eu; test "$(%[1]s)" = %[2]s; /usr/bin/install -d -o root -g wheel -m 755 %[3]s; test ! -L %[4]s; /usr/bin/install -o root -g wheel -m 700 %[5]s %[4]s; rm -f %[5]s; test "$(%[6]s)" = %[2]s; printf '%[7]s\n'`,
			shaCommand("macos", shQuote(tmp)), sha256Hex(body), resetScriptDir, dest, shQuote(tmp), shaCommand("macos", dest), m))
		res, err := runCommand(ctx, env, cmd, 60)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 || !strings.Contains(res.Output, m) {
			return fmt.Errorf("install %s exited %d: %s", name, res.ExitCode, tail(firstNonEmpty(res.Output, res.Error), 300))
		}
	}
	return nil
}

// stepReset runs an installed, hash-pinned reset helper. Erase and reinstall
// only schedule Apple's erase/install; verify_clean is the proof and is
// polled until the receipt checks out.
func (a *App) stepReset(ctx *sdk.AppCtx, env *stepEnv, step string) error {
	if env.machine == nil || env.platform != "macos" {
		return errors.New("automatic reset is only available for macOS pool machines")
	}
	if step != "verify_clean" && !env.machine.Validated {
		return errors.New("machine is not validated; erase is refused until its recovery has been tested")
	}
	name := resetScriptNames[step]
	sha := env.machine.ScriptHashes[name]
	if len(sha) != 64 {
		return fmt.Errorf("no pinned hash for %s; run inspect first", name)
	}
	path := resetScriptDir + "/" + name
	pinned := secureDir("macos") + "/" + env.op.ID + "-" + name
	m := marker(env.op.ID, step)
	cmd := asRoot(fmt.Sprintf(`set -eu; test ! -L %[1]s; test "$(/usr/bin/stat -f '%%Su:%%Lp' %[1]s)" = root:700; /usr/bin/install -d -m 700 %[2]s; /bin/cp %[1]s %[3]s; /bin/chmod 700 %[3]s; test "$(%[4]s)" = %[5]s; %[3]s %[6]s %[7]s %[8]s`,
		path, secureDir("macos"), pinned, shaCommand("macos", pinned), sha, shQuote(env.op.ID), shQuote(env.customer), shQuote(env.machine.Serial)))
	res, err := runCommand(ctx, env, cmd, 60)
	if step == "verify_clean" {
		// The Mac is erasing, rebooting or re-enrolling: keep polling.
		if err != nil {
			return pending("Mac unreachable: still erasing or restarting, or stopped at the setup screens (it needs device-management enrollment to come back on its own): " + err.Error())
		}
		if res.ExitCode != 0 || !strings.Contains(res.Output, m) {
			return pending(fmt.Sprintf("%s (verify-clean exit %d)", resetExitReason(step, res.ExitCode), res.ExitCode))
		}
		return nil
	}
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, m) {
		return fmt.Errorf("%s (%s exit %d): %s", resetExitReason(step, res.ExitCode), name, res.ExitCode, tail(firstNonEmpty(res.Output, res.Error), 300))
	}
	return nil
}

// resetExitReason explains the exit codes of scripts/macos/wipe.sh,
// reinstall.sh and verify-clean.sh in plain words for the run log and
// hosting.operation.failed.
func resetExitReason(step string, code int) string {
	common := map[int]string{
		10: "the script must run as root on macOS",
		11: "invalid operation id",
		12: "invalid operation id",
		13: "the Mac's serial number does not match the machine record",
	}
	byStep := map[int]string{
		14: "recovery setup missing: /var/root/.apteva-hosting/recovery.plist is absent or not root-only",
		15: "recovery setup not marked validated (RecoveryValidated) on the Mac",
		16: "the management password file is missing or not root-only",
		17: "the macOS installer app is missing",
		18: "the recovery helper /usr/local/libexec/apteva-hosting/recovery-handoff is missing or not root-only",
		19: "the recovery helper does not match RecoveryHandoffSHA256",
		20: "the macOS installer's code signature is invalid",
		21: "Gatekeeper rejected the macOS installer",
		22: "the management user has no secure token (needed to erase Apple silicon)",
		23: "the recovery helper failed (its exit code is in the output)",
	}
	if step == "verify_clean" {
		byStep = map[int]string{
			14: "no receipt yet: the erase or the device-management re-enrollment has not finished",
			15: "the receipt is for another operation or Mac",
			16: "device-management re-enrollment not verified in the receipt",
			17: "the data volume did not change: the erase has not happened yet",
			18: "the data volume differs from the one in the receipt",
			19: "the macOS version differs from the one in the receipt",
			20: "a customer home folder is still on the Mac",
			21: "a customer SSH key is still on the Mac",
			22: "a customer account is still on the Mac",
			23: "sshd rejected the SSH configuration",
		}
	}
	if r, ok := common[code]; ok {
		return r
	}
	if r, ok := byStep[code]; ok {
		return r
	}
	return "unexpected failure"
}

// ─── On-demand instances ─────────────────────────────────────────────────

func instanceName(t *Tenant) string { return "hosting-" + t.ID }

func (a *App) stepCreateInstance(ctx *sdk.AppCtx, env *stepEnv) error {
	db := ctx.AppDB()
	t := env.tenant
	if t == nil {
		return errors.New("create_instance needs a tenant")
	}
	if t.InstanceID > 0 {
		return nil
	}
	// A run that lost its lease may already have created the instance;
	// adopt it by name instead of buying a second one.
	var list struct {
		Instances []instanceInfo `json:"instances"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_list", withProject(env.projectID, map[string]any{}), &list); err == nil {
		for _, inst := range list.Instances {
			if inst.Name == instanceName(t) && inst.Status != "destroyed" {
				return a.adoptInstance(db, env, &inst)
			}
		}
	}
	s := env.profile.onDemand()
	input := map[string]any{
		"name":      instanceName(t),
		"provider":  s.Provider,
		"size":      s.ServerType,
		"tags_json": jsonOrEmpty(map[string]any{"apteva.hosting.tenant_id": t.ID, "apteva.hosting.profile_key": t.ProfileKey}, "{}"),
	}
	if s.ProviderConnectionID > 0 {
		input["provider_connection_id"] = s.ProviderConnectionID
	}
	if s.Region != "" {
		input["region"] = s.Region
	}
	if s.Image != "" {
		input["image"] = s.Image
	}
	if len(s.Setup) > 0 {
		input["setup"] = s.Setup
	}
	var out struct {
		Instance instanceInfo `json:"instance"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_create", withProject(env.projectID, input), &out); err != nil {
		return fmt.Errorf("instance_create: %w", err)
	}
	if out.Instance.ID == 0 {
		return errors.New("instance_create returned no instance id")
	}
	return a.adoptInstance(db, env, &out.Instance)
}

func (a *App) adoptInstance(db querier, env *stepEnv, inst *instanceInfo) error {
	env.instance = inst.ID
	env.tenant.InstanceID = inst.ID
	if _, err := db.Exec(`UPDATE hosting_tenants SET instance_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, inst.ID, env.tenant.ID); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE hosting_operations SET instance_id=? WHERE id=?`, inst.ID, env.op.ID)
	return err
}

func (a *App) stepWaitReady(ctx *sdk.AppCtx, env *stepEnv) error {
	inst, err := instanceGet(ctx, env.projectID, env.instance)
	if err != nil {
		return pending(err.Error())
	}
	switch inst.Status {
	case "ready":
	case "error", "destroyed":
		return fmt.Errorf("instance %d is %s: %s", inst.ID, inst.Status, inst.Error)
	default:
		return pending("instance is " + firstNonEmpty(inst.Status, "starting"))
	}
	platform := firstNonEmpty(env.profile.onDemand().Platform, inst.Platform, "linux")
	host, port, issue := customerEndpoint(inst)
	if issue != "" {
		return fmt.Errorf("instance %d is ready but customers cannot reach it: %s", inst.ID, issue)
	}
	return dbTenantMergeMetadata(ctx.AppDB(), env.tenant.ID, map[string]any{
		"platform":    platform,
		"access_host": host,
		"access_port": port,
	})
}

func (a *App) stepDestroyInstance(ctx *sdk.AppCtx, env *stepEnv) error {
	if env.instance <= 0 {
		return nil
	}
	inst, err := instanceGet(ctx, env.projectID, env.instance)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil
		}
		return pending(err.Error())
	}
	if inst.Status == "destroyed" {
		return nil
	}
	if at, ok := parseSQLTime(inst.DeletableAt); ok && at.After(now()) {
		_ = dbTenantMergeMetadata(ctx.AppDB(), env.tenant.ID, map[string]any{"destroy_scheduled_at": sqlTime(at)})
		return pendingUntil("provider minimum allocation until "+sqlTime(at), at.Add(time.Minute))
	}
	var out struct {
		Destroyed bool   `json:"destroyed"`
		Status    string `json:"status"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_destroy", withProject(env.projectID, map[string]any{"id": env.instance}), &out); err != nil {
		return fmt.Errorf("instance_destroy: %w", err)
	}
	if !out.Destroyed {
		return pending("destroy is " + firstNonEmpty(out.Status, "in progress"))
	}
	return nil
}

// ─── Containers on a machine ─────────────────────────────────────────────

func (a *App) stepRunWorkload(ctx *sdk.AppCtx, env *stepEnv) error {
	t := env.tenant
	if t == nil || env.product == nil || !env.product.runsContainer() || t.WorkloadID != "" {
		return nil
	}
	input := containerRunInput(t, env.profile, env.product, mapFromAny(mapFromAny(t.Metadata)["runtime_config"]), env.instance)
	name := strFromAny(input["name"])
	var out workloadResp
	if boolFromAny(mapFromAny(t.Metadata)["workload_run_started"], false) {
		// An earlier attempt may have started the container before it could
		// save the id: adopt it by name instead of starting a second one.
		if w := findWorkloadByName(ctx, name); w != nil {
			out.Workload = *w
		}
	}
	if out.Workload.ID == "" {
		if err := dbTenantMergeMetadata(ctx.AppDB(), t.ID, map[string]any{"workload_run_started": true}); err != nil {
			return err
		}
		if err := ctx.PlatformAPI().CallAppResult("containers", "containers_run", input, &out); err != nil {
			if w := findWorkloadByName(ctx, name); w != nil {
				out.Workload = *w
			} else {
				return fmt.Errorf("containers_run: %w", err)
			}
		}
	}
	if out.Workload.ID == "" {
		return errors.New("containers_run returned no workload id")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE hosting_tenants SET workload_id=?, last_health_status=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		out.Workload.ID, firstNonEmpty(out.Workload.HealthStatus, out.Workload.Status, "unknown"), t.ID); err != nil {
		return err
	}
	if target := workloadTarget(&out.Workload); target != "" {
		_ = dbTenantMergeMetadata(ctx.AppDB(), t.ID, map[string]any{"workload_url": target})
	}
	return nil
}

func (a *App) stepWorkload(ctx *sdk.AppCtx, env *stepEnv, step string) error {
	if env.tenant == nil || env.tenant.WorkloadID == "" {
		return nil
	}
	tool := map[string]string{"stop_workload": "containers_stop", "start_workload": "containers_start", "destroy_workload": "containers_destroy"}[step]
	input := map[string]any{"workload_id": env.tenant.WorkloadID}
	if step == "destroy_workload" {
		input["delete_volumes"] = true
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("containers", tool, input, &out); err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if step == "destroy_workload" {
		_, _ = ctx.AppDB().Exec(`UPDATE hosting_tenants SET workload_id='', updated_at=CURRENT_TIMESTAMP WHERE id=?`, env.tenant.ID)
	}
	return nil
}

// findWorkloadByName returns Hosting's live workload with this name, if any.
func findWorkloadByName(ctx *sdk.AppCtx, name string) *containerWorkload {
	if name == "" {
		return nil
	}
	var list struct {
		Workloads []containerWorkload `json:"workloads"`
	}
	if err := ctx.PlatformAPI().CallAppResult("containers", "containers_list", map[string]any{"limit": 500}, &list); err != nil {
		return nil
	}
	for i := range list.Workloads {
		w := list.Workloads[i]
		if w.Name == name && w.Status != "destroyed" && w.Status != "deleted" {
			return &w
		}
	}
	return nil
}

// stepCheckOS compares the machine's OS version with its expected_os (a
// prefix such as "15" or "15.3" on macOS, "ubuntu 24.04" on Linux). Machines
// without expected_os skip it.
func (a *App) stepCheckOS(ctx *sdk.AppCtx, env *stepEnv) error {
	if env.machine == nil {
		return nil
	}
	if env.platform == "macos" {
		if err := a.checkActivationLock(ctx, env); err != nil {
			return err
		}
	}
	if strings.TrimSpace(env.machine.ExpectedOS) == "" {
		return nil
	}
	cmd := `/usr/bin/sw_vers -productVersion`
	if env.platform != "macos" {
		cmd = `. /etc/os-release && printf '%s %s\n' "$ID" "$VERSION_ID"`
	}
	res, err := runCommand(ctx, env, cmd, 30)
	if err != nil {
		return err
	}
	got := strings.TrimSpace(res.Output)
	if res.ExitCode != 0 || got == "" {
		return fmt.Errorf("could not read the OS version (exit %d): %s", res.ExitCode, tail(firstNonEmpty(res.Output, res.Error), 200))
	}
	if !osMatches(got, env.machine.ExpectedOS) {
		return fmt.Errorf("machine runs %q, expected %q", got, env.machine.ExpectedOS)
	}
	return nil
}

// checkActivationLock refuses a Mac whose Activation Lock is on: after an
// erase it would stop at the lock screen until the Apple Account that owns
// it signs in. Macs that do not report the status (older models) pass.
func (a *App) checkActivationLock(ctx *sdk.AppCtx, env *stepEnv) error {
	res, err := runCommand(ctx, env, asRoot(`/usr/sbin/system_profiler SPHardwareDataType 2>/dev/null | /usr/bin/awk -F': ' '/Activation Lock Status/{print $2}'`), 60)
	if err != nil {
		return err
	}
	if status := strings.TrimSpace(res.Output); strings.EqualFold(status, "enabled") {
		return errors.New("Activation Lock is enabled: turn off Find My (or clear the lock in Apple Business Manager) before selling this Mac")
	}
	return nil
}

func osMatches(got, expected string) bool {
	got, expected = strings.ToLower(strings.TrimSpace(got)), strings.ToLower(strings.TrimSpace(expected))
	return got == expected || strings.HasPrefix(got, expected+".")
}
