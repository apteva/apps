package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

type responseError struct {
	Code    int
	Message string
}

func (e *responseError) Error() string        { return e.Message }
func apiError(code int, message string) error { return &responseError{code, message} }
func errorStatus(err error) int {
	var e *responseError
	if errors.As(err, &e) {
		return e.Code
	}
	return 400
}

func strictIntArg(args map[string]any, key string, def, min, max int) (int, error) {
	v, ok := args[key]
	if !ok {
		return def, nil
	}
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x || x > 9007199254740991 || x < -9007199254740991 {
			return 0, fmt.Errorf("%s must be an exact integer", key)
		}
		n = int64(x)
	case json.Number:
		var err error
		n, err = x.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < int64(min) || n > int64(max) {
		return 0, fmt.Errorf("%s must be between %d and %d", key, min, max)
	}
	return int(n), nil
}

func recordHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func recordValueEqual(rtype, a, b string) bool {
	switch rtype {
	case "A", "AAAA":
		pa, e1 := netip.ParseAddr(a)
		pb, e2 := netip.ParseAddr(b)
		return e1 == nil && e2 == nil && pa == pb
	case "CNAME", "NS", "PTR", "ALIAS", "MX", "SRV":
		return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(a), "."), strings.TrimSuffix(strings.TrimSpace(b), "."))
	default:
		return a == b
	}
}
func hasExactDesiredRecord(records []DNSRecord, domain, sub, rtype, value string, ttl, prio int) bool {
	for _, r := range recordsAtName(records, domain, sub, rtype) {
		if recordValueEqual(rtype, r.Value, value) && r.TTL == ttl && r.Prio == prio {
			return true
		}
	}
	return false
}
func recordSubaddress(domain, name string) (string, error) {
	name = normaliseSubaddress(name)
	// Absolute names must remain inside this zone; relative names remain relative.
	absolute := strings.HasSuffix(name, ".")
	name = strings.TrimSuffix(name, ".")
	if name == domain {
		name = ""
	} else if strings.HasSuffix(name, "."+domain) {
		name = strings.TrimSuffix(name, "."+domain)
	} else if absolute {
		return "", errors.New("absolute DNS name is outside the domain")
	}
	if err := validateSubaddress(name); err != nil {
		return "", err
	}
	if len(name) > 0 && len(name)+1+len(domain) > 253 {
		return "", errors.New("DNS owner name exceeds 253 characters")
	}
	for i, label := range strings.Split(name, ".") {
		if strings.Contains(label, "*") && (i != 0 || label != "*") {
			return "", errors.New("wildcard must be the entire first label")
		}
	}
	return name, nil
}

// Priority is distinct from presence: zero is a valid explicit value.
func priorityValue(rtype, value string, previous *DNSRecord) (string, int, error) {
	if rtype != "MX" && rtype != "SRV" {
		return value, 0, nil
	}
	fields := strings.Fields(value)
	prio := 10
	if previous != nil {
		prio = previous.Prio
	}
	want := 2
	if rtype == "SRV" {
		want = 4
	}
	if len(fields) == want {
		n, err := strconv.Atoi(fields[0])
		if err != nil || n < 0 || n > 65535 {
			return "", 0, errors.New("priority must be an integer from 0 to 65535")
		}
		prio = n
		fields = fields[1:]
	} else if !(rtype == "MX" && len(fields) == 1) && !(rtype == "SRV" && previous != nil && len(fields) == 3) {
		return "", 0, fmt.Errorf("%s requires %s", rtype, map[string]string{"MX": "priority host", "SRV": "priority weight port target"}[rtype])
	}
	if rtype == "SRV" {
		for _, v := range fields[:2] {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 65535 {
				return "", 0, errors.New("SRV weight and port must be between 0 and 65535")
			}
		}
	}
	return strings.Join(fields, " "), prio, nil
}

type recordCapabilities struct {
	WriteTypes  []string `json:"write_types"`
	DeleteTypes []string `json:"delete_types"`
	MinTTL      int      `json:"min_ttl"`
	MaxTTL      int      `json:"max_ttl"`
}

func capabilities(slug string) recordCapabilities {
	c := recordCapabilities{WriteTypes: strings.Fields("A AAAA CNAME MX TXT NS SRV CAA ALIAS PTR HTTPS SVCB TLSA"), MinTTL: 60, MaxTTL: 2147483647}
	switch slug {
	case "porkbun":
		c.MinTTL = 600
		c.WriteTypes = strings.Fields("A AAAA CNAME MX TXT NS SRV CAA ALIAS HTTPS SVCB TLSA")
	case "namecheap":
		c.MaxTTL = 60000
		c.WriteTypes = strings.Fields("A AAAA CNAME MX TXT NS CAA ALIAS URL URL301 FRAME MXE")
	case "spaceship":
		c.MaxTTL = 3600
		c.WriteTypes = strings.Fields("A AAAA CNAME MX TXT NS SRV CAA ALIAS PTR")
	case "ionos":
		c.WriteTypes = strings.Fields("A AAAA CNAME MX TXT NS SRV CAA")
	}
	c.DeleteTypes = append([]string(nil), c.WriteTypes...)
	if slug == "spaceship" {
		c.DeleteTypes = append(c.DeleteTypes, "HTTPS", "SVCB", "TLSA")
	}
	return c
}
func includes(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func validateRecordValue(slug, rtype, value string, ttl int) error {
	c := capabilities(slug)
	if !includes(c.WriteTypes, rtype) {
		return fmt.Errorf("%s cannot write %s records", slug, rtype)
	}
	if ttl < c.MinTTL || ttl > c.MaxTTL {
		return fmt.Errorf("%s TTL must be between %d and %d", slug, c.MinTTL, c.MaxTTL)
	}
	if strings.ContainsAny(value, "\x00\r\n") && rtype != "TXT" {
		return errors.New("invalid record value")
	}
	switch rtype {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(value)
		if err != nil || rtype == "A" && !ip.Is4() || rtype == "AAAA" && !ip.Is6() {
			return fmt.Errorf("invalid %s address", rtype)
		}
	case "MX", "SRV":
		content, _, err := priorityValue(rtype, value, nil)
		if err != nil {
			return err
		}
		fields := strings.Fields(content)
		target := fields[len(fields)-1]
		if target != "." && !looksLikeDomain(strings.TrimSuffix(strings.ToLower(target), ".")) {
			return errors.New("invalid mail/service target")
		}
		if slug == "spaceship" && rtype == "SRV" && fields[1] == "0" {
			return errors.New("Spaceship SRV port must be at least 1")
		}
		return nil
	case "CNAME", "NS", "ALIAS", "PTR":
		if !looksLikeDomain(strings.TrimSuffix(strings.ToLower(value), ".")) {
			return errors.New("record target must be a domain name")
		}
	case "CAA":
		f := strings.Fields(value)
		if len(f) < 3 {
			return errors.New("CAA requires flag tag value")
		}
		n, err := strconv.Atoi(f[0])
		if err != nil || n < 0 || n > 255 {
			return errors.New("CAA flag must be 0 to 255")
		}
	}
	return nil
}

const createRecordID = "__domains_create_only__"

func spaceshipOwner(m map[string]any, domain string) string {
	name := spaceshipStringField(m, "name")
	if strings.EqualFold(spaceshipStringField(m, "type"), "SRV") && spaceshipStringField(m, "service") != "" && spaceshipStringField(m, "protocol") != "" {
		prefix := spaceshipStringField(m, "service") + "." + spaceshipStringField(m, "protocol")
		if name == "@" || name == "" || name == domain {
			name = prefix
		} else {
			name = prefix + "." + name
		}
	}
	return spaceshipCanonicalName(domain, name)
}
