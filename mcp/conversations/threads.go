package main

// threads.go — thread-per-conversation, the channel-chat parity piece.
//
// Every conversation gets its own core thread ("chat-<conversation id>",
// the same convention channel-chat uses so a future data migration keeps
// thread identities). The thread is spawned lazily on first inbound
// message through the SDK's ThreadClient:
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
// Every failure degrades to the agent's main thread via SendEvent: a
// stopped agent, a platform without ThreadClient, or a spawn error must
// never lose a user's message.

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
	return a.ensureConversationThreadForAgent(app, conv, conv.LeadAgentID)
}

// ensureConversationThreadForAgent gives every participating agent an
// isolated thread for the room. Core namespaces thread ids by agent.
func (a *App) ensureConversationThreadForAgent(app *sdk.AppCtx, conv *Conversation, agentID int64) string {
	if app == nil {
		return ""
	}
	if agentID <= 0 {
		return ""
	}
	tc, ok := app.PlatformAPI().(sdk.ThreadClient)
	if !ok {
		// Platform (or test stub) without thread support: main thread.
		return ""
	}

	threadID := conv.ThreadID
	if threadID == "" {
		threadID = conversationThreadID(conv.ID)
	}
	suffix := conversationThreadDirective(conv)
	cacheKey := fmt.Sprintf("%d/%s", agentID, conv.ID)
	wantHash := threadSuffixHash(suffix)
	if prev, ok := spawnedThreads.Load(cacheKey); ok && prev.(string) == wantHash {
		return threadID
	}

	_, err := tc.SpawnThread(sdk.ThreadSpawnRequest{
		AgentID:         agentID,
		ThreadID:        threadID,
		DirectiveSuffix: suffix,
		// MCP nil → the platform supplies the agent's spawnable MCP
		// set. Tools nil → core's defaults for opaque threads.
	})
	if err != nil {
		// Do not cache failure — the agent may simply be stopped; the
		// next message retries the spawn.
		app.Logger().Warn("conversation thread spawn failed — falling back to main",
			"agent", agentID, "conversation", conv.ID, "err", err)
		return ""
	}
	spawnedThreads.Store(cacheKey, wantHash)

	if conv.ThreadID != threadID {
		if err := a.store.SetConversationThread(conv.ID, threadID); err == nil {
			conv.ThreadID = threadID
		}
	}
	return threadID
}
