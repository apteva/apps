package main

// hub.go — in-process pub/sub behind the SSE stream. Same two-scope
// shape as channel-chat's hub: per-conversation subscribers drive the
// open chat panel, per-user subscribers drive the global bell/tray.
// The hub only carries forward motion; reconnects backfill from the
// store via ?since=.

import "sync"

type hub struct {
	mu     sync.RWMutex
	nextID uint64
	byConv map[string]map[uint64]chan Message
	byUser map[string]map[uint64]chan Message
	// frames — ephemeral streaming-bubble subscribers, per
	// conversation. Deliberately a separate scope from messages: frames
	// are never persisted, never backfilled, and a slow subscriber
	// drops them without consequence.
	frames map[string]map[uint64]chan StreamFrame
}

func newHub() *hub {
	return &hub{
		byConv: map[string]map[uint64]chan Message{},
		byUser: map[string]map[uint64]chan Message{},
		frames: map[string]map[uint64]chan StreamFrame{},
	}
}

func (h *hub) subscribeFrames(conversationID string) (<-chan StreamFrame, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan StreamFrame, 64)
	if h.frames[conversationID] == nil {
		h.frames[conversationID] = map[uint64]chan StreamFrame{}
	}
	h.frames[conversationID][id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subs, ok := h.frames[conversationID]; ok {
			delete(subs, id)
			if len(subs) == 0 {
				delete(h.frames, conversationID)
			}
		}
	}
}

func (h *hub) publishStream(f StreamFrame) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.frames[f.ConversationID] {
		select {
		case ch <- f:
		default: // ephemeral: drop, never block
		}
	}
}

func (h *hub) subscribeConversation(conversationID string) (<-chan Message, func()) {
	return h.subscribe(h.byConv, conversationID)
}

func (h *hub) subscribeUser(userID string) (<-chan Message, func()) {
	return h.subscribe(h.byUser, userID)
}

func (h *hub) subscribe(scope map[string]map[uint64]chan Message, key string) (<-chan Message, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan Message, 64)
	if scope[key] == nil {
		scope[key] = map[uint64]chan Message{}
	}
	scope[key][id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subs, ok := scope[key]; ok {
			delete(subs, id)
			if len(subs) == 0 {
				delete(scope, key)
			}
		}
	}
}

func (h *hub) publish(conversationID string, m Message) {
	h.fanOut(h.byConv, conversationID, m)
}

func (h *hub) publishToUser(userID string, m Message) {
	h.fanOut(h.byUser, userID, m)
}

// publishBroadcast reaches every user-scope subscriber regardless of
// key — inbox items are operator-facing whoever owns the source
// conversation.
func (h *hub) publishBroadcast(m Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, subs := range h.byUser {
		for _, ch := range subs {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

func (h *hub) fanOut(scope map[string]map[uint64]chan Message, key string, m Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range scope[key] {
		select {
		case ch <- m:
		default:
			// Slow subscriber: drop rather than block the writer. The
			// client's ?since= backfill reconciles the gap on reconnect.
		}
	}
}

// watching reports whether anyone has the conversation open — the
// IsActive gate agents (later, the server relay) can consult.
func (h *hub) watching(conversationID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byConv[conversationID]) > 0
}
