package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type deepSeekTranscript struct {
	OpenedView          map[string]any
	SessionID           string
	Messages            []map[string]any
	Calls               []string
	ToolEvents          []map[string]any
	Closed              bool
	CloseBeforeFinal    bool
	Observations        int
	CompactObservations int
}

func (s *deepSeekTranscript) assertHandoff() error {
	attached := 0
	finalCount := 0
	final := ""
	progress := false
	for _, message := range s.Messages {
		components, _ := message["components"].([]any)
		for _, raw := range components {
			component, _ := raw.(map[string]any)
			if component["app"] != "computer" || component["name"] != "browser-view" {
				continue
			}
			attached++
			if message["phase"] != "progress" {
				return fmt.Errorf("browser view was not handed off during progress")
			}
			if !reflect.DeepEqual(component, s.OpenedView) {
				return fmt.Errorf("view locator/props differ from the returned view")
			}
			progress = true
		}
		if message["phase"] == "final" {
			finalCount++
			if finalCount > 1 {
				return fmt.Errorf("agent emitted more than one final reply")
			}
			final = fmt.Sprint(message["text"])
			if len(components) > 0 {
				return fmt.Errorf("final reply reattached the browser view")
			}
		}
	}
	if attached != 1 || !progress {
		return fmt.Errorf("expected one progress view, got %d", attached)
	}
	if !s.Closed || !s.CloseBeforeFinal {
		return fmt.Errorf("agent did not close the session before its final reply")
	}
	if !strings.Contains(final, "MAPLE-731") {
		return fmt.Errorf("final reply did not report the independently known release code")
	}
	lower := strings.ToLower(final)
	if !strings.Contains(lower, "view") && !strings.Contains(lower, "browser") && !strings.Contains(lower, "above") {
		return fmt.Errorf("final reply did not reference the existing browser view")
	}
	return nil
}

// This is a complete tool-calling conversation with the configured DeepSeek
// route. The harness neither opens/closes the successful session nor injects
// screenshot calls or a scripted action plan. Messaging is an in-memory sink;
// no real Conversations installation or user conversation is modified.
func TestLLMDeepSeekViewHandoffLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_DEEPSEEK_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_DEEPSEEK_TESTS=1 for the real DeepSeek Tier 3 test")
	}
	key := os.Getenv("COMPUTER_DEEPSEEK_API_KEY")
	if key == "" {
		key = os.Getenv("DEEPSEEK_API_KEY")
	}
	if key == "" {
		t.Fatal("DeepSeek test was requested but no runtime API key is configured")
	}
	endpoint := strings.TrimRight(os.Getenv("COMPUTER_DEEPSEEK_BASE_URL"), "/")
	if endpoint == "" {
		endpoint = "https://api.deepseek.com"
	}
	model := os.Getenv("COMPUTER_DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	if !strings.Contains(strings.ToLower(model), "deepseek") {
		t.Fatal("this regression requires a DeepSeek model, not a substitute model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 90 * time.Second}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("read-only fixture received a write")
			http.Error(w, "read only", 405)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Release board</title><h1>Release board</h1><p>Release details are further down the page.</p><label>Private scratch notes<textarea aria-label="Private scratch notes" style="display:block;width:420px;height:100px">`+strings.Repeat("Scratch text; do not edit.\n", 100)+`</textarea></label><div style="height:650px"></div><h2>Current release</h2><label>Release code<input aria-label="Release code" readonly value="MAPLE-731"></label><p>Release verified. End of board.</p>`)
	}))
	defer fixture.Close()
	sidecar := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"), tk.WithEnv("APTEVA_INSTALL_ID", "4242"))
	transcript := &deepSeekTranscript{}
	t.Cleanup(func() {
		if directory := os.Getenv("COMPUTER_LLM_ARTIFACT_DIR"); directory != "" {
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			evidence, _ := json.MarshalIndent(map[string]any{"model": model, "tool_calls": transcript.Calls, "tool_events": transcript.ToolEvents, "messages": transcript.Messages, "view": transcript.OpenedView, "closed": transcript.Closed, "close_before_final": transcript.CloseBeforeFinal, "observations": transcript.Observations, "compact_observations": transcript.CompactObservations}, "", "  ")
			if err := os.WriteFile(filepath.Join(directory, "deepseek-view-handoff.json"), evidence, 0600); err != nil {
				t.Fatal(err)
			}
		}
	})
	// Failure cleanup is a safety net, never evidence that the agent closed.
	defer func() {
		if transcript.SessionID != "" && !transcript.Closed {
			_, _ = sidecar.MCPRaw("tools/call", map[string]any{"name": "browser_session", "arguments": map[string]any{"action": "close", "session_id": transcript.SessionID}})
		}
	}()
	skill, err := os.ReadFile("skills/how-to-use-computer.md")
	if err != nil {
		t.Fatal(err)
	}
	tools := make([]any, 0, 3)
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "browser_session" || tool.Name == "computer_use" {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
		}
	}
	tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": "conversations_send", "description": "Send a progress or final reply to the current conversation. Copy a returned component view into components when showing an app. Use this for user-facing messages.", "parameters": schemaObject(map[string]any{"text": map[string]any{"type": "string"}, "phase": map[string]any{"type": "string", "enum": []string{"progress", "final"}}, "components": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, []string{"text", "phase"})}})
	messages := []map[string]any{{"role": "system", "content": "You are a browser assistant. Local Chromium is available; use backend=local. This route consumes structured observations rather than image pixels; include_som exposes visible controls and values. Use conversations_send for progress and final replies.\n\n" + string(skill)},
		{"role": "user", "content": "Find the current release code near the bottom of the release board at " + fixture.URL + " and report it. Show me the browser while you work. This is a read-only inspection; leave the scratch notes untouched."}}
	// Some subscription gateways require a stable routing session header.
	providerHeaders := map[string]string{}
	if raw := os.Getenv("COMPUTER_DEEPSEEK_HEADERS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &providerHeaders); err != nil {
			t.Fatal("invalid COMPUTER_DEEPSEEK_HEADERS JSON")
		}
	}
	maxTools := 30
	t.Logf("DeepSeek route model=%s; isolated Chromium and in-memory message sink", model)
	for turn := 0; turn < 20; turn++ {
		payload := map[string]any{"model": model, "messages": messages, "tools": tools, "tool_choice": "auto", "temperature": 0, "max_tokens": 4096, "stream": false}
		encoded, _ := json.Marshal(payload)
		request, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/chat/completions", bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "apteva-computer-tier3/0.11.0")
		for name, value := range providerHeaders {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("DeepSeek inference transport failed: %v", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != 200 {
			t.Fatalf("DeepSeek inference HTTP %d: %s", response.StatusCode, string(raw)[:min(len(raw), 900)])
		}
		var completion struct {
			Model   string `json:"model"`
			Choices []struct {
				Message map[string]any `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &completion); err != nil || len(completion.Choices) != 1 {
			t.Fatalf("invalid inference result: %v", err)
		}
		if !strings.Contains(strings.ToLower(completion.Model), "deepseek") {
			t.Fatalf("provider returned a non-DeepSeek model %q", completion.Model)
		}
		message := completion.Choices[0].Message
		// Preserve reasoning_content for providers that require it across tool turns.
		messages = append(messages, message)
		calls, _ := message["tool_calls"].([]any)
		if len(calls) == 0 {
			// A model's ordinary final assistant reply is also a user-facing final
			// turn; capture it rather than requiring an extra messaging tool call.
			if text, ok := message["content"].(string); ok && strings.TrimSpace(text) != "" {
				transcript.Messages = append(transcript.Messages, map[string]any{"phase": "final", "text": text})
				transcript.CloseBeforeFinal = transcript.Closed
			}
			if err := transcript.assertHandoff(); err != nil {
				t.Fatal(err)
			}
			break
		}
		for _, rawCall := range calls {
			if len(transcript.Calls) >= maxTools {
				t.Fatal("DeepSeek exceeded bounded tool budget")
			}
			call := rawCall.(map[string]any)
			function := call["function"].(map[string]any)
			name := fmt.Sprint(function["name"])
			var args map[string]any
			if err := json.Unmarshal([]byte(fmt.Sprint(function["arguments"])), &args); err != nil {
				t.Fatal(err)
			}
			transcript.Calls = append(transcript.Calls, name)
			transcript.ToolEvents = append(transcript.ToolEvents, map[string]any{"tool": name, "arguments": args})
			var result map[string]any
			if name == "conversations_send" {
				transcript.Messages = append(transcript.Messages, args)
				if args["phase"] == "final" {
					transcript.CloseBeforeFinal = transcript.Closed
				}
				result = map[string]any{"sent": true, "message_id": len(transcript.Messages)}
			} else if name == "browser_session" || name == "computer_use" {
				action := fmt.Sprint(args["action"])
				if name == "computer_use" {
					if action == "scroll" {
						shown := false
						for _, message := range transcript.Messages {
							if components, ok := message["components"].([]any); ok && message["phase"] == "progress" {
								for _, component := range components {
									if reflect.DeepEqual(component, transcript.OpenedView) {
										shown = true
									}
								}
							}
						}
						if !shown {
							t.Fatal("agent scrolled before handing off the returned browser view")
						}
					}
					switch action {
					case "screenshot", "scroll", "wait", "wait_for", "wait_for_stable":
					default:
						t.Fatalf("read-only task attempted action %s", action)
					}
				} else if action != "open" && action != "close" && action != "status" {
					t.Fatalf("unexpected session action %s", action)
				}
				if action == "open" {
					if transcript.SessionID != "" {
						t.Fatal("agent opened more than one session")
					}
					if strings.TrimRight(fmt.Sprint(args["url"]), "/") != fixture.URL || (args["backend"] != nil && args["backend"] != "" && args["backend"] != "local") {
						t.Fatalf("agent did not use the isolated local fixture: url=%v backend=%v", args["url"], args["backend"])
					}
				}
				result = callComputerMCP(t, sidecar, name, args)
				if name == "browser_session" && action == "open" {
					if result["backend"] != "local" {
						t.Fatalf("fixture did not use local Chromium: %v", result["error"])
					}
					transcript.SessionID = fmt.Sprint(result["session_id"])
					transcript.OpenedView, _ = result["view"].(map[string]any)
					if transcript.OpenedView == nil || transcript.OpenedView["resource"] == nil {
						t.Fatal("opened view lacks trusted resource locator")
					}
				}
				if name == "browser_session" && action == "close" && result["closed"] == true {
					transcript.Closed = true
				}
				if name == "computer_use" && result["screenshot"] != nil {
					transcript.Observations++
					if args["compact"] == true {
						transcript.CompactObservations++
					}
				}
			} else {
				t.Fatalf("unexpected tool %s", name)
			}
			// Match the application's binary transport boundary: pixels are attachments,
			// never JSON/base64 text tokens. This text-only handoff test uses SoM values.
			modelResult := map[string]any{}
			for k, v := range result {
				if k != "screenshot" && k != "png_b64" {
					modelResult[k] = v
				}
			}
			transcript.ToolEvents[len(transcript.ToolEvents)-1]["result"] = modelResult
			encoded, _ := json.Marshal(modelResult)
			if len(encoded) > 96*1024 {
				t.Fatalf("model observation exceeded 96 KiB: %d", len(encoded))
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call["id"], "content": string(encoded)})
			t.Logf("turn=%d tool=%s action=%v model_result_bytes=%d", turn, name, args["action"], len(encoded))
		}
		if transcript.CloseBeforeFinal {
			break
		}
	}
	if err := transcript.assertHandoff(); err != nil {
		t.Fatal(err)
	}
	if transcript.Observations > 5 {
		t.Fatal("agent repeated screenshots beyond the fixture observation budget")
	}
	if transcript.Observations < 1 || transcript.CompactObservations < 1 {
		t.Fatal("agent did not exercise compact browser observations")
	}
	// Independent browser lifecycle checks; harness cleanup cannot satisfy these.
	status := callComputerMCP(t, sidecar, "browser_session", map[string]any{"action": "status", "session_id": transcript.SessionID})
	if status["status"] == "active" || !reflect.DeepEqual(status["view"], transcript.OpenedView) {
		t.Fatal("session still active or resource locator changed after closure")
	}
	req, _ := http.NewRequest("GET", sidecar.URL()+"/sessions/"+transcript.SessionID+"/screenshot", nil)
	req.Header.Set("Authorization", "Bearer "+sidecar.Token())
	frame, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer frame.Body.Close()
	if frame.StatusCode != 200 || !strings.HasPrefix(frame.Header.Get("Content-Type"), "image/") {
		t.Fatal("closed view did not retain its final frame")
	}
	t.Logf("DeepSeek handoff verified: one progress view, reused locator, agent closure, saved final frame; %d calls", len(transcript.Calls))
}

func callComputerMCP(t *testing.T, sidecar *tk.Sidecar, name string, args map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req, _ := http.NewRequest("POST", sidecar.URL()+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sidecar.Token())
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var wire struct {
		Result struct {
			Structured map[string]any `json:"structuredContent"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	if wire.Error != nil {
		return map[string]any{"failed": true, "error": wire.Error.Message}
	}
	if wire.Result.Structured != nil {
		return wire.Result.Structured
	}
	if len(wire.Result.Content) == 0 {
		t.Fatal("missing tool result")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(wire.Result.Content[0].Text), &result); err != nil {
		return map[string]any{"failed": true, "error": wire.Result.Content[0].Text}
	}
	return result
}
