package main

// conversations — one app for every conversation surface.
//
// Internal dashboard chat and the inbox (approvals / reports / alerts /
// status) share one durable model.
// The organizing rule: inbox items ARE messages. An agent's approval
// request is a typed card inside its conversation; the inbox is a
// priority-ordered query over those rows; acting on a card anywhere
// mutates the one row every surface renders. Telegram is an optional
// transport backed by platform-managed integration connections.
//
// Developed in parallel with apteva-server's built-in channel-chat and
// the deprecated channels sidecar. Both are donors; neither is
// modified. See DESIGN.md for the migration phases that eventually
// retire them.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Embed the source installer manifest, including notification deep links,
// so installed and runtime metadata cannot drift.
//
//go:embed apteva.yaml
var manifestYAML string

type App struct {
	attachmentMu     sync.Mutex
	topicMu          sync.Mutex // serializes find-or-create of a topic conversation
	queueReleaseMu   sync.Mutex // serializes releaseQueuedMessages decisions
	asyncDelivery    bool
	deliveryWorker   *deliveryWorker
	store            *store
	hub              *hub
	adapters         *adapterRegistry
	streamer         *streamer
	telegramFeedback *telegramFeedbackManager
	// compactions holds the open context_compaction row per agent thread.
	compactionMu sync.Mutex
	compactions  map[string]*openCompaction
	// telemetryStop cancels the bridge subscription on unmount.
	telemetryStop func()
	// workers watches agents' spawned threads while any is linked to a
	// conversation and unfinished.
	workers *workerWatches
}

func main() { sdk.Run(&App{asyncDelivery: true}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("conversations requires a db block")
	}
	a.store = newStore(ctx.AppDB())
	if err := a.recoverVoiceSessions(ctx); err != nil {
		ctx.Logger().Warn("voice session recovery incomplete", "err", err)
	}
	if err := a.store.interruptToolActivities(); err != nil {
		return err
	}
	a.hub = newHub()
	a.adapters = newAdapterRegistry(a, a.hub)
	a.streamer = newStreamer(a.hub)
	a.streamer.throttle = 40 * time.Millisecond
	a.streamer.resolve = func(agentID int64, threadID string) string {
		var id string
		err := a.store.db.QueryRow(`SELECT c.id FROM conversations c JOIN conversation_agent_threads t ON t.conversation_id=c.id JOIN participants p ON p.conversation_id=c.id AND p.agent_id=t.agent_id WHERE t.agent_id=? AND t.thread_id=? AND c.archived_at IS NULL`, agentID, threadID).Scan(&id)
		if err != nil {
			_ = a.store.db.QueryRow(`SELECT conversation_id FROM conversation_voice_sessions WHERE agent_id=? AND thread_id=? AND status IN ('starting','active')`, agentID, threadID).Scan(&id)
		}
		return id
	}
	a.telegramFeedback = newTelegramFeedbackManager(a)
	a.streamer.onFrame = a.telegramFeedback.OnFrame
	a.streamer.onActivityChange = func(chat string) {
		a.publishListProgress(chat)
		a.releaseQueuedMessages(chat)
	}
	mountedCtx = ctx
	a.workers = newWorkerWatches(a.workerObserver)
	a.resumeWorkerWatches()
	// Recover queue rows that were created while the process was down and no
	// response owns the conversation anymore.
	// The SDK serializes the app writer DB through one connection. Read the
	// conversation IDs and close that cursor before releaseQueuedMessages does
	// its per-conversation queries; querying while rows is still open deadlocks
	// startup on a fresh SDK build.
	var queuedConversationIDs []string
	if rows, err := a.store.db.Query(`SELECT id FROM conversations WHERE archived_at IS NULL`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				queuedConversationIDs = append(queuedConversationIDs, id)
			}
		}
		_ = rows.Close()
	}
	for _, id := range queuedConversationIDs {
		a.releaseQueuedMessages(id)
	}
	// Token-level streaming when the platform grants it; Stage-1 phase
	// frames otherwise. The bridge connects asynchronously because the
	// platform may still be registering this install during startup.
	if a.runTelemetryFeed(ctx) {
		ctx.Logger().Info("telemetry bridge enabled — connecting")
	}
	// Crash recovery: anything the ledger recorded but never confirmed
	// goes out again according to its persisted retry schedule.
	if a.asyncDelivery {
		a.startDeliveryWorker(ctx)
		a.wakeDeliveries()
	} else if redelivered, err := a.redeliverPending(ctx); err != nil {
		ctx.Logger().Warn("pending redelivery incomplete", "err", err)
	} else if redelivered > 0 {
		ctx.Logger().Info("redelivered pending messages", "count", redelivered)
	}
	ctx.Logger().Info("conversations mounted")
	return nil
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	if a.store != nil && ctx != nil {
		if err := a.recoverVoiceSessions(ctx); err != nil {
			ctx.Logger().Warn("voice shutdown incomplete", "err", err)
		}
	}
	if a.deliveryWorker != nil {
		close(a.deliveryWorker.stop)
		a.deliveryWorker.done.Wait()
	}
	legacyThreadPlatforms.Delete(a)
	if a.telemetryStop != nil {
		a.telemetryStop()
	}
	if a.workers != nil {
		a.workers.close()
	}
	if a.telegramFeedback != nil {
		a.telegramFeedback.Close()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "delivery-retry",
			Schedule: "@every 10s",
			Run: func(_ context.Context, app *sdk.AppCtx) error {
				if err := a.expireComponents(app); err != nil {
					return err
				}
				if a.deliveryWorker != nil {
					a.wakeDeliveries()
					return nil
				}
				_, err := a.redeliverPending(app)
				return err
			},
		},
		{
			Name:     "thread-lifecycle",
			Schedule: "@every 5m",
			Run: func(_ context.Context, app *sdk.AppCtx) error {
				return a.runThreadLifecycle(app)
			},
		},
		{
			Name:     "telegram-maintenance",
			Schedule: "@every 1h",
			Run: func(_ context.Context, _ *sdk.AppCtx) error {
				if err := a.store.PruneDeliveryHistory(); err != nil {
					return err
				}
				if err := a.store.PruneTelegramState(); err != nil {
					return err
				}
				return a.store.PruneTransportOnboarding()
			},
		},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{{Event: sdk.AgentEventLifecycleEvent, Handler: a.handleApprovalLifecycle}}
}

// sendKindMessage is send's default kind: an ordinary chat reply. Alerts and
// reports use the stored component kinds of the same name.
const sendKindMessage = "message"

// loadWhenNamed is a generic host hint: load this tool for a model only when
// the agent's instructions, the request or a skill name it, not by topical
// relevance. Hosts that do not know the hint list the tool as usual.
var loadWhenNamed = map[string]any{"io.apteva/autoload": "named"}

// sendToolDescription is deliberately short. Pacing, components, resource
// views and suggestions are taught by the using-conversations skill and the
// conversation thread directive; repeating them here crowded other tools out
// of the model's tool budget.
const sendToolDescription = "Post a chat reply, an alert card or a report card to a conversation. kind=message (default) is a reply from the thread that owns the conversation. " +
	"Main replies only in the conversation it answers, or acknowledges its own resolved approval with phase=acknowledgement and approval_message_id; " +
	"elsewhere main posts cards, not replies. kind=alert (text, severity) and kind=report (title, summary, optional sections and period) " +
	"file Inbox cards in the operator conversation that owns the work. Without a conversation_id, main sets topic to reuse or create that work conversation. " +
	"Phases: an acknowledgement alone before work tools, progress only during long work, then one final outcome. " +
	"Optionally attach one next-message suggestion to a final reply with " +
	"components=[{app:conversations,name:reply-suggestions,props:{items:[{id:next,text:USER_MESSAGE}]}}]: a short first-person request the user could send " +
	"for a useful next logical step, not for greetings or routine acknowledgements. " +
	"Other components and app views follow the Conversations skill."

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// Attachment readers remain implemented for compatibility and direct
		// tests, but are no longer agent-facing. Non-image files arrive in the
		// thread event as shared file references.
		{Name: "resolve_thread_identity", Description: "Internal trusted backend identity resolution; never accepts a user identity from an agent.", InputSchema: schemaObject(map[string]any{"agent_id": map[string]any{"type": "integer"}, "thread_id": map[string]any{"type": "string"}}, []string{"agent_id", "thread_id"}), HandlerCtx: a.toolResolveThreadIdentity},
		{
			Name:        "send",
			Description: sendToolDescription,
			InputSchema: schemaObject(map[string]any{
				"conversation_id":     map[string]any{"type": "string", "description": "Exact id. A conversation thread may omit it for its own conversation."},
				"topic":               map[string]any{"type": "string", "description": "Main, instead of conversation_id: a work topic title such as \"Backup recovery\", reused or created."},
				"kind":                map[string]any{"type": "string", "enum": []string{sendKindMessage, kindAlert, kindReport}, "description": "message (default) replies in chat; alert and report file Inbox cards."},
				"text":                map[string]any{"type": "string", "description": "Markdown reply, or the alert text. In a numbered list the list supplies the number: write \"1. Define the goal\", never \"1. 01. Define the goal\". When item titles carry their own sequence numbers, drop that prefix and use a plain numbered list."},
				"phase":               map[string]any{"type": "string", "enum": []string{"acknowledgement", "progress", "final"}, "description": "message only. final (default) ends the visible response, also before pace; acknowledgement/progress mean work remains now."},
				"severity":            map[string]any{"type": "string", "enum": []string{"info", "warn", "error"}, "description": "alert: error breaks things, warn degrades, info (default) informs."},
				"title":               map[string]any{"type": "string", "description": "report: a headline naming the outcome."},
				"summary":             map[string]any{"type": "string", "description": "report: one or two plain sentences (about 25 words) for the Inbox preview."},
				"sections":            map[string]any{"type": "array", "items": schemaObject(map[string]any{"title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}}, nil), "description": "report: optional details in short paragraphs, bullets or labeled links."},
				"period":              map[string]any{"type": "string", "description": "report: the time window covered."},
				"idempotency_key":     map[string]any{"type": "string"},
				"approval_message_id": map[string]any{"type": "integer", "minimum": 1, "description": "Main's own resolved approval being acknowledged."},
				"components":          componentsSchema(),
				"attachments":         map[string]any{"type": "array", "items": sdk.FileReferencePassthroughSchema("Image or file handle from the platform. Pass the complete _file handle unchanged.")},
			}, nil),
			HandlerCtx: a.toolSend,
		},
		{
			Name: "request_approval",
			Meta: map[string]any{"io.apteva/wakeOnResult": true},
			Description: "Ask the operator to approve gated work before doing it. The card asks the question, so do not announce it " +
				"with a separate send. The verdict returns to the asking thread as approval.result. Use the conversation that " +
				"owns the work, or topic from main. Omit actions for Approve/Deny.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"topic":           map[string]any{"type": "string", "description": "Main only, instead of conversation_id: the work topic title. Reuses or creates that conversation."},
				"title":           map[string]any{"type": "string"},
				"body":            map[string]any{"type": "string"},
				"actions":         approvalActionsSchema(),
			}, []string{"title"}),
			HandlerCtx: a.toolRequestApproval,
		},
		// Aliases of send kind=report/alert, kept while agents, directives and
		// saved tool selections still name them. Hosts load them only when named.
		{
			Name:        "report",
			Meta:        loadWhenNamed,
			Description: "Alias of conversations_send kind=report. Prefer conversations_send.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"topic":           map[string]any{"type": "string"},
				"title":           map[string]any{"type": "string"},
				"summary":         map[string]any{"type": "string"},
				"period":          map[string]any{"type": "string"},
				"sections":        map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, []string{"title", "summary"}),
			HandlerCtx: a.toolReport,
		},
		{
			Name:        "alert",
			Meta:        loadWhenNamed,
			Description: "Alias of conversations_send kind=alert. Prefer conversations_send.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"topic":           map[string]any{"type": "string"},
				"text":            map[string]any{"type": "string"},
				"severity":        map[string]any{"type": "string", "enum": []string{"info", "warn", "error"}},
			}, []string{"text"}),
			HandlerCtx: a.toolAlert,
		},
		{
			Name: "create",
			Meta: loadWhenNamed,
			Description: "Main-thread conversation management only. Reuse the originating conversation or search existing work topics first. " +
				"Create a conversation only for work with no relevant existing conversation. Name the work, such as " +
				"\"November content plan\" or \"Backup recovery\". Title-idempotent: if you already have a conversation " +
				"with this title it is returned with created=false, so reusing a stable title never duplicates. " +
				"Conversation threads and generic workers do not create conversations. Avoid generic Reports, Alerts, or Approvals buckets. Titles name ongoing topics — " +
				"omit per-notification timestamps, counters, or generic item types.",
			InputSchema: schemaObject(map[string]any{
				"title": map[string]any{"type": "string"},
			}, []string{"title"}),
			HandlerCtx: a.toolCreate,
		},
		{
			Name: "list",
			Meta: loadWhenNamed,
			Description: "Main-thread conversation management only. List conversations this agent participates in. " +
				"Use a known originating conversation_id directly; otherwise search for the work topic before creating a conversation. " +
				"Discovery does not grant ownership of their Core threads: never update, rename, kill or replace them. " +
				"Conversation threads already have their exact id; generic workers report to their parent. Pass query " +
				"to filter by title (case-insensitive substring) — search before creating.",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer"},
				"query": map[string]any{"type": "string"},
			}, nil),
			HandlerCtx: a.toolList,
		},
		{
			Name: "history",
			Meta: loadWhenNamed,
			Description: "Read a conversation transcript. Main may inspect a participating conversation; a conversation " +
				"thread may read only its own exact conversation; generic workers are not granted this tool.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"since_id":        map[string]any{"type": "integer"},
				"limit":           map[string]any{"type": "integer"},
			}, []string{"conversation_id"}),
			HandlerCtx: a.toolHistory,
		},
		{
			Name: "keep_thread",
			Meta: loadWhenNamed,
			Description: "Answer a [conversation idle check] from a conversation thread that still has ongoing timed work " +
				"(a scheduled follow-up, a running job, a watched condition). Keeps this thread awake for another idle period. " +
				"Only the conversation's own thread may call it; do nothing when no timed work remains.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"reason":          map[string]any{"type": "string", "maxLength": 300, "description": "The ongoing timed work that needs this thread."},
			}, []string{"conversation_id", "reason"}),
			HandlerCtx: a.toolKeepThread,
		},
		{
			Name:     "inbox_post",
			Exposure: sdk.ToolExposureAppOnly,
			Description: "INTERNAL sibling-app entry point (CallAppResult) — not for agents; agent calls are " +
				"refused. Agents raise items with conversations_send kind=alert or kind=report, or " +
				"conversations_request_approval, into a conversation (an exact conversation_id, or topic from main). With callback_tool set, inbox actions call back into the " +
				"posting app. Pass conversation_id to keep the item in the existing operator conversation that owns " +
				"the work in the authenticated project. Calls without conversation context use the legacy app inbox conversation.",
			InputSchema: schemaObject(map[string]any{
				"kind":              map[string]any{"type": "string", "enum": []string{"report", "alert", "approval"}},
				"conversation_id":   map[string]any{"type": "string", "description": "The existing operator conversation that owns the work, in the authenticated project. Supply it when posting about work originating in a conversation."},
				"title":             map[string]any{"type": "string"},
				"body":              map[string]any{"type": "string"},
				"severity":          map[string]any{"type": "string"},
				"actions":           approvalActionsSchema(),
				"sections":          map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"callback_tool":     map[string]any{"type": "string"},
				"client_message_id": map[string]any{"type": "string", "maxLength": 200, "description": "Stable request key scoped to the authenticated app install. Reuse only for identical retries."},
				"agent_id":          map[string]any{"type": "integer"},
			}, []string{"kind", "title"}),
			HandlerCtx: a.toolInboxPost,
		},
	}
}

// ─── agent identity ──────────────────────────────────────────────────

type callIdentity struct {
	AgentID    int64
	ThreadID   string
	ToolCallID string
	ProjectID  string
}

// requireAgentCaller fails closed: a tool call without caller identity
// cannot write into conversations. (The SDK treats nil caller as full
// access for back-compat; a conversation store must not.) The project may be
// empty: agents without a project, such as the Apteva Helper, act only in
// conversations they were added to (requireParticipant), and cannot list or
// create conversations (requireCallerProject).
func requireAgentCaller(ctx context.Context) (*callIdentity, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AgentID == 0 {
		return nil, errors.New("caller identity required: this tool must be called by an agent")
	}
	if strings.TrimSpace(caller.ThreadID) == "" {
		return nil, errors.New("trusted agent thread context required")
	}
	return &callIdentity{AgentID: caller.AgentID, ThreadID: strings.TrimSpace(caller.ThreadID),
		ToolCallID: caller.ToolCallID, ProjectID: strings.TrimSpace(caller.ProjectID)}, nil
}

// requireCallerProject guards the tools that act across a project rather than
// on a conversation the caller was added to.
func requireCallerProject(from *callIdentity, tool string) error {
	if from.ProjectID == "" {
		return fmt.Errorf("%s needs an authenticated project; this agent has no project, so it can act only in conversations it was added to (use their conversation_id)", tool)
	}
	return nil
}

// boundConversation returns the conversation that owns the caller's opaque
// thread, if any. Conversations owns this relationship in its store; neither
// Core nor the SDK needs to classify threads as main/conversation/worker.
func (a *App) boundConversation(from *callIdentity) (*Conversation, error) {
	// Main may answer one conversation (main routing), but that binding is a
	// delivery route. Main stays the agent's global caller.
	if from.ThreadID == mainThreadID {
		return nil, nil
	}
	var retired int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM retired_conversation_threads WHERE (?='' OR project_id=?) AND agent_id=? AND thread_id=?`, from.ProjectID, from.ProjectID, from.AgentID, from.ThreadID).Scan(&retired); err != nil {
		return nil, err
	}
	if retired > 0 {
		return nil, errors.New("originating conversation was deleted; thread is retired")
	}
	return a.store.ConversationForAgentThread(from.ProjectID, from.AgentID, from.ThreadID)
}

// requireGlobalCaller refuses conversation-management tools from a thread
// that Conversations itself created for a particular conversation. Other
// opaque threads are intentionally not classified; as in Tasks, parents keep
// global mutation tools away from generic workers when spawning them.
func (a *App) requireGlobalCaller(from *callIdentity, tool string) error {
	bound, err := a.boundConversation(from)
	if err != nil {
		return err
	}
	if bound != nil {
		return fmt.Errorf("%s is not available from conversation %s; conversation management belongs to the parent/main thread", tool, bound.ID)
	}
	return nil
}

// requireOperatorAudience is the structural guard behind the skill's
// rule: inbox-kind items never land in front of a public audience. A
// site visitor must not see approvals about backups or error alerts —
// those belong in an operator conversation that references the public
// one.
func requireOperatorAudience(conv *Conversation, kind string) error {
	if conv.Audience == "public" {
		return fmt.Errorf("%s refused: %q is a public (visitor) conversation — send an escalation to parent/main, which raises the %s in an operator conversation and references conversation %s; this bound thread must reply to the visitor using conversations_send kind=message only",
			kind, conv.Title, kind, conv.ID)
	}
	return nil
}

// resolveDestination finds the conversation an item is posted to. An exact
// conversation_id always wins. A bound conversation thread can only post in its
// own conversation, so it may omit the id. Any other caller names the work
// topic instead, and the conversation with that title is reused, or created
// when create is set. Plain replies never create one: a new conversation has
// no thread waiting for a reply.
func (a *App) resolveDestination(from *callIdentity, args map[string]any, create bool) (*Conversation, error) {
	if id := strings.TrimSpace(stringArg(args, "conversation_id")); id != "" {
		return a.requireParticipant(from, id)
	}
	bound, err := a.boundConversation(from)
	if err != nil {
		return nil, err
	}
	if bound != nil {
		return a.requireParticipant(from, bound.ID)
	}
	topic := strings.TrimSpace(stringArg(args, "topic"))
	if topic == "" {
		return nil, errors.New("conversation_id or topic required: use the originating conversation_id, or pass topic (a work topic title such as \"Backup recovery\") to reuse or create that conversation")
	}
	conv, _, err := a.topicConversation(from, topic, "topic", create)
	return conv, err
}

// topicConversation returns the caller's conversation titled title, creating
// it when none exists and create is set. Titles are idempotent per agent; the
// lock keeps two parallel calls for a new topic from creating it twice.
func (a *App) topicConversation(from *callIdentity, title, tool string, create bool) (*Conversation, bool, error) {
	if err := requireCallerProject(from, tool); err != nil {
		return nil, false, err
	}
	if err := a.requireGlobalCaller(from, tool); err != nil {
		return nil, false, err
	}
	a.topicMu.Lock()
	defer a.topicMu.Unlock()
	existing, err := a.store.FindAgentConversationByTitle(from.ProjectID, from.AgentID, title)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		conv, err := a.requireParticipant(from, existing.ID)
		return conv, false, err
	}
	if !create {
		return nil, false, fmt.Errorf("no conversation titled %q: a reply goes to an existing conversation; post kind=alert or kind=report by topic instead", title)
	}
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID:   from.ProjectID,
		LeadAgentID: from.AgentID,
		Title:       title,
		Origin:      "agent",
	})
	if err != nil {
		return nil, false, err
	}
	return conv, true, nil
}

// requireParticipant loads the conversation and verifies the calling
// agent belongs to it. A caller with a project must match the conversation's
// project. A caller without one (such as the Apteva Helper) is authorized by
// its participant row alone, which only project users can add; the rest of
// the call then runs in the conversation's project.
func (a *App) requireParticipant(from *callIdentity, conversationID string) (*Conversation, error) {
	conv, err := a.store.GetConversation(conversationID)
	if err != nil {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	projectless := from.ProjectID == ""
	if !projectless && conv.ProjectID != from.ProjectID {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	ok, err := a.store.IsParticipantAgent(conversationID, from.AgentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("agent %d is not a participant of %s", from.AgentID, conversationID)
	}
	bound, err := a.boundConversation(from)
	if err != nil {
		return nil, err
	}
	if bound != nil && bound.ID != conv.ID {
		return nil, fmt.Errorf("thread %s belongs to conversation %s and cannot operate on conversation %s", from.ThreadID, bound.ID, conv.ID)
	}
	if projectless {
		from.ProjectID = conv.ProjectID
	}
	return conv, nil
}

// ─── tools ───────────────────────────────────────────────────────────

func (a *App) toolSend(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	text, _ := args["text"].(string)
	phase := strings.TrimSpace(stringArg(args, "phase"))
	if phase == "" {
		phase = "final"
	}
	switch phase {
	case "acknowledgement", "progress", "final":
	default:
		return nil, fmt.Errorf("invalid phase %q", phase)
	}
	kind := strings.ToLower(strings.TrimSpace(stringArg(args, "kind")))
	if kind == "" {
		kind = sendKindMessage
	}
	if kind != sendKindMessage && kind != kindAlert && kind != kindReport {
		return nil, fmt.Errorf("invalid kind %q: use message, alert or report", kind)
	}
	conv, err := a.resolveDestination(from, args, kind != sendKindMessage)
	if err != nil {
		return nil, err
	}
	if kind != sendKindMessage {
		if presentArg(args["components"]) || presentArg(args["attachments"]) {
			return nil, fmt.Errorf("components and attachments belong to kind=message; a %s card carries only its own fields", kind)
		}
		if kind == kindAlert {
			return a.postAlert(app, from, conv, args)
		}
		return a.postReport(app, from, conv, args)
	}
	clientID := strings.TrimSpace(stringArg(args, "idempotency_key"))
	if from.ThreadID == mainThreadID {
		approvalID := int64(intArg(args, "approval_message_id", 0))
		routed := a.store.mainRouted(conv.ID, from.AgentID)
		if approvalID > 0 || !routed {
			// Outside the conversation main answers, main may only acknowledge
			// its own resolved approval.
			approval, err := a.store.GetMessage(approvalID)
			if phase != "acknowledgement" || err != nil || approval.ComponentKind != kindApproval ||
				approval.ConversationID != conv.ID || approval.AgentID != from.AgentID || approval.ThreadID != mainThreadID ||
				(approval.ActionStatus == "" || approval.ActionStatus == "pending") {
				return nil, errors.New("ordinary conversation replies belong to the mapped conversation thread; from main, post kind=alert or kind=report here, or acknowledge your own resolved approval with phase=acknowledgement and approval_message_id")
			}
			// An event retry or a second tool call must not duplicate the receipt.
			clientID = fmt.Sprintf("approval:%d:acknowledgement", approvalID)
		}
	}
	components, err := componentsArg(args, "components")
	if err != nil {
		return nil, err
	}
	attachments, err := attachmentsArg(args, "attachments")
	if err != nil {
		return nil, err
	}
	if err := a.authorizeAgentReferenceAttachments(ctx, app, conv, attachments); err != nil {
		return nil, err
	}
	if err := normalizeInteractions(components, time.Now()); err != nil {
		return nil, err
	}
	if err := normalizeReplySuggestions(components, phase, text); err != nil {
		return nil, err
	}
	hasInteraction, blocking := false, false
	for _, c := range components {
		if c.Interaction != nil {
			hasInteraction = true
			blocking = blocking || c.Interaction.Blocking
		}
	}
	if blocking && phase != "final" {
		return nil, errors.New("blocking components require phase=final before waiting")
	}
	if hasInteraction {
		if err := requireOperatorAudience(conv, "interactive components"); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(text) == "" && len(attachments) == 0 && len(components) == 0 {
		return nil, errors.New("text or attachments required")
	}
	kind, actionStatus := "", ""
	if hasInteraction {
		kind, actionStatus = kindInteractive, interactionMessageState(components)
	}
	msg, inserted, err := a.appendAndDeliver(app, conv, &Message{
		ComponentKind: kind, ActionStatus: actionStatus,
		ConversationID: conv.ID, Role: "agent", Content: text,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ClientID: clientID, toolCallID: from.ToolCallID, Phase: phase,
		Components: components, Attachments: attachments,
	})
	if err != nil {
		return nil, err
	}
	// The durable reply supersedes any pending thinking bubble.
	a.streamer.settleAck(conv.ID, from.AgentID)
	if phase == "final" || blocking {
		a.streamer.finishResponse(conv.ID, from.AgentID)
	} else {
		a.streamer.intermediateReply(conv.ID, from.AgentID)
	}
	receipt := map[string]any{"message_id": msg.ID, "conversation_id": conv.ID, "phase": msg.Phase,
		"inserted": inserted, "duplicate_suppressed": !inserted}
	var refs []map[string]any
	for _, component := range components {
		if component.Resource != nil {
			refs = append(refs, map[string]any{"app": component.App, "name": component.Name, "resource": component.Resource})
		}
	}
	if len(refs) > 0 {
		receipt["view_refs"] = refs
	}
	return receipt, nil
}

func (a *App) toolRequestApproval(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	body := strings.TrimSpace(stringArg(args, "body"))
	if title == "" {
		return nil, errors.New("title required")
	}
	if body == "" {
		body = title
	}
	conv, err := a.resolveDestination(from, args, true)
	if err != nil {
		return nil, err
	}
	if err := requireOperatorAudience(conv, "approval"); err != nil {
		return nil, err
	}
	actions, err := approvalActionsArg(args)
	if err != nil {
		return nil, err
	}
	msg, inserted, err := a.appendAndDeliver(app, conv, &Message{
		ConversationID: conv.ID, Role: "agent",
		Content: "Approval requested: " + title,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ComponentKind: kindApproval,
		Components:    []Component{approvalCard(title, body, actions)},
		toolCallID:    from.ToolCallID,
	})
	if err != nil {
		return nil, err
	}
	// The approval card is the response: the next step belongs to the user.
	if inserted {
		a.streamer.settleAck(conv.ID, from.AgentID)
		a.streamer.finishResponseWithMessage(conv.ID, from.AgentID, msg.ID)
	}
	return map[string]any{"message_id": msg.ID, "status": "pending", "inserted": inserted, "duplicate_suppressed": !inserted}, nil
}

// toolReport and toolAlert are the legacy names of send kind=report/alert.
func (a *App) toolReport(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.resolveDestination(from, args, true)
	if err != nil {
		return nil, err
	}
	return a.postReport(app, from, conv, args)
}

func (a *App) toolAlert(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.resolveDestination(from, args, true)
	if err != nil {
		return nil, err
	}
	return a.postAlert(app, from, conv, args)
}

// postReport files a report card. A model that writes the summary as text is
// understood rather than refused.
func (a *App) postReport(app *sdk.AppCtx, from *callIdentity, conv *Conversation, args map[string]any) (any, error) {
	if err := requireOperatorAudience(conv, "report"); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	summary := firstNonEmptyString(strings.TrimSpace(stringArg(args, "summary")), strings.TrimSpace(stringArg(args, "text")))
	if title == "" || summary == "" {
		return nil, errors.New("a report needs title and summary")
	}
	sections, err := genericObjectsArg(args, "sections", 50)
	if err != nil {
		return nil, err
	}
	msg, inserted, err := a.appendAndDeliver(app, conv, &Message{
		ConversationID: conv.ID, Role: "agent",
		Content: "Report: " + title,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ComponentKind: kindReport,
		Components:    []Component{reportCard(title, summary, stringArg(args, "period"), sections)},
		ClientID:      strings.TrimSpace(stringArg(args, "idempotency_key")),
		toolCallID:    from.ToolCallID,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": msg.ID, "conversation_id": conv.ID, "kind": kindReport, "status": "sent",
		"inserted": inserted, "duplicate_suppressed": !inserted}, nil
}

// postAlert files an alert card. Severity defaults to info.
func (a *App) postAlert(app *sdk.AppCtx, from *callIdentity, conv *Conversation, args map[string]any) (any, error) {
	if err := requireOperatorAudience(conv, "alert"); err != nil {
		return nil, err
	}
	text := firstNonEmptyString(strings.TrimSpace(stringArg(args, "text")), strings.TrimSpace(stringArg(args, "summary")))
	if text == "" {
		return nil, errors.New("an alert needs text")
	}
	severity := stringArg(args, "severity")
	switch severity {
	case "info", "warn", "error":
	case "":
		severity = "info"
	default:
		return nil, fmt.Errorf("invalid severity %q: use info, warn or error", severity)
	}
	msg, inserted, err := a.appendAndDeliver(app, conv, &Message{
		ConversationID: conv.ID, Role: "agent", Content: text,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ComponentKind: kindAlert, Severity: severity,
		Components: []Component{alertCard(text, severity)},
		ClientID:   strings.TrimSpace(stringArg(args, "idempotency_key")),
		toolCallID: from.ToolCallID,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": msg.ID, "conversation_id": conv.ID, "kind": kindAlert, "severity": severity,
		"inserted": inserted, "duplicate_suppressed": !inserted}, nil
}

// toolCreate is title-idempotent per agent. The model chooses a meaningful
// work topic after checking whether a relevant conversation already exists.
func (a *App) toolCreate(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	conv, created, err := a.topicConversation(from, title, "conversations_create", true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversation_id": conv.ID, "title": conv.Title, "created": created}, nil
}

func (a *App) toolList(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireCallerProject(from, "conversations_list"); err != nil {
		return nil, err
	}
	if err := a.requireGlobalCaller(from, "conversations_list"); err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	conversations, err := a.store.SearchConversationsForAgent(from.ProjectID, from.AgentID, strings.TrimSpace(stringArg(args, "query")), limit)
	if err != nil {
		return nil, err
	}

	out := map[string]any{"conversations": conversations}
	if from.ThreadID == mainThreadID {
		// Agent-facing only: the conversation whose messages arrive in main.
		if id := a.store.mainConversationID(from.ProjectID, from.AgentID); id != "" {
			out["main_conversation_id"] = id
		}
	}
	return out, nil
}

func (a *App) toolHistory(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.requireParticipant(from, stringArg(args, "conversation_id"))
	if err != nil {
		return nil, err
	}
	messages, err := a.store.Transcript(conv.ID, int64(intArg(args, "since_id", 0)), intArg(args, "limit", 100))
	if err != nil {
		return nil, err
	}
	return map[string]any{"messages": messages}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────

// presentArg reports whether a tool argument carries a value; models often
// send empty arrays for optional fields they do not use.
func presentArg(v any) bool {
	if list, ok := v.([]any); ok {
		return len(list) > 0
	}
	return v != nil
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func intArg(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return fallback
}

func schemaObject(props map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func genericObjectsArg(args map[string]any, key string, max int) ([]map[string]any, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	var out []map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("%s must be an array of objects", key)
	}
	if len(out) > max {
		return nil, fmt.Errorf("%s supports at most %d entries", key, max)
	}
	return out, nil
}

func componentsArg(args map[string]any, key string) ([]Component, error) {
	objects, err := genericObjectsArg(args, key, 20)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(objects)
	if err != nil {
		return nil, err
	}
	var out []Component
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("invalid components: %w", err)
	}
	for _, c := range out {
		if strings.TrimSpace(c.App) == "" || strings.TrimSpace(c.Name) == "" {
			return nil, errors.New("each component requires app and name")
		}
		if c.Resource != nil {
			if err := c.Resource.Validate(); err != nil {
				return nil, fmt.Errorf("invalid component resource: %w", err)
			}
			if c.Interaction != nil {
				return nil, errors.New("interactive components are message-owned and cannot share a resource view")
			}
		}
	}
	return out, nil
}

func attachmentsArg(args map[string]any, key string) ([]Attachment, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out []Attachment
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("%s must be an array of attachments", key)
	}
	if len(out) > 10 {
		return nil, errors.New("attachments supports at most 10 entries")
	}
	for _, attachment := range out {
		if attachment.Ref != "" {
			if err := attachment.validateReferenceMetadata(); err != nil {
				return nil, err
			}
			continue
		}
		if attachment.Type != "image" || strings.TrimSpace(attachment.DataURL) == "" {
			return nil, fmt.Errorf("unsupported attachment type %q", attachment.Type)
		}
	}
	return out, nil
}

// Keep the model-facing contract aligned with approvalActionsArg.
func approvalActionsSchema() map[string]any {
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": 8,
		"description": "Optional custom choices. Omit for Approve/Deny. Each choice requires a unique id and label; use id, not value. The selected id is returned in approval.result. Without styles the first choice has an accent outline, the rest are neutral.",
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"id", "label"},
			"properties": map[string]any{
				"id":    map[string]any{"type": "string", "minLength": 1, "description": "Unique non-empty action identifier. pending and resolved are reserved."},
				"label": map[string]any{"type": "string", "minLength": 1, "description": "Human-readable choice, such as Delete repository or Keep repository."},
				"style": map[string]any{"type": "string", "enum": []string{"primary", "secondary", "danger"}, "description": "primary: filled theme accent; secondary: neutral outline; danger: accent outline for a consequential action."},
			},
		},
	}
}

func approvalActionsArg(args map[string]any) ([]approvalAction, error) {
	raw, ok := args["actions"]
	if !ok || raw == nil {
		return defaultApprovalActions(), nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var actions []approvalAction
	if err := json.Unmarshal(encoded, &actions); err != nil {
		return nil, errors.New("actions must be an array of {id,label,style?}")
	}
	if len(actions) == 0 || len(actions) > 8 {
		return nil, errors.New("actions must contain 1 to 8 entries")
	}
	seen := map[string]bool{}
	for i := range actions {
		actions[i].ID = strings.TrimSpace(actions[i].ID)
		actions[i].Label = strings.TrimSpace(actions[i].Label)
		if actions[i].ID == "" || actions[i].ID == "pending" || actions[i].ID == "resolved" || actions[i].Label == "" || seen[actions[i].ID] {
			return nil, errors.New("each action requires a unique id and non-empty label")
		}
		seen[actions[i].ID] = true
	}
	return actions, nil
}
