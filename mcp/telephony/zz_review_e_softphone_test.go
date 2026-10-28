package main

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
)

func reviewECaptureFrame(sequence uint32, sentMS float64) []byte {
	frame := make([]byte, 32+960)
	binary.LittleEndian.PutUint32(frame, softphoneAudioFrameV2)
	binary.LittleEndian.PutUint32(frame[4:], sequence)
	binary.LittleEndian.PutUint64(frame[16:], math.Float64bits(sentMS))
	return frame
}

// The browser send clock and mediaClockMS drift apart. A lifetime minimum
// turned 100ppm into 720ms of "excess" after two hours, dropping every
// microphone frame as capture_transit_age once it passed 250ms.
func TestReviewECaptureTransitToleratesClockSkewOverTwoHours(t *testing.T) {
	now := 0.0
	previous := captureTransitClockMS
	captureTransitClockMS = func() float64 { return now }
	t.Cleanup(func() { captureTransitClockMS = previous })
	h := &softphoneHub{}
	sequence := uint32(0)
	for server := 1000.0; server <= 2*3600*1000; server += 200 {
		now = server
		// The browser clock runs 100ppm slow relative to the server's.
		if h.observeCaptureTiming(reviewECaptureFrame(sequence, 5000+server*(1-100e-6))) {
			t.Fatalf("frame at %.0fs dropped as stale; excess %.1fms", server/1000, h.captureTransitExcessMS)
		}
		sequence++
	}
	if h.captureTransitExcessMS > 10 || h.captureStaleBytes != 0 {
		t.Fatalf("skew accumulated: excess %.1fms stale %d", h.captureTransitExcessMS, h.captureStaleBytes)
	}
	// A sudden sustained delay is still detected.
	now += 600
	if !h.observeCaptureTiming(reviewECaptureFrame(sequence, 5000+(now-600)*(1-100e-6)-300)) {
		t.Fatal("sustained 300ms+ delay was not detected")
	}
	if h.captureDropEvents[len(h.captureDropEvents)-1].Reason != "capture_transit_age" {
		t.Fatal("wrong drop reason")
	}
}

// A reconnecting browser obtained hub H from hubFor while the old handler's
// cleanup cleared its browser and dropIfEmpty closed and removed H. The new
// socket then lived on an orphan and the carrier got a fresh hub.
func TestReviewEReconnectNeverAttachesToDroppedHub(t *testing.T) {
	for i := 0; i < 2000; i++ {
		r := &softphoneRegistry{}
		oldBrowser, newBrowser, peer := &websocketWriterPump{}, &websocketWriterPump{}, &websocketWriterPump{}
		old := r.attach("call", func(h *softphoneHub) { h.setBrowser(oldBrowser) })
		var wg sync.WaitGroup
		var attached *softphoneHub
		wg.Add(2)
		go func() {
			defer wg.Done()
			old.clearBrowser(oldBrowser)
			r.dropIfEmpty("call", old)
		}()
		go func() {
			defer wg.Done()
			attached = r.attach("call", func(h *softphoneHub) { h.setBrowser(newBrowser) })
		}()
		wg.Wait()
		carrier := r.attach("call", func(h *softphoneHub) { h.setPeer(peer) })
		attached.mu.Lock()
		closed, browser := attached.closed, attached.browser
		attached.mu.Unlock()
		if closed || browser != newBrowser || carrier != attached || r.lookup("call") != attached {
			t.Fatalf("iteration %d: browser on orphan hub (closed=%v, same carrier hub=%v)", i, closed, carrier == attached)
		}
	}
}
