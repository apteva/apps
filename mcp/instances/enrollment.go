package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
		for _, key := range []string{"name", "ssh_host", "ssh_user", "ssh_port", "setup", "vpn"} {
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
	vpn, _ := args["vpn"].(bool)
	if name == "" || user == "" || (!vpn && host == "") {
		return nil, errors.New("name, ssh_user, and ssh_host (or vpn=true) are required")
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
	if vpn {
		if port != 22 {
			return nil, errors.New("VPN enrollment currently uses SSH port 22")
		}
		if _, err = checkedVPNStatus(ctx); err != nil {
			return nil, err
		}
		host = ""
	}
	privateKey, publicKey, err := generateSSHKeypair()
	if err != nil {
		return nil, err
	}
	providerID := net.JoinHostPort(host, fmt.Sprint(port))
	if vpn {
		providerID = fmt.Sprintf("enrollment:%x", sha256.Sum256([]byte(publicKey)))
	}
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: name, Provider: "external", ProviderID: providerID, Status: "provisioning", SSHHost: host, SSHPort: port, SSHUser: user, SSHPrivateKey: privateKey, SSHPublicKey: publicKey, TagsJSON: strArg(args, "tags_json"), Setup: options})
	if err != nil {
		return nil, err
	}
	if vpn {
		fingerprint := sha256.Sum256([]byte(publicKey))
		inst.EnrollmentPeer = fmt.Sprintf("apteva-instance-%d-%x", inst.ID, fingerprint[:6])
		if err = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"enrollment_peer": inst.EnrollmentPeer}); err != nil {
			return nil, err
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
	command, err := hostEnrollmentCommand(inst, vpnConfig)
	if err != nil {
		return nil, err
	}
	return map[string]any{"instance": inst.stripSecrets(), "authorization": map[string]any{"ssh_user": inst.SSHUser, "ssh_host": inst.SSHHost, "ssh_port": inst.SSHPort, "public_key": inst.SSHPublicKey, "command": command, "contains_credentials": vpnConfig != "", "next_step": "On Ubuntu/Debian, run command on this server as an administrator. Alternatively add public_key to the SSH user's authorized_keys and configure VPN if selected. Then call instance_wait_ready; instance_get reports setup progress."}}, nil
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
func hostEnrollmentCommand(inst *Instance, vpnConfig string) (string, error) {
	if !sshUserPattern.MatchString(inst.SSHUser) {
		return "", errors.New("unsupported SSH account name for enrollment command")
	}
	elevated := inst.Setup != nil && (inst.Setup.Requested.Baseline || inst.Setup.Requested.Docker || inst.Setup.Requested.Runtimes)
	script := `set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run this command as an administrator'; exit 1; }
[ -r /etc/os-release ] || { echo 'Enrollment command supports Ubuntu/Debian'; exit 1; }
. /etc/os-release
case "$ID" in ubuntu|debian) ;; *) echo 'Enrollment command supports Ubuntu/Debian'; exit 1;; esac
` + "user=" + quoteShellArg(inst.SSHUser) + "\npublic_key=" + quoteShellArg(inst.SSHPublicKey) + "\n" + `
if ! command -v sshd >/dev/null 2>&1 || ! command -v sudo >/dev/null 2>&1; then
 apt-get update -qq
 DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-server sudo
fi
if ! id "$user" >/dev/null 2>&1; then useradd --create-home --shell /bin/bash "$user"; fi
home_dir=$(getent passwd "$user" | cut -d: -f6)
case "$home_dir" in /*) ;; *) echo 'Cannot determine account home'; exit 1;; esac
[ ! -L "$home_dir/.ssh" ] && [ ! -L "$home_dir/.ssh/authorized_keys" ] || { echo 'Refusing symlinked SSH configuration'; exit 1; }
install -d -m 0700 -o "$user" -g "$(id -gn "$user")" "$home_dir/.ssh"
touch "$home_dir/.ssh/authorized_keys"
grep -qxF "$public_key" "$home_dir/.ssh/authorized_keys" || printf '%s\n' "$public_key" >> "$home_dir/.ssh/authorized_keys"
chown "$user:$(id -gn "$user")" "$home_dir/.ssh/authorized_keys"
chmod 0600 "$home_dir/.ssh/authorized_keys"
systemctl enable --now ssh
`
	if elevated && inst.SSHUser != "root" {
		script += `# Selected software setup needs unattended administrator access.
sudo_file=$(mktemp)
trap 'rm -f "$sudo_file"' EXIT
printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$user" > "$sudo_file"
visudo -cf "$sudo_file"
install -m 0440 "$sudo_file" "/etc/sudoers.d/apteva-instance-` + fmt.Sprint(inst.ID) + `"
rm -f "$sudo_file"
trap - EXIT
`
	}
	if vpnConfig != "" {
		fingerprint := sha256.Sum256([]byte(inst.SSHPublicKey))
		iface := fmt.Sprintf("aptv%x", fingerprint[:5])
		if len(iface) > 15 {
			return "", errors.New("instance ID exceeds WireGuard interface name limit")
		}
		script += `if ! command -v wg-quick >/dev/null 2>&1; then apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y wireguard-tools; fi
install -d -m 0700 /etc/wireguard
umask 077
config_file=$(mktemp /etc/wireguard/.apteva-enroll.XXXXXX)
trap 'rm -f "$config_file"' EXIT
printf '%s' ` + quoteShellArg(base64.StdEncoding.EncodeToString([]byte(vpnConfig))) + ` | base64 -d > "$config_file"
target=/etc/wireguard/` + iface + `.conf
if [ -e "$target" ] && ! cmp -s "$config_file" "$target"; then echo 'Existing VPN configuration differs; refusing to overwrite it'; exit 1; fi
install -m 0600 "$config_file" "$target"
rm -f "$config_file"
trap - EXIT
systemctl enable --now wg-quick@` + iface + `
`
	}
	script += "printf '%s\\n' 'SSH authorized. Instances will verify connectivity and complete the selected setup.'\n"
	return "sudo sh <<'APTEVA_HOST_ENROLLMENT'\n" + script + "APTEVA_HOST_ENROLLMENT", nil
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
