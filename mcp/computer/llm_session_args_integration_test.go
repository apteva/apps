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
	if _, err := exec.LookPath(computerLLMBinary()); err != nil {
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

// TestLLMSessionLifetimeIntentLive proves with a real model that estimated task
// duration does not become a destructive provider-session lifetime. The same
// model must still honor a user's explicit request for a one-minute disposable
// cloud browser, which is why Computer retains timeout=60 as a valid API value.
func TestLLMSessionLifetimeIntentLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath(computerLLMBinary()); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}

	tool := findTool(t, (&App{}).MCPTools(), "browser_session")
	tests := []struct {
		name        string
		task        string
		wantTimeout *int
	}{
		{
			name: "short task omits session lifetime",
			task: `Open the saved context "Alexa Patreon" at https://www.patreon.com/c/alexaentranced for a quick read-only audit expected to take about one minute. The page may need a 10-second action wait after opening. The user did not request a browser-session lifetime or provider shutdown deadline.`,
		},
		{
			name:        "explicit provider lifetime is honored",
			task:        `Open the saved context "Alexa Patreon" at https://www.patreon.com/c/alexaentranced. The user explicitly requires this disposable cloud browser session itself to be terminated by the provider after exactly 60 wall-clock seconds, including while active.`,
			wantTimeout: intPointer(60),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := `You are an agent choosing the exact arguments for Computer's browser_session tool.
Return only the JSON arguments for the single open call. Omit every optional argument not authorized by the task. Distinguish the estimated duration of work and short action waits from the cloud provider's maximum browser-session lifetime.

Task:
` + tt.task + `

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
			if decision.Action != "open" || decision.ContextName != "Alexa Patreon" {
				t.Fatalf("model did not select the saved context: %+v", decision)
			}
			if tt.wantTimeout == nil {
				if decision.Timeout != nil {
					t.Fatalf("model converted estimated task duration into session timeout: %+v", decision)
				}
			} else if decision.Timeout == nil || *decision.Timeout != *tt.wantTimeout {
				t.Fatalf("model ignored explicit provider lifetime: want %d, decision=%+v", *tt.wantTimeout, decision)
			}
			if decision.Backend != "" || decision.Persist != nil || decision.AutoCreateContext != nil ||
				decision.ProxyMode != "" || decision.ProxyProfile != "" || decision.ProxyCountry != "" || decision.ProxySticky != "" ||
				len(decision.Viewport) != 0 || len(decision.Environment) != 0 || decision.PresentationMode != "" {
				t.Fatalf("model synthesized unrelated optional arguments: %+v", decision)
			}
			t.Logf("LLM lifetime decision: %s", response.ArgumentsJSON)
		})
	}
}

func intPointer(value int) *int { return &value }
