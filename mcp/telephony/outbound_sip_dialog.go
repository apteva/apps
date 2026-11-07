package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	"time"
)

func outboundSIPTimer(response *sip.Response, previous sipSessionTimer) (sipSessionTimer, error) {
	if response == nil {
		return previous, nil
	}
	header := response.GetHeader("Session-Expires")
	if header == nil {
		return previous, nil
	}
	req := sip.NewRequest(sip.UPDATE, sip.Uri{})
	req.AppendHeader(sip.HeaderClone(header))
	req.AppendHeader(sip.NewHeader("Supported", "timer"))
	timer, _, err := parseSIPSessionTimer(req, previous)
	timer.local = !timer.local // We are the UAC for this response.
	return timer, err
}
func (s *outboundSIPSession) runSessionTimer() {
	timerClock := time.NewTimer(time.Hour)
	timerClock.Stop()
	defer timerClock.Stop()
	retryAt := time.Time{}
	for {
		s.timerMu.Lock()
		timer, updated := s.timer, s.timerUpdated
		s.timerMu.Unlock()
		var due <-chan time.Time
		if timer.interval > 0 {
			deadline := updated.Add(timer.interval)
			if timer.local {
				deadline = updated.Add(timer.interval / 2)
				if retryAt.After(deadline) {
					deadline = retryAt
				}
				if expiry := updated.Add(timer.interval); expiry.Before(deadline) {
					deadline = expiry
				}
			}
			if !timerClock.Stop() {
				select {
				case <-timerClock.C:
				default:
				}
			}
			timerClock.Reset(max(time.Until(deadline), time.Microsecond))
			due = timerClock.C
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.timerWake:
			retryAt = time.Time{}
			continue
		case <-due:
		}
		// Recheck after waking: a remote refresh may have extended this deadline.
		s.timerMu.Lock()
		timer, updated = s.timer, s.timerUpdated
		s.timerMu.Unlock()
		if timer.interval == 0 {
			continue
		}
		elapsed := time.Since(updated)
		if elapsed >= timer.interval {
			s.finish("local_error", errors.New("outbound SIP session refresh expired"))
			return
		}
		if timer.local && elapsed >= timer.interval/2 {
			retryAt = time.Now().Add(time.Second)
			if s.signalMu.TryLock() {
				err := s.sendSessionRefresh(timer)
				s.signalMu.Unlock()
				if err != nil && !errors.Is(err, errSIPRefreshPending) {
					s.finish("local_error", err)
					return
				}
			}
		}
	}
}

// Invoked with signalMu held. A successful re-INVITE is ACKed even when its
// answer is invalid; the caller then ends the dialog without changing media.
func (s *outboundSIPSession) sendSessionRefresh(timer sipSessionTimer) (result error) {
	status := 0
	s.diagnostics.event("sip_refresh_attempt", "local", "INVITE", 0)
	defer func() {
		detail := ""
		if result != nil {
			detail = transportEvidenceError(result)
		}
		s.diagnostics.event("sip_refresh_result", "local", detail, status)
	}()
	if s.ended.Load() {
		return context.Canceled
	}
	target := s.dialog.InviteResponse.Contact()
	if target == nil {
		return errors.New("outbound SIP refresh target missing")
	}
	body, err := buildSIPMediaAnswer(s.gateway.cfg, s.media.offer, s.media.localPort, &sipMediaSecurity{LocalKey: s.localKey})
	if err != nil {
		return err
	}
	// RFC 3264 §8: the same o= session, version bumped only on change.
	body = nextSIPLocalSDP(s.localSDP, body)
	req := sip.NewRequest(sip.INVITE, target.Address)
	req.SetBody(body)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.AppendHeader(sip.NewHeader("Supported", "timer"))
	req.AppendHeader(sip.NewHeader("Session-Expires", fmt.Sprintf("%d;refresher=uac", int(timer.interval/time.Second))))
	ctx, cancel := context.WithTimeout(s.ctx, min(10*time.Second, timer.interval/4))
	defer cancel()
	response, err := s.doAuthenticatedRefresh(ctx, req)
	if err != nil {
		return err
	}
	status = response.StatusCode
	if status == 491 {
		return errSIPRefreshPending
	}
	if !response.IsSuccess() {
		return fmt.Errorf("outbound SIP refresh rejected: %d", status)
	}
	ack := sip.NewRequest(sip.ACK, target.Address)
	if err = s.dialog.WriteRequest(ack); err != nil {
		return err
	}
	s.localSDP = body
	offer, err := parseSIPMediaOfferOptions(response.Body(), s.gateway.cfg, []string{s.media.offer.Codec}, true)
	if err != nil || !sameSIPMedia(offer, s.media.offer) {
		return errors.New("outbound SIP refresh changed unsupported media")
	}
	updated, err := outboundSIPTimer(response, timer)
	if err != nil {
		return err
	}
	s.timerMu.Lock()
	s.timer = updated
	s.timerUpdated = time.Now()
	signalSIPTimer(s.timerWake)
	s.timerMu.Unlock()
	s.media.remoteMode.Store(sipRemoteMediaMode(offer.Direction))
	return nil
}
func (s *outboundSIPSession) handleRefresh(req *sip.Request, tx sip.ServerTransaction) {
	reply := func(code int, reason string) {
		s.diagnostics.event("sip_refresh_result", "carrier", reason, code)
		_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
	}
	if !s.mediaReady.Load() && !s.ended.Load() && s.matchesEarlyDialog(req) {
		s.handleEarlyRefresh(req, tx)
		return
	}
	dialog, err := s.gateway.outboundDialogs.MatchRequestDialog(req)
	if err != nil || dialog != s.dialog || s.ended.Load() || !s.mediaReady.Load() {
		reply(481, "Call Does Not Exist")
		return
	}
	if !s.signalMu.TryLock() {
		reply(491, "Request Pending")
		return
	}
	defer s.signalMu.Unlock()
	if req.CSeq() == nil || req.CSeq().SeqNo <= s.remoteSeq.Load() {
		reply(500, "Invalid CSeq")
		return
	}
	s.diagnostics.event("sip_refresh_attempt", "carrier", string(req.Method), 0)
	s.timerMu.Lock()
	previous := s.timer
	s.timerMu.Unlock()
	// For a carrier-initiated refresh we act as UAS, regardless of initial direction.
	timer, code, err := parseSIPSessionTimer(req, previous)
	if err != nil {
		res := sip.NewResponseFromRequest(req, code, "Invalid Session Timer", nil)
		if code == 422 {
			res.AppendHeader(sip.NewHeader("Min-SE", "90"))
		}
		_ = tx.Respond(res)
		return
	}
	direction := "sendrecv"
	answer, err := buildSIPMediaAnswer(s.gateway.cfg, s.media.offer, s.media.localPort, &sipMediaSecurity{LocalKey: s.localKey})
	if err != nil {
		reply(500, "SDP Error")
		return
	}
	if len(req.Body()) > 0 {
		offer, e := parseSIPMediaOfferOptions(req.Body(), s.gateway.cfg, []string{s.media.offer.Codec}, true)
		if e != nil || !sameSIPMedia(offer, s.media.offer) {
			reply(488, "Media Change Not Supported")
			return
		}
		direction = offer.Direction
		// Echo the carrier's current crypto tag, as the inbound refresh does.
		if offer.Secure {
			answer = sipSDPWithCryptoTag(answer, offer.CryptoTag)
		}
	}
	answer = nextSIPLocalSDP(s.localSDP, sipSDPWithDirection(answer, sipAnswerDirection(direction)))
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	if req.Method == sip.INVITE || len(req.Body()) > 0 {
		res = sip.NewSDPResponseFromRequest(req, answer)
	}
	if contact := s.dialog.InviteRequest.Contact(); contact != nil {
		res.AppendHeader(sip.HeaderClone(contact))
	}
	addSIPTimerHeaders(res, timer)
	s.remoteSeq.Store(req.CSeq().SeqNo)
	if req.Method == sip.INVITE {
		ack := make(chan struct{}, 1)
		s.timerMu.Lock()
		s.refreshACK = ack
		s.refreshCSeq = req.CSeq().SeqNo
		s.timerMu.Unlock()
		defer func() { s.timerMu.Lock(); s.refreshACK = nil; s.timerMu.Unlock() }()
		if err = respondSIPRefresh(s.ctx, s.gateway.ackTimeout, tx, res, ack); err != nil {
			if s.ctx.Err() == nil {
				s.finish("local_error", err)
			}
			return
		}
	} else if err = tx.Respond(res); err != nil {
		return
	}
	if req.Method == sip.INVITE || len(req.Body()) > 0 {
		s.localSDP = answer
	}
	s.timerMu.Lock()
	s.timer = timer
	s.timerUpdated = time.Now()
	signalSIPTimer(s.timerWake)
	s.timerMu.Unlock()
	s.media.remoteMode.Store(sipRemoteMediaMode(direction))
	s.diagnostics.event("sip_refresh_result", "carrier", "", 200)
}

// matchesEarlyDialog accepts an in-dialog request before the 2xx is ACKed.
// sipgo only indexes a client dialog after its 2xx, so match the Call-ID (the
// caller's lookup key) and our From tag carried as the request's To tag.
func (s *outboundSIPSession) matchesEarlyDialog(req *sip.Request) bool {
	if s.dialog == nil || s.dialog.InviteRequest == nil || req.To() == nil || s.dialog.InviteRequest.From() == nil {
		return false
	}
	ours, ok := s.dialog.InviteRequest.From().Params.Get("tag")
	theirs, present := req.To().Params.Get("tag")
	return ok && present && ours != "" && ours == theirs && sipCallID(req) == s.providerCallID
}

// RFC 5057 treats 481 as dialog-terminating, so an early-dialog refresh must
// not get one. Our INVITE offer is still unanswered: a session-timer UPDATE
// without SDP succeeds, an UPDATE offer gets 500 with Retry-After (RFC 3311
// §5.2), and a re-INVITE gets 491 (RFC 3261 §14.2).
func (s *outboundSIPSession) handleEarlyRefresh(req *sip.Request, tx sip.ServerTransaction) {
	reply := func(code int, reason string, headers ...sip.Header) {
		s.diagnostics.event("sip_refresh_result", "carrier", reason, code)
		res := sip.NewResponseFromRequest(req, code, reason, nil)
		for _, header := range headers {
			res.AppendHeader(header)
		}
		_ = tx.Respond(res)
	}
	if req.CSeq() == nil || req.CSeq().SeqNo <= s.remoteSeq.Load() {
		reply(500, "Invalid CSeq")
		return
	}
	s.diagnostics.event("sip_refresh_attempt", "carrier", "early_"+string(req.Method), 0)
	switch {
	case req.Method == sip.INVITE:
		reply(491, "Request Pending")
		return
	case len(req.Body()) > 0:
		reply(500, "Offer Pending", sip.NewHeader("Retry-After", "1"))
		return
	}
	timer, code, err := parseSIPSessionTimer(req, sipSessionTimer{})
	if err != nil {
		if code == 422 {
			reply(code, "Session Interval Too Small", sip.NewHeader("Min-SE", "90"))
		} else {
			reply(code, "Invalid Session Timer")
		}
		return
	}
	s.remoteSeq.Store(req.CSeq().SeqNo)
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	if contact := s.dialog.InviteRequest.Contact(); contact != nil {
		res.AppendHeader(sip.HeaderClone(contact))
	}
	addSIPTimerHeaders(res, timer)
	if tx.Respond(res) == nil {
		s.diagnostics.event("sip_refresh_result", "carrier", "early_dialog", 200)
	}
}

func (s *outboundSIPSession) handleACK(req *sip.Request) {
	if req.CSeq() == nil {
		return
	}
	dialog, err := s.gateway.outboundDialogs.MatchRequestDialog(req)
	if err != nil || dialog != s.dialog {
		return
	}
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.refreshACK == nil || s.refreshCSeq != req.CSeq().SeqNo {
		return
	}
	if len(req.Body()) > 0 {
		s.gateway.runTask(func() { s.finish("local_error", errors.New("unsupported outbound refresh ACK SDP")) })
		return
	}
	select {
	case s.refreshACK <- struct{}{}:
	default:
	}
	s.diagnostics.event("sip_refresh_ack", "carrier", "", 0)
}

// A challenged in-dialog request gets one authenticated retry. Dialog.Do keeps
// its CSeq and routing state consistent; API credentials are never used here.
func (s *outboundSIPSession) doAuthenticatedRefresh(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	res, err := s.dialog.Do(ctx, req)
	if err != nil || res == nil || (res.StatusCode != 401 && res.StatusCode != 407) {
		return res, err
	}
	if !validSIPDigestCredentials(outboundSIPCredentials{Username: s.settings.username, Password: s.settings.password}) {
		return nil, errors.New("outbound SIP refresh authentication unavailable")
	}
	challengeHeader, authorizationHeader := "WWW-Authenticate", "Authorization"
	if res.StatusCode == 407 {
		challengeHeader, authorizationHeader = "Proxy-Authenticate", "Proxy-Authorization"
	}
	header := res.GetHeader(challengeHeader)
	if header == nil {
		return nil, errors.New("outbound SIP refresh challenge missing")
	}
	challenge, err := digest.ParseChallenge(header.Value())
	if err != nil {
		return nil, errors.New("outbound SIP refresh challenge invalid")
	}
	challenge.Algorithm = sip.ASCIIToUpper(challenge.Algorithm)
	credentials, err := digest.Digest(challenge, digest.Options{Method: req.Method.String(), URI: req.Recipient.Addr(), Username: s.settings.username, Password: s.settings.password})
	if err != nil {
		return nil, errors.New("outbound SIP refresh authentication failed")
	}
	req.RemoveHeader(authorizationHeader)
	req.AppendHeader(sip.NewHeader(authorizationHeader, credentials.String()))
	req.RemoveHeader("Via")
	return s.dialog.Do(ctx, req)
}
