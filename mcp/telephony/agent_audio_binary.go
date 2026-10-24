package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gobwas/ws"
	"net"
	"time"
)

func (a *App) bridgeContinuousBinaryAgent(row *callRow, bridge *carrierBridgeLease, core net.Conn, coreWriter, carrierWriter *websocketWriterPump, tap *callAudioTap, frontend *carrierAudioFrontend, session *agentAudioSession, provider string, tracker *twilioPlaybackTracker) {
	ctx := bridge.ctx
	shape := "vonage_binary"
	if provider == "sinch" {
		shape = "sinch_binary"
	}
	policy := session.policy()
	policy.onSent = func(silent bool) {
		if !silent {
			a.observeAIOutput(row, time.Now().UTC(), bridge.generation)
		}
	}
	listen := newPCMResampler(16000, 24000)
	pacer := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, shape, "", false, tracker, policy, func(raw []byte) error {
		var frame struct {
			Media struct {
				Payload string `json:"payload"`
			} `json:"media"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			return err
		}
		if frame.Command != "" {
			return carrierWriter.Write(ws.OpText, raw)
		}
		pcm, err := base64.StdEncoding.DecodeString(frame.Media.Payload)
		if err != nil {
			return err
		}
		if err = carrierWriter.Write(ws.OpBinary, pcm); err == nil && tap.hasListeners() {
			tap.publish(1, pcm16ToBytes(listen.Process(bytesToPCM16(pcm))))
		}
		return err
	}, func(progress twilioPlaybackProgress) error {
		return session.progress(coreWriter, progress, "estimated")
	}, func(err error) { bridge.socketError(mediaCloseLegCarrier, "write", err); bridge.cancel() })
	a.bindAIPlayback(row, bridge.generation, func() bool { return pacer.pendingSpeechSamples.Load() > 0 || tracker.hasPendingSpeech() })
	if err := session.ready(coreWriter, "estimated"); err != nil {
		return
	}
	resampler := newPCMResampler(24000, 16000)
	var frame realtimeBridgeControl
	for {
		raw, op, err := readWebSocketData(core, ws.StateClientSide, coreWriter, bridge.receivedFor(mediaCloseLegCore))
		if err != nil {
			bridge.socketError(mediaCloseLegCore, "read", err)
			return
		}
		if op == ws.OpText {
			c, ok := parseRealtimeBridgeControl(raw)
			if !ok {
				continue
			}
			if c.Type == "audio.frame" {
				frame = c
			}
			if c.Type == "interrupt" {
				frontend.markInterrupt(c.Source)
				if matchesAgentPlayback(frame.ItemID, frame.Generation, c) {
					frame = realtimeBridgeControl{}
				}
				receipt, err := pacer.cancelPlayback(ctx, c)
				if err != nil {
					return
				}
				if session.cleared(coreWriter, c, receipt) != nil {
					return
				}
			}
			continue
		}
		if op != ws.OpBinary || len(raw) == 0 || len(raw)%2 != 0 {
			continue
		}
		if !session.accept(frame) {
			frame = realtimeBridgeControl{}
			continue
		}
		pcm := resampler.Process(bytesToPCM16(raw))
		packets := newCarrierAudioPacketizer(16000).add(pcm, len(raw)/2, frame)
		// Binary providers accept subframe PCM; preserve the trailing real samples.
		count := 0
		for _, packet := range packets {
			count += len(packet.PCM)
		}
		if count < len(pcm) {
			packets = append(packets, carrierPacedPacket{PCM: append([]int16(nil), pcm[count:]...), ItemID: frame.ItemID, Generation: frame.Generation, Silence: frame.Silence, AudioEndMS: frame.AudioEndMS})
		}
		err = session.withPlaybackPressure(ctx, frame, func() error { _, _, e := pacer.enqueue(ctx, packets); return e })
		if err != nil {
			if !errors.Is(err, errCarrierPacerOverflow) {
				bridge.socketError(mediaCloseLegCarrier, "write", err)
				return
			}
			if session.overflow(coreWriter, frame.ItemID, frame.Generation, "carrier_queue_overflow") != nil {
				return
			}
		}
		frame = realtimeBridgeControl{}
	}
}
