package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/gobwas/ws"
)

// Identity comes from the stored call, never from a browser's headers or URL.
func (a *App) correlatedBridgeDialer(row *callRow) ws.Dialer {
	h := http.Header{}
	if row != nil {
		h.Set("X-Apteva-Call-ID", row.ID)
		if id := boundedTransportID(row.ThreadID); id != "" {
			h.Set("X-Apteva-Thread-ID", id)
		}
	}
	return ws.Dialer{Header: ws.HandshakeHeaderHTTP(h), OnHeader: func(key, value []byte) error {
		if row != nil && strings.EqualFold(string(key), proxyConnectionHeader) {
			if id := boundedTransportID(string(value)); id != "" {
				at := ringTime(time.Now())
				e := carrierBridgeEvidence{Kind: "core_proxy_handshake", Leg: "core", At: at, ProxyConnectionID: id}
				a.carrierTelemetry.enqueue(carrierTelemetryRecord{InstallID: a.installID, CallID: row.ID, ProjectID: row.ProjectID, Kind: "event", At: at, ProxyConnectionID: id, Event: &e})
			}
		}
		return nil
	}}
}
