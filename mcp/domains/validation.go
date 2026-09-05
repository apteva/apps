package main

import (
	"errors"
	"fmt"
	"strings"
)

// ─── Address normalisation ────────────────────────────────────────

// normaliseDomainName strips the scheme/path and any trailing dot,
// lowercases, and validates that what's left looks like a domain.
func normaliseDomainName(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", errors.New("empty domain name")
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	if !looksLikeDomain(s) {
		return "", fmt.Errorf("invalid domain name %q", s)
	}
	return s, nil
}

func looksLikeDomain(s string) bool {
	if len(s) < 3 || len(s) > 253 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n@/?#") {
		return false
	}
	if !strings.Contains(s, ".") {
		return false
	}
	// Reject leading/trailing dot, consecutive dots.
	if s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c == '-' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				continue
			}
			return false
		}
	}
	allNumeric := true
	for _, c := range labels[len(labels)-1] {
		if c < '0' || c > '9' {
			allNumeric = false
			break
		}
	}
	if allNumeric {
		return false
	}
	return true
}

// normaliseRecordType: uppercase, validate against the record types
// most DNS providers and our messaging app care about.
func normaliseRecordType(t string) (string, error) {
	t = strings.ToUpper(strings.TrimSpace(t))
	switch t {
	case "A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "ALIAS", "PTR", "HTTPS", "SVCB", "TLSA", "URL", "URL301", "FRAME", "MXE":
		return t, nil
	}
	return "", fmt.Errorf("unsupported record type %q", t)
}

// normaliseSubaddress: '@' or ” means apex; otherwise lowercase.
func normaliseSubaddress(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "@" {
		return ""
	}
	return s
}

func validateSubaddress(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > 253 || strings.ContainsAny(s, " \t\r\n@/?#") {
		return fmt.Errorf("invalid DNS record name %q", s)
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid DNS record name %q", s)
		}
		for _, c := range label {
			if c == '-' || c == '_' || c == '*' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				continue
			}
			return fmt.Errorf("invalid DNS record name %q", s)
		}
	}
	return nil
}
