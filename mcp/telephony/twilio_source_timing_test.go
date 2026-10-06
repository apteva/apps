package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Real Twilio JSON -> G.711 decoder/resampler -> authenticated loopback peer
// -> browser socket. Advance the source clock mapping to simulate ten seconds
// of upstream delivery delay without sleeping inside the unit suite.
func TestTwilioHumanSourceTimingThroughActualBridge(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://public.example.test")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&answerPlatform{}))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	a := &App{installID: 42}
	row := insertSoftphoneCall(t, a, "in-progress")
	peerServer := softphoneTestServer(t, a)
	u, _ := url.Parse(peerServer.URL)
	t.Setenv("APTEVA_APP_PORT", u.Port())
	browser := dialWS(t, peerServer.URL+"/softphone/media/"+row.ID+"/"+row.PeerToken)
	wsutil.WriteClientText(browser, []byte(`{"type":"media.capabilities","version":2,"versions":[3,2]}`))
	browser.SetReadDeadline(time.Now().Add(time.Second))
	for {
		data, op, err := wsutil.ReadServerData(browser)
		if err != nil {
			t.Fatal(err)
		}
		var ack map[string]any
		if op == ws.OpText && json.Unmarshal(data, &ack) == nil && ack["type"] == "media.capabilities" {
			if ack["version"] != float64(3) {
				t.Fatal("APT3 not negotiated")
			}
			break
		}
	}
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); a.handleTwilioMediaStream(w, r) }))
	t.Cleanup(server.Close)
	path := "/media/twilio/" + row.ID + "/" + row.CallbackSecret
	request := httptest.NewRequest("GET", path, nil)
	sig := twilioTestSignature(a.publicRequestURL(request), url.Values{}, "test-auth-token")
	dialer := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"X-Twilio-Signature": {sig}})}
	carrier, _, _, err := dialer.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		carrier.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Twilio handler not drained")
		}
	}()
	start, _ := json.Marshal(map[string]any{"event": "start", "streamSid": "source-stream", "start": map[string]string{"callSid": "CA-source"}})
	if err := wsutil.WriteClientText(carrier, start); err != nil {
		t.Fatal(err)
	}
	pcm := pcm16ToUlaw(sinePCM(8000, 440, 160))
	send := func(n int) {
		frame, _ := json.Marshal(map[string]any{"event": "media", "sequenceNumber": strconv.Itoa(n + 2), "streamSid": "source-stream", "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(pcm), "timestamp": strconv.Itoa(n * 20), "chunk": strconv.Itoa(n + 1), "track": "inbound"}})
		if err := wsutil.WriteClientText(carrier, frame); err != nil {
			t.Fatal(err)
		}
	}
	send(0)
	first := readBinaryWithin(t, browser, time.Second)
	if sourceAudioHeader(first) != 64 || binary.LittleEndian.Uint64(first[40:]) != 1 {
		t.Fatal("Twilio metadata missing at browser")
	}
	hub := a.softphones.hubFor(row.ID)
	hub.reception.mu.Lock()
	hub.reception.base -= 10000
	hub.reception.mu.Unlock()
	for n := 1; n <= 500; n++ {
		send(n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for hub.reception.snapshot(mediaClockMS(), false).Frames < 501 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := hub.reception.snapshot(mediaClockMS(), false)
	if s.Frames != 501 || s.StaleDroppedMS < 9600 || s.SourceSequence != 501 {
		t.Fatalf("Twilio catchup bypassed source guard: %+v", s)
	}
	recent := readBinaryWithin(t, browser, time.Second)
	if sourceAudioHeader(recent) != 64 {
		t.Fatal("source header lost in hub")
	}
	if ts := math.Float64frombits(binary.LittleEndian.Uint64(recent[32:])); ts < 9600 {
		t.Fatalf("old speech forwarded: timestamp=%v", ts)
	}
	if rmsPCM(bytesToPCM16(recent[64:])) < 5000 {
		t.Fatal("fresh caller speech changed/attenuated")
	}
	// Healthy operator direction continues after the caller-side catchup guard.
	if err := wsutil.WriteClientBinary(browser, pcm16ToBytes(sinePCM(24000, 900, 480))); err != nil {
		t.Fatal(err)
	}
	carrier.SetReadDeadline(time.Now().Add(time.Second))
	data, _, err := wsutil.ReadServerData(carrier)
	if err != nil {
		t.Fatal(err)
	}
	var media twilioFrame
	if json.Unmarshal(data, &media) != nil || media.Event != "media" || media.Media == nil {
		t.Fatalf("operator audio interrupted: %s", data)
	}
}
