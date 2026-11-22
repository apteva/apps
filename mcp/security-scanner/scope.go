package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// Allowlist entries are IPs, CIDRs or hostnames. An address is allowed when
// an IP/CIDR entry covers it, or when it was resolved from a hostname target
// that is itself on the allowlist. Exclusions always win.

type scopeEntry struct {
	raw    string
	prefix netip.Prefix // valid for IP/CIDR entries
	host   string       // lower-cased hostname entries
}

type Scope struct {
	allow   []scopeEntry
	exclude []scopeEntry
}

// AddressDecision explains what happened to one expanded address.
type AddressDecision struct {
	Target   string `json:"target"`
	Address  string `json:"address,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Allowed  bool   `json:"allowed"`
	Reason   string `json:"reason"`
}

type ScopeResult struct {
	Allowed []AddressDecision `json:"allowed"`
	Dropped []AddressDecision `json:"dropped"`
}

type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func defaultResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func parseScopeEntry(raw string) (scopeEntry, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return scopeEntry{}, errors.New("empty target")
	}
	if p, err := netip.ParsePrefix(value); err == nil {
		return scopeEntry{raw: value, prefix: p.Masked()}, nil
	}
	if a, err := netip.ParseAddr(value); err == nil {
		a = a.Unmap()
		return scopeEntry{raw: value, prefix: netip.PrefixFrom(a, a.BitLen())}, nil
	}
	if !validHostname(value) {
		return scopeEntry{}, fmt.Errorf("%q is not an IP, CIDR or hostname", raw)
	}
	return scopeEntry{raw: value, host: strings.TrimSuffix(value, ".")}, nil
}

// normalizeTargetValue validates and canonicalises an allowlist or exclusion value.
func normalizeTargetValue(raw string) (string, error) {
	e, err := parseScopeEntry(raw)
	if err != nil {
		return "", err
	}
	if e.host != "" {
		return e.host, nil
	}
	if e.prefix.IsSingleIP() {
		return e.prefix.Addr().String(), nil
	}
	return e.prefix.String(), nil
}

func validHostname(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if len(s) == 0 || len(s) > 253 || !strings.Contains(s, ".") && s != "localhost" {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func newScope(allow, exclude []string) (*Scope, error) {
	s := &Scope{}
	for _, v := range allow {
		e, err := parseScopeEntry(v)
		if err != nil {
			return nil, err
		}
		s.allow = append(s.allow, e)
	}
	for _, v := range exclude {
		e, err := parseScopeEntry(v)
		if err != nil {
			return nil, err
		}
		s.exclude = append(s.exclude, e)
	}
	return s, nil
}

func (s *Scope) excluded(addr netip.Addr, hostname string) string {
	for _, e := range s.exclude {
		if e.host != "" && hostname != "" && e.host == hostname {
			return "hostname " + e.raw + " is excluded"
		}
		if e.host == "" && e.prefix.Contains(addr) {
			return e.raw + " is excluded"
		}
	}
	return ""
}

func (s *Scope) allowedBy(addr netip.Addr, hostname string) string {
	for _, e := range s.allow {
		if e.host != "" && hostname != "" && e.host == hostname {
			return "hostname " + e.raw + " is on the allowlist"
		}
		if e.host == "" && e.prefix.Contains(addr) {
			return e.raw + " is on the allowlist"
		}
	}
	return ""
}

// Expand turns targets into concrete addresses and decides each against the
// allowlist. It never sends traffic to targets (DNS lookups only).
func (s *Scope) Expand(ctx context.Context, targets []string, maxAddresses int, resolve resolver) (*ScopeResult, error) {
	if len(targets) == 0 {
		return nil, errors.New("at least one target is required")
	}
	if resolve == nil {
		resolve = defaultResolver
	}
	out := &ScopeResult{Allowed: []AddressDecision{}, Dropped: []AddressDecision{}}
	seen := map[string]bool{}
	decide := func(target string, addr netip.Addr, hostname string) {
		addr = addr.Unmap()
		key := addr.String()
		if seen[key] {
			return
		}
		seen[key] = true
		d := AddressDecision{Target: target, Address: key, Hostname: hostname}
		if r := s.excluded(addr, hostname); r != "" {
			d.Reason = r
			out.Dropped = append(out.Dropped, d)
			return
		}
		if r := s.allowedBy(addr, hostname); r != "" {
			d.Allowed, d.Reason = true, r
			out.Allowed = append(out.Allowed, d)
			return
		}
		d.Reason = "not on the allowlist"
		out.Dropped = append(out.Dropped, d)
	}
	for _, raw := range targets {
		e, err := parseScopeEntry(raw)
		if err != nil {
			return nil, err
		}
		if e.host != "" {
			addrs, err := resolve(ctx, e.host)
			if err != nil || len(addrs) == 0 {
				out.Dropped = append(out.Dropped, AddressDecision{Target: e.raw, Hostname: e.host, Reason: "hostname did not resolve"})
				continue
			}
			for _, a := range addrs {
				decide(e.raw, a, e.host)
			}
			continue
		}
		size := prefixSize(e.prefix)
		if size > uint64(maxAddresses) {
			return nil, fmt.Errorf("%s expands to %d addresses; the limit is %d per scan", e.raw, size, maxAddresses)
		}
		for a := e.prefix.Addr(); e.prefix.Contains(a); a = a.Next() {
			// Skip network and broadcast addresses for IPv4 ranges larger than /31.
			if e.prefix.Addr().Is4() && e.prefix.Bits() < 31 && (a == e.prefix.Addr() || !e.prefix.Contains(a.Next())) {
				continue
			}
			decide(e.raw, a, "")
			if !a.Next().IsValid() {
				break
			}
		}
		if len(seen) > maxAddresses {
			return nil, fmt.Errorf("targets expand to more than %d addresses", maxAddresses)
		}
	}
	if len(out.Allowed) > maxAddresses {
		return nil, fmt.Errorf("targets expand to %d allowed addresses; the limit is %d per scan", len(out.Allowed), maxAddresses)
	}
	sort.SliceStable(out.Allowed, func(i, j int) bool {
		a, _ := netip.ParseAddr(out.Allowed[i].Address)
		b, _ := netip.ParseAddr(out.Allowed[j].Address)
		return a.Less(b)
	})
	return out, nil
}

func prefixSize(p netip.Prefix) uint64 {
	hostBits := p.Addr().BitLen() - p.Bits()
	if hostBits >= 63 {
		return ^uint64(0)
	}
	return 1 << uint(hostBits)
}
