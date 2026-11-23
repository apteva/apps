package main

// main_routing.go — an agent's first qualifying operator chat runs in main.
//
// There is no flag, label or setting. When an inbound message for an agent
// is recorded and that agent has no conversation bound to its main thread,
// a qualifying conversation binds to main (thread_id='main') instead of a
// chat-<id> thread. A unique index allows one such binding per agent; every
// other conversation keeps its own chat-* thread.
//
// The one exception is decided when the conversation is created: a lead agent
// without a project (the Apteva Helper) has a main thread that is not a chat
// thread, so the create handler stores main_routing=0 and its chats always get
// a chat-* thread. Routing reads that column; it never asks the platform.
//
// Main stays a global caller: the main binding is a delivery route, never an
// ownership restriction (see boundConversation).

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const mainThreadID = "main"

// mainRoutingEligibleTx reports whether a conversation may be answered by the
// agent's main thread: a one-agent operator chat started in the web UI, with no
// transport binding and no external participant, whose lead was not excluded
// at creation (main_routing=0).
func mainRoutingEligibleTx(tx *sql.Tx, conversationID string, agentID int64) (bool, error) {
	var lead int64
	var kind, origin, audience string
	var mainRouting int
	err := tx.QueryRow(`SELECT lead_agent_id, kind, origin, audience, main_routing FROM conversations WHERE id=?`, conversationID).
		Scan(&lead, &kind, &origin, &audience, &mainRouting)
	if err != nil {
		return false, err
	}
	if mainRouting == 0 || lead != agentID || kind != "direct" || (origin != "web" && origin != "") || (audience != "operator" && audience != "") {
		return false, nil
	}
	var bridged int
	if err := tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM telegram_bindings WHERE conversation_id=?) +
		(SELECT COUNT(*) FROM participants WHERE conversation_id=? AND external_identity!='') +
		(SELECT COUNT(*) FROM participants WHERE conversation_id=? AND agent_id IS NOT NULL AND agent_id>0 AND agent_id!=?)`,
		conversationID, conversationID, conversationID, agentID).Scan(&bridged); err != nil {
		return false, err
	}
	return bridged == 0, nil
}

// bindInboundAgentTx records which thread answers an inbound message, in the
// same transaction as the message. Existing bindings are never changed here:
// a conversation that already has a chat-* thread keeps it, and the per-agent
// unique index turns a lost race for main into an ordinary chat-* binding.
func bindInboundAgentTx(tx *sql.Tx, conversationID string, agentID int64) error {
	eligible, err := mainRoutingEligibleTx(tx, conversationID, agentID)
	if err != nil {
		return err
	}
	if eligible {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conversation_agent_threads(conversation_id,agent_id,thread_id)
			VALUES(?,?,?)`, conversationID, agentID, mainThreadID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO conversation_agent_threads(conversation_id,agent_id,thread_id)
		VALUES(?,?,?)`, conversationID, agentID, conversationThreadID(conversationID))
	return err
}

// mainRouted reports whether the agent's main thread answers this conversation.
func (s *store) mainRouted(conversationID string, agentID int64) bool {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM conversation_agent_threads WHERE conversation_id=? AND agent_id=? AND thread_id=?`,
		conversationID, agentID, mainThreadID).Scan(&one)
	return err == nil
}

// mainConversationID returns the live conversation main answers for an agent
// in a project, or "" when there is none.
func (s *store) mainConversationID(projectID string, agentID int64) string {
	var id string
	_ = s.db.QueryRow(`SELECT c.id FROM conversation_agent_threads t JOIN conversations c ON c.id=t.conversation_id
		WHERE t.agent_id=? AND t.thread_id=? AND c.project_id=? AND c.archived_at IS NULL`,
		agentID, mainThreadID, projectID).Scan(&id)
	return id
}

// mainReplyContract rides on every chat message delivered to main. Main keeps
// its own directive and tools; this is the only conversation context it gets.
func mainReplyContract(conv *Conversation) string {
	return fmt.Sprintf("\n\n[Conversation %q, id %s. You answer this operator conversation from main.]"+
		" Reply in it with conversations_send conversation_id=%s."+
		" Before calling any work tool, send phase=acknowledgement alone and briefly say what you are about to do;"+
		" finish with phase=final carrying the outcome. Answers needing no tool go straight to phase=final."+
		" Use this conversation for reports, alerts, approvals and questions about work requested here.",
		conv.Title, conv.ID, conv.ID)
}

// withMainReplyContract appends the reply contract to the first text part of
// an inbound payload, which is either a string or a list of content parts.
func withMainReplyContract(payload any, conv *Conversation) any {
	contract := mainReplyContract(conv)
	switch p := payload.(type) {
	case string:
		return p + contract
	case []map[string]any:
		out := make([]map[string]any, len(p))
		copy(out, p)
		for i, part := range out {
			if part["type"] == "text" {
				text, _ := part["text"].(string)
				clone := map[string]any{}
				for k, v := range part {
					clone[k] = v
				}
				clone["text"] = text + contract
				out[i] = clone
				return out
			}
		}
		return append([]map[string]any{{"type": "text", "text": strings.TrimSpace(contract)}}, out...)
	}
	return payload
}

// deliverToMain sends an inbound chat message to the agent's main thread
// through the tracked event API: the same stable event id, the same
// accepted-or-duplicate receipt rule, and no fallback to another thread.
func deliverToMain(app *sdk.AppCtx, conv *Conversation, agentID int64, event sdk.ThreadEvent) error {
	client := app.AgentEventsAPI()
	if client == nil {
		return errors.New("platform does not support tracked agent events")
	}
	receipt, err := client.SendTrackedAgentEvent(sdk.AgentEventRequest{
		AgentID: agentID, ThreadID: mainThreadID, SourceEventID: event.ID,
		Message: withMainReplyContract(event.Message, conv),
	})
	if err != nil {
		return err
	}
	if receipt == nil || receipt.SourceEventID != event.ID || receipt.ThreadID != mainThreadID || (!receipt.Accepted && !receipt.Duplicate) {
		return fmt.Errorf("platform did not confirm inbound event %q for main", event.ID)
	}
	return nil
}
