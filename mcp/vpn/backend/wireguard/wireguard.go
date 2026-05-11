// Package wireguard implements the backend.Backend interface for
// WireGuard on a Debian / Ubuntu host. All host I/O is funnelled
// through backend.HostExec — never net.Dial / os.Exec from here.
package wireguard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apteva/apps/mcp/vpn/backend"
)

const (
	confPath  = "/etc/wireguard/wg0.conf"
	iface     = "wg0"
	installTO = 300 // apt-get can take a while on a fresh image
	reloadTO  = 30
	pollTO    = 15
)

type Backend struct{}

// New returns a fresh WireGuard backend. Stateless — safe to share.
func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return "wireguard" }

// Install: package install → keypair → first wg0.conf (no peers yet)
// → enable systemd unit → sysctl forwarding. Idempotent: re-running on
// an already-installed host rewrites the config and restarts the unit.
func (b *Backend) Install(ctx context.Context, host backend.HostExec, opts backend.InstallOpts) (*backend.ServerIdentity, error) {
	if _, _, err := host.Run(ctx,
		"DEBIAN_FRONTEND=noninteractive apt-get update -y && "+
			"DEBIAN_FRONTEND=noninteractive apt-get install -y wireguard iptables",
		installTO,
	); err != nil {
		return nil, fmt.Errorf("apt-get install wireguard: %w", err)
	}

	priv, pub, err := generateKeypair()
	if err != nil {
		return nil, err
	}

	srv := backend.ServerIdentity{
		PublicKey:   pub,
		PrivateKey:  priv,
		Endpoint:    opts.Endpoint,
		ListenPort:  opts.ListenPort,
		NetworkCIDR: opts.NetworkCIDR,
		MTU:         opts.MTU,
	}

	if err := writeServerConf(ctx, host, srv, nil); err != nil {
		return nil, err
	}

	// IPv4 forwarding — wg-quick can do this via sysctl but the
	// PostUp ruleset assumes it; persist it so reboots don't break
	// the install. The `tee` form is so `>` redirection inherits root.
	if _, _, err := host.Run(ctx,
		"echo 'net.ipv4.ip_forward=1' | tee /etc/sysctl.d/99-vpn-app.conf >/dev/null && "+
			"sysctl -w net.ipv4.ip_forward=1",
		reloadTO,
	); err != nil {
		return nil, fmt.Errorf("enable ip_forward: %w", err)
	}

	// Enable + start the unit. wg-quick@wg0 reads /etc/wireguard/wg0.conf.
	if _, _, err := host.Run(ctx,
		"systemctl enable --now wg-quick@"+iface,
		reloadTO,
	); err != nil {
		return nil, fmt.Errorf("systemctl enable wg-quick@%s: %w", iface, err)
	}

	return &srv, nil
}

func (b *Backend) Uninstall(ctx context.Context, host backend.HostExec) error {
	// Best-effort stop + disable; if the unit isn't there we still
	// proceed to clean files.
	_, _, _ = host.Run(ctx, "systemctl disable --now wg-quick@"+iface, reloadTO)
	if _, _, err := host.Run(ctx, "rm -f "+confPath+" /etc/sysctl.d/99-vpn-app.conf && sysctl -w net.ipv4.ip_forward=0", reloadTO); err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	return nil
}

// AddPeer generates fresh credentials, returns the Peer for the
// orchestrator to store, then asks the daemon to pick up the new
// peer list via `wg syncconf` — that's the hot-reload incantation
// that doesn't drop existing peer sessions.
func (b *Backend) AddPeer(
	ctx context.Context, host backend.HostExec,
	server backend.ServerIdentity, peers []backend.Peer,
	in backend.AddPeerIn,
) (*backend.AddPeerOut, error) {
	priv, pub, err := generateKeypair()
	if err != nil {
		return nil, err
	}
	psk, err := generatePSK()
	if err != nil {
		return nil, err
	}
	newPeer := backend.Peer{
		Name:         in.Name,
		PublicKey:    pub,
		PrivateKey:   priv,
		PresharedKey: psk,
		Address:      in.Address,
		AllowedIPs:   in.AllowedIPs,
		DNS:          in.DNS,
		Keepalive:    in.Keepalive,
	}

	full := append(peers, newPeer)
	if err := writeServerConf(ctx, host, server, full); err != nil {
		return nil, err
	}
	if err := syncConf(ctx, host); err != nil {
		return nil, err
	}

	return &backend.AddPeerOut{
		Peer:         newPeer,
		ClientConfig: renderClientConf(server, newPeer),
	}, nil
}

func (b *Backend) RemovePeer(
	ctx context.Context, host backend.HostExec,
	server backend.ServerIdentity, remaining []backend.Peer,
) error {
	if err := writeServerConf(ctx, host, server, remaining); err != nil {
		return err
	}
	return syncConf(ctx, host)
}

// Stats parses `wg show wg0 dump`. Hot path — called every
// metrics_poll_seconds (default 30) — so we keep the timeout tight.
func (b *Backend) Stats(ctx context.Context, host backend.HostExec) ([]backend.PeerStats, error) {
	out, exit, err := host.Run(ctx, "wg show "+iface+" dump", pollTO)
	if err != nil {
		return nil, fmt.Errorf("wg show dump: %w (exit=%d)", err, exit)
	}
	if exit != 0 {
		return nil, fmt.Errorf("wg show dump exit=%d: %s", exit, strings.TrimSpace(out))
	}
	return parseDump(out), nil
}

func (b *Backend) RenderClientConfig(server backend.ServerIdentity, peer backend.Peer) string {
	return renderClientConf(server, peer)
}

// ─── helpers ────────────────────────────────────────────────────────

// writeServerConf renders + uploads /etc/wireguard/wg0.conf to the
// host. wg-quick reads this file on startup; `wg syncconf` reads
// the stripped form for hot reload.
func writeServerConf(ctx context.Context, host backend.HostExec, srv backend.ServerIdentity, peers []backend.Peer) error {
	content := renderServerConf(srv, peers)
	if err := host.Upload(ctx, confPath, []byte(content)); err != nil {
		return fmt.Errorf("upload %s: %w", confPath, err)
	}
	// wg-quick refuses to load a world-readable config. Same chmod
	// every WG-on-Linux tutorial mandates.
	if _, _, err := host.Run(ctx, "chmod 600 "+confPath, reloadTO); err != nil {
		return fmt.Errorf("chmod %s: %w", confPath, err)
	}
	return nil
}

// syncConf is the hot-reload incantation. `wg-quick strip` drops the
// PostUp/PostDown lines (which `wg syncconf` doesn't understand); the
// resulting stream goes straight to the kernel module via stdin.
// Existing peer sessions stay up.
func syncConf(ctx context.Context, host backend.HostExec) error {
	_, exit, err := host.Run(ctx,
		"wg syncconf "+iface+" <(wg-quick strip "+iface+")",
		reloadTO,
	)
	if err != nil || exit != 0 {
		// Process-substitution `<()` needs bash, not sh. Some minimal
		// images symlink /bin/sh → dash, which fails the above. Retry
		// via an explicit bash -c. If bash is missing we surface the
		// original error.
		_, exit2, err2 := host.Run(ctx,
			`bash -c 'wg syncconf `+iface+` <(wg-quick strip `+iface+`)'`,
			reloadTO,
		)
		if err2 == nil && exit2 == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wg syncconf: %w", err)
		}
		return errors.New("wg syncconf exited non-zero")
	}
	return nil
}
