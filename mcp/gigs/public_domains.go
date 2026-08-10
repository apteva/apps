package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type gigPublicDomain struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id"`
	Hostname      string `json:"hostname"`
	ApexDomain    string `json:"apex_domain"`
	DNSName       string `json:"dns_name"`
	DNSType       string `json:"dns_type"`
	DNSValue      string `json:"dns_value"`
	DNSManaged    bool   `json:"dns_managed"`
	IngressTarget string `json:"ingress_target"`
	IsDefault     bool   `json:"is_default"`
	Status        string `json:"status"`
	StatusDetail  string `json:"status_detail,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type domainInventoryRow struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	RegistrarSlug   string `json:"registrar_slug,omitempty"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
}

type domainDNSRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type attachPublicDomainRequest struct {
	ApexDomain  string `json:"apex_domain"`
	Subdomain   string `json:"subdomain"`
	DNSTarget   string `json:"dns_target"`
	AutoDNS     *bool  `json:"auto_dns"`
	MakeDefault *bool  `json:"make_default"`
}

func (a *App) handleHTTPPublicDomainsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := listGigPublicDomains(ctx.AppDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		inventory, inventoryErr := listDomainInventory(ctx, pid)
		target, _ := suggestedPublicDNSTarget(ctx)
		out := map[string]any{
			"public_domains":       rows,
			"available_domains":    inventory,
			"suggested_dns_target": target,
			"domains_bound":        ctx.IntegrationFor("domains") != nil,
		}
		if inventoryErr != nil {
			out["domains_error"] = inventoryErr.Error()
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body attachPublicDomainRequest
		if err := httpDecode(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		row, err := attachGigPublicDomain(ctx, pid, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"public_domain": row})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPublicDomainItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/public-domains/"), "/")
	parts := strings.Split(rest, "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 && parts[1] == "default" {
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		row, err := setDefaultGigPublicDomain(ctx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"public_domain": row})
		return
	}
	if len(parts) != 1 || r.Method != http.MethodDelete {
		httpErr(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}
	removeDNS := r.URL.Query().Get("remove_dns") != "false"
	if err := detachGigPublicDomain(ctx, pid, id, removeDNS); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"deleted": true, "id": id})
}

func attachGigPublicDomain(ctx *sdk.AppCtx, pid string, input attachPublicDomainRequest) (*gigPublicDomain, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("platform unavailable")
	}
	if ctx.IntegrationFor("domains") == nil {
		return nil, errors.New("Domains is not bound to Gigs")
	}
	apex, err := normalizePublicHostname(input.ApexDomain)
	if err != nil {
		return nil, fmt.Errorf("apex_domain: %w", err)
	}
	sub, err := normalizePublicSubdomain(input.Subdomain)
	if err != nil {
		return nil, err
	}
	hostname := apex
	if sub != "" {
		hostname = sub + "." + apex
	}
	if existing, err := getGigPublicDomainByHostname(ctx.AppDB(), pid, hostname); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if err := requireDomainInInventory(ctx, pid, apex); err != nil {
		return nil, err
	}

	target := strings.TrimSpace(input.DNSTarget)
	if target == "" {
		target, err = suggestedPublicDNSTarget(ctx)
		if err != nil {
			return nil, err
		}
	}
	target, recordType, err := normalizeDNSTarget(target)
	if err != nil {
		return nil, err
	}
	if sub == "" && recordType == "CNAME" {
		return nil, errors.New("an apex hostname cannot use the inferred CNAME target; provide the platform's public IP or choose a subdomain")
	}
	dnsName := sub
	if dnsName == "" {
		dnsName = "@"
	}
	autoDNS := input.AutoDNS == nil || *input.AutoDNS
	dnsManaged := false
	if autoDNS {
		created, err := ensureGigDNSRecord(ctx, pid, apex, dnsName, recordType, target)
		if err != nil {
			return nil, err
		}
		dnsManaged = created
	}

	ingressTarget, err := gigsIngressTarget()
	if err != nil {
		if dnsManaged {
			_ = removeGigDNSRecord(ctx, pid, apex, dnsName, recordType, target)
		}
		return nil, err
	}
	if _, err := ctx.WithProject(pid).PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    ingressTarget,
		ProjectID: pid,
		OwnerKind: "gigs",
		CertFQDN:  hostname,
		AllowHTTP: false,
		TLSMode:   "auto",
	}); err != nil {
		if dnsManaged {
			_ = removeGigDNSRecord(ctx, pid, apex, dnsName, recordType, target)
		}
		return nil, fmt.Errorf("expose Gigs ingress: %w", err)
	}
	keepRemoteState := false
	defer func() {
		if keepRemoteState {
			return
		}
		_ = ctx.WithProject(pid).PlatformAPI().UnexposeIngress(hostname)
		if dnsManaged {
			_ = removeGigDNSRecord(ctx, pid, apex, dnsName, recordType, target)
		}
	}()

	makeDefault := input.MakeDefault != nil && *input.MakeDefault
	count := 0
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gig_public_domains WHERE project_id=?`, pid).Scan(&count)
	if count == 0 {
		makeDefault = true
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if makeDefault {
		if _, err := tx.Exec(`UPDATE gig_public_domains SET is_default=0,updated_at=CURRENT_TIMESTAMP WHERE project_id=?`, pid); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(`INSERT INTO gig_public_domains
		(project_id,hostname,apex_domain,dns_name,dns_type,dns_value,dns_managed,ingress_target,is_default,status)
		VALUES (?,?,?,?,?,?,?,?,?,'active')`,
		pid, hostname, apex, dnsName, recordType, target, boolInt(dnsManaged), ingressTarget, boolInt(makeDefault))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	keepRemoteState = true
	ctx.EmitWithProject("gig.public_domain_attached", pid, map[string]any{"id": id, "hostname": hostname})
	return getGigPublicDomain(ctx.AppDB(), pid, id)
}

func detachGigPublicDomain(ctx *sdk.AppCtx, pid string, id int64, removeDNS bool) error {
	row, err := getGigPublicDomain(ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("public domain not found")
	}
	if err := ctx.WithProject(pid).PlatformAPI().UnexposeIngress(row.Hostname); err != nil {
		return fmt.Errorf("remove Gigs ingress: %w", err)
	}
	if removeDNS && row.DNSManaged {
		if err := removeGigDNSRecord(ctx, pid, row.ApexDomain, row.DNSName, row.DNSType, row.DNSValue); err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE gig_public_domains SET status='detaching',status_detail=?,is_default=0,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, err.Error(), pid, id)
			if row.IsDefault {
				_, _ = ctx.AppDB().Exec(`UPDATE gig_public_domains SET is_default=1,updated_at=CURRENT_TIMESTAMP
					WHERE id=(SELECT id FROM gig_public_domains WHERE project_id=? AND status='active' ORDER BY created_at,id LIMIT 1)`, pid)
			}
			return fmt.Errorf("ingress removed, but DNS cleanup needs attention: %w", err)
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM gig_public_domains WHERE project_id=? AND id=?`, pid, id); err != nil {
		return err
	}
	if row.IsDefault {
		if _, err := tx.Exec(`UPDATE gig_public_domains SET is_default=1,updated_at=CURRENT_TIMESTAMP
			WHERE id=(SELECT id FROM gig_public_domains WHERE project_id=? AND status='active' ORDER BY created_at,id LIMIT 1)`, pid); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ctx.EmitWithProject("gig.public_domain_detached", pid, map[string]any{"id": id, "hostname": row.Hostname})
	return nil
}

func setDefaultGigPublicDomain(db *sql.DB, pid string, id int64) (*gigPublicDomain, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM gig_public_domains WHERE project_id=? AND id=?`, pid, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("public domain not found")
		}
		return nil, err
	}
	if status != "active" {
		return nil, errors.New("only an active public domain can be the default")
	}
	if _, err := tx.Exec(`UPDATE gig_public_domains SET is_default=CASE WHEN id=? THEN 1 ELSE 0 END,updated_at=CURRENT_TIMESTAMP WHERE project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getGigPublicDomain(db, pid, id)
}

func reconcileGigPublicDomains(ctx *sdk.AppCtx) {
	if ctx == nil || ctx.AppDB() == nil || ctx.PlatformAPI() == nil {
		return
	}
	target, err := gigsIngressTarget()
	if err != nil {
		ctx.Logger().Warn("public domain reconciliation skipped", "err", err.Error())
		return
	}
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,hostname FROM gig_public_domains WHERE status<>'detaching'`)
	if err != nil {
		ctx.Logger().Warn("public domain reconciliation query failed", "err", err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var pid, hostname string
		if err := rows.Scan(&id, &pid, &hostname); err != nil {
			continue
		}
		_, exposeErr := ctx.WithProject(pid).PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
			Hostname: hostname, Target: target, ProjectID: pid, OwnerKind: "gigs",
			CertFQDN: hostname, TLSMode: "auto",
		})
		status, detail := "active", ""
		if exposeErr != nil {
			status, detail = "error", exposeErr.Error()
		}
		_, _ = ctx.AppDB().Exec(`UPDATE gig_public_domains SET ingress_target=?,status=?,status_detail=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, target, status, nullStr(detail), id)
	}
}

func listDomainInventory(ctx *sdk.AppCtx, pid string) ([]domainInventoryRow, error) {
	if ctx == nil || ctx.PlatformAPI() == nil || ctx.IntegrationFor("domains") == nil {
		return []domainInventoryRow{}, nil
	}
	var out struct {
		Domains []domainInventoryRow `json:"domains"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("domains", "domain_list", map[string]any{"_project_id": pid}, &out); err != nil {
		return nil, fmt.Errorf("domains.domain_list: %w", err)
	}
	return out.Domains, nil
}

func requireDomainInInventory(ctx *sdk.AppCtx, pid, apex string) error {
	rows, err := listDomainInventory(ctx, pid)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSuffix(row.Name, "."), apex) {
			return nil
		}
	}
	return fmt.Errorf("domain %q is not registered in the Domains app", apex)
}

func ensureGigDNSRecord(ctx *sdk.AppCtx, pid, apex, name, recordType, value string) (bool, error) {
	records, err := listDomainDNSRecords(ctx, pid, apex, name)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if !domainRecordAtName(record.Name, apex, name) {
			continue
		}
		if strings.EqualFold(record.Type, recordType) && strings.EqualFold(strings.TrimSuffix(record.Value, "."), strings.TrimSuffix(value, ".")) {
			return false, nil
		}
		if strings.EqualFold(record.Type, recordType) || strings.EqualFold(record.Type, "CNAME") || recordType == "CNAME" {
			return false, fmt.Errorf("DNS record %s %s already exists with a different value; Gigs will not overwrite it", name, record.Type)
		}
	}
	var out map[string]any
	err = ctx.WithProject(pid).PlatformAPI().CallAppResult("domains", "domain_records_set", map[string]any{
		"_project_id": pid, "domain": apex, "name": name, "type": recordType, "value": value, "ttl": 600,
	}, &out)
	if err != nil {
		return false, fmt.Errorf("domains.domain_records_set: %w", err)
	}
	return true, nil
}

func removeGigDNSRecord(ctx *sdk.AppCtx, pid, apex, name, recordType, value string) error {
	records, err := listDomainDNSRecords(ctx, pid, apex, name)
	if err != nil {
		return err
	}
	matches := 0
	sameTypeAtName := 0
	recordID := ""
	for _, record := range records {
		if !domainRecordAtName(record.Name, apex, name) || !strings.EqualFold(record.Type, recordType) {
			continue
		}
		sameTypeAtName++
		if strings.EqualFold(strings.TrimSuffix(record.Value, "."), strings.TrimSuffix(value, ".")) {
			matches++
			recordID = record.ID
		}
	}
	if matches == 0 {
		return nil
	}
	if matches != 1 || (recordID == "" && sameTypeAtName != 1) {
		return fmt.Errorf("found %d owned matches among %d records at this name; refusing broad deletion", matches, sameTypeAtName)
	}
	args := map[string]any{"_project_id": pid, "domain": apex, "name": name, "type": recordType}
	if recordID != "" {
		args["record_id"] = recordID
	}
	var out map[string]any
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("domains", "domain_records_delete", args, &out); err != nil {
		return fmt.Errorf("domains.domain_records_delete: %w", err)
	}
	return nil
}

func listDomainDNSRecords(ctx *sdk.AppCtx, pid, apex, name string) ([]domainDNSRecord, error) {
	var out struct {
		Records []domainDNSRecord `json:"records"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("domains", "domain_records_list", map[string]any{
		"_project_id": pid, "domain": apex, "name": name,
	}, &out); err != nil {
		return nil, fmt.Errorf("domains.domain_records_list: %w", err)
	}
	return out.Records, nil
}

func domainRecordAtName(recordName, apex, name string) bool {
	recordName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(recordName), "."))
	apex = strings.ToLower(strings.TrimSuffix(apex, "."))
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" || name == "@" {
		return recordName == "" || recordName == "@" || recordName == apex
	}
	return recordName == name || recordName == name+"."+apex
}

func gigsIngressTarget() (string, error) {
	port := strings.TrimSpace(os.Getenv("APTEVA_APP_PORT"))
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", errors.New("APTEVA_APP_PORT is unavailable; Gigs cannot expose an exact-install ingress target")
	}
	return "http://127.0.0.1:" + strconv.Itoa(n), nil
}

func suggestedPublicDNSTarget(ctx *sdk.AppCtx) (string, error) {
	base := ""
	if ctx != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil {
			base = strings.TrimSpace(info.PublicURL)
		}
	}
	if base == "" {
		base = strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL"))
	}
	if base == "" {
		return "", errors.New("the platform Public URL is unset; provide the public server IP or hostname")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("the platform Public URL has no valid hostname")
	}
	return u.Hostname(), nil
}

func normalizeDNSTarget(value string) (string, string, error) {
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
	host, err := normalizePublicHostname(value)
	if err != nil {
		return "", "", fmt.Errorf("dns_target: %w", err)
	}
	return host, "CNAME", nil
}

func normalizePublicHostname(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") {
		return "", errors.New("must be a fully-qualified domain name")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("contains an invalid DNS label")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", errors.New("contains characters not allowed in a hostname")
			}
		}
	}
	return value, nil
}

func normalizePublicSubdomain(value string) (string, error) {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "."))
	if value == "" || value == "@" {
		return "", nil
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("subdomain contains an invalid DNS label")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", errors.New("subdomain contains invalid characters")
			}
		}
	}
	return value, nil
}

func listGigPublicDomains(db *sql.DB, pid string) ([]*gigPublicDomain, error) {
	rows, err := db.Query(`SELECT id,project_id,hostname,apex_domain,dns_name,dns_type,dns_value,dns_managed,
		ingress_target,is_default,status,COALESCE(status_detail,''),COALESCE(created_at,''),COALESCE(updated_at,'')
		FROM gig_public_domains WHERE project_id=? ORDER BY is_default DESC,hostname`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*gigPublicDomain{}
	for rows.Next() {
		row := &gigPublicDomain{}
		var managed, def int
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Hostname, &row.ApexDomain, &row.DNSName, &row.DNSType, &row.DNSValue, &managed,
			&row.IngressTarget, &def, &row.Status, &row.StatusDetail, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.DNSManaged = managed != 0
		row.IsDefault = def != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func getGigPublicDomain(db *sql.DB, pid string, id int64) (*gigPublicDomain, error) {
	row := &gigPublicDomain{}
	var managed, def int
	err := db.QueryRow(`SELECT id,project_id,hostname,apex_domain,dns_name,dns_type,dns_value,dns_managed,
		ingress_target,is_default,status,COALESCE(status_detail,''),COALESCE(created_at,''),COALESCE(updated_at,'')
		FROM gig_public_domains WHERE project_id=? AND id=?`, pid, id).Scan(
		&row.ID, &row.ProjectID, &row.Hostname, &row.ApexDomain, &row.DNSName, &row.DNSType, &row.DNSValue, &managed,
		&row.IngressTarget, &def, &row.Status, &row.StatusDetail, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.DNSManaged = managed != 0
	row.IsDefault = def != 0
	return row, nil
}

func getGigPublicDomainByHostname(db *sql.DB, pid, hostname string) (*gigPublicDomain, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM gig_public_domains WHERE project_id=? AND hostname=?`, pid, hostname).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getGigPublicDomain(db, pid, id)
}

func resolveGigPublicBaseURL(db *sql.DB, pid string, publicDomainID int64) (string, error) {
	q := `SELECT hostname FROM gig_public_domains WHERE project_id=? AND status='active' AND is_default=1`
	args := []any{pid}
	if publicDomainID > 0 {
		q = `SELECT hostname FROM gig_public_domains WHERE project_id=? AND status='active' AND id=?`
		args = append(args, publicDomainID)
	}
	var hostname string
	err := db.QueryRow(q, args...).Scan(&hostname)
	if errors.Is(err, sql.ErrNoRows) {
		if publicDomainID > 0 {
			return "", errors.New("selected public domain not found or inactive")
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return "https://" + hostname, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
