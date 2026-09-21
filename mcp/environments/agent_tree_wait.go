package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// agentTreePollInterval is deliberately short because a completed ephemeral
// worker disappears from the live thread roster. Tests replace it with a much
// smaller value; production keeps enough distance to avoid polling Core hard.
var agentTreePollInterval = 500 * time.Millisecond

type agentTreeRuntime interface {
	WaitRuntimeAgent(string, string, sdk.RuntimeAgentWaitRequest) (*sdk.RuntimeAgentExecution, error)
	ListRuntimeAgentThreads(string, string) (json.RawMessage, error)
	GetRuntimeAgentThread(string, string, string) (json.RawMessage, error)
}

type runtimeThreadRow struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id,omitempty"`
	Iteration int    `json:"iteration"`
}

type runtimeThreadContext struct {
	ID        string                 `json:"id"`
	Iteration int                    `json:"iteration"`
	Messages  []runtimeThreadMessage `json:"messages"`
}

type runtimeThreadMessage struct {
	Role        string              `json:"role"`
	Content     string              `json:"content"`
	Parts       []runtimeThreadPart `json:"parts"`
	ToolCalls   []runtimeToolCall   `json:"tool_calls,omitempty"`
	ToolResults []runtimeToolResult `json:"tool_results,omitempty"`
}

type runtimeThreadPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type runtimeToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type runtimeToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

func (m runtimeThreadMessage) text() string {
	if strings.TrimSpace(m.Content) != "" {
		return m.Content
	}
	var out strings.Builder
	for _, part := range m.Parts {
		if part.Type == "text" {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

// waitRuntimeAgentTree preserves the existing single-thread wait semantics for
// the root, but treats an idle root as provisional while descendants are still
// alive. Descendant contexts are sampled while they exist and folded into the
// normalized trace. Once the tree is empty, the root is waited again so it can
// consume worker completion messages and produce its actual final response.
func waitRuntimeAgentTree(runtime agentTreeRuntime, runtimeID, agent string, req sdk.RuntimeAgentWaitRequest) (*sdk.RuntimeAgentExecution, error) {
	rootThread := strings.TrimSpace(req.ThreadID)
	if rootThread == "" {
		rootThread = "main"
	}
	req.ThreadID = rootThread
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}
	started := time.Now()
	deadline := started.Add(timeout)

	root, err := runtime.WaitRuntimeAgent(runtimeID, agent, req)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("runtime returned no root execution")
	}
	if root.Status == "failed" || root.Status == "timeout" || root.Reason == "max_turns" {
		return root, nil
	}

	children := map[string]*sdk.RuntimeAgentExecution{}
	sawDescendant := false

	for {
		rows, err := listRuntimeThreadRows(runtime, runtimeID, agent)
		if err != nil {
			return nil, err
		}
		descendants := descendantRows(rows, rootThread)
		if len(descendants) == 0 {
			if !sawDescendant {
				return mergeRuntimeTreeExecution(root, children), nil
			}
			remaining := time.Until(deadline)
			if remaining < 5*time.Second {
				return timedOutRuntimeTreeExecution(root, children), nil
			}
			follow := req
			follow.TimeoutSeconds = int(remaining / time.Second)
			if follow.TimeoutSeconds < 5 {
				follow.TimeoutSeconds = 5
			}
			root, err = runtime.WaitRuntimeAgent(runtimeID, agent, follow)
			if err != nil {
				return nil, err
			}
			if root == nil {
				return nil, fmt.Errorf("runtime returned no resumed root execution")
			}
			if root.Status == "failed" || root.Status == "timeout" || root.Reason == "max_turns" {
				return mergeRuntimeTreeExecution(root, children), nil
			}
			// The resumed root may have spawned another generation of workers.
			// Re-read the roster before accepting its idle state as terminal.
			sawDescendant = false
			continue
		}

		sawDescendant = true
		for _, row := range descendants {
			raw, getErr := runtime.GetRuntimeAgentThread(runtimeID, agent, row.ID)
			if getErr != nil {
				// Ephemeral workers can disappear between the roster and context
				// reads. The next roster read resolves that race.
				continue
			}
			var context runtimeThreadContext
			if err := json.Unmarshal(raw, &context); err != nil {
				return nil, fmt.Errorf("decode runtime thread %s: %w", row.ID, err)
			}
			if context.ID == "" {
				context.ID = row.ID
			}
			if context.Iteration < row.Iteration {
				context.Iteration = row.Iteration
			}
			children[row.ID] = normalizeRuntimeThreadContext(context)
		}

		if time.Now().After(deadline) {
			return timedOutRuntimeTreeExecution(root, children), nil
		}
		time.Sleep(agentTreePollInterval)
	}
}

func listRuntimeThreadRows(runtime agentTreeRuntime, runtimeID, agent string) ([]runtimeThreadRow, error) {
	raw, err := runtime.ListRuntimeAgentThreads(runtimeID, agent)
	if err != nil {
		return nil, err
	}
	var rows []runtimeThreadRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode runtime thread roster: %w", err)
	}
	return rows, nil
}

func descendantRows(rows []runtimeThreadRow, root string) []runtimeThreadRow {
	byID := make(map[string]runtimeThreadRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	out := make([]runtimeThreadRow, 0, len(rows))
	for _, row := range rows {
		if row.ID == "" || row.ID == root {
			continue
		}
		seen := map[string]bool{row.ID: true}
		parent := row.ParentID
		for parent != "" && !seen[parent] {
			if parent == root {
				out = append(out, row)
				break
			}
			seen[parent] = true
			next, ok := byID[parent]
			if !ok {
				break
			}
			parent = next.ParentID
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func normalizeRuntimeThreadContext(context runtimeThreadContext) *sdk.RuntimeAgentExecution {
	trace := []sdk.RuntimeTraceEvent{}
	pending := map[string]int{}
	turns := 0
	for _, message := range context.Messages {
		text := strings.TrimSpace(message.text())
		switch message.Role {
		case "assistant":
			if text != "" {
				trace = append(trace, sdk.RuntimeTraceEvent{Index: len(trace), ThreadID: context.ID, Role: "agent", Content: text})
			}
			for _, call := range message.ToolCalls {
				tool := &sdk.RuntimeToolCall{ID: call.ID, Name: call.Name, Input: append(json.RawMessage(nil), call.Args...)}
				trace = append(trace, sdk.RuntimeTraceEvent{Index: len(trace), ThreadID: context.ID, Role: "tool", ToolCall: tool})
				pending[call.ID] = len(trace) - 1
			}
			if text != "" || len(message.ToolCalls) > 0 {
				turns++
			}
		case "user":
			if text != "" && !strings.HasPrefix(text, "(no events)") {
				trace = append(trace, sdk.RuntimeTraceEvent{Index: len(trace), ThreadID: context.ID, Role: "user", Content: text})
			}
		}
		for _, result := range message.ToolResults {
			if index, ok := pending[result.CallID]; ok && index >= 0 && index < len(trace) && trace[index].ToolCall != nil {
				trace[index].ToolCall.Output = strings.TrimSpace(result.Content)
				trace[index].ToolCall.IsError = result.IsError
				delete(pending, result.CallID)
			} else {
				trace = append(trace, sdk.RuntimeTraceEvent{Index: len(trace), ThreadID: context.ID, Role: "tool_result", Content: strings.TrimSpace(result.Content)})
			}
		}
	}
	if context.Iteration > turns {
		turns = context.Iteration
	}
	return &sdk.RuntimeAgentExecution{Status: "completed", Reason: "captured", ThreadID: context.ID, Turns: turns, Trace: trace}
}

func runtimeTreeTurns(root *sdk.RuntimeAgentExecution, children map[string]*sdk.RuntimeAgentExecution) int {
	turns := 0
	if root != nil {
		turns += root.Turns
	}
	for _, child := range children {
		if child != nil {
			turns += child.Turns
		}
	}
	return turns
}

func mergeRuntimeTreeExecution(root *sdk.RuntimeAgentExecution, children map[string]*sdk.RuntimeAgentExecution) *sdk.RuntimeAgentExecution {
	if root == nil {
		return nil
	}
	merged := *root
	rootTrace := append([]sdk.RuntimeTraceEvent(nil), root.Trace...)
	insert := len(rootTrace)
	for i := len(rootTrace) - 1; i >= 0; i-- {
		if rootTrace[i].ThreadID == root.ThreadID && (rootTrace[i].Role == "agent" || rootTrace[i].Role == "assistant") {
			insert = i
			break
		}
	}
	trace := append([]sdk.RuntimeTraceEvent(nil), rootTrace[:insert]...)
	ids := make([]string, 0, len(children))
	for id := range children {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if child := children[id]; child != nil {
			trace = append(trace, child.Trace...)
		}
	}
	trace = append(trace, rootTrace[insert:]...)
	for i := range trace {
		trace[i].Index = i
	}
	merged.Trace = trace
	merged.Turns = runtimeTreeTurns(root, children)
	return &merged
}

func timedOutRuntimeTreeExecution(root *sdk.RuntimeAgentExecution, children map[string]*sdk.RuntimeAgentExecution) *sdk.RuntimeAgentExecution {
	merged := mergeRuntimeTreeExecution(root, children)
	if merged == nil {
		merged = &sdk.RuntimeAgentExecution{ThreadID: "main"}
	}
	merged.Status = "timeout"
	merged.Reason = "timeout"
	merged.FinishedAt = time.Now().UTC()
	return merged
}
