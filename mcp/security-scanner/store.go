package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Target struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

type Settings struct {
	RatePPS       int    `json:"rate_pps"`
	ParallelHosts int    `json:"parallel_hosts"`
	MaxAddresses  int    `json:"max_addresses"`
	CVELookup     bool   `json:"cve_lookup"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// Hard ceilings no project setting can exceed.
const (
	maxRatePPS       = 1000
	maxParallelHosts = 64
	maxAddressLimit  = 65536
)

var defaultSettings = Settings{RatePPS: 100, ParallelHosts: 10, MaxAddresses: 1024, CVELookup: true}

type Scan struct {
	ID          int64             `json:"id"`
	Profile     string            `json:"profile"`
	Targets     []string          `json:"targets"`
	Addresses   []string          `json:"addresses,omitempty"`
	Dropped     []AddressDecision `json:"dropped,omitempty"`
	TCPPorts    int               `json:"tcp_port_count"`
	UDPPorts    []int             `json:"udp_ports"`
	Options     map[string]any    `json:"options"`
	StartedBy   string            `json:"started_by"`
	Status      string            `json:"status"`
	Phase       string            `json:"phase"`
	HostsTotal  int               `json:"hosts_total"`
	HostsDone   int               `json:"hosts_done"`
	HostsUp     int               `json:"hosts_up"`
	OpenPorts   int               `json:"open_ports"`
	FindingsNew int               `json:"findings_new"`
	ProbesSent  int64             `json:"probes_sent"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   string            `json:"created_at"`
	StartedAt   string            `json:"started_at,omitempty"`
	CompletedAt string            `json:"completed_at,omitempty"`
	tcpPorts    []int
}

type Host struct {
	ID        int64     `json:"id"`
	Address   string    `json:"address"`
	Hostname  string    `json:"hostname,omitempty"`
	Up        bool      `json:"up"`
	FirstSeen string    `json:"first_seen"`
	LastSeen  string    `json:"last_seen"`
	LastScan  int64     `json:"last_scan_id"`
	Services  []Service `json:"services"`
	Findings  int       `json:"open_findings"`
}

type Service struct {
	Port      int            `json:"port"`
	Protocol  string         `json:"protocol"`
	Service   string         `json:"service"`
	Product   string         `json:"product,omitempty"`
	Version   string         `json:"version,omitempty"`
	TLS       bool           `json:"tls"`
	Banner    string         `json:"banner,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	LastSeen  string         `json:"last_seen"`
	LastScan  int64          `json:"last_scan_id"`
}

type Finding struct {
	ID          int64    `json:"id"`
	Address     string   `json:"address"`
	Port        int      `json:"port"`
	Protocol    string   `json:"protocol"`
	RuleID      string   `json:"rule_id"`
	Title       string   `json:"title"`
	Severity    string   `json:"severity"`
	CVSS        float64  `json:"cvss"`
	EPSS        float64  `json:"epss"`
	KEV         bool     `json:"kev"`
	CVEs        []string `json:"cve_ids"`
	Evidence    string   `json:"evidence"`
	Remediation string   `json:"remediation"`
	Status      string   `json:"status"`
	Note        string   `json:"note"`
	FirstSeen   string   `json:"first_seen"`
	LastSeen    string   `json:"last_seen"`
	FirstScan   int64    `json:"first_scan_id"`
	LastScan    int64    `json:"last_scan_id"`
	FixedScan   int64    `json:"fixed_scan_id,omitempty"`
}

// ---- targets ----

func listTargets(db *sql.DB, pid string) ([]Target, error) {
	rows, err := db.Query(`SELECT id,kind,value,note,created_at FROM targets WHERE project_id=? ORDER BY kind, value`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.Kind, &t.Value, &t.Note, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func addTarget(db *sql.DB, pid, kind, value, note string) (*Target, error) {
	if kind != "allow" && kind != "exclude" {
		return nil, errors.New("kind must be allow or exclude")
	}
	norm, err := normalizeTargetValue(value)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	res, err := db.Exec(`INSERT INTO targets(project_id,kind,value,note,created_at) VALUES(?,?,?,?,?)
        ON CONFLICT(project_id,kind,value) DO UPDATE SET note=excluded.note`, pid, kind, norm, strings.TrimSpace(note), now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	t := &Target{ID: id, Kind: kind, Value: norm, Note: strings.TrimSpace(note), CreatedAt: now}
	_ = db.QueryRow(`SELECT id,created_at FROM targets WHERE project_id=? AND kind=? AND value=?`, pid, kind, norm).Scan(&t.ID, &t.CreatedAt)
	return t, nil
}

func removeTarget(db *sql.DB, pid string, id int64) error {
	res, err := db.Exec(`DELETE FROM targets WHERE project_id=? AND id=?`, pid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func loadScope(db *sql.DB, pid string) (*Scope, int, error) {
	targets, err := listTargets(db, pid)
	if err != nil {
		return nil, 0, err
	}
	var allow, exclude []string
	for _, t := range targets {
		if t.Kind == "allow" {
			allow = append(allow, t.Value)
		} else {
			exclude = append(exclude, t.Value)
		}
	}
	s, err := newScope(allow, exclude)
	return s, len(allow), err
}

// ---- settings ----

func getSettings(db *sql.DB, pid string) (Settings, error) {
	s := defaultSettings
	var cve int
	err := db.QueryRow(`SELECT rate_pps,parallel_hosts,max_addresses,cve_lookup,updated_at FROM settings WHERE project_id=?`, pid).
		Scan(&s.RatePPS, &s.ParallelHosts, &s.MaxAddresses, &cve, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultSettings, nil
	}
	s.CVELookup = cve == 1
	return s, err
}

func saveSettings(db *sql.DB, pid string, args map[string]any) (Settings, error) {
	s, err := getSettings(db, pid)
	if err != nil {
		return s, err
	}
	if _, ok := args["rate_pps"]; ok {
		s.RatePPS = int(int64Arg(args, "rate_pps"))
	}
	if _, ok := args["parallel_hosts"]; ok {
		s.ParallelHosts = int(int64Arg(args, "parallel_hosts"))
	}
	if _, ok := args["max_addresses"]; ok {
		s.MaxAddresses = int(int64Arg(args, "max_addresses"))
	}
	s.CVELookup = boolArg(args, "cve_lookup", s.CVELookup)
	if s.RatePPS < 1 || s.RatePPS > maxRatePPS {
		return s, fmt.Errorf("rate_pps must be between 1 and %d", maxRatePPS)
	}
	if s.ParallelHosts < 1 || s.ParallelHosts > maxParallelHosts {
		return s, fmt.Errorf("parallel_hosts must be between 1 and %d", maxParallelHosts)
	}
	if s.MaxAddresses < 1 || s.MaxAddresses > maxAddressLimit {
		return s, fmt.Errorf("max_addresses must be between 1 and %d", maxAddressLimit)
	}
	s.UpdatedAt = nowUTC()
	_, err = db.Exec(`INSERT INTO settings(project_id,rate_pps,parallel_hosts,max_addresses,cve_lookup,updated_at) VALUES(?,?,?,?,?,?)
        ON CONFLICT(project_id) DO UPDATE SET rate_pps=excluded.rate_pps, parallel_hosts=excluded.parallel_hosts,
        max_addresses=excluded.max_addresses, cve_lookup=excluded.cve_lookup, updated_at=excluded.updated_at`,
		pid, s.RatePPS, s.ParallelHosts, s.MaxAddresses, boolInt(s.CVELookup), s.UpdatedAt)
	return s, err
}

// ---- scans ----

const scanCols = `id,profile,targets_json,addresses_json,dropped_json,tcp_ports_json,udp_ports_json,options_json,started_by,status,phase,
    hosts_total,hosts_done,hosts_up,open_ports,findings_new,probes_sent,error,created_at,COALESCE(started_at,''),COALESCE(completed_at,'')`

func scanScan(row interface{ Scan(...any) error }, withAddresses bool) (*Scan, error) {
	var s Scan
	var targets, addrs, dropped, tcp, udp, opts string
	if err := row.Scan(&s.ID, &s.Profile, &targets, &addrs, &dropped, &tcp, &udp, &opts, &s.StartedBy, &s.Status, &s.Phase,
		&s.HostsTotal, &s.HostsDone, &s.HostsUp, &s.OpenPorts, &s.FindingsNew, &s.ProbesSent, &s.Error, &s.CreatedAt, &s.StartedAt, &s.CompletedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(targets), &s.Targets)
	_ = json.Unmarshal([]byte(tcp), &s.tcpPorts)
	_ = json.Unmarshal([]byte(udp), &s.UDPPorts)
	_ = json.Unmarshal([]byte(opts), &s.Options)
	s.TCPPorts = len(s.tcpPorts)
	if withAddresses {
		_ = json.Unmarshal([]byte(addrs), &s.Addresses)
		_ = json.Unmarshal([]byte(dropped), &s.Dropped)
	}
	return &s, nil
}

func getScan(db *sql.DB, pid string, id int64) (*Scan, error) {
	return scanScan(db.QueryRow(`SELECT `+scanCols+` FROM scans WHERE project_id=? AND id=?`, pid, id), true)
}

func listScans(db *sql.DB, pid, status string, limit int) ([]Scan, error) {
	q := `SELECT ` + scanCols + ` FROM scans WHERE project_id=?`
	args := []any{pid}
	if status != "" && status != "all" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, clamp(limit, 1, 200))
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scan{}
	for rows.Next() {
		s, err := scanScan(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func insertScan(db *sql.DB, pid string, s *Scan) error {
	now := nowUTC()
	res, err := db.Exec(`INSERT INTO scans(project_id,profile,targets_json,addresses_json,dropped_json,tcp_ports_json,udp_ports_json,options_json,started_by,status,hosts_total,created_at)
        VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, s.Profile, mustJSON(s.Targets), mustJSON(s.Addresses), mustJSON(s.Dropped), mustJSON(s.tcpPorts), mustJSON(s.UDPPorts),
		mustJSON(s.Options), s.StartedBy, "queued", len(s.Addresses), now)
	if err != nil {
		return err
	}
	s.ID, _ = res.LastInsertId()
	s.Status, s.CreatedAt, s.HostsTotal = "queued", now, len(s.Addresses)
	return nil
}

// markInterruptedScans fails scans left running by a previous process.
func markInterruptedScans(db *sql.DB) error {
	_, err := db.Exec(`UPDATE scans SET status='failed', error='interrupted: the app restarted during the scan', completed_at=?
        WHERE status IN ('queued','running')`, nowUTC())
	return err
}

// ---- hosts and services ----

func upsertHost(db *sql.DB, pid, addr, hostname string, up bool, scanID int64) (int64, error) {
	now := nowUTC()
	_, err := db.Exec(`INSERT INTO hosts(project_id,address,hostname,up,first_seen,last_seen,last_scan_id) VALUES(?,?,?,?,?,?,?)
        ON CONFLICT(project_id,address) DO UPDATE SET up=excluded.up, last_seen=excluded.last_seen, last_scan_id=excluded.last_scan_id,
        hostname=CASE WHEN excluded.hostname<>'' THEN excluded.hostname ELSE hosts.hostname END`,
		pid, addr, hostname, boolInt(up), now, now, scanID)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM hosts WHERE project_id=? AND address=?`, pid, addr).Scan(&id)
	return id, err
}

func saveServices(db *sql.DB, pid string, hostID int64, addr string, scanID int64, services []ServiceInfo, scannedTCP, scannedUDP []int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUTC()
	open := map[string]bool{}
	for _, s := range services {
		open[fmt.Sprintf("%s/%d", s.Protocol, s.Port)] = true
		if _, err := tx.Exec(`INSERT INTO services(project_id,host_id,port,protocol,state,service,product,version,tls,banner,details_json,first_seen,last_seen,last_scan_id)
            VALUES(?,?,?,?,'open',?,?,?,?,?,?,?,?,?)
            ON CONFLICT(host_id,port,protocol) DO UPDATE SET state='open', service=excluded.service, product=excluded.product,
            version=excluded.version, tls=excluded.tls, banner=excluded.banner, details_json=excluded.details_json,
            last_seen=excluded.last_seen, last_scan_id=excluded.last_scan_id`,
			pid, hostID, s.Port, s.Protocol, s.Service, s.Product, s.Version, boolInt(s.TLS), s.Banner, mustJSON(s.Details), now, now, scanID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO scan_services(scan_id,address,port,protocol,service,product,version) VALUES(?,?,?,?,?,?,?)`,
			scanID, addr, s.Port, s.Protocol, s.Service, s.Product, s.Version); err != nil {
			return err
		}
	}
	// Ports scanned this time and not open any more are marked closed.
	rows, err := tx.Query(`SELECT id,port,protocol FROM services WHERE host_id=? AND state='open'`, hostID)
	if err != nil {
		return err
	}
	var closeIDs []int64
	for rows.Next() {
		var id int64
		var port int
		var proto string
		if err := rows.Scan(&id, &port, &proto); err != nil {
			rows.Close()
			return err
		}
		scanned := (proto == "tcp" && containsSorted(scannedTCP, port)) || (proto == "udp" && containsInt(scannedUDP, port))
		if scanned && !open[fmt.Sprintf("%s/%d", proto, port)] {
			closeIDs = append(closeIDs, id)
		}
	}
	rows.Close()
	for _, id := range closeIDs {
		if _, err := tx.Exec(`UPDATE services SET state='closed', last_scan_id=? WHERE id=?`, scanID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func searchHosts(db *sql.DB, pid, q string, includeClosed bool, limit int) ([]Host, error) {
	query := `SELECT id,address,hostname,up,first_seen,last_seen,COALESCE(last_scan_id,0),
        (SELECT COUNT(*) FROM findings f WHERE f.project_id=hosts.project_id AND f.address=hosts.address AND f.status IN ('open','confirmed'))
        FROM hosts WHERE project_id=?`
	args := []any{pid}
	if q = strings.TrimSpace(q); q != "" {
		query += ` AND (address LIKE ? OR hostname LIKE ? OR id IN (SELECT host_id FROM services WHERE service LIKE ? OR product LIKE ? OR CAST(port AS TEXT)=?))`
		like := "%" + q + "%"
		args = append(args, like, like, like, like, q)
	}
	query += ` ORDER BY up DESC, last_seen DESC LIMIT ?`
	args = append(args, clamp(limit, 1, 500))
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	out := []Host{}
	for rows.Next() {
		var h Host
		var up int
		if err := rows.Scan(&h.ID, &h.Address, &h.Hostname, &up, &h.FirstSeen, &h.LastSeen, &h.LastScan, &h.Findings); err != nil {
			rows.Close()
			return nil, err
		}
		h.Up = up == 1
		out = append(out, h)
	}
	rows.Close()
	for i := range out {
		svcQuery := `SELECT port,protocol,service,product,version,tls,banner,details_json,last_seen,COALESCE(last_scan_id,0) FROM services WHERE host_id=?`
		if !includeClosed {
			svcQuery += ` AND state='open'`
		}
		srows, err := db.Query(svcQuery+` ORDER BY protocol, port`, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Services = []Service{}
		for srows.Next() {
			var s Service
			var tls int
			var details string
			if err := srows.Scan(&s.Port, &s.Protocol, &s.Service, &s.Product, &s.Version, &tls, &s.Banner, &details, &s.LastSeen, &s.LastScan); err != nil {
				srows.Close()
				return nil, err
			}
			s.TLS = tls == 1
			_ = json.Unmarshal([]byte(details), &s.Details)
			out[i].Services = append(out[i].Services, s)
		}
		srows.Close()
	}
	return out, nil
}

// ---- findings ----

const findingCols = `id,address,port,protocol,rule_id,title,severity,cvss,epss,kev,cve_ids_json,evidence,remediation,status,note,
    first_seen,last_seen,COALESCE(first_scan_id,0),COALESCE(last_scan_id,0),COALESCE(fixed_scan_id,0)`

func scanFinding(row interface{ Scan(...any) error }) (*Finding, error) {
	var f Finding
	var kev int
	var cves string
	if err := row.Scan(&f.ID, &f.Address, &f.Port, &f.Protocol, &f.RuleID, &f.Title, &f.Severity, &f.CVSS, &f.EPSS, &kev, &cves,
		&f.Evidence, &f.Remediation, &f.Status, &f.Note, &f.FirstSeen, &f.LastSeen, &f.FirstScan, &f.LastScan, &f.FixedScan); err != nil {
		return nil, err
	}
	f.KEV = kev == 1
	_ = json.Unmarshal([]byte(cves), &f.CVEs)
	if f.CVEs == nil {
		f.CVEs = []string{}
	}
	return &f, nil
}

// upsertFinding records a finding and reports whether it is new (or reopened after a fix).
func upsertFinding(db *sql.DB, pid string, scanID int64, d FindingDraft) (bool, error) {
	now := nowUTC()
	var id int64
	var status string
	err := db.QueryRow(`SELECT id,status FROM findings WHERE project_id=? AND address=? AND port=? AND protocol=? AND rule_id=?`,
		pid, d.Address, d.Port, d.Protocol, d.RuleID).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec(`INSERT INTO findings(project_id,address,port,protocol,rule_id,title,severity,cvss,epss,kev,cve_ids_json,evidence,remediation,status,first_seen,last_seen,first_scan_id,last_scan_id)
            VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'open',?,?,?,?)`,
			pid, d.Address, d.Port, d.Protocol, d.RuleID, d.Title, d.Severity, d.CVSS, d.EPSS, boolInt(d.KEV), mustJSON(nonNil(d.CVEs)),
			d.Evidence, d.Remediation, now, now, scanID, scanID)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	reopened := status == "fixed"
	newStatus := status
	if reopened {
		newStatus = "open"
	}
	_, err = db.Exec(`UPDATE findings SET title=?, severity=?, cvss=?, epss=?, kev=?, cve_ids_json=?, evidence=?, remediation=?,
        status=?, last_seen=?, last_scan_id=?, fixed_scan_id=CASE WHEN ? THEN NULL ELSE fixed_scan_id END WHERE id=?`,
		d.Title, d.Severity, d.CVSS, d.EPSS, boolInt(d.KEV), mustJSON(nonNil(d.CVEs)), d.Evidence, d.Remediation,
		newStatus, now, scanID, reopened, id)
	return reopened, err
}

// markFixed closes findings on a host that this scan checked but did not see again.
func markFixed(db *sql.DB, pid, addr string, scanID int64, scannedTCP, scannedUDP []int, skipCVE bool) (int, error) {
	q := `SELECT id,port,protocol,COALESCE(last_scan_id,0) FROM findings WHERE project_id=? AND address=? AND status IN ('open','confirmed')`
	if skipCVE {
		q += ` AND rule_id NOT LIKE 'cve:%'`
	}
	rows, err := db.Query(q, pid, addr)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id, last int64
		var port int
		var proto string
		if err := rows.Scan(&id, &port, &proto, &last); err != nil {
			rows.Close()
			return 0, err
		}
		if last == scanID {
			continue
		}
		covered := port == 0 || (proto == "tcp" && containsSorted(scannedTCP, port)) || (proto == "udp" && containsInt(scannedUDP, port))
		if covered {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE findings SET status='fixed', fixed_scan_id=? WHERE id=?`, scanID, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

type findingFilter struct {
	Severity []string
	Status   []string
	Address  string
	Port     int
	CVE      string
	ScanID   int64
	Query    string
	Limit    int
	Offset   int
}

func searchFindings(db *sql.DB, pid string, f findingFilter) ([]Finding, int, error) {
	where := `project_id=?`
	args := []any{pid}
	inList := func(col string, values []string) {
		if len(values) == 0 {
			return
		}
		where += ` AND ` + col + ` IN (` + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + `)`
		for _, v := range values {
			args = append(args, v)
		}
	}
	inList("severity", f.Severity)
	if len(f.Status) == 0 {
		f.Status = []string{"open", "confirmed"}
	}
	if !(len(f.Status) == 1 && f.Status[0] == "all") {
		inList("status", f.Status)
	}
	if f.Address != "" {
		where += ` AND address=?`
		args = append(args, f.Address)
	}
	if f.Port > 0 {
		where += ` AND port=?`
		args = append(args, f.Port)
	}
	if f.CVE != "" {
		where += ` AND cve_ids_json LIKE ?`
		args = append(args, `%"`+strings.ToUpper(f.CVE)+`"%`)
	}
	if f.ScanID > 0 {
		where += ` AND last_scan_id=?`
		args = append(args, f.ScanID)
	}
	if f.Query != "" {
		where += ` AND (title LIKE ? OR evidence LIKE ? OR address LIKE ?)`
		like := "%" + f.Query + "%"
		args = append(args, like, like, like)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM findings WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + findingCols + ` FROM findings WHERE ` + where +
		` ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END, kev DESC, cvss DESC, address, port LIMIT ? OFFSET ?`
	args = append(args, clamp(defaultInt(f.Limit, 100), 1, 500), max(f.Offset, 0))
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Finding{}
	for rows.Next() {
		item, err := scanFinding(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *item)
	}
	return out, total, rows.Err()
}

func getFinding(db *sql.DB, pid string, id int64) (*Finding, error) {
	return scanFinding(db.QueryRow(`SELECT `+findingCols+` FROM findings WHERE project_id=? AND id=?`, pid, id))
}

var triageStatuses = map[string]bool{"open": true, "confirmed": true, "false_positive": true, "risk_accepted": true}

func updateFinding(db *sql.DB, pid string, id int64, status string, note *string) (*Finding, error) {
	if status != "" && !triageStatuses[status] {
		return nil, errors.New("status must be open, confirmed, false_positive or risk_accepted")
	}
	f, err := getFinding(db, pid, id)
	if err != nil {
		return nil, err
	}
	if status != "" {
		f.Status = status
	}
	if note != nil {
		f.Note = strings.TrimSpace(*note)
	}
	if _, err := db.Exec(`UPDATE findings SET status=?, note=? WHERE project_id=? AND id=?`, f.Status, f.Note, pid, id); err != nil {
		return nil, err
	}
	return getFinding(db, pid, id)
}

func severityCounts(db *sql.DB, pid string) (map[string]int, error) {
	counts := map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0, "info": 0}
	rows, err := db.Query(`SELECT severity, COUNT(*) FROM findings WHERE project_id=? AND status IN ('open','confirmed') GROUP BY severity`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		counts[sev] = n
	}
	return counts, rows.Err()
}

// ---- helpers ----

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func containsSorted(sorted []int, v int) bool {
	lo, hi := 0, len(sorted)
	for lo < hi {
		mid := (lo + hi) / 2
		switch {
		case sorted[mid] == v:
			return true
		case sorted[mid] < v:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return false
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func defaultInt(n, fallback int) int {
	if n == 0 {
		return fallback
	}
	return n
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func stringSliceArg(args map[string]any, key string) []string {
	var values []string
	switch v := args[key].(type) {
	case []string:
		values = v
	case []any:
		for _, item := range v {
			values = append(values, fmt.Sprint(item))
		}
	case string:
		values = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range values {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
