//go:build scenario

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// No user prompt or scenario directive mentions suggestions or supplies their
// wording/schema. Only the shipped Conversations guidance teaches this feature.
// Acceptance must produce working code, not just a plausible-sounding prompt.
func TestScenario_CodingSuggestedReply(t *testing.T) {
	c := newScenarioClient(t)
	agent, _ := c.ensureAgent()
	if c.installs["code"] == 0 {
		t.Fatal("scenario must provision Code")
	}
	const slug = "shipping-boundary"
	repoPath := "/api/apps/code/api/repos/" + slug
	if status := c.do("POST", "/api/apps/code/api/repos", map[string]any{"name": "Shipping boundary", "slug": slug, "framework": "blank"}, nil); status != 200 {
		t.Fatalf("seed repository: %d", status)
	}
	defer c.do("DELETE", repoPath+"?force=1", nil, nil)
	file := func(method, path, content string) string {
		t.Helper()
		q := url.Values{"project_id": {os.Getenv("APTEVA_TEST_PROJECT_ID")}, "install_id": {fmt.Sprint(c.installs["code"])}}
		req, err := http.NewRequest(method, c.base+repoPath+"/files/"+path+"?"+q.Encode(), strings.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s %s: status=%d error=%v", method, path, resp.StatusCode, err)
		}
		return string(body)
	}
	const buggy = "export function shippingCents(subtotalCents) {\n  return subtotalCents > 5000 ? 0 : 499;\n}\n"
	const existingTests = `import { expect, test } from "bun:test";
import { shippingCents } from "./shipping.js";
test("below threshold costs 499 cents", () => expect(shippingCents(4999)).toBe(499));
test("above threshold is free", () => expect(shippingCents(5001)).toBe(0));
`
	for path, content := range map[string]string{
		"README.md":        "Shipping policy: orders of $50.00 or more have free shipping. Smaller orders cost $4.99. All amounts passed to shippingCents are integer cents. Run checks with bun test.\n",
		"shipping.js":      buggy,
		"shipping.test.js": existingTests,
	} {
		file("PUT", path, content)
	}
	snapshot := func() map[string]string {
		t.Helper()
		var tree struct {
			Files []struct {
				Path string `json:"path"`
				Dir  bool   `json:"is_dir"`
			} `json:"files"`
		}
		if code := c.do("GET", repoPath+"/tree", nil, &tree); code != 200 {
			t.Fatalf("tree: %d", code)
		}
		out := map[string]string{}
		for _, entry := range tree.Files {
			if !entry.Dir {
				out[entry.Path] = file("GET", entry.Path, "")
			}
		}
		return out
	}
	seed := snapshot()
	// Establish the fixture before asking the model: existing tests pass but an
	// independent boundary check fails. Reuse these same checks after acceptance.
	run := func(files map[string]string, independent bool) (string, error) {
		t.Helper()
		dir := t.TempDir()
		for path, content := range files {
			clean := filepath.Clean(path)
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				t.Fatalf("unsafe repository path: %q", path)
			}
			target := filepath.Join(dir, clean)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if independent {
			const check = `import { expect, test } from "bun:test";
import { shippingCents } from "./shipping.js";
test("independent shipping policy", () => {
  for (const amount of [0, 1, 2499, 4999]) expect(shippingCents(amount)).toBe(499);
  for (const amount of [5000, 5001, 7500, 100000]) expect(shippingCents(amount)).toBe(0);
});
`
			if err := os.WriteFile(filepath.Join(dir, "independent.test.js"), []byte(check), 0600); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bun", "test")
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("Bun checks timed out: %s", output)
		}
		return string(output), err
	}
	if output, err := run(seed, false); err != nil {
		t.Fatalf("fixture's existing tests must pass: %s", output)
	}
	if output, err := run(seed, true); err == nil || !strings.Contains(output, "Expected: 0") {
		t.Fatalf("fixture must fail at the free-shipping boundary: %s", output)
	}
	t.Log("fixture verified: existing tests pass; independent exact-threshold check fails")

	t.Log("waiting for newly provisioned agent to enter app telemetry ownership")
	time.Sleep(65 * time.Second)
	var conv Conversation
	if code := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agent, "title": "Investigate shipping charge"}, &conv); code != 200 || conv.ID == "" {
		t.Fatalf("create conversation: %d", code)
	}
	defer c.deleteConversation(conv.ID)
	path := "/api/apps/conversations/messages?chat_id=" + conv.ID
	rows := func() []Message {
		var out []Message
		if code := c.do("GET", path, nil, &out); code != 200 {
			t.Fatalf("history: %d", code)
		}
		return out
	}
	send := func(content, key string) Message {
		var out Message
		if code := c.do("POST", path, map[string]any{"content": content, "client_message_id": key}, &out); code != 200 {
			t.Fatalf("send: %d", code)
		}
		return out
	}
	waitFinal := func(after int64) Message {
		t.Helper()
		deadline := time.Now().Add(180 * time.Second)
		for time.Now().Before(deadline) {
			for _, message := range rows() {
				if message.ID > after && message.Role == "agent" && message.Phase == "final" {
					t.Logf("model final: %s", message.Content)
					return message
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatal("no final reply within 180s")
		return Message{}
	}
	suggestion := func(message Message) string {
		t.Helper()
		var text string
		for _, component := range message.Components {
			if component.App == appName && component.Name == replySuggestionComponent {
				items, err := genericObjectsArg(component.Props, "items", 1)
				if err != nil || len(items) != 1 || text != "" || component.Interaction != nil {
					t.Fatalf("invalid single suggestion: %+v", message.Components)
				}
				text, _ = items[0]["text"].(string)
			}
		}
		return text
	}

	greeting := send("Hi!", "coding-greeting")
	if reply := waitFinal(greeting.ID); suggestion(reply) != "" {
		t.Fatal("ordinary greeting should not generate a suggested task")
	}
	request := send("Customers say an order of exactly $50 is still charged $4.99 for shipping, although our policy says $50 or more ships free. Please investigate the shipping-boundary repository, read the implementation and tests, and explain the cause and the coverage gap. Don't change any files yet.", "coding-investigate")
	diagnosis := waitFinal(request.ID)
	text := suggestion(diagnosis)
	t.Logf("independently generated suggestion: %q", text)
	if text == "" {
		t.Fatal("natural coding investigation produced no suggested next message")
	}
	// A small explicit rubric, not an exact wording match: actionable code work,
	// relevant to this defect, with regression coverage. Actual execution below
	// is the stronger usefulness check.
	hasAny := func(text string, stems ...string) bool {
		for _, stem := range stems {
			if strings.Contains(strings.ToLower(text), stem) {
				return true
			}
		}
		return false
	}
	if !hasAny(text, "fix", "implement", "correct", "patch", "change", "update") ||
		!hasAny(text, "ship", "threshold", "boundar", "$50", "5000") ||
		!hasAny(text, "test", "regression", "coverage") {
		t.Fatalf("suggestion must be a relevant fix with regression coverage: %q", text)
	}
	if !hasAny(diagnosis.Content, "5000", "$50", "50.00") || !hasAny(diagnosis.Content, "test", "coverage") {
		t.Fatalf("diagnosis did not identify the boundary and coverage gap: %q", diagnosis.Content)
	}
	time.Sleep(8 * time.Second)
	if !reflect.DeepEqual(seed, snapshot()) {
		t.Fatal("investigation or unaccepted suggestion changed repository files")
	}
	for _, message := range rows() {
		if message.ID > diagnosis.ID {
			t.Fatalf("suggestion auto-continued before user input: %+v", message)
		}
	}
	// Persisted metadata must survive a fresh history read, unchanged.
	var restored string
	for _, message := range rows() {
		if message.ID == diagnosis.ID {
			restored = suggestion(message)
		}
	}
	if restored != text {
		t.Fatal("natural suggestion lost on durable reload")
	}
	accepted := send(text, "coding-accept") // Exact generated text; no extra instructions.
	waitFinal(accepted.ID)
	fixed := snapshot()
	if fixed["shipping.js"] == buggy {
		t.Fatal("accepted suggestion did not change the implementation")
	}
	if output, err := run(fixed, true); err != nil {
		t.Fatalf("accepted suggestion failed independent policy checks: %s", output)
	} else {
		t.Logf("independent verification of retrieved repository:\n%s", output)
	}
	// Reintroducing the original bug must fail the model's own regression suite,
	// without our independent checks, proving the added tests catch this defect.
	mutant := map[string]string{}
	for path, content := range fixed {
		mutant[path] = content
	}
	mutant["shipping.js"] = buggy
	if output, err := run(mutant, false); err == nil || !strings.Contains(output, "Expected: 0") {
		t.Fatalf("agent regression tests did not detect the original bug: %s", output)
	} else {
		t.Logf("agent regression suite detects original bug:\n%s", output)
	}
	end := send("Thanks, that's all for now.", "coding-close")
	if reply := waitFinal(end.ID); suggestion(reply) != "" {
		t.Fatal("routine closure should not generate more suggested work")
	}
	t.Log("natural coding task generated one relevant next step; no work before explicit acceptance; accepted exact suggestion produced a verified fix and a regression test that catches the original bug; greeting and closure omitted suggestions")
}
