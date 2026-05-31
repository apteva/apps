package main

import (
	"sync"
	"sync/atomic"
)

type hub struct {
	mu   sync.RWMutex
	subs map[string]map[uint64]chan Message
	next atomic.Uint64
}

func newHub() *hub {
	return &hub{subs: make(map[string]map[uint64]chan Message)}
}

func (h *hub) hasSubscribers(chatID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[chatID]) > 0
}

func (h *hub) subscribe(chatID string) (chan Message, func()) {
	ch := make(chan Message, 64)
	id := h.next.Add(1)
	h.mu.Lock()
	if h.subs[chatID] == nil {
		h.subs[chatID] = make(map[uint64]chan Message)
	}
	h.subs[chatID][id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if m, ok := h.subs[chatID]; ok {
			if c, ok := m[id]; ok {
				close(c)
				delete(m, id)
			}
			if len(m) == 0 {
				delete(h.subs, chatID)
			}
		}
	}
}

func (h *hub) publish(m Message) {
	h.mu.RLock()
	subs := h.subs[m.ChatID]
	out := make([]chan Message, 0, len(subs))
	for _, ch := range subs {
		out = append(out, ch)
	}
	h.mu.RUnlock()
	for _, ch := range out {
		select {
		case ch <- m:
		default:
		}
	}
}
