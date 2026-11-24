package main

import (
	"encoding/json"
	"strings"
	"time"
)

// ModelUse names the provider and model of one Core LLM call.
type ModelUse struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
}

// AgentModelInfo is what an operator sees for one agent in a conversation:
// the thread the conversation is bound to for that agent, and the model
// behind the agent's latest reply here.
type AgentModelInfo struct {
	AgentID     int64      `json:"agent_id"`
	ThreadID    string     `json:"thread_id,omitempty"`
	LastReply   *ModelUse  `json:"last_reply,omitempty"`
	ReplyThread string     `json:"reply_thread_id,omitempty"`
	RepliedAt   *time.Time `json:"replied_at,omitempty"`
}

func modelUseKey(agent int64, thread string) string {
	return streamCallKey(agent, thread, "")
}

// observeModel keeps the model each agent thread is using, from Core's
// llm.start (sent before the call, so it always precedes the reply the call
// produces) and llm.done (names the provider that answered, including a
// fallback). A primary provider error clears it: until the fallback's
// llm.done arrives, the model behind the next reply is unknown.
func (s *streamer) observeModel(event string, agent int64, thread, raw string) {
	if agent <= 0 || thread == "" {
		return
	}
	switch event {
	case "llm.start", "llm.done", "llm.provider_error":
	default:
		return
	}
	var d struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Role     string `json:"role"`
	}
	if json.Unmarshal([]byte(raw), &d) != nil {
		return
	}
	key := modelUseKey(agent, thread)
	s.mu.Lock()
	defer s.mu.Unlock()
	if event == "llm.provider_error" {
		if d.Role == "primary" {
			delete(s.models, key)
		}
		return
	}
	model := strings.TrimSpace(d.Model)
	if model == "" {
		return
	}
	s.models[key] = ModelUse{Provider: strings.TrimSpace(d.Provider), Model: model}
}

// modelFor returns the model the agent's thread is using now, if known.
func (s *streamer) modelFor(agent int64, thread string) *ModelUse {
	if s == nil || agent <= 0 || thread == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	use, ok := s.models[modelUseKey(agent, thread)]
	if !ok {
		return nil
	}
	return &use
}

// AgentModels reports, per agent participant, the bound thread and the
// model behind the agent's latest reply that recorded one.
func (s *store) AgentModels(conversationID string, agents []int64) ([]AgentModelInfo, error) {
	out := make([]AgentModelInfo, 0, len(agents))
	index := map[int64]int{}
	for _, id := range agents {
		index[id] = len(out)
		out = append(out, AgentModelInfo{AgentID: id})
	}
	rows, err := s.db.Query(`SELECT agent_id,thread_id FROM conversation_agent_threads WHERE conversation_id=?`, conversationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var agent int64
		var thread string
		if err := rows.Scan(&agent, &thread); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[agent]; ok {
			out[i].ThreadID = thread
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT m.agent_id,m.thread_id,m.generated_by,m.created_at FROM messages m
		WHERE m.id IN (SELECT MAX(id) FROM messages WHERE conversation_id=? AND role='agent' AND generated_by!='' GROUP BY agent_id)`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent int64
		var thread, raw, created string
		if err := rows.Scan(&agent, &thread, &raw, &created); err != nil {
			return nil, err
		}
		i, ok := index[agent]
		if !ok {
			continue
		}
		var use ModelUse
		if json.Unmarshal([]byte(raw), &use) != nil || use.Model == "" {
			continue
		}
		at, _ := parseSQLiteTime(created)
		at = at.UTC()
		out[i].LastReply, out[i].ReplyThread, out[i].RepliedAt = &use, thread, &at
	}
	return out, rows.Err()
}
