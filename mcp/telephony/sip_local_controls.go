package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type localSIPHold struct {
	mu     sync.Mutex
	pcm    []int16
	offset int
}

func (h *localSIPHold) frame(samples int) []int16 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]int16, samples)
	for i := range out {
		out[i] = h.pcm[h.offset]
		h.offset = (h.offset + 1) % len(h.pcm)
	}
	return out
}

type localSIPController struct{ app *App }

func (c *localSIPController) media(row *callRow) (*sipRTPMedia, error) {
	g := c.app.directSIPGateway()
	if g == nil {
		return nil, errors.New("SIP gateway unavailable")
	}
	s := g.sessionByCall(row.ID)
	if s == nil && row.Direction == "outbound" && row.CarrierSlug == "didww" {
		outbound := g.outboundSessionByCall(row.ID)
		if outbound == nil || outbound.ended.Load() {
			return nil, errCallNoLongerActive
		}
		m := outbound.media
		if m == nil {
			return nil, errors.New("SIP call not answered")
		}
		return m, nil
	}
	if s == nil || s.ended.Load() {
		return nil, errCallNoLongerActive
	}
	s.answerMu.Lock()
	m := s.media
	s.answerMu.Unlock()
	if m == nil {
		return nil, errors.New("SIP call not answered")
	}
	return m, nil
}
func (c *localSIPController) StartHoldMusic(_ *sdk.AppCtx, row *callRow, url, _ string) error {
	m, err := c.media(row)
	if err != nil {
		return err
	}
	if err = validHoldMusicURL(url); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path, _, err := downloadProviderMedia(ctx, url, "wav", "", "")
	if err != nil {
		return err
	}
	defer os.Remove(path)
	pcm, err := readLocalPromptAudio(path)
	if err != nil {
		return err
	}
	pcm = newPCMResampler(24000, m.sampleRate()).Process(pcm)
	if len(pcm) == 0 {
		return errors.New("empty hold music")
	}
	m.hold.Store(&localSIPHold{pcm: pcm})
	if p := m.pacer.Load(); p != nil {
		_, err = p.clear(ctx)
	}
	return err
}
func (c *localSIPController) StopHoldMusic(_ *sdk.AppCtx, row *callRow, _ string) error {
	m, err := c.media(row)
	if err != nil {
		return err
	}
	m.hold.Store(nil)
	return nil
}
func (c *localSIPController) PauseRecording(_ *sdk.AppCtx, row *callRow, _ string) error {
	m, err := c.media(row)
	if err != nil {
		return err
	}
	r := m.recorder.Load()
	if r == nil {
		return errors.New("recording unavailable")
	}
	r.paused.Store(true)
	return nil
}
func (c *localSIPController) ResumeRecording(_ *sdk.AppCtx, row *callRow, _ string) error {
	m, err := c.media(row)
	if err != nil {
		return err
	}
	r := m.recorder.Load()
	if r == nil {
		return errors.New("recording unavailable")
	}
	r.paused.Store(false)
	return nil
}
