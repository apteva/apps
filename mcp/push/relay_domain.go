package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultRelayHostname = ""

type relayDomainConfig struct {
	Hostname   string
	ProjectID  string
	DNSManaged bool
	DNSDomain  string
	DNSName    string
	DNSType    string
	DNSValue   string
	DNSError   string
	CreatedAt  string
	UpdatedAt  string
}

type relayDNSStatus struct {
	Managed bool   `json:"managed"`
	Domain  string `json:"domain,omitempty"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Value   string `json:"value,omitempty"`
	Error   string `json:"error,omitempty"`
}

type relayDomainState struct {
	Configured   bool              `json:"configured"`
	Hostname     string            `json:"hostname"`
	RelayURL     string            `json:"relay_url,omitempty"`
	ProjectID    string            `json:"project_id,omitempty"`
	DomainsBound bool              `json:"domains_bound"`
	DNS          relayDNSStatus    `json:"dns"`
	Route        *sdk.IngressRoute `json:"route,omitempty"`
	RouteError   string            `json:"route_error,omitempty"`
}

type configureRelayDomainRequest struct {
	Hostname  string `json:"hostname"`
	ProjectID string `json:"project_id"`
	AutoDNS   *bool  `json:"auto_dns"`
	DNSTarget string `json:"dns_target"`
}

func (a *App) handleRelayDomainStatus(w http.ResponseWriter, _ *http.Request) {
	state, err := a.currentRelayDomainState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load relay domain")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleConfigureRelayDomain(w http.ResponseWriter, r *http.Request) {
	var input configureRelayDomainRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hostname, err := normalizeRelayHostname(input.Hostname)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.DNSTarget = normalizeDNSHost(input.DNSTarget)
	autoDNS := input.AutoDNS == nil || *input.AutoDNS

	previous, previousErr := a.store.relayDomain()
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "could not load existing relay domain")
		return
	}
	route, err := a.ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    "app://push",
		ProjectID: input.ProjectID,
		OwnerKind: "push",
		CertFQDN:  hostname,
		AllowHTTP: false,
		TLSMode:   "auto",
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not expose relay ingress: "+err.Error())
		return
	}

	dns := a.configureRelayDNS(hostname, input.ProjectID, input.DNSTarget, autoDNS)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	config := &relayDomainConfig{
		Hostname:   hostname,
		ProjectID:  input.ProjectID,
		DNSManaged: dns.Managed,
		DNSDomain:  dns.Domain,
		DNSName:    dns.Name,
		DNSType:    dns.Type,
		DNSValue:   dns.Value,
		DNSError:   dns.Error,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if previous != nil {
		config.CreatedAt = previous.CreatedAt
	}
	if err := a.store.saveRelayDomain(config); err != nil {
		if previous == nil || previous.Hostname != hostname {
			_ = a.ctx.PlatformAPI().UnexposeIngress(hostname)
		}
		writeError(w, http.StatusInternalServerError, "could not save relay domain")
		return
	}
	if previous != nil && previous.Hostname != hostname {
		_ = a.ctx.PlatformAPI().UnexposeIngress(previous.Hostname)
	}

	a.ctx.Emit("relay.domain_configured", map[string]any{
		"hostname":    hostname,
		"project_id":  input.ProjectID,
		"dns_managed": dns.Managed,
	})
	status := http.StatusCreated
	if previous != nil {
		status = http.StatusOK
	}
	writeJSON(w, status, a.relayDomainState(config, route, ""))
}

func (a *App) handleDetachRelayDomain(w http.ResponseWriter, r *http.Request) {
	config, err := a.store.relayDomain()
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, a.relayDomainState(nil, nil, ""))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load relay domain")
		return
	}
	if err := a.ctx.PlatformAPI().UnexposeIngress(config.Hostname); err != nil {
		writeError(w, http.StatusBadGateway, "could not remove relay ingress: "+err.Error())
		return
	}

	removeDNS := r.URL.Query().Get("remove_dns") == "true"
	dnsError := ""
	if removeDNS && config.DNSManaged {
		dnsError = a.removeRelayDNS(config)
	}
	if err := a.store.deleteRelayDomain(); err != nil {
		writeError(w, http.StatusInternalServerError, "relay route was removed but local state could not be cleared")
		return
	}
	a.ctx.Emit("relay.domain_detached", map[string]any{
		"hostname":    config.Hostname,
		"dns_removed": removeDNS && dnsError == "",
	})
	state := a.relayDomainState(nil, nil, "")
	if dnsError != "" {
		state.DNS.Error = dnsError
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) currentRelayDomainState() (*relayDomainState, error) {
	config, err := a.store.relayDomain()
	if errors.Is(err, sql.ErrNoRows) {
		return a.relayDomainState(nil, nil, ""), nil
	}
	if err != nil {
		return nil, err
	}
	routes, routeErr := a.ctx.PlatformAPI().ListIngressRoutes()
	if routeErr != nil {
		return a.relayDomainState(config, nil, routeErr.Error()), nil
	}
	for i := range routes {
		if strings.EqualFold(routes[i].Hostname, config.Hostname) {
			return a.relayDomainState(config, &routes[i], ""), nil
		}
	}
	return a.relayDomainState(config, nil, "The configured hostname is not currently exposed by platform ingress."), nil
}

func (a *App) relayDomainState(config *relayDomainConfig, route *sdk.IngressRoute, routeError string) *relayDomainState {
	state := &relayDomainState{
		Hostname:     defaultRelayHostname,
		DomainsBound: a.ctx.IntegrationFor("domains") != nil,
		Route:        route,
		RouteError:   routeError,
	}
	if config == nil {
		return state
	}
	state.Configured = true
	state.Hostname = config.Hostname
	state.RelayURL = "https://" + config.Hostname
	state.ProjectID = config.ProjectID
	state.DNS = relayDNSStatus{
		Managed: config.DNSManaged,
		Domain:  config.DNSDomain,
		Name:    config.DNSName,
		Type:    config.DNSType,
		Value:   config.DNSValue,
		Error:   config.DNSError,
	}
	return state
}

func (a *App) configureRelayDNS(hostname, projectID, target string, autoDNS bool) relayDNSStatus {
	if target == "" {
		target = platformDNSHost(a.ctx)
	}
	dns := suggestedRelayDNS(hostname, target)
	if !autoDNS {
		dns.Error = "Automatic DNS is disabled; create this record manually."
		return dns
	}
	if a.ctx.IntegrationFor("domains") == nil {
		dns.Error = "Domains is not bound; create this record manually."
		return dns
	}
	if projectID == "" {
		dns.Error = "A project is required to publish DNS through Domains."
		return dns
	}
	if target == "" {
		dns.Error = "The platform Public URL is unset; provide a DNS target or create the record manually."
		return dns
	}
	domain, name, err := resolveRelayManagedApex(a.ctx, projectID, hostname)
	if err != nil {
		dns.Error = err.Error()
		return dns
	}
	dns.Domain = domain
	dns.Name = name
	dns.Type = dnsTypeForTarget(target)
	dns.Value = target
	if dns.Type == "CNAME" && name == "@" {
		dns.Error = "Apex CNAME records are not supported; use a relay subdomain or configure an A/ALIAS record manually."
		return dns
	}
	if strings.EqualFold(hostname, target) {
		dns.Error = "The relay DNS record cannot point to itself; set the platform Public URL to its underlying host."
		return dns
	}
	var output map[string]any
	err = a.ctx.WithProject(projectID).PlatformAPI().CallAppResult("domains", "domain_records_set", map[string]any{
		"domain": domain,
		"name":   name,
		"type":   dns.Type,
		"value":  target,
		"ttl":    300,
	}, &output)
	if err != nil {
		dns.Error = "Could not publish DNS through Domains: " + err.Error()
		return dns
	}
	dns.Managed = true
	return dns
}

func (a *App) removeRelayDNS(config *relayDomainConfig) string {
	if a.ctx.IntegrationFor("domains") == nil {
		return "Relay ingress was removed, but Domains is not bound so its DNS record was retained."
	}
	var output map[string]any
	err := a.ctx.WithProject(config.ProjectID).PlatformAPI().CallAppResult("domains", "domain_records_delete", map[string]any{
		"domain": config.DNSDomain,
		"name":   config.DNSName,
		"type":   config.DNSType,
	}, &output)
	if err != nil {
		return "Relay ingress was removed, but its DNS record could not be deleted: " + err.Error()
	}
	return ""
}

func resolveRelayManagedApex(ctx *sdk.AppCtx, projectID, hostname string) (string, string, error) {
	var response struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := ctx.WithProject(projectID).PlatformAPI().CallAppResult(
		"domains",
		"domain_list",
		map[string]any{},
		&response,
	); err != nil {
		return "", "", fmt.Errorf("could not read Domains inventory: %w", err)
	}
	best := ""
	for _, item := range response.Domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(item.Name), "."))
		if hostname == domain || strings.HasSuffix(hostname, "."+domain) {
			if len(domain) > len(best) {
				best = domain
			}
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("no Domains entry owns %q", hostname)
	}
	if hostname == best {
		return best, "@", nil
	}
	return best, strings.TrimSuffix(hostname[:len(hostname)-len(best)], "."), nil
}

func suggestedRelayDNS(hostname, target string) relayDNSStatus {
	parts := strings.Split(hostname, ".")
	domain := hostname
	name := "@"
	if len(parts) > 2 {
		domain = strings.Join(parts[len(parts)-2:], ".")
		name = strings.Join(parts[:len(parts)-2], ".")
	}
	return relayDNSStatus{
		Domain: domain,
		Name:   name,
		Type:   dnsTypeForTarget(target),
		Value:  target,
	}
}

func dnsTypeForTarget(target string) string {
	ip := net.ParseIP(target)
	if ip == nil {
		return "CNAME"
	}
	if ip.To4() != nil {
		return "A"
	}
	return "AAAA"
}

func platformDNSHost(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil {
			if host := normalizeDNSHost(info.PublicURL); host != "" {
				return host
			}
		}
	}
	for _, key := range []string{"APTEVA_PUBLIC_HOST", "APTEVA_PUBLIC_URL", "PUBLIC_URL"} {
		if host := normalizeDNSHost(os.Getenv(key)); host != "" {
			return host
		}
	}
	return ""
}

func normalizeDNSHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "//" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	if host == "" && parsed.Scheme == "" {
		host = parsed.Path
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func normalizeRelayHostname(value string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if host == "" {
		return "", errors.New("hostname is required")
	}
	if len(host) > 253 || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return "", errors.New("hostname must be a valid public DNS name")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("hostname must be a valid public DNS name")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("hostname must be a valid public DNS name")
			}
		}
	}
	return host, nil
}

func (s *store) relayDomain() (*relayDomainConfig, error) {
	var config relayDomainConfig
	var managed int
	err := s.db.QueryRow(`
		SELECT hostname, project_id, dns_managed, dns_domain, dns_name, dns_type,
		       dns_value, dns_error, created_at, updated_at
		FROM relay_domain_config WHERE id = 1`).
		Scan(
			&config.Hostname,
			&config.ProjectID,
			&managed,
			&config.DNSDomain,
			&config.DNSName,
			&config.DNSType,
			&config.DNSValue,
			&config.DNSError,
			&config.CreatedAt,
			&config.UpdatedAt,
		)
	if err != nil {
		return nil, err
	}
	config.DNSManaged = managed != 0
	return &config, nil
}

func (s *store) saveRelayDomain(config *relayDomainConfig) error {
	managed := 0
	if config.DNSManaged {
		managed = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO relay_domain_config
			(id, hostname, project_id, dns_managed, dns_domain, dns_name, dns_type,
			 dns_value, dns_error, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			project_id = excluded.project_id,
			dns_managed = excluded.dns_managed,
			dns_domain = excluded.dns_domain,
			dns_name = excluded.dns_name,
			dns_type = excluded.dns_type,
			dns_value = excluded.dns_value,
			dns_error = excluded.dns_error,
			updated_at = excluded.updated_at`,
		config.Hostname,
		config.ProjectID,
		managed,
		config.DNSDomain,
		config.DNSName,
		config.DNSType,
		config.DNSValue,
		config.DNSError,
		config.CreatedAt,
		config.UpdatedAt,
	)
	return err
}

func (s *store) deleteRelayDomain() error {
	_, err := s.db.Exec(`DELETE FROM relay_domain_config WHERE id = 1`)
	return err
}
