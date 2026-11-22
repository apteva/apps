package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var regexpVersion = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?[\w.-]*`)

// FindingDraft is a finding produced by a rule before it is stored.
type FindingDraft struct {
	Address     string
	Port        int
	Protocol    string
	RuleID      string
	Title       string
	Severity    string
	CVSS        float64
	EPSS        float64
	KEV         bool
	CVEs        []string
	Evidence    string
	Remediation string
}

var severityRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1, "info": 0}

// severityFor maps CVSS + KEV to our five levels.
func severityFor(cvss float64, kev bool) string {
	switch {
	case kev || cvss >= 9.0:
		return "critical"
	case cvss >= 7.0:
		return "high"
	case cvss >= 4.0:
		return "medium"
	case cvss > 0:
		return "low"
	default:
		return "info"
	}
}

func detailBool(s ServiceInfo, key string) (bool, bool) {
	v, ok := s.Details[key].(bool)
	return v, ok
}

func detailString(s ServiceInfo, key string) string {
	v, _ := s.Details[key].(string)
	return v
}

// exposureFindings applies rules that need no CVE data: risky services,
// missing authentication, cleartext protocols and TLS hygiene.
func exposureFindings(addr string, s ServiceInfo, now time.Time) []FindingDraft {
	base := func(rule, title, sev, evidence, fix string) FindingDraft {
		return FindingDraft{Address: addr, Port: s.Port, Protocol: s.Protocol, RuleID: rule, Title: title, Severity: sev, Evidence: evidence, Remediation: fix}
	}
	where := fmt.Sprintf("%s/%d", s.Protocol, s.Port)
	var out []FindingDraft

	switch s.Service {
	case "telnet":
		out = append(out, base("exposure:telnet", "Telnet service exposed", "high",
			"Telnet negotiation received on "+where,
			"Disable Telnet and use SSH. If the device needs it, restrict it to a management network."))
	case "ftp":
		out = append(out, base("exposure:ftp-cleartext", "Cleartext FTP service exposed", "medium",
			strings.TrimSpace("FTP banner on "+where+": "+s.Banner),
			"Replace FTP with SFTP or FTPS, or restrict access by firewall."))
	case "redis":
		if unauth, ok := detailBool(s, "unauthenticated"); ok && unauth {
			out = append(out, base("exposure:redis-noauth", "Redis accepts commands without authentication", "high",
				"PING answered +PONG on "+where+" without credentials",
				"Enable requirepass or ACLs, bind Redis to localhost or a private network, and block the port at the firewall."))
		}
	case "elasticsearch":
		if unauth, _ := detailBool(s, "unauthenticated"); unauth {
			out = append(out, base("exposure:elasticsearch-noauth", "Elasticsearch API readable without authentication", "high",
				fmt.Sprintf("GET / returned cluster info (version %s) on %s", s.Version, where),
				"Enable Elasticsearch security (authentication and TLS) and keep the API off public networks."))
		}
	case "couchdb":
		if unauth, _ := detailBool(s, "unauthenticated"); unauth {
			out = append(out, base("exposure:couchdb-open", "CouchDB API reachable", "medium",
				"GET / returned the CouchDB welcome document on "+where,
				"Require authentication and keep CouchDB off public networks."))
		}
	case "docker":
		if unauth, _ := detailBool(s, "unauthenticated"); unauth {
			out = append(out, base("exposure:docker-api", "Docker Engine API exposed without TLS", "critical",
				"Docker API answered over plain HTTP on "+where+"; anyone who can reach it controls the host",
				"Stop exposing the Docker socket over TCP, or require mutual TLS and restrict it by firewall."))
		}
	case "memcached":
		out = append(out, base("exposure:memcached", "Memcached reachable without authentication", "medium",
			fmt.Sprintf("version command answered %s on %s", s.Version, where),
			"Bind Memcached to localhost or a private network and disable UDP."))
	case "mysql", "postgresql", "mongodb", "mssql", "oracle", "cassandra":
		out = append(out, base("exposure:database-port", "Database port reachable from the scanner", "medium",
			fmt.Sprintf("%s listening on %s", nonEmpty(s.Product, s.Service), where),
			"Databases should only be reachable from application hosts. Restrict the port by firewall or security group."))
	case "rdp":
		out = append(out, base("exposure:rdp", "Remote Desktop exposed", "medium",
			"RDP port open on "+where,
			"Put RDP behind a VPN or gateway and enforce Network Level Authentication."))
	case "smb", "netbios-ssn":
		out = append(out, base("exposure:smb", "SMB file sharing exposed", "medium",
			"SMB port open on "+where,
			"Block SMB (139/445) at the network edge."))
	case "vnc":
		out = append(out, base("exposure:vnc", "VNC remote desktop exposed", "medium",
			"RFB handshake "+s.Version+" on "+where,
			"Put VNC behind a VPN or SSH tunnel and require strong authentication."))
	case "kubelet", "etcd":
		out = append(out, base("exposure:"+s.Service, "Kubernetes component port exposed ("+s.Service+")", "high",
			s.Service+" port open on "+where,
			"Keep cluster control-plane and node APIs on private networks."))
	case "snmp":
		out = append(out, base("exposure:snmp", "SNMP service answers", "low",
			"SNMPv3 discovery answered on "+where,
			"Restrict SNMP to monitoring hosts and use SNMPv3 with authentication and privacy."))
	case "dns":
		if s.Banner != "" {
			out = append(out, base("exposure:dns-version", "DNS server discloses its version", "low",
				"version.bind returned \""+s.Banner+"\" on "+where,
				"Hide the version string (for BIND: options { version \"none\"; })."))
		}
	}

	// HTTP hygiene.
	if server := detailString(s, "server_header"); server != "" && regexpVersion.MatchString(server) {
		out = append(out, base("http:version-disclosure", "Web server discloses its version", "low",
			"Server: "+server, "Remove version details from the Server header (for nginx: server_tokens off)."))
	}
	if missing, ok := s.Details["missing_headers"].([]string); ok && len(missing) > 0 {
		out = append(out, base("http:missing-security-headers", "Missing HTTP security headers", "info",
			"Missing: "+strings.Join(missing, ", "), "Add the missing headers in the web server or application."))
	}

	// TLS hygiene.
	if s.TLS {
		if notAfter := detailString(s, "cert_not_after"); notAfter != "" {
			if t, err := time.Parse(time.RFC3339, notAfter); err == nil {
				switch {
				case t.Before(now):
					out = append(out, base("tls:cert-expired", "TLS certificate has expired", "medium",
						"Certificate expired on "+t.Format("2006-01-02"), "Renew the certificate."))
				case t.Before(now.Add(14 * 24 * time.Hour)):
					out = append(out, base("tls:cert-expiring", "TLS certificate expires within 14 days", "medium",
						"Certificate expires on "+t.Format("2006-01-02"), "Renew the certificate and check that auto-renewal works."))
				}
			}
		}
		if self, _ := detailBool(s, "cert_self_signed"); self {
			out = append(out, base("tls:self-signed", "Self-signed TLS certificate", "low",
				"Issuer equals subject: "+detailString(s, "cert_subject"), "Use a certificate from a trusted CA for public services."))
		}
		if legacy := detailString(s, "legacy_tls"); legacy != "" {
			out = append(out, base("tls:legacy-protocol", "Server accepts "+legacy, "medium",
				"Handshake succeeded with "+legacy, "Disable TLS 1.0 and 1.1; allow TLS 1.2 and 1.3 only."))
		}
	}
	return out
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
