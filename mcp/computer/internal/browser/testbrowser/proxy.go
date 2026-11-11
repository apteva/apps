package testbrowser

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type boundary struct {
	policy    Policy
	server    *http.Server
	listener  net.Listener
	mu        sync.Mutex
	requests  []Request
	blocked   int
	denied    int
	truncated bool
}

func newBoundary(policy Policy) (*boundary, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &boundary{policy: policy, listener: listener}
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}
func (b *boundary) URL() string { return "http://" + b.listener.Addr().String() }
func (b *boundary) close()      { _ = b.server.Close() }
func (b *boundary) record(req Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.requests) < 200 {
		b.requests = append(b.requests, req)
	} else {
		b.truncated = true
	}
}
func (b *boundary) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := *r.URL
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	name := b.policy.Match(&u)
	if r.Method == http.MethodConnect || name == "" {
		b.mu.Lock()
		b.denied++
		// Browser services also use the proxy. Only web-content requests count as
		// page failures; every request is still denied before reaching the network.
		page := r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != ""
		if page {
			b.blocked++
		}
		b.mu.Unlock()
		if page {
			b.record(Request{Method: r.Method, Origin: safeOrigin(&u), Status: http.StatusForbidden})
		}
		http.Error(w, "browser test boundary denied request", http.StatusForbidden)
		return
	}
	target := u
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: target.Scheme, Host: target.Host})
	proxy.Transport = &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second,
	}
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.URL.Path = target.Path
		req.URL.RawPath = target.RawPath
		req.URL.RawQuery = target.RawQuery
		req.Host = target.Host
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Proxy-Connection")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		b.record(Request{Method: r.Method, Origin: safeOrigin(&target), Target: name, Status: resp.StatusCode})
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		b.record(Request{Method: r.Method, Origin: safeOrigin(&target), Target: name, Status: 502})
		http.Error(w, "test upstream unavailable", 502)
	}
	proxy.ServeHTTP(w, r)
}
func safeOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
func (b *boundary) diagnostics() Diagnostics {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Diagnostics{Requests: append([]Request{}, b.requests...), Blocked: b.blocked, BoundaryDenied: b.denied, Truncated: b.truncated}
}
