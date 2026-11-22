package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Servers that host several sites (Caddy, nginx, Traefik, CDNs) often refuse
// a TLS handshake that carries no server name, so scanning a bare IP would
// miss the certificate and TLS checks. For addresses scanned without a
// hostname we pick a name to send as SNI (and as the HTTP Host header):
//
//  1. a hostname on the project allowlist that resolves to the address, else
//  2. the reverse-DNS (PTR) name, if it resolves back to the same address.
//
// The name only selects which site on an already-allowed address answers;
// it never widens scope. Names on the exclusion list are never used.

const (
	maxSNIAllowlistNames = 64
	maxSNIReverseLookups = 256
	sniLookupTimeout     = 3 * time.Second
)

type reverseResolver func(ctx context.Context, addr string) ([]string, error)

func defaultReverseResolver(ctx context.Context, addr string) ([]string, error) {
	return net.DefaultResolver.LookupAddr(ctx, addr)
}

// tlsNames returns address -> hostname for addresses in addrs that have no
// name in have. It does not modify have.
func tlsNames(ctx context.Context, scope *Scope, addrs []string, have map[string]string, resolve resolver, reverse reverseResolver) map[string]string {
	if resolve == nil {
		resolve = defaultResolver
	}
	if reverse == nil {
		reverse = defaultReverseResolver
	}
	missing := map[string]netip.Addr{}
	for _, a := range addrs {
		if have[a] != "" {
			continue
		}
		if ip, err := netip.ParseAddr(a); err == nil {
			missing[a] = ip
		}
	}
	out := map[string]string{}
	if len(missing) == 0 || scope == nil {
		return out
	}
	usable := func(ip netip.Addr, name string) bool {
		return name != "" && validHostname(name) && scope.excluded(ip, name) == ""
	}

	// 1. Allowlisted hostnames, in allowlist order (first match wins).
	looked := 0
	for _, e := range scope.allow {
		if e.host == "" || looked >= maxSNIAllowlistNames || len(out) == len(missing) {
			continue
		}
		looked++
		lctx, cancel := context.WithTimeout(ctx, sniLookupTimeout)
		ips, err := resolve(lctx, e.host)
		cancel()
		if err != nil {
			continue
		}
		for _, ip := range ips {
			key := ip.Unmap().String()
			if want, ok := missing[key]; ok && out[key] == "" && usable(want, e.host) {
				out[key] = e.host
			}
		}
	}

	// 2. Forward-confirmed reverse DNS for whatever is still unnamed.
	var todo []string
	for a := range missing {
		if out[a] == "" && len(todo) < maxSNIReverseLookups {
			todo = append(todo, a)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, a := range todo {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(a string) {
			defer wg.Done()
			defer func() { <-sem }()
			ip := missing[a]
			lctx, cancel := context.WithTimeout(ctx, sniLookupTimeout)
			defer cancel()
			names, err := reverse(lctx, a)
			if err != nil {
				return
			}
			for _, n := range names {
				n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
				if !usable(ip, n) {
					continue
				}
				fwd, err := resolve(lctx, n)
				if err != nil {
					continue
				}
				for _, f := range fwd {
					if f.Unmap() == ip.Unmap() {
						mu.Lock()
						out[a] = n
						mu.Unlock()
						return
					}
				}
			}
		}(a)
	}
	wg.Wait()
	return out
}
