package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Machines behind a router keep `ssh -N -R` open to the server Instances runs
// on. The server account can only accept those forwards: its authorized_keys
// file is root-owned and each key may open exactly one loopback listener.
const (
	reverseTunnelUser          = "apteva-tunnel"
	reverseTunnelHostAlias     = "apteva-tunnel-server"
	reverseTunnelKeysFile      = "/etc/apteva-tunnel/authorized_keys"
	reverseTunnelConfigMarker  = "# apteva-instances reverse tunnel"
	defaultReverseTunnelPorts  = "22000-22999"
	reverseTunnelSetupTimeout  = 60 * time.Second
	reverseTunnelCommandPrefix = "restrict,port-forwarding,command=\"/bin/false\",permitlisten="
)

var reverseTunnelHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// Test seams: the server-side commands run on local host 0.
var (
	reverseTunnelRunLocal    = runLocal
	reverseTunnelInContainer = runningInContainer
	reverseTunnelPortFree    = loopbackPortFree
	reverseTunnelMu          sync.Mutex
)

type reverseTunnelConfig struct {
	Host      string
	SSHPort   int
	PortFirst int
	PortLast  int
}

type reverseTunnel struct {
	InstanceID       int64
	Port             int
	LocalSSHPort     int
	PublicKey        string
	CredentialRef    string
	ServerHost       string
	ServerPort       int
	ServerKnownHosts string
}

func loadReverseTunnelConfig(config sdk.Config) (reverseTunnelConfig, error) {
	get := func(env, key string) string {
		if v, ok := os.LookupEnv(env); ok {
			return strings.TrimSpace(v)
		}
		if config == nil {
			return ""
		}
		return strings.TrimSpace(config.Get(key))
	}
	c := reverseTunnelConfig{Host: get("APTEVA_INSTANCES_TUNNEL_HOST", "reverse_tunnel_host"), SSHPort: 22}
	if c.Host == "" {
		return c, errors.New("reverse tunnels are not configured: set reverse_tunnel_host in the Instances settings to the public hostname or IP machines can reach this server on over SSH")
	}
	if !reverseTunnelHostPattern.MatchString(c.Host) || net.ParseIP(c.Host) != nil && net.ParseIP(c.Host).IsLoopback() {
		return c, fmt.Errorf("reverse_tunnel_host %q must be a public hostname or IP address", c.Host)
	}
	if raw := get("APTEVA_INSTANCES_TUNNEL_SSH_PORT", "reverse_tunnel_ssh_port"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			return c, errors.New("reverse_tunnel_ssh_port must be between 1 and 65535")
		}
		c.SSHPort = n
	}
	ports := get("APTEVA_INSTANCES_TUNNEL_PORTS", "reverse_tunnel_ports")
	if ports == "" {
		ports = defaultReverseTunnelPorts
	}
	first, last, ok := strings.Cut(ports, "-")
	a, errA := strconv.Atoi(strings.TrimSpace(first))
	b, errB := strconv.Atoi(strings.TrimSpace(last))
	if !ok || errA != nil || errB != nil || a < 1024 || b > 65535 || a > b {
		return c, errors.New("reverse_tunnel_ports must be a range like 22000-22999 within 1024-65535")
	}
	c.PortFirst, c.PortLast = a, b
	return c, nil
}

// Reverse tunnels listen on the server's loopback and are managed through
// local host 0. Inside a container both would be the container's, not the
// server's, so the machine's tunnel would be unreachable.
func runningInContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

func loopbackPortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

const reverseTunnelPrivilege = `set -eu
[ "$(uname -s)" = Linux ] || { echo 'Reverse tunnels need the Apteva server to run Linux'; exit 1; }
if [ "$(id -u)" -eq 0 ]; then SUDO=""; else sudo -n true 2>/dev/null || { echo 'Reverse tunnel setup needs root or passwordless sudo on the Apteva server'; exit 1; }; SUDO="sudo -n"; fi
sshd_bin=$(command -v sshd || echo /usr/sbin/sshd)
[ -x "$sshd_bin" ] || { echo 'OpenSSH server is not installed on the Apteva server'; exit 1; }
`

// One-time, idempotent server setup. Appends a marked Match block at the end
// of sshd_config (a Match block inside an included file can capture later
// main-config lines on older OpenSSH), validates the result before installing
// it, and confirms the effective settings for the tunnel account.
func reverseTunnelServerSetupScript() string {
	block := reverseTunnelConfigMarker + ` (managed by Instances; remove this block to disable)
Match User ` + reverseTunnelUser + `
    AuthorizedKeysFile ` + reverseTunnelKeysFile + `
    AuthenticationMethods publickey
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AllowTcpForwarding remote
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    X11Forwarding no
    PermitTTY no
    GatewayPorts no
    ForceCommand /bin/false
    ClientAliveInterval 30
    ClientAliveCountMax 3
`
	return reverseTunnelPrivilege + `
user=` + reverseTunnelUser + `
if ! id "$user" >/dev/null 2>&1; then
 nologin=$(command -v nologin || echo /usr/sbin/nologin)
 $SUDO useradd --system --create-home --home-dir /var/lib/apteva-tunnel --shell "$nologin" "$user"
fi
$SUDO install -d -m 0755 -o root -g root /etc/apteva-tunnel
[ -e ` + reverseTunnelKeysFile + ` ] || $SUDO install -m 0644 -o root -g root /dev/null ` + reverseTunnelKeysFile + `
if ! $SUDO grep -qF ` + quoteShellArg(reverseTunnelConfigMarker) + ` /etc/ssh/sshd_config; then
 candidate=$(mktemp)
 trap 'rm -f "$candidate"' EXIT
 $SUDO cat /etc/ssh/sshd_config > "$candidate"
 printf '\n%s' ` + quoteShellArg(block) + ` >> "$candidate"
 $SUDO "$sshd_bin" -t -f "$candidate" || { echo 'The SSH server rejected the tunnel configuration; nothing was changed'; exit 1; }
 $SUDO cp /etc/ssh/sshd_config /etc/ssh/sshd_config.apteva-backup
 $SUDO install -m 0644 -o root -g root "$candidate" /etc/ssh/sshd_config
 rm -f "$candidate"
 trap - EXIT
 # Reload only a running daemon; an idle socket-activated sshd (Ubuntu 24.04)
 # reads the new configuration when it next starts.
 if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet ssh; then $SUDO systemctl reload ssh
 elif command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet sshd; then $SUDO systemctl reload sshd
 elif [ -r /run/sshd.pid ] && kill -0 "$(cat /run/sshd.pid)" 2>/dev/null; then $SUDO kill -HUP "$(cat /run/sshd.pid)"
 fi
fi
effective=$($SUDO "$sshd_bin" -T -C "user=$user,host=apteva-tunnel-check,addr=192.0.2.1")
printf '%s\n' "$effective" | grep -qx 'allowtcpforwarding remote' || { echo 'The SSH server does not apply the tunnel settings to the apteva-tunnel account; check its configuration for an earlier Match block'; exit 1; }
for directive in allowusers allowgroups denyusers denygroups; do
 values=$(printf '%s\n' "$effective" | sed -n "s/^$directive //p")
 [ -z "$values" ] || echo "WARNING $directive $values"
done
echo HOSTKEYS
for key in /etc/ssh/ssh_host_ed25519_key.pub /etc/ssh/ssh_host_ecdsa_key.pub /etc/ssh/ssh_host_rsa_key.pub; do [ -r "$key" ] && cut -d' ' -f1,2 "$key"; done
`
}

// Ensures the server side is set up and returns known_hosts lines for the
// tunnel alias. A configured AllowUsers/AllowGroups that excludes the tunnel
// account would silently refuse every machine, so it is refused here.
func ensureReverseTunnelServer() (string, error) {
	if reverseTunnelInContainer() {
		return "", errors.New("reverse tunnels need Instances to run directly on the server, not in a container: the tunnel listens on the server's loopback address")
	}
	out, code, err := reverseTunnelRunLocal(reverseTunnelServerSetupScript(), reverseTunnelSetupTimeout)
	if err != nil || code != 0 {
		return "", fmt.Errorf("reverse tunnel server setup failed: %s", strings.TrimSpace(truncateSetupOutput(out)))
	}
	head, keys, found := strings.Cut(out, "HOSTKEYS\n")
	if !found {
		return "", errors.New("reverse tunnel server setup returned no host keys")
	}
	for _, line := range strings.Split(head, "\n") {
		if rest, ok := strings.CutPrefix(line, "WARNING "); ok {
			directive, values, _ := strings.Cut(rest, " ")
			if directive == "allowusers" || directive == "allowgroups" {
				return "", fmt.Errorf("the SSH server restricts logins (%s %s); allow the %s account before using reverse tunnels", directive, values, reverseTunnelUser)
			}
		}
	}
	known := []string{}
	for _, line := range strings.Split(strings.TrimSpace(keys), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.HasPrefix(f[0], "ssh-") || len(f) == 2 && strings.HasPrefix(f[0], "ecdsa-") {
			known = append(known, reverseTunnelHostAlias+" "+f[0]+" "+f[1])
		}
	}
	if len(known) == 0 {
		return "", errors.New("the Apteva server has no readable SSH host keys")
	}
	return strings.Join(known, "\n") + "\n", nil
}

func reverseTunnelKeyLine(t reverseTunnel) string {
	return fmt.Sprintf(`%s"127.0.0.1:%d" %s apteva-instance-%d`, reverseTunnelCommandPrefix, t.Port, t.PublicKey, t.InstanceID)
}

// Rewrites the server's tunnel authorized_keys from the database, so it always
// matches the set of registered tunnel machines exactly. A removed machine's
// live session is closed, which frees its port immediately.
func syncReverseTunnelKeys(db *sql.DB, closePorts ...int) error {
	reverseTunnelMu.Lock()
	defer reverseTunnelMu.Unlock()
	tunnels, err := listReverseTunnels(db)
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		lines = append(lines, reverseTunnelKeyLine(t))
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	script := reverseTunnelPrivilege + `
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
printf '%s' ` + quoteShellArg(content) + ` > "$tmp"
$SUDO install -m 0644 -o root -g root "$tmp" ` + reverseTunnelKeysFile + `
`
	for _, port := range closePorts {
		// Only sshd sessions of the tunnel account are ever signalled.
		// ps truncates long user names, so compare numeric uids.
		script += fmt.Sprintf(`tunnel_uid=$(id -u %[1]s)
for pid in $($SUDO ss -Htlnp 'sport = :%[2]d' 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | sort -u); do
 [ "$(ps -o uid= -p "$pid" 2>/dev/null | tr -d ' ')" = "$tunnel_uid" ] && $SUDO kill "$pid" 2>/dev/null || true
done
sleep 1
# Without process details (restricted /proc), end every tunnel session: the
# account runs nothing else, and other machines reconnect within seconds.
if $SUDO ss -Htln 'sport = :%[2]d' 2>/dev/null | grep -q .; then $SUDO pkill -u "$tunnel_uid" 2>/dev/null || true; sleep 1; fi
`, reverseTunnelUser, port)
	}
	out, code, err := reverseTunnelRunLocal(script, reverseTunnelSetupTimeout)
	if err != nil || code != 0 {
		return fmt.Errorf("update reverse tunnel keys: %s", strings.TrimSpace(truncateSetupOutput(out)))
	}
	return nil
}

func listReverseTunnels(db *sql.DB) ([]reverseTunnel, error) {
	rows, err := db.Query(`SELECT instance_id, port, local_ssh_port, public_key, credential_ref, server_host, server_port, server_known_hosts FROM reverse_tunnels ORDER BY port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reverseTunnel
	for rows.Next() {
		var t reverseTunnel
		if err := rows.Scan(&t.InstanceID, &t.Port, &t.LocalSSHPort, &t.PublicKey, &t.CredentialRef, &t.ServerHost, &t.ServerPort, &t.ServerKnownHosts); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func getReverseTunnel(db *sql.DB, instanceID int64) (*reverseTunnel, error) {
	var t reverseTunnel
	err := db.QueryRow(`SELECT instance_id, port, local_ssh_port, public_key, credential_ref, server_host, server_port, server_known_hosts FROM reverse_tunnels WHERE instance_id=?`, instanceID).
		Scan(&t.InstanceID, &t.Port, &t.LocalSSHPort, &t.PublicKey, &t.CredentialRef, &t.ServerHost, &t.ServerPort, &t.ServerKnownHosts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

func reverseTunnelCredentialOwner(id int64) string { return fmt.Sprintf("reverse-tunnel:%d", id) }

// Allocates the lowest free port in the range and records the tunnel. The
// UNIQUE port column makes concurrent registrations safe: a lost race retries.
func createReverseTunnel(db *sql.DB, cfg reverseTunnelConfig, instanceID int64, localSSHPort int, knownHosts string) (*reverseTunnel, error) {
	privateKey, publicKey, err := generateSSHKeypair()
	if err != nil {
		return nil, err
	}
	ref, err := storeSSHCredential(db, reverseTunnelCredentialOwner(instanceID), privateKey)
	if err != nil {
		return nil, err
	}
	t, err := allocateReverseTunnel(db, cfg, reverseTunnel{InstanceID: instanceID, LocalSSHPort: localSSHPort, PublicKey: publicKey, CredentialRef: ref, ServerHost: cfg.Host, ServerPort: cfg.SSHPort, ServerKnownHosts: knownHosts})
	if err != nil {
		_ = removeSSHCredential(db, ref)
	}
	return t, err
}

func allocateReverseTunnel(db *sql.DB, cfg reverseTunnelConfig, t reverseTunnel) (*reverseTunnel, error) {
	for attempt := 0; attempt < 5; attempt++ {
		used := map[int]bool{}
		rows, err := db.Query(`SELECT port FROM reverse_tunnels`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p int
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			used[p] = true
		}
		rows.Close()
		port := 0
		for p := cfg.PortFirst; p <= cfg.PortLast; p++ {
			if !used[p] && reverseTunnelPortFree(p) {
				port = p
				break
			}
		}
		if port == 0 {
			return nil, fmt.Errorf("no free reverse tunnel port in %d-%d", cfg.PortFirst, cfg.PortLast)
		}
		t.Port = port
		_, err = db.Exec(`INSERT INTO reverse_tunnels(instance_id, port, local_ssh_port, public_key, credential_ref, server_host, server_port, server_known_hosts, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			t.InstanceID, t.Port, t.LocalSSHPort, t.PublicKey, t.CredentialRef, t.ServerHost, t.ServerPort, t.ServerKnownHosts, nowUTC())
		if err == nil {
			return &t, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a reverse tunnel port; retry")
}

func reverseTunnelEnrollment(db *sql.DB, t *reverseTunnel) (*tunnelEnrollment, error) {
	key, err := loadSSHCredential(db, reverseTunnelCredentialOwner(t.InstanceID), t.CredentialRef)
	if err != nil {
		return nil, err
	}
	return &tunnelEnrollment{Port: t.Port, LocalSSHPort: t.LocalSSHPort, ServerHost: t.ServerHost, ServerPort: t.ServerPort, KnownHosts: t.ServerKnownHosts, PrivateKey: key, PublicKey: t.PublicKey}, nil
}

// Forgets the machine's tunnel: its key leaves the server, its live session is
// closed and its port becomes free. The machine itself is never contacted.
func removeReverseTunnel(db *sql.DB, inst *Instance) error {
	t, err := getReverseTunnel(db, inst.ID)
	if err != nil || t == nil {
		return err
	}
	if _, err = db.Exec(`DELETE FROM reverse_tunnels WHERE instance_id=?`, inst.ID); err != nil {
		return err
	}
	if err = syncReverseTunnelKeys(db, t.Port); err != nil {
		// Restore the row so a retried destroy repeats the server cleanup.
		_, _ = db.Exec(`INSERT OR IGNORE INTO reverse_tunnels(instance_id, port, local_ssh_port, public_key, credential_ref, server_host, server_port, server_known_hosts, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			t.InstanceID, t.Port, t.LocalSSHPort, t.PublicKey, t.CredentialRef, t.ServerHost, t.ServerPort, t.ServerKnownHosts, nowUTC())
		return err
	}
	return removeSSHCredential(db, t.CredentialRef)
}
