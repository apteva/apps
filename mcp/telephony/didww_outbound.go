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
	if req.RecordingMode == recordingModeAlways {
		return nil, errors.New("DIDWW direct SIP has no provider-cloud recording")
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
	if row == nil {
		return errors.New("DIDWW call is missing")
	}
	g := c.app.directSIPGateway()
	if g == nil {
		return errors.New("DIDWW SIP gateway is unavailable")
	}
	s := g.outboundSessionByCall(row.ID)
	if s == nil && row.ID == "" {
		s = g.outboundSessionByProviderCall(row.CarrierSID)
	}
	if s == nil {
		return errors.New("DIDWW SIP dialog is no longer active")
	}
	s.finish("local", nil)
	return nil
}

type didwwSIPSettings struct {
	host, username, password string
	port                     int
	secure                   bool
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
	gateway        *sipGateway
	callID         string
	providerCallID string
	dialog         *sipgo.DialogClientSession
	media          *sipRTPMedia
	localKey       []byte
	settings       didwwSIPSettings
	ctx            context.Context
	cancel         context.CancelFunc
	dialTimeout    time.Duration
	maxDuration    time.Duration
	ended          atomic.Bool
	inviteDone     chan struct{}
	cleanupOnce    sync.Once
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
	if row == nil || row.ID == "" || row.CarrierSlug != "didww" {
		return errors.New("invalid DIDWW outbound call")
	}
	if g.cfg.Transport != "tls" || (g.cfg.SRTPMode == sipSRTPRequired && !settings.secure) ||
		(g.cfg.SRTPMode == sipSRTPDisabled && settings.secure) {
		return errors.New("DIDWW outbound TLS/SRTP settings conflict with the SIP gateway policy")
	}
	g.mu.Lock()
	if g.stopping || g.outboundByCall[row.ID] != nil || g.outboundReserved[row.ID] {
		g.mu.Unlock()
		return errors.New("DIDWW SIP gateway stopping or call already active")
	}
	if len(g.byCall)+len(g.reserved)+len(g.outboundByCall)+len(g.outboundReserved) >= g.cfg.MaxSessions {
		g.mu.Unlock()
		return errors.New("SIP gateway call capacity reached")
	}
	if g.outboundReserved == nil {
		g.outboundReserved = make(map[string]bool)
	}
	g.outboundReserved[row.ID] = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.outboundReserved, row.ID)
		g.mu.Unlock()
	}()
	if !g.startWork() {
		return errors.New("SIP gateway stopping")
	}
	workOwned := true
	defer func() {
		if workOwned {
			g.work.Done()
		}
	}()
	localKey := []byte(nil)
	if settings.secure {
		localKey = make([]byte, 30)
		if _, err := rand.Read(localKey); err != nil {
			return fmt.Errorf("generate DIDWW SRTP key: %w", err)
		}
	}
	placeholder := sipMediaOffer{RemoteAddress: netip.MustParseAddr("127.0.0.1"), RemotePort: 9, PayloadType: 0,
		Codec: "PCMU", PacketSamples: 160}
	media, err := openSIPRTPMedia(g.cfg, placeholder)
	if err != nil {
		return err
	}
	offer := placeholder
	offer.Secure = settings.secure
	offer.CryptoTag = "1"
	body, err := buildSIPMediaAnswer(g.cfg, offer, media.localPort, &sipMediaSecurity{LocalKey: localKey})
	if err != nil {
		media.Close()
		return err
	}
	params := sip.NewParams()
	params.Add("transport", "tls")
	recipient := sip.Uri{Scheme: "sips", User: compactPhoneNumber(row.ToNumber), Host: settings.host,
		Port: settings.port, UriParams: params}
	fromParams := sip.NewParams()
	fromParams.Add("tag", sip.GenerateTagN(16))
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sips", User: compactPhoneNumber(row.FromNumber), Host: g.cfg.PublicHost}, Params: fromParams}
	ctx, cancel := context.WithCancel(g.ctx)
	dialog, err := g.outboundDialogs.Invite(ctx, recipient, body, from, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		cancel()
		media.Close()
		return fmt.Errorf("DIDWW SIP INVITE: %w", err)
	}
	providerCallID := sipCallID(dialog.InviteRequest)
	if providerCallID == "" {
		cancel()
		media.Close()
		_ = dialog.Close()
		return errors.New("DIDWW SIP INVITE has no Call-ID")
	}
	s := &outboundSIPSession{gateway: g, callID: row.ID, providerCallID: providerCallID,
		dialog: dialog, media: media, localKey: localKey, settings: settings, ctx: ctx, cancel: cancel,
		dialTimeout: time.Duration(max(5, min(timeoutSec, 120))) * time.Second,
		maxDuration: time.Duration(max(60, min(maxDurationSec, 14400))) * time.Second,
		inviteDone:  make(chan struct{})}
	g.mu.Lock()
	if g.outboundByCall[row.ID] != nil {
		g.mu.Unlock()
		cancel()
		media.Close()
		_ = dialog.Close()
		return errors.New("DIDWW call already active")
	}
	delete(g.outboundReserved, row.ID)
	g.outboundByCall[row.ID] = s
	g.outboundByProviderCall[providerCallID] = s
	g.mu.Unlock()
	identityErr := g.app.db().updateCarrierIdentity(row.ID, providerCallID, "")
	go func() { defer g.work.Done(); s.waitAnswer() }()
	workOwned = false
	if identityErr != nil {
		s.finish("local_error", fmt.Errorf("persist DIDWW SIP Call-ID: %w", identityErr))
		return fmt.Errorf("persist DIDWW SIP Call-ID: %w", identityErr)
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
			_ = s.dialog.Bye(byeCtx)
			stopBye()
		}
		close(s.inviteDone)
		if s.ended.Load() {
			s.cleanup()
		}
	}()
	dialCtx, stopDial := context.WithTimeout(s.ctx, s.dialTimeout)
	defer stopDial()
	err := s.dialog.WaitAnswer(dialCtx, sipgo.AnswerOptions{
		Username: s.settings.username, Password: s.settings.password,
		OnResponse: func(response *sip.Response) error {
			if response != nil && response.IsProvisional() && response.StatusCode >= 180 {
				_ = s.gateway.app.db().updateStatus(s.callID, "ringing", "")
			}
			return nil
		},
	})
	if err != nil {
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
		s.finishWithStatus("carrier", status, err)
		return
	}
	answer, err := parseSIPMediaOffer(s.dialog.InviteResponse.Body(), s.gateway.cfg)
	if err == nil && (!answer.Secure && s.settings.secure || answer.Secure && !s.settings.secure ||
		answer.Codec != "PCMU" || answer.PayloadType != 0 || answer.Secure && answer.CryptoTag != "1") {
		err = errors.New("DIDWW answered with media that was not offered")
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
	s.media.offer = answer
	s.media.remote = net.UDPAddrFromAddrPort(netip.AddrPortFrom(answer.RemoteAddress, uint16(answer.RemotePort)))
	ackCtx, stopAck := context.WithTimeout(context.Background(), 3*time.Second)
	err = s.dialog.Ack(ackCtx)
	stopAck()
	if err != nil {
		s.finishWithStatus("local_error", "failed", err)
		return
	}
	if s.ended.Load() {
		return
	}
	_ = s.gateway.app.db().updateStatus(s.callID, "in-progress", "")
	if !s.gateway.runTask(func() {
		s.gateway.app.bridgeSIPMedia(sipBridgeSession{
			callID: s.callID, ctx: s.ctx, media: s.media, finish: s.finish, hangup: s.hangup,
		})
	}) {
		s.finishWithStatus("local_error", "failed", errors.New("SIP gateway stopping"))
		return
	}
	timer := time.NewTimer(s.maxDuration)
	defer timer.Stop()
	select {
	case <-timer.C:
		s.finish("local", nil)
	case <-s.ctx.Done():
	}
}

func (s *outboundSIPSession) hangup() error {
	s.finish("local", nil)
	return nil
}

func (s *outboundSIPSession) finish(leg string, cause error) {
	s.finishWithStatus(leg, "", cause)
}

func (s *outboundSIPSession) finishWithStatus(leg, status string, cause error) {
	if !s.ended.CompareAndSwap(false, true) {
		return
	}
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
