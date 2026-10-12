package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Optional competing application work for the paced media benchmark. Uses
// real local recorders, database reads/writes and the notification outbox.
// Signaling, browser rendering and the external Apteva proxy remain separate
// integration tests; this harness does not claim full host capacity.
func startSIPMixedLoad(t *testing.T, count int) ([]*localCallRecorder, func() (int64, int64)) {
	if os.Getenv("TELEPHONY_SIP_MIXED_LOAD") != "1" {
		return make([]*localCallRecorder, count), func() (int64, int64) { return 0, 0 }
	}
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	recorders := make([]*localCallRecorder, count)
	rows := make([]*callRow, count)
	for i := range recorders {
		rows[i] = recoveryCall(t, a, fmt.Sprintf("mixed-%d", i), "telnyx")
		rows[i].RecordingMode = recordingModeAlways
		var err error
		recorders[i], err = a.newLocalRecording(rows[i], false)
		if err != nil {
			t.Fatal(err)
		}
		file := recorders[i].file.Name()
		t.Cleanup(func() { os.Remove(file); os.Remove(filepath.Dir(file)) })
	}
	var ops atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				for _, row := range rows {
					if ops.Load()%10 != 0 {
						break
					}
					a.observeCallNotification(row.ProjectID, row.ID, "telephony.audio.degraded", map[string]any{"reason": "load_fixture"})
				}
				if err := a.flushCallNotifications(t.Context()); err != nil {
					t.Error(err)
					return
				}
				w := httptest.NewRecorder()
				a.handleListCalls(w, httptest.NewRequest("GET", "/calls?project_id=project-a&limit=100", nil))
				if w.Code != 200 {
					t.Errorf("mixed call list: %d %s", w.Code, w.Body)
					return
				}
				ops.Add(1)
			}
		}
	}()
	return recorders, func() (int64, int64) {
		close(stop)
		wg.Wait()
		var dropped int64
		for _, r := range recorders {
			r.Close()
			dropped += r.dropped.Load()
		}
		return ops.Load(), dropped
	}
}
