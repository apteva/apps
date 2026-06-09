package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type ipResolver func(ctx context.Context, host string) ([]net.IP, error)

func defaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.IP)
	}
	return out, nil
}

func guardURL(ctx context.Context, raw string, allowed, blocked []string, resolve ipResolver) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("only http and https URLs are supported")
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return errors.New("URL host is required")
	}
	if matchesDomain(host, blocked) {
		return fmt.Errorf("host %q is blocked", host)
	}
	if len(allowed) > 0 && !matchesDomain(host, allowed) {
		return fmt.Errorf("host %q is not in allowed_domains", host)
	}
	if resolve == nil {
		resolve = defaultResolver
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return fmt.Errorf("host %q resolves to blocked address %s", host, ip.String())
		}
	}
	return nil
}

func matchesDomain(host string, patterns []string) bool {
	host = strings.Trim(strings.ToLower(host), ".")
	for _, pattern := range patterns {
		pattern = strings.Trim(strings.ToLower(strings.TrimSpace(pattern)), ".")
		if pattern == "" {
			continue
		}
		if host == pattern || strings.HasSuffix(host, "."+pattern) {
			return true
		}
	}
	return false
}

func parseDomainList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || v4[0] == 127 {
			return true
		}
	}
	return false
}
