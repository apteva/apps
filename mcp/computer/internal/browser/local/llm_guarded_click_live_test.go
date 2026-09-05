package local

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

type llmBrowserDecision struct {
	Action       string `json:"action"`
	Label        int    `json:"label"`
	ExpectedText string `json:"expected_text"`
	Reason       string `json:"reason"`
}

// TestLLMGuardedPatreonShapeLive is an opt-in end-to-end model regression. A
// real model receives the exact annotated frame and structured SoM that an
// agent sees. It must avoid inventing Schedule while Publish is loading, then
// successfully use an ordinary label after the page becomes stable.
func TestLLMGuardedPatreonShapeLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}
	var published atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/published" {
			published.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><style>body{font-family:system-ui;margin:0}.bar{height:72px;display:flex;justify-content:flex-end;align-items:center;padding:0 32px;background:#111}button{width:150px;height:42px}.spinner{display:inline-block;width:18px;height:18px;border:3px solid #bbb;border-top-color:#111;border-radius:50%}</style>
<div class="bar"><button id="publish">Publish</button></div><main style="padding:32px"><h1>Create a post</h1><label>Title <input id="title" placeholder="Post title"></label><p>Autosaving draft…</p></main>
<script>window.startLoading=()=>{let b=publish;b.disabled=true;b.setAttribute('aria-busy','true');b.setAttribute('data-loading','true');b.innerHTML='<span class="spinner" role="progressbar" aria-label="Saving"></span>'};window.releaseLoading=()=>{let b=publish;b.disabled=false;b.removeAttribute('aria-busy');b.removeAttribute('data-loading');b.textContent='Publish'};publish.onclick=()=>fetch('/published',{method:'POST'});</script>`))
	}))
	defer server.Close()

	c, err := New(computer.DisplaySize{Width: 1000, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	// Seed the last-stable accessible-name cache before the spinner replaces
	// the visible label, matching a real autosave transition.
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`window.startLoading()`, nil)); err != nil {
		t.Fatal(err)
	}
	loadingFrame, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true})
	if err != nil {
		t.Fatal(err)
	}
	decision := askBrowserModel(t, loadingFrame, c.LastSetOfMark(), "You want to schedule this post for later. Choose the single safe next browser action. Never invent a control that is absent. Never activate Publish. When the only relevant control is loading, wait_for_stable.")
	if decision.Action == "click" || decision.Action == "double_click" {
		t.Fatalf("model attempted a click while Publish was loading: %+v", decision)
	}
	if published.Load() != 0 {
		t.Fatalf("loading decision published the post: %d", published.Load())
	}

	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`window.releaseLoading()`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := c.ExecuteAction(computer.Action{Type: "wait_for_stable", QuietMS: 250, TimeoutMS: 3000}); err != nil {
		t.Fatal(err)
	}
	stableFrame, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true})
	if err != nil {
		t.Fatal(err)
	}
	decision = askBrowserModel(t, stableFrame, c.LastSetOfMark(), "Click the ordinary Post title field so text can be entered. Use its current SoM label and exact accessible name. Do not click Publish.")
	if decision.Action != "click" || decision.Label <= 0 {
		t.Fatalf("model did not choose the ordinary title label: %+v", decision)
	}
	if err := c.ExecuteAction(computer.Action{Type: "click", Label: decision.Label, ExpectedText: decision.ExpectedText}); err != nil {
		t.Fatalf("ordinary model-selected guarded click regressed: decision=%+v err=%v", decision, err)
	}
	var focused string
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`document.activeElement&&document.activeElement.id`, &focused)); err != nil {
		t.Fatal(err)
	}
	if focused != "title" {
		t.Fatalf("model click focused %q, want title; decision=%+v", focused, decision)
	}
	if published.Load() != 0 {
		t.Fatalf("ordinary interaction unexpectedly published: %d", published.Load())
	}
}

func askBrowserModel(t *testing.T, frame []byte, targets []computer.SetOfMarkTarget, goal string) llmBrowserDecision {
	t.Helper()
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "frame.jpg")
	schemaPath := filepath.Join(tmp, "schema.json")
	resultPath := filepath.Join(tmp, "result.json")
	if err := os.WriteFile(imagePath, frame, 0o600); err != nil {
		t.Fatal(err)
	}
	schema := `{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["click","wait_for_stable","screenshot"]},"label":{"type":"integer"},"expected_text":{"type":"string"},"reason":{"type":"string"}},"required":["action","label","expected_text","reason"]}`
	if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	targetJSON, _ := json.Marshal(targets)
	prompt := fmt.Sprintf("You are a cautious browser agent. Inspect the attached current frame and this current structured SoM: %s\nGoal: %s\nReturn only the required decision object. A label is valid only if it appears in the current SoM. loading=true means do not click it.", targetJSON, goal)
	model := strings.TrimSpace(os.Getenv("COMPUTER_LLM_TEST_MODEL"))
	if model == "" {
		model = "gpt-5.6-terra"
	}
	t.Logf("LLM regression model: %s", model)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, computerLLMBinary(), "exec", "--ephemeral", "--ignore-rules", "--skip-git-repo-check", "-s", "read-only", "-m", model, "--image", imagePath, "--output-schema", schemaPath, "--output-last-message", resultPath, prompt)
	cmd.Dir = tmp
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("LLM browser decision failed: %v\n%s", err, conciseLLMLog(output))
	}
	var decision llmBrowserDecision
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decision); err != nil {
		t.Fatalf("decode LLM decision %q: %v", raw, err)
	}
	t.Logf("LLM decision: %+v", decision)
	return decision
}

func computerLLMBinary() string {
	if binary := strings.TrimSpace(os.Getenv("COMPUTER_LLM_CODEX_BIN")); binary != "" {
		return binary
	}
	return "codex"
}

func conciseLLMLog(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	var out []string
	for _, line := range lines {
		if len(line) > 1000 {
			line = line[:1000] + " [truncated]"
		}
		out = append(out, line)
	}
	if len(out) > 50 {
		out = out[len(out)-50:]
	}
	return strings.Join(out, "\n")
}
