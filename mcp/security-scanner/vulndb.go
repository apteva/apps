package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// CVE is the subset of an NVD record we keep.
type CVE struct {
	ID          string  `json:"id"`
	CVSS        float64 `json:"cvss"`
	Description string  `json:"description,omitempty"`
}

type vulnSource struct {
	db         *sql.DB
	client     *http.Client
	nvdURL     string
	kevURL     string
	epssURL    string
	apiKey     string
	cacheTTL   time.Duration
	mu         sync.Mutex
	lastNVD    time.Time
	nvdSpacing time.Duration
}

func newVulnSource(db *sql.DB, apiKey string) *vulnSource {
	spacing := 6500 * time.Millisecond // public NVD limit: 5 requests per 30 s
	if apiKey != "" {
		spacing = 700 * time.Millisecond
	}
	return &vulnSource{
		db:         db,
		client:     &http.Client{Timeout: 30 * time.Second},
		nvdURL:     "https://services.nvd.nist.gov/rest/json/cves/2.0",
		kevURL:     "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json",
		epssURL:    "https://api.first.org/data/v1/epss",
		apiKey:     apiKey,
		cacheTTL:   24 * time.Hour,
		nvdSpacing: spacing,
	}
}

// cpeVendorProducts maps fingerprinted product names to NVD CPE vendor:product pairs.
var cpeVendorProducts = map[string][]string{
	"OpenSSH":       {"openbsd:openssh"},
	"Dropbear SSH":  {"dropbear_ssh_project:dropbear_ssh"},
	"Apache httpd":  {"apache:http_server"},
	"nginx":         {"f5:nginx", "nginx:nginx"},
	"Microsoft IIS": {"microsoft:internet_information_services"},
	"lighttpd":      {"lighttpd:lighttpd"},
	"vsftpd":        {"beasts:vsftpd"},
	"ProFTPD":       {"proftpd:proftpd"},
	"Exim":          {"exim:exim"},
	"Sendmail":      {"sendmail:sendmail"},
	"MySQL":         {"oracle:mysql"},
	"MariaDB":       {"mariadb:mariadb"},
	"Redis":         {"redis:redis"},
	"Elasticsearch": {"elastic:elasticsearch"},
	"Memcached":     {"memcached:memcached"},
	"CouchDB":       {"apache:couchdb"},
	"BIND":          {"isc:bind"},
	"dnsmasq":       {"thekelleys:dnsmasq"},
	"Unbound":       {"nlnetlabs:unbound"},
	"PHP":           {"php:php"},
	"FileZilla Server": {"filezilla-project:filezilla_server"},
}

var reOpenSSHVersion = regexp.MustCompile(`^(\d+\.\d+)(p\d+)?`)

// cpeMatchStrings builds NVD virtualMatchString values for a product/version.
func cpeMatchStrings(product, version string) []string {
	pairs := cpeVendorProducts[product]
	if len(pairs) == 0 || version == "" {
		return nil
	}
	ver, update := strings.ToLower(version), ""
	if product == "OpenSSH" {
		m := reOpenSSHVersion.FindStringSubmatch(ver)
		if m == nil {
			return nil
		}
		ver, update = m[1], m[2]
	} else {
		ver = regexpVersion.FindString(ver)
		if ver == "" {
			return nil
		}
		ver = strings.TrimRight(ver, ".-")
	}
	out := make([]string, 0, len(pairs))
	for _, vp := range pairs {
		s := "cpe:2.3:a:" + vp + ":" + ver
		if update != "" {
			s += ":" + update
		}
		out = append(out, s)
	}
	return out
}

// lookupCVEs returns CVEs affecting product/version, using the cache first.
func (v *vulnSource) lookupCVEs(ctx context.Context, product, version string) ([]CVE, error) {
	seen := map[string]CVE{}
	for _, match := range cpeMatchStrings(product, version) {
		cves, err := v.cachedNVD(ctx, match)
		if err != nil {
			return nil, err
		}
		for _, c := range cves {
			if old, ok := seen[c.ID]; !ok || c.CVSS > old.CVSS {
				seen[c.ID] = c
			}
		}
	}
	out := make([]CVE, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CVSS != out[j].CVSS {
			return out[i].CVSS > out[j].CVSS
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (v *vulnSource) cachedNVD(ctx context.Context, match string) ([]CVE, error) {
	var raw, fetched string
	err := v.db.QueryRowContext(ctx, `SELECT cves_json, fetched_at FROM cve_cache WHERE cpe=?`, match).Scan(&raw, &fetched)
	if err == nil {
		if t, perr := time.Parse(time.RFC3339, fetched); perr == nil && time.Since(t) < v.cacheTTL {
			var cves []CVE
			if json.Unmarshal([]byte(raw), &cves) == nil {
				return cves, nil
			}
		}
	}
	cves, err := v.fetchNVD(ctx, match)
	if err != nil {
		return nil, err
	}
	_, _ = v.db.ExecContext(ctx, `INSERT INTO cve_cache(cpe,cves_json,fetched_at) VALUES(?,?,?)
        ON CONFLICT(cpe) DO UPDATE SET cves_json=excluded.cves_json, fetched_at=excluded.fetched_at`,
		match, mustJSON(cves), nowUTC())
	return cves, nil
}

type nvdResponse struct {
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE struct {
			ID           string `json:"id"`
			VulnStatus   string `json:"vulnStatus"`
			Descriptions []struct {
				Lang  string `json:"lang"`
				Value string `json:"value"`
			} `json:"descriptions"`
			Metrics map[string][]struct {
				CVSSData struct {
					BaseScore float64 `json:"baseScore"`
				} `json:"cvssData"`
			} `json:"metrics"`
		} `json:"cve"`
	} `json:"vulnerabilities"`
}

func (v *vulnSource) fetchNVD(ctx context.Context, match string) ([]CVE, error) {
	// Respect NVD's public rate limit across concurrent scans.
	v.mu.Lock()
	wait := time.Until(v.lastNVD.Add(v.nvdSpacing))
	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			v.mu.Unlock()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	v.lastNVD = time.Now()
	v.mu.Unlock()

	q := url.Values{"virtualMatchString": {match}, "noRejected": {""}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.nvdURL+"?"+strings.Replace(q.Encode(), "noRejected=", "noRejected", 1), nil)
	if err != nil {
		return nil, err
	}
	if v.apiKey != "" {
		req.Header.Set("apiKey", v.apiKey)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nvd lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nvd lookup for %s: HTTP %d", match, resp.StatusCode)
	}
	var body nvdResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("nvd lookup: %w", err)
	}
	out := make([]CVE, 0, len(body.Vulnerabilities))
	for _, item := range body.Vulnerabilities {
		c := CVE{ID: item.CVE.ID}
		for _, key := range []string{"cvssMetricV40", "cvssMetricV31", "cvssMetricV30", "cvssMetricV2"} {
			if m := item.CVE.Metrics[key]; len(m) > 0 {
				c.CVSS = m[0].CVSSData.BaseScore
				break
			}
		}
		for _, d := range item.CVE.Descriptions {
			if d.Lang == "en" {
				c.Description = truncate(d.Value, 300)
				break
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// kevSet returns the CISA Known Exploited Vulnerabilities catalogue, refreshed daily.
func (v *vulnSource) kevSet(ctx context.Context) (map[string]bool, error) {
	var raw, fetched string
	err := v.db.QueryRowContext(ctx, `SELECT cves_json, fetched_at FROM kev_cache WHERE id=1`).Scan(&raw, &fetched)
	fresh := false
	if err == nil {
		if t, perr := time.Parse(time.RFC3339, fetched); perr == nil && time.Since(t) < v.cacheTTL {
			fresh = true
		}
	}
	if !fresh {
		ids, ferr := v.fetchKEV(ctx)
		if ferr == nil {
			raw = mustJSON(ids)
			_, _ = v.db.ExecContext(ctx, `INSERT INTO kev_cache(id,cves_json,fetched_at) VALUES(1,?,?)
                ON CONFLICT(id) DO UPDATE SET cves_json=excluded.cves_json, fetched_at=excluded.fetched_at`, raw, nowUTC())
		} else if raw == "" {
			return nil, ferr
		}
	}
	var ids []string
	_ = json.Unmarshal([]byte(raw), &ids)
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

func (v *vulnSource) fetchKEV(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.kevURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kev download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kev download: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Vulnerabilities []struct {
			CVEID string `json:"cveID"`
		} `json:"vulnerabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("kev download: %w", err)
	}
	ids := make([]string, 0, len(body.Vulnerabilities))
	for _, item := range body.Vulnerabilities {
		ids = append(ids, item.CVEID)
	}
	return ids, nil
}

// epssScores fetches exploit-probability scores for up to 100 CVEs per request.
func (v *vulnSource) epssScores(ctx context.Context, ids []string) map[string]float64 {
	out := map[string]float64{}
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.epssURL+"?cve="+url.QueryEscape(strings.Join(ids[start:end], ",")), nil)
		if err != nil {
			continue
		}
		resp, err := v.client.Do(req)
		if err != nil {
			continue
		}
		var body struct {
			Data []struct {
				CVE  string `json:"cve"`
				EPSS string `json:"epss"`
			} `json:"data"`
		}
		if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&body) == nil {
			for _, d := range body.Data {
				var f float64
				fmt.Sscan(d.EPSS, &f)
				out[d.CVE] = f
			}
		}
		resp.Body.Close()
	}
	return out
}

var distroHint = regexp.MustCompile(`(?i)\b(ubuntu|debian|deb\d+u\d+|el\d|centos|rhel|red hat|fedora|suse|alpine|amzn)\b`)

// cveFinding summarises all CVEs for one service into a single finding.
func cveFinding(addr string, s ServiceInfo, cves []CVE, kev map[string]bool, epss map[string]float64) *FindingDraft {
	if len(cves) == 0 {
		return nil
	}
	maxCVSS, maxEPSS, anyKEV := 0.0, 0.0, false
	ids := make([]string, 0, len(cves))
	for _, c := range cves {
		ids = append(ids, c.ID)
		maxCVSS = max(maxCVSS, c.CVSS)
		maxEPSS = max(maxEPSS, epss[c.ID])
		anyKEV = anyKEV || kev[c.ID]
	}
	// KEV entries first so they are never hidden by truncation.
	sort.SliceStable(ids, func(i, j int) bool { return kev[ids[i]] && !kev[ids[j]] })
	title := fmt.Sprintf("%s %s has %d known CVE%s (highest CVSS %.1f)", s.Product, s.Version, len(cves), plural(len(cves)), maxCVSS)
	if anyKEV {
		title += ", including actively exploited ones"
	}
	evidence := strings.TrimSpace(fmt.Sprintf("%s/%d reports %s %s. %s", s.Protocol, s.Port, s.Product, s.Version, s.Banner))
	fix := "Upgrade " + s.Product + " to a supported release with these CVEs fixed."
	if distroHint.MatchString(s.Banner) {
		fix += " The banner shows a distribution package, which may include backported fixes without changing the version number; confirm with the vendor advisory before reporting."
	}
	return &FindingDraft{
		Address: addr, Port: s.Port, Protocol: s.Protocol,
		RuleID:   "cve:" + strings.ToLower(strings.ReplaceAll(s.Product, " ", "-")),
		Title:    title,
		Severity: severityFor(maxCVSS, anyKEV),
		CVSS:     maxCVSS, EPSS: maxEPSS, KEV: anyKEV,
		CVEs:     ids, Evidence: evidence, Remediation: fix,
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
