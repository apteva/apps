package main

import (
	"errors"
	"net"
	"strconv"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// Diagnostic sockets never consume the fixed UDP range reserved for calls.
// Deployments with strict inbound firewalls may configure a separate forwarded
// diagnostic range. Otherwise use OS ephemeral ports outside the call range.
type networkCheckNet struct {
	transport.Net
	reservedMin, reservedMax uint16
}

func (n *networkCheckNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	for i := 0; i < 16; i++ {
		c, err := n.Net.ListenUDP(network, addr)
		if err != nil {
			return nil, err
		}
		port := c.LocalAddr().(*net.UDPAddr).Port
		if n.reservedMin == 0 || port < int(n.reservedMin) || port > int(n.reservedMax) {
			return c, nil
		}
		c.Close()
		if addr.Port != 0 {
			break
		}
	}
	return nil, errors.New("diagnostic UDP ports unavailable outside call range")
}
func networkCheckPorts(c *softphoneRTCConfig, values map[string]string) error {
	lo, hi := values["softphone_network_check_udp_port_min"], values["softphone_network_check_udp_port_max"]
	if lo == "" && hi == "" {
		c.PortMin = 0
		c.PortMax = 0
		return nil
	}
	minPort, e1 := strconv.Atoi(lo)
	maxPort, e2 := strconv.Atoi(hi)
	if e1 != nil || e2 != nil || minPort < 1024 || maxPort > 65535 || maxPort < minPort {
		return errors.New("invalid diagnostic UDP range")
	}
	if c.PortMin != 0 && minPort <= int(c.PortMax) && maxPort >= int(c.PortMin) {
		return errors.New("diagnostic UDP range overlaps call range")
	}
	c.PortMin = uint16(minPort)
	c.PortMax = uint16(maxPort)
	return nil
}
func networkCheckNetwork(lo, hi uint16) (*networkCheckNet, error) {
	base, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return &networkCheckNet{base, lo, hi}, nil
}
