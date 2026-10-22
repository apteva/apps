package main

import (
	"testing"
)

func TestTURNRenewalCompletionRequiresNewPathAndAllActiveMediaDirections(t *testing.T) {
	before := rtcRestartMediaProgress{pair: "old", sent: 10, received: 20, send: true, receive: true}
	for _, v := range []rtcRestartMediaProgress{
		{pair: "old", sent: 11, received: 21},
		{pair: "new", sent: 10, received: 20},
		{pair: "new", sent: 11, received: 20},
		{pair: "new", sent: 10, received: 21},
	} {
		if rtcRestartMediaResumed(before, v) {
			t.Fatal("signaling or one-way media was called success", v)
		}
	}
	if !rtcRestartMediaResumed(before, rtcRestartMediaProgress{pair: "new", sent: 11, received: 21}) {
		t.Fatal("resumed media not recognized")
	}
	listen := rtcRestartMediaProgress{pair: "old", sent: 10, send: true}
	if !rtcRestartMediaResumed(listen, rtcRestartMediaProgress{pair: "new", sent: 11}) {
		t.Fatal("receive-only browser requires nonexistent microphone")
	}
	coach := rtcRestartMediaProgress{pair: "old", received: 10, receive: true}
	if !rtcRestartMediaResumed(coach, rtcRestartMediaProgress{pair: "new", received: 11}) {
		t.Fatal("send-only coaching requires nonexistent playback")
	}
	stats := &rtcMediaStats{}
	stats.rtp.attempts.Store(12)
	stats.rtp.errors.Store(1)
	stats.ingress.packets.Store(21)
	p := rtcRestartProgress("new", stats)

	if !rtcRestartMediaResumed(before, p) {
		t.Fatal("actual Pion stats fields ignored", p)
	}
}
