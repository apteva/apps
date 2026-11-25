package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var sshUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)

func registerHost(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if id := int64Arg(args, "id"); id > 0 {
		for _, key := range []string{"name", "ssh_host", "ssh_user", "ssh_port", "setup", "vpn", "connect", "platform", "public_ssh", "public_ssh_allowed_cidrs"} {
			if _, ok := args[key]; ok {
				return nil, errors.New("resume with id alone; use instance_wait_ready to change setup")
			}
		}
		inst, err := dbGetInstance(ctx.AppDB(), id)
		if err != nil {
			return nil, err
		}
		if inst.Provider != "external" {
			return nil, errors.New("only external hosts can resume enrollment")
		}
		return enrollmentResponse(ctx, inst)
	}
	options, err := parseSetup(args)
	if err != nil {
		return nil, err
	}
	if options == nil {
		options = &SetupOptions{}
	}

	name := strings.TrimSpace(strArg(args, "name"))
	host := strings.TrimSpace(strArg(args, "ssh_host"))
	user := strings.TrimSpace(strArg(args, "ssh_user"))
	port := intArg(args, "ssh_port", 22)
	connect, platform := strings.TrimSpace(strArg(args, "connect")), strings.TrimSpace(strArg(args, "platform"))
	if vpn, _ := args["vpn"].(bool); vpn {
		if connect != "" && connect != "vpn" {
			return nil, errors.New("vpn=true conflicts with connect; use connect alone")
		}
		connect = "vpn"
	}
	if connect == "" {
		connect = "direct"
	}
	switch connect {
	case "direct", "vpn", "reverse_tunnel":
	default:
		return nil, errors.New("connect must be direct, vpn or reverse_tunnel")
	}
	if platform != "" && platform != "linux" && platform != "macos" {
		return nil, errors.New("platform must be linux or macos")
	}
	if platform == "macos" && connect == "vpn" {
		return nil, errors.New("VPN enrollment supports Ubuntu/Debian; register Macs behind a router with connect=reverse_tunnel")
	}
	if options.Docker && platform == "macos" {
		return nil, errors.New(macOSDockerUnsupported)
	}
	if options.Apps && platform == "macos" {
		return nil, errors.New(macOSAppsUnsupported)
	}
	var publicSSH *publicSSHOptions
	if want, _ := args["public_ssh"].(bool); want {
		if connect != "reverse_tunnel" {
			return nil, errors.New("public_ssh applies to connect=reverse_tunnel; machines registered directly are already reachable at their own address")
		}
		opts, err := parsePublicSSHOptions(args["public_ssh_allowed_cidrs"], args["public_ssh_max_connections"])
		if err != nil {
			return nil, err
		}
		if _, err = loadPublicSSHConfig(ctx.Config()); err != nil {
			return nil, err
		}
		publicSSH = &opts
	} else if _, ok := args["public_ssh_allowed_cidrs"]; ok {
		return nil, errors.New("public_ssh_allowed_cidrs needs public_ssh=true")
	}
	if name == "" || user == "" || (connect == "direct" && host == "") {
		return nil, errors.New("name, ssh_user, and ssh_host (unless connect is vpn or reverse_tunnel) are required")
	}
	if !sshUserPattern.MatchString(user) {
		return nil, errors.New("ssh_user must be a valid POSIX account name")
	}
	if strings.ContainsAny(host, " /\\\t\r\n") || strings.HasPrefix(host, "-") {
		return nil, errors.New("ssh_host must be a hostname or IP address")
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("ssh_port must be between 1 and 65535")
	}
	var tunnelConfig reverseTunnelConfig
	knownHosts := ""
	switch connect {
	case "vpn":
		if port != 22 {
			return nil, errors.New("VPN enrollment currently uses SSH port 22")
		}
		if _, err = checkedVPNStatus(ctx); err != nil {
			return nil, err
		}
		host = ""
	case "reverse_tunnel":
		// ssh_port is the machine's own SSH port; the tunnel carries it to a
		// loopback port on this server, which Instances then connects to.
		if host != "" {
			return nil, errors.New("ssh_host is not used with reverse_tunnel: the machine connects to this server")
		}
		if tunnelConfig, err = loadReverseTunnelConfig(ctx.Config()); err != nil {
			return nil, err
		}
		if knownHosts, err = ensureReverseTunnelServer(); err != nil {
			return nil, err
		}
		host = "127.0.0.1"
	}
	privateKey, publicKey, err := generateSSHKeypair()
	if err != nil {
		return nil, err
	}
	providerID := net.JoinHostPort(host, fmt.Sprint(port))
	switch connect {
	case "vpn":
		providerID = fmt.Sprintf("enrollment:%x", sha256.Sum256([]byte(publicKey)))
	case "reverse_tunnel":
		providerID = fmt.Sprintf("reverse-tunnel:%x", sha256.Sum256([]byte(publicKey)))
	}
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: name, Provider: "external", ProviderID: providerID, Status: "provisioning", SSHHost: host, SSHPort: port, SSHUser: user, SSHPrivateKey: privateKey, SSHPublicKey: publicKey, TagsJSON: strArg(args, "tags_json"), Setup: options, Platform: platform, ConnectMode: connect})
	if err != nil {
		return nil, err
	}
	switch connect {
	case "vpn":
		fingerprint := sha256.Sum256([]byte(publicKey))
		inst.EnrollmentPeer = fmt.Sprintf("apteva-instance-%d-%x", inst.ID, fingerprint[:6])
		if err = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"enrollment_peer": inst.EnrollmentPeer}); err != nil {
			return nil, err
		}
	case "reverse_tunnel":
		t, e := createReverseTunnel(ctx.AppDB(), tunnelConfig, inst.ID, port, knownHosts)
		if e == nil {
			inst.SSHPort = t.Port
			e = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"ssh_port": t.Port})
		}
		if e == nil {
			e = syncReverseTunnelKeys(ctx.AppDB())
		}
		if e == nil && publicSSH != nil {
			// The setup command makes the machine key-only before its tunnel
			// starts, so no check over SSH is needed here.
			_, e = enablePublicSSH(ctx, inst, *publicSSH, false)
		}
		if e != nil {
			// Nothing was handed to the machine yet: undo the registration.
			forgetReverseTunnel(ctx.AppDB(), inst.ID)
			_ = dbDeleteInstance(ctx.AppDB(), inst.ID)
			_ = removeSSHCredential(ctx.AppDB(), inst.SSHCredentialRef)
			return nil, e
		}
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)
	result, err := enrollmentResponse(ctx, inst)
	if err != nil {
		return nil, fmt.Errorf("instance %d registered; resume with instance_register(id=%d): %w", inst.ID, inst.ID, err)
	}
	return result, nil
}

type vpnStatus struct {
	Installed   bool   `json:"installed"`
	Backend     string `json:"backend"`
	NetworkCIDR string `json:"network_cidr"`
}

func checkedVPNStatus(ctx *sdk.AppCtx) (*vpnStatus, error) {
	binding := ctx.IntegrationFor("vpn")
	if binding == nil || binding.Kind != "app" || ctx.PlatformAPI() == nil {
		return nil, errors.New("bind the VPN app to Instances and install its VPN server first")
	}
	var status vpnStatus
	if err := ctx.PlatformAPI().CallAppResult("vpn", "vpn_status", map[string]any{}, &status); err != nil {
		return nil, err
	}
	if !status.Installed {
		return nil, errors.New("install the VPN server using vpn_install before enrolling home servers")
	}
	if status.Backend != "wireguard" {
		return nil, fmt.Errorf("home enrollment supports WireGuard; VPN backend is %q", status.Backend)
	}
	if _, _, err := net.ParseCIDR(status.NetworkCIDR); err != nil {
		return nil, errors.New("VPN returned an invalid network CIDR")
	}
	return &status, nil
}
func enrollmentResponse(ctx *sdk.AppCtx, inst *Instance) (any, error) {
	unlock, err := lockResource(ctx.AppDB(), "instance", inst.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	inst, err = dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return nil, err
	}
	if inst.Status == "destroying" || inst.Status == "rolling_back" {
		return nil, ErrOperationSuperseded
	}

	vpnConfig := ""
	if inst.EnrollmentPeer != "" {
		status, err := checkedVPNStatus(ctx)
		if err != nil {
			return nil, err
		}
		var peers struct {
			Peers []struct {
				Name      string `json:"name"`
				RevokedAt int64  `json:"revoked_at"`
			} `json:"peers"`
		}
		if err = ctx.PlatformAPI().CallAppResult("vpn", "vpn_peer_list", map[string]any{"include_revoked": true}, &peers); err != nil {
			return nil, err
		}
		tool := "vpn_peer_add"
		args := map[string]any{"name": inst.EnrollmentPeer, "allowed_ips": status.NetworkCIDR, "keepalive": 25}
		for _, peer := range peers.Peers {
			if peer.Name == inst.EnrollmentPeer {
				if peer.RevokedAt > 0 {
					return nil, errors.New("VPN peer was revoked; register a new host")
				}
				tool = "vpn_peer_config"
				args = map[string]any{"name": inst.EnrollmentPeer}
				break
			}
		}
		var peer struct {
			Address string `json:"address"`
			Config  string `json:"config"`
		}
		if err = ctx.PlatformAPI().CallAppResult("vpn", tool, args, &peer); err != nil {
			return nil, err
		}
		ip := strings.Split(peer.Address, "/")[0]
		if net.ParseIP(ip) == nil {
			return nil, errors.New("VPN returned an invalid peer address")
		}
		vpnConfig, err = safeWireGuardConfig(peer.Config, status.NetworkCIDR)
		if err != nil {
			return nil, err
		}
		inst.SSHHost = ip
		if err = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"ssh_host": ip, "provider_id": net.JoinHostPort(ip, fmt.Sprint(inst.SSHPort))}); err != nil {
			return nil, err
		}
	}
	var tunnel *tunnelEnrollment
	if inst.ConnectMode == "reverse_tunnel" {
		t, err := getReverseTunnel(ctx.AppDB(), inst.ID)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, errors.New("this registration's reverse tunnel was removed; register the machine again")
		}
		// Re-assert the server side: setup and keys are idempotent.
		if _, err = ensureReverseTunnelServer(); err != nil {
			return nil, err
		}
		if err = syncReverseTunnelKeys(ctx.AppDB()); err != nil {
			return nil, err
		}
		if tunnel, err = reverseTunnelEnrollment(ctx.AppDB(), t); err != nil {
			return nil, err
		}
	}
	command, err := hostEnrollmentCommand(inst, vpnConfig, tunnel)
	if err != nil {
		return nil, err
	}
	authorization := map[string]any{"ssh_user": inst.SSHUser, "ssh_host": inst.SSHHost, "ssh_port": inst.SSHPort, "public_key": inst.SSHPublicKey, "command": command, "contains_credentials": vpnConfig != "" || tunnel != nil, "connect": inst.ConnectMode, "next_step": enrollmentNextStep(inst)}
	if tunnel != nil {
		authorization["tunnel_port"] = tunnel.Port
		authorization["tunnel_server"] = net.JoinHostPort(tunnel.ServerHost, fmt.Sprint(tunnel.ServerPort))
	}
	authorization["host_key_check"] = "The command prints the machine's SSH host key fingerprints; pass that value to instance_wait_ready as expected_host_key so Instances verifies it instead of trusting the first key it sees."
	if tunnel != nil && tunnel.KeysOnly {
		authorization["public_ssh_note"] = "Public SSH is on: the command also makes this machine's SSH server accept keys only."
	}
	return map[string]any{"instance": inst.stripSecrets(), "authorization": authorization}, nil
}

// Never execute wg-quick shell hooks from a peer configuration. Route only
// the VPN subnet, leaving the home machine's internet routing and DNS intact.
func safeWireGuardConfig(config, network string) (string, error) {
	if strings.TrimSpace(config) == "" {
		return "", errors.New("VPN returned empty configuration")
	}
	out := []string{}
	section := ""
	keys := map[string]bool{}
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[Interface]" || line == "[Peer]" {
			section = line
			out = append(out, line)
			continue
		}
		pair := strings.SplitN(line, "=", 2)
		if len(pair) != 2 {
			return "", errors.New("invalid WireGuard configuration")
		}
		key, value := strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
		if key == "DNS" {
			continue
		}
		valid := (section == "[Interface]" && (key == "PrivateKey" || key == "Address" || key == "MTU")) || (section == "[Peer]" && (key == "PublicKey" || key == "PresharedKey" || key == "Endpoint" || key == "AllowedIPs" || key == "PersistentKeepalive"))
		if !valid {
			return "", fmt.Errorf("unsupported WireGuard setting %q", key)
		}
		if key == "AllowedIPs" {
			value = network
		}
		keys[key] = true
		out = append(out, key+" = "+value)
	}
	for _, key := range []string{"PrivateKey", "Address", "PublicKey", "Endpoint", "AllowedIPs"} {
		if !keys[key] {
			return "", fmt.Errorf("WireGuard configuration missing %s", key)
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}
func hostEnrollmentCommand(inst *Instance, vpnConfig string, tunnel *tunnelEnrollment) (string, error) {
	if !sshUserPattern.MatchString(inst.SSHUser) {
		return "", errors.New("unsupported SSH account name for enrollment command")
	}
	elevated := inst.Setup != nil && (inst.Setup.Requested.Baseline || inst.Setup.Requested.Docker || inst.Setup.Requested.Runtimes || inst.Setup.Requested.Apps)
	localSSHPort := inst.SSHPort
	if tunnel != nil {
		localSSHPort = tunnel.LocalSSHPort
	}
	linux, err := linuxEnrollmentBody(inst, elevated, vpnConfig)
	if err != nil {
		return "", err
	}
	// One command for every supported OS: it detects macOS or Linux itself.
	script := enrollmentPrelude + "user=" + quoteShellArg(inst.SSHUser) + "\npublic_key=" + quoteShellArg(inst.SSHPublicKey) + "\n" +
		"os_name=$(uname -s)\nif [ \"$os_name\" = Darwin ]; then\n" + macEnrollmentBody(inst, elevated, vpnConfig != "", localSSHPort) + "else\n" + linux + "fi\n"
	if tunnel != nil && tunnel.KeysOnly {
		// Before the tunnel starts, so the public port never reaches an SSH
		// server that accepts passwords.
		script += "# Public SSH: this machine's SSH server must accept keys only.\nSUDO=\"\"\nsshd_bin=$(command -v sshd || echo /usr/sbin/sshd)\n" + keysOnlySSHDBody
	}
	if tunnel != nil {
		script += tunnelEnrollmentBody(tunnel)
	}
	script += printHostKeyFingerprint
	script += "printf '%s\\n' 'SSH authorized. Instances will verify connectivity and complete the selected setup.'\n"
	return "sudo sh <<'APTEVA_HOST_ENROLLMENT'\n" + script + "APTEVA_HOST_ENROLLMENT", nil
}

const macOSDockerUnsupported = "Docker setup isn't available on macOS: install Docker Desktop yourself, or use a Linux machine for Docker"

func enrollmentNextStep(inst *Instance) string {
	run := "Run command on the machine as an administrator (Ubuntu/Debian or macOS; the command detects which). "
	if inst.Platform == "macos" {
		run = "Run command in Terminal on the Mac as an administrator. If macOS refuses to turn on Remote Login, turn it on once in System Settings > General > Sharing, or give Terminal Full Disk Access, then run the command again. "
	}
	switch inst.ConnectMode {
	case "reverse_tunnel":
		run += "It installs a background job that keeps a tunnel to this server open, so the machine needs only outbound internet access. "
	case "vpn":
		run += "It also configures the WireGuard VPN peer. "
	default:
		run += "Alternatively add public_key to the SSH user's authorized_keys. "
	}
	return run + "The command contains credentials when it sets up a VPN or tunnel; do not share it. Then call instance_wait_ready; instance_get reports setup progress."
}

func (a *App) httpRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST")
		return
	}
	var args map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&args); err != nil {
		httpErr(w, 400, "invalid JSON")
		return
	}
	result, err := registerHost(appCtxForRequest(r), args)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, result)
}
func waitReadyWithOptions(work context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	options, err := parseSetup(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	retry, _ := args["retry"].(bool)
	if retry || options != nil {
		if err = retryInstanceSetup(ctx, id, options); err != nil {
			return nil, err
		}
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if expected := strArg(args, "expected_host_key"); expected != "" {
		if err = expectHostKey(ctx.AppDB(), inst, expected); err != nil {
			return nil, err
		}
	}
	if inst.Provider == "external" && inst.Status == "provisioning" && inst.SSHHost != "" {
		kickExternalReadiness(ctx, id)
	}
	async, _ := args["async"].(bool)
	if !async {
		inst, err = waitInstanceReady(work, ctx, id, time.Duration(intArg(args, "timeout_s", 300))*time.Second)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"ready": inst.Status == "ready", "id": inst.ID, "status": inst.Status, "instance": inst.stripSecrets()}, nil
}

// Only revoke peers created by this enrollment flow. Unmanaged external hosts
// retain the original forget-only destroy behavior.
func revokeEnrollmentPeer(ctx *sdk.AppCtx, inst *Instance) error {
	if inst.EnrollmentPeer == "" {
		return nil
	}
	if ctx.PlatformAPI() == nil {
		return errors.New("VPN connection required to revoke the enrollment peer")
	}
	var peers struct {
		Peers []struct {
			Name      string `json:"name"`
			RevokedAt int64  `json:"revoked_at"`
		} `json:"peers"`
	}
	if err := ctx.PlatformAPI().CallAppResult("vpn", "vpn_peer_list", map[string]any{"include_revoked": true}, &peers); err != nil {
		return err
	}
	for _, p := range peers.Peers {
		if p.Name == inst.EnrollmentPeer && p.RevokedAt == 0 {
			var out map[string]any
			return ctx.PlatformAPI().CallAppResult("vpn", "vpn_peer_remove", map[string]any{"name": p.Name}, &out)
		}
	}
	return nil
}
