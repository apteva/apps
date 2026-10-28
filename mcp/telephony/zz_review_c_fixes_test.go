package main

// Regressions for review batch C (carrier security, pacing, termination).
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestReviewCTwilioMediaSignatureUsesSplitMediaIngress(t *testing.T) {
	a := &App{runtime: runtimeTestActive()}
	a.runtime.endpoints = map[string]string{"callbacks": "https://voice.test/api", "carrier_media": "wss://voice.test"}
	signed := a.publicWSStreamURL("twilio", "call1", "sec")
	req := httptest.NewRequest(http.MethodGet, "/media/twilio/call1/sec", nil)
	if got := a.publicRequestURL(req); got != signed || got != "wss://voice.test/media/twilio/call1/sec" {
		t.Fatalf("signature URL %q, carrier was given %q", got, signed)
	}
	// Without a dedicated media endpoint the installed-app base still applies.
	b := &App{installID: 42}
	t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
	if got, want := b.publicRequestURL(req), b.publicWSStreamURL("twilio", "call1", "sec"); got != want {
		t.Fatalf("fallback signature URL %q != stream URL %q", got, want)
	}
}

func TestReviewCTelnyxSignatureWithoutAppContextDoesNotPanic(t *testing.T) {
	previous := globalCtx
	globalCtx = nil
	t.Cleanup(func() { globalCtx = previous })
	row := testCall("telnyx-nil", "in-progress")
	row.CarrierSlug = "telnyx"
	req := httptest.NewRequest(http.MethodPost, "/webhook/status/"+row.ID+"?token="+row.CallbackSecret, strings.NewReader(`{}`))
	if err := (&App{}).authorizeCallRequest(req, &row); err == nil {
		t.Fatal("unverifiable Telnyx callback was accepted")
	}
}

func TestReviewCRedactsSoftphoneMediaToken(t *testing.T) {
	got := redactURL("wss://agents.test/api/apps/telephony/_install/42/softphone/media/call-1/browser-token")
	if strings.Contains(got, "browser-token") || !strings.HasSuffix(got, "/softphone/media/call-1/REDACTED") {
		t.Fatalf("softphone token leaked: %s", got)
	}
	if got := redactURL("wss://agents.test/softphone/media/call-1/browser-token"); strings.Contains(got, "browser-token") {
		t.Fatalf("root softphone token leaked: %s", got)
	}
}

func TestReviewCPlivoClearAudioAddressesStream(t *testing.T) {
	clear := buildCarrierClear("plivo", "stream-9").(map[string]string)
	if clear["event"] != "clearAudio" || clear["streamId"] != "stream-9" {
		t.Fatalf("plivo clear = %#v", clear)
	}
}

type reviewCWrites struct {
	mu     sync.Mutex
	frames [][]int16
	at     []time.Time
}

func (w *reviewCWrites) l16(raw []byte) error {
	var frame struct {
		Event string `json:"event"`
		Media struct {
			Payload string `json:"payload"`
		} `json:"media"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Media.Payload == "" {
		return nil
	}
	data, _ := base64.StdEncoding.DecodeString(frame.Media.Payload)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, bytesToPCM16(data))
	w.at = append(w.at, time.Now())
	return nil
}

func (w *reviewCWrites) ulaw(raw []byte) error {
	var frame twilioOutbound
	if json.Unmarshal(raw, &frame) != nil || frame.Event != "media" {
		return nil
	}
	data, _ := base64.StdEncoding.DecodeString(frame.Media.Payload)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, ulawToPCM16(data))
	w.at = append(w.at, time.Now())
	return nil
}

func (w *reviewCWrites) snapshot() [][]int16 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]int16(nil), w.frames...)
}

func (w *reviewCWrites) waitFor(t *testing.T, n int) [][]int16 {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		if frames := w.snapshot(); len(frames) >= n {
			return frames
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("got %d carrier frames, want %d", len(w.snapshot()), n)
	return nil
}

func reviewCConstant(n int, value int16) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = value
	}
	return pcm
}

func traditionalAgentFixture() *agentAudioSession {
	s := continuousAgentFixture()
	s.profile.Mode = "traditional"
	return s
}

// The packetizer remainder of the last frame of a reply must reach the
// carrier without waiting for the next reply.
func TestReviewCJSONPacerFlushesIdleReplyTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := &reviewCWrites{}
	tail := &carrierPacketizerTail{packetizer: newCarrierAudioPacketizer(16000)}
	policy := bufferedCarrierPacerPolicy()
	policy.tail = tail
	p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "telnyx", "stream", false, newTwilioPlaybackTracker(), policy, writes.l16, nil, nil)
	tail.mu.Lock()
	tail.lastInputAt = time.Now()
	packets := tail.packetizer.add(reviewCConstant(480, 1000), 720, realtimeBridgeControl{ItemID: "reply", Generation: 1})
	_, _, err := p.enqueue(ctx, packets)
	tail.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	frames := writes.waitFor(t, 2)
	if len(frames[0]) != 320 || len(frames[1]) != 160 {
		t.Fatalf("frames %d/%d, want full packet then 10 ms tail", len(frames[0]), len(frames[1]))
	}
}

func TestReviewCJSONPacerFadesOnlyAroundUnderruns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := &reviewCWrites{}
	policy := carrierPacerPolicy{bufferMS: 20, maxQueueMS: 15000, agentSession: traditionalAgentFixture()}
	p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "telnyx", "stream", false, newTwilioPlaybackTracker(), policy, writes.l16, nil, nil)
	packet := func(n int) carrierPacedPacket {
		return carrierPacedPacket{PCM: reviewCConstant(n, 8000), ItemID: "a", Generation: 1}
	}
	if _, _, err := p.enqueue(ctx, []carrierPacedPacket{packet(320)}); err != nil {
		t.Fatal(err)
	}
	writes.waitFor(t, 1)
	time.Sleep(80 * time.Millisecond) // carrier lead runs dry
	if _, _, err := p.enqueue(ctx, []carrierPacedPacket{packet(320), packet(320), packet(100)}); err != nil {
		t.Fatal(err)
	}
	frames := writes.waitFor(t, 4)
	if frames[0][0] != 8000 {
		t.Fatal("first packet of a call was faded")
	}
	if frames[1][0] > 1000 || frames[1][200] != 8000 {
		t.Fatalf("speech after underrun not faded in: %d", frames[1][0])
	}
	if frames[2][0] != 8000 || frames[2][319] != 8000 || frames[1][319] != 8000 {
		t.Fatal("contiguous packet boundary was faded")
	}
	if last := frames[3]; len(last) != 100 || last[99] > 1000 || last[0] != 8000 {
		t.Fatalf("released partial tail not faded out: len=%d end=%d", len(last), last[len(last)-1])
	}
}

func TestReviewCTwilioPacerFadesOnlyAroundUnderruns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := &reviewCWrites{}
	policy := carrierPacerPolicy{bufferMS: 20, maxQueueMS: 15000, agentSession: traditionalAgentFixture()}
	p := newTwilioAudioPacerWithPolicy(ctx, "stream", newTwilioPlaybackTracker(), policy, writes.ulaw, nil)
	frame := realtimeBridgeControl{ItemID: "a", Generation: 1}
	if _, err := p.enqueue(ctx, []twilioAudioPacket{{PCM: reviewCConstant(160, 8000)}}, frame); err != nil {
		t.Fatal(err)
	}
	writes.waitFor(t, 1)
	time.Sleep(80 * time.Millisecond)
	if _, err := p.enqueue(ctx, twilioAudioPackets(reviewCConstant(370, 8000), 0, frame), frame); err != nil {
		t.Fatal(err)
	}
	frames := writes.waitFor(t, 4)
	if frames[0][0] < 7000 {
		t.Fatal("first packet of a call was faded")
	}
	if frames[1][0] > 1000 || frames[1][100] < 7000 {
		t.Fatalf("speech after underrun not faded in: %d", frames[1][0])
	}
	if frames[2][0] < 7000 || frames[1][159] < 7000 || frames[2][159] < 7000 {
		t.Fatal("contiguous packet boundary was faded")
	}
	if last := frames[3]; len(last) != 50 || last[49] > 1000 || last[0] < 7000 {
		t.Fatalf("released partial tail not faded out: len=%d", len(last))
	}
}

// A short tail followed promptly by its continuation is never faded.
func TestReviewCTwilioPacerKeepsContinuedTailUnfaded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := &reviewCWrites{}
	policy := carrierPacerPolicy{bufferMS: 200, maxQueueMS: 15000, agentSession: traditionalAgentFixture()}
	p := newTwilioAudioPacerWithPolicy(ctx, "stream", newTwilioPlaybackTracker(), policy, writes.ulaw, nil)
	frame := realtimeBridgeControl{ItemID: "a", Generation: 1}
	if _, err := p.enqueue(ctx, twilioAudioPackets(reviewCConstant(250, 8000), 0, frame), frame); err != nil {
		t.Fatal(err)
	}
	if _, err := p.enqueue(ctx, twilioAudioPackets(reviewCConstant(160, 8000), 0, frame), frame); err != nil {
		t.Fatal(err)
	}
	frames := writes.waitFor(t, 3)
	if len(frames[1]) != 90 || frames[1][89] < 7000 || frames[2][0] < 7000 {
		t.Fatal("contiguous short packet was faded")
	}
}

func TestReviewCNoClearCarrierLeadStaysSmall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := &reviewCWrites{}
	p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "vonage_binary", "", false, newTwilioPlaybackTracker(), bufferedCarrierPacerPolicy(), writes.l16, nil, nil)
	packets := make([]carrierPacedPacket, 50)
	for i := range packets {
		packets[i] = carrierPacedPacket{PCM: make([]int16, 320)}
	}
	if _, _, err := p.enqueue(ctx, packets); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if n := len(writes.snapshot()); n > 6 {
		t.Fatalf("Vonage has no remote clear but %d ms was sent ahead", n*20)
	}
	cleared, err := p.clear(ctx)
	if err != nil || cleared < 800 {
		t.Fatalf("local clear dropped only %d ms: %v", cleared, err)
	}
}

func TestReviewCEstimatedProgressWaitsForCarrierPlayout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progressAt := make(chan time.Time, 1)
	tracker := newTwilioPlaybackTracker()
	policy := carrierPacerPolicy{bufferMS: 20, maxQueueMS: 15000}
	p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "plivo", "stream", false, tracker, policy, func([]byte) error { return nil },
		func(twilioPlaybackProgress) error { progressAt <- time.Now(); return nil }, nil)
	started := time.Now()
	if _, _, err := p.enqueue(ctx, []carrierPacedPacket{{PCM: make([]int16, 320), ItemID: "a", Generation: 1, AudioEndMS: 20}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if remote, _ := tracker.affectedPlaybacks("a", 1); !remote {
		t.Fatal("estimated ack dropped audio the carrier may still be playing; interrupt would skip the remote clear")
	}
	select {
	case at := <-progressAt:
		if at.Sub(started) < carrierEstimatedPlaybackMargin {
			t.Fatalf("estimated progress after %s", at.Sub(started))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("estimated progress never delivered")
	}
}

func TestReviewCTerminationWithoutCarrierCallID(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	row.Direction, row.Status, row.CarrierSID = "outbound", "ringing", ""
	row.CarrierSlug, row.CarrierRequestID = "plivo", "request-1"
	if err := a.terminateCarrierCall(ctx, row); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "cancel_call_request" || platform.integrationCalls[0].Input["request_uuid"] != "request-1" {
		t.Fatalf("queued Plivo request not canceled: %+v", platform.integrationCalls)
	}
	row.CarrierSlug = "twilio"
	if err := a.terminateCarrierCall(ctx, row); !errors.Is(err, errCarrierCallIDUnavailable) {
		t.Fatalf("unterminated call reported %v", err)
	}
	row.CarrierRequestID = ""
	if err := a.terminateCarrierCall(ctx, row); err != nil {
		t.Fatalf("never-placed call: %v", err)
	}
}

func TestReviewCAmbiguousAnswerTimeoutKeepsClaim(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "media_confirms", false: "expires"}[confirmed], func(t *testing.T) {
			previousWait := carrierAnswerConfirmWait
			carrierAnswerConfirmWait = 300 * time.Millisecond
			if confirmed {
				carrierAnswerConfirmWait = 3 * time.Second
			}
			t.Cleanup(func() { carrierAnswerConfirmWait = previousWait })
			a, _, p, _, row, _ := reliabilityFixture(t)
			g, ctx, _ := activationGate(t, a, p)
			activationExec(t, a, `UPDATE calls SET status='answering',peer_token='peer-1',thread_id='human-thread',audio_bridge_url='' WHERE id=?`, row.ID)
			row = activationRow(t, a, row.ID)
			if confirmed {
				go func() {
					<-g.entered
					time.Sleep(250 * time.Millisecond) // after the command deadline
					_, _ = a.db().db.Exec(`UPDATE calls SET media_connected_at=?,media_status='connected' WHERE id=?`, ringTime(time.Now()), row.ID)
				}()
			}
			request, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			got, err := a.activateHumanCarrier(request, ctx, row)
			stored := activationRow(t, a, row.ID)
			if confirmed {
				if err != nil || got == nil || stored.Status != "answered" || stored.AudioBridgeURL == "pending" {
					t.Fatalf("carrier-answered call reset: err=%v status=%s bridge=%s", err, stored.Status, stored.AudioBridgeURL)
				}
				return
			}
			if err == nil || stored.Status != "pending" {
				t.Fatalf("unconfirmed ambiguous answer did not expire: err=%v status=%s", err, stored.Status)
			}
		})
	}
}

func TestReviewCVonageEventFromOtherLegRejected(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := testCall("vonage-leg", "in-progress")
	row.CarrierSlug, row.CarrierSID = "vonage", "leg-a"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook/status/"+row.ID+"?token="+row.CallbackSecret, strings.NewReader(`{"uuid":"leg-b","status":"completed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.handleStatusCallback(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Vonage leg accepted: %d", rec.Code)
	}
	if stored, _ := a.db().findCall(row.ID); stored.Status != "in-progress" {
		t.Fatalf("foreign leg changed status to %s", stored.Status)
	}
}

func TestReviewCPlivoAnswerDoesNotRebindCarrierSID(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "plivo", Fields: map[string]string{"password": "plivo-token"}}})
	row := testCall("plivo-rebind", "ringing")
	row.CarrierSlug, row.CarrierSID = "plivo", "uuid-a"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"CallUUID": {"uuid-b"}, "CallStatus": {"in-progress"}}
	req := httptest.NewRequest(http.MethodPost, "/xml/plivo/"+row.ID+"?token="+row.CallbackSecret, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signPlivoTestRequest(a, req, form, "plivo-token")
	rec := httptest.NewRecorder()
	a.handlePlivoXML(rec, req)
	if stored, _ := a.db().findCall(row.ID); rec.Code != http.StatusForbidden || stored.CarrierSID != "uuid-a" {
		t.Fatalf("signed foreign CallUUID rebound call: %d %s", rec.Code, stored.CarrierSID)
	}
}
