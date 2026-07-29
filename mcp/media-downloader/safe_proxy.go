package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type safeProxy struct {
	server    *http.Server
	listener  net.Listener
	url       string
	resolver  ipResolver
	transport *http.Transport
}

func startSafeProxy(resolve ipResolver) (*safeProxy, error) {
	if resolve == nil {
		resolve = defaultResolver
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start download egress proxy: %w", err)
	}
	proxy := &safeProxy{listener: listener, url: "http://" + listener.Addr().String(), resolver: resolve}
	proxy.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           proxy.safeDialContext,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	proxy.server = &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		_ = proxy.server.Serve(listener)
	}()
	return proxy, nil
}

func (p *safeProxy) Close(ctx context.Context) error {
	if p == nil || p.server == nil {
		return nil
	}
	if p.transport != nil {
		p.transport.CloseIdleConnections()
	}
	return p.server.Shutdown(ctx)
}

func (p *safeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	if r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
		http.Error(w, "only HTTP and HTTPS proxy requests are allowed", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	removeHopHeaders(out.Header)
	if p.transport == nil {
		p.transport = &http.Transport{
			Proxy:                 nil,
			DialContext:           p.safeDialContext,
			ForceAttemptHTTP2:     true,
			ResponseHeaderTimeout: 60 * time.Second,
		}
	}
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "download egress denied: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *safeProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	target, err := p.safeDialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "download egress denied: "+err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		target.Close()
		http.Error(w, "proxy connection hijacking is unavailable", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		target.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		target.Close()
		return
	}
	if buffered.Reader.Buffered() > 0 {
		_, _ = io.CopyN(target, buffered, int64(buffered.Reader.Buffered()))
	}
	go proxyCopy(target, client)
	go proxyCopy(client, target)
}

func proxyCopy(dst net.Conn, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func (p *safeProxy) safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid target %q: %w", address, err)
	}
	ips, err := p.resolver(ctx, strings.Trim(host, "[]"))
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("host %q resolved to no addresses", host)
	}
	var dialErr error
	dialer := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		if blockedIP(ip) {
			dialErr = fmt.Errorf("host %q resolves to blocked address %s", host, ip)
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = err
	}
	if dialErr == nil {
		dialErr = errors.New("no public address available")
	}
	return nil, dialErr
}

func removeHopHeaders(header http.Header) {
	connection := header.Get("Connection")
	for _, key := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(key)
	}
	if connection != "" {
		for _, key := range strings.Split(connection, ",") {
			header.Del(strings.TrimSpace(key))
		}
	}
}
