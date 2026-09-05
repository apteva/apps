package main

import (
	"context"
	"errors"
	"fmt"
	"net"
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

var e164InSIPValue = regexp.MustCompile(`\+[1-9][0-9]{7,14}`)

type sipGateway struct {
	app     *App
	appCtx  *sdk.AppCtx
	cfg     sipGatewayConfig
	ua      *sipgo.UserAgent
	client  *sipgo.Client
	server  *sipgo.Server
	dialogs *sipgo.DialogServerCache
	ctx     context.Context
	cancel  context.CancelFunc

	mu             sync.RWMutex
	byProviderCall map[string]*sipSession
	byCall         map[string]*sipSession
	reserved       map[string]bool
	ackTimeout     time.Duration
	work           sync.WaitGroup
	stopping       bool
	stopDeadline   time.Time
}

type sipSession struct {
	gateway *sipGateway
	dialog  *sipgo.DialogServerSession
	route   *routeRow
	call    *callRow
	offer   sipMediaOffer
	media   *sipRTPMedia
	ctx     context.Context
	cancel  context.CancelFunc

	answerMu      sync.Mutex
	ended         atomic.Bool
	mediaOnce     sync.Once
	inviteTx      sip.ServerTransaction
	initialACK    chan struct{}
	ackOnce       sync.Once
	signalMu      sync.Mutex
	refreshMu     sync.Mutex
	refreshACK    chan struct{}
	refreshCSeq   uint32
	remoteSeq     atomic.Uint32
	localSDP      []byte
	remoteTarget  sip.Uri
	timer         sipSessionTimer
	timerUpdated  time.Time
	refreshCancel context.CancelFunc
}

func (a *App) startSIPGateway(ctx *sdk.AppCtx) error {
	routeCount, err := a.db().directSIPRouteCount()
	if err != nil {
		return fmt.Errorf("count direct SIP routes: %w", err)
	}
	eager := configBool(ctx.Config(), "sip_enabled", "TELEPHONY_SIP_ENABLED", false) || routeCount > 0
	if !eager {
		return nil
	}
	return a.ensureSIPGateway(ctx)
}

func (a *App) ensureSIPGateway(ctx *sdk.AppCtx) error {
	if a.directSIPGateway() != nil {
		return nil
	}
	a.sip.startMu.Lock()
	defer a.sip.startMu.Unlock()
	if a.directSIPGateway() != nil {
		return nil
	}
	cfg, err := a.resolveSIPGatewayConfig(ctx, true)
	if err != nil {
		return err
	}
	gateway, err := newSIPGateway(a, ctx, cfg)
	if err != nil {
		return err
	}
	if err := gateway.Start(); err != nil {
		gateway.Stop()
		return err
	}
	a.sip.mu.Lock()
	a.sip.gateway = gateway
	a.sip.mu.Unlock()
	ctx.Logger().Info("direct SIP gateway listening",
		"transport", cfg.Transport, "listen", cfg.ListenAddress,
		"endpoint", cfg.endpointURI(), "srtp", cfg.SRTPMode)
	return nil
}

func (a *App) resolveSIPGatewayConfig(ctx *sdk.AppCtx, forceEnabled bool) (sipGatewayConfig, error) {
	publicURL := ""
	if a != nil {
		publicURL = a.publicBase()
	}
	if publicURL == "" && ctx != nil && ctx.PlatformAPI() != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil {
			publicURL = info.PublicURL
		}
	}
	return loadSIPGatewayConfigWithOptions(ctx.Config(), sipConfigOptions{
		ForceEnabled: forceEnabled,
		PublicURL:    publicURL,
	})
}

func (a *App) stopSIPGateway() {
	a.sip.mu.Lock()
	gateway := a.sip.gateway
	a.sip.gateway = nil
	a.sip.mu.Unlock()
	if gateway != nil {
		gateway.Stop()
	}
}

func (a *App) directSIPGateway() *sipGateway {
	a.sip.mu.RLock()
	defer a.sip.mu.RUnlock()
	return a.sip.gateway
}

func newSIPGateway(app *App, appCtx *sdk.AppCtx, cfg sipGatewayConfig) (*sipGateway, error) {
	cfg.certificate = &sipTLSCertificate{cfg: cfg}
	tlsConfig, err := cfg.tlsConfig()
	if err != nil {
		return nil, err
	}
	uaOptions := []sipgo.UserAgentOption{
		sipgo.WithUserAgent("Apteva-Telephony"),
		sipgo.WithUserAgentHostname(cfg.PublicHost),
	}
	if tlsConfig != nil {
		uaOptions = append(uaOptions, sipgo.WithUserAgenTLSConfig(tlsConfig))
	}
	ua, err := sipgo.NewUA(uaOptions...)
	if err != nil {
		return nil, err
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	_, portValue, _ := net.SplitHostPort(cfg.ListenAddress)
	port, _ := strconv.Atoi(portValue)
	params := sip.NewParams()
	params.Add("transport", cfg.Transport)
	contact := sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "apteva", Host: cfg.PublicHost, Port: port, UriParams: params,
	}}
	dialogs := sipgo.NewDialogServerCache(client, contact)
	gatewayCtx, cancel := context.WithCancel(context.Background())
	gateway := &sipGateway{
		app: app, appCtx: appCtx, cfg: cfg, ua: ua, client: client, server: server, dialogs: dialogs,
		ctx: gatewayCtx, cancel: cancel,
		byProviderCall: make(map[string]*sipSession), byCall: make(map[string]*sipSession), reserved: make(map[string]bool), ackTimeout: 32 * time.Second,
	}
	gateway.registerHandlers()
	return gateway, nil
}

func (g *sipGateway) Start() error {
	ready := make(chan struct{})
	listenCtx := context.WithValue(g.ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready))
	errCh := make(chan error, 1)
	go func() {
		var err error
		if g.cfg.Transport == "tls" {
			tlsConfig, tlsErr := g.cfg.tlsConfig()
			if tlsErr != nil {
				err = tlsErr
			} else {
				err = g.server.ListenAndServeTLS(listenCtx, "tls", g.cfg.ListenAddress, tlsConfig)
			}
		} else {
			err = g.server.ListenAndServe(listenCtx, g.cfg.Transport, g.cfg.ListenAddress)
		}
		if err != nil && g.ctx.Err() == nil {
			g.appCtx.Logger().Error("direct SIP listener stopped", "err", err)
		}
		errCh <- err
	}()
	select {
	case <-ready:
		return nil
	case err := <-errCh:
		return err
	case <-time.After(3 * time.Second):
		return errors.New("direct SIP listener did not become ready")
	}
}

func (g *sipGateway) Stop() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.stopping = true
	g.stopDeadline = time.Now().Add(5 * time.Second)
	g.mu.Unlock()
	g.cancel()
	g.mu.RLock()
	sessions := make([]*sipSession, 0, len(g.byCall))
	for _, session := range g.byCall {
		sessions = append(sessions, session)
	}
	g.mu.RUnlock()
	jobs := make(chan *sipSession, len(sessions))
	for _, s := range sessions {
		jobs <- s
	}
	close(jobs)
	var finishing sync.WaitGroup
	for i := 0; i < min(16, len(sessions)); i++ {
		finishing.Add(1)
		go func() {
			defer finishing.Done()
			for session := range jobs {
				session.finish("local_error", errors.New("direct SIP gateway stopped"))
			}
		}()
	}
	finishing.Wait()
	if g.ua != nil {
		_ = g.ua.Close()
	}
	g.work.Wait()
}

func (g *sipGateway) registerHandlers() {
	g.server.OnInvite(g.trackHandler(g.handleInvite))
	g.server.OnAck(g.trackHandler(g.handleACK))
	g.server.OnUpdate(g.trackHandler(g.handleRefresh))
	g.server.OnBye(g.trackHandler(func(request *sip.Request, transaction sip.ServerTransaction) {
		if !g.cfg.sourceAllowed(request.Source()) {
			_ = transaction.Respond(sip.NewResponseFromRequest(request, 403, "Forbidden", nil))
			return
		}
		providerCallID := sipCallID(request)
		session := g.sessionByProviderCall(providerCallID)
		if session == nil || request.CSeq() == nil || request.CSeq().SeqNo <= session.remoteSeq.Load() {
			_ = transaction.Respond(sip.NewResponseFromRequest(request, 481, "Call Does Not Exist", nil))
			return
		}
		if err := g.dialogs.ReadBye(request, transaction); err != nil {
			_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call Does Not Exist", nil))
			return
		}
		if session := g.sessionByProviderCall(providerCallID); session != nil {
			session.finish("carrier", nil)
		}
	}))
	g.server.OnCancel(g.trackHandler(func(request *sip.Request, transaction sip.ServerTransaction) {
		// Matched CANCEL is handled by sipgo's INVITE transaction. An
		// unmatched CANCEL must never end a dialog using Call-ID alone.
		status := 481
		if !g.cfg.sourceAllowed(request.Source()) {
			status = 403
		}
		_ = transaction.Respond(sip.NewResponseFromRequest(request, status, "No Matching Transaction", nil))
	}))
	g.server.OnOptions(g.trackHandler(func(request *sip.Request, transaction sip.ServerTransaction) {
		if !g.cfg.sourceAllowed(request.Source()) {
			_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusForbidden, "Forbidden", nil))
			return
		}
		response := sip.NewResponseFromRequest(request, sip.StatusOK, "OK", nil)
		response.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, CANCEL, BYE, OPTIONS, UPDATE"))
		_ = transaction.Respond(response)
	}))
}

func (g *sipGateway) handleInvite(request *sip.Request, transaction sip.ServerTransaction) {
	respond := func(status int, reason string) {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, status, reason, nil))
	}
	if !g.cfg.sourceAllowed(request.Source()) {
		respond(sip.StatusForbidden, "Forbidden")
		return
	}
	if request.To() != nil && request.To().Params.Has("tag") {
		g.handleRefresh(request, transaction)
		return
	}
	providerCallID := sipCallID(request)
	if providerCallID == "" || request.Contact() == nil {
		respond(sip.StatusBadRequest, "Missing Dialog Headers")
		return
	}
	if status := g.reserveSession(providerCallID); status != 0 {
		respond(status, "Call Admission Rejected")
		return
	}
	defer g.releaseReservation(providerCallID)
	timer, status, err := parseSIPSessionTimer(request, sipSessionTimer{})
	if err != nil {
		res := sip.NewResponseFromRequest(request, status, err.Error(), nil)
		if status == 422 {
			res.AppendHeader(sip.NewHeader("Min-SE", "90"))
		}
		_ = transaction.Respond(res)
		return
	}
	offer, err := parseSIPMediaOffer(request.Body(), g.cfg)
	if err != nil {
		g.appCtx.Logger().Warn("direct SIP offer rejected", "source", request.Source(), "err", err)
		respond(sip.StatusNotAcceptableHere, "Unsupported Media")
		return
	}
	route, calledNumber, err := g.findRouteForInvite(request)
	if err != nil {
		g.appCtx.Logger().Warn("direct SIP route lookup failed", "source", request.Source(), "err", err)
		respond(sip.StatusInternalServerError, "Route Lookup Failed")
		return
	}
	if route == nil {
		respond(sip.StatusNotFound, "No Route")
		return
	}
	dialog, err := g.dialogs.ReadInvite(request, transaction)
	if err != nil {
		respond(sip.StatusBadRequest, "Invalid Dialog")
		return
	}
	caller := sipCallerNumber(request)
	routeForCall := *route
	// Direct SIP bypasses provider call control, so provider-cloud recording
	// cannot be requested even when the project default is enabled.
	routeForCall.RecordingMode = recordingModeOff
	call, created, err := g.app.recordInboundCall(&routeForCall, providerCallID, caller, calledNumber, inboundCallMetadata{
		IngressPath: "sip_direct",
	})
	if err != nil {
		_ = dialog.Respond(sip.StatusInternalServerError, "Call Setup Failed", nil)
		_ = dialog.Close()
		return
	}
	if !created {
		_ = dialog.Respond(sip.StatusLoopDetected, "Duplicate Call", nil)
		_ = dialog.Close()
		return
	}
	sessionCtx, cancel := context.WithCancel(g.ctx)
	session := &sipSession{
		gateway: g, dialog: dialog, route: &routeForCall, call: call, offer: offer,
		ctx: sessionCtx, cancel: cancel, inviteTx: transaction, initialACK: make(chan struct{}), remoteTarget: request.Contact().Address, timer: timer,
	}
	session.remoteSeq.Store(request.CSeq().SeqNo)
	dialog.OnState(func(state sip.DialogState) {
		if state == sip.DialogStateEnded {
			g.runTask(func() { session.finish("carrier", nil) })
		}
		if state == sip.DialogStateEstablished {
			g.runTask(session.watchInitialACK)
		}
	})
	if !g.addSession(session) {
		session.finish("local_error", errors.New("SIP gateway stopping"))
		return
	}
	if err := dialog.Respond(sip.StatusTrying, "Trying", nil); err != nil {
		session.finish("carrier", err)
		return
	}
	if err := dialog.Respond(sip.StatusRinging, "Ringing", nil); err != nil {
		session.finish("carrier", err)
		return
	}
	projectCtx := g.appCtx.WithProject(route.ProjectID)
	if err := g.app.deliverOutboxCall(projectCtx, call.ID); err != nil {
		projectCtx.Logger().Warn("deliver direct SIP incoming call event", "call", call.ID, "err", err)
	}
	g.app.enqueueImmediateAnswer(route, call.ID)

	// sipgo terminates the INVITE server transaction when this handler
	// returns. Keep it alive so carrier CANCEL requests can match while the
	// call rings and so final responses remain retransmittable until ACK.
	select {
	case <-session.ctx.Done():
	case <-dialog.Context().Done():
	}
}

func (g *sipGateway) findRouteForInvite(request *sip.Request) (*routeRow, string, error) {
	for _, phone := range sipCalledNumberCandidates(request) {
		route, err := g.app.db().findDirectSIPRouteByNumber(phone)
		if err != nil {
			return nil, "", err
		}
		if route != nil {
			return route, route.PhoneNumber, nil
		}
	}
	return nil, "", nil
}

func (g *sipGateway) Answer(row *callRow) error {
	if !g.startWork() {
		return errors.New("SIP gateway stopping")
	}
	defer g.work.Done()
	session := g.sessionByCall(row.ID)
	if session == nil {
		return errors.New("direct SIP dialog is no longer active")
	}
	return session.answer()
}

func (g *sipGateway) StartMedia(row *callRow) error {
	session := g.sessionByCall(row.ID)
	if session == nil {
		return errors.New("direct SIP dialog is no longer active")
	}
	return session.startMedia()
}

func (g *sipGateway) Reject(row *callRow) error {
	if !g.startWork() {
		return errors.New("SIP gateway stopping")
	}
	defer g.work.Done()
	session := g.sessionByCall(row.ID)
	if session == nil {
		return errors.New("direct SIP dialog is no longer active")
	}
	session.answerMu.Lock()
	answered := session.media != nil
	session.answerMu.Unlock()
	if answered {
		return errors.New("direct SIP call has already been answered")
	}
	if err := session.dialog.Respond(sip.StatusBusyHere, "Busy Here", nil); err != nil {
		return err
	}
	session.finish("local_error", errors.New("call rejected"))
	return nil
}

func (g *sipGateway) Hangup(row *callRow) error {
	session := g.sessionByCall(row.ID)
	if session == nil {
		return errors.New("direct SIP dialog is no longer active")
	}
	return session.hangup()
}

func (s *sipSession) answer() error {
	s.answerMu.Lock()
	if s.ended.Load() {
		s.answerMu.Unlock()
		return errors.New("SIP session ended")
	}
	if s.media != nil {
		s.answerMu.Unlock()
		return nil
	}
	media, err := openSIPRTPMedia(s.gateway.cfg, s.offer)
	if err != nil {
		s.answerMu.Unlock()
		return err
	}
	answer, err := buildSIPMediaAnswer(s.gateway.cfg, s.offer, media.localPort, media.security)
	if err != nil {
		media.Close()
		s.answerMu.Unlock()
		return err
	}
	s.media = media
	s.localSDP = answer
	s.answerMu.Unlock()
	// Dialog state callbacks may synchronously finish the session.
	response := sip.NewSDPResponseFromRequest(s.dialog.InviteRequest, answer)
	addSIPTimerHeaders(response, s.timer)
	if err := s.dialog.WriteResponse(response); err != nil {
		s.finish("local_error", err)
		return err
	}
	s.answerMu.Lock()
	s.timerUpdated = time.Now()
	s.answerMu.Unlock()
	s.gateway.runTask(s.runSessionTimer)
	return nil
}

func (s *sipSession) startMedia() error {
	s.answerMu.Lock()
	answered := s.media != nil && !s.ended.Load()
	s.answerMu.Unlock()
	if !answered {
		return errors.New("direct SIP call has not been answered")
	}
	var launchErr error
	s.mediaOnce.Do(func() {
		if !s.gateway.runTask(func() { s.gateway.app.bridgeDirectSIPMedia(s) }) {
			launchErr = errors.New("SIP gateway stopping")
		}
	})
	return launchErr
}

func (s *sipSession) hangup() error { return s.finishWithSignaling("local_error", nil) }

func (s *sipSession) finish(leg string, cause error) { _ = s.finishWithSignaling(leg, cause) }
func (s *sipSession) finishWithSignaling(leg string, cause error) error {
	if !s.ended.CompareAndSwap(false, true) {
		return nil
	}
	s.answerMu.Lock()
	refreshCancel := s.refreshCancel
	s.answerMu.Unlock()
	if refreshCancel != nil {
		refreshCancel()
	}
	var signalingErr error
	if leg != "carrier" && s.dialog != nil {
		if s.dialog.LoadState() >= sip.DialogStateEstablished && s.dialog.LoadState() != sip.DialogStateEnded {
			s.signalMu.Lock()
			deadline := time.Now().Add(3 * time.Second)
			s.gateway.mu.RLock()
			stopDeadline := s.gateway.stopDeadline
			s.gateway.mu.RUnlock()
			if !stopDeadline.IsZero() && stopDeadline.Before(deadline) {
				deadline = stopDeadline
			}
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			s.answerMu.Lock()
			target := s.remoteTarget
			s.answerMu.Unlock()
			signalingErr = s.dialog.WriteBye(ctx, sip.NewRequest(sip.BYE, target))
			cancel()
			s.signalMu.Unlock()
		} else if s.inviteTx != nil && s.dialog.LoadState() != sip.DialogStateEnded {
			signalingErr = s.inviteTx.Respond(sip.NewResponseFromRequest(s.dialog.InviteRequest, 487, "Request Terminated", nil))
		}
	}
	func() {
		defer s.gateway.removeSession(s)
		defer s.dialog.Close()
		s.cancel()
		s.answerMu.Lock()
		media := s.media
		s.answerMu.Unlock()
		if media != nil {
			media.Close()
		}
		row, err := s.gateway.app.db().findCall(s.call.ID)
		if err != nil || row == nil {
			return
		}
		projectCtx := s.gateway.appCtx.WithProject(row.ProjectID)
		if !isTerminalStatus(row.Status) {
			status := "completed"
			message := ""
			if row.Status == "pending" || row.Status == "answering" {
				status = "canceled"
			}
			if cause != nil {
				if leg != "carrier" && row.Status != "pending" {
					status = "failed"
				}
				message = cause.Error()
			}
			_ = s.gateway.app.db().updateStatus(row.ID, status, message)
		}
		if err := s.gateway.app.killCallThread(projectCtx, row); err != nil {
			projectCtx.Logger().Warn("kill direct SIP call thread", "call", row.ID, "leg", leg, "err", err)
		}
	}()
	return signalingErr
}

func (g *sipGateway) addSession(session *sipSession) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping || session.ended.Load() {
		return false
	}
	delete(g.reserved, session.call.CarrierSID)
	g.byProviderCall[session.call.CarrierSID] = session
	g.byCall[session.call.ID] = session
	return true
}

func (g *sipGateway) removeSession(session *sipSession) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byProviderCall[session.call.CarrierSID] == session {
		delete(g.byProviderCall, session.call.CarrierSID)
	}
	if g.byCall[session.call.ID] == session {
		delete(g.byCall, session.call.ID)
	}
}

func (g *sipGateway) sessionByProviderCall(id string) *sipSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byProviderCall[id]
}

func (g *sipGateway) sessionByCall(id string) *sipSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byCall[id]
}

func (g *sipGateway) sessionCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.byCall)
}

func sipCallID(request *sip.Request) string {
	if request == nil || request.CallID() == nil {
		return ""
	}
	value := strings.TrimSpace(request.CallID().Value())
	if len(value) > 255 {
		return ""
	}
	return value
}

func sipCalledNumberCandidates(request *sip.Request) []string {
	var values []string
	if request != nil {
		values = append(values, request.Recipient.User)
		if header := request.To(); header != nil {
			values = append(values, header.Address.User)
		}
		for _, name := range []string{"Diversion", "P-Called-Party-ID", "X-Original-To"} {
			for _, header := range request.GetHeaders(name) {
				values = append(values, header.Value())
			}
		}
	}
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		match := e164InSIPValue.FindString(value)
		if match == "" {
			digits := strings.TrimLeft(strings.TrimSpace(value), "+")
			if len(digits) >= 8 && len(digits) <= 15 {
				match = "+" + digits
			}
		}
		if validE164(match) && !seen[match] {
			seen[match] = true
			out = append(out, match)
		}
	}
	return out
}

func sipCallerNumber(request *sip.Request) string {
	if request == nil || request.From() == nil {
		return "anonymous"
	}
	value := request.From().Address.User
	if match := e164InSIPValue.FindString(value); validE164(match) {
		return match
	}
	digits := strings.TrimLeft(strings.TrimSpace(value), "+")
	if len(digits) >= 8 && len(digits) <= 15 {
		candidate := "+" + digits
		if validE164(candidate) {
			return candidate
		}
	}
	return "anonymous"
}
