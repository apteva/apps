package main

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// rateLimiter hands out one token every 1/pps seconds; every connection
// attempt and UDP datagram waits for a token.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	sent     atomic.Int64
}

func newRateLimiter(pps int) *rateLimiter {
	if pps < 1 {
		pps = 1
	}
	return &rateLimiter{interval: time.Second / time.Duration(pps)}
}

func (r *rateLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	if r.next.Before(now) {
		r.next = now
	}
	wait := r.next.Sub(now)
	r.next = r.next.Add(r.interval)
	r.mu.Unlock()
	r.sent.Add(1)
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type portState int

const (
	portClosed portState = iota
	portOpen
	portFiltered
)

// connectProbe does a full TCP connect and closes immediately.
func connectProbe(ctx context.Context, lim *rateLimiter, addr string, port int, timeout time.Duration) portState {
	if err := lim.Wait(ctx); err != nil {
		return portFiltered
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err == nil {
		conn.Close()
		return portOpen
	}
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused") {
		return portClosed
	}
	return portFiltered
}

var discoveryPorts = []int{443, 80, 22, 3389, 445}

// hostUp treats a host as up when any discovery port connects or actively
// refuses (a RST proves something answered).
func hostUp(ctx context.Context, lim *rateLimiter, addr string, timeout time.Duration) bool {
	for _, port := range discoveryPorts {
		if ctx.Err() != nil {
			return false
		}
		if st := connectProbe(ctx, lim, addr, port, timeout); st == portOpen || st == portClosed {
			return true
		}
	}
	return false
}

// scanTCPPorts connect-scans ports with bounded parallelism per host.
func scanTCPPorts(ctx context.Context, lim *rateLimiter, addr string, ports []int, timeout time.Duration, retries, parallel int) []int {
	if parallel < 1 {
		parallel = 1
	}
	var mu sync.Mutex
	open := []int{}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for port := range jobs {
				st := connectProbe(ctx, lim, addr, port, timeout)
				for r := 0; st == portFiltered && r < retries && ctx.Err() == nil; r++ {
					st = connectProbe(ctx, lim, addr, port, timeout)
				}
				if st == portOpen {
					mu.Lock()
					open = append(open, port)
					mu.Unlock()
				}
			}
		}()
	}
	for _, p := range ports {
		if ctx.Err() != nil {
			break
		}
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return uniqueSorted(open)
}

// probeUDP sends one protocol-correct, read-only request and reports a
// service only when it answers. Silence means open|filtered and is not recorded.
func probeUDP(ctx context.Context, lim *rateLimiter, addr string, port int, timeout time.Duration) (ServiceInfo, bool) {
	var payload []byte
	info := ServiceInfo{Port: port, Protocol: "udp"}
	switch port {
	case 53:
		payload, info.Service = dnsVersionQuery(), "dns"
	case 123:
		payload, info.Service = ntpClientRequest(), "ntp"
	case 161:
		payload, info.Service = snmpV3Discovery(), "snmp"
	default:
		return info, false
	}
	if err := lim.Wait(ctx); err != nil {
		return info, false
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(addr, strconv.Itoa(port)), timeout)
	if err != nil {
		return info, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout + time.Second))
	if _, err := conn.Write(payload); err != nil {
		return info, false
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return info, false
	}
	switch port {
	case 53:
		if v := parseDNSTXT(buf[:n]); v != "" {
			info.Banner = v
			lv := strings.ToLower(v)
			ver := regexpVersion.FindString(v)
			switch {
			case strings.Contains(lv, "dnsmasq"):
				info.Product, info.Version = "dnsmasq", ver
			case strings.Contains(lv, "unbound"):
				info.Product, info.Version = "Unbound", ver
			case strings.Contains(lv, "powerdns"):
				info.Product, info.Version = "PowerDNS", ver
			case ver != "":
				// version.bind is answered with a bare version string by BIND.
				info.Product, info.Version = "BIND", ver
			}
		}
	case 123:
		if n >= 48 {
			info.set("stratum", int(buf[1]))
		}
	case 161:
		info.set("snmp_v3", true)
	}
	return info, true
}

func dnsVersionQuery() []byte {
	// ID 0x5ec5, RD=0, one question: version.bind TXT CH.
	q := []byte{0x5e, 0xc5, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	q = append(q, 7, 'v', 'e', 'r', 's', 'i', 'o', 'n', 4, 'b', 'i', 'n', 'd', 0)
	return append(q, 0x00, 0x10, 0x00, 0x03)
}

// parseDNSTXT returns the first TXT string in a DNS answer, if any.
func parseDNSTXT(b []byte) string {
	if len(b) < 12 || binary.BigEndian.Uint16(b[6:8]) == 0 {
		return ""
	}
	i := 12
	// Skip the question name + type/class.
	for i < len(b) && b[i] != 0 {
		if b[i]&0xC0 == 0xC0 {
			i++
			break
		}
		i += int(b[i]) + 1
	}
	i += 5
	if i >= len(b) {
		return ""
	}
	// Answer name (usually a pointer).
	if b[i]&0xC0 == 0xC0 {
		i += 2
	} else {
		for i < len(b) && b[i] != 0 {
			i += int(b[i]) + 1
		}
		i++
	}
	if i+10 > len(b) || binary.BigEndian.Uint16(b[i:i+2]) != 16 {
		return ""
	}
	rdlen := int(binary.BigEndian.Uint16(b[i+8 : i+10]))
	i += 10
	if i+rdlen > len(b) || rdlen < 1 {
		return ""
	}
	l := int(b[i])
	if l+1 > rdlen {
		return ""
	}
	return printable(b[i+1 : i+1+l])
}

func ntpClientRequest() []byte {
	p := make([]byte, 48)
	p[0] = 0x1b // LI=0, VN=3, Mode=3 (client)
	return p
}

// snmpV3Discovery builds an unauthenticated SNMPv3 engine-discovery GET,
// which every v3 agent answers with a Report PDU. No community strings.
func snmpV3Discovery() []byte {
	octet := func(b []byte) []byte { return berTLV(0x04, b) }
	integer := func(v int) []byte {
		b := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		for len(b) > 1 && b[0] == 0 && b[1]&0x80 == 0 {
			b = b[1:]
		}
		return berTLV(0x02, b)
	}
	seq := func(parts ...[]byte) []byte {
		var body []byte
		for _, p := range parts {
			body = append(body, p...)
		}
		return berTLV(0x30, body)
	}
	header := seq(integer(0x4a69), integer(65507), octet([]byte{0x04}), integer(3))
	usm := octet(seq(octet(nil), integer(0), integer(0), octet(nil), octet(nil), octet(nil)))
	pdu := berTLV(0xa0, append(append(append(integer(0x37f0), integer(0)...), integer(0)...), seq()...))
	scoped := seq(octet(nil), octet(nil), pdu)
	return seq(integer(3), header, usm, scoped)
}

func berTLV(tag byte, body []byte) []byte {
	out := []byte{tag}
	n := len(body)
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	case n < 0x100:
		out = append(out, 0x81, byte(n))
	default:
		out = append(out, 0x82, byte(n>>8), byte(n))
	}
	return append(out, body...)
}
