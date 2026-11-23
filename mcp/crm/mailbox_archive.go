package main

import (
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Messaging ids sent per archive request; Messaging accepts up to 2000 and
// archives at most 50 threads, most recent first.
const mailboxArchiveMessageLimit = 2000

// Closing marks the mailbox thread read; it stays in the inbox.
const closeMailboxAction = "mark_read"

// mailboxArchiveResult is Messaging's message_thread_archive reply, or the
// error that stopped it. Closing never fails because of it.
type mailboxArchiveResult struct {
	Action    string           `json:"action,omitempty"`
	Archived  []map[string]any `json:"archived,omitempty"`
	Failed    []map[string]any `json:"failed,omitempty"`
	Skipped   int              `json:"skipped,omitempty"`
	Remaining int              `json:"remaining,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// archiveConversationMailbox marks read the provider mailbox threads behind
// an email conversation that was just closed (closeMailboxAction). Messaging
// decides which threads it can update (today Gmail threads from a mailbox
// this project owns); other providers are skipped there. Returns nil when
// there is nothing to ask: no Messaging binding, not email, no messages from
// the bound install, or a Messaging version without the tool.
func archiveConversationMailbox(ctx *sdk.AppCtx, pid string, convo *Conversation) *mailboxArchiveResult {
	if convo == nil || convo.Channel != channelEmail || messagingBound(ctx) == nil {
		return nil
	}
	install := messagingInstallID(ctx)
	rows, err := ctx.AppDB().Query(`SELECT DISTINCT messaging_id FROM contact_activities
		WHERE project_id=? AND conversation_id=? AND messaging_id IS NOT NULL AND messaging_id>0
		  AND messaging_install_id IN (0, ?)
		ORDER BY messaging_id DESC LIMIT ?`, pid, convo.ID, install, mailboxArchiveMessageLimit)
	if err != nil {
		return &mailboxArchiveResult{Error: err.Error()}
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return &mailboxArchiveResult{Error: err.Error()}
		}
		ids = append(ids, id)
	}
	err = rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		return &mailboxArchiveResult{Error: err.Error()}
	}
	if len(ids) == 0 {
		return nil
	}
	var out mailboxArchiveResult
	if err := callMessagingTool(ctx, "message_thread_archive", map[string]any{"_project_id": pid, "message_ids": ids, "action": closeMailboxAction}, &out); err != nil {
		if messagingToolMissing(err) {
			return nil
		}
		ctx.Logger().Warn("crm mailbox update failed", "project_id", pid, "conversation_id", convo.ID, "err", err)
		return &mailboxArchiveResult{Error: err.Error()}
	}
	if len(out.Archived) == 0 && len(out.Failed) == 0 && out.Remaining == 0 {
		return nil
	}
	return &out
}

// Messaging older than message_thread_archive answers "tool not found".
func messagingToolMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "tool not found") || strings.Contains(msg, "unknown tool") || strings.Contains(msg, "-32601")
}
