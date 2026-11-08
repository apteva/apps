package main

import (
	"fmt"
	"net"
)

func (b *Broker) endpointStatus() map[string]any {
	ready := true
	scheme := "mqtt"
	out := map[string]any{"advertised_host": b.transport.host, "advertised_port": b.transport.publicPort, "tls": b.transport.mode == "tls", "mode": b.transport.mode}
	if b.certificates != nil {
		certificate := b.certificates.status()
		out["certificate"] = certificate
		ready, _ = certificate["ready"].(bool)
		scheme = "mqtts"
	}
	out["ready"] = ready
	out["endpoint"] = ""
	if ready {
		out["endpoint"] = scheme + "://" + net.JoinHostPort(b.transport.host, fmt.Sprint(b.transport.publicPort))
	}
	return out
}
