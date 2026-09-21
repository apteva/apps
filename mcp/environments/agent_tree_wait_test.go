package main

import (
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type agentTreeRuntimeStub struct {
	waits    []*sdk.RuntimeAgentExecution
	rosters  [][]runtimeThreadRow
	contexts map[string]runtimeThreadContext
	waitCall int
	listCall int
}

func (s *agentTreeRuntimeStub) WaitRuntimeAgent(string, string, sdk.RuntimeAgentWaitRequest) (*sdk.RuntimeAgentExecution, error) {
	index := s.waitCall
	s.waitCall++
	if index >= len(s.waits) {
		index = len(s.waits) - 1
	}
	value := *s.waits[index]
	value.Trace = append([]sdk.RuntimeTraceEvent(nil), value.Trace...)
	return &value, nil
}

func (s *agentTreeRuntimeStub) ListRuntimeAgentThreads(string, string) (json.RawMessage, error) {
	index := s.listCall
	s.listCall++
	if index >= len(s.rosters) {
		index = len(s.rosters) - 1
	}
	return json.Marshal(s.rosters[index])
}

func (s *agentTreeRuntimeStub) GetRuntimeAgentThread(_, _, threadID string) (json.RawMessage, error) {
	return json.Marshal(s.contexts[threadID])
}

func TestWaitRuntimeAgentTreeWaitsForWorkerAndResumedRoot(t *testing.T) {
	previous := agentTreePollInterval
	agentTreePollInterval = time.Millisecond
	t.Cleanup(func() { agentTreePollInterval = previous })

	initial := &sdk.RuntimeAgentExecution{
		Status: "completed", Reason: "idle", ThreadID: "main", Turns: 2,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
		Trace: []sdk.RuntimeTraceEvent{
			{Index: 0, ThreadID: "main", Role: "user", Content: "repair checkout"},
			{Index: 1, ThreadID: "main", Role: "agent", Content: "I will delegate."},
			{Index: 2, ThreadID: "main", Role: "tool", ToolCall: &sdk.RuntimeToolCall{Name: "spawn", Output: "worker started"}},
		},
	}
	resumed := &sdk.RuntimeAgentExecution{
		Status: "completed", Reason: "idle", ThreadID: "main", Turns: 3,
		StartedAt: initial.StartedAt, FinishedAt: time.Now(),
		Trace: []sdk.RuntimeTraceEvent{
			{Index: 0, ThreadID: "main", Role: "user", Content: "repair checkout"},
			{Index: 1, ThreadID: "main", Role: "agent", Content: "I will delegate."},
			{Index: 2, ThreadID: "main", Role: "tool", ToolCall: &sdk.RuntimeToolCall{Name: "spawn", Output: "worker started"}},
			{Index: 3, ThreadID: "main", Role: "user", Content: "worker completed the repair"},
			{Index: 4, ThreadID: "main", Role: "agent", Content: "Repair complete and verified."},
		},
		Metrics: sdk.RuntimeAgentMetrics{LLMCalls: 4, ToolCalls: 2, TokensIn: 100, TokensOut: 20},
	}
	stub := &agentTreeRuntimeStub{
		waits: []*sdk.RuntimeAgentExecution{initial, resumed},
		rosters: [][]runtimeThreadRow{
			{{ID: "main", Iteration: 2}, {ID: "checkout-worker", ParentID: "main", Iteration: 4}},
			{{ID: "main", Iteration: 2}},
			{{ID: "main", Iteration: 3}},
		},
		contexts: map[string]runtimeThreadContext{
			"checkout-worker": {
				ID: "checkout-worker", Iteration: 4,
				Messages: []runtimeThreadMessage{
					{Role: "assistant", Content: "Applying the checkout repair.", ToolCalls: []runtimeToolCall{{ID: "edit-1", Name: "code_code_apply_patch", Args: json.RawMessage(`{"path":"pricing/cart.go"}`)}}},
					{Role: "user", ToolResults: []runtimeToolResult{{CallID: "edit-1", Content: `{"ok":true}`}}},
					{Role: "assistant", Content: "The repair is complete."},
				},
			},
		},
	}

	execution, err := waitRuntimeAgentTree(stub, "runtime-one", "main", sdk.RuntimeAgentWaitRequest{TimeoutSeconds: 30, MaxTurns: 22})
	if err != nil {
		t.Fatal(err)
	}
	if stub.waitCall != 2 {
		t.Fatalf("root wait calls=%d, want initial and post-worker waits", stub.waitCall)
	}
	if execution.Status != "completed" || execution.Reason != "idle" || execution.Turns != 7 {
		t.Fatalf("execution=%#v", execution)
	}
	workerTool, finalRoot := -1, -1
	for i, event := range execution.Trace {
		if event.ThreadID == "checkout-worker" && event.ToolCall != nil && event.ToolCall.Name == "code_code_apply_patch" && event.ToolCall.Output == `{"ok":true}` {
			workerTool = i
		}
		if event.ThreadID == "main" && event.Role == "agent" && event.Content == "Repair complete and verified." {
			finalRoot = i
		}
		if event.Index != i {
			t.Fatalf("trace index %d contains index %d", i, event.Index)
		}
	}
	if workerTool < 0 || finalRoot < 0 || workerTool > finalRoot {
		t.Fatalf("workerTool=%d finalRoot=%d trace=%#v", workerTool, finalRoot, execution.Trace)
	}
	if execution.Metrics.TokensIn != 100 || execution.Metrics.ToolCalls != 2 {
		t.Fatalf("metrics=%#v", execution.Metrics)
	}
}

func TestDescendantRowsIncludesNestedThreadsOnly(t *testing.T) {
	rows := []runtimeThreadRow{
		{ID: "main"},
		{ID: "worker", ParentID: "main"},
		{ID: "nested", ParentID: "worker"},
		{ID: "other-root"},
		{ID: "other-child", ParentID: "other-root"},
	}
	got := descendantRows(rows, "main")
	if len(got) != 2 || got[0].ID != "nested" || got[1].ID != "worker" {
		t.Fatalf("descendants=%#v", got)
	}
}
