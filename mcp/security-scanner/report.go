package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var severityOrder = []string{"critical", "high", "medium", "low", "info"}

// buildReport renders a severity-grouped Markdown report for one scan or the whole project.
func buildReport(db *sql.DB, pid string, scanID int64, includeInfo bool) (map[string]any, error) {
	filter := findingFilter{Limit: 500}
	var scan *Scan
	title := "Security scan report: all open findings"
	if scanID > 0 {
		var err error
		scan, err = getScan(db, pid, scanID)
		if err != nil {
			return nil, err
		}
		filter.ScanID = scanID
		title = fmt.Sprintf("Security scan report: scan #%d", scanID)
	}
	findings, total, err := searchFindings(db, pid, filter)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	grouped := map[string][]Finding{}
	for _, f := range findings {
		counts[f.Severity]++
		grouped[f.Severity] = append(grouped[f.Severity], f)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nGenerated %s.\n\n", title, nowUTC())
	if scan != nil {
		fmt.Fprintf(&b, "Scan #%d, %s profile, %s. Targets: %s. %d of %d hosts up, %d open ports.\n\n",
			scan.ID, scan.Profile, scan.Status, strings.Join(scan.Targets, ", "), scan.HostsUp, scan.HostsTotal, scan.OpenPorts)
		if scan.Error != "" {
			fmt.Fprintf(&b, "Note: %s\n\n", scan.Error)
		}
	}
	b.WriteString("## Summary\n\n| Severity | Open findings |\n| --- | --- |\n")
	for _, sev := range severityOrder {
		fmt.Fprintf(&b, "| %s | %d |\n", titleCase(sev), counts[sev])
	}
	if total > len(findings) {
		fmt.Fprintf(&b, "\nShowing the %d most severe of %d findings.\n", len(findings), total)
	}
	for _, sev := range severityOrder {
		if sev == "info" && !includeInfo {
			if counts["info"] > 0 {
				fmt.Fprintf(&b, "\n%d informational finding(s) omitted.\n", counts["info"])
			}
			continue
		}
		items := grouped[sev]
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (%d)\n", titleCase(sev), len(items))
		for _, f := range items {
			where := f.Address
			if f.Port > 0 {
				where = fmt.Sprintf("%s %s/%d", f.Address, f.Protocol, f.Port)
			}
			fmt.Fprintf(&b, "\n### %s\n\n- **Where:** %s\n- **Status:** %s\n", f.Title, where, strings.ReplaceAll(f.Status, "_", " "))
			if f.CVSS > 0 {
				fmt.Fprintf(&b, "- **CVSS:** %.1f", f.CVSS)
				if f.EPSS > 0 {
					fmt.Fprintf(&b, " · **EPSS:** %.1f%%", f.EPSS*100)
				}
				if f.KEV {
					b.WriteString(" · **On CISA KEV list**")
				}
				b.WriteString("\n")
			}
			if len(f.CVEs) > 0 {
				shown := f.CVEs
				if len(shown) > 10 {
					shown = shown[:10]
				}
				fmt.Fprintf(&b, "- **CVEs:** %s", strings.Join(shown, ", "))
				if len(f.CVEs) > 10 {
					fmt.Fprintf(&b, " and %d more", len(f.CVEs)-10)
				}
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "- **Evidence:** %s\n- **Fix:** %s\n", f.Evidence, f.Remediation)
			if f.Note != "" {
				fmt.Fprintf(&b, "- **Analyst note:** %s\n", f.Note)
			}
		}
	}
	return map[string]any{"markdown": b.String(), "counts": counts, "total": total, "scan": scan}, nil
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

type serviceKey struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Service  string `json:"service"`
	Product  string `json:"product,omitempty"`
	Version  string `json:"version,omitempty"`
}

func scanServiceSet(db *sql.DB, scanID int64) (map[string]serviceKey, error) {
	rows, err := db.Query(`SELECT address,port,protocol,service,product,version FROM scan_services WHERE scan_id=?`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]serviceKey{}
	for rows.Next() {
		var k serviceKey
		if err := rows.Scan(&k.Address, &k.Port, &k.Protocol, &k.Service, &k.Product, &k.Version); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s|%s|%d", k.Address, k.Protocol, k.Port)] = k
	}
	return out, rows.Err()
}

// diffScans compares a scan with an earlier completed scan that shares at least one address.
func diffScans(db *sql.DB, pid string, scanID, againstID int64) (map[string]any, error) {
	cur, err := getScan(db, pid, scanID)
	if err != nil {
		return nil, err
	}
	if againstID == 0 {
		prev, err := listScans(db, pid, "completed", 200)
		if err != nil {
			return nil, err
		}
		curAddrs := map[string]bool{}
		for _, a := range cur.Addresses {
			curAddrs[a] = true
		}
		for _, p := range prev {
			if p.ID >= scanID {
				continue
			}
			full, err := getScan(db, pid, p.ID)
			if err != nil {
				continue
			}
			for _, a := range full.Addresses {
				if curAddrs[a] {
					againstID = p.ID
					break
				}
			}
			if againstID != 0 {
				break
			}
		}
	}
	if againstID == 0 {
		return nil, errors.New("no earlier completed scan covers any of these addresses")
	}
	prevScan, err := getScan(db, pid, againstID)
	if err != nil {
		return nil, err
	}
	a, err := scanServiceSet(db, againstID)
	if err != nil {
		return nil, err
	}
	b, err := scanServiceSet(db, scanID)
	if err != nil {
		return nil, err
	}
	opened, closed, changed := []serviceKey{}, []serviceKey{}, []map[string]any{}
	for k, v := range b {
		old, ok := a[k]
		switch {
		case !ok:
			opened = append(opened, v)
		case old.Product != v.Product || old.Version != v.Version:
			changed = append(changed, map[string]any{"before": old, "after": v})
		}
	}
	// Only report a port as closed when the newer scan actually covered it.
	for k, v := range a {
		if _, ok := b[k]; ok {
			continue
		}
		covered := containsString(cur.Addresses, v.Address) &&
			((v.Protocol == "tcp" && containsSorted(cur.tcpPorts, v.Port)) || (v.Protocol == "udp" && containsInt(cur.UDPPorts, v.Port)))
		if covered {
			closed = append(closed, v)
		}
	}
	sortKeys := func(list []serviceKey) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Address != list[j].Address {
				return list[i].Address < list[j].Address
			}
			return list[i].Port < list[j].Port
		})
	}
	sortKeys(opened)
	sortKeys(closed)

	newFindings, _, err := searchFindings(db, pid, findingFilter{Status: []string{"all"}, Limit: 500})
	if err != nil {
		return nil, err
	}
	added, fixed := []Finding{}, []Finding{}
	for _, f := range newFindings {
		if f.FirstScan == scanID {
			added = append(added, f)
		}
		if f.FixedScan == scanID {
			fixed = append(fixed, f)
		}
	}
	return map[string]any{
		"scan_id": scanID, "compared_with": againstID, "compared_with_completed_at": prevScan.CompletedAt,
		"new_open_ports": opened, "closed_ports": closed, "version_changes": changed,
		"new_findings": added, "fixed_findings": fixed,
	}, nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
