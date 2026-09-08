package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBodyDeadlineClearsOnLengthAndChunkedEOF(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(map[bool]string{true: "chunked", false: "content-length"}[chunked], func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w, finish := boundGatewayBody(w, r, 30*time.Millisecond)
				defer finish()
				if r.Method == "POST" {
					if chunked {
						if _, err := io.ReadAll(r.Body); err != nil {
							t.Error(err)
						}
					} else {
						if _, err := io.ReadFull(r.Body, make([]byte, 4)); err != nil {
							t.Error(err)
						}
					}
					time.Sleep(80 * time.Millisecond)
				}
				if r.Context().Err() != nil {
					t.Error("body deadline survived completed upload")
				}
				io.WriteString(w, "ok")
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					count.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			var body io.Reader = strings.NewReader("body")
			if chunked {
				body = io.NopCloser(body)
			}
			req, _ := http.NewRequest("POST", server.URL, body)
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			resp, err = server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if count.Load() != 1 {
				t.Fatalf("completed upload unnecessarily closed connection: %d", count.Load())
			}
		})
	}
}
func TestSlowUploadDeadlineClosesConnection(t *testing.T) {
	failed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w, finish := boundGatewayBody(w, r, 30*time.Millisecond)
		defer finish()
		_, err := io.ReadAll(r.Body)
		failed <- err
		http.Error(w, "request body timed out", 408)
	}))
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 100000\r\n\r\nx")
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("stalled upload accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("slow upload was not interrupted")
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 408 || !response.Close {
		t.Fatalf("timed-out connection reusable: status=%d close=%v", response.StatusCode, response.Close)
	}
}
func TestGatewayRejectsOversizedUploadBeforeAuth(t *testing.T) {
	app, _ := mountTestApp(t)
	r := httptest.NewRequest("POST", "/gw/none/", strings.NewReader("x"))
	r.ContentLength = maxRequestBytes + 1
	response := httptest.NewRecorder()
	app.handleGateway(response, r)
	if response.Code != 413 {
		t.Fatalf("oversized upload: %d", response.Code)
	}
}
