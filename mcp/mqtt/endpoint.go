package main

import (
	"fmt"
	"net"
	"net/http"
)

// The supervisor checks the socket selected by transport_mode through this
// route. Enrollment tracks certificate availability separately so ACME can
// use the listening socket while obtaining the initial certificate.
func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.broker == nil || a.broker.listener == nil || !a.broker.listener.serving.Load() ||
		a.durable == nil || !a.durable.ready.Load() || a.durable.failed.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

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
	if b.app.durable != nil {
		ready = ready && b.app.durable.ready.Load() && !b.app.durable.failed.Load()
	}
	out["durable_acceptance"] = ready
	out["ready"] = ready
	out["endpoint"] = ""
	if ready {
		out["endpoint"] = scheme + "://" + net.JoinHostPort(b.transport.host, fmt.Sprint(b.transport.publicPort))
	}
	return out
}
