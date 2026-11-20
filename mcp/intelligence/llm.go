package main

// LLM access goes through the platform's llm app (llm_chat_complete), so
// keys, routing and usage accounting stay in one place. The model is used
// only to extract structured claims and to propose evidence-cited
// adjustments; it never sets a probability directly.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type llmClient interface {
	Complete(system, user string, maxTokens int) (string, error)
	Model() string
}

type platformLLM struct {
	ctx   *sdk.AppCtx
	model string
	limit int
}

var llmBudget = struct {
	sync.Mutex
	day   string
	calls int
}{}

func llmCallsToday() int {
	llmBudget.Lock()
	defer llmBudget.Unlock()
	if llmBudget.day != time.Now().UTC().Format("2006-01-02") {
		return 0
	}
	return llmBudget.calls
}

func takeLLMBudget(limit int) bool {
	llmBudget.Lock()
	defer llmBudget.Unlock()
	day := time.Now().UTC().Format("2006-01-02")
	if llmBudget.day != day {
		llmBudget.day, llmBudget.calls = day, 0
	}
	if llmBudget.calls >= limit {
		return false
	}
	llmBudget.calls++
	return true
}

var errLLMBudget = errors.New("daily LLM call budget exhausted")

func (p *platformLLM) Model() string { return p.model }

func (p *platformLLM) Complete(system, user string, maxTokens int) (string, error) {
	if !takeLLMBudget(p.limit) {
		return "", errLLMBudget
	}
	req := map[string]any{"model": p.model, "max_tokens": maxTokens, "temperature": 0,
		"messages":     []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"subject_type": "app", "subject_id": "intelligence"}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := p.ctx.PlatformAPI().CallAppResult("llm", "llm_chat_complete", req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("llm returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// appLLM returns nil when no model is configured; callers then use their
// deterministic fallback.
func appLLM(ctx *sdk.AppCtx) llmClient {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	model := strings.TrimSpace(configString(ctx, "llm_model", "anthropic/claude-haiku-5-5"))
	if model == "" || model == "none" {
		return nil
	}
	return &platformLLM{ctx: ctx, model: model, limit: configInt(ctx, "llm_daily_calls", 300, 0, 100000)}
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok {
		return strings.TrimSpace(v)
	}
	return def
}

func configBool(ctx *sdk.AppCtx, key string, def bool) bool {
	switch strings.ToLower(configString(ctx, key, "")) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

// decodeLLMJSON pulls the first JSON object out of a model reply, tolerating
// code fences and surrounding prose.
func decodeLLMJSON(reply string, out any) error {
	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start < 0 || end <= start {
		return fmt.Errorf("no JSON object in model reply")
	}
	return json.Unmarshal([]byte(reply[start:end+1]), out)
}
