// Package backend defines the VPN-protocol-agnostic interface every
// concrete backend (WireGuard today; OpenVPN / IKEv2 later) implements.
//
// Backends are pure host orchestrators: they generate cryptographic
// material, render daemon + client configs, and shell out via HostExec
// to install / reload / introspect the daemon. They never touch the
// app's DB — the orchestrator (main package) owns persistence and IP
// allocation, then asks the Backend to "make it so" on the host.
//
// Lifecycle on a fresh install:
//
//	Install   → backend generates server keys + writes daemon config
//	AddPeer   → backend renders the new peer block, hot-reloads daemon
//	RemovePeer→ backend re-renders the daemon config sans peer
//	Stats     → backend asks the running daemon for per-peer counters
//	Uninstall → backend stops + cleans up files on the host
package backend

import "context"

// HostExec is the surface a Backend uses to act on the remote host.
// Implementations wrap instances.instance_run_command and
// instance_upload_file under one type so Backends don't reach for
// PlatformAPI directly. Run returns stdout+stderr combined.
type HostExec interface {
	Run(ctx context.Context, cmd string, timeoutS int) (output string, exit int, err error)
	Upload(ctx context.Context, path string, content []byte) error
}

// Backend is the contract every VPN-protocol impl satisfies. Methods
// take everything they need by value (server identity, current peer
// list) so the impl is stateless across calls — the orchestrator is
// the source of truth for "what peers exist."
type Backend interface {
	// Name returns the canonical short name ("wireguard").
	Name() string

	// Install lays the daemon down on the host. Returns the server's
	// generated identity (keys, listen socket) for the orchestrator to
	// persist. Idempotent: re-running on a freshly-installed host is
	// expected to no-op the package install but always rewrite config.
	Install(ctx context.Context, host HostExec, opts InstallOpts) (*ServerIdentity, error)

	// Uninstall tears down: stops the service, removes generated
	// daemon config files. Leaves the package installed (operators
	// might want to keep the binary around).
	Uninstall(ctx context.Context, host HostExec) error

	// AddPeer generates fresh credentials for `in.Name` and pushes a
	// daemon config including all `peers` plus the new one. Hot-reload
	// is expected — existing peer sessions must not drop. Returns the
	// new Peer (with generated credentials) and the client config
	// text the user will paste / import.
	AddPeer(ctx context.Context, host HostExec, server ServerIdentity, peers []Peer, in AddPeerIn) (*AddPeerOut, error)

	// RemovePeer re-renders the daemon config with `remaining` as the
	// new peer list and reloads. The orchestrator has already marked
	// the revoked peer in the DB; this call just tells the daemon.
	RemovePeer(ctx context.Context, host HostExec, server ServerIdentity, remaining []Peer) error

	// Stats asks the daemon for per-peer counters. Returns one entry
	// per known peer in the daemon's view; peers that have never
	// handshaken still appear (LastHandshake=0). Cheap to call every
	// few seconds.
	Stats(ctx context.Context, host HostExec) ([]PeerStats, error)

	// RenderClientConfig produces the text a client (WireGuard.app,
	// wg-quick, NetworkManager, …) imports. Pure function — no host
	// I/O, no DB. Used by vpn_peer_config to re-emit configs from
	// stored credentials.
	RenderClientConfig(server ServerIdentity, peer Peer) string
}

// InstallOpts describes what the orchestrator wants the daemon to look
// like. Backends use only the fields they need (WireGuard ignores
// nothing; an OpenVPN backend would ignore MTU and pull other knobs
// from its own config_schema additions).
type InstallOpts struct {
	Endpoint    string // "host:port" advertised to peers
	ListenPort  int    // UDP listen port the daemon binds
	NetworkCIDR string // e.g. "10.13.13.0/24"; server takes .1
	MTU         int
}

// ServerIdentity is what the orchestrator persists after Install. The
// PrivateKey is stored so subsequent reloads (AddPeer / RemovePeer)
// can re-render the daemon config from scratch without consulting the
// host. Endpoint may differ from InstallOpts.Endpoint when the
// orchestrator auto-detected from instances.instance_get.
type ServerIdentity struct {
	PublicKey   string
	PrivateKey  string
	Endpoint    string
	ListenPort  int
	NetworkCIDR string
	MTU         int
}

// AddPeerIn is the orchestrator → backend handoff for one new peer.
// The orchestrator has already picked the Address and validated Name
// uniqueness; AllowedIPs / DNS / Keepalive arrive resolved (config
// defaults applied).
type AddPeerIn struct {
	Name       string
	Address    string // CIDR, e.g. "10.13.13.5/32"
	AllowedIPs string
	DNS        string
	Keepalive  int
}

// AddPeerOut bundles what the orchestrator persists (the Peer) and
// what it returns to the agent (the client config text).
type AddPeerOut struct {
	Peer         Peer
	ClientConfig string
}

// Peer is a peer's stable record. The orchestrator stores this; the
// Backend gets it back on AddPeer (for re-rendering the full daemon
// config) and on RenderClientConfig (for re-emitting).
type Peer struct {
	Name         string
	PublicKey    string
	PrivateKey   string // empty if the backend doesn't store peer privkey
	PresharedKey string
	Address      string
	AllowedIPs   string
	DNS          string
	Keepalive    int
}

// PeerStats is one row of "what does the daemon currently see?". The
// orchestrator joins these against the DB by PublicKey.
type PeerStats struct {
	PublicKey     string
	LastHandshake int64 // unix seconds; 0 if never
	RxBytes       int64
	TxBytes       int64
}
