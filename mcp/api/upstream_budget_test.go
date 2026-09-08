package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Runs with real elapsed time to catch transport timers that scaled tests miss.
func TestUpstreamHeaderBudgetRealDurations(t *testing.T) {
	if os.Getenv("API_LONG_UPSTREAM") != "1" {
		t.Skip("set API_LONG_UPSTREAM=1 for 5/12/25 second validation")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		var seconds int
		fmt.Sscanf(r.URL.Path, "/api/apps/callback/apps/functions/proxy/fn/delay-%d", &seconds)
		select {
		case <-time.After(time.Duration(seconds) * time.Second):
			fmt.Fprint(w, `{"ok":true}`)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	t.Setenv("APTEVA_GATEWAY_URL", upstream.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
	app, _ := mountTestApp(t)
	for _, seconds := range []int{5, 12, 25} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			rr := httptest.NewRecorder()
			start := time.Now()
			status, err := app.dispatchRoute(rr, httptest.NewRequest("GET", "/", nil), &API{ProjectID: testProject}, &APIRoute{TargetKind: "function", TargetRef: fmt.Sprintf("delay-%d", seconds), TimeoutMS: 30000}, "/", nil, authContext{})
			t.Logf("upstream=%ds elapsed=%s status=%d error=%v", seconds, time.Since(start), status, err)
			if err != nil || status != 200 || !strings.Contains(rr.Body.String(), `"ok":true`) {
				t.Fatalf("permitted response failed: %d %s %v", status, rr.Body.String(), err)
			}
		})
	}
}

func TestGatewayRouteBudgetAndCancellation(t *testing.T) {
	for _, kind := range []string{"function", "http", "app"} {
		for _, cancelClient := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/client=%t", kind, cancelClient), func(t *testing.T) {
				arrived := make(chan struct{})
				cancelled := make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					close(arrived)
					<-r.Context().Done()
					close(cancelled)
				}))
				defer upstream.Close()
				t.Setenv("APTEVA_GATEWAY_URL", upstream.URL)
				t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
				app, _ := mountTestApp(t)
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				parent = context.WithValue(parent, gatewayRequestIDKey{}, "correlation-test")
				if cancelClient {
					go func() { <-arrived; cancel() }()
				}
				rr := httptest.NewRecorder()
				route := &APIRoute{TargetKind: kind, TargetRef: upstream.URL, TimeoutMS: 100, ProjectID: testProject}
				if kind != "http" {
					route.TargetRef = "fn"
				}
				status, err := app.dispatchRoute(rr, httptest.NewRequest("GET", "/", nil).WithContext(parent), &API{ProjectID: testProject}, route, "/", nil, authContext{})
				expected := "gateway_timeout"
				expectedStatus := 504
				if cancelClient {
					expected = "client_cancelled"
					expectedStatus = 499
				}
				var failure *gatewayFailure
				if !errors.As(err, &failure) || failure.code != expected || status != expectedStatus {
					t.Fatalf("status=%d err=%v", status, err)
				}
				if cancelClient {
					if rr.Body.Len() != 0 {
						t.Fatal("wrote response to disconnected client")
					}
				} else {
					var body map[string]any
					json.Unmarshal(rr.Body.Bytes(), &body)
					if body["error_code"] != expected || body["request_id"] != "correlation-test" {
						t.Fatal(body)
					}
				}
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("upstream not cancelled")
				}
			})
		}
	}
}

func TestFunctionDeadlineResponses(t *testing.T) {
	for _, code := range []string{"queue_timeout", "invocation_timeout", "app_call_timeout"} {
		t.Run(code, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(504)
				fmt.Fprintf(w, `{"error_code":%q,"error":"private internals"}`, code)
			}))
			defer up.Close()
			t.Setenv("APTEVA_GATEWAY_URL", up.URL)
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
			app, _ := mountTestApp(t)
			rr := httptest.NewRecorder()
			status, err := app.dispatchRoute(rr, httptest.NewRequest("GET", "/", nil), &API{ProjectID: testProject}, &APIRoute{TargetKind: "function", TargetRef: "fn", TimeoutMS: 1000}, "/", nil, authContext{})
			expected := code
			if code != "queue_timeout" {
				expected = "upstream_timeout"
			}
			if status != 504 || err == nil || !strings.Contains(rr.Body.String(), expected) || strings.Contains(rr.Body.String(), "private internals") {
				t.Fatalf("%d %s %v", status, rr.Body.String(), err)
			}
		})
	}
}

func TestProxyUpstreamTimeoutIsNotGatewayTimeout(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(504)
		io.WriteString(w, "upstream deadline")
	}))
	defer up.Close()
	app, _ := mountTestApp(t)
	rr := httptest.NewRecorder()
	status, err := app.dispatchRoute(rr, httptest.NewRequest("GET", "/", nil), nil, &APIRoute{TargetKind: "http", TargetRef: up.URL, TimeoutMS: 1000}, "/", nil, authContext{})
	var failure *gatewayFailure
	if status != 504 || !errors.As(err, &failure) || failure.code != "upstream_timeout" {
		t.Fatalf("%d %v", status, err)
	}
}

func TestGatewayQueueAndExecutionShareBudget(t *testing.T) {
	// Neither stage alone exhausts the route budget; together they do. There
	// must be no timer reset when an upstream advances from queue to execution.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 2; i++ {
			select {
			case <-time.After(75 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	app, _ := mountTestApp(t)
	rr := httptest.NewRecorder()
	status, err := app.dispatchRoute(rr, httptest.NewRequest("GET", "/", nil), nil, &APIRoute{TargetKind: "http", TargetRef: upstream.URL, TimeoutMS: 100}, "/", nil, authContext{})
	if status != 504 || err == nil || !strings.Contains(rr.Body.String(), "gateway_timeout") {
		t.Fatalf("%d %v", status, err)
	}
	rr = httptest.NewRecorder()
	status, err = app.dispatchRoute(rr, httptest.NewRequest("GET", "/", nil), nil, &APIRoute{TargetKind: "http", TargetRef: upstream.URL, TimeoutMS: 300}, "/", nil, authContext{})
	if status != 200 || err != nil {
		t.Fatalf("permitted queue+execution failed: %d %v", status, err)
	}
}
