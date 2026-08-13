package stability

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
)

func TestTrackerAccountsForSessionNetworkAndNavigation(t *testing.T) {
	tracker := &Tracker{
		ctx: context.Background(), inflight: make(map[network.RequestID]struct{}),
		frames: make(map[cdp.FrameID]struct{}), lastActivity: time.Now().Add(-time.Second),
	}
	requestID := network.RequestID("request-1")
	frameID := cdp.FrameID("frame-1")
	tracker.observe(&network.EventRequestWillBeSent{RequestID: requestID, Type: network.ResourceTypeXHR})
	tracker.observe(&page.EventFrameStartedLoading{FrameID: frameID})
	if len(tracker.inflight) != 1 || len(tracker.frames) != 1 {
		t.Fatalf("started activity not tracked: requests=%d frames=%d", len(tracker.inflight), len(tracker.frames))
	}
	tracker.observe(&network.EventLoadingFinished{RequestID: requestID})
	tracker.observe(&page.EventFrameStoppedLoading{FrameID: frameID})
	if len(tracker.inflight) != 0 || len(tracker.frames) != 0 {
		t.Fatalf("completed activity not cleared: requests=%d frames=%d", len(tracker.inflight), len(tracker.frames))
	}

	// Long-lived transports must not make stable-state impossible forever.
	tracker.observe(&network.EventRequestWillBeSent{RequestID: "socket", Type: network.ResourceTypeWebSocket})
	tracker.observe(&network.EventRequestWillBeSent{RequestID: "events", Type: network.ResourceTypeEventSource})
	if len(tracker.inflight) != 0 {
		t.Fatalf("long-lived requests should be ignored: %v", tracker.inflight)
	}
}
