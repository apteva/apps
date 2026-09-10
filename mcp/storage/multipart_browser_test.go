package main

// Optional end-to-end fixture: run STORAGE_BROWSER_TEST_SERVER=1 go test -run
// '^TestBrowserMultipartServer$' -timeout 10m, then STORAGE_TEST_BACKEND=http://127.0.0.1:19182
// bun run test. Only loopback servers and temporary data are used.
import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/cors"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestBrowserMultipartServer(t *testing.T) {
	if os.Getenv("STORAGE_BROWSER_TEST_SERVER") != "1" {
		t.Skip("optional browser fixture")
	}
	rec := tk.NewEmitRecorder()
	platform := &dashboardPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform), tk.WithProjectID(""), tk.WithEmitter(rec), tk.WithEnv("APTEVA_PUBLIC_URL", "http://127.0.0.1:19180"), tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()), tk.WithConfig(map[string]string{"max_upload_size_mb": "5120", "s3_part_size_mb": "16"}))
	type session struct {
		key      string
		parts    map[int]remotePart
		complete bool
	}
	var mu sync.Mutex
	sessions := map[string]*session{}
	scenario := "cors-denied"
	var config cors.Config
	serial, active, peak, writes, eventsBefore := 0, 0, 0, 0, 0
	var received, apiBytes int64
	s3 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		q := r.URL.Query()
		if r.URL.Path == "/__browser-probe" {
			mu.Unlock()
			w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:19180")
			w.WriteHeader(200)
			return
		}
		if r.Method == "OPTIONS" {
			allowed := len(config.CORSRules) > 0 || (scenario == "browser-denied" && !strings.Contains(r.UserAgent(), "Chrome"))
			mu.Unlock()
			if !allowed {
				w.WriteHeader(403)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:19180")
			w.Header().Set("Access-Control-Allow-Methods", "PUT")
			w.Header().Set("Access-Control-Allow-Headers", "content-type")
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if q.Has("cors") {
			defer mu.Unlock()
			if scenario == "cors-denied" {
				w.WriteHeader(403)
				fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
				return
			}
			if r.Method == "GET" {
				if len(config.CORSRules) == 0 {
					w.WriteHeader(404)
					fmt.Fprint(w, `<Error><Code>NoSuchCORSConfiguration</Code></Error>`)
					return
				}
				_ = xml.NewEncoder(w).Encode(config)
				return
			}
			if r.Method == "PUT" {
				writes++
				if err := xml.NewDecoder(r.Body).Decode(&config); err != nil {
					t.Error(err)
				}
				return
			}
		}
		if r.Method == "POST" && q.Has("uploads") {
			serial++
			id := fmt.Sprint(serial)
			sessions[id] = &session{key: r.URL.Path, parts: map[int]remotePart{}}
			mu.Unlock()
			fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>test-bucket</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, r.URL.Path, id)
			return
		}
		if r.Method == "HEAD" {
			defer mu.Unlock()
			for _, s := range sessions {
				if s.key == r.URL.Path && s.complete {
					var total int64
					for _, p := range s.parts {
						total += p.Size
					}
					w.Header().Set("Content-Length", fmt.Sprint(total))
					w.Header().Set("Last-Modified", "Thu, 10 Sep 2026 10:00:00 GMT")
					w.Header().Set("ETag", `"complete-etag"`)
					return
				}
			}
			w.WriteHeader(404)
			return
		}
		s := sessions[q.Get("uploadId")]
		if s == nil {
			mu.Unlock()
			w.WriteHeader(404)
			return
		}
		if r.Method == "PUT" {
			n, _ := strconv.Atoi(q.Get("partNumber"))
			active++
			peak = max(peak, active)
			mu.Unlock()
			count, err := io.Copy(io.Discard, r.Body)
			mu.Lock()
			active--
			if err == nil {
				received += count
				s.parts[n] = remotePart{n, count, fmt.Sprint("etag-", n)}
			}
			mu.Unlock()
			if err != nil {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:19180")
			w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, n))
			return
		}
		defer mu.Unlock()
		if r.Method == "GET" {
			nums := []int{}
			for n := range s.parts {
				nums = append(nums, n)
			}
			sort.Ints(nums)
			fmt.Fprint(w, `<ListPartsResult><IsTruncated>false</IsTruncated>`)
			for _, n := range nums {
				p := s.parts[n]
				fmt.Fprintf(w, `<Part><PartNumber>%d</PartNumber><ETag>%s</ETag><Size>%d</Size></Part>`, p.Number, p.ETag, p.Size)
			}
			fmt.Fprint(w, `</ListPartsResult>`)
			return
		}
		if r.Method == "POST" {
			s.complete = true
			fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>test-bucket</Bucket><Key>video.mp4</Key><ETag>complete-etag</ETag></CompleteMultipartUploadResult>`)
			return
		}
		if r.Method == "DELETE" {
			delete(sessions, q.Get("uploadId"))
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(400)
	}))
	defer s3.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = s3.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()
	client, err := minio.New(strings.TrimPrefix(s3.URL, "https://"), &minio.Options{Creds: credentials.NewStaticV4("test-key", "test-secret", ""), Region: "us-east-1", Secure: true, Transport: s3.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	be := &s3Backend{client: client, bucket: "test-bucket"}
	globalBackend = be
	done := make(chan struct{})
	app := &App{}
	listener, err := net.Listen("tcp", "127.0.0.1:19182")
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__stop":
			close(done)
			return
		case "/__scenario":
			be.dashboardMu.Lock()
			be.corsMu.Lock()
			mu.Lock()
			scenario = r.URL.Query().Get("name")
			config = cors.Config{}
			sessions = map[string]*session{}
			received = 0
			apiBytes = 0
			peak = 0
			writes = 0
			eventsBefore = len(rec.EventsByTopic("file.added"))
			be.corsOrigin = ""
			be.dashboardUntil = time.Time{}
			platform.mu.Lock()
			platform.calls = nil
			platform.err = nil
			if scenario == "csp-unapproved" {
				platform.err = errors.New("HTTP 403 permission not approved")
			}
			platform.mu.Unlock()
			mu.Unlock()
			be.corsMu.Unlock()
			be.dashboardMu.Unlock()
			if scenario != "csp-stale" {
				_ = reconcileDashboardUploadOrigin(context.Background(), ctx)
			}
			httpJSON(w, map[string]any{"ok": true})
			return
		case "/__csp":
			origins := []string{}
			platform.mu.Lock()
			if platform.err == nil && len(platform.calls) > 0 {
				origins = platform.calls[len(platform.calls)-1].Origins
			}
			platform.mu.Unlock()
			httpJSON(w, map[string]any{"policy": "connect-src 'self' ws: wss: " + strings.Join(origins, " ")})
			return
		case "/__stats":
			mu.Lock()
			defer mu.Unlock()
			parts := 0
			for _, s := range sessions {
				parts += len(s.parts)
			}
			var scratch int64
			_ = filepath.Walk(uploadsDir(ctx), func(_ string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					scratch += info.Size()
				}
				return nil
			})
			httpJSON(w, map[string]any{"bytes": received, "apiBytes": apiBytes, "parts": parts, "peak": peak, "corsWrites": writes, "events": len(rec.EventsByTopic("file.added")) - eventsBefore, "scratchBytes": scratch, "origin": s3.URL})
			return
		}
		r.Header.Set("X-User-ID", "1")
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/storage")
		if r.Method == "PUT" {
			mu.Lock()
			apiBytes += r.ContentLength
			mu.Unlock()
		}
		if r.URL.Path == "/uploads" {
			app.handleUploadsCollection(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/uploads/") {
			app.handleUploadsItem(w, r)
			return
		}
		httpJSON(w, map[string]any{"files": []any{}, "folders": []any{}})
	})}
	go func() { _ = api.Serve(listener) }()
	defer api.Close()
	t.Log("local multipart browser fixture ready on 127.0.0.1:19182")
	<-done
}
