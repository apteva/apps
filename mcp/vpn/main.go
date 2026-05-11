// Apteva VPN app — backend-agnostic VPN orchestrator. Drives an
// `instances` host to install a VPN daemon (WireGuard in v0.1),
// manages peers via MCP tools, and polls per-peer statistics on a
// timer. The Backend interface in ./backend keeps the protocol
// pluggable: adding OpenVPN / IKEv2 later means a new sibling
// directory under ./backend, no changes here.
package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/vpn/backend"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	mu sync.Mutex // serializes daemon-touching ops; SQLite is per-conn safe

	ctx       *sdk.AppCtx
	backend   backend.Backend
	pollNotif chan struct{} // buffered(1); the worker tries-receive on its tick
}

// ─── lifecycle ──────────────────────────────────────────────────────

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("vpn: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("vpn: requires a db block")
	}
	a.ctx = ctx

	b, err := pickBackend(configString(ctx, "backend", "wireguard"))
	if err != nil {
		return err
	}
	a.backend = b
	a.pollNotif = make(chan struct{}, 1)

	ctx.Logger().Info("vpn mounted", "backend", b.Name())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name: "stats-poll",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.runStatsPoller(ctx)
			},
		},
	}
}

// ─── HTTP routes (panel reads) ──────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/health", Handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }, NoAuth: true},
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/peers", Handler: a.handlePeers},
	}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	out, err := a.statusSnapshot()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

func (a *App) handlePeers(w http.ResponseWriter, r *http.Request) {
	includeRevoked := r.URL.Query().Get("include_revoked") == "true"
	peers, err := listPeers(a.ctx.AppDB(), projectScope(), includeRevoked)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"peers": peers, "count": len(peers)})
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	obj := func(props map[string]any, required []string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}

	return []sdk.Tool{
		{
			Name:        "vpn_status",
			Description: "Backend, host_id, endpoint, port, install timestamp, peer counts (active / revoked), last stats-poll outcome.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolStatus,
		},
		{
			Name: "vpn_install",
			Description: "Install the VPN daemon on the bound Instances host, generate server keys, push daemon config, enable as a service. " +
				"Args (all optional, default from config_schema): host_id, endpoint, listen_port, network_cidr.",
			InputSchema: obj(map[string]any{
				"host_id":      num,
				"endpoint":     str,
				"listen_port":  num,
				"network_cidr": str,
			}, nil),
			Handler: a.toolInstall,
		},
		{
			Name:        "vpn_uninstall",
			Description: "Stop the daemon and remove generated configs. Refuses if active peers exist unless force=true. Args: force?.",
			InputSchema: obj(map[string]any{"force": boolean}, nil),
			Handler:     a.toolUninstall,
		},
		{
			Name: "vpn_peer_add",
			Description: "Allocate the next free IP, generate credentials, hot-reload the daemon (no peer-session drop). " +
				"Returns the client config text plus an inline SVG QR for one-tap mobile import. " +
				"Args: name (req), allowed_ips? (default from config), dns? (default from config), keepalive? (default 25).",
			InputSchema: obj(map[string]any{
				"name":        str,
				"allowed_ips": str,
				"dns":         str,
				"keepalive":   num,
			}, []string{"name"}),
			Handler: a.toolPeerAdd,
		},
		{
			Name:        "vpn_peer_remove",
			Description: "Revoke a peer and re-render the daemon config without it. The peer's keys stay in the DB (revoked_at marked) so the address isn't reused. Args: name.",
			InputSchema: obj(map[string]any{"name": str}, []string{"name"}),
			Handler:     a.toolPeerRemove,
		},
		{
			Name:        "vpn_peer_list",
			Description: "All peers with last_handshake_at, rx_bytes, tx_bytes, address, revoked_at. Args: include_revoked? (default false).",
			InputSchema: obj(map[string]any{"include_revoked": boolean}, nil),
			Handler:     a.toolPeerList,
		},
		{
			Name:        "vpn_peer_config",
			Description: "Re-emit client config text + QR for an existing peer. Credentials are stored server-side, so this works any number of times. Args: name.",
			InputSchema: obj(map[string]any{"name": str}, []string{"name"}),
			Handler:     a.toolPeerConfig,
		},
		{
			Name:        "vpn_announce",
			Description: "Force an immediate stats poll instead of waiting for the next periodic tick. Returns {polled: true|false, error?}.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolAnnounce,
		},
	}
}

// ─── tool handlers ──────────────────────────────────────────────────

func (a *App) toolStatus(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return a.statusSnapshot()
}

func (a *App) toolInstall(_ *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	pid := projectScope()
	if existing, err := getServer(a.ctx.AppDB(), pid); err == nil && existing != nil {
		return nil, fmt.Errorf("already installed (host_id=%d, endpoint=%s) — call vpn_uninstall first to reinstall",
			existing.HostID, existing.Endpoint)
	}

	hostID := int64Arg(args, "host_id", int64(strInt(configString(a.ctx, "host_id", "0"), 0)))
	listenPort := intArg(args, "listen_port", strInt(configString(a.ctx, "listen_port", "51820"), 51820))
	networkCIDR := strArgDef(args, "network_cidr", configString(a.ctx, "network_cidr", "10.13.13.0/24"))
	endpoint := strArgDef(args, "endpoint", configString(a.ctx, "endpoint", ""))
	mtu := strInt(configString(a.ctx, "mtu", "1420"), 1420)

	if endpoint == "" {
		ip, err := hostPublicIP(a.ctx.PlatformAPI(), hostID)
		if err != nil {
			return nil, fmt.Errorf("auto-detect endpoint: %w", err)
		}
		if ip == "" {
			return nil, errors.New("host has no public IP — set the `endpoint` config field manually")
		}
		endpoint = fmt.Sprintf("%s:%d", ip, listenPort)
	}

	host := newHostExec(a.ctx.PlatformAPI(), hostID)
	srv, err := a.backend.Install(context.Background(), host, backend.InstallOpts{
		Endpoint:    endpoint,
		ListenPort:  listenPort,
		NetworkCIDR: networkCIDR,
		MTU:         mtu,
	})
	if err != nil {
		return nil, fmt.Errorf("backend install: %w", err)
	}

	if err := insertServer(a.ctx.AppDB(), pid, hostID, a.backend.Name(), *srv); err != nil {
		// Best-effort rollback: tell the host to remove what we just
		// wrote. Failure here is logged but not bubbled — the operator
		// can run vpn_uninstall manually.
		if uerr := a.backend.Uninstall(context.Background(), host); uerr != nil {
			a.ctx.Logger().Warn("vpn_install: rollback uninstall failed", "err", uerr.Error())
		}
		return nil, fmt.Errorf("persist server: %w", err)
	}

	a.pokePoller()
	return map[string]any{
		"backend":      a.backend.Name(),
		"host_id":      hostID,
		"endpoint":     srv.Endpoint,
		"listen_port":  srv.ListenPort,
		"public_key":   srv.PublicKey,
		"network_cidr": srv.NetworkCIDR,
	}, nil
}

func (a *App) toolUninstall(_ *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	pid := projectScope()
	srv, err := getServer(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}

	force, _ := args["force"].(bool)
	active, _ := countPeers(a.ctx.AppDB(), pid)
	if active > 0 && !force {
		return nil, fmt.Errorf("%d active peers exist; pass force=true to uninstall anyway", active)
	}

	host := newHostExec(a.ctx.PlatformAPI(), srv.HostID)
	if err := a.backend.Uninstall(context.Background(), host); err != nil {
		// Push through DB cleanup anyway — the operator already
		// asked us to tear down. They can fix the host manually.
		a.ctx.Logger().Warn("vpn_uninstall: backend cleanup failed", "err", err.Error())
	}
	if err := deleteServer(a.ctx.AppDB(), pid); err != nil {
		return nil, err
	}
	return map[string]any{"uninstalled": true}, nil
}

func (a *App) toolPeerAdd(_ *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	if strings.ContainsAny(name, " \t\n\r") {
		return nil, errors.New("name cannot contain whitespace")
	}

	pid := projectScope()
	srv, err := getServer(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}

	taken, err := takenAddresses(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, fmt.Errorf("read taken addresses: %w", err)
	}
	addr, err := allocatePeerIP(taken, srv.NetworkCIDR)
	if err != nil {
		return nil, err
	}

	allowed := strArgDef(args, "allowed_ips", configString(a.ctx, "default_allowed_ips", "0.0.0.0/0,::/0"))
	dns := strArgDef(args, "dns", configString(a.ctx, "dns_servers", "1.1.1.1,1.0.0.1"))
	keepalive := intArg(args, "keepalive", strInt(configString(a.ctx, "persistent_keepalive", "25"), 25))

	active, err := activePeersForBackend(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}

	host := newHostExec(a.ctx.PlatformAPI(), srv.HostID)
	out, err := a.backend.AddPeer(context.Background(), host, srv.identity(), active, backend.AddPeerIn{
		Name:       name,
		Address:    addr,
		AllowedIPs: allowed,
		DNS:        dns,
		Keepalive:  keepalive,
	})
	if err != nil {
		return nil, fmt.Errorf("backend add peer: %w", err)
	}

	row, err := insertPeer(a.ctx.AppDB(), pid, out.Peer)
	if err != nil {
		return nil, fmt.Errorf("persist peer: %w", err)
	}

	if configFlag(a.ctx, "archive_configs", false) {
		a.tryArchiveConfig(name, out.ClientConfig)
	}

	a.pokePoller()
	return map[string]any{
		"name":    row.Name,
		"address": row.Address,
		"config":  out.ClientConfig,
		"qr_svg":  renderQRSVG(out.ClientConfig),
	}, nil
}

func (a *App) toolPeerRemove(_ *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	pid := projectScope()
	srv, err := getServer(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	if err := revokePeer(a.ctx.AppDB(), pid, name); err != nil {
		return nil, err
	}
	remaining, err := activePeersForBackend(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	host := newHostExec(a.ctx.PlatformAPI(), srv.HostID)
	if err := a.backend.RemovePeer(context.Background(), host, srv.identity(), remaining); err != nil {
		return nil, fmt.Errorf("backend remove peer: %w", err)
	}
	return map[string]any{"revoked": name}, nil
}

func (a *App) toolPeerList(_ *sdk.AppCtx, args map[string]any) (any, error) {
	include, _ := args["include_revoked"].(bool)
	peers, err := listPeers(a.ctx.AppDB(), projectScope(), include)
	if err != nil {
		return nil, err
	}
	return map[string]any{"peers": peers, "count": len(peers)}, nil
}

func (a *App) toolPeerConfig(_ *sdk.AppCtx, args map[string]any) (any, error) {
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	pid := projectScope()
	srv, err := getServer(a.ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	p, err := getPeerByName(a.ctx.AppDB(), pid, name)
	if err != nil {
		return nil, err
	}
	if p.RevokedAt > 0 {
		return nil, fmt.Errorf("peer %q is revoked; create a new peer instead of re-emitting a revoked one", name)
	}
	conf := a.backend.RenderClientConfig(srv.identity(), p.toBackend())
	return map[string]any{
		"name":    p.Name,
		"address": p.Address,
		"config":  conf,
		"qr_svg":  renderQRSVG(conf),
	}, nil
}

func (a *App) toolAnnounce(_ *sdk.AppCtx, _ map[string]any) (any, error) {
	if err := a.pollOnce(context.Background()); err != nil {
		return map[string]any{"polled": false, "error": err.Error()}, nil
	}
	return map[string]any{"polled": true}, nil
}

// ─── status snapshot ───────────────────────────────────────────────

type StatusOut struct {
	Installed    bool   `json:"installed"`
	Backend      string `json:"backend,omitempty"`
	HostID       int64  `json:"host_id,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`
	PublicKey    string `json:"public_key,omitempty"`
	NetworkCIDR  string `json:"network_cidr,omitempty"`
	InstalledAt  int64  `json:"installed_at,omitempty"`
	ActivePeers  int    `json:"active_peers"`
	RevokedPeers int    `json:"revoked_peers"`
	LastPollAt   int64  `json:"last_poll_at,omitempty"`
	LastPollOK   bool   `json:"last_poll_ok"`
}

func (a *App) statusSnapshot() (*StatusOut, error) {
	pid := projectScope()
	srv, err := getServer(a.ctx.AppDB(), pid)
	if errors.Is(err, errNoServer) {
		return &StatusOut{Installed: false, Backend: a.backend.Name()}, nil
	}
	if err != nil {
		return nil, err
	}
	active, revoked := countPeers(a.ctx.AppDB(), pid)
	return &StatusOut{
		Installed:    true,
		Backend:      srv.Backend,
		HostID:       srv.HostID,
		Endpoint:     srv.Endpoint,
		ListenPort:   srv.ListenPort,
		PublicKey:    srv.PublicKey,
		NetworkCIDR:  srv.NetworkCIDR,
		InstalledAt:  srv.InstalledAt,
		ActivePeers:  active,
		RevokedPeers: revoked,
		LastPollAt:   srv.LastPollAt,
		LastPollOK:   srv.LastPollOK,
	}, nil
}

// ─── stats poller ──────────────────────────────────────────────────

func (a *App) runStatsPoller(ctx context.Context) error {
	interval := time.Duration(strInt(configString(a.ctx, "metrics_poll_seconds", "30"), 30)) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.ctx.Done():
			return nil
		case <-t.C:
		case <-a.pollNotif:
			// On-demand kick (vpn_install / vpn_peer_add / vpn_announce).
		}
		if err := a.pollOnce(ctx); err != nil {
			a.ctx.Logger().Warn("stats poll", "err", err.Error())
		}
	}
}

// pollOnce runs one round of the stats poller. No-ops cleanly when
// the install hasn't run yet — nothing to poll, mark last_poll_ok =
// false but don't error.
func (a *App) pollOnce(ctx context.Context) error {
	pid := projectScope()
	srv, err := getServer(a.ctx.AppDB(), pid)
	if errors.Is(err, errNoServer) {
		return nil
	}
	if err != nil {
		return err
	}
	host := newHostExec(a.ctx.PlatformAPI(), srv.HostID)
	stats, err := a.backend.Stats(ctx, host)
	if err != nil {
		updateServerPoll(a.ctx.AppDB(), pid, false)
		return err
	}
	applyStats(a.ctx.AppDB(), pid, stats)
	updateServerPoll(a.ctx.AppDB(), pid, true)
	return nil
}

func (a *App) pokePoller() {
	select {
	case a.pollNotif <- struct{}{}:
	default:
		// Channel already has a pending kick; one is plenty.
	}
}

// ─── storage archive (optional) ─────────────────────────────────────

func (a *App) tryArchiveConfig(name, content string) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := a.ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
		"path":        "/.vpn/peers/" + name + ".conf",
		"content_b64": base64.StdEncoding.EncodeToString([]byte(content)),
		"overwrite":   true,
	}, &out)
	if err != nil {
		// Storage may be unbound (optional dep) or just temporarily
		// down. We don't fail the peer_add — the config text is
		// already in the response.
		a.ctx.Logger().Warn("archive peer config", "name", name, "err", err.Error())
	}
}

// ─── helpers ────────────────────────────────────────────────────────

func projectScope() string {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid
	}
	return "default"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func strArgDef(args map[string]any, key, def string) string {
	if s := strArg(args, key); s != "" {
		return s
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case string:
			if x, err := jsonNumberToInt(n); err == nil {
				return x
			}
		}
	}
	return def
}

func int64Arg(args map[string]any, key string, def int64) int64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int:
			return int64(n)
		case int64:
			return n
		}
	}
	return def
}

func jsonNumberToInt(s string) (int, error) {
	// JSON-decoded numbers always come back float64 via map[string]any,
	// so the string path only triggers when an upstream caller hand-
	// crafts an arg map with a numeric string. Handle it for forgiving
	// parsing.
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok && v != "" {
		return v
	}
	return def
}

func configFlag(ctx *sdk.AppCtx, key string, def bool) bool {
	switch strings.ToLower(configString(ctx, key, "")) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

// ─── main ───────────────────────────────────────────────────────────

func main() {
	sdk.Run(&App{})
}
