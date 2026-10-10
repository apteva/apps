package main

import (
	"errors"
	"github.com/pion/opus"
)

// The codec is owned by one sender goroutine. Optional native support never
// makes the established Go encoder unavailable on another installation.
type rtcOpusEncoder interface {
	EncodeFloat32([]float32, []byte) (int, error)
	configure(bitrate, lossPercent int) error
	capability() string
	Close()
}

type rtcGoOpusEncoder struct{ *opus.Encoder }

func (e *rtcGoOpusEncoder) configure(bitrate, loss int) error {
	if err := e.SetBitrate(bitrate); err != nil {
		return err
	}
	return e.SetLossRate(loss)
}
func (e *rtcGoOpusEncoder) capability() string { return "go_opus_plc" }
func (e *rtcGoOpusEncoder) Close()             {}
func newRTCGoEncoder(bitrate int) (rtcOpusEncoder, error) {
	e, err := opus.NewEncoder(opus.WithChannels(1), opus.WithBitrate(bitrate))
	if err != nil {
		return nil, err
	}
	if err := e.SetLossRate(10); err != nil {
		return nil, err
	}
	return &rtcGoOpusEncoder{e}, nil
}
func newRTCOpusEncoder(bitrate int) (rtcOpusEncoder, error) {
	return newRTCOpusEncoderWithFEC(bitrate, true)
}
func newRTCOpusEncoderWithFEC(bitrate int, fec bool) (rtcOpusEncoder, error) {
	if !fec {
		// Opt-out restores the original encoder and avoids native FEC codec cost.
		return selectRTCOpusEncoder(bitrate, newRTCGoEncoder)
	}
	return selectRTCOpusEncoder(bitrate, newRTCNativeEncoder)
}
func selectRTCOpusEncoder(bitrate int, native func(int) (rtcOpusEncoder, error)) (rtcOpusEncoder, error) {
	if bitrate < 16000 || bitrate > 32000 {
		return nil, errors.New("invalid RTC voice bitrate")
	}
	if e, err := native(bitrate); err == nil {
		return e, nil
	}
	return newRTCGoEncoder(bitrate)
}

type rtcOpusDecoder interface {
	decode([]byte, []int16) (int, error)
	recover([]byte, []int16, bool) (int, error)
	Close()
}
type rtcGoOpusDecoder struct{ *opus.Decoder }

func (d *rtcGoOpusDecoder) decode(p []byte, out []int16) (int, error) { return d.DecodeToInt16(p, out) }
func (d *rtcGoOpusDecoder) recover(_ []byte, out []int16, _ bool) (int, error) {
	err := d.DecodePLC(out)
	return len(out), err
}
func (d *rtcGoOpusDecoder) Close() {}
func newRTCOpusDecoder() (rtcOpusDecoder, error) {
	return newRTCOpusDecoderWithFEC(true)
}
func newRTCOpusDecoderWithFEC(fec bool) (rtcOpusDecoder, error) {
	if fec {
		if d, err := newRTCNativeDecoder(); err == nil {
			return d, nil
		}
	}
	d, err := opus.NewDecoderWithOutput(24000, 1)
	if err != nil {
		return nil, err
	}
	return &rtcGoOpusDecoder{&d}, nil
}

// Repair only small, explicit packet losses on an established 20 ms timeline.
// Timestamp gaps with consecutive sequence numbers are DTX, never lost speech.
type rtcDecodeTimeline struct {
	valid   bool
	seq     uint16
	ts      uint32
	samples int
}

func (d *rtcDecodeTimeline) missing(seq uint16, ts uint32) int {
	gap := int(uint16(seq-d.seq)) - 1
	if !d.valid || d.samples != 480 || gap < 1 || gap > 3 || uint32(gap+1)*960 != ts-d.ts {
		return 0
	}
	return gap
}
