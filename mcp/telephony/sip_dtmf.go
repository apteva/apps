package main

import (
	"encoding/binary"
	"errors"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"strings"
	"time"
)

type sipDTMFState struct {
	digits    string
	tick      int
	timestamp uint32
}

func (p *sipRTPPacer) queueDTMF(digits string) error {
	if p.media.offer.DTMFPayloadType < 0 {
		return errors.New("carrier did not negotiate telephone-event")
	}
	select {
	case <-p.done:
		return errors.New("SIP media ended")
	default:
	}
	select {
	case p.dtmfCh <- digits:
		return nil
	default:
		return errors.New("keypad queue is full")
	}
}
func (p *sipRTPPacer) dtmfTick(now time.Time, timestamp uint32) (bool, error) {
	state := &p.dtmf
	if state.digits == "" {
		return false, nil
	}
	digit := state.digits[0]
	if digit == 'w' || digit == 'W' {
		state.tick++
		if state.tick >= 25 {
			state.digits = state.digits[1:]
			state.tick = 0
		}
		return true, nil
	}
	event := strings.IndexByte("0123456789*#ABCD", digit)
	if state.tick == 0 {
		state.timestamp = timestamp
	}
	payload := []byte{byte(event), 10, 0, 0}
	duration := min(state.tick+1, 13) * 160
	if state.tick >= 13 {
		payload[1] |= 0x80
	}
	binary.BigEndian.PutUint16(payload[2:], uint16(duration))
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: uint8(p.media.offer.DTMFPayloadType), SequenceNumber: p.sequence, Timestamp: state.timestamp, SSRC: p.ssrc, Marker: state.tick == 0}, Payload: payload}
	p.sequence++
	if err := p.media.writePacket(packet); err != nil {
		return true, err
	}
	state.tick++
	if state.tick >= 16 {
		state.digits = state.digits[1:]
		state.tick = 0
	}
	return true, nil
}
func (g *sipGateway) handleInfo(req *sip.Request, tx sip.ServerTransaction) {
	reply := func(code int, text string) { _ = tx.Respond(sip.NewResponseFromRequest(req, code, text, nil)) }
	if !g.cfg.sourceAllowed(req.Source()) {
		reply(403, "Forbidden")
		return
	}
	s := g.sessionByProviderCall(sipCallID(req))
	dialog, err := g.dialogs.MatchDialogRequest(req)
	if err != nil || s == nil || s.dialog != dialog || s.ended.Load() {
		reply(481, "Call Does Not Exist")
		return
	}
	if len(req.Body()) > 512 {
		reply(413, "Too Large")
		return
	}
	typ := req.GetHeader("Content-Type")
	if typ == nil || !strings.EqualFold(strings.TrimSpace(typ.Value()), "application/dtmf-relay") {
		reply(415, "Unsupported Media Type")
		return
	}
	digit := ""
	for _, line := range strings.Split(string(req.Body()), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "Signal") {
			digit = strings.TrimSpace(v)
		}
	}
	if len(digit) != 1 || !strings.Contains("0123456789*#ABCD", digit) {
		reply(400, "Invalid Digit")
		return
	}
	if req.CSeq() == nil || req.CSeq().SeqNo <= s.remoteSeq.Load() {
		reply(500, "Invalid CSeq")
		return
	}
	if err = dialog.ReadRequest(req, tx); err != nil {
		reply(500, "Invalid CSeq")
		return
	}
	s.remoteSeq.Store(req.CSeq().SeqNo)
	s.local.mu.Lock()
	digits := s.local.digits
	s.local.mu.Unlock()
	if digits != nil {
		select {
		case digits <- digit:
		default:
		}
	}
	reply(200, "OK")
}
