package wireguard

import (
	"fmt"
	"strings"

	"github.com/apteva/apps/mcp/vpn/backend"
)

// renderServerConf builds the full /etc/wireguard/wg0.conf — interface
// block + one [Peer] block per active peer. The PostUp / PostDown
// rules turn the host into a NAT gateway for tunnel traffic; without
// them peers can reach the host but not the wider internet.
//
// Note: we deliberately pin the egress interface as `eth0`. Hetzner
// VPS images all expose their public NIC as eth0. If the orchestrator
// later targets hosts where that's wrong (e.g. Vultr images with
// `enp*` names), make this a config knob.
func renderServerConf(s backend.ServerIdentity, peers []backend.Peer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", s.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", serverAddressFromCIDR(s.NetworkCIDR))
	fmt.Fprintf(&b, "ListenPort = %d\n", s.ListenPort)
	if s.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", s.MTU)
	}
	b.WriteString("PostUp = iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE; iptables -A FORWARD -i wg0 -j ACCEPT; iptables -A FORWARD -o wg0 -j ACCEPT\n")
	b.WriteString("PostDown = iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE; iptables -D FORWARD -i wg0 -j ACCEPT; iptables -D FORWARD -o wg0 -j ACCEPT\n")

	for _, p := range peers {
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "# name: %s\n", p.Name)
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		if p.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", p.Address)
	}
	return b.String()
}

// renderClientConf is the file the user pastes into WireGuard.app /
// wg-quick / NetworkManager. Symmetric to renderServerConf but with
// the perspective flipped — the client's Interface block holds *its*
// keys + assigned address; the single [Peer] block is the server.
func renderClientConf(s backend.ServerIdentity, p backend.Peer) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", p.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", p.Address)
	if p.DNS != "" {
		fmt.Fprintf(&b, "DNS = %s\n", p.DNS)
	}
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", s.PublicKey)
	if p.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
	}
	fmt.Fprintf(&b, "Endpoint = %s\n", s.Endpoint)
	allowed := p.AllowedIPs
	if allowed == "" {
		allowed = "0.0.0.0/0, ::/0"
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", allowed)
	if p.Keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
	}
	return b.String()
}

// serverAddressFromCIDR turns "10.13.13.0/24" into "10.13.13.1/24" —
// the server always takes the .1 host. Best-effort: if the input is
// malformed we hand it back untouched and let wg-quick complain at
// service-start.
func serverAddressFromCIDR(cidr string) string {
	slash := strings.Index(cidr, "/")
	if slash < 0 {
		return cidr
	}
	netPart, mask := cidr[:slash], cidr[slash:]
	dot := strings.LastIndex(netPart, ".")
	if dot < 0 {
		return cidr
	}
	return netPart[:dot] + ".1" + mask
}
