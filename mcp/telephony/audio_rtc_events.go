package main

// Small paced browser event messages append only observations for both transports. They never replace
// call state, authorization, cumulative media measurements or browser ownership.
func mergeRTCEventHistory[T comparable](old, incoming []T, limit int) []T {
	out := make([]T, 0, min(limit, len(old)+len(incoming)))
	seen := map[T]bool{}
	for _, source := range [][]T{old, incoming} {
		for _, v := range source {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
func (t *audioCallTelemetry) observeRTCEventsConnection(w *websocketWriterPump, v browserAudioDiagnostics) {
	t.mu.Lock()
	defer t.mu.Unlock()
	network, ok := t.sockets[w]
	if !ok || network.ConnectionID != t.socket.ConnectionID {
		return
	}
	for i := range v.DropEvents {
		v.DropEvents[i].ConnectionID = t.connectionAtLocked(v.DropEvents[i].Timestamp)
	}
	for i := range v.SessionEvents {
		v.SessionEvents[i].ConnectionID = t.connectionAtLocked(v.SessionEvents[i].Timestamp)
	}
	t.browser.DropEvents = mergeRTCEventHistory(t.browser.DropEvents, v.DropEvents, 100)
	t.browser.SessionEvents = mergeRTCEventHistory(t.browser.SessionEvents, v.SessionEvents, 50)
	if v.Timing != nil {
		next := browserAudioTiming{}
		if t.browser.Timing != nil {
			next = *t.browser.Timing
		}
		next.Runtime = v.Timing.Runtime
		t.browser.Timing = &next
	}
	t.dirty = true
}
