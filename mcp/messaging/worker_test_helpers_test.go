package main

import (
	"net/http"
	"net/http/httptest"
)

// Existing end-to-end handler fixtures include one deterministic worker tick.
// Durability tests call the real webhook directly to inspect pre-worker state.
func (a *App) handleInboundAndProcessForTest(w *httptest.ResponseRecorder, r *http.Request) {
	a.handleInboundWebhook(w, r)
	_ = a.retryMessagingWork(globalCtx)
}
func (a *App) handleTwilioInboundAndProcessForTest(w *httptest.ResponseRecorder, r *http.Request) {
	a.handleTwilioInboundWebhook(w, r)
	_ = a.retryMessagingWork(globalCtx)
}
