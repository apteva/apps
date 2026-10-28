package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
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
)

var didwwOutboundHost = regexp.MustCompile(`^[a-z0-9-]+(?:\.[a-z0-9-]+)*\.out\.didww\.com$`)

type didwwCarrier struct {
	app            *App
	connID         int64
	fields         map[string]string
	timeoutSec     int
	maxDurationSec int
}

func (c *didwwCarrier) Slug() string { return "didww" }

func (c *didwwCarrier) Place(_ *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	if _, err := didwwOutboundSettings(c.fields); err != nil {
		return nil, err
	}
	if !validE164(req.To) || !validE164(req.From) || req.CallID == "" {
		return nil, errors.New("DIDWW outbound call requires E.164 destination, caller ID, and call ID")
	}
	if req.MachineDetection != "" && req.MachineDetection != machineDetectionOff {
		return nil, errors.New("DIDWW direct SIP does not support answering machine detection")
	}
	c.timeoutSec = req.TimeoutSec
	c.maxDurationSec = req.MaxDurationSec
	// The provider's REST API cannot place a call. A local ID is returned now;
	// StartPlacedCall sends INVITE only once the call row is durable.
	return &carrierPlaceResult{CarrierSID: req.CallID}, nil
}

func (c *didwwCarrier) StartPlacedCall(ctx *sdk.AppCtx, row *callRow) error {
	settings, err := didwwOutboundSettings(c.fields)
	if err != nil {
		return err
	}
	if err := c.app.ensureSIPGateway(ctx); err != nil {
		return fmt.Errorf("start DIDWW SIP gateway: %w", err)
	}
	g := c.app.directSIPGateway()
	if g == nil {
		return errors.New("DIDWW SIP gateway is unavailable")
	}
	return g.startDIDWWOutbound(row, settings, c.timeoutSec, c.maxDurationSec)
}

func (c *didwwCarrier) Hangup(_ *sdk.AppCtx, row *callRow) error {
	return hangupOutboundSIP(c.app, row)
}

type didwwSIPSettings struct {
	anchorUsername           string
	host, username, password string
	port                     int
	secure                   bool
	codecs                   []string
	scheme                   string
	e164                     bool
}

func didwwOutboundSettings(fields map[string]string) (didwwSIPSettings, error) {
	host := strings.ToLower(strings.TrimSpace(fields["sip_host"]))
	username := strings.TrimSpace(fields["sip_username"])
	password := fields["sip_password"]
	if !didwwOutboundHost.MatchString(host) || len(host) > 253 {
		return didwwSIPSettings{}, errors.New("DIDWW sip_host must be a DIDWW outbound signaling hostname")
	}
	if username == "" || password == "" || len(username) > 128 || strings.ContainsAny(username, "\r\n\x00") {
		return didwwSIPSettings{}, errors.New("DIDWW SIP username and password are required")
	}
	if mode := strings.TrimSpace(fields["sip_transport"]); mode != "" && !strings.EqualFold(mode, "tls") {
		return didwwSIPSettings{}, errors.New("DIDWW outbound SIP currently requires TLS signaling")
	}
	secure := true
	switch strings.ToLower(strings.TrimSpace(fields["sip_media_encryption"])) {
	case "", "srtp_sdes":
	case "disabled":
		secure = false
	default:
		return didwwSIPSettings{}, errors.New("DIDWW SIP media encryption must be srtp_sdes or disabled")
	}
	port := 5061
	if value := strings.TrimSpace(fields["sip_port"]); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			return didwwSIPSettings{}, errors.New("DIDWW sip_port is invalid")
		}
		port = parsed
	}
	return didwwSIPSettings{host: host, username: username, password: password, port: port, secure: secure}, nil
}

type outboundSIPSession struct {
	playbackPolicy sipPlaybackPolicy
	diagnostics    *sipLiveDiagnostics
	gateway        *sipGateway
	callID         string
	providerCallID string
	carrierSlug    string // immutable provider identity for regional dialog TLS
	dialog         *sipgo.DialogClientSession
	media          *sipRTPMedia
	localKey       []byte
	settings       didwwSIPSettings
	ctx            context.Context
	cancel         context.CancelFunc
	dialDeadline   time.Time
	maxDuration    time.Duration
	peerEnded      atomic.Bool
	ended          atomic.Bool
	mediaReady     atomic.Bool
	signalMu       sync.Mutex
	timerMu        sync.Mutex
	timer          sipSessionTimer
	timerUpdated   time.Time
	timerWake      chan struct{}
	localSDP       []byte
	remoteSeq      atomic.Uint32
	refreshACK     chan struct{}
	refreshCSeq    uint32
	inviteDone     chan struct{}
	cleanupOnce    sync.Once
	terminationErr error // published by closing inviteDone
}

func (g *sipGateway) outboundSessionByCall(callID string) *outboundSIPSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.outboundByCall[callID]
}

func (g *sipGateway) outboundSessionByProviderCall(providerCallID string) *outboundSIPSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.outboundByProviderCall[providerCallID]
}

func (g *sipGateway) startDIDWWOutbound(row *callRow, settings didwwSIPSettings, timeoutSec, maxDurationSec int) error {
	if row == nil || row.CarrierSlug != "didww" {
		return errors.New("invalid DIDWW outbound call")
	}
	return g.startSIPOutbound(row, settings, timeoutSec, maxDurationSec)
}

func (g *sipGateway) startSIPOutbound(row *callRow, settings didwwSIPSettings, timeoutSec, maxDurationSec int) error {
	if row == nil || row.ID == "" || row.Direction != "outbound" || (row.CarrierSlug != "didww" && row.CarrierSlug != "telnyx" && row.CarrierSlug != "twilio") {
		return errors.New("invalid outbound SIP call")
	}
	if len(settings.codecs) == 0 {
		settings.codecs = []string{"PCMU"}
	}
	if err := validateLocalMediaSettings(localMediaSettings{Codecs: settings.codecs}); err != nil {
		return err
	}
	if settings.scheme == "" {
		settings.scheme = "sips"
	}

	if g.cfg.Transport != "tls" || (g.cfg.SRTPMode == sipSRTPRequired && !settings.secure) ||
		(g.cfg.SRTPMode == sipSRTPDisabled && settings.secure) {
		return errors.New("Outbound SIP TLS/SRTP settings conflict with the SIP gateway policy")
	}
	mediaSettings, err := g.app.localMediaSettingsForCall(row.ProjectID, row.RouteID, "outbound", row.CarrierConnectionID, row.FromNumber)
	if err != nil {
		return fmt.Errorf("load outbound SIP media settings: %w", err)
	}
	if err := validateLocalMediaSettings(mediaSettings); err != nil {
		return err
	}
	playbackPolicy := sipPlaybackPolicy{}
	if mediaSettings.RTPPlayback != nil {
		playbackPolicy = *mediaSettings.RTPPlayback
	}
	if mediaSettings.AudioProfileCodecs {
		if row.CarrierSlug == "twilio" {
			for _, codec := range mediaSettings.Codecs {
				if codec == "G722" {
					return errors.New("Twilio outbound SIP audio profile must use PCMU/PCMA")
				}
			}
		}
		settings.codecs = append([]string(nil), mediaSettings.Codecs...)
	}
	g.mu.Lock()
	if g.stopping || g.outboundByCall[row.ID] != nil || g.outboundReserved[row.ID] {
		g.mu.Unlock()
		return errors.New("Outbound SIP gateway stopping or call already active")
	}
	if len(g.byCall)+len(g.reserved)+len(g.outboundByCall)+len(g.outboundReserved) >= g.cfg.MaxSessions {
		g.mu.Unlock()
		return errors.New("SIP gateway call capacity reached")
	}
	if g.outboundReserved == nil {
		g.outboundReserved = make(map[string]bool)
	}
	ctx, cancel := context.WithCancel(g.ctx)
	if g.outboundPending == nil {
		g.outboundPending = map[string]context.CancelFunc{}
	}
	g.outboundPending[row.ID] = cancel
	g.outboundReserved[row.ID] = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.outboundReserved, row.ID)
		delete(g.outboundPending, row.ID)
		g.mu.Unlock()
	}()
	if !g.startWork() {
		cancel()
		return errors.New("SIP gateway stopping")
	}
	workOwned := true
	defer func() {
		if workOwned {
			cancel()
			g.finishWork()
		}
	}()
	localKey := []byte(nil)
	if settings.secure {
		localKey = make([]byte, 30)
		if _, err := rand.Read(localKey); err != nil {
			return fmt.Errorf("generate outbound SRTP key: %w", err)
		}
	}
	placeholder := sipMediaOffer{RemoteAddress: netip.MustParseAddr("127.0.0.1"), RemotePort: 9, PayloadType: 0,
		Codec: "PCMU", PacketSamples: 160}
	media, err := openSIPRTPMedia(g.cfg, placeholder)
	if err != nil {
		return err
	}
	media.startEarlyReceiver()
	body, err := buildOutboundSIPOffer(g.cfg, settings, media.localPort, localKey)
	if err != nil {
		media.Close()
		return err
	}
	params := sip.NewParams()
	params.Add("transport", "tls")
	to, fromNumber := compactPhoneNumber(row.ToNumber), compactPhoneNumber(row.FromNumber)
	if settings.e164 {
		to, fromNumber = row.ToNumber, row.FromNumber
	}
	recipient := sip.Uri{Scheme: settings.scheme, User: to, Host: settings.host,
		Port: settings.port, UriParams: params}
	fromParams := sip.NewParams()
	fromParams.Add("tag", sip.GenerateTagN(16))
	from := &sip.FromHeader{Address: sip.Uri{Scheme: settings.scheme, User: fromNumber, Host: g.cfg.PublicHost}, Params: fromParams}
	dialDeadline := time.Now().Add(time.Duration(max(5, min(timeoutSec, 120))) * time.Second)
	inviteCtx, stopInvite := context.WithDeadline(ctx, dialDeadline)
	defer stopInvite()
	headers := []sip.Header{from, sip.NewHeader("Content-Type", "application/sdp"), sip.NewHeader("Supported", "timer"), sip.NewHeader("Session-Expires", "1800;refresher=uac")}
	if row.CarrierSlug == "telnyx" && settings.anchorUsername != "" {
		headers = append(headers, sip.NewHeader("X-Telnyx-Username", settings.anchorUsername))
	}
	dialog, err := g.outboundDialogs.Invite(inviteCtx, recipient, body, headers...)
	if err != nil {
		cancel()
		media.Close()
		return fmt.Errorf("Outbound SIP INVITE: %w", err)
	}
	providerCallID := sipCallID(dialog.InviteRequest)
	if providerCallID == "" {
		cancel()
		media.Close()
		_ = dialog.Close()
		return errors.New("Outbound SIP INVITE has no Call-ID")
	}
	s := &outboundSIPSession{playbackPolicy: playbackPolicy, gateway: g, callID: row.ID, providerCallID: providerCallID, carrierSlug: row.CarrierSlug,
		dialog: dialog, media: media, localKey: localKey, settings: settings, ctx: ctx, cancel: cancel,
		timerWake: make(chan struct{}, 1), dialDeadline: dialDeadline,
		maxDuration: time.Duration(max(60, min(maxDurationSec, maximumConnectedDurationSec))) * time.Second,
		inviteDone:  make(chan struct{}), localSDP: append([]byte(nil), body...)}
	s.diagnostics = g.app.newSIPDiagnostics(row, dialog.ID)
	s.diagnostics.event("sip_invite_sent", "local", "", 0)
	g.mu.Lock()
	if g.outboundByCall[row.ID] != nil {
		g.mu.Unlock()
		s.diagnostics.end("local", errors.New("duplicate_outbound_call"))
		cancel()
		media.Close()
		_ = dialog.Close()
		return errors.New("outbound SIP call already active")
	}
	delete(g.outboundReserved, row.ID)
	g.outboundByCall[row.ID] = s
	g.outboundByProviderCall[providerCallID] = s
	g.mu.Unlock()
	current, findErr := g.app.db().findCall(row.ID)
	identityErr := g.app.db().updateCarrierIdentity(row.ID, providerCallID, "")
	go func() { defer g.finishWork(); s.waitAnswer() }()
	workOwned = false
	if findErr != nil || current == nil || isTerminalStatus(current.Status) || ctx.Err() != nil {
		s.finish("local", nil)
		return errors.New("outbound SIP call ended during placement")
	}
	if identityErr != nil {
		s.finish("local_error", fmt.Errorf("persist outbound SIP Call-ID: %w", identityErr))
		return fmt.Errorf("persist outbound SIP Call-ID: %w", identityErr)
	}
	row.CarrierSID = providerCallID
	if g.ctx.Err() != nil {
		s.finish("local_error", errors.New("SIP gateway stopping"))
		return errors.New("SIP gateway stopping")
	}
	return nil
}

func (s *outboundSIPSession) waitAnswer() {
	defer func() {
		// Only this signaling goroutine sends BYE. A concurrent hangup can
		// arrive between the 200 response and ACK; sending BYE from that
		// goroutine would race the ACK and could send it twice.
		if s.ended.Load() && s.dialog.LoadState() == sip.DialogStateConfirmed {
			byeCtx, stopBye := context.WithTimeout(context.Background(), 3*time.Second)
			s.diagnostics.event("sip_bye_requested", "local", "call_ended", 0)
			s.signalMu.Lock()
			err := s.dialog.Bye(byeCtx)
			s.terminationErr = sipTerminationResult(s.diagnostics, err,
				s.peerEnded.Load() || s.dialog.LoadState() == sip.DialogStateEnded)
			s.signalMu.Unlock()

			stopBye()
		}
		close(s.inviteDone)
		if s.ended.Load() {
			s.cleanup()
		}
	}()
	dialCtx, stopDial := context.WithDeadline(s.ctx, s.dialDeadline)
	defer stopDial()
	err := s.dialog.WaitAnswer(dialCtx, sipgo.AnswerOptions{
		Username: s.settings.username, Password: s.settings.password,
		OnResponse: func(response *sip.Response) error {
			if response != nil {
				s.diagnostics.event("sip_invite_response", "carrier", "", response.StatusCode)
			}
			if response != nil && response.IsProvisional() && len(response.Body()) > 0 {
				// Validate for diagnostics only. The final answer independently
				// negotiates codec/source/keys; early audio is intentionally drained.
				_, e := parseSIPMediaOfferCodecs(response.Body(), s.gateway.cfg, s.settings.codecs)
				detail := "local_ringback_early_media_drained"
				if e != nil {
					detail = "unsupported_provisional_media_drained"
				}
				s.diagnostics.event("sip_early_media", "carrier", detail, response.StatusCode)
			}
			if response != nil && response.IsProvisional() && response.StatusCode >= 180 {
				_ = s.gateway.app.db().updateStatus(s.callID, "ringing", "")
			}
			return nil
		},
	})
	if err != nil {
		// A 2xx that crossed our CANCEL (hangup or dial timeout while ringing)
		// is stored by sipgo while WaitAnswer still returns the context error.
		// RFC 3261 §9.1: ACK it, then the deferred BYE ends the answered dialog.
		if response := s.dialog.InviteResponse; response != nil && response.IsSuccess() {
			s.diagnostics.event("sip_cancel_crossed_answer", "carrier", "", response.StatusCode)
			ackCtx, stopAck := context.WithTimeout(context.Background(), 3*time.Second)
			if ackErr := s.dialog.Ack(ackCtx); ackErr == nil {
				s.diagnostics.event("sip_ack", "local", "", 0)
			}
			stopAck()
		}
		status := "failed"
		if errors.Is(dialCtx.Err(), context.DeadlineExceeded) {
			status = "no-answer"
		} else if response := s.dialog.InviteResponse; response != nil {
			switch response.StatusCode {
			case sip.StatusBusyHere:
				status = "busy"
			case sip.StatusRequestTimeout, sip.StatusTemporarilyUnavailable:
				status = "no-answer"
			}
		}
		if response := s.dialog.InviteResponse; response != nil && !response.IsSuccess() {
			s.gateway.app.db().observeAccountSIP(s.callID, response.StatusCode, response.Reason)
		}
		s.finishWithStatus("carrier", status, err)
		return
	}
	answer, err := parseSIPMediaOfferCodecs(s.dialog.InviteResponse.Body(), s.gateway.cfg, s.settings.codecs)
	if err == nil && (!answer.Secure && s.settings.secure || answer.Secure && !s.settings.secure ||
		answer.PayloadType != map[string]uint8{"G722": 9, "PCMU": 0, "PCMA": 8}[answer.Codec] || answer.DTMFPayloadType >= 0 && answer.DTMFPayloadType != 101 || answer.Secure && answer.CryptoTag != "1") {
		err = errors.New("outbound SIP answered with media that was not offered")
	}
	if err == nil && answer.Secure {
		s.media.security, err = newSIPMediaSecurityWithLocalKey(answer, s.localKey)
	}
	if err != nil {
		ackCtx, stopAck := context.WithTimeout(context.Background(), 3*time.Second)
		_ = s.dialog.Ack(ackCtx)
		stopAck()
		s.finishWithStatus("local_error", "failed", err)
		return
	}
	s.diagnostics.codecs(s.dialog.InviteResponse.Body(), s.settings.codecs, answer.Codec, "outbound_final_answer")
	s.media.offer = answer
	s.media.remote = net.UDPAddrFromAddrPort(netip.AddrPortFrom(answer.RemoteAddress, uint16(answer.RemotePort)))
	if s.media.early != nil {
		s.media.early.answered.Store(true)
	}
	s.diagnostics.attach(s.media, s.dialog.ID)
	s.diagnostics.event("sip_answer", "carrier", "", 200)
	ackCtx, stopAck := context.WithTimeout(context.Background(), 3*time.Second)
	err = s.dialog.Ack(ackCtx)
	stopAck()
	if err != nil {
		s.finishWithStatus("local_error", "failed", err)
		return
	}
	s.diagnostics.event("sip_ack", "local", "", 0)
	s.mediaReady.Store(true)
	sessionTimer, timerErr := outboundSIPTimer(s.dialog.InviteResponse, sipSessionTimer{})
	if timerErr != nil {
		s.finishWithStatus("local_error", "failed", timerErr)
		return
	}
	s.timerMu.Lock()
	s.timer = sessionTimer
	s.timerUpdated = time.Now()
	s.timerMu.Unlock()
	if !s.gateway.runTask(s.runSessionTimer) {
		s.finish("local_error", errors.New("SIP gateway stopping"))
		return
	}
	if s.ended.Load() {
		return
	}
	_, _ = s.gateway.app.db().updateStatusWithFacts(s.callID, "in-progress", "", lifecycleFacts{Source: "provider", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if !s.gateway.runTask(func() {
		s.gateway.app.bridgeSIPMedia(sipBridgeSession{
			playbackPolicy: s.playbackPolicy, callID: s.callID, ctx: s.ctx, media: s.media, finish: s.finish, hangup: s.hangup,
		})
	}) {
		s.finishWithStatus("local_error", "failed", errors.New("SIP gateway stopping"))
		return
	}
	timer := time.NewTimer(s.maxDuration)
	defer timer.Stop()
	select {
	case <-timer.C:
		_, _ = s.gateway.app.db().db.Exec(`UPDATE calls SET termination_reason='time_limit',termination_cause='max_duration',termination_initiator='telephony' WHERE id=? AND status NOT IN ('completed','failed','busy','no-answer','canceled')`, s.callID)
		s.finish("local", nil)
	case <-s.ctx.Done():
	}
}

func (s *outboundSIPSession) hangup() error {
	s.finish("local", nil)
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case <-s.inviteDone:
		return s.terminationErr
	case <-timer.C:
		return errors.New("outbound SIP termination not confirmed within deadline")
	}
}

func (s *outboundSIPSession) finish(leg string, cause error) {
	s.finishWithStatus(leg, "", cause)
}

func (s *outboundSIPSession) finishWithStatus(leg, status string, cause error) {
	if !s.ended.CompareAndSwap(false, true) {
		return
	}
	s.diagnostics.end(leg, cause)
	s.cancel()
	go func() {
		<-s.inviteDone
		s.cleanup()
	}()
	row, err := s.gateway.app.db().findCall(s.callID)
	if err != nil || row == nil {
		return
	}
	if !isTerminalStatus(row.Status) {
		if status == "" {
			status = "completed"
			if row.Status == "initiated" || row.Status == "ringing" {
				status = "canceled"
			}
			if cause != nil {
				status = "failed"
			}
		}
		message := ""
		if cause != nil {
			message = cause.Error()
		}
		_ = s.gateway.app.db().updateStatus(s.callID, status, message)
	}
	_ = s.gateway.app.killCallThread(s.gateway.appCtx.WithProject(row.ProjectID), row)
}

func (s *outboundSIPSession) cleanup() {
	s.cleanupOnce.Do(func() {
		if s.media != nil {
			s.media.Close()
		}
		if s.dialog != nil {
			_ = s.dialog.Close()
		}
		s.gateway.mu.Lock()
		delete(s.gateway.outboundByCall, s.callID)
		delete(s.gateway.outboundByProviderCall, s.providerCallID)
		s.gateway.mu.Unlock()
	})
}
