package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type communityDomainInventory struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
}

type communityDNSRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type communityDomainStatus struct {
	Configured       bool                       `json:"configured"`
	CommunityID      string                     `json:"community_id"`
	Hostname         string                     `json:"hostname,omitempty"`
	PortalURL        string                     `json:"portal_url,omitempty"`
	DomainsBound     bool                       `json:"domains_bound"`
	DNSManaged       bool                       `json:"dns_managed"`
	DNSDomain        string                     `json:"dns_domain,omitempty"`
	DNSName          string                     `json:"dns_name,omitempty"`
	DNSType          string                     `json:"dns_type,omitempty"`
	DNSValue         string                     `json:"dns_value,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Ingress          *sdk.IngressRoute          `json:"ingress,omitempty"`
	AvailableDomains []communityDomainInventory `json:"available_domains,omitempty"`
	SuggestedTarget  string                     `json:"suggested_dns_target,omitempty"`
}

func communityDomainTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "community_domain_options",
			Description: "List Domains-managed apex domains and the suggested DNS target for one community.",
			InputSchema: schemaObject(map[string]any{"community_id": map[string]any{"type": "string"}}, []string{"community_id"}),
			Handler:     toolCommunityDomainOptions,
		},
		{
			Name:        "community_domain_attach",
			Description: "Attach one subdomain to a community. Domains writes DNS when auto_dns is true; native Apteva ingress owns routing and TLS.",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"apex_domain":  map[string]any{"type": "string"},
				"subdomain":    map[string]any{"type": "string"},
				"dns_target":   map[string]any{"type": "string"},
				"auto_dns":     map[string]any{"type": "boolean"},
				"allow_http":   map[string]any{"type": "boolean"},
				"http_port":    map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			}, []string{"community_id", "apex_domain"}),
			Handler: toolCommunityDomainAttach,
		},
		{
			Name:        "community_domain_status",
			Description: "Return the community's configured hostname plus native ingress and certificate status.",
			InputSchema: schemaObject(map[string]any{"community_id": map[string]any{"type": "string"}}, []string{"community_id"}),
			Handler:     toolCommunityDomainStatus,
		},
		{
			Name:        "community_domain_detach",
			Description: "Detach a community hostname, remove native ingress, and remove only the exact DNS record Community created.",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"remove_dns":   map[string]any{"type": "boolean"},
			}, []string{"community_id"}),
			Handler: toolCommunityDomainDetach,
		},
	}
}

func toolCommunityDomainOptions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityReadable(ctx, community); err != nil {
		return nil, err
	}
	status := domainStatusFromCommunity(ctx, community)
	status.AvailableDomains, _ = listCommunityDomainInventory(ctx, community.ProjectID)
	status.SuggestedTarget, _ = suggestedCommunityDNSTarget(ctx)
	return status, nil
}

func toolCommunityDomainStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	status := domainStatusFromCommunity(ctx, community)
	if !status.Configured || ctx.PlatformAPI() == nil {
		return status, nil
	}
	routes, routeErr := ctx.WithProject(community.ProjectID).PlatformAPI().ListIngressRoutes()
	if routeErr != nil {
		status.Error = routeErr.Error()
		return status, nil
	}
	for i := range routes {
		if strings.EqualFold(routes[i].Hostname, status.Hostname) {
			status.Ingress = &routes[i]
			break
		}
	}
	if status.Ingress == nil && status.Error == "" {
		status.Error = "The hostname is configured but native ingress is not currently exposed."
	}
	return status, nil
}

func toolCommunityDomainAttach(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(community.AuthClientID) == "" {
		return nil, errors.New("configure this community's Auth client before attaching a domain")
	}
	apex, err := normalizeCommunityHostname(strArg(args, "apex_domain", ""))
	if err != nil {
		return nil, fmt.Errorf("apex_domain: %w", err)
	}
	subdomain, err := normalizeCommunitySubdomain(strArg(args, "subdomain", ""))
	if err != nil {
		return nil, err
	}
	hostname := apex
	if subdomain != "" {
		hostname = subdomain + "." + apex
	}
	if owner, ownerErr := communityForPortalHostname(ctx.AppDB(), "", hostname); ownerErr != nil {
		return nil, ownerErr
	} else if owner != nil && owner.ID != community.ID {
		return nil, fmt.Errorf("%s is already attached to %s", hostname, owner.Name)
	}
	currentHostname := portalHostname(community.PortalHost)
	if community.PortalDNSDomain != "" && currentHostname != "" && !strings.EqualFold(currentHostname, hostname) {
		return nil, fmt.Errorf("%s is already attached; disconnect it before choosing a different hostname", currentHostname)
	}

	autoDNS := true
	if value, ok := args["auto_dns"].(bool); ok {
		autoDNS = value
	}
	allowHTTP, _ := args["allow_http"].(bool)
	httpPort := 0
	if raw, ok := args["http_port"].(float64); ok {
		httpPort = int(raw)
	} else if raw, ok := args["http_port"].(int); ok {
		httpPort = raw
	}
	if httpPort < 0 || httpPort > 65535 || (httpPort > 0 && !allowHTTP) {
		return nil, errors.New("http_port must be between 1 and 65535 and is allowed only with allow_http")
	}
	dnsTarget := strings.TrimSpace(strArg(args, "dns_target", ""))
	if dnsTarget == "" {
		dnsTarget, err = suggestedCommunityDNSTarget(ctx)
		if err != nil {
			return nil, err
		}
	}
	dnsTarget, recordType, err := normalizeCommunityDNSTarget(dnsTarget)
	if err != nil {
		return nil, err
	}
	if subdomain == "" && recordType == "CNAME" {
		return nil, errors.New("an apex hostname cannot use a CNAME target; choose a subdomain or provide the platform's public IP")
	}
	dnsName := subdomain
	if dnsName == "" {
		dnsName = "@"
	}
	dnsManaged := false
	if autoDNS {
		if ctx.IntegrationFor("domains") == nil {
			return nil, errors.New("bind the Domains app to Community or disable automatic DNS")
		}
		if err := requireCommunityDomainInventory(ctx, community.ProjectID, apex); err != nil {
			return nil, err
		}
		dnsManaged, err = ensureCommunityDNSRecord(ctx, community.ProjectID, apex, dnsName, recordType, dnsTarget)
		if err != nil {
			return nil, err
		}
	}

	portalURL, err := communityPortalOrigin(ctx, hostname, allowHTTP, httpPort)
	if err != nil {
		if dnsManaged {
			_ = removeCommunityDNSRecord(ctx, community.ProjectID, apex, dnsName, recordType, dnsTarget)
		}
		return nil, err
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("native Apteva ingress is unavailable")
	}
	route, err := ctx.WithProject(community.ProjectID).PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname: hostname, Target: communityIngressTarget(community.ProjectID), ProjectID: community.ProjectID,
		OwnerKind: "community", CertFQDN: hostname, AllowHTTP: allowHTTP, TLSMode: map[bool]string{true: "off", false: "auto"}[allowHTTP],
	})
	if err != nil {
		if dnsManaged {
			_ = removeCommunityDNSRecord(ctx, community.ProjectID, apex, dnsName, recordType, dnsTarget)
		}
		return nil, fmt.Errorf("expose Community ingress: %w", err)
	}
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		_ = ctx.WithProject(community.ProjectID).PlatformAPI().UnexposeIngress(hostname)
		if dnsManaged {
			_ = removeCommunityDNSRecord(ctx, community.ProjectID, apex, dnsName, recordType, dnsTarget)
		}
	}()
	if err := updateCommunityAuthOrigin(ctx, community, portalURL, true); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE communities SET
		portal_host=?, portal_dns_managed=?, portal_dns_domain=?, portal_dns_name=?,
		portal_dns_type=?, portal_dns_value=?, portal_domain_error=''
		WHERE id=?`, portalURL, boolInt(dnsManaged), apex, dnsName, recordType, dnsTarget, community.ID); err != nil {
		_ = updateCommunityAuthOrigin(ctx, community, portalURL, false)
		return nil, err
	}
	if previous := strings.TrimRight(strings.TrimSpace(community.PortalHost), "/"); previous != "" && previous != strings.TrimRight(portalURL, "/") {
		_ = updateCommunityAuthOrigin(ctx, community, previous, false)
	}
	rollback = false
	community, err = loadCommunity(ctx.AppDB(), community.ID)
	if err != nil {
		return nil, err
	}
	status := domainStatusFromCommunity(ctx, community)
	status.Ingress = route
	emit(ctx, "community.domain_attached", map[string]any{"community_id": community.ID, "hostname": hostname})
	emit(ctx, "community.updated", map[string]any{"community_id": community.ID})
	return status, nil
}

func toolCommunityDomainDetach(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	status := domainStatusFromCommunity(ctx, community)
	if !status.Configured {
		return status, nil
	}
	removeDNS := true
	if value, ok := args["remove_dns"].(bool); ok {
		removeDNS = value
	}
	if routes, listErr := ctx.WithProject(community.ProjectID).PlatformAPI().ListIngressRoutes(); listErr == nil {
		for i := range routes {
			if strings.EqualFold(routes[i].Hostname, status.Hostname) {
				if err := ctx.WithProject(community.ProjectID).PlatformAPI().UnexposeIngress(status.Hostname); err != nil {
					return nil, fmt.Errorf("remove Community ingress: %w", err)
				}
				break
			}
		}
	}
	if removeDNS && community.PortalDNSManaged {
		if err := removeCommunityDNSRecord(ctx, community.ProjectID, community.PortalDNSDomain, community.PortalDNSName, community.PortalDNSType, community.PortalDNSValue); err != nil {
			return nil, err
		}
	}
	if err := updateCommunityAuthOrigin(ctx, community, community.PortalHost, false); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE communities SET
		portal_host='', portal_dns_managed=0, portal_dns_domain='', portal_dns_name='',
		portal_dns_type='', portal_dns_value='', portal_domain_error='' WHERE id=?`, community.ID); err != nil {
		return nil, err
	}
	emit(ctx, "community.domain_detached", map[string]any{"community_id": community.ID, "hostname": status.Hostname})
	emit(ctx, "community.updated", map[string]any{"community_id": community.ID})
	return domainStatusFromCommunity(ctx, Community{ID: community.ID}), nil
}

func domainStatusFromCommunity(ctx *sdk.AppCtx, community Community) communityDomainStatus {
	hostname := portalHostname(community.PortalHost)
	return communityDomainStatus{
		Configured: community.PortalDNSDomain != "" && hostname != "", CommunityID: community.ID,
		Hostname: hostname, PortalURL: community.PortalHost, DomainsBound: ctx != nil && ctx.IntegrationFor("domains") != nil,
		DNSManaged: community.PortalDNSManaged, DNSDomain: community.PortalDNSDomain, DNSName: community.PortalDNSName,
		DNSType: community.PortalDNSType, DNSValue: community.PortalDNSValue, Error: community.PortalDomainError,
	}
}

func updateCommunityAuthOrigin(ctx *sdk.AppCtx, community Community, origin string, add bool) error {
	if strings.TrimSpace(community.AuthClientID) == "" || strings.TrimSpace(origin) == "" {
		return nil
	}
	key := "add_allowed_origins"
	if !add {
		key = "remove_allowed_origins"
	}
	var out map[string]any
	if err := callAppResult(ctx, "auth", "auth_clients_update", map[string]any{
		"client_id": community.AuthClientID, key: []string{strings.TrimRight(origin, "/")},
	}, &out); err != nil {
		return fmt.Errorf("update Auth client origin: %w", err)
	}
	return nil
}

func communityIngressTarget(projectID string) string {
	appName := "community"
	if globalCtx != nil && globalCtx.Manifest() != nil && globalCtx.Manifest().Name != "" {
		appName = globalCtx.Manifest().Name
	}
	query := url.Values{"project_id": []string{projectID}, "ingress_auth": []string{"app_token"}}
	return (&url.URL{Scheme: "app", Host: appName, RawQuery: query.Encode()}).String()
}

func communityPortalOrigin(ctx *sdk.AppCtx, hostname string, allowHTTP bool, explicitHTTPPort int) (string, error) {
	scheme := "https"
	port := ""
	if allowHTTP {
		scheme = "http"
		if explicitHTTPPort > 0 {
			port = fmt.Sprint(explicitHTTPPort)
		} else if info, err := ctx.PlatformInfo(); err == nil && info != nil {
			if parsed, parseErr := url.Parse(info.PublicURL); parseErr == nil {
				port = parsed.Port()
			}
		}
	}
	host := hostname
	if port != "" && !((scheme == "http" && port == "80") || (scheme == "https" && port == "443")) {
		host = net.JoinHostPort(hostname, port)
	}
	return scheme + "://" + host, nil
}

func reconcileCommunityDomains(ctx *sdk.AppCtx) {
	if ctx == nil || ctx.AppDB() == nil || ctx.PlatformAPI() == nil {
		return
	}
	rows, err := ctx.AppDB().Query(`SELECT ` + communityCols + ` FROM communities WHERE archived_at IS NULL AND portal_dns_domain <> '' AND portal_host <> ''`)
	if err != nil {
		ctx.Logger().Warn("community domain reconciliation query failed", "err", err.Error())
		return
	}
	communities := []Community{}
	for rows.Next() {
		community, scanErr := scanCommunity(rows.Scan)
		if scanErr != nil {
			continue
		}
		communities = append(communities, community)
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		ctx.Logger().Warn("community domain reconciliation scan failed", "err", rowsErr.Error())
		return
	}
	for _, community := range communities {
		hostname := portalHostname(community.PortalHost)
		if hostname == "" {
			continue
		}
		allowHTTP := strings.HasPrefix(strings.ToLower(community.PortalHost), "http://")
		_, exposeErr := ctx.WithProject(community.ProjectID).PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
			Hostname: hostname, Target: communityIngressTarget(community.ProjectID), ProjectID: community.ProjectID,
			OwnerKind: "community", CertFQDN: hostname, AllowHTTP: allowHTTP, TLSMode: map[bool]string{true: "off", false: "auto"}[allowHTTP],
		})
		detail := ""
		if exposeErr != nil {
			detail = exposeErr.Error()
		}
		_, _ = ctx.AppDB().Exec(`UPDATE communities SET portal_domain_error=? WHERE id=?`, detail, community.ID)
	}
}

func listCommunityDomainInventory(ctx *sdk.AppCtx, projectID string) ([]communityDomainInventory, error) {
	if ctx == nil || ctx.PlatformAPI() == nil || ctx.IntegrationFor("domains") == nil {
		return []communityDomainInventory{}, nil
	}
	var out struct {
		Domains []communityDomainInventory `json:"domains"`
	}
	if err := callAppResult(ctx.WithProject(projectID), "domains", "domain_list", map[string]any{"_project_id": projectID}, &out); err != nil {
		return nil, fmt.Errorf("domains.domain_list: %w", err)
	}
	return out.Domains, nil
}

func requireCommunityDomainInventory(ctx *sdk.AppCtx, projectID, apex string) error {
	domains, err := listCommunityDomainInventory(ctx, projectID)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		if strings.EqualFold(strings.TrimSuffix(domain.Name, "."), apex) {
			return nil
		}
	}
	return fmt.Errorf("domain %q is not registered in the Domains app", apex)
}

func ensureCommunityDNSRecord(ctx *sdk.AppCtx, projectID, domain, name, recordType, value string) (bool, error) {
	records, err := listCommunityDNSRecords(ctx, projectID, domain, name)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if !communityDNSRecordAtName(record.Name, domain, name) {
			continue
		}
		if strings.EqualFold(record.Type, recordType) && strings.EqualFold(strings.TrimSuffix(record.Value, "."), strings.TrimSuffix(value, ".")) {
			return false, nil
		}
		if strings.EqualFold(record.Type, recordType) || strings.EqualFold(record.Type, "CNAME") || recordType == "CNAME" {
			return false, fmt.Errorf("DNS record %s %s already exists with a different value; Community will not overwrite it", name, record.Type)
		}
	}
	var out map[string]any
	if err := callAppResult(ctx.WithProject(projectID), "domains", "domain_records_set", map[string]any{
		"_project_id": projectID, "domain": domain, "name": name, "type": recordType, "value": value, "ttl": 600,
	}, &out); err != nil {
		return false, fmt.Errorf("domains.domain_records_set: %w", err)
	}
	return true, nil
}

func removeCommunityDNSRecord(ctx *sdk.AppCtx, projectID, domain, name, recordType, value string) error {
	records, err := listCommunityDNSRecords(ctx, projectID, domain, name)
	if err != nil {
		return err
	}
	matches, sameType, recordID := 0, 0, ""
	for _, record := range records {
		if !communityDNSRecordAtName(record.Name, domain, name) || !strings.EqualFold(record.Type, recordType) {
			continue
		}
		sameType++
		if strings.EqualFold(strings.TrimSuffix(record.Value, "."), strings.TrimSuffix(value, ".")) {
			matches++
			recordID = record.ID
		}
	}
	if matches == 0 {
		return nil
	}
	if matches != 1 || (recordID == "" && sameType != 1) {
		return fmt.Errorf("found %d owned DNS matches among %d records; refusing broad deletion", matches, sameType)
	}
	input := map[string]any{"_project_id": projectID, "domain": domain, "name": name, "type": recordType}
	if recordID != "" {
		input["record_id"] = recordID
	}
	var out map[string]any
	if err := callAppResult(ctx.WithProject(projectID), "domains", "domain_records_delete", input, &out); err != nil {
		return fmt.Errorf("domains.domain_records_delete: %w", err)
	}
	return nil
}

func listCommunityDNSRecords(ctx *sdk.AppCtx, projectID, domain, name string) ([]communityDNSRecord, error) {
	var out struct {
		Records []communityDNSRecord `json:"records"`
	}
	if err := callAppResult(ctx.WithProject(projectID), "domains", "domain_records_list", map[string]any{
		"_project_id": projectID, "domain": domain, "name": name,
	}, &out); err != nil {
		return nil, fmt.Errorf("domains.domain_records_list: %w", err)
	}
	return out.Records, nil
}

func communityDNSRecordAtName(recordName, apex, name string) bool {
	recordName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(recordName), "."))
	apex = strings.ToLower(strings.TrimSuffix(apex, "."))
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" || name == "@" {
		return recordName == "" || recordName == "@" || recordName == apex
	}
	return recordName == name || recordName == name+"."+apex
}

func suggestedCommunityDNSTarget(ctx *sdk.AppCtx) (string, error) {
	if ctx == nil {
		return "", errors.New("platform unavailable")
	}
	info, err := ctx.PlatformInfo()
	if err != nil || info == nil || strings.TrimSpace(info.PublicURL) == "" {
		return "", errors.New("the platform Public URL is unset; provide the public server IP or hostname")
	}
	parsed, err := url.Parse(info.PublicURL)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("the platform Public URL has no valid hostname")
	}
	return parsed.Hostname(), nil
}

func normalizeCommunityDNSTarget(value string) (string, string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" {
		return "", "", errors.New("dns_target required")
	}
	if ip := net.ParseIP(value); ip != nil {
		if ip.To4() != nil {
			return ip.String(), "A", nil
		}
		return ip.String(), "AAAA", nil
	}
	host, err := normalizeCommunityHostname(value)
	if err != nil {
		return "", "", fmt.Errorf("dns_target: %w", err)
	}
	return host, "CNAME", nil
}

func normalizeCommunityHostname(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") {
		return "", errors.New("must be a fully-qualified domain name")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("contains an invalid DNS label")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("contains an invalid DNS label")
			}
		}
	}
	return value, nil
}

func normalizeCommunitySubdomain(value string) (string, error) {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "."))
	if value == "" || value == "@" {
		return "", nil
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("subdomain contains an invalid DNS label")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("subdomain contains an invalid DNS label")
			}
		}
	}
	return value, nil
}

func portalHostname(portalHost string) string {
	parsed, err := url.Parse(strings.TrimSpace(portalHost))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func communityForPortalHostname(db *sql.DB, projectID, hostname string) (*Community, error) {
	query := `SELECT ` + communityCols + ` FROM communities WHERE archived_at IS NULL AND portal_host<>''`
	var rows *sql.Rows
	var err error
	if projectID == "" {
		rows, err = db.Query(query)
	} else {
		rows, err = db.Query(query+` AND project_id=?`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		community, scanErr := scanCommunity(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		if strings.EqualFold(portalHostname(community.PortalHost), hostname) {
			return &community, nil
		}
	}
	return nil, rows.Err()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
