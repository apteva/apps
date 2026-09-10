package main

import (
	sdk "github.com/apteva/app-sdk"
	"testing"
	"time"
)

func TestHumanTransportDiagnosticsDoNotRequireSpeech(t *testing.T) {
	for _, peer := range []string{peerKindHuman, peerKindExternal} {
		t.Run(peer, func(t *testing.T) {
			f := newCarrierAudioFrontend(8000)
			row := &callRow{ID: "human-diagnostics", PeerKind: peer}
			for _, pcm := range [][]int16{make([]int16, 160), {1, -1, 2, -2}} {
				result := processCarrierInput(row, f, pcm)
				if len(result.PCM) != len(pcm) || &result.PCM[0] != &pcm[0] || result.SpeechStarted {
					t.Fatal("passthrough audio was altered or gated")
				}
			}
			if stats := f.transportSnapshot(); stats.Frames != 2 || stats.Samples != 164 || stats.AudioMS != 20 {
				t.Fatalf("transport: %+v", stats)
			}
			logger := &capturingAudioLogger{}
			logAudioFrontendDiagnostics(logger, f, row, "twilio", carrierCodecPCMU8, 20, 0)
			if logger.fields["frames"] != int64(2) || logger.fields["analyzed_frames"] != int64(0) || logger.fields["max_queued_ms"] != 20 {
				t.Fatalf("diagnostics: %+v", logger.fields)
			}
			if _, ok := logger.fields["input_rms_avg_dbfs"]; ok {
				t.Fatal("bypassed AI analyser supplied misleading RMS")
			}
		})
	}
}

func TestTransportDiagnosticGapAndVariablePacketSizes(t *testing.T) {
	f := newCarrierAudioFrontend(16000)
	at := time.Unix(100, 0)
	f.observeInput(160, at)
	f.observeInput(640, at.Add(70*time.Millisecond))
	f.observeInput(0, at.Add(time.Second))
	stats := f.transportSnapshot()
	if stats.Frames != 2 || stats.Samples != 800 || stats.AudioMS != 50 || stats.MaxGapMS != 70 {
		t.Fatalf("transport: %+v", stats)
	}
}

func TestHumanSendAheadIsConfigurableWithoutChangingSafetyLimits(t *testing.T) {
	t.Setenv("TELEPHONY_HUMAN_AUDIO_SEND_AHEAD_MS", "")
	for _, value := range []string{"20", "40", "60", "80", "0", "999", "invalid"} {
		config := sdk.Config{"human_audio_send_ahead_ms": value}
		policy := humanCarrierPacerPolicy(config)
		want := 40
		if value == "20" {
			want = 20
		}
		if value == "60" {
			want = 60
		}
		if value == "80" {
			want = 80
		}
		if policy.bufferMS != want || !policy.dropStale || policy.maxQueueMS != 180 || policy.adaptiveMaxQueueMS != 220 || policy.trimToMS != 110 {
			t.Fatalf("config %s: %+v", value, policy)
		}
	}
}
