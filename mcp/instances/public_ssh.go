package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Public SSH for reverse-tunnel machines. The tunnel key stays limited to its
// loopback port; Instances itself accepts connections on a public port and
// copies them to that loopback port. Nothing on the machine side can open a
// public listener.
const (
	defaultPublicSSHPorts          = "23000-23999"
	defaultPublicSSHMaxConnections = 10
	maxPublicSSHMaxConnections     = 100
	maxPublicSSHCIDRs              = 16
	publicSSHRateWindow            = time.Minute
	publicSSHRatePerSource         = 20
	publicSSHChangedTopic          = "instance.public_ssh_changed"
)

// PublicSSH is what instance_get / instance_list report for tunnel machines.
type PublicSSH struct {
	Enabled        bool     `json:"enabled"`
	Host           string   `json:"host,omitempty"`
	Port           int      `json:"port,omitempty"`
	AllowedCIDRs   []string `json:"allowed_cidrs,omitempty"`
	MaxConnections int      `json:"max_connections,omitempty"`
	Firewall       string   `json:"firewall,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type publicSSHConfig struct {
	Host      string
	PortFirst int
	PortLast  int
}

type publicSSHOptions struct {
	AllowedCIDRs   []string
	MaxConnections int
}

// Test seams.
var (
	publicSSHListenHost = ""
	publicSSHPortFree   = func(port int) bool {
		l, err := net.Listen("tcp", net.JoinHostPort(publicSSHListenHost, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = l.Close()
		return true
	}
	publicSSHCheckMachine = func(inst *Instance) (string, int, error) {
		return runSSH(inst, keysOnlySSHDRemote, 90*time.Second)
	}
)

var (
	publicSSHMu      sync.Mutex // serializes enable / disable / restore
	publicSSHStateMu sync.Mutex
	publicSSHProxies = map[int64]*publicSSHProxy{}
	publicSSHStates  = map[int64]PublicSSH{}
)

func loadPublicSSHConfig(config sdk.Config) (publicSSHConfig, error) {
	tunnel, err := loadReverseTunnelConfig(config)
	if err != nil {
		return publicSSHConfig{}, err
	}
	get := func(env, key string) string {
		if v, ok := os.LookupEnv(env); ok {
			return strings.TrimSpace(v)
		}
		if config == nil {
			return ""
		}
		return strings.TrimSpace(config.Get(key))
	}
	c := publicSSHConfig{Host: get("APTEVA_INSTANCES_TUNNEL_PUBLIC_HOST", "reverse_tunnel_public_host")}
	if c.Host == "" {
		c.Host = tunnel.Host
	}
	if !reverseTunnelHostPattern.MatchString(c.Host) {
		return c, fmt.Errorf("reverse_tunnel_public_host %q must be a hostname or IP address", c.Host)
	}
	ports := get("APTEVA_INSTANCES_TUNNEL_PUBLIC_PORTS", "reverse_tunnel_public_ports")
	if ports == "" {
		ports = defaultPublicSSHPorts
	}
	first, last, ok := strings.Cut(ports, "-")
	a, errA := strconv.Atoi(strings.TrimSpace(first))
	b, errB := strconv.Atoi(strings.TrimSpace(last))
	if !ok || errA != nil || errB != nil || a < 1024 || b > 65535 || a > b {
		return c, errors.New("reverse_tunnel_public_ports must be a range like 23000-23999 within 1024-65535")
	}
	if a <= tunnel.PortLast && tunnel.PortFirst <= b {
		return c, errors.New("reverse_tunnel_public_ports must not overlap reverse_tunnel_ports")
	}
	c.PortFirst, c.PortLast = a, b
	return c, nil
}

func parsePublicSSHOptions(rawCIDRs, rawMax any) (publicSSHOptions, error) {
	o := publicSSHOptions{MaxConnections: defaultPublicSSHMaxConnections}
	if rawMax != nil {
		n, ok := rawMax.(float64)
		if v, isInt := rawMax.(int); isInt {
			n, ok = float64(v), true
		}
		if !ok || n != float64(int(n)) || n < 1 || n > maxPublicSSHMaxConnections {
			return o, fmt.Errorf("max_connections must be between 1 and %d", maxPublicSSHMaxConnections)
		}
		o.MaxConnections = int(n)
	}
	if rawCIDRs == nil {
		return o, nil
	}
	var values []string
	switch v := rawCIDRs.(type) {
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return o, errors.New("allowed_cidrs must be a list of strings")
			}
			values = append(values, s)
		}
	case []string:
		values = v
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				values = append(values, s)
			}
		}
	default:
		return o, errors.New("allowed_cidrs must be a list of strings")
	}
	if len(values) > maxPublicSSHCIDRs {
		return o, fmt.Errorf("at most %d allowed_cidrs", maxPublicSSHCIDRs)
	}
	seen := map[string]bool{}
	for _, raw := range values {
		prefix, err := parseSourcePrefix(raw)
		if err != nil {
			return o, err
		}
		if s := prefix.String(); !seen[s] {
			seen[s] = true
			o.AllowedCIDRs = append(o.AllowedCIDRs, s)
		}
	}
	return o, nil
}

func parseSourcePrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "/") {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid source address %q", raw)
		}
		return netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()), nil
	}
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid source CIDR %q", raw)
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked(), nil
}

func splitCIDRs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// publicSSHView is attached to every tunnel machine by stripSecrets.
func publicSSHView(id int64) *PublicSSH {
	publicSSHStateMu.Lock()
	defer publicSSHStateMu.Unlock()
	state, ok := publicSSHStates[id]
	if !ok {
		return &PublicSSH{Enabled: false}
	}
	state.AllowedCIDRs = append([]string(nil), state.AllowedCIDRs...)
	return &state
}

func setPublicSSHState(id int64, state *PublicSSH) {
	publicSSHStateMu.Lock()
	defer publicSSHStateMu.Unlock()
	if state == nil {
		delete(publicSSHStates, id)
		return
	}
	publicSSHStates[id] = *state
}

// Turns public SSH on, or updates its allowed sources and connection limit
// when it is already on. checkMachine verifies (and if needed sets) key-only
// logins on the machine over SSH first; registration skips it because its
// setup command does that before the tunnel starts.
func enablePublicSSH(ctx *sdk.AppCtx, inst *Instance, opts publicSSHOptions, checkMachine bool) (*PublicSSH, error) {
	publicSSHMu.Lock()
	defer publicSSHMu.Unlock()
	if inst.ConnectMode != "reverse_tunnel" {
		return nil, errors.New("public SSH is for machines registered with connect=reverse_tunnel")
	}
	cfg, err := loadPublicSSHConfig(ctx.Config())
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	t, err := getReverseTunnel(db, inst.ID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errors.New("this machine has no reverse tunnel")
	}
	cidrs := strings.Join(opts.AllowedCIDRs, ",")
	if t.PublicPort > 0 {
		// Already on: apply the new limits in place.
		if _, err = db.Exec(`UPDATE reverse_tunnels SET public_cidrs=?, public_max_connections=? WHERE instance_id=?`, cidrs, opts.MaxConnections, inst.ID); err != nil {
			return nil, err
		}
		t.PublicCIDRs, t.PublicMaxConnections = cidrs, opts.MaxConnections
		state := startPublicSSHProxy(t, cfg.Host)
		emitPublicSSHChanged(ctx, inst, state)
		return state, nil
	}
	if checkMachine {
		if err = ensureMachineKeysOnly(inst); err != nil {
			return nil, err
		}
	}
	port, err := allocatePublicSSHPort(db, cfg, inst.ID, cidrs, opts.MaxConnections)
	if err != nil {
		return nil, err
	}
	firewall, err := openPublicSSHFirewall(inst.ID, port)
	if err != nil {
		_, _ = db.Exec(`UPDATE reverse_tunnels SET public_port=0, public_cidrs='', public_max_connections=0 WHERE instance_id=?`, inst.ID)
		return nil, err
	}
	_, _ = db.Exec(`UPDATE reverse_tunnels SET public_firewall=? WHERE instance_id=?`, firewall, inst.ID)
	t.PublicPort, t.PublicCIDRs, t.PublicMaxConnections, t.PublicFirewall = port, cidrs, opts.MaxConnections, firewall
	state := startPublicSSHProxy(t, cfg.Host)
	if state.Error != "" {
		stopPublicSSHProxy(inst.ID)
		_ = closePublicSSHFirewall(inst.ID, port, firewall)
		_, _ = db.Exec(`UPDATE reverse_tunnels SET public_port=0, public_cidrs='', public_max_connections=0, public_firewall='' WHERE instance_id=?`, inst.ID)
		setPublicSSHState(inst.ID, nil)
		return nil, errors.New(state.Error)
	}
	emitPublicSSHChanged(ctx, inst, state)
	return state, nil
}

// Turns public SSH off: the listener and its live sessions close first, then
// the server firewall rule. A failed firewall cleanup keeps the row, so a
// retry repeats it. Safe to call when public SSH is off.
func disablePublicSSH(db *sql.DB, instanceID int64) error {
	publicSSHMu.Lock()
	defer publicSSHMu.Unlock()
	t, err := getReverseTunnel(db, instanceID)
	if err != nil || t == nil || t.PublicPort == 0 {
		stopPublicSSHProxy(instanceID)
		setPublicSSHState(instanceID, nil)
		return err
	}
	stopPublicSSHProxy(instanceID)
	if err = closePublicSSHFirewall(instanceID, t.PublicPort, t.PublicFirewall); err != nil {
		setPublicSSHState(instanceID, &PublicSSH{Enabled: true, Port: t.PublicPort, Error: "turning off: " + err.Error()})
		return err
	}
	if _, err = db.Exec(`UPDATE reverse_tunnels SET public_port=0, public_cidrs='', public_max_connections=0, public_firewall='' WHERE instance_id=?`, instanceID); err != nil {
		return err
	}
	setPublicSSHState(instanceID, nil)
	if globalCtx != nil {
		if inst, err := dbGetInstance(db, instanceID); err == nil {
			emitPublicSSHChanged(globalCtx, inst, &PublicSSH{Enabled: false})
		}
	}
	return nil
}

// Restarts every public SSH proxy recorded in the database.
func restorePublicSSH(ctx *sdk.AppCtx) {
	publicSSHMu.Lock()
	defer publicSSHMu.Unlock()
	tunnels, err := listReverseTunnels(ctx.AppDB())
	if err != nil {
		ctx.Logger().Error("public ssh restore", "error", err)
		return
	}
	host := ""
	if cfg, err := loadPublicSSHConfig(ctx.Config()); err == nil {
		host = cfg.Host
	}
	for i := range tunnels {
		if tunnels[i].PublicPort == 0 {
			continue
		}
		if state := startPublicSSHProxy(&tunnels[i], host); state.Error != "" {
			ctx.Logger().Error("public ssh restore", "instance_id", tunnels[i].InstanceID, "port", tunnels[i].PublicPort, "error", state.Error)
		}
	}
}

func allocatePublicSSHPort(db *sql.DB, cfg publicSSHConfig, instanceID int64, cidrs string, max int) (int, error) {
	for attempt := 0; attempt < 5; attempt++ {
		used := map[int]bool{}
		rows, err := db.Query(`SELECT public_port FROM reverse_tunnels WHERE public_port > 0`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var p int
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return 0, err
			}
			used[p] = true
		}
		rows.Close()
		port := 0
		for p := cfg.PortFirst; p <= cfg.PortLast; p++ {
			if !used[p] && publicSSHPortFree(p) {
				port = p
				break
			}
		}
		if port == 0 {
			return 0, fmt.Errorf("no free public SSH port in %d-%d", cfg.PortFirst, cfg.PortLast)
		}
		res, err := db.Exec(`UPDATE reverse_tunnels SET public_port=?, public_cidrs=?, public_max_connections=?, public_firewall='' WHERE instance_id=? AND public_port=0`, port, cidrs, max, instanceID)
		if err == nil {
			if n, _ := res.RowsAffected(); n != 1 {
				return 0, errors.New("public SSH changed concurrently; retry")
			}
			return port, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") {
			return 0, err
		}
	}
	return 0, errors.New("could not allocate a public SSH port; retry")
}

// The server's own firewall (ufw or firewalld, when active) gets a rule for
// the port. A provider firewall in front of the server is outside Instances'
// reach: the documentation says to allow the public range there once.
func openPublicSSHFirewall(instanceID int64, port int) (string, error) {
	script := reverseTunnelPrivilege + fmt.Sprintf(`port=%d
comment='apteva-instances public ssh %d'
if command -v ufw >/dev/null 2>&1 && $SUDO ufw status 2>/dev/null | grep -q '^Status: active'; then
 $SUDO ufw allow "$port/tcp" comment "$comment" >/dev/null
 echo FIREWALL=ufw
elif command -v firewall-cmd >/dev/null 2>&1 && $SUDO firewall-cmd --state >/dev/null 2>&1; then
 $SUDO firewall-cmd --add-port="$port/tcp" >/dev/null
 $SUDO firewall-cmd --permanent --add-port="$port/tcp" >/dev/null
 echo FIREWALL=firewalld
else
 echo FIREWALL=none
fi
`, port, instanceID)
	out, code, err := reverseTunnelRunLocal(script, reverseTunnelSetupTimeout)
	if err != nil || code != 0 {
		return "", fmt.Errorf("open public SSH port in the server firewall: %s", strings.TrimSpace(truncateSetupOutput(out)))
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "FIREWALL="); ok {
			return v, nil
		}
	}
	return "", errors.New("open public SSH port in the server firewall: no result")
}

func closePublicSSHFirewall(instanceID int64, port int, firewall string) error {
	var body string
	switch firewall {
	case "ufw":
		body = fmt.Sprintf("$SUDO ufw delete allow '%d/tcp' >/dev/null 2>&1 || true\n", port)
	case "firewalld":
		body = fmt.Sprintf("$SUDO firewall-cmd --remove-port='%[1]d/tcp' >/dev/null 2>&1 || true\n$SUDO firewall-cmd --permanent --remove-port='%[1]d/tcp' >/dev/null 2>&1 || true\n", port)
	default:
		return nil
	}
	out, code, err := reverseTunnelRunLocal(reverseTunnelPrivilege+body, reverseTunnelSetupTimeout)
	if err != nil || code != 0 {
		return fmt.Errorf("close public SSH port %d in the server firewall: %s", port, strings.TrimSpace(truncateSetupOutput(out)))
	}
	return nil
}

// Runs the key-only check on the machine. It needs the machine connected and
// sudo for the management account; otherwise the error carries a command an
// administrator can run on the machine instead.
func ensureMachineKeysOnly(inst *Instance) error {
	out, code, err := publicSSHCheckMachine(inst)
	switch {
	case err != nil:
		return fmt.Errorf("public SSH checks that the machine's SSH server accepts keys only, and the machine is not reachable now (%v). Turn it on when the machine is connected", err)
	case code == 3:
		return fmt.Errorf("public SSH needs the machine's SSH server to accept keys only, and the management account cannot use sudo to check it. Run this on the machine as an administrator, then try again:\n%s", keysOnlySSHDCommand())
	case code != 0:
		return fmt.Errorf("could not make the machine's SSH server accept keys only: %s", strings.TrimSpace(truncateSetupOutput(out)))
	}
	return nil
}

func emitPublicSSHChanged(ctx *sdk.AppCtx, inst *Instance, state *PublicSSH) {
	if ctx == nil || inst == nil {
		return
	}
	payload := instanceEventPayload(inst)
	payload["public_ssh"] = state
	emitInstanceEventMap(ctx, publicSSHChangedTopic, payload)
}

// ─── Proxy ─────────────────────────────────────────────────────────

type publicSSHProxy struct {
	instanceID int64
	target     int
	listener   net.Listener

	mu      sync.Mutex
	allowed []netip.Prefix
	max     int
	conns   map[net.Conn]struct{}
	recent  map[netip.Addr][]time.Time
	closed  bool
}

// Starts the proxy for a tunnel, or applies new limits to a running one, and
// records the state reported to callers.
func startPublicSSHProxy(t *reverseTunnel, host string) *PublicSSH {
	state := &PublicSSH{Enabled: true, Host: host, Port: t.PublicPort, AllowedCIDRs: splitCIDRs(t.PublicCIDRs), MaxConnections: t.PublicMaxConnections, Firewall: t.PublicFirewall}
	var allowed []netip.Prefix
	for _, raw := range state.AllowedCIDRs {
		if p, err := parseSourcePrefix(raw); err == nil {
			allowed = append(allowed, p)
		}
	}
	max := t.PublicMaxConnections
	if max <= 0 {
		max = defaultPublicSSHMaxConnections
	}
	publicSSHStateMu.Lock()
	existing := publicSSHProxies[t.InstanceID]
	publicSSHStateMu.Unlock()
	if existing != nil && existing.target == t.Port && existing.listener.Addr().(*net.TCPAddr).Port == t.PublicPort {
		existing.mu.Lock()
		existing.allowed, existing.max = allowed, max
		existing.mu.Unlock()
		setPublicSSHState(t.InstanceID, state)
		return state
	}
	if existing != nil {
		stopPublicSSHProxy(t.InstanceID)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(publicSSHListenHost, strconv.Itoa(t.PublicPort)))
	if err != nil {
		state.Error = fmt.Sprintf("listen on public port %d: %v", t.PublicPort, err)
		setPublicSSHState(t.InstanceID, state)
		return state
	}
	p := &publicSSHProxy{instanceID: t.InstanceID, target: t.Port, listener: ln, allowed: allowed, max: max, conns: map[net.Conn]struct{}{}, recent: map[netip.Addr][]time.Time{}}
	publicSSHStateMu.Lock()
	publicSSHProxies[t.InstanceID] = p
	publicSSHStateMu.Unlock()
	setPublicSSHState(t.InstanceID, state)
	go p.serve()
	return state
}

func stopPublicSSHProxy(instanceID int64) {
	publicSSHStateMu.Lock()
	p := publicSSHProxies[instanceID]
	delete(publicSSHProxies, instanceID)
	publicSSHStateMu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	_ = p.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *publicSSHProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go p.handle(conn)
	}
}

// admit applies the source filter, the per-source rate limit and the
// concurrent connection limit. log is false for repeated refusals from one
// source, so a scanner cannot flood the log.
func (p *publicSSHProxy) admit(conn net.Conn, now time.Time) (ok bool, reason string, log bool) {
	addr := netip.Addr{}
	if tcp, isTCP := conn.RemoteAddr().(*net.TCPAddr); isTCP {
		addr, _ = netip.AddrFromSlice(tcp.IP)
		addr = addr.Unmap()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false, "closed", false
	}
	window := p.recent[addr][:0]
	for _, at := range p.recent[addr] {
		if now.Sub(at) < publicSSHRateWindow {
			window = append(window, at)
		}
	}
	window = append(window, now)
	p.recent[addr] = window
	if len(p.recent) > 4096 {
		for a, times := range p.recent {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= publicSSHRateWindow {
				delete(p.recent, a)
			}
		}
	}
	log = len(window) <= publicSSHRatePerSource+1
	if len(p.allowed) > 0 {
		allowed := false
		for _, prefix := range p.allowed {
			if prefix.Contains(addr) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, "source not allowed", log
		}
	}
	if len(window) > publicSSHRatePerSource {
		return false, "too many new connections from this source", log
	}
	if len(p.conns) >= p.max {
		return false, "connection limit reached", log
	}
	p.conns[conn] = struct{}{}
	return true, "", true
}

func (p *publicSSHProxy) release(conn net.Conn) {
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
	_ = conn.Close()
}

func (p *publicSSHProxy) handle(conn net.Conn) {
	ok, reason, log := p.admit(conn, time.Now())
	if log && globalCtx != nil {
		if ok {
			globalCtx.Logger().Info("public ssh connection", "instance_id", p.instanceID, "source", conn.RemoteAddr().String())
		} else {
			globalCtx.Logger().Warn("public ssh connection refused", "instance_id", p.instanceID, "source", conn.RemoteAddr().String(), "reason", reason)
		}
	}
	if !ok {
		_ = conn.Close()
		return
	}
	defer p.release(conn)
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.target)), 10*time.Second)
	if err != nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = upstream.Close()
		return
	}
	p.conns[upstream] = struct{}{}
	p.mu.Unlock()
	defer p.release(upstream)
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(upstream, conn)
	go pipe(conn, upstream)
	<-done
	<-done
}

// ─── Tool and HTTP ─────────────────────────────────────────────────

func setPublicSSH(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	enabled, ok := args["enabled"].(bool)
	if !ok {
		return nil, errors.New("enabled (boolean) is required")
	}
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if inst.ConnectMode != "reverse_tunnel" {
		return nil, errors.New("public SSH is for machines registered with connect=reverse_tunnel")
	}
	if !enabled {
		if _, present := args["allowed_cidrs"]; present {
			return nil, errors.New("allowed_cidrs applies when enabling")
		}
		if err = disablePublicSSH(ctx.AppDB(), id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "public_ssh": publicSSHView(id)}, nil
	}
	if inst.Status == "destroying" || inst.Status == "rolling_back" {
		return nil, ErrOperationSuperseded
	}
	opts, err := parsePublicSSHOptions(args["allowed_cidrs"], args["max_connections"])
	if err != nil {
		return nil, err
	}
	state, err := enablePublicSSH(ctx, inst, opts, true)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "public_ssh": state}
	if len(state.AllowedCIDRs) == 0 {
		result["note"] = "Any source address can connect. Set allowed_cidrs to limit it to known networks."
	}
	result["provider_firewall"] = fmt.Sprintf("If a cloud firewall sits in front of this server, allow TCP %d there.", state.Port)
	return result, nil
}

func (a *App) toolPublicSSHSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return setPublicSSH(ctx, args)
}

// The cleanup command must be built before destroy removes the tunnel row.
func machineCleanupFor(db *sql.DB, inst *Instance) string {
	if normalizeProvider(inst.Provider) != "external" || inst.SSHPublicKey == "" || !sshUserPattern.MatchString(inst.SSHUser) {
		return ""
	}
	t, _ := getReverseTunnel(db, inst.ID)
	return machineCleanupCommand(inst, t)
}

func destroyResult(db *sql.DB, id int64, cleanup string) map[string]any {
	result := map[string]any{"destroyed": true, "id": id}
	if pending, err := dbGetInstance(db, id); err == nil {
		result = map[string]any{"destroyed": false, "id": id, "status": pending.Status}
	}
	if cleanup != "" {
		result["cleanup_command"] = cleanup
		result["cleanup_note"] = "Run cleanup_command on the machine as an administrator. It removes the tunnel job (which otherwise keeps retrying with its revoked key), the management key and the sudo rule. The SSH account stays."
	}
	return result
}
