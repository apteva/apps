package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const protocol = "apteva.mobile/v1"

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type remote struct {
	base   string
	token  string
	client *http.Client
}

func newRemote(raw, token string) (*remote, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return nil, errors.New("runner_url requires HTTPS or loopback HTTP, without embedded credentials, query or fragment")
	}
	if len(token) < 32 {
		return nil, errors.New("runner_token must contain at least 32 characters")
	}
	return &remote{base: strings.TrimRight(raw, "/"), token: token, client: &http.Client{Timeout: 150 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (r *remote) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, data)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, problem("infrastructure_error", "Mobile runner is unreachable")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 18<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 18<<20 {
		return nil, errors.New("runner response exceeds limit")
	}
	if res.StatusCode >= 300 {
		var failure apiError
		if json.Unmarshal(b, &failure) == nil && failure.Code != "" {
			return nil, &failure
		}
		return nil, problem("infrastructure_error", "Mobile runner rejected request")
	}
	if res.Header.Get("X-Mobile-Protocol") != protocol {
		return nil, problem("infrastructure_error", "Mobile runner protocol mismatch")
	}
	return b, nil
}
func (r *remote) Execute(ctx context.Context, pid, tool string, args map[string]any) (any, error) {
	b, err := r.request(ctx, "POST", "/v1/call", map[string]any{"protocol": protocol, "project_id": pid, "tool": tool, "args": args})
	if err != nil {
		return nil, err
	}
	var v any
	err = json.Unmarshal(b, &v)
	return v, err
}
func (r *remote) Evidence(ctx context.Context, pid, aid string) ([]byte, error) {
	return r.request(ctx, "GET", "/v1/artifacts/"+url.PathEscape(aid)+"?project_id="+url.QueryEscape(pid), nil)
}
func (e *engine) Evidence(_ context.Context, pid, aid string) ([]byte, error) {
	return e.evidence(pid, aid)
}

func runnerHandler(e *engine, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "protocol": protocol})
	})
	mux.HandleFunc("/v1/call", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var req struct {
			Protocol  string         `json:"protocol"`
			ProjectID string         `json:"project_id"`
			Tool      string         `json:"tool"`
			Args      map[string]any `json:"args"`
		}
		if readJSON(w, r, &req) != nil {
			writeFailure(w, problem("invalid_argument", "invalid runner request"))
			return
		}
		if req.Protocol != protocol {
			writeFailure(w, problem("invalid_argument", "unsupported protocol"))
			return
		}
		v, err := e.Execute(r.Context(), req.ProjectID, req.Tool, req.Args)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("/v1/artifacts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "GET required", 405)
			return
		}
		b, err := e.evidence(r.URL.Query().Get("project_id"), strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"))
		if err != nil {
			writeFailure(w, err)
			return
		}
		writePNG(w, b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Mobile-Protocol", protocol)
		if r.URL.Path != "/health" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			if !sameOrigin(r) {
				http.Error(w, "cross-origin requests are not allowed", 403)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func runRunner(args []string) error {
	flags := flag.NewFlagSet("mobile --runner", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:9086", "loopback listen address; expose through an HTTPS reverse proxy or SSH tunnel")
	data := flags.String("data-dir", "", "persistent runner directory (required)")
	appium := flags.String("appium-url", "", "trusted Appium server URL")
	devicesFile := flags.String("devices-file", "", "JSON device inventory file")
	android := flags.String("android-driver", "adb", "adb or appium")
	artifacts := flags.String("artifact-dir", "", "directory of installable APK/IPA files")
	locks := flags.String("lock-dir", "", "shared hardware lock directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("unexpected runner arguments")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !isLoopback(host) {
		return errors.New("runner must listen on loopback; use an authenticated HTTPS reverse proxy or SSH tunnel for remote access")
	}
	token := os.Getenv("APTEVA_MOBILE_RUNNER_TOKEN")
	if len(token) < 32 {
		return errors.New("set APTEVA_MOBILE_RUNNER_TOKEN to at least 32 random characters")
	}
	if *data == "" {
		return errors.New("--data-dir is required")
	}
	if err = os.MkdirAll(*data, 0700); err != nil {
		return err
	}
	var inventory string
	if *devicesFile != "" {
		b, err := os.ReadFile(*devicesFile)
		if err != nil {
			return err
		}
		inventory = string(b)
	}
	db, err := sql.Open("sqlite", filepath.Join(*data, "mobile.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;` + migrationSQL); err != nil {
		return err
	}
	e, err := newEngine(db, *data, newDrivers(runnerConfig{AppiumURL: *appium, DevicesJSON: inventory, AndroidDriver: *android, ArtifactDir: *artifacts, LockDir: *locks}))
	if err != nil {
		return err
	}
	defer e.Shutdown()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: runnerHandler(e, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 165 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
