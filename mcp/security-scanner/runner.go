package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type scanOptions struct {
	Profile        string
	TCPSpec        string
	UDPSpec        string
	TreatAllUp     bool
	TimeoutMS      int
	Retries        int
	RatePPS        int
	ParallelHosts  int
	CVELookup      bool
	StartedBy      string
}

const maxConcurrentScansPerProject = 1

// scanManager owns running scans and their cancel functions.
type scanManager struct {
	mu      sync.Mutex
	running map[int64]context.CancelFunc
	byProj  map[string]int
	dbMu    sync.Mutex // serialise writes from host workers
	vulns   func(db *sql.DB) *vulnSource
	resolve resolver
	wg      sync.WaitGroup
}

func newScanManager(apiKey string) *scanManager {
	return &scanManager{
		running: map[int64]context.CancelFunc{},
		byProj:  map[string]int{},
		vulns:   func(db *sql.DB) *vulnSource { return newVulnSource(db, apiKey) },
	}
}

// prepare validates scope and options and stores the queued scan. Nothing is sent yet.
func (m *scanManager) prepare(ctx context.Context, db *sql.DB, pid string, targets []string, opt scanOptions) (*Scan, *ScopeResult, portPlan, error) {
	settings, err := getSettings(db, pid)
	if err != nil {
		return nil, nil, portPlan{}, err
	}
	scope, allowCount, err := loadScope(db, pid)
	if err != nil {
		return nil, nil, portPlan{}, err
	}
	if allowCount == 0 {
		return nil, nil, portPlan{}, errors.New("the allowlist is empty: a person must add targets in the Security panel before scanning")
	}
	plan, err := planForProfile(opt.Profile, opt.TCPSpec, opt.UDPSpec)
	if err != nil {
		return nil, nil, portPlan{}, err
	}
	res, err := scope.Expand(ctx, targets, settings.MaxAddresses, m.resolve)
	if err != nil {
		return nil, nil, portPlan{}, err
	}
	if len(res.Allowed) == 0 {
		return nil, res, plan, errors.New("no target address is on the allowlist; nothing to scan")
	}
	addrs := make([]string, 0, len(res.Allowed))
	for _, a := range res.Allowed {
		addrs = append(addrs, a.Address)
	}
	if opt.Profile == "" {
		opt.Profile = "standard"
	}
	opt.TimeoutMS = clamp(defaultInt(opt.TimeoutMS, 1000), 100, 5000)
	opt.Retries = clamp(opt.Retries, 0, 2)
	opt.RatePPS = clamp(defaultInt(opt.RatePPS, settings.RatePPS), 1, settings.RatePPS)
	opt.ParallelHosts = clamp(defaultInt(opt.ParallelHosts, settings.ParallelHosts), 1, settings.ParallelHosts)
	opt.CVELookup = opt.CVELookup && settings.CVELookup
	s := &Scan{
		Profile: opt.Profile, Targets: targets, Addresses: addrs, Dropped: res.Dropped,
		UDPPorts: nonNilInts(plan.UDP), StartedBy: opt.StartedBy, tcpPorts: nonNilInts(plan.TCP),
		Options: map[string]any{
			"treat_all_up": opt.TreatAllUp, "timeout_ms": opt.TimeoutMS, "retries": opt.Retries,
			"rate_pps": opt.RatePPS, "parallel_hosts": opt.ParallelHosts, "cve_lookup": opt.CVELookup,
		},
	}
	s.TCPPorts = len(s.tcpPorts)
	return s, res, plan, nil
}

func (m *scanManager) start(app *sdk.AppCtx, pid string, targets []string, opt scanOptions) (*Scan, error) {
	db := app.AppDB()
	s, res, plan, err := m.prepare(context.Background(), db, pid, targets, opt)
	if err != nil {
		return nil, err
	}
	hostnames := map[string]string{}
	for _, a := range res.Allowed {
		if a.Hostname != "" {
			hostnames[a.Address] = a.Hostname
		}
	}
	opt.TimeoutMS, _ = s.Options["timeout_ms"].(int)
	opt.Retries, _ = s.Options["retries"].(int)
	opt.RatePPS, _ = s.Options["rate_pps"].(int)
	opt.ParallelHosts, _ = s.Options["parallel_hosts"].(int)
	opt.CVELookup, _ = s.Options["cve_lookup"].(bool)
	m.mu.Lock()
	if m.byProj[pid] >= maxConcurrentScansPerProject {
		m.mu.Unlock()
		return nil, errors.New("a scan is already running in this project; wait for it or cancel it")
	}
	if err := insertScan(db, pid, s); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running[s.ID] = cancel
	m.byProj[pid]++
	m.mu.Unlock()

	app.EmitWithProject("security.scan.started", pid, map[string]any{"scan_id": s.ID, "profile": s.Profile, "hosts": len(s.Addresses)})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.running, s.ID)
			m.byProj[pid]--
			m.mu.Unlock()
			cancel()
		}()
		m.run(ctx, app, pid, s, plan, opt, hostnames)
	}()
	return s, nil
}

func (m *scanManager) cancel(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.running[id]; ok {
		c()
		return true
	}
	return false
}

func (m *scanManager) cancelAll(db *sql.DB, pid string) int {
	rows, err := db.Query(`SELECT id FROM scans WHERE project_id=? AND status IN ('queued','running')`, pid)
	if err != nil {
		return 0
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if m.cancel(id) {
			n++
		}
	}
	return n
}

func (m *scanManager) exec(db *sql.DB, q string, args ...any) {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	_, _ = db.Exec(q, args...)
}

type hostResult struct {
	addr     string
	hostname string
	up       bool
	services []ServiceInfo
}

func (m *scanManager) run(ctx context.Context, app *sdk.AppCtx, pid string, s *Scan, plan portPlan, opt scanOptions, hostnames map[string]string) {
	db := app.AppDB()
	m.exec(db, `UPDATE scans SET status='running', phase='scanning', started_at=? WHERE id=?`, nowUTC(), s.ID)
	lim := newRateLimiter(opt.RatePPS)
	timeout := time.Duration(opt.TimeoutMS) * time.Millisecond

	results := make([]hostResult, 0, len(s.Addresses))
	var resMu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	var hostsDone, hostsUp, openPorts, findingsNew int
	var critical []FindingDraft

	for i := 0; i < opt.ParallelHosts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range jobs {
				r := m.scanHost(ctx, lim, addr, hostnames[addr], plan, opt, timeout)
				if ctx.Err() != nil {
					return
				}
				newCount, crit := m.persistHost(db, pid, s.ID, r, plan)
				resMu.Lock()
				results = append(results, r)
				hostsDone++
				if r.up {
					hostsUp++
				}
				openPorts += len(r.services)
				findingsNew += newCount
				critical = append(critical, crit...)
				done, up, open, fnew := hostsDone, hostsUp, openPorts, findingsNew
				resMu.Unlock()
				m.exec(db, `UPDATE scans SET hosts_done=?, hosts_up=?, open_ports=?, findings_new=?, probes_sent=? WHERE id=?`,
					done, up, open, fnew, lim.sent.Load(), s.ID)
			}
		}()
	}
	for _, addr := range s.Addresses {
		if ctx.Err() != nil {
			break
		}
		jobs <- addr
	}
	close(jobs)
	wg.Wait()

	if ctx.Err() != nil {
		m.finish(app, pid, s.ID, "cancelled", "", lim)
		return
	}

	// CVE matching once per distinct product/version across all hosts.
	var cveErr error
	if opt.CVELookup {
		m.exec(db, `UPDATE scans SET phase='matching CVEs' WHERE id=?`, s.ID)
		n, crit, err := m.matchCVEs(ctx, db, pid, s.ID, results)
		cveErr = err
		resMu.Lock()
		findingsNew += n
		critical = append(critical, crit...)
		resMu.Unlock()
		m.exec(db, `UPDATE scans SET findings_new=? WHERE id=?`, findingsNew, s.ID)
	}
	if ctx.Err() != nil {
		m.finish(app, pid, s.ID, "cancelled", "", lim)
		return
	}

	// Close findings on live hosts that this scan covered but no longer sees.
	fixed := 0
	for _, r := range results {
		if !r.up {
			continue
		}
		m.dbMu.Lock()
		// CVE findings are only closed when CVE matching ran cleanly.
		n, _ := markFixed(db, pid, r.addr, s.ID, plan.TCP, plan.UDP, !opt.CVELookup || cveErr != nil)
		m.dbMu.Unlock()
		fixed += n
	}

	for _, c := range critical {
		app.EmitWithProject("security.finding.critical", pid, map[string]any{
			"scan_id": s.ID, "address": c.Address, "port": c.Port, "title": c.Title, "cve_ids": c.CVEs,
		})
	}
	msg := ""
	if cveErr != nil {
		msg = "CVE lookup incomplete: " + cveErr.Error()
	}
	m.exec(db, `UPDATE scans SET phase=? WHERE id=?`, fmt.Sprintf("done; %d finding(s) marked fixed", fixed), s.ID)
	m.finish(app, pid, s.ID, "completed", msg, lim)
}

func (m *scanManager) finish(app *sdk.AppCtx, pid string, id int64, status, errMsg string, lim *rateLimiter) {
	db := app.AppDB()
	m.exec(db, `UPDATE scans SET status=?, error=?, probes_sent=?, completed_at=? WHERE id=?`, status, errMsg, lim.sent.Load(), nowUTC(), id)
	if s, err := getScan(db, pid, id); err == nil {
		app.EmitWithProject("security.scan.completed", pid, map[string]any{
			"scan_id": id, "status": status, "hosts_up": s.HostsUp, "open_ports": s.OpenPorts, "findings_new": s.FindingsNew,
		})
	}
}

func (m *scanManager) scanHost(ctx context.Context, lim *rateLimiter, addr, hostname string, plan portPlan, opt scanOptions, timeout time.Duration) hostResult {
	r := hostResult{addr: addr, hostname: hostname}
	if !opt.TreatAllUp && !hostUp(ctx, lim, addr, timeout) {
		return r
	}
	r.up = true
	open := scanTCPPorts(ctx, lim, addr, plan.TCP, timeout, opt.Retries, 32)
	p := &prober{timeout: timeout, limiter: lim, hostname: hostname}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, port := range open {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(port int) {
			defer wg.Done()
			defer func() { <-sem }()
			info := p.fingerprintTCP(ctx, addr, port)
			mu.Lock()
			r.services = append(r.services, info)
			mu.Unlock()
		}(port)
	}
	wg.Wait()
	for _, port := range plan.UDP {
		if ctx.Err() != nil {
			break
		}
		if info, ok := probeUDP(ctx, lim, addr, port, timeout); ok {
			r.services = append(r.services, info)
		}
	}
	sort.Slice(r.services, func(i, j int) bool {
		if r.services[i].Protocol != r.services[j].Protocol {
			return r.services[i].Protocol < r.services[j].Protocol
		}
		return r.services[i].Port < r.services[j].Port
	})
	// A host that answered discovery only with RSTs and has no open port is
	// still "up"; keep it so the inventory shows it.
	return r
}

func (m *scanManager) persistHost(db *sql.DB, pid string, scanID int64, r hostResult, plan portPlan) (int, []FindingDraft) {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	hostID, err := upsertHost(db, pid, r.addr, r.hostname, r.up, scanID)
	if err != nil || !r.up {
		return 0, nil
	}
	_ = saveServices(db, pid, hostID, r.addr, scanID, r.services, plan.TCP, plan.UDP)
	now := time.Now()
	newCount := 0
	var critical []FindingDraft
	for _, svc := range r.services {
		for _, d := range exposureFindings(r.addr, svc, now) {
			isNew, err := upsertFinding(db, pid, scanID, d)
			if err == nil && isNew {
				newCount++
				if d.Severity == "critical" {
					critical = append(critical, d)
				}
			}
		}
	}
	return newCount, critical
}

func (m *scanManager) matchCVEs(ctx context.Context, db *sql.DB, pid string, scanID int64, results []hostResult) (int, []FindingDraft, error) {
	type key struct{ product, version string }
	byKey := map[key][]CVE{}
	keys := []key{}
	for _, r := range results {
		for _, s := range r.services {
			k := key{s.Product, s.Version}
			if s.Version == "" || len(cpeMatchStrings(s.Product, s.Version)) == 0 {
				continue
			}
			if _, ok := byKey[k]; !ok {
				byKey[k] = nil
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return 0, nil, nil
	}
	vs := m.vulns(db)
	var firstErr error
	allIDs := []string{}
	for _, k := range keys {
		cves, err := vs.lookupCVEs(ctx, k.product, k.version)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		byKey[k] = cves
		for _, c := range cves {
			allIDs = append(allIDs, c.ID)
		}
	}
	kev, err := vs.kevSet(ctx)
	if err != nil {
		kev = map[string]bool{}
		if firstErr == nil {
			firstErr = err
		}
	}
	epss := vs.epssScores(ctx, uniqueStrings(allIDs))
	newCount := 0
	var critical []FindingDraft
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	for _, r := range results {
		for _, s := range r.services {
			d := cveFinding(r.addr, s, byKey[key{s.Product, s.Version}], kev, epss)
			if d == nil {
				continue
			}
			isNew, err := upsertFinding(db, pid, scanID, *d)
			if err == nil && isNew {
				newCount++
				if d.Severity == "critical" {
					critical = append(critical, *d)
				}
			}
		}
	}
	return newCount, critical, firstErr
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func nonNilInts(in []int) []int {
	if in == nil {
		return []int{}
	}
	return in
}
