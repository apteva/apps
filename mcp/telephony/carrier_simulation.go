package main

// Simulated carrier for direct SIP inside an Environment.
//
// The platform's integration simulation speaks the provider's HTTP API,
// webhooks and media streams, but not SIP: the platform stays protocol
// agnostic. When a simulated Telnyx number points at a direct-SIP (FQDN)
// connection, the simulation hands the call to this app
// (sdk.IntegrationSimulationReceiver), and Telephony plays the carrier
// itself: it places a real SIP call over loopback into its own SIP gateway,
// sends the fixture audio as RTP, measures the audio it gets back and reports
// what happened into the simulation's record (sdk.IntegrationSimulationReporter).
//
// It only ever calls this install's own gateway on a loopback address, and
// the SDK only routes hand-offs inside an Environment.

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/gotranspile/g722"
	"github.com/pion/rtp"
)

const (
	carrierSimKindInbound = "sip_inbound_call"
	carrierSimKindHangup  = "sip_hangup"
	carrierSimKindDTMF    = "sip_dtmf"
	carrierSimKindPlay    = "sip_play"

	carrierSimMaxCalls       = 8
	carrierSimRingTimeout    = 3 * time.Minute
	carrierSimMaxDuration    = 10 * time.Minute
	carrierSimMaxAudio       = 120 * time.Second
	carrierSimMaxQueuedAudio = 10 * time.Minute
	carrierSimReportEvery    = 2 * time.Second
	carrierSimFrame          = 20 * time.Millisecond
	carrierSimDTMFPayload    = 101
	// A received 20 ms frame counts as speech above this RMS (about -42 dBFS).
	carrierSimSpeechRMS = 260
)

var errCarrierSimRingTimeout = errors.New("ring timeout")

var _ sdk.IntegrationSimulationReceiver = (*App)(nil)

var (
	carrierSimNumberPattern = regexp.MustCompile(`^\+?[0-9A-Za-z._-]{1,64}$`)
	carrierSimDigitsPattern = regexp.MustCompile(`^[0-9*#A-D]{1,32}$`)
	carrierSimPayloadTypes  = map[string]uint8{"PCMU": 0, "PCMA": 8, "G722": 9}
)

type carrierSimulator struct {
	mu      sync.Mutex
	calls   map[string]*carrierSimCall
	stopped bool
	work    sync.WaitGroup
}

type carrierSimAudio struct {
	Name      string `json:"name"`
	WAVBase64 string `json:"wav_base64"`
}

// hasAudio is false for a silent caller: the simulation sends
// {"name":"silence"} without audio bytes.
func (a *carrierSimAudio) hasAudio() bool {
	return a != nil && strings.TrimSpace(a.WAVBase64) != ""
}

type carrierSimInbound struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Target struct {
		Host      string `json:"host"`
		Port      int    `json:"port"`
		Transport string `json:"transport"`
	} `json:"target"`
	Codecs         []string         `json:"codecs"`
	EncryptedMedia json.RawMessage  `json:"encrypted_media"`
	CallerAudio    *carrierSimAudio `json:"caller_audio"`
}

type carrierSimHangup struct {
	Cause string `json:"cause"`
	Q850  int    `json:"q850"`
}

type carrierSimCall struct {
	sim      *carrierSimulator
	reporter sdk.IntegrationSimulationReporter
	logger   interface{ Warn(string, ...any) }
	event    sdk.IntegrationSimulationEvent
	request  carrierSimInbound
	codecs   []string

	ctx     context.Context
	cancel  context.CancelFunc
	ua      *sipgo.UserAgent
	sigIP   netip.Addr
	sigPort int
	rtp     *net.UDPConn
	rtcp    *net.UDPConn

	hangupCh chan carrierSimHangup
	dtmfCh   chan string
	done     chan struct{}

	mu        sync.Mutex
	fields    map[string]any
	responses []int
	play      [][]int16 // caller audio at the negotiated codec's rate
	playNames []string
	queuedPCM int
	dtmfSent  string
	answered  bool
	ended     bool
	rate      int
	dtmfPT    int
	changed   bool
	stats     carrierSimRTPStats
	reportMu  sync.Mutex
	finalSent atomic.Bool
}

type carrierSimRTPStats struct {
	SentPackets     int64 `json:"sent_packets"`
	SentMS          int64 `json:"sent_ms"`
	ReceivedPackets int64 `json:"received_packets"`
	ReceivedMS      int64 `json:"received_ms"`
	NonSilentMS     int64 `json:"non_silent_ms"`
	Peak            int   `json:"peak"`
	RMS             int   `json:"rms"`
	sumSquares      float64
	samples         int64
}

func (a *App) carrierSimulation() *carrierSimulator {
	a.carrierSimOnce.Do(func() { a.carrierSim = &carrierSimulator{calls: map[string]*carrierSimCall{}} })
	return a.carrierSim
}

// HandleIntegrationSimulation implements sdk.IntegrationSimulationReceiver.
func (a *App) HandleIntegrationSimulation(ctx *sdk.AppCtx, event sdk.IntegrationSimulationEvent) (any, error) {
	if sdk.CurrentEnvironmentID() == "" {
		return nil, errors.New("simulated carrier calls run only inside an Environment")
	}
	sim := a.carrierSimulation()
	switch event.Kind {
	case carrierSimKindInbound:
		return sim.startInbound(a, ctx, event)
	case carrierSimKindHangup, carrierSimKindDTMF, carrierSimKindPlay:
		call := sim.call(event.ID)
		if call == nil {
			return nil, fmt.Errorf("no active simulated SIP call %q", event.ID)
		}
		return call.control(event)
	default:
		return nil, fmt.Errorf("unsupported simulation hand-off kind %q", event.Kind)
	}
}

func (s *carrierSimulator) call(id string) *carrierSimCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[id]
}

// stop ends every simulated call (BYE or CANCEL) and waits for them.
func (s *carrierSimulator) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	calls := make([]*carrierSimCall, 0, len(s.calls))
	for _, call := range s.calls {
		calls = append(calls, call)
	}
	s.mu.Unlock()
	for _, call := range calls {
		call.requestHangup(carrierSimHangup{Cause: "normal_clearing", Q850: 16})
	}
	done := make(chan struct{})
	go func() { s.work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		for _, call := range calls {
			call.cancel()
		}
		s.work.Wait()
	}
}

func (s *carrierSimulator) startInbound(app *App, ctx *sdk.AppCtx, event sdk.IntegrationSimulationEvent) (any, error) {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.Collection) == "" {
		return nil, errors.New("a simulated SIP call needs the simulation collection and id")
	}
	var request carrierSimInbound
	if err := json.Unmarshal(event.Payload, &request); err != nil {
		return nil, fmt.Errorf("invalid sip_inbound_call payload: %w", err)
	}
	if !carrierSimNumberPattern.MatchString(request.To) || !carrierSimNumberPattern.MatchString(request.From) {
		return nil, errors.New("sip_inbound_call needs from and to numbers")
	}
	if carrierSimEncrypted(request.EncryptedMedia) {
		return nil, errors.New("simulated SIP calls use plain RTP; set sip_srtp=disabled inside the Environment")
	}
	gateway := app.directSIPGateway()
	if gateway == nil {
		return nil, errors.New("the direct SIP gateway is not running")
	}
	target, transport, err := carrierSimTarget(gateway.cfg, request)
	if err != nil {
		return nil, err
	}
	codecs, err := carrierSimCodecs(request.Codecs)
	if err != nil {
		return nil, err
	}
	var audio []int16
	var audioRate int
	if request.CallerAudio.hasAudio() {
		if audio, audioRate, err = decodeCarrierSimWAV(request.CallerAudio.WAVBase64); err != nil {
			return nil, fmt.Errorf("caller audio %q: %w", request.CallerAudio.Name, err)
		}
	}
	reporter, _ := ctx.PlatformAPI().(sdk.IntegrationSimulationReporter)
	if reporter == nil {
		return nil, errors.New("this platform does not accept simulation reports")
	}

	s.mu.Lock()
	switch {
	case s.stopped:
		s.mu.Unlock()
		return nil, errors.New("Telephony is stopping")
	case s.calls[event.ID] != nil:
		s.mu.Unlock()
		return nil, fmt.Errorf("simulated SIP call %q is already active", event.ID)
	case len(s.calls) >= carrierSimMaxCalls:
		s.mu.Unlock()
		return nil, fmt.Errorf("at most %d simulated SIP calls can run at once", carrierSimMaxCalls)
	}
	callCtx, cancel := context.WithCancel(context.Background())
	call := &carrierSimCall{
		sim: s, reporter: reporter, logger: ctx.Logger(), event: event, request: request, codecs: codecs,
		ctx: callCtx, cancel: cancel,
		hangupCh: make(chan carrierSimHangup, 1), dtmfCh: make(chan string, 8), done: make(chan struct{}),
		fields: map[string]any{}, dtmfPT: -1,
	}
	s.calls[event.ID] = call
	s.work.Add(1)
	s.mu.Unlock()

	if err := call.open(transport, target.Addr()); err != nil {
		call.release()
		return nil, err
	}
	if audio != nil {
		call.pendingAudio(request.CallerAudio.Name, audio, audioRate)
	}
	dialog, err := call.invite(target, transport)
	if err != nil {
		call.closeMedia()
		call.release()
		return nil, err
	}
	go call.run(dialog)
	return map[string]any{"state": "inviting", "sip_call_id_header": sipCallID(dialog.InviteRequest)}, nil
}

func carrierSimEncrypted(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	switch strings.ToLower(value) {
	case "", "null", "false", `""`, `"none"`, `"disabled"`:
		return false
	}
	return true
}

// The only allowed target is this install's own gateway on loopback.
func carrierSimTarget(cfg sipGatewayConfig, request carrierSimInbound) (netip.AddrPort, string, error) {
	transport := strings.ToLower(strings.TrimSpace(request.Target.Transport))
	if transport == "" {
		transport = "udp"
	}
	if transport != "udp" && transport != "tcp" {
		return netip.AddrPort{}, "", fmt.Errorf("simulated SIP calls use udp or tcp, not %q", transport)
	}
	if transport != cfg.Transport {
		return netip.AddrPort{}, "", fmt.Errorf("the SIP gateway listens on %s, not %s", cfg.Transport, transport)
	}
	listen, err := netip.ParseAddrPort(cfg.ListenAddress)
	if err != nil || !listen.Addr().Unmap().IsLoopback() {
		return netip.AddrPort{}, "", errors.New("simulated SIP calls need the gateway on a loopback sip_listen address")
	}
	host := strings.Trim(strings.TrimSpace(request.Target.Host), "[]")
	ip, err := netip.ParseAddr(host)
	if err != nil && strings.EqualFold(host, "localhost") {
		ip, err = netip.MustParseAddr("127.0.0.1"), nil
	}
	if err != nil || !ip.Unmap().IsLoopback() {
		return netip.AddrPort{}, "", fmt.Errorf("simulated SIP target %q is not a loopback address", request.Target.Host)
	}
	if request.Target.Port != int(listen.Port()) {
		return netip.AddrPort{}, "", fmt.Errorf("simulated SIP target port %d is not this gateway's port %d", request.Target.Port, listen.Port())
	}
	return listen, transport, nil
}

// Telnyx connection codecs use provisioning names; SDP uses RTP names.
func carrierSimCodecs(values []string) ([]string, error) {
	var out []string
	for _, value := range values {
		codec := strings.ToUpper(strings.TrimSpace(value))
		switch codec {
		case "G711U", "PCMU":
			codec = "PCMU"
		case "G711A", "PCMA":
			codec = "PCMA"
		case "G722":
		default:
			continue
		}
		if !containsString(out, codec) {
			out = append(out, codec)
		}
	}
	if len(values) == 0 {
		out = []string{"PCMU", "PCMA"}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("none of the connection codecs %v can be simulated (G722, G711U, G711A)", values)
	}
	return out, nil
}

// decodeCarrierSimWAV accepts RIFF/WAVE PCM16, mono or stereo.
func decodeCarrierSimWAV(encoded string) ([]int16, int, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(raw) == 0 {
		return nil, 0, errors.New("wav_base64 is missing or not base64")
	}
	if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, errors.New("audio is not a RIFF/WAVE file")
	}
	channels, rate := 0, 0
	for offset := 12; offset+8 <= len(raw); {
		id, size := string(raw[offset:offset+4]), int(binary.LittleEndian.Uint32(raw[offset+4:offset+8]))
		body := offset + 8
		if size < 0 || body+size > len(raw) {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size < 16 || binary.LittleEndian.Uint16(raw[body:]) != 1 || binary.LittleEndian.Uint16(raw[body+14:]) != 16 {
				return nil, 0, errors.New("audio must be uncompressed 16-bit PCM")
			}
			channels = int(binary.LittleEndian.Uint16(raw[body+2:]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4:]))
			if channels < 1 || channels > 2 || rate < 8000 || rate > 48000 {
				return nil, 0, errors.New("audio must be mono or stereo at 8-48 kHz")
			}
		case "data":
			if channels == 0 {
				return nil, 0, errors.New("WAVE data precedes its format")
			}
			frames := size / (2 * channels)
			if frames == 0 {
				return nil, 0, errors.New("audio contains no samples")
			}
			if time.Duration(frames)*time.Second/time.Duration(rate) > carrierSimMaxAudio {
				return nil, 0, fmt.Errorf("audio is longer than %s", carrierSimMaxAudio)
			}
			pcm := make([]int16, frames)
			for i := range pcm {
				at := body + i*2*channels
				sample := int32(int16(binary.LittleEndian.Uint16(raw[at:])))
				if channels == 2 {
					sample = (sample + int32(int16(binary.LittleEndian.Uint16(raw[at+2:])))) / 2
				}
				pcm[i] = int16(sample)
			}
			return pcm, rate, nil
		}
		offset = body + size + size%2
	}
	return nil, 0, errors.New("audio has no PCM data")
}

func (c *carrierSimCall) control(event sdk.IntegrationSimulationEvent) (any, error) {
	c.mu.Lock()
	ended, answered := c.ended, c.answered
	c.mu.Unlock()
	if ended {
		return nil, fmt.Errorf("simulated SIP call %q has ended", event.ID)
	}
	switch event.Kind {
	case carrierSimKindHangup:
		request := carrierSimHangup{Cause: "normal_clearing", Q850: 16}
		if len(event.Payload) > 0 {
			if err := json.Unmarshal(event.Payload, &request); err != nil {
				return nil, fmt.Errorf("invalid sip_hangup payload: %w", err)
			}
		}
		if request.Cause == "" {
			request.Cause = "normal_clearing"
		}
		if request.Q850 <= 0 || request.Q850 > 127 {
			request.Q850 = 16
		}
		c.requestHangup(request)
		return map[string]any{"hangup": "requested"}, nil
	case carrierSimKindDTMF:
		var request struct {
			Digits string `json:"digits"`
		}
		if err := json.Unmarshal(event.Payload, &request); err != nil || !carrierSimDigitsPattern.MatchString(request.Digits) {
			return nil, errors.New("sip_dtmf needs 1-32 digits from 0-9*#A-D")
		}
		if !answered {
			return nil, errors.New("the simulated SIP call is not answered yet")
		}
		c.mu.Lock()
		negotiated := c.dtmfPT >= 0
		c.mu.Unlock()
		if !negotiated {
			return nil, errors.New("Telephony did not accept telephone-event for this call")
		}
		select {
		case c.dtmfCh <- request.Digits:
			return map[string]any{"queued": request.Digits}, nil
		default:
			return nil, errors.New("too many queued DTMF sequences")
		}
	default: // carrierSimKindPlay
		var request struct {
			CallerAudio *carrierSimAudio `json:"caller_audio"`
		}
		if err := json.Unmarshal(event.Payload, &request); err != nil || request.CallerAudio == nil {
			return nil, errors.New("sip_play needs caller_audio")
		}
		if !request.CallerAudio.hasAudio() {
			return map[string]any{"queued_ms": 0}, nil
		}
		pcm, rate, err := decodeCarrierSimWAV(request.CallerAudio.WAVBase64)
		if err != nil {
			return nil, fmt.Errorf("caller audio %q: %w", request.CallerAudio.Name, err)
		}
		if err := c.pendingAudio(request.CallerAudio.Name, pcm, rate); err != nil {
			return nil, err
		}
		return map[string]any{"queued_ms": len(pcm) * 1000 / rate}, nil
	}
}

func (c *carrierSimCall) requestHangup(request carrierSimHangup) {
	select {
	case c.hangupCh <- request:
	default:
	}
}

// pendingAudio queues caller audio; before the answer it keeps the source
// rate and is converted once the codec is known.
func (c *carrierSimCall) pendingAudio(name string, pcm []int16, rate int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	outRate := c.rate
	if outRate == 0 {
		outRate = 16000
	}
	queued := time.Duration(c.queuedPCM) * time.Second / time.Duration(outRate)
	if queued+time.Duration(len(pcm))*time.Second/time.Duration(rate) > carrierSimMaxQueuedAudio {
		return errors.New("too much caller audio is queued")
	}
	converted := resamplePCM(pcm, rate, outRate)
	c.play = append(c.play, converted)
	c.queuedPCM += len(converted)
	c.playNames = append(c.playNames, name)
	c.fields["audio_queued"] = append([]string(nil), c.playNames...)
	c.changed = true
	return nil
}

// open binds the SIP socket and a pair of adjacent RTP/RTCP ports.
func (c *carrierSimCall) open(transport string, gatewayIP netip.Addr) error {
	ip := gatewayIP.Unmap()
	port, err := freeLoopbackSIPPort(transport, ip)
	if err != nil {
		return fmt.Errorf("simulated carrier SIP port: %w", err)
	}
	c.sigIP, c.sigPort = ip, port
	for range 32 {
		rtpConn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, 0)))
		if err != nil {
			return fmt.Errorf("simulated carrier RTP port: %w", err)
		}
		rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
		if rtpPort%2 == 0 && rtpPort < 65535 {
			if rtcpConn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(rtpPort+1)))); err == nil {
				c.rtp, c.rtcp = rtpConn, rtcpConn
				return nil
			}
		}
		_ = rtpConn.Close()
	}
	return errors.New("simulated carrier could not bind adjacent RTP/RTCP ports")
}

func (c *carrierSimCall) closeMedia() {
	if c.rtp != nil {
		_ = c.rtp.Close()
	}
	if c.rtcp != nil {
		_ = c.rtcp.Close()
	}
	if c.ua != nil {
		_ = c.ua.Close()
	}
}

func (c *carrierSimCall) release() {
	c.cancel()
	c.sim.mu.Lock()
	if c.sim.calls[c.event.ID] == c {
		delete(c.sim.calls, c.event.ID)
	}
	c.sim.mu.Unlock()
	c.sim.work.Done()
}

func (c *carrierSimCall) invite(target netip.AddrPort, transport string) (*sipgo.DialogClientSession, error) {
	host := c.sigIP.String()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("Apteva-Simulated-Carrier"), sipgo.WithUserAgentHostname(host))
	if err != nil {
		return nil, err
	}
	c.ua = ua
	server, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, err
	}
	local := net.JoinHostPort(host, strconv.Itoa(c.sigPort))
	options := []sipgo.ClientOption{sipgo.WithClientHostname(host)}
	if transport == "udp" {
		// Requests leave from the listening socket, so in-dialog requests
		// from the gateway come back to it.
		options = append(options, sipgo.WithClientConnectionAddr(local))
	}
	client, err := sipgo.NewClient(ua, options...)
	if err != nil {
		return nil, err
	}
	params := sip.NewParams()
	params.Add("transport", transport)
	contact := sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "carrier", Host: host, Port: c.sigPort, UriParams: params}}
	dialogs := sipgo.NewDialogClientCache(client, contact)
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := dialogs.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
			return
		}
		c.mu.Lock()
		c.fields["bye_reason"] = sipReasonHeader(req)
		c.mu.Unlock()
	})
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// A re-INVITE (session refresh or hold) keeps the current media.
		if req.To() == nil || !req.To().Params.Has("tag") {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusMethodNotAllowed, "Not Accepting Calls", nil))
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", c.localSDP()))
	})
	server.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
	server.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
	ready := make(chan struct{})
	listenCtx := context.WithValue(c.ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready))
	listenErr := make(chan error, 1)
	go func() { listenErr <- server.ListenAndServe(listenCtx, transport, local) }()
	select {
	case <-ready:
	case err := <-listenErr:
		return nil, fmt.Errorf("simulated carrier SIP listener: %w", err)
	case <-time.After(3 * time.Second):
		return nil, errors.New("simulated carrier SIP listener did not start")
	}

	recipient := sip.Uri{Scheme: "sip", User: c.request.To, Host: target.Addr().String(), Port: int(target.Port()), UriParams: params}
	fromParams := sip.NewParams()
	fromParams.Add("tag", sip.GenerateTagN(16))
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: c.request.From, Host: host}, Params: fromParams}
	// Reports wait for run(): the platform may still be delivering this
	// hand-off, and a failed INVITE is reported by the hand-off's own error.
	invitedAt := carrierSimNow()
	dialog, err := dialogs.Invite(c.ctx, recipient, c.localSDP(), from, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		return nil, fmt.Errorf("simulated SIP INVITE: %w", err)
	}
	c.mu.Lock()
	c.fields["state"], c.fields["invited_at"], c.fields["transport"] = "inviting", invitedAt, transport
	c.fields["sip_call_id_header"] = sipCallID(dialog.InviteRequest)
	c.mu.Unlock()
	return dialog, nil
}

func (c *carrierSimCall) localSDP() []byte {
	port := c.rtp.LocalAddr().(*net.UDPAddr).Port
	formats := make([]string, 0, len(c.codecs)+1)
	lines := make([]string, 0, len(c.codecs)+2)
	for _, codec := range c.codecs {
		pt := strconv.Itoa(int(carrierSimPayloadTypes[codec]))
		formats = append(formats, pt)
		lines = append(lines, "a=rtpmap:"+pt+" "+codec+"/8000")
	}
	formats = append(formats, strconv.Itoa(carrierSimDTMFPayload))
	lines = append(lines, fmt.Sprintf("a=rtpmap:%d telephone-event/8000", carrierSimDTMFPayload),
		fmt.Sprintf("a=fmtp:%d 0-16", carrierSimDTMFPayload))
	ip := c.sigIP.String()
	return []byte("v=0\r\n" +
		"o=carrier 1 1 IN IP4 " + ip + "\r\n" +
		"s=Apteva simulated carrier\r\n" +
		"c=IN IP4 " + ip + "\r\n" +
		"t=0 0\r\n" +
		fmt.Sprintf("m=audio %d RTP/AVP %s\r\n", port, strings.Join(formats, " ")) +
		strings.Join(lines, "\r\n") + "\r\n" +
		"a=ptime:20\r\na=sendrecv\r\n")
}

func (c *carrierSimCall) run(dialog *sipgo.DialogClientSession) {
	defer c.release()
	defer c.closeMedia()
	defer func() { _ = dialog.Close() }()

	c.report("inviting", nil)
	answered, hangup, err := c.waitAnswer(dialog)
	if err != nil || !answered {
		if hangup != nil {
			c.finish(map[string]any{"state": "ended", "ended_by": "caller", "cause": hangup.Cause, "q850": hangup.Q850})
		} else if err != nil {
			c.finish(c.inviteFailure(dialog, err))
		}
		return
	}
	answer, err := parseSIPMediaOfferCodecs(dialog.InviteResponse.Body(), c.mediaConfig(), c.codecs)
	ackCtx, stopAck := context.WithTimeout(context.Background(), 3*time.Second)
	ackErr := dialog.Ack(ackCtx)
	stopAck()
	if err == nil && answer.PayloadType != carrierSimPayloadTypes[answer.Codec] {
		err = fmt.Errorf("Telephony answered %s with payload type %d", answer.Codec, answer.PayloadType)
	}
	if err == nil {
		err = ackErr
	}
	if err != nil {
		c.bye(dialog, carrierSimHangup{Cause: "incompatible_destination", Q850: 88})
		c.finish(map[string]any{"state": "failed", "ended_by": "caller", "error": "answer: " + err.Error()})
		return
	}
	remote := net.UDPAddrFromAddrPort(netip.AddrPortFrom(answer.RemoteAddress, uint16(answer.RemotePort)))
	rate := 8000
	if answer.Codec == "G722" {
		rate = 16000
	}
	c.mu.Lock()
	c.answered, c.rate = true, rate
	if answer.DTMFPayloadType >= 0 {
		c.dtmfPT = answer.DTMFPayloadType
	}
	// Audio queued before the answer was converted to 16 kHz.
	if rate != 16000 {
		c.queuedPCM = 0
		for i := range c.play {
			c.play[i] = resamplePCM(c.play[i], 16000, rate)
			c.queuedPCM += len(c.play[i])
		}
	}
	c.mu.Unlock()
	c.report("answered", map[string]any{"state": "answered", "answered_at": carrierSimNow(), "codec": answer.Codec})

	mediaCtx, stopMedia := context.WithCancel(c.ctx)
	var media sync.WaitGroup
	media.Add(3)
	go func() { defer media.Done(); c.send(mediaCtx, remote, answer) }()
	go func() { defer media.Done(); c.receive(mediaCtx, answer) }()
	go func() { defer media.Done(); c.drainRTCP(mediaCtx) }()
	defer func() { stopMedia(); media.Wait() }()

	ticker := time.NewTicker(carrierSimReportEvery)
	defer ticker.Stop()
	limit := time.NewTimer(carrierSimMaxDuration)
	defer limit.Stop()
	for {
		select {
		case request := <-c.hangupCh:
			stopMedia()
			media.Wait()
			if err := c.bye(dialog, request); err != nil {
				c.finish(map[string]any{"state": "ended", "ended_by": "caller", "cause": request.Cause, "q850": request.Q850, "error": "BYE: " + err.Error()})
				return
			}
			c.finish(map[string]any{"state": "ended", "ended_by": "caller", "cause": request.Cause, "q850": request.Q850})
			return
		case <-dialog.Context().Done():
			stopMedia()
			media.Wait()
			c.mu.Lock()
			reason, _ := c.fields["bye_reason"].(map[string]any)
			c.mu.Unlock()
			end := map[string]any{"state": "ended", "ended_by": "app", "cause": "normal_clearing", "q850": 16}
			if reason != nil {
				for key, value := range reason {
					end[key] = value
				}
			}
			c.finish(end)
			return
		case <-limit.C:
			stopMedia()
			media.Wait()
			request := carrierSimHangup{Cause: "max_duration", Q850: 102}
			_ = c.bye(dialog, request)
			c.finish(map[string]any{"state": "ended", "ended_by": "caller", "cause": request.Cause, "q850": request.Q850})
			return
		case <-ticker.C:
			c.report("", nil)
		case <-c.ctx.Done():
			stopMedia()
			media.Wait()
			_ = c.bye(dialog, carrierSimHangup{Cause: "normal_clearing", Q850: 16})
			c.finish(map[string]any{"state": "ended", "ended_by": "caller", "cause": "simulation_stopped"})
			return
		}
	}
}

// waitAnswer returns the caller's hang-up when the caller gave up while
// ringing; the CANCEL has then been sent.
func (c *carrierSimCall) waitAnswer(dialog *sipgo.DialogClientSession) (bool, *carrierSimHangup, error) {
	ringCtx, stopRing := context.WithTimeoutCause(c.ctx, carrierSimRingTimeout, errCarrierSimRingTimeout)
	defer stopRing()
	var hangup atomic.Pointer[carrierSimHangup]
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case request := <-c.hangupCh:
			hangup.Store(&request)
			stopRing()
		case <-ringCtx.Done():
		}
	}()
	err := dialog.WaitAnswer(ringCtx, sipgo.AnswerOptions{OnResponse: func(response *sip.Response) error {
		c.mu.Lock()
		c.responses = append(c.responses, int(response.StatusCode))
		c.fields["responses"] = append([]int(nil), c.responses...)
		c.changed = true
		c.mu.Unlock()
		if response.StatusCode == sip.StatusRinging || response.StatusCode == sip.StatusSessionInProgress {
			c.mu.Lock()
			_, rang := c.fields["ringing_at"]
			c.mu.Unlock()
			if !rang {
				c.report("ringing", map[string]any{"state": "ringing", "ringing_at": carrierSimNow()})
			}
		}
		return nil
	}})
	stopRing()
	<-watching
	if err == nil {
		// A hang-up that raced the answer is handled once the call is up.
		if request := hangup.Load(); request != nil {
			c.requestHangup(*request)
		}
		return true, nil, nil
	}
	// A 2xx that crossed our CANCEL must still be acknowledged and ended.
	if response := dialog.InviteResponse; response != nil && response.IsSuccess() {
		ackCtx, stopAck := context.WithTimeout(context.Background(), 3*time.Second)
		if dialog.Ack(ackCtx) == nil {
			_ = c.bye(dialog, carrierSimHangup{Cause: "originator_cancel", Q850: 16})
		}
		stopAck()
	}
	if request := hangup.Load(); request != nil {
		return false, request, err
	}
	if errors.Is(context.Cause(ringCtx), errCarrierSimRingTimeout) {
		request := carrierSimHangup{Cause: "no_answer", Q850: 19}
		return false, &request, err
	}
	return false, nil, err
}

func (c *carrierSimCall) inviteFailure(dialog *sipgo.DialogClientSession, err error) map[string]any {
	var rejected *sipgo.ErrDialogResponse
	if errors.As(err, &rejected) && rejected.Res != nil {
		return map[string]any{"state": "ended", "ended_by": "app", "cause": "rejected",
			"sip_status": int(rejected.Res.StatusCode), "reason": rejected.Res.Reason}
	}
	if response := dialog.InviteResponse; response != nil && response.StatusCode >= 300 {
		return map[string]any{"state": "ended", "ended_by": "app", "cause": "rejected",
			"sip_status": int(response.StatusCode), "reason": response.Reason}
	}
	return map[string]any{"state": "failed", "error": "INVITE: " + err.Error()}
}

func (c *carrierSimCall) mediaConfig() sipGatewayConfig {
	return sipGatewayConfig{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, SRTPMode: sipSRTPDisabled}
}

func (c *carrierSimCall) bye(dialog *sipgo.DialogClientSession, request carrierSimHangup) error {
	if dialog.LoadState() == sip.DialogStateEnded {
		return nil
	}
	recipient := dialog.InviteRequest.Recipient
	if contact := dialog.InviteResponse.Contact(); contact != nil {
		recipient = contact.Address
	}
	bye := sip.NewRequest(sip.BYE, *recipient.Clone())
	bye.AppendHeader(sip.NewHeader("Reason", fmt.Sprintf("Q.850;cause=%d;text=%q", request.Q850, request.Cause)))
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return dialog.WriteBye(ctx, bye)
}

func sipReasonHeader(req *sip.Request) map[string]any {
	header := req.GetHeader("Reason")
	if header == nil {
		return nil
	}
	value := header.Value()
	out := map[string]any{"reason": value}
	for _, part := range strings.Split(value, ";") {
		key, raw, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		raw = strings.Trim(raw, `"`)
		switch strings.ToLower(key) {
		case "cause":
			if code, err := strconv.Atoi(raw); err == nil {
				out["q850"] = code
			}
		case "text":
			out["cause"] = raw
		}
	}
	return out
}

func (c *carrierSimCall) send(ctx context.Context, remote *net.UDPAddr, answer sipMediaOffer) {
	samples := c.frameSamples()
	encoder := (*g722.Encoder)(nil)
	if answer.Codec == "G722" {
		encoder = g722.NewEncoder(g722.Rate64000, 0)
	}
	encode := func(pcm []int16) []byte {
		if encoder == nil {
			return encodeSIPG711(pcm, answer.Codec)
		}
		out := make([]byte, len(pcm)/2)
		return out[:encoder.Encode(out, pcm)]
	}
	ssrc := uint32(time.Now().UnixNano())
	sequence := uint16(ssrc >> 7)
	timestamp := ssrc << 3
	write := func(packet *rtp.Packet) bool {
		raw, err := packet.Marshal()
		if err == nil {
			_, err = c.rtp.WriteToUDP(raw, remote)
		}
		return err == nil
	}
	var digits string
	var tick int
	var eventStamp uint32
	ticker := time.NewTicker(carrierSimFrame)
	defer ticker.Stop()
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if digits == "" {
			select {
			case digits = <-c.dtmfCh:
				tick = 0
			default:
			}
		}
		if digits != "" {
			// RFC 4733: 8 packets (160 ms) per digit, the last three marked
			// end, then 40 ms of silence before the next digit.
			if tick < 8 {
				if tick == 0 {
					eventStamp = timestamp
				}
				event := strings.IndexByte("0123456789*#ABCD", digits[0])
				payload := []byte{byte(event), 10, 0, 0}
				if tick >= 5 {
					payload[1] |= 0x80
				}
				binary.BigEndian.PutUint16(payload[2:], uint16(min(tick+1, 6)*160))
				c.mu.Lock()
				pt := c.dtmfPT
				c.mu.Unlock()
				packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: uint8(pt), SequenceNumber: sequence,
					Timestamp: eventStamp, SSRC: ssrc, Marker: tick == 0}, Payload: payload}
				if write(packet) {
					sequence++
				}
				tick++
				if tick == 8 {
					c.mu.Lock()
					c.dtmfSent += digits[:1]
					c.fields["dtmf_sent"] = c.dtmfSent
					c.changed = true
					c.mu.Unlock()
				}
				timestamp += 160
				continue
			}
			tick++
			if tick >= 10 {
				digits, tick = digits[1:], 0
			}
		}
		frame := c.nextFrame(samples)
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: answer.PayloadType, SequenceNumber: sequence,
			Timestamp: timestamp, SSRC: ssrc, Marker: first}, Payload: encode(frame)}
		if write(packet) {
			sequence++
			first = false
			c.mu.Lock()
			c.stats.SentPackets++
			c.stats.SentMS += int64(carrierSimFrame / time.Millisecond)
			c.changed = true
			c.mu.Unlock()
		}
		timestamp += 160 // G.722's RTP clock is 8 kHz too (RFC 3551).
	}
}

func (c *carrierSimCall) frameSamples() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate * int(carrierSimFrame/time.Millisecond) / 1000
}

// nextFrame returns the next 20 ms of caller audio, or silence.
func (c *carrierSimCall) nextFrame(samples int) []int16 {
	frame := make([]int16, samples)
	c.mu.Lock()
	defer c.mu.Unlock()
	filled := 0
	for filled < samples && len(c.play) > 0 {
		n := copy(frame[filled:], c.play[0])
		filled += n
		c.queuedPCM -= n
		if n == len(c.play[0]) {
			c.play = c.play[1:]
			if len(c.playNames) > 0 {
				played, _ := c.fields["audio_played"].([]string)
				c.fields["audio_played"] = append(played, c.playNames[0])
				c.playNames = c.playNames[1:]
				c.fields["audio_queued"] = append([]string(nil), c.playNames...)
			}
		} else {
			c.play[0] = c.play[0][n:]
		}
	}
	return frame
}

func (c *carrierSimCall) receive(ctx context.Context, answer sipMediaOffer) {
	decoder := (*g722.Decoder)(nil)
	rate := 8000
	if answer.Codec == "G722" {
		decoder = g722.NewDecoder(g722.Rate64000, 0)
		rate = 16000
	}
	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		_ = c.rtp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := c.rtp.ReadFromUDP(buf)
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				continue
			}
			return
		}
		var packet rtp.Packet
		if packet.Unmarshal(buf[:n]) != nil || packet.PayloadType != answer.PayloadType {
			continue
		}
		var pcm []int16
		if decoder != nil {
			pcm = make([]int16, len(packet.Payload)*2)
			pcm = pcm[:decoder.Decode(pcm, packet.Payload)]
		} else {
			pcm = decodeSIPG711(packet.Payload, answer.Codec)
		}
		c.observe(pcm, rate)
	}
}

func (c *carrierSimCall) observe(pcm []int16, rate int) {
	if len(pcm) == 0 {
		return
	}
	var squares float64
	peak := 0
	for _, sample := range pcm {
		value := int(sample)
		if value < 0 {
			value = -value
		}
		peak = max(peak, value)
		squares += float64(sample) * float64(sample)
	}
	ms := int64(len(pcm) * 1000 / rate)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &c.stats
	s.ReceivedPackets++
	s.ReceivedMS += ms
	if math.Sqrt(squares/float64(len(pcm))) >= carrierSimSpeechRMS {
		s.NonSilentMS += ms
	}
	s.Peak = max(s.Peak, peak)
	s.sumSquares += squares
	s.samples += int64(len(pcm))
	s.RMS = int(math.Round(math.Sqrt(s.sumSquares / float64(s.samples))))
	c.changed = true
}

func (c *carrierSimCall) drainRTCP(ctx context.Context) {
	buf := make([]byte, 1500)
	for ctx.Err() == nil {
		_ = c.rtcp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, _, err := c.rtcp.ReadFromUDP(buf); err != nil {
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				return
			}
		}
	}
}

func (c *carrierSimCall) finish(fields map[string]any) {
	if !c.finalSent.CompareAndSwap(false, true) {
		return
	}
	c.mu.Lock()
	c.ended = true
	c.mu.Unlock()
	fields["ended_at"] = carrierSimNow()
	label, _ := fields["state"].(string)
	c.report(label, fields)
}

// report merges fields into the record and sends the current state. With no
// fields and no label it is the periodic report, sent only when something
// changed.
func (c *carrierSimCall) report(label string, fields map[string]any) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.mu.Lock()
	for key, value := range fields {
		c.fields[key] = value
	}
	if label == "" && len(fields) == 0 && !c.changed {
		c.mu.Unlock()
		return
	}
	c.changed = false
	if c.answered {
		stats := c.stats
		c.fields["rtp"] = map[string]any{
			"sent_packets": stats.SentPackets, "sent_ms": stats.SentMS,
			"received_packets": stats.ReceivedPackets, "received_ms": stats.ReceivedMS,
			"non_silent_ms": stats.NonSilentMS, "peak": stats.Peak, "rms": stats.RMS,
		}
	}
	snapshot := make(map[string]any, len(c.fields))
	for key, value := range c.fields {
		if key != "bye_reason" {
			snapshot[key] = value
		}
	}
	c.mu.Unlock()
	err := c.reporter.ReportIntegrationSimulation(sdk.IntegrationSimulationReport{
		Integration: c.event.Integration, ConnectionID: c.event.ConnectionID,
		Collection: c.event.Collection, ID: c.event.ID, Event: label, Fields: snapshot,
	})
	if err != nil && c.logger != nil {
		c.logger.Warn("simulated carrier report failed", "id", c.event.ID, "event", label, "err", err)
	}
}

func carrierSimNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
