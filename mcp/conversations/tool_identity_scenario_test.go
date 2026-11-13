//go:build scenario

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keep Core and the real model unchanged. A loopback fault-injection proxy at
// the runner-owned sidecar boundary reproduces a provider reusing call_0, then
// retries each write with exactly the same trusted headers and body.
func TestScenario_ReusedToolCallIdentity(t *testing.T) {
	c := newScenarioClient(t)
	agent, _ := c.ensureAgent()
	profiles := c.ownershipProfiles(agent)
	match := regexp.MustCompile(`CONVERSATIONS_TEST_UPSTREAM=(http://127\.0\.0\.1:[0-9]+)`).FindStringSubmatch(profiles["main"].Directive)
	if len(match) != 2 {
		t.Fatal("runner-owned loopback sidecar URL missing from fixture directive")
	}
	upstream := match[1]
	var mu sync.Mutex
	writes, retries := 0, 0
	var failures []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &rpc)
		write := r.URL.Path == "/mcp" && rpc.Method == "tools/call" &&
			(rpc.Params.Name == "send" || rpc.Params.Name == "report" || rpc.Params.Name == "alert" || rpc.Params.Name == "request_approval")
		// Keep the next user turn from starting until this invocation and its
		// injected transport retry have completed. The SDK has no execution ID
		// to distinguish a late retry from an identical call after new input.
		if write {
			mu.Lock()
			defer mu.Unlock()
		}
		forward := func() (int, http.Header, []byte, error) {
			req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), bytes.NewReader(body))
			if err != nil {
				return 0, nil, nil, err
			}
			req.Header = r.Header.Clone()
			if write {
				req.Header.Set("X-Apteva-Tool-Call-ID", "call_0")
			}
			resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
			if err != nil {
				return 0, nil, nil, err
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			return resp.StatusCode, resp.Header, raw, err
		}
		code, headers, raw, err := forward()
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		if write {
			_, _, retry, retryErr := forward()
			firstID, firstInserted, firstErr := identityReceipt(raw)
			retryID, retryInserted, secondErr := identityReceipt(retry)
			writes++
			if retryErr != nil || firstErr != nil || secondErr != nil || firstID == 0 || retryID != firstID || retryInserted {
				failures = append(failures, fmt.Sprintf("%s: first=%d/%v/%v retry=%d/%v/%v transport=%v", rpc.Params.Name, firstID, firstInserted, firstErr, retryID, retryInserted, secondErr, retryErr))
			} else {
				retries++
			}
		}
		for k, values := range headers {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(code)
		_, _ = w.Write(raw)
	}))
	defer proxy.Close()
	setUpstream := func(endpoint string) {
		t.Helper()
		if code := c.do("PUT", fmt.Sprintf("/api/apps/installs/%d/status", c.installs["conversations"]), map[string]any{"status": "running", "sidecar_url": endpoint}, nil); code != 200 {
			t.Fatalf("set runner-owned test transport: %d", code)
		}
	}
	setUpstream(proxy.URL)
	defer setUpstream(upstream)
	var conv Conversation
	if code := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agent, "title": "Reused call identity"}, &conv); code != 200 || conv.ID == "" {
		t.Fatalf("create: %d", code)
	}
	defer c.deleteConversation(conv.ID)
	send := func(text, key string) int64 {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		var message Message
		if code := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{"content": text, "client_message_id": key}, &message); code != 200 {
			t.Fatalf("send: %d", code)
		}
		return message.ID
	}
	wait := func(after int64, accept func(Message) bool) Message {
		t.Helper()
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			var messages []Message
			if c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &messages) != 200 {
				t.Fatal("history unavailable")
			}
			for _, m := range messages {
				if m.ID > after && m.Role == "agent" && accept(m) {
					return m
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatal("reply/card was not delivered")
		return Message{}
	}
	// The same visible reply must be saved again for a fresh user turn.
	for turn := 0; turn < 2; turn++ {
		after := send("Reply with exactly IDENTITY_OK and nothing else.", fmt.Sprintf("same-reply-%d", turn))
		wait(after, func(m Message) bool { return m.Phase == "final" && strings.TrimSpace(m.Content) == "IDENTITY_OK" })
	}
	after := send("Using these supplied facts only: all five preview checks passed. Put a concise report in my Inbox with the title Preview checks and that result.", "report")
	report := wait(after, func(m Message) bool { return m.ComponentKind == kindReport })
	after = send("Using this supplied fact only: one preview source is unavailable. Raise a warning alert in my Inbox. Do not investigate.", "alert")
	alert := wait(after, func(m Message) bool { return m.ComponentKind == kindAlert })
	after = send("Request my approval in the Inbox before deleting the obsolete preview. Do not delete anything or wait for a verdict during this test.", "approval")
	approval := wait(after, func(m Message) bool { return m.ComponentKind == kindApproval })
	// Reloaded history must contain the distinct cards and exactly two replies.
	var history []Message
	c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &history)
	seen, finals := map[int64]bool{}, 0
	for _, m := range history {
		seen[m.ID] = true
		if m.Role == "agent" && strings.TrimSpace(m.Content) == "IDENTITY_OK" {
			finals++
		}
	}
	if finals != 2 || !seen[report.ID] || !seen[alert.ID] || !seen[approval.ID] {
		t.Fatalf("durable history lost or duplicated replies/cards: finals=%d", finals)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes < 5 || retries != writes || len(failures) > 0 {
		t.Fatalf("writes=%d retries=%d failures=%v", writes, retries, failures)
	}
	t.Logf("real model delivered both identical-turn replies and all three Inbox cards with call_0; %d/%d transport retries suppressed", retries, writes)
}

func identityReceipt(raw []byte) (int64, bool, error) {
	var rpc struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return 0, false, err
	}
	if len(rpc.Error) > 0 || rpc.Result.IsError {
		return 0, false, fmt.Errorf("MCP error: %s", raw)
	}
	for _, content := range rpc.Result.Content {
		var receipt struct {
			ID       int64 `json:"message_id"`
			Inserted bool  `json:"inserted"`
		}
		if json.Unmarshal([]byte(content.Text), &receipt) == nil && receipt.ID > 0 {
			return receipt.ID, receipt.Inserted, nil
		}
	}
	return 0, false, fmt.Errorf("no message receipt: %s", raw)
}
