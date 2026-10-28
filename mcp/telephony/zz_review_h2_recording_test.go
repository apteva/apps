package main

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

func reviewH2Recorder(t *testing.T, id string) (*App, *localCallRecorder, string) {
	t.Helper()
	a, _, _ := localSpeechFixture(t)
	call := callRow{ID: id, ProjectID: "project-a", Direction: "inbound", Status: "answered", RecordingMode: recordingModeAlways, RecordingChannels: "dual", RecordingStorageMode: recordingStorageProvider}
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	r, err := a.newLocalRecording(&call, false)
	if err != nil {
		t.Fatal(err)
	}
	path := r.file.Name()
	t.Cleanup(func() { os.Remove(path) })
	return a, r, path
}

func reviewH2Frame(v int16) []int16 {
	p := make([]int16, 480)
	for i := range p {
		p[i] = v
	}
	return p
}

// Sample index of the first occurrence of v on channel ch, or -1.
func reviewH2Find(b []byte, ch int, v int16) int {
	for s := 0; 44+s*4+ch*2+2 <= len(b); s++ {
		if int16(binary.LittleEndian.Uint16(b[44+s*4+ch*2:])) == v {
			return s
		}
	}
	return -1
}

// skip(i) drops caller frame i; late(i) reports the caller frame's arrival offset.
func reviewH2Capture(t *testing.T, id string, late func(i int) (time.Duration, bool)) []byte {
	t.Helper()
	_, r, path := reviewH2Recorder(t, id)
	base := r.started
	for i := 0; i < 100; i++ {
		on := time.Duration(i*20) * time.Millisecond
		r.queue <- localRecordingFrame{1, reviewH2Frame(int16(i + 1)), base.Add(on)}
		if at, ok := late(i); ok {
			r.queue <- localRecordingFrame{0, reviewH2Frame(int16(i + 1)), base.Add(at)}
		}
	}
	r.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReviewH2LocalRecordingStallBurstStaysAligned(t *testing.T) {
	// Caller frames 25..39 (300 ms) stall and arrive together at t=800 ms.
	b := reviewH2Capture(t, "h2-burst", func(i int) (time.Duration, bool) {
		if i >= 25 && i < 40 {
			return 800 * time.Millisecond, true
		}
		return time.Duration(i*20) * time.Millisecond, true
	})
	for _, marker := range []int16{26, 33, 40, 41, 91} {
		c, ag := reviewH2Find(b, 0, marker), reviewH2Find(b, 1, marker)
		if c < 0 || ag < 0 || c != ag {
			t.Fatalf("frame %d caller@%d agent@%d samples (offset %d ms)", marker-1, c, ag, (c-ag)/24)
		}
	}
}

func TestReviewH2LocalRecordingRealGapKeepsWallTime(t *testing.T) {
	// Caller is silent for frames 25..39 and resumes on time.
	b := reviewH2Capture(t, "h2-gap", func(i int) (time.Duration, bool) {
		return time.Duration(i*20) * time.Millisecond, i < 25 || i >= 40
	})
	for _, marker := range []int16{25, 41, 91} {
		c, ag := reviewH2Find(b, 0, marker), reviewH2Find(b, 1, marker)
		if c < 0 || c != ag {
			t.Fatalf("frame %d caller@%d agent@%d samples", marker-1, c, ag)
		}
	}
	for s := 25 * 480; s < 40*480; s++ {
		if v := int16(binary.LittleEndian.Uint16(b[44+s*4:])); v != 0 {
			t.Fatalf("silence filled at sample %d: %d", s, v)
		}
	}
}

func TestReviewH2LocalRecordingWriteFailureKeepsPrimaryReason(t *testing.T) {
	a, r, _ := reviewH2Recorder(t, "h2-write-failure")
	r.file.Close()
	r.dropped.Store(40)
	r.Record(0, make([]int16, 480))
	r.Close()
	row, err := a.db().findRecording(r.row.ID)
	if err != nil || row.ProviderStatus != "failed" {
		t.Fatalf("recording=%+v err=%v", row, err)
	}
	if !strings.HasPrefix(row.LastError, "local recording write failed") || !strings.Contains(row.LastError, "40 ms omitted") {
		t.Fatalf("last_error=%q", row.LastError)
	}
}
