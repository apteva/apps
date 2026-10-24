package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
)

// The session format is fixed before Core starts and verified at the renderer
// handshake. Carrier and provider transport choices are independent.
func (p agentAudioProfile) mediaRequest() *sdk.RealtimeMediaConfig {
	if p.Transport == "" && p.Format == "" {
		return nil
	}
	return &sdk.RealtimeMediaConfig{Transport: p.Transport, Format: p.Format}
}
func (s *agentAudioSession) mediaFormat() string {
	if s == nil || s.capabilities.Format == "" {
		return "pcm16"
	}
	return s.capabilities.Format
}
func (s *agentAudioSession) byteRate() int {
	if s.mediaFormat() == "pcm16" {
		return 48000
	}
	return 8000
}
func (s *agentAudioSession) pcm24Samples(bytes int) int { return bytes * 24000 / s.byteRate() }
func (s *agentAudioSession) validateMedia() error {
	wanted := s.profile.Format
	if wanted == "" {
		wanted = "pcm16"
	}
	if s.mediaFormat() != wanted {
		return errors.New("Core audio format does not match requested session format")
	}
	rate := 24000
	if wanted != "pcm16" {
		rate = 8000
	}
	if s.capabilities.Format != "" && (s.capabilities.SampleRate != rate || s.capabilities.Channels != 1) {
		return errors.New("unsupported Core audio rate or channels")
	}
	return nil
}
func (l *agentCoreLink) mediaRate() int {
	if l.byteRate > 0 {
		return l.byteRate
	}
	return 48000
}
func (p *websocketWriterPump) mediaRate() int {
	if n := p.audioRate.Load(); n > 0 {
		return int(n)
	}
	return 48000
}

type agentMediaConverter struct {
	session   *agentAudioSession
	resampler *pcmResampler
}

func newAgentMediaConverter(s *agentAudioSession, carrierRate int, input bool) *agentMediaConverter {
	rate := 24000
	if s.byteRate() == 8000 {
		rate = 8000
	}
	var r *pcmResampler
	if carrierRate != rate {
		if input {
			r = newPCMResampler(carrierRate, rate)
		} else {
			r = newPCMResampler(rate, carrierRate)
		}
	}
	return &agentMediaConverter{session: s, resampler: r}
}
func (c *agentMediaConverter) input(pcm []int16, encoded []byte, format string) []byte {
	if encoded != nil && c.session.mediaFormat() == format && c.session.continuous() && c.session.profile.Preprocessing == "analysis_only" {
		return encoded
	}
	if c.resampler != nil {
		pcm = c.resampler.Process(pcm)
	}
	switch c.session.mediaFormat() {
	case "g711_ulaw":
		return pcm16ToUlaw(pcm)
	case "g711_alaw":
		return pcm16ToAlaw(pcm)
	default:
		return pcm16ToBytes(pcm)
	}
}
func (c *agentMediaConverter) output(data []byte) []int16 {
	var pcm []int16
	switch c.session.mediaFormat() {
	case "g711_ulaw":
		pcm = ulawToPCM16(data)
	case "g711_alaw":
		pcm = alawToPCM16(data)
	default:
		pcm = bytesToPCM16(data)
	}
	if c.resampler != nil {
		pcm = c.resampler.Process(pcm)
	}
	return pcm
}

func (c *agentMediaConverter) inputWithPCM24(pcm, pcm24 []int16, encoded []byte, format string) []byte {
	if c.session.mediaFormat() == "pcm16" {
		return pcm16ToBytes(pcm24)
	}
	return c.input(pcm, encoded, format)
}
