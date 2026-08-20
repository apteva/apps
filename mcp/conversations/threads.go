package main

// threads.go — thread-per-conversation, the channel-chat parity piece.
//
// Every conversation gets its own core thread ("chat-<conversation id>",
// the same convention channel-chat uses so a future data migration keeps
// thread identities). The thread is spawned lazily on first inbound
// message through the SDK's ThreadClient. That first message is included
// in the spawn request, so core queues it before starting the thread:
//
//   - DirectiveSuffix rides on top of the agent's main directive —
//     the composition happens core-side, so the suffix only carries the
//     conversation's own context and reply contract.
//   - MCP is left nil on purpose: the platform fills in the agent's
//     spawnable MCP set (the callback's server-side default), which is
//     the sidecar-visible equivalent of channel-chat's filtered list.
//   - Spawn is idempotent (core's POST /threads/{id} on an existing
//     thread reports it rather than erroring), so the per-process cache
//     is an optimization, not a correctness requirement. A changed
//     suffix hash re-spawns, which refreshes the thread's directive —
//     the sidecar-visible approximation of channel-chat's drift update
//     until the SDK grows an UpdateThread surface.
//
// Spawn receipts are part of the delivery contract. Conversations only
// considers an initial event delivered when the platform reports its
// stable id as accepted or duplicate. A duplicate is success: it means a
// retry reached an event core has already persisted.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// conversationThreadID mirrors channel-chat's naming so thread
// identities survive the eventual data migration.
func conversationThreadID(conversationID string) string { return "chat-" + conversationID }

// conversationThreadEventID is stable across retries and app restarts.
// The message id is the durable store id, and the agent id disambiguates
// fan-out when a room addresses more than one agent.
func conversationThreadEventID(conversationID string, messageID, agentID int64) string {
	return fmt.Sprintf("conversation:%s:message:%d:agent:%d", conversationID, messageID, agentID)
}

// spawnedThreads caches (agent, conversation) → suffix hash for this
// process, mirroring channel-chat's spawnedChatThreads: skip the spawn
// round-trip when nothing changed, re-spawn when the directive drifted.
var spawnedThreads sync.Map

// conversationThreadDirective is the suffix core appends to the agent's
// main directive for this thread.
func conversationThreadDirective(conv *Conversation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nYou are in the conversation %q (id %s).", conv.Title, conv.ID)
	if conv.Origin != "web" && conv.Origin != "" {
		fmt.Fprintf(&b, " It is bridged from %s; participants may be external users.", conv.Origin)
	}
	b.WriteString(" Reply in this same conversation using conversations_send" +
		" (conversation_id=" + conv.ID + ")." +
		" Use conversations_request_approval before consequential external actions.")
	return b.String()
}

func threadSuffixHash(suffix string) string {
	sum := sha256.Sum256([]byte(suffix))
	return hex.EncodeToString(sum[:8])
}

// ensureConversationThread returns the conversation's live thread id,
// spawning or refreshing it as needed. Empty return means "use main" —
// the caller falls back to SendEvent.
func (a *App) ensureConversationThread(app *sdk.AppCtx, conv *Conversation) string {
	threadID, _, _ := a.ensureConversationThreadForAgent(app, conv, conv.LeadAgentID, nil)
	return threadID
}

// ensureConversationThreadForAgent gives every participating agent an
// isolated thread for the room. Core namespaces thread ids by agent. When
// initialEvent is non-nil and a spawn is needed, its receipt must explicitly
// acknowledge the event before initialDelivered is true.
func (a *App) ensureConversationThreadForAgent(
	app *sdk.AppCtx,
	conv *Conversation,
	agentID int64,
	initialEvent *sdk.ThreadEvent,
) (threadID string, initialDelivered bool, err error) {
	if app == nil {
		return "", false, fmt.Errorf("platform unavailable")
	}
	if agentID <= 0 {
		return "", false, fmt.Errorf("agent id required")
	}
	tc, ok := app.PlatformAPI().(sdk.ThreadClient)
	if !ok {
		// Platform (or test stub) without thread support: main thread.
		return "", false, fmt.Errorf("platform does not support threads")
	}

	threadID = conv.ThreadID
	if threadID == "" {
		threadID = conversationThreadID(conv.ID)
	}
	suffix := conversationThreadDirective(conv)
	cacheKey := fmt.Sprintf("%d/%s", agentID, conv.ID)
	wantHash := threadSuffixHash(suffix)
	if prev, ok := spawnedThreads.Load(cacheKey); ok && prev.(string) == wantHash {
		return threadID, false, nil
	}

	spawn := sdk.ThreadSpawnRequest{
		AgentID:         agentID,
		ThreadID:        threadID,
		DirectiveSuffix: suffix,
		// MCP nil → the platform supplies the agent's spawnable MCP
		// set. Tools nil → core's defaults for opaque threads.
	}
	if initialEvent != nil {
		spawn.Events = []sdk.ThreadEvent{*initialEvent}
	}
	result, err := tc.SpawnThread(spawn)
	if err != nil {
		// Do not cache failure — the agent may simply be stopped; the
		// next message retries the spawn.
		app.Logger().Warn("conversation thread spawn failed — falling back to main",
			"agent", agentID, "conversation", conv.ID, "err", err)
		return "", false, err
	}
	if initialEvent != nil && !threadEventAcknowledged(result, initialEvent.ID) {
		// Do not cache an unacknowledged spawn. The platform may have
		// created the thread, but it did not prove that it persisted the
		// user's message. A retry uses the same id and is therefore safe.
		return threadID, false, fmt.Errorf("platform did not acknowledge initial event %q", initialEvent.ID)
	}
	spawnedThreads.Store(cacheKey, wantHash)

	if conv.ThreadID != threadID {
		if err := a.store.SetConversationThread(conv.ID, threadID); err == nil {
			conv.ThreadID = threadID
		}
	}
	return threadID, initialEvent != nil, nil
}

func threadEventAcknowledged(result *sdk.ThreadSpawnResult, eventID string) bool {
	if result == nil || eventID == "" {
		return false
	}
	for _, accepted := range result.Events.Accepted {
		if accepted == eventID {
			return true
		}
	}
	for _, duplicate := range result.Events.Duplicates {
		if duplicate == eventID {
			return true
		}
	}
	return false
}
