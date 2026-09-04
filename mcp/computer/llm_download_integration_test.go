package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

type llmDownloadPlan struct {
	Steps []struct {
		Tool           string `json:"tool"`
		Action         string `json:"action,omitempty"`
		SessionID      string `json:"session_id,omitempty"`
		DownloadID     string `json:"download_id,omitempty"`
		BlobSourceStep int    `json:"blob_source_step,omitempty"`
		Arguments      struct {
			Action         string `json:"action,omitempty"`
			SessionID      string `json:"session_id,omitempty"`
			DownloadID     string `json:"download_id,omitempty"`
			BlobSourceStep int    `json:"blob_source_step,omitempty"`
		} `json:"arguments,omitempty"`
	} `json:"steps"`
}

// TestLLMUsesBrowserDownloadLifecycleLive exercises the current agent-visible
// Computer contract with a real model. It guards against treating click success
// as completion, refetching an authenticated URL, or asking Computer to unzip.
func TestLLMUsesBrowserDownloadLifecycleLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}

	downloadTool := findTool(t, (&App{}).MCPTools(), "browser_download")
	computerTool := findTool(t, (&App{}).MCPTools(), "computer_use")
	prompt := `Choose the exact next tool-call sequence for this browser task.

Goal: save a ZIP downloaded from an authenticated procurement portal into Storage. Do not inspect or unzip it.
The browser remains open. A guarded Computer click has just returned:
{"action_dispatched":true,"downloads_started":[{"id":"dl_prod_123","filename":"RM6173-bid-pack.zip","status":"in_progress"}],"session_id":"br_prod_456"}

The download URL used login cookies and a POST body. A successful click does not prove completion. Do not reconstruct or refetch the URL and do not put base64 in the plan. The Storage tool name is exactly "storage" and it accepts the blobref produced after Core handles a binary tool result.

Return exactly three ordered steps. For each step choose a tool and arguments. The final Storage step must identify which earlier step supplies its blobref using blob_source_step (one-based). Do not add list, sleep, HTTP fetch, unzip, or document-analysis steps.

Computer computer_use description:
` + computerTool.Description + `

Computer browser_download description:
` + downloadTool.Description + `

browser_download schema:
` + mustJSON(downloadTool.InputSchema)

	var response struct {
		PlanJSON string `json:"plan_json"`
	}
	callComputerLLM(t, nil, prompt,
		`{"type":"object","additionalProperties":false,"properties":{"plan_json":{"type":"string","description":"A JSON object containing exactly the three ordered tool-call steps."}},"required":["plan_json"]}`,
		&response)
	var plan llmDownloadPlan
	if err := json.Unmarshal([]byte(response.PlanJSON), &plan); err != nil {
		t.Fatalf("decode model plan %q: %v", response.PlanJSON, err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("model returned %d steps: %+v", len(plan.Steps), plan)
	}
	wait, get, store := plan.Steps[0], plan.Steps[1], plan.Steps[2]
	if wait.Action == "" {
		wait.Action, wait.SessionID, wait.DownloadID = wait.Arguments.Action, wait.Arguments.SessionID, wait.Arguments.DownloadID
	}
	if get.Action == "" {
		get.Action, get.SessionID, get.DownloadID = get.Arguments.Action, get.Arguments.SessionID, get.Arguments.DownloadID
	}
	if store.BlobSourceStep == 0 {
		store.BlobSourceStep = store.Arguments.BlobSourceStep
	}
	if wait.Tool != "browser_download" || wait.Action != "wait" || wait.SessionID != "br_prod_456" || wait.DownloadID != "dl_prod_123" {
		t.Fatalf("model did not wait for the browser lifecycle first: %s", response.PlanJSON)
	}
	if get.Tool != "browser_download" || get.Action != "get" || get.SessionID != wait.SessionID || get.DownloadID != wait.DownloadID {
		t.Fatalf("model did not export the same completed download second: %s", response.PlanJSON)
	}
	if store.Tool != "storage" || store.BlobSourceStep != 2 {
		t.Fatalf("model did not pass Core's blobref from get to Storage: %s", response.PlanJSON)
	}

	// Run the model-selected Computer calls through the real handlers. This
	// proves its plan conforms to Computer rather than only matching prose.
	payload := []byte("PK\x03\x04authenticated POST fixture")
	now := time.Now().UTC()
	meta := backends.Download{ID: wait.DownloadID, Filename: "RM6173-bid-pack.zip", MIMEType: "application/zip", Size: int64(len(payload)), Status: backends.DownloadCompleted, CreatedAt: now, CompletedAt: &now}
	comp := &downloadTestComp{fakeComp: &fakeComp{}, downloads: []backends.Download{meta}, payloads: map[string][]byte{meta.ID: payload}}
	owner := sessionOwner{ProjectID: "project-prod", AgentID: 9, ThreadID: "thread-prod"}
	app := downloadApp(wait.SessionID, comp, owner)
	ctx := downloadCaller(owner.ProjectID, owner.ThreadID, owner.AgentID)
	waitArgs := map[string]any{"action": wait.Action, "session_id": wait.SessionID, "download_id": wait.DownloadID}
	waitResult, err := app.toolBrowserDownload(ctx, nil, waitArgs)
	if err != nil || waitResult.(map[string]any)["terminal"] != true {
		t.Fatalf("model wait call failed: result=%#v err=%v", waitResult, err)
	}
	getArgs := map[string]any{"action": get.Action, "session_id": get.SessionID, "download_id": get.DownloadID}
	getResult, err := app.toolBrowserDownload(ctx, nil, getArgs)
	if err != nil || getResult.(map[string]any)["_binary"] != true {
		t.Fatalf("model get call did not produce Core's binary envelope: result=%#v err=%v", getResult, err)
	}
	t.Logf("LLM chose browser-confirmed wait -> browser byte export -> Storage blobref handoff: %s", mustJSON(plan))
}
