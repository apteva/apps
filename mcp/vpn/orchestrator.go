package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strconv"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/vpn/backend"
	"github.com/apteva/apps/mcp/vpn/backend/wireguard"
)

// hostExec adapts ctx.PlatformAPI().CallAppResult against the
// `instances` app into the backend.HostExec interface. One value per
// (install, host_id) — cheap to construct, no caching needed.
type hostExec struct {
	api    sdk.PlatformClient
	hostID int64
}

func newHostExec(api sdk.PlatformClient, hostID int64) backend.HostExec {
	return &hostExec{api: api, hostID: hostID}
}

func (h *hostExec) Run(_ context.Context, cmd string, timeoutS int) (string, int, error) {
	if timeoutS <= 0 {
		timeoutS = 60
	}
	var out struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Err      string `json:"error"`
	}
	err := h.api.CallAppResult("instances", "instance_run_command", map[string]any{
		"id":        h.hostID,
		"cmd":       cmd,
		"timeout_s": timeoutS,
	}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("instance_run_command host=%d: %w", h.hostID, err)
	}
	// `instances` surfaces command-level failures as a populated
	// `error` field (process couldn't start, SSH lost, etc.) rather
	// than a non-zero exit. Treat that as an error so callers can
	// distinguish "command failed to run" from "command ran, said no".
	if out.Err != "" {
		return out.Output, out.ExitCode, errors.New(out.Err)
	}
	return out.Output, out.ExitCode, nil
}

func (h *hostExec) Upload(_ context.Context, path string, content []byte) error {
	var out struct {
		BytesWritten int `json:"bytes_written"`
	}
	return h.api.CallAppResult("instances", "instance_upload_file", map[string]any{
		"id":          h.hostID,
		"path":        path,
		"content_b64": base64.StdEncoding.EncodeToString(content),
	}, &out)
}

// hostPublicIP asks the instances app for the host's public IPv4
// (preferred) or v6 (bracketed for use in a URL/endpoint). Returns
// the empty string when the host has neither, so callers can decide
// whether that's fatal or just "force the operator to set `endpoint`
// in config".
func hostPublicIP(api sdk.PlatformClient, hostID int64) (string, error) {
	var out struct {
		Instance struct {
			PublicIPv4 string `json:"public_ipv4"`
			PublicIPv6 string `json:"public_ipv6"`
			Status     string `json:"status"`
		} `json:"instance"`
	}
	if err := api.CallAppResult("instances", "instance_get", map[string]any{"id": hostID}, &out); err != nil {
		return "", fmt.Errorf("instance_get %d: %w", hostID, err)
	}
	if out.Instance.PublicIPv4 != "" {
		return out.Instance.PublicIPv4, nil
	}
	if out.Instance.PublicIPv6 != "" {
		return "[" + out.Instance.PublicIPv6 + "]", nil
	}
	return "", nil
}

// pickBackend resolves the configured backend name into a Backend
// impl. Empty / "wireguard" → WireGuard. Anything else is an error —
// we don't silently fall back, otherwise a typo in config silently
// installs the wrong daemon.
func pickBackend(name string) (backend.Backend, error) {
	switch name {
	case "", "wireguard":
		return wireguard.New(), nil
	default:
		return nil, fmt.Errorf("backend %q not supported (v0.1 ships only 'wireguard')", name)
	}
}

// allocatePeerIP picks the next unused /32 inside the server's
// network_cidr. Sequential allocation; .1 is reserved for the
// server. With a /24 we top out at 253 simultaneous active peers,
// which is fine for the homelab / small-team scope. If a larger
// network is configured we just walk further.
//
// Revoked peers' addresses are *not* reused — that would let a
// returning device get a different revoked peer's address and shadow
// it in client routing tables. Cheap to keep the address claimed for
// the install's life.
func allocatePeerIP(taken []string, cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("parse network_cidr %q: %w", cidr, err)
	}
	used := map[string]bool{}
	for _, t := range taken {
		// taken entries are stored as "10.13.13.5/32"; key on the
		// addr only so we ignore mask variance.
		if a, err := netip.ParsePrefix(t); err == nil {
			used[a.Addr().String()] = true
		}
	}
	// Server is .1.
	srv := serverAddr(prefix)
	used[srv.String()] = true

	a := prefix.Addr()
	for {
		a = a.Next()
		if !prefix.Contains(a) {
			return "", errors.New("network_cidr exhausted — no free peer addresses")
		}
		if isNetworkOrBroadcast(a, prefix) {
			continue
		}
		if used[a.String()] {
			continue
		}
		return a.String() + "/32", nil
	}
}

// serverAddr returns the .1 host inside the prefix. We just bump the
// network address once; for /24 that's network.1, for /16 that's also
// network.1. Good enough — operators wanting a different server
// address can override at install time later (not exposed today).
func serverAddr(p netip.Prefix) netip.Addr {
	return p.Addr().Next()
}

// isNetworkOrBroadcast returns true for the all-zeros or all-ones
// host inside the prefix. We skip both for peer allocation — they're
// reserved by IP convention even though Linux would technically let
// us assign them.
func isNetworkOrBroadcast(a netip.Addr, p netip.Prefix) bool {
	if a == p.Addr() {
		return true
	}
	// Broadcast = network OR (NOT mask). For v4 /24 = .255, /16 = .255.255, etc.
	if a.Is4() {
		bits := p.Bits()
		hostBits := 32 - bits
		if hostBits <= 0 {
			return false
		}
		networkBytes := p.Addr().As4()
		var bcast [4]byte
		copy(bcast[:], networkBytes[:])
		// Apply the host-side ones.
		for i := 3; i >= 0 && hostBits > 0; i-- {
			take := hostBits
			if take > 8 {
				take = 8
			}
			mask := byte((1 << take) - 1)
			bcast[i] |= mask
			hostBits -= take
		}
		return netip.AddrFrom4(bcast) == a
	}
	return false
}

// strInt parses a config int with a default. Keeps callers terse.
func strInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
