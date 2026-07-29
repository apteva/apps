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

type configureDomainInput struct {
	BaseDomain string `json:"base_domain"`
	ProjectID  string `json:"project_id"`
	AutoDNS    *bool  `json:"auto_dns"`
	DNSTarget  string `json:"dns_target"`
}

func (a *App) handleConfigureDomain(w http.ResponseWriter, r *http.Request) {
	var input configureDomainInput
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID := requestProjectID(r, input.ProjectID)
	cfg, status, err := a.configureDomain(projectID, input)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, status, configResponse(
		cfg,
		a.ctx.IntegrationFor("domains") != nil,
		requestProjectID(r, ""),
	))
}

func (a *App) configureDomain(projectID string, input configureDomainInput) (*tunnelConfig, int, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()

	baseDomain, err := normalizeDomain(input.BaseDomain)
	if err != nil {
		return nil, 0, newClientError(err.Error())
	}
	if projectID == "" {
		return nil, 0, newClientError("project_id is required")
	}
	existing, loadErr := a.store.config()
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return nil, 0, loadErr
	}
	if existing != nil && existing.ProjectID != projectID {
		return nil, 0, newForbiddenError("the tunnel domain is managed by its operator project")
	}
	if existing != nil && existing.BaseDomain != baseDomain {
		hasActive, err := a.store.anyActiveTunnels()
		if err != nil {
			return nil, 0, err
		}
		if hasActive {
			return nil, 0, newConflictError("delete active tunnels before changing the base domain")
		}
	}
	autoDNS := input.AutoDNS == nil || *input.AutoDNS
	dns := a.configureWildcardDNS(projectID, baseDomain, input.DNSTarget, autoDNS)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cfg := &tunnelConfig{
		BaseDomain: baseDomain,
		ProjectID:  projectID,
		DNS:        dns,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	status := http.StatusCreated
	if existing != nil {
		cfg.CreatedAt = existing.CreatedAt
		status = http.StatusOK
	}
	if err := a.store.saveConfig(cfg); err != nil {
		return nil, 0, err
	}
	a.ctx.EmitWithProject("tunnel.domain_configured", projectID, map[string]any{
		"base_domain": baseDomain,
		"dns_managed": dns.Managed,
	})
	return cfg, status, nil
}

func configResponse(cfg *tunnelConfig, domainsBound bool, callerProjectID string) map[string]any {
	return map[string]any{
		"configured":          true,
		"base_domain":         cfg.BaseDomain,
		"operator_project_id": cfg.ProjectID,
		"can_configure":       callerProjectID != "" && callerProjectID == cfg.ProjectID,
		"domains_bound":       domainsBound,
		"dns":                 cfg.DNS,
		"created_at":          cfg.CreatedAt,
		"updated_at":          cfg.UpdatedAt,
	}
}

func (a *App) configureWildcardDNS(projectID, baseDomain, requestedTarget string, auto bool) dnsStatus {
	target := normalizeDNSHost(requestedTarget)
	if target == "" {
		target = platformDNSHost(a.ctx)
	}
	dns := dnsStatus{
		Type:  dnsTypeForTarget(target),
		Value: target,
	}
	if a.ctx.IntegrationFor("domains") == nil {
		dns.Domain, dns.Name = suggestedDNSParts(baseDomain)
		dns.Error = "Domains is not bound; create the wildcard DNS record manually."
		return dns
	}
	domain, name, err := resolveManagedDNSZone(a.ctx, projectID, baseDomain)
	if err != nil {
		dns.Domain, dns.Name = suggestedDNSParts(baseDomain)
		dns.Error = err.Error()
		return dns
	}
	dns.Domain = domain
	if name == "@" {
		dns.Name = "*"
	} else {
		dns.Name = "*." + name
	}
	if !auto {
		dns.Error = "Automatic DNS is disabled; create the wildcard record manually."
		return dns
	}
	if target == "" {
		dns.Error = "The platform Public URL is unset; provide a DNS target or create the wildcard record manually."
		return dns
	}
	if dns.Type == "CNAME" && strings.EqualFold(target, baseDomain) {
		dns.Error = "The tunnel wildcard cannot point to its own base domain."
		return dns
	}
	var output map[string]any
	err = a.ctx.WithProject(projectID).PlatformAPI().CallAppResult(
		"domains",
		"domain_records_set",
		map[string]any{
			"domain": domain,
			"name":   dns.Name,
			"type":   dns.Type,
			"value":  target,
			"ttl":    300,
		},
		&output,
	)
	if err != nil {
		dns.Error = "Could not publish wildcard DNS through Domains: " + err.Error()
		return dns
	}
	dns.Managed = true
	return dns
}

func resolveManagedDNSZone(ctx *sdk.AppCtx, projectID, hostname string) (string, string, error) {
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

func suggestedDNSParts(baseDomain string) (string, string) {
	// In manual mode we intentionally describe the record by its exact
	// owner name instead of guessing a registrable zone (which is unsafe
	// for suffixes such as co.uk). Operators can place this FQDN in
	// whichever authoritative zone owns it.
	return baseDomain, "*"
}

func normalizeDomain(value string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if strings.HasPrefix(host, "*.") {
		host = strings.TrimPrefix(host, "*.")
	}
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return "", errors.New("base_domain must be a fully-qualified DNS name")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("base_domain must be a valid DNS name")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("base_domain must be a valid DNS name")
			}
		}
	}
	return host, nil
}

var reservedTunnelNames = map[string]struct{}{
	"admin": {}, "api": {}, "app": {}, "connect": {}, "dashboard": {},
	"health": {}, "status": {}, "support": {}, "www": {},
}

func normalizeTunnelName(value string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(value))
	if len(name) < 3 || len(name) > 63 {
		return "", errors.New("name must contain between 3 and 63 characters")
	}
	if _, reserved := reservedTunnelNames[name]; reserved {
		return "", errors.New("name is reserved")
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return "", errors.New("name must start and end with a letter or number")
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return "", errors.New("name may contain only lowercase letters, numbers, and hyphens")
		}
	}
	return name, nil
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

type domainError struct {
	status  int
	message string
}

func (e *domainError) Error() string { return e.message }

func newClientError(message string) error {
	return &domainError{status: http.StatusBadRequest, message: message}
}

func newConflictError(message string) error {
	return &domainError{status: http.StatusConflict, message: message}
}

func newForbiddenError(message string) error {
	return &domainError{status: http.StatusForbidden, message: message}
}

func writeDomainError(w http.ResponseWriter, err error) {
	var typed *domainError
	if errors.As(err, &typed) {
		writeError(w, typed.status, typed.message)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
