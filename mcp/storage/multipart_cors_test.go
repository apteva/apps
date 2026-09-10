package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/cors"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestAutomaticBucketCors(t *testing.T) {
	for _, scenario := range []string{"merge", "already-allowed", "denied", "missing", "limit"} {
		t.Run(scenario, func(t *testing.T) {
			origin := "https://dashboard.example"
			old := cors.Rule{ID: "other-app", AllowedOrigin: []string{"https://other.example"}, AllowedMethod: []string{"GET"}, ExposeHeader: []string{"ETag"}}
			config := cors.Config{CORSRules: []cors.Rule{old}}
			if scenario == "limit" {
				for len(config.CORSRules) < 100 {
					config.CORSRules = append(config.CORSRules, old)
				}
			}
			reads, writes, probes := 0, 0, 0
			allowed := scenario == "already-allowed"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "OPTIONS" {
					probes++
					if r.URL.RawQuery != "" || r.Header.Get("Origin") != origin || r.Header.Get("Access-Control-Request-Method") != "PUT" {
						t.Error("invalid preflight")
					}
					if !allowed {
						w.WriteHeader(403)
						return
					}
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "PUT")
					w.Header().Set("Access-Control-Allow-Headers", "content-type")
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				if !r.URL.Query().Has("cors") {
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(400)
					return
				}
				if r.Method == "GET" {
					reads++
					if scenario == "denied" {
						w.WriteHeader(403)
						fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
						return
					}
					if scenario == "missing" {
						w.WriteHeader(404)
						fmt.Fprint(w, `<Error><Code>NoSuchCORSConfiguration</Code></Error>`)
						return
					}
					_ = xml.NewEncoder(w).Encode(config)
					return
				}
				if r.Method == "PUT" {
					writes++
					var next cors.Config
					if err := xml.NewDecoder(r.Body).Decode(&next); err != nil {
						t.Error(err)
					}
					if scenario != "missing" && !reflect.DeepEqual(next.CORSRules[0], old) {
						t.Errorf("existing rule changed: %+v", next)
					}
					last := next.CORSRules[len(next.CORSRules)-1]
					if !reflect.DeepEqual(last.AllowedOrigin, []string{origin}) || !reflect.DeepEqual(last.AllowedMethod, []string{"PUT"}) {
						t.Error("overbroad CORS rule")
					}
					config = next
					allowed = true
					w.WriteHeader(200)
					return
				}
				t.Error("unexpected method")
				w.WriteHeader(400)
			}))
			defer server.Close()
			client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("key", "secret", ""), Region: "us-east-1"})
			if err != nil {
				t.Fatal(err)
			}
			be := &s3Backend{client: client, bucket: "test-bucket"}
			err = be.PrepareBrowserUpload(context.Background(), origin)
			wantFailure := scenario == "denied" || scenario == "limit"
			if (err != nil) != wantFailure {
				t.Fatalf("unexpected preparation error: %v", err)
			}
			firstReads, firstWrites, firstProbes := reads, writes, probes
			_ = be.PrepareBrowserUpload(context.Background(), origin)
			if reads != firstReads || writes != firstWrites || probes != firstProbes {
				t.Fatal("cache repeated setup")
			}
			be.corsUntil = time.Time{}
			_ = be.PrepareBrowserUpload(context.Background(), origin)
			if writes != firstWrites {
				t.Fatal("repeated CORS write")
			}
			if scenario == "already-allowed" && (reads != 0 || writes != 0) {
				t.Fatal("requested unnecessary CORS permissions")
			}
			if (scenario == "merge" || scenario == "missing") && writes != 1 {
				t.Fatal("CORS not configured")
			}
		})
	}
}
func TestUploadOriginOnlyTrustsConfiguredPlatform(t *testing.T) {
	ctx, _, be, _ := directFixture(t)
	for _, origin := range []string{"https://attacker.example", "null", "https://dashboard.example.evil"} {
		if prepareBrowserUpload(context.Background(), ctx, origin) {
			t.Fatalf("trusted %s", origin)
		}
	}
	if !prepareBrowserUpload(context.Background(), ctx, "https://dashboard.example") {
		t.Fatal("configured origin rejected")
	}
	be.corsError = errors.New("AccessDenied")
	if prepareBrowserUpload(context.Background(), ctx, "https://dashboard.example") {
		t.Fatal("ignored CORS failure")
	}
	for _, raw := range []string{"null", "//example.com", "https://user:pass@example.com", "file:///tmp/a"} {
		if uploadOrigin(raw) != "" {
			t.Fatal(raw)
		}
	}
	if uploadOrigin("https://Dashboard.example/base") != "https://dashboard.example" {
		t.Fatal("origin normalization")
	}
}

func TestCorsProbeHonorsCallerDeadline(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("key", "secret", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	be := &s3Backend{client: client, bucket: "test-bucket"}
	c, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := be.PrepareBrowserUpload(c, "https://dashboard.example"); err == nil {
		t.Fatal("ignored timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("setup blocked upload")
	}
	if be.corsOrigin != "" {
		t.Fatal("cached caller cancellation")
	}
	if calls.Load() == 0 {
		t.Fatal("no actual probe")
	}
}
