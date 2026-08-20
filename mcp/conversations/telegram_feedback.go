package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// telegramFeedbackManager owns ephemeral response UX only. Nothing here is
// persisted: the ordinary Telegram delivery ledger remains the sole source of
// truth for the final message.
type telegramFeedbackManager struct {
	app *App

	mu       sync.Mutex
	sessions map[string]*telegramFeedbackSession // binding id -> active response
	nextID   int64

	typingEvery time.Duration
	draftEvery  time.Duration
	flushEvery  time.Duration
	timeout     time.Duration
}

type telegramFeedbackSession struct {
	manager      *telegramFeedbackManager
	app          *sdk.AppCtx
	binding      TelegramBinding
	mode         string
	draftID      int64
	cancel       context.CancelFunc
	done         chan struct{}
	updates      chan string
	warned       bool
	draftStarted bool
	lastDraft    string
	latestDraft  string
}

func newTelegramFeedbackManager(app *App) *telegramFeedbackManager {
	return &telegramFeedbackManager{
		app: app, sessions: map[string]*telegramFeedbackSession{}, nextID: time.Now().UnixNano() & 0x7fffffff,
		typingEvery: 4 * time.Second, draftEvery: 20 * time.Second,
		flushEvery: 750 * time.Millisecond, timeout: 2 * time.Minute,
	}
}

func (m *telegramFeedbackManager) Start(app *sdk.AppCtx, cfg *TelegramConnectionConfig, binding *TelegramBinding) {
	if m == nil || m.app == nil || app == nil || cfg == nil || binding == nil {
		return
	}
	mode, err := normalizeTelegramFeedback(cfg.ResponseFeedback)
	if err != nil || mode == telegramFeedbackOff {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if previous := m.sessions[binding.ID]; previous != nil {
		previous.cancel()
	}
	m.nextID++
	if m.nextID <= 0 {
		m.nextID = 1
	}
	session := &telegramFeedbackSession{
		manager: m, app: app.WithProject(binding.ProjectID), binding: *binding,
		mode: mode, draftID: m.nextID, cancel: cancel,
		done: make(chan struct{}), updates: make(chan string, 1),
	}
	m.sessions[binding.ID] = session
	m.mu.Unlock()

	// Telegram's ordinary typing state is the immediate acknowledgement. An
	// empty native draft renders as a ghost message in some clients, so the
	// first draft waits for meaningful answer text from a tool chunk.
	session.sendTyping()
	go session.run(ctx)
}

func (m *telegramFeedbackManager) OnFrame(frame StreamFrame) {
	if m == nil || frame.ConversationID == "" || frame.Text == "" || frame.Done {
		return
	}
	m.mu.Lock()
	sessions := make([]*telegramFeedbackSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session.binding.ConversationID == frame.ConversationID && session.canDraft() {
			sessions = append(sessions, session)
		}
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.offer(frame.Text)
	}
}

func (m *telegramFeedbackManager) CompleteBinding(bindingID string) {
	if m == nil || bindingID == "" {
		return
	}
	m.mu.Lock()
	session := m.sessions[bindingID]
	if session != nil {
		delete(m.sessions, bindingID)
		session.cancel()
	}
	m.mu.Unlock()
	if session != nil {
		select {
		case <-session.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (m *telegramFeedbackManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	sessions := make([]*telegramFeedbackSession, 0, len(m.sessions))
	for id, session := range m.sessions {
		delete(m.sessions, id)
		session.cancel()
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		select {
		case <-session.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *telegramFeedbackSession) run(ctx context.Context) {
	typing := time.NewTicker(s.manager.typingEvery)
	drafts := time.NewTicker(s.manager.draftEvery)
	flush := time.NewTicker(s.manager.flushEvery)
	timeout := time.NewTimer(s.manager.timeout)
	defer func() {
		typing.Stop()
		drafts.Stop()
		flush.Stop()
		timeout.Stop()
		s.manager.mu.Lock()
		if s.manager.sessions[s.binding.ID] == s {
			delete(s.manager.sessions, s.binding.ID)
		}
		s.manager.mu.Unlock()
		close(s.done)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout.C:
			return
		case text := <-s.updates:
			s.latestDraft = text
			if s.canDraft() && !s.draftStarted {
				s.draftStarted = true
				s.sendDraft(text)
			}
		case <-typing.C:
			s.sendTyping()
		case <-drafts.C:
			if s.canDraft() && s.latestDraft != "" {
				s.sendDraft(s.latestDraft)
			}
		case <-flush.C:
			if s.canDraft() && s.latestDraft != "" && s.latestDraft != s.lastDraft {
				s.sendDraft(s.latestDraft)
			}
		}
	}
}

func (s *telegramFeedbackSession) canDraft() bool {
	return s != nil && s.mode == telegramFeedbackLive && s.binding.ChatType == "private"
}

func (s *telegramFeedbackSession) offer(text string) {
	text = telegramTextLimit(text)
	if text == "" {
		return
	}
	select {
	case s.updates <- text:
	default:
		select {
		case <-s.updates:
		default:
		}
		select {
		case s.updates <- text:
		default:
		}
	}
}

func (s *telegramFeedbackSession) sendTyping() {
	_, err := s.manager.app.executeTelegram(s.app, s.binding.ConnectionID, "send_chat_action", map[string]any{
		"chat_id": s.binding.ChatID, "action": "typing",
	})
	s.warn(err)
}

func (s *telegramFeedbackSession) sendDraft(text string) {
	text = telegramTextLimit(text)
	if text == "" {
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(s.binding.ChatID), 10, 64)
	if err != nil || chatID <= 0 {
		return
	}
	_, err = s.manager.app.executeTelegram(s.app, s.binding.ConnectionID, "send_message_draft", map[string]any{
		"chat_id": chatID, "draft_id": s.draftID, "text": text,
	})
	if err == nil {
		s.lastDraft = text
	}
	s.warn(err)
}

func (s *telegramFeedbackSession) warn(err error) {
	if err == nil || s.warned || s.app == nil {
		return
	}
	s.warned = true
	s.app.Logger().Warn("Telegram response feedback unavailable; final delivery will continue",
		"binding_id", s.binding.ID, "err", err)
}
