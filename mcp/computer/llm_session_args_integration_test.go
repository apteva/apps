package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type llmSessionOpenDecision struct {
	Action            string         `json:"action"`
	URL               string         `json:"url,omitempty"`
	ContextName       string         `json:"context_name"`
	Backend           string         `json:"backend,omitempty"`
	Persist           *bool          `json:"persist,omitempty"`
	AutoCreateContext *bool          `json:"auto_create_context,omitempty"`
	Timeout           *int           `json:"timeout,omitempty"`
	ProxyMode         string         `json:"proxy_mode,omitempty"`
	ProxyProfile      string         `json:"proxy_profile,omitempty"`
	ProxyCountry      string         `json:"proxy_country,omitempty"`
	ProxySticky       string         `json:"proxy_sticky,omitempty"`
	Viewport          map[string]any `json:"viewport,omitempty"`
	Environment       map[string]any `json:"environment,omitempty"`
	PresentationMode  string         `json:"presentation_mode,omitempty"`
}

// TestLLMMinimalBrowserSessionArgumentsLive verifies the agent-facing tool
// contract with a real model. A routine saved-login audit must not cause the
// model to synthesize proxy, environment, viewport, or lifecycle defaults.
func TestLLMMinimalBrowserSessionArgumentsLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}

	tool := findTool(t, (&App{}).MCPTools(), "browser_session")
	prompt := `You are an agent choosing the exact arguments for Computer's browser_session tool.
Task: open the saved browser context named "Monika Login" for a normal read-only Patreon audit. The task does not request a country, proxy, custom browser identity, locale, timezone, geolocation, device emulation, viewport, persistence change, backend override, or presentation demo.
Return only the JSON arguments you would send. Follow the tool instructions and do not invent requirements or populate optional defaults.

Tool description:
` + tool.Description + `

Tool input schema:
` + mustJSON(tool.InputSchema)

	var response struct {
		ArgumentsJSON string `json:"arguments_json"`
	}
	callComputerLLM(t, nil, prompt,
		`{"type":"object","additionalProperties":false,"properties":{"arguments_json":{"type":"string","description":"The exact browser_session arguments as a JSON object, with unused optional fields omitted."}},"required":["arguments_json"]}`,
		&response)
	var decision llmSessionOpenDecision
	if err := json.Unmarshal([]byte(response.ArgumentsJSON), &decision); err != nil {
		t.Fatalf("decode model tool arguments %q: %v", response.ArgumentsJSON, err)
	}

	if decision.Action != "open" || decision.ContextName != "Monika Login" {
		t.Fatalf("model did not select the saved context: %+v", decision)
	}
	if decision.Backend != "" || decision.Persist != nil || decision.AutoCreateContext != nil || decision.Timeout != nil ||
		decision.ProxyMode != "" || decision.ProxyProfile != "" || decision.ProxyCountry != "" || decision.ProxySticky != "" ||
		len(decision.Viewport) != 0 || len(decision.Environment) != 0 || decision.PresentationMode != "" {
		t.Fatalf("model synthesized advanced optional arguments: %+v", decision)
	}
	if decision.URL != "" && !strings.Contains(strings.ToLower(decision.URL), "patreon.com") {
		t.Fatalf("model chose an unrelated URL: %+v", decision)
	}
	t.Logf("LLM chose minimal browser_session arguments: %+v", decision)
}
