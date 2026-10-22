package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (c config) transports(now time.Time) []string {
	out := []string{"udp", "tcp"}
	if c.tlsValid(now) {
		out = append(out, "tls")
	}
	return out
}

func (c config) publicSettings() sdk.Config {
	peers := make([]string, 0, len(c.allowedPeers))
	for _, peer := range c.allowedPeers {
		peers = append(peers, peer.String())
	}
	return sdk.Config{"tls_mode": c.tlsMode, "dns_zone": c.dnsZone, "dns_project_id": c.dnsProject, "acme_email": c.acmeEmail, "acme_accept_terms": strconv.FormatBool(c.acmeTerms), "public_ip": c.publicIP.String(), "public_host": c.publicHost, "bind_ip": c.bindIP, "realm": c.realm, "credential_ttl_seconds": strconv.Itoa(int(c.ttl / time.Second)), "max_allocations": strconv.Itoa(c.maxAllocations), "allocations_per_credential": strconv.Itoa(c.perCredential), "bytes_per_second": strconv.Itoa(c.bytesPerSecond), "allowed_peer_cidrs": strings.Join(peers, ",")}
}

func (a *App) handleValidateSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Config map[string]string `json:"config"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 192<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Config == nil {
		http.Error(w, "invalid settings request", 400)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "exactly one JSON object required", 400)
		return
	}
	known := map[string]bool{}
	for _, field := range a.Manifest().ConfigSchema {
		known[field.Name] = true
	}
	values := a.cfg.publicSettings()
	values["shared_secret"] = a.cfg.secret
	values["tls_storage_key"] = a.cfg.storageKey
	values["tls_certificate_pem"] = a.cfg.tlsCertificatePEM
	values["tls_private_key_pem"] = a.cfg.tlsPrivateKeyPEM
	for name, value := range request.Config {
		if !known[name] {
			http.Error(w, "unknown TURN setting", 400)
			return
		}
		values[name] = value
	}
	cfg, err := parseConfig(values)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if cfg.tlsMode == "managed" {
		if !domainsBound(a.appCtx) {
			http.Error(w, "Bind Domains before enabling managed TLS", 400)
			return
		}
		if a.project == "" && cfg.dnsProject == "" {
			http.Error(w, "Set the project owning the DNS zone", 400)
			return
		}
		if a.project != "" && cfg.dnsProject != "" && cfg.dnsProject != a.project {
			http.Error(w, "DNS project must match this TURN installation", 400)
			return
		}
	}
	if a.tlsManager != nil && cfg.storageKey != a.cfg.storageKey {
		http.Error(w, "Keep the existing managed certificate storage key when changing settings", 400)
		return
	}
	writeJSON(w, 200, map[string]any{"valid": true, "tls_certificate": cfg.certificateStatus(time.Now())})
}
