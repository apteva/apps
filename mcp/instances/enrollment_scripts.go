package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// tunnelEnrollment is everything the machine needs to keep its reverse tunnel
// to this server open. PrivateKey can only open one listener on the server.
type tunnelEnrollment struct {
	Port         int
	LocalSSHPort int
	ServerHost   string
	ServerPort   int
	KnownHosts   string
	PrivateKey   string
	PublicKey    string
}

// Shared by both branches: POSIX sh, BSD and GNU userland. macOS releases
// before 13 only accept -D for base64 decoding.
const enrollmentPrelude = `set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run this command as an administrator'; exit 1; }
decode_b64() { if [ "$(printf 'YQ==' | base64 -d 2>/dev/null)" = a ]; then base64 -d; else base64 -D; fi; }
`

// Authorizes the management key for the SSH account. Expects $user,
// $public_key and $home_dir; identical on Linux and macOS.
const enrollmentAuthorizeKey = `case "$home_dir" in /*) ;; *) echo 'Cannot determine account home'; exit 1;; esac
[ ! -L "$home_dir/.ssh" ] && [ ! -L "$home_dir/.ssh/authorized_keys" ] || { echo 'Refusing symlinked SSH configuration'; exit 1; }
install -d -m 0700 -o "$user" -g "$(id -gn "$user")" "$home_dir/.ssh"
touch "$home_dir/.ssh/authorized_keys"
grep -qxF "$public_key" "$home_dir/.ssh/authorized_keys" || printf '%s\n' "$public_key" >> "$home_dir/.ssh/authorized_keys"
chown "$user:$(id -gn "$user")" "$home_dir/.ssh/authorized_keys"
chmod 0600 "$home_dir/.ssh/authorized_keys"
`

func enrollmentSudoers(id int64) string {
	return `# Selected software setup needs unattended administrator access.
install -d -m 0755 /etc/sudoers.d
sudo_file=$(mktemp)
trap 'rm -f "$sudo_file"' EXIT
printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$user" > "$sudo_file"
visudo -cf "$sudo_file" >/dev/null
install -m 0440 "$sudo_file" "/etc/sudoers.d/apteva-instance-` + fmt.Sprint(id) + `"
rm -f "$sudo_file"
trap - EXIT
`
}

// macOS: turn on Remote Login, create a hidden management account if it does
// not exist, authorize the key and keep the machine awake and reachable.
func macEnrollmentBody(inst *Instance, elevated, vpn bool) string {
	if vpn {
		return "echo 'VPN enrollment supports Ubuntu/Debian. Register this Mac with connect=reverse_tunnel instead.'; exit 1\n"
	}
	script := `command -v sysadminctl >/dev/null 2>&1 || { echo 'This macOS version is not supported'; exit 1; }
if ! id "$user" >/dev/null 2>&1; then
 # The password is random and never shown: Instances signs in with its key only.
 account_password=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 40)
 sysadminctl -addUser "$user" -fullName 'Apteva management' -password "$account_password" -home "/Users/$user" -shell /bin/zsh >/dev/null 2>&1 || true
 unset account_password
 id "$user" >/dev/null 2>&1 || { echo 'Could not create the management account'; exit 1; }
 createhomedir -c -u "$user" >/dev/null 2>&1 || true
 dscl . -create "/Users/$user" IsHidden 1
fi
home_dir=$(dscl . -read "/Users/$user" NFSHomeDirectory | sed 's/^NFSHomeDirectory: *//')
[ -d "$home_dir" ] || install -d -m 0750 -o "$user" -g "$(id -gn "$user")" "$home_dir"
` + enrollmentAuthorizeKey + `
# When Remote Login is limited to some users, macOS checks this group.
if dscl . -read /Groups/com.apple.access_ssh >/dev/null 2>&1; then dseditgroup -o edit -a "$user" -t user com.apple.access_ssh; fi
ssh_listening() { nc -z -G 3 127.0.0.1 22 >/dev/null 2>&1; }
if ! ssh_listening; then
 systemsetup -setremotelogin on >/dev/null 2>&1 || true
 ssh_listening || launchctl load -w /System/Library/LaunchDaemons/ssh.plist >/dev/null 2>&1 || true
 sleep 2
fi
if ! ssh_listening; then
 echo 'macOS did not allow turning on Remote Login from the command line. Do one of these once, then run this command again:'
 echo ' - System Settings > General > Sharing: turn on Remote Login'
 echo ' - System Settings > Privacy & Security > Full Disk Access: allow the app you run this command in (for example Terminal)'
 exit 1
fi
# Keep the Mac up: no sleep, restart after a power failure, wake for network.
for setting in 'sleep 0' 'disksleep 0' 'autorestart 1' 'womp 1'; do pmset -a $setting >/dev/null 2>&1 || true; done
# Security updates still install; macOS upgrades that restart the Mac do not.
defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticallyInstallMacOSUpdates -bool false
if pmset -g batt 2>/dev/null | grep -q InternalBattery; then echo 'Note: this Mac has a battery. Keep it on power with the lid open: macOS sleeps when the lid closes, whatever the power settings.'; fi
`
	if elevated && inst.SSHUser != "root" {
		// Homebrew refuses to install unless its user is an administrator.
		script += "dseditgroup -o edit -a \"$user\" -t user admin\n" + enrollmentSudoers(inst.ID)
	}
	return script
}

func linuxEnrollmentBody(inst *Instance, elevated bool, vpnConfig string) (string, error) {
	script := `[ -r /etc/os-release ] || { echo 'Enrollment command supports Ubuntu/Debian and macOS'; exit 1; }
. /etc/os-release
case "$ID" in ubuntu|debian) ;; *) echo 'Enrollment command supports Ubuntu/Debian and macOS'; exit 1;; esac
if ! command -v sshd >/dev/null 2>&1 || ! command -v sudo >/dev/null 2>&1; then
 apt-get update -qq
 DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-server sudo
fi
if ! id "$user" >/dev/null 2>&1; then useradd --create-home --shell /bin/bash "$user"; fi
home_dir=$(getent passwd "$user" | cut -d: -f6)
` + enrollmentAuthorizeKey + `systemctl enable --now ssh
`
	if elevated && inst.SSHUser != "root" {
		script += enrollmentSudoers(inst.ID)
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
printf '%s' ` + quoteShellArg(base64.StdEncoding.EncodeToString([]byte(vpnConfig))) + ` | decode_b64 > "$config_file"
target=/etc/wireguard/` + iface + `.conf
if [ -e "$target" ] && ! cmp -s "$config_file" "$target"; then echo 'Existing VPN configuration differs; refusing to overwrite it'; exit 1; fi
install -m 0600 "$config_file" "$target"
rm -f "$config_file"
trap - EXIT
systemctl enable --now wg-quick@` + iface + `
umask 022
`
	}
	return script, nil
}

// tunnelServiceName is stable per tunnel key, so re-running the command
// replaces the same job and two Apteva servers never collide on one machine.
func tunnelServiceName(publicKey string) string {
	sum := sha256.Sum256([]byte(publicKey))
	return fmt.Sprintf("apteva-tunnel-%x", sum[:5])
}

// The tunnel job runs as the management account, never as root. The server's
// host keys are pinned under a fixed alias, so a DNS or IP change cannot make
// ssh accept a different server.
func tunnelArgs(t *tunnelEnrollment, keyFile, knownHostsFile string) []string {
	return []string{
		"-N", "-T",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=20",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "HostKeyAlias=" + reverseTunnelHostAlias,
		"-o", "UserKnownHostsFile=" + knownHostsFile,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityFile=" + keyFile,
		"-p", fmt.Sprint(t.ServerPort),
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", t.Port, t.LocalSSHPort),
		reverseTunnelUser + "@" + t.ServerHost,
	}
}

func tunnelEnrollmentBody(t *tunnelEnrollment) string {
	name := tunnelServiceName(t.PublicKey)
	dir := "/etc/apteva-tunnel"
	keyFile, knownHostsFile := dir+"/"+name+".key", dir+"/"+name+".known_hosts"
	args := tunnelArgs(t, keyFile, knownHostsFile)
	quoted := make([]string, len(args))
	plistArgs := ""
	for i, a := range args {
		quoted[i] = quoteShellArg(a)
		plistArgs += "<string>" + xmlEscape(a) + "</string>"
	}
	label := "com.apteva." + name
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + label + `</string><key>UserName</key><string>USER_PLACEHOLDER</string><key>ProgramArguments</key><array><string>/usr/bin/ssh</string>` + plistArgs + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer><key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>`
	unit := "[Unit]\nDescription=Apteva reverse tunnel\nAfter=network-online.target\nWants=network-online.target\nStartLimitIntervalSec=0\n[Service]\nUser=USER_PLACEHOLDER\nExecStart=SSH_PLACEHOLDER " + strings.Join(args, " ") + "\nRestart=always\nRestartSec=10\nNoNewPrivileges=true\n[Install]\nWantedBy=multi-user.target\n"
	return `# Reverse tunnel: keep a forward-only SSH session open to the Apteva server.
install -d -m 0755 /etc/apteva-tunnel
umask 077
tunnel_tmp=$(mktemp /etc/apteva-tunnel/.enroll.XXXXXX)
trap 'rm -f "$tunnel_tmp"' EXIT
printf '%s' ` + quoteShellArg(base64.StdEncoding.EncodeToString([]byte(t.PrivateKey))) + ` | decode_b64 > "$tunnel_tmp"
install -m 0600 -o "$user" -g "$(id -gn "$user")" "$tunnel_tmp" ` + quoteShellArg(keyFile) + `
printf '%s' ` + quoteShellArg(base64.StdEncoding.EncodeToString([]byte(t.KnownHosts))) + ` | decode_b64 > "$tunnel_tmp"
install -m 0644 "$tunnel_tmp" ` + quoteShellArg(knownHostsFile) + `
rm -f "$tunnel_tmp"
trap - EXIT
umask 022
user_xml=$(printf '%s' "$user" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')
if [ "$os_name" = Darwin ]; then
 plist=/Library/LaunchDaemons/` + label + `.plist
 printf '%s' ` + quoteShellArg(plist) + ` | sed "s/USER_PLACEHOLDER/$user_xml/" > "$plist"
 chown root:wheel "$plist"
 chmod 0644 "$plist"
 launchctl bootout system "$plist" >/dev/null 2>&1 || true
 launchctl bootstrap system "$plist"
else
 command -v ssh >/dev/null 2>&1 || { apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-client; }
 ssh_bin=$(command -v ssh)
 printf '%s' ` + quoteShellArg(unit) + ` | sed -e "s|USER_PLACEHOLDER|$user|" -e "s|SSH_PLACEHOLDER|$ssh_bin|" > /etc/systemd/system/` + name + `.service
 chmod 0644 /etc/systemd/system/` + name + `.service
 systemctl daemon-reload
 systemctl enable ` + name + `.service >/dev/null 2>&1
 systemctl restart ` + name + `.service
fi
printf '%s\n' 'Reverse tunnel started; it reconnects automatically after network loss or restart.'
`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}
