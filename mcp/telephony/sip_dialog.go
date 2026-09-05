package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
)

type sipSessionTimer struct {
	interval time.Duration
	local    bool
}

func sipHasToken(req *sip.Request, header, token string) bool {
	for _, h := range req.GetHeaders(header) {
		for _, v := range strings.Split(h.Value(), ",") {
			if strings.EqualFold(strings.TrimSpace(v), token) {
				return true
			}
		}
	}
	return false
}
func parseSIPSessionTimer(req *sip.Request, previous sipSessionTimer) (sipSessionTimer, int, error) {
	hdr := req.GetHeader("Session-Expires")
	if hdr == nil {
		return previous, 0, nil
	}
	parts := strings.Split(hdr.Value(), ";")
	seconds, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	if err != nil || seconds > 86400 {
		return previous, 400, errors.New("Invalid Session-Expires")
	}
	minimum := uint64(90)
	if h := req.GetHeader("Min-SE"); h != nil {
		v, err := strconv.ParseUint(strings.TrimSpace(strings.Split(h.Value(), ";")[0]), 10, 32)
		if err != nil || v < 90 || v > 86400 {
			return previous, 400, errors.New("Invalid Min-SE")
		}
		minimum = v
	}
	if seconds < minimum {
		return previous, 422, errors.New("Session Interval Too Small")
	}
	result := sipSessionTimer{interval: time.Duration(seconds) * time.Second, local: !sipHasToken(req, "Supported", "timer")}
	seen := false
	for _, part := range parts[1:] {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if strings.EqualFold(kv[0], "refresher") {
			if seen || len(kv) != 2 {
				return previous, 400, errors.New("Invalid refresher")
			}
			seen = true
			switch strings.ToLower(strings.TrimSpace(kv[1])) {
			case "uas":
				result.local = true
			case "uac":
				result.local = false
			default:
				return previous, 400, errors.New("Invalid refresher")
			}
		}
	}
	return result, 0, nil
}
func addSIPTimerHeaders(res *sip.Response, timer sipSessionTimer) {
	res.AppendHeader(sip.NewHeader("Supported", "timer"))
	res.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, CANCEL, BYE, OPTIONS, UPDATE"))
	if timer.interval > 0 {
		refresher := "uac"
		if timer.local {
			refresher = "uas"
		}
		res.AppendHeader(sip.NewHeader("Session-Expires", fmt.Sprintf("%d;refresher=%s", int(timer.interval/time.Second), refresher)))
		if !timer.local {
			res.AppendHeader(sip.NewHeader("Require", "timer"))
		}
	}
}
func (g *sipGateway) reserveSession(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return 503
	}
	if g.byProviderCall[id] != nil || g.reserved[id] {
		return 482
	}
	if len(g.byCall)+len(g.reserved) >= g.cfg.MaxSessions {
		return 503
	}
	if g.reserved == nil {
		g.reserved = make(map[string]bool)
	}
	g.reserved[id] = true
	return 0
}
func (g *sipGateway) releaseReservation(id string) {
	g.mu.Lock()
	delete(g.reserved, id)
	g.mu.Unlock()
}

func (g *sipGateway) handleACK(req *sip.Request, tx sip.ServerTransaction) {
	if !g.cfg.sourceAllowed(req.Source()) || req.CSeq() == nil {
		return
	}
	dialog, err := g.dialogs.MatchDialogRequest(req)
	if err != nil {
		return
	}
	s := g.sessionByProviderCall(sipCallID(req))
	if s == nil || s.dialog != dialog {
		return
	}
	s.answerMu.Lock()
	if s.refreshACK != nil && req.CSeq().SeqNo == s.refreshCSeq {
		ack := s.refreshACK
		if len(req.Body()) > 0 {
			offer, err := parseSIPMediaOffer(req.Body(), g.cfg)
			if err != nil || !sameSIPMedia(offer, s.offer) {
				s.answerMu.Unlock()
				g.runTask(func() { s.finish("local_error", errors.New("unsupported refresh ACK media")) })
				return
			}
		}
		select {
		case ack <- struct{}{}:
		default:
		}
		s.answerMu.Unlock()
		return
	}
	initial := req.CSeq().SeqNo == s.dialog.InviteRequest.CSeq().SeqNo
	s.answerMu.Unlock()
	if initial && dialog.ReadAck(req, tx) == nil {
		s.ackOnce.Do(func() { close(s.initialACK) })
	}
}
func sameSIPMedia(a, b sipMediaOffer) bool {
	return a.RemoteAddress == b.RemoteAddress && a.RemotePort == b.RemotePort && a.PayloadType == b.PayloadType && a.Codec == b.Codec && a.PacketSamples == b.PacketSamples && a.Secure == b.Secure && bytes.Equal(a.RemoteKey, b.RemoteKey)
}
func (g *sipGateway) handleRefresh(req *sip.Request, tx sip.ServerTransaction) {
	reply := func(status int, reason string) { _ = tx.Respond(sip.NewResponseFromRequest(req, status, reason, nil)) }
	if !g.cfg.sourceAllowed(req.Source()) {
		reply(403, "Forbidden")
		return
	}
	dialog, err := g.dialogs.MatchDialogRequest(req)
	s := g.sessionByProviderCall(sipCallID(req))
	if err != nil || s == nil || s.dialog != dialog || s.ended.Load() {
		reply(481, "Call Does Not Exist")
		return
	}
	if dialog.LoadState() != sip.DialogStateConfirmed || !s.refreshMu.TryLock() {
		reply(491, "Request Pending")
		return
	}
	defer s.refreshMu.Unlock()
	if req.CSeq() == nil || req.CSeq().SeqNo <= s.remoteSeq.Load() {
		reply(500, "Invalid CSeq")
		return
	}
	// sipgo maintains the remote CSeq for later BYE validation.
	if err := dialog.ReadRequest(req, tx); err != nil {
		reply(500, "Invalid CSeq")
		return
	}
	s.remoteSeq.Store(req.CSeq().SeqNo)
	s.answerMu.Lock()
	previous := s.timer
	answer := append([]byte(nil), s.localSDP...)
	s.answerMu.Unlock()
	timer, status, err := parseSIPSessionTimer(req, previous)
	if err != nil {
		res := sip.NewResponseFromRequest(req, status, err.Error(), nil)
		if status == 422 {
			res.AppendHeader(sip.NewHeader("Min-SE", "90"))
		}
		_ = tx.Respond(res)
		return
	}
	if len(req.Body()) > 0 {
		offer, err := parseSIPMediaOffer(req.Body(), g.cfg)
		if err != nil || !sameSIPMedia(offer, s.offer) {
			reply(488, "Media Change Not Supported")
			return
		}
		// Echo the current offer's crypto tag even when keys/media are unchanged.
		if offer.Secure {
			answer = bytes.Replace(answer, []byte("a=crypto:"+s.offer.CryptoTag+" "), []byte("a=crypto:"+offer.CryptoTag+" "), 1)
		}
	}
	var response *sip.Response
	if req.Method == sip.INVITE || len(req.Body()) > 0 {
		response = sip.NewSDPResponseFromRequest(req, answer)
	} else {
		response = sip.NewResponseFromRequest(req, 200, "OK", nil)
	}
	response.AppendHeader(sip.HeaderClone(dialog.InviteResponse.Contact()))
	addSIPTimerHeaders(response, timer)
	if req.Method == sip.INVITE {
		ack := make(chan struct{}, 1)
		s.answerMu.Lock()
		s.refreshACK = ack
		s.refreshCSeq = req.CSeq().SeqNo
		s.answerMu.Unlock()
		defer func() { s.answerMu.Lock(); s.refreshACK = nil; s.answerMu.Unlock() }()
		if err = s.respondRefresh(tx, response, ack); err != nil {
			s.finish("local_error", err)
			return
		}
	} else if err = tx.Respond(response); err != nil {
		return
	}
	s.answerMu.Lock()
	s.timer = timer
	s.timerUpdated = time.Now()
	if contact := req.Contact(); contact != nil {
		s.remoteTarget = contact.Address
	}
	s.answerMu.Unlock()
}
func (s *sipSession) respondRefresh(tx sip.ServerTransaction, res *sip.Response, ack <-chan struct{}) error {
	if err := tx.Respond(res); err != nil {
		return err
	}
	deadline := time.NewTimer(s.gateway.ackTimeout)
	defer deadline.Stop()
	retry := time.NewTimer(sip.T1)
	defer retry.Stop()
	interval := sip.T1
	for {
		select {
		case <-ack:
			return nil
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-deadline.C:
			return errors.New("SIP refresh ACK timed out")
		case <-retry.C:
			if err := tx.Respond(res); err != nil {
				return err
			}
			interval = min(2*interval, sip.T2)
			retry.Reset(interval)
		}
	}
}
func (s *sipSession) watchInitialACK() {
	timer := time.NewTimer(s.gateway.ackTimeout)
	defer timer.Stop()
	select {
	case <-s.initialACK:
		return
	case <-s.ctx.Done():
		return
	case <-timer.C:
		// The pinned dialog helper otherwise restarts its ACK timeout on each
		// retransmission. Termination lets a bounded BYE proceed without the ACK.
		s.inviteTx.Terminate()
		s.finish("local_error", errors.New("SIP answer ACK timed out"))
	}
}
func (s *sipSession) runSessionTimer() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.answerMu.Lock()
			timer, updated := s.timer, s.timerUpdated
			s.answerMu.Unlock()
			if timer.interval == 0 {
				continue
			}
			elapsed := time.Since(updated)
			if elapsed >= timer.interval {
				s.finish("local_error", errors.New("SIP session refresh expired"))
				return
			}
			if timer.local && elapsed >= timer.interval/2 && s.refreshMu.TryLock() {
				err := s.sendSessionRefresh(timer)
				s.refreshMu.Unlock()
				if err != nil {
					if errors.Is(err, errSIPRefreshPending) {
						continue
					}
					s.finish("local_error", err)
					return
				}
			}
		}
	}
}

var errSIPRefreshPending = errors.New("SIP refresh collision")

func (s *sipSession) sendSessionRefresh(timer sipSessionTimer) error {
	s.signalMu.Lock()
	defer s.signalMu.Unlock()
	if s.ended.Load() {
		return context.Canceled
	}
	s.answerMu.Lock()
	target := s.remoteTarget
	body := append([]byte(nil), s.localSDP...)
	s.answerMu.Unlock()
	request := sip.NewRequest(sip.INVITE, target)
	request.SetBody(body)
	request.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	request.AppendHeader(sip.NewHeader("Supported", "timer"))
	request.AppendHeader(sip.NewHeader("Session-Expires", fmt.Sprintf("%d;refresher=uac", int(timer.interval/time.Second))))
	ctx, cancel := context.WithTimeout(s.ctx, min(10*time.Second, timer.interval/4))
	defer cancel()
	s.answerMu.Lock()
	if s.ended.Load() {
		s.answerMu.Unlock()
		return context.Canceled
	}
	s.refreshCancel = cancel
	s.answerMu.Unlock()
	defer func() { s.answerMu.Lock(); s.refreshCancel = nil; s.answerMu.Unlock() }()
	response, err := s.dialog.Do(ctx, request)
	if err != nil {
		return err
	}
	if response.StatusCode == 491 {
		return errSIPRefreshPending
	}
	if !response.IsSuccess() {
		return fmt.Errorf("SIP refresh rejected: %d", response.StatusCode)
	}
	// Every successful re-INVITE gets its ACK, even if its SDP is unsupported.
	ack := sip.NewRequest(sip.ACK, target)
	if err = s.dialog.WriteRequest(ack); err != nil {
		return err
	}
	offer, err := parseSIPMediaOffer(response.Body(), s.gateway.cfg)
	if err != nil || !sameSIPMedia(offer, s.offer) {
		return errors.New("SIP refresh response changed unsupported media")
	}
	if h := response.GetHeader("Session-Expires"); h != nil {
		pseudo := sip.NewRequest(sip.UPDATE, target)
		pseudo.AppendHeader(sip.HeaderClone(h))
		pseudo.AppendHeader(sip.NewHeader("Supported", "timer"))
		parsed, _, err := parseSIPSessionTimer(pseudo, timer)
		if err != nil {
			return err
		}
		timer.interval = parsed.interval
		timer.local = !parsed.local // local is UAC for this exchange.
	}
	s.answerMu.Lock()
	s.timer = timer
	s.timerUpdated = time.Now()
	if c := response.Contact(); c != nil {
		s.remoteTarget = c.Address
	}
	s.answerMu.Unlock()
	return nil
}

// Track handler and media work so shutdown cannot release the database/context
// while an answered call is still publishing final state or draining sockets.
func (g *sipGateway) startWork() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return false
	}
	g.work.Add(1)
	return true
}
func (g *sipGateway) runTask(fn func()) bool {
	if !g.startWork() {
		return false
	}
	go func() { defer g.work.Done(); fn() }()
	return true
}
func (g *sipGateway) trackHandler(fn func(*sip.Request, sip.ServerTransaction)) func(*sip.Request, sip.ServerTransaction) {
	return func(req *sip.Request, tx sip.ServerTransaction) {
		if !g.startWork() {
			return
		}
		defer g.work.Done()
		fn(req, tx)
	}
}
