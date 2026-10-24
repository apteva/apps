package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestAgentCoreLinkThreeFailuresKeepRendererAlive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var connections atomic.Int64
	payload := pcm16ToBytes([]int16{23, -1500, 3200, -22})
	newRemote := func() (net.Conn, error) {
		c, p := net.Pipe()
		n := connections.Add(1)
		go func() {
			defer p.Close()
			ready, _, err := wsutil.ReadClientData(p)
			if err != nil {
				return
			}
			if !bytes.Contains(ready, []byte("playback.ready")) {
				return
			}
			if n <= 3 {
				_ = wsutil.WriteServerMessage(p, ws.OpClose, ws.NewCloseFrameBody(1011, "live audio input backpressure"))
				return
			}
			_ = wsutil.WriteServerMessage(p, ws.OpText, []byte(`{"type":"audio.frame","item_id":"next","audio_end_ms":20}`))
			_ = wsutil.WriteServerMessage(p, ws.OpBinary, payload)
			data, op, err := wsutil.ReadClientData(p)
			if err == nil {
				_ = wsutil.WriteServerMessage(p, op, data)
			}
		}()
		return c, nil
	}
	first, _ := newRemote()
	link := newAgentCoreLink(ctx, first, func(context.Context) (net.Conn, error) { return newRemote() }, nil)
	defer link.Close()
	if err := wsutil.WriteClientMessage(link, ws.OpText, []byte(`{"type":"playback.ready"}`)); err != nil {
		t.Fatal(err)
	}
	data, op, err := wsutil.ReadServerData(link)
	if err != nil || op != ws.OpText || !bytes.Contains(data, []byte("next")) {
		t.Fatalf("metadata: %s %v", data, err)
	}
	data, op, err = wsutil.ReadServerData(link)
	if err != nil || op != ws.OpBinary || !bytes.Equal(data, payload) {
		t.Fatalf("output changed: %v", err)
	}
	if err = wsutil.WriteClientMessage(link, ws.OpBinary, payload); err != nil {
		t.Fatal(err)
	}
	data, _, err = wsutil.ReadServerData(link)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("input changed: %v", err)
	}
	s := link.snapshot()
	if s.Reconnects != 3 || s.InputDiscardedMS != 0 || connections.Load() != 4 {
		t.Fatal(s, connections.Load())
	}
}

func TestAgentCoreLinkNeverReplaysPartialOutputOrOldInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c, p := net.Pipe()
	lost := make(chan struct{})
	release := make(chan struct{})
	go func() {
		defer p.Close()
		_, _, _ = wsutil.ReadClientData(p)
		_ = wsutil.WriteServerMessage(p, ws.OpText, []byte(`{"type":"audio.frame","item_id":"old"}`))
		_ = wsutil.WriteServerMessage(p, ws.OpClose, ws.NewCloseFrameBody(1011, "retry"))
		close(lost)
	}()
	c2, p2 := net.Pipe()
	defer p2.Close()
	go func() {
		defer p2.Close()
		_, _, _ = wsutil.ReadClientData(p2)
		_ = wsutil.WriteServerMessage(p2, ws.OpText, []byte(`{"type":"audio.frame","item_id":"fresh"}`))
		_ = wsutil.WriteServerMessage(p2, ws.OpBinary, []byte{1, 0})
		<-ctx.Done()
	}()
	link := newAgentCoreLink(ctx, c, func(ctx context.Context) (net.Conn, error) {
		select {
		case <-release:
			return c2, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, nil)
	defer link.Close()
	_ = wsutil.WriteClientMessage(link, ws.OpText, []byte(`{"type":"playback.ready"}`))
	<-lost
	for i := 0; i < 20; i++ {
		if err := wsutil.WriteClientMessage(link, ws.OpBinary, make([]byte, 960)); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	data, op, err := wsutil.ReadServerData(link)
	if err != nil || op != ws.OpText || !bytes.Contains(data, []byte("fresh")) {
		t.Fatalf("stale metadata replayed: %s %v", data, err)
	}
	if state := link.snapshot(); state.InputDiscardedMS+state.AmbiguousInputMS != 400 {
		t.Fatal(link.snapshot())
	}
}

func TestAgentCoreLinkNormalCloseAndPolicyAreNotRetried(t *testing.T) {
	for _, code := range []ws.StatusCode{1000, 1007, 1008} {
		t.Run(strconv.Itoa(int(code)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c, p := net.Pipe()
			go func() {
				defer p.Close()
				_ = wsutil.WriteServerMessage(p, ws.OpClose, ws.NewCloseFrameBody(code, "ended"))
			}()
			var called atomic.Bool
			link := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { called.Store(true); return nil, io.EOF }, nil)
			defer link.Close()
			_, _, err := wsutil.ReadServerData(link)
			if err == nil || called.Load() {
				t.Fatal(err, called.Load())
			}
		})
	}
}

func TestAgentCoreLinkCancellationInterruptsPendingPressure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, p := net.Pipe()
	defer p.Close()
	s := continuousAgentFixture()
	control := realtimeBridgeControl{Type: "interrupt", ItemID: "speech", Generation: 1}
	raw, _ := json.Marshal(control)
	link := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, func(c realtimeBridgeControl) { s.cancel(c.ItemID, c.Generation) })
	defer link.Close()
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- s.withPlaybackPressure(ctx, control, func() error { return errCarrierPacerOverflow }) }()
	if err := wsutil.WriteServerMessage(p, ws.OpText, raw); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || time.Since(started) > 150*time.Millisecond {
		t.Fatal(err, time.Since(started))
	}
}

func TestAgentPlaybackPressureBoundsAndCancellation(t *testing.T) {
	s := continuousAgentFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frame := realtimeBridgeControl{ItemID: "reply", Generation: 1}
	var tries int
	if err := s.withPlaybackPressure(ctx, frame, func() error {
		tries++
		if tries < 4 {
			return errCarrierPacerOverflow
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if tries != 4 || s.snapshot().PlaybackPressure.Recovered != 1 {
		t.Fatal(tries, s.snapshot())
	}
	started := time.Now()
	err := s.withPlaybackPressure(ctx, frame, func() error { return errCarrierPacerOverflow })
	if !errors.Is(err, errCarrierPacerOverflow) || time.Since(started) > 400*time.Millisecond || s.snapshot().PlaybackPressure.Exhausted != 1 {
		t.Fatal(err, time.Since(started))
	}
	cancel()
	if err = s.withPlaybackPressure(ctx, frame, func() error { return errCarrierPacerOverflow }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAgentPlaybackPressurePreservesNaturalSpeechAndQuietPCM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := continuousAgentFixture()
	s.profile.BacklogMS = 200
	s.profile.SendAheadMS = 20
	fixture := sipSpeechFixtures[0]
	speech := loadSIPSpeech(t, fixture.id, fixture.hash, 16000)[:4800]
	// Quiet material and digital silence must pass through the same pressure path.
	for i := 500; i < 1000; i++ {
		speech[i] /= 100
	}
	clear(speech[1000:1400])
	var mu sync.Mutex
	var delivered []int16
	var clearCount int
	p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "telnyx", "stream", false, newTwilioPlaybackTracker(), s.policy(), func(raw []byte) error {
		var f carrierMediaFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if f.Event == "clear" {
			clearCount++
			return nil
		}
		if f.Media != nil {
			pcm, err := decodeCarrierPCM(f.Media.Payload, carrierCodecL16_16)
			if err != nil {
				return err
			}
			delivered = append(delivered, pcm...)
		}
		return nil
	}, nil, nil)
	defer func() { cancel(); <-p.done }()
	packets := func(start, end int) []carrierPacedPacket {
		var out []carrierPacedPacket
		for i := start; i < end; i += 320 {
			out = append(out, carrierPacedPacket{PCM: append([]int16(nil), speech[i:i+320]...), ItemID: "reply", Generation: 1, AudioEndMS: (i + 320) * 1000 / 16000})
		}
		return out
	}
	if _, _, err := p.enqueue(ctx, packets(0, 3200)); err != nil {
		t.Fatal(err)
	}
	frame := realtimeBridgeControl{ItemID: "reply", Generation: 1}
	next := packets(3200, 4800)
	if err := s.withPlaybackPressure(ctx, frame, func() error { _, _, err := p.enqueue(ctx, next); return err }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(delivered) == len(speech)
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if clearCount != 0 || !bytes.Equal(pcm16ToBytes(delivered), pcm16ToBytes(speech)) {
		t.Fatalf("audio changed: sent %d/%d samples, clears %d", len(delivered), len(speech), clearCount)
	}
	if s.snapshot().PlaybackPressure.Recovered != 1 {
		t.Fatal(s.snapshot())
	}
}

func TestAgentCoreLinkHangupStopsRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, p := net.Pipe()
	defer p.Close()
	entered := make(chan struct{})
	exited := make(chan struct{})
	link := newAgentCoreLink(ctx, c, func(ctx context.Context) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}, nil)
	go func() { _ = wsutil.WriteServerMessage(p, ws.OpClose, ws.NewCloseFrameBody(1011, "retry")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no reconnect")
	}
	cancel()
	_ = link.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("recovery survived hangup")
	}
}

func BenchmarkAgentPlaybackPressureNoPressure(b *testing.B) {
	s := continuousAgentFixture()
	ctx := context.Background()
	frame := realtimeBridgeControl{ItemID: "reply", Generation: 1}
	enqueue := func() error { return nil }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.withPlaybackPressure(ctx, frame, enqueue) != nil {
			b.Fatal("unexpected pressure")
		}
	}
}

type continuityPlatform struct {
	*answerPlatform
	address  string
	onRenew  func()
	renewals atomic.Int64
}

func (p *continuityPlatform) RenewRealtimeAudioBridge(int64, string) (*sdk.RealtimeSpawnResult, error) {
	p.renewals.Add(1)
	if p.onRenew != nil {
		p.onRenew()
	}
	return &sdk.RealtimeSpawnResult{AudioBridgeURL: p.address}, nil
}

func TestAgentContinuityTelnyxKeepsSingleCarrierStream(t *testing.T) {
	p := &continuityPlatform{answerPlatform: &answerPlatform{}}
	a, appCtx := withTelephonyTestContext(t, p)
	a.startCarrierMediaRecovery(appCtx)
	t.Cleanup(func() { a.stopCarrierBridges(); a.stopRoutingDispatcher() })
	var connections atomic.Int64
	contract := `{"version":1,"continuous_conversation":true,"local_interruption_required":true,"scoped_cancellation":true,"receipt_evidence":true}`
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := ws.HTTPUpgrader{Header: http.Header{agentAudioHeader: []string{contract}}}
		conn, _, _, err := u.Upgrade(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		generation := connections.Add(1)
		for {
			raw, _, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
			if bytes.Contains(raw, []byte("playback.ready")) {
				break
			}
		}
		if generation <= 3 {
			_ = wsutil.WriteServerMessage(conn, ws.OpClose, ws.NewCloseFrameBody(1011, "live audio input backpressure"))
			_, _, _ = wsutil.ReadClientData(conn)
			return
		}
		_ = wsutil.WriteServerMessage(conn, ws.OpText, []byte(`{"type":"audio.frame","item_id":"reply","audio_end_ms":100,"session_generation":1}`))
		pcm := make([]int16, 2400)
		for i := range pcm {
			pcm[i] = 1200
		}
		_ = wsutil.WriteServerMessage(conn, ws.OpBinary, pcm16ToBytes(pcm))
		for {
			if _, _, err = wsutil.ReadClientData(conn); err != nil {
				return
			}
		}
	}))
	defer core.Close()
	p.address = "ws" + strings.TrimPrefix(core.URL, "http")
	row := testCall("continuous-core-only", "in-progress")
	row.ProjectID = appCtx.CurrentProject()
	row.PeerKind = peerKindRealtime
	row.CarrierSlug = "telnyx"
	row.AudioBridgeURL = p.address
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		a.handleTelnyxMediaStream(w, r)
	}))
	t.Cleanup(func() {
		a.cancelCarrierBridge(row.ID)
		select {
		case <-handlerDone:
		case <-time.After(3 * time.Second):
			t.Error("carrier handler survived cleanup")
		}
	})
	defer server.Close()
	conn := dialWS(t, server.URL+"/media/telnyx/"+row.ID+"/"+row.CallbackSecret)
	defer conn.Close()
	writeRecoveryTelnyxFrame(t, conn, &row, "one-stream")
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		raw, _, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatal("carrier was disconnected", err)
		}
		if bytes.Contains(raw, []byte(`"event":"media"`)) {
			break
		}
	}
	if p.renewals.Load() != 3 || connections.Load() != 4 || len(p.integrationCalls) != 0 {
		t.Fatalf("renewed Core %d, connections %d, carrier calls %v", p.renewals.Load(), connections.Load(), p.integrationCalls)
	}
	var count int
	if err := a.db().db.QueryRow(`SELECT count(*) FROM carrier_media_bridges WHERE call_id=?`, row.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("carrier generation was replaced", count, err)
	}
	if !bridgeClaimActive(t, a, row.ID) {
		t.Fatal("carrier ownership released")
	}
	a.cancelCarrierBridge(row.ID)
}

func TestAgentContinuityExhaustionNeverRestartsHealthyCarrier(t *testing.T) {
	p := &answerPlatform{}
	a, ctx := withTelephonyTestContext(t, p)
	row := recoveryCall(t, a, "core-exhaustion", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.coreManaged.Store(true)
	b.setStream("healthy-carrier")
	b.media()
	b.socketError(mediaCloseLegCore, "read", wsutil.ClosedError{Code: 1011, Reason: agentCoreRecoveryExhausted})
	b.finish()
	if state := bridgeState(t, b); state != "failed" {
		t.Fatal(state)
	}
	if err = a.runCarrierMediaRecovery(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	a.decisionWG.Wait()
	if len(p.integrationCalls) != 0 {
		t.Fatal("Core failure restarted carrier", p.integrationCalls)
	}
}

func BenchmarkAgentCoreLinkDuplex(b *testing.B) {
	for _, resilient := range []bool{false, true} {
		b.Run(strconv.FormatBool(resilient), func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, p := net.Pipe()
			defer p.Close()
			go func() {
				for {
					data, op, err := wsutil.ReadClientData(p)
					if err != nil {
						return
					}
					if wsutil.WriteServerMessage(p, op, data) != nil {
						return
					}
				}
			}()
			var conn net.Conn = c
			if resilient {
				conn = newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, nil)
			}
			defer conn.Close()
			pcm := make([]byte, 960)
			b.ReportAllocs()
			b.SetBytes(1920)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := wsutil.WriteClientMessage(conn, ws.OpBinary, pcm); err != nil {
					b.Fatal(err)
				}
				if _, _, err := wsutil.ReadServerData(conn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAgentCoreLinkConcurrentPCMIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errorsOut := make(chan error, 32)
	var wg sync.WaitGroup
	for stream := 0; stream < 32; stream++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, p := net.Pipe()
			defer p.Close()
			go func() {
				for {
					data, op, err := wsutil.ReadClientData(p)
					if err != nil {
						return
					}
					if wsutil.WriteServerMessage(p, op, data) != nil {
						return
					}
				}
			}()
			conn := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, nil)
			defer conn.Close()
			pcm := make([]byte, 960)
			for frame := 0; frame < 100; frame++ {
				for i := range pcm {
					pcm[i] = byte(stream + frame + i)
				}
				if err := wsutil.WriteClientMessage(conn, ws.OpBinary, pcm); err != nil {
					errorsOut <- err
					return
				}
				got, _, err := wsutil.ReadServerData(conn)
				if err != nil {
					errorsOut <- err
					return
				}
				if !bytes.Equal(got, pcm) {
					errorsOut <- errors.New("cross-stream or changed PCM")
					return
				}
			}
			if s := conn.snapshot(); s.Reconnects != 0 || s.InputDiscardedMS != 0 {
				errorsOut <- errors.New("unexpected healthy-stream recovery/drop")
			}
		}()
	}
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func TestAgentCoreLinkRecoveryCoalescesOldReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, p := net.Pipe()
	defer p.Close()
	recovering := make(chan struct{})
	release := make(chan struct{})
	c2, p2 := net.Pipe()
	defer p2.Close()
	go func() {
		_, _, _ = wsutil.ReadClientData(p)
		_ = wsutil.WriteServerMessage(p, ws.OpClose, ws.NewCloseFrameBody(1011, "retry"))
	}()
	go func() {
		defer p2.Close()
		_, _, _ = wsutil.ReadClientData(p2)
		_ = wsutil.WriteServerMessage(p2, ws.OpBinary, []byte{1, 0})
		<-ctx.Done()
	}()
	link := newAgentCoreLink(ctx, c, func(ctx context.Context) (net.Conn, error) {
		close(recovering)
		select {
		case <-release:
			return c2, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, nil)
	defer link.Close()
	_ = wsutil.WriteClientMessage(link, ws.OpText, []byte(`{"type":"playback.ready"}`))
	<-recovering
	for i := 0; i < 100; i++ {
		if err := wsutil.WriteClientMessage(link, ws.OpText, []byte(`{"type":"playback.progress","item_id":"old","audio_end_ms":20}`)); err != nil {
			t.Fatal("receipt traffic killed the carrier-facing link", err)
		}
	}
	close(release)
	data, op, err := wsutil.ReadServerData(link)
	if err != nil || op != ws.OpBinary || len(data) != 2 {
		t.Fatal(data, op, err)
	}
}

func TestAgentCoreLinkFragmentedPCMAndPings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, p := net.Pipe()
	defer p.Close()
	samples := make([]byte, 960)
	for i := range samples {
		samples[i] = byte(i)
	}
	go func() {
		writer := &agentRelayWriter{conn: p, state: ws.StateServerSide}
		got, op, err := readAgentRelayData(p, ws.StateServerSide, writer)
		if err != nil || op != ws.OpBinary || !bytes.Equal(got, samples) {
			return
		}
		go func() { _, _, _ = readAgentRelayData(p, ws.StateServerSide, writer) }()
		_ = ws.WriteFrame(p, ws.NewFrame(ws.OpBinary, false, samples[:400]))
		_ = wsutil.WriteServerMessage(p, ws.OpPing, []byte("alive"))
		_ = ws.WriteFrame(p, ws.NewFrame(ws.OpContinuation, true, samples[400:]))
		// Consume the pong; the caller closes the connection after checking PCM.
		_, _, _ = readAgentRelayData(p, ws.StateServerSide, writer)
	}()
	link := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, nil)
	defer link.Close()
	_ = ws.WriteFrame(link, ws.MaskFrameInPlace(ws.NewFrame(ws.OpBinary, false, append([]byte(nil), samples[:400]...))))
	_ = ws.WriteFrame(link, ws.MaskFrameInPlace(ws.NewFrame(ws.OpContinuation, true, append([]byte(nil), samples[400:]...))))
	got, op, err := wsutil.ReadServerData(link)
	if err != nil || op != ws.OpBinary || !bytes.Equal(got, samples) {
		t.Fatal("fragmentation/ping changed PCM", len(got), err)
	}
}

func TestAgentContinuityOwnershipChangeDuringRenewal(t *testing.T) {
	p := &continuityPlatform{answerPlatform: &answerPlatform{}, address: "wss://unused.invalid/audio"}
	a, appCtx := withTelephonyTestContext(t, p)
	row := testCall("changed-owner", "in-progress")
	row.ProjectID = appCtx.CurrentProject()
	row.PeerKind = peerKindRealtime
	row.AudioBridgeURL = "ws://original.invalid/audio"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	p.onRenew = func() { _, _ = a.db().db.Exec(`UPDATE calls SET peer_kind='human',agent_id=0 WHERE id=?`, row.ID) }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _, _ = wsutil.ReadClientData(peer)
		_ = wsutil.WriteServerMessage(peer, ws.OpClose, ws.NewCloseFrameBody(1011, "retry"))
	}()
	conn := a.resilientAgentBridge(ctx, &row, c, nil, continuousAgentFixture())
	defer conn.Close()
	_ = wsutil.WriteClientMessage(conn, ws.OpText, []byte(`{"type":"playback.ready"}`))
	if _, _, err := wsutil.ReadServerData(conn); err == nil {
		t.Fatal("stale owner kept bridge")
	}
	current, err := a.db().findCall(row.ID)
	if err != nil || current.AudioBridgeURL != row.AudioBridgeURL || p.renewals.Load() != 1 {
		t.Fatal("stale renewal changed the new owner", current, err, p.renewals.Load())
	}
}
