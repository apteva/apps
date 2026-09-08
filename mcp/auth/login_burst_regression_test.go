package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestIncidentConcurrentPasswordLogins(t *testing.T) {
	_, clientID := newAuthCtx(t)
	app := &App{}
	signup := callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "burst@example.com", "password": "GoodPassword123", "client_id": clientID})
	if signup.Code != 201 {
		t.Fatalf("signup status: %d", signup.Code)
	}
	server := httptest.NewServer(http.HandlerFunc(app.handleLogin))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{"email": "burst@example.com", "password": "GoodPassword123", "client_id": clientID})
	const count = 12
	start := make(chan struct{})
	results := make(chan int, count)
	var workers sync.WaitGroup
	began := time.Now()
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			resp, err := server.Client().Post(server.URL+"/login", "application/json", bytes.NewReader(payload))
			if err != nil {
				results <- 0
				return
			}
			defer resp.Body.Close()
			var data map[string]any
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data)
			if resp.StatusCode == 200 && data["access_token"] == nil {
				results <- 0
				return
			}
			results <- resp.StatusCode
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	codes := map[int]int{}
	for code := range results {
		codes[code]++
	}
	t.Logf("12 concurrent password logins: statuses=%v elapsed=%s", codes, time.Since(began))
	if codes[200] != count {
		t.Fatalf("valid login burst rejected: %v", codes)
	}
}
