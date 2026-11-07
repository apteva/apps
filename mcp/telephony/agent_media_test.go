package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/gobwas/ws"
	"testing"
	"time"
)

func TestAgentMediaNativeAndDefault(t *testing.T) {
	for _, format := range []string{"pcm16", "g711_ulaw", "g711_alaw"} {
		rate := 24000
		if format != "pcm16" {
			rate = 8000
		}
		s := &agentAudioSession{profile: agentAudioProfile{Format: format, Preprocessing: "analysis_only"}, capabilities: agentAudioCapabilities{Format: format, SampleRate: rate, Channels: 1, Continuous: true}}
		if e := s.validateMedia(); e != nil {
			t.Fatal(e)
		}
		if s.pcm24Samples(s.byteRate()/50) != 480 {
			t.Fatal("20ms duration mismatch")
		}
		input := newAgentMediaConverter(s, 8000, true)
		output := newAgentMediaConverter(s, 8000, false)
		source := bytes.Repeat([]byte{0x19}, 160)
		pcm := ulawToPCM16(source)
		raw := input.input(pcm, source, "g711_ulaw")
		if format != "pcm16" && len(raw) != s.byteRate()/50 {
			t.Fatal("wrong frame size", format, len(raw))
		}
		if format == "g711_ulaw" && !bytes.Equal(raw, source) {
			t.Fatal("mu-law pass-through changed bytes")
		}
		if got := output.output(raw); len(got) == 0 || len(got) > 160 {
			t.Fatal("output duration", format, len(got))
		}
		l := &agentCoreLink{byteRate: s.byteRate()}
		l.discard(agentLinkMessage{op: ws.OpBinary, data: make([]byte, s.byteRate()/50)})
		if l.snapshot().InputDiscardedMS != 20 {
			t.Fatal("incorrect discard duration")
		}
		s.capabilities.SampleRate = 123
		if s.validateMedia() == nil {
			t.Fatal("bad rate accepted")
		}
	}
	legacy := &agentAudioSession{}
	if legacy.validateMedia() != nil || legacy.byteRate() != 48000 {
		t.Fatal("legacy default changed")
	}
	legacy.profile.Format = "g711_ulaw"
	if legacy.validateMedia() == nil {
		t.Fatal("legacy Core silently accepted native format")
	}
	for _, p := range []agentAudioProfile{{Format: "opus"}, {Transport: "sip"}} {
		if _, e := resolveAgentAudioProfile(p); e == nil {
			t.Fatal("unsupported profile accepted")
		}
	}
}
func TestAgentMediaTwilioPacerPreservesNativeBytes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	written := make(chan []byte, 8)
	tracker := newTwilioPlaybackTracker()
	p := newTwilioAudioPacer(ctx, "fixture", tracker, func(b []byte) error { written <- b; return nil }, func(e error) { t.Error(e) })
	// Include both mu-law zero representations: decode/re-encode loses one.
	raw := make([]byte, 160)
	for i := range raw {
		raw[i] = byte(i)
	}
	raw[0] = 0x7f
	packets := twilioAudioPackets(ulawToPCM16(raw), 480, realtimeBridgeControl{ItemID: "reply", AudioEndMS: 20})
	packets[0].Encoded = raw
	if _, e := p.enqueue(ctx, packets, realtimeBridgeControl{ItemID: "reply", AudioEndMS: 20}); e != nil {
		t.Fatal(e)
	}
	select {
	case b := <-written:
		var frame twilioOutbound
		_ = json.Unmarshal(b, &frame)
		got, _ := base64.StdEncoding.DecodeString(frame.Media.Payload)
		if !bytes.Equal(got, raw) {
			t.Fatal("native bytes were transcoded")
		}
	case <-time.After(time.Second):
		t.Fatal("pacer stalled")
	}
}

func TestAgentMediaProfileFrozenBeforeOutboundSpawn(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	for _, direction := range []string{"inbound", "outbound"} {
		row := testCall("native-"+direction, "initiated")
		row.Direction = direction
		row.ProjectID = ctx.CurrentProject()
		row.AgentID = 7
		row.PeerKind = peerKindRealtime
		if _, e := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"scope": "agent/7", "config": map[string]any{"transport": "websocket", "format": "g711_ulaw"}}); e != nil {
			t.Fatal(e)
		}
		selected, e := a.resolveAgentAudioForCall(&row)
		if e != nil {
			t.Fatal(e)
		}
		row.preparedAgentAudio = &selected
		// A concurrent profile edit between Core spawn and carrier placement must
		// not cause Telephony to interpret the already-started session differently.
		if _, e = a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"scope": "agent/7", "config": map[string]any{"transport": "webrtc", "format": "pcm16"}}); e != nil {
			t.Fatal(e)
		}
		if e = a.db().insertCall(row); e != nil {
			t.Fatal(e)
		}
		pinned, e := a.pinAgentAudioProfile(&row)
		if e != nil || pinned.Format != "g711_ulaw" || pinned.Transport != "websocket" {
			t.Fatal("spawn contract changed", pinned, e)
		}
		reloaded, e := a.db().findCall(row.ID)
		if e != nil {
			t.Fatal(e)
		}
		pinned, e = a.pinAgentAudioProfile(reloaded)
		if e != nil || pinned != selected {
			t.Fatal("reconnect lost format", pinned, e)
		}
	}
}
