package main

import (
	"math"
	"os"
	"strings"
	"sync"
	"time"

	webrtcvad "github.com/osgochina/webrtcvad-go"
)

const (
	audioAnalysisFrameMS       = 20
	defaultSpeechStartMS       = 280
	defaultSpeechGapMS         = 60
	defaultSpeechRearmMS       = 400
	defaultNoiseFloor          = 0.004
	minimumSpeechRMS           = 0.006
	speechToNoiseRatio         = 1.8
	noiseSuppressionFloorGain  = 0.25
	noiseSuppressionSpeechGain = 1.0
)

type localBargeInMode int

const (
	localBargeInFallback localBargeInMode = iota
	localBargeInOff
)

func configuredLocalBargeInMode() localBargeInMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELEPHONY_LOCAL_BARGE_IN_MODE"))) {
	case "fallback", "local":
		return localBargeInFallback
	default:
		return localBargeInOff
	}
}

func noiseSuppressionEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELEPHONY_NOISE_SUPPRESSION"))) {
	case "0", "false", "off", "disabled":
		return false
	default:
		return true
	}
}

type audioFrontendResult struct {
	PCM             []int16
	SpeechStarted   bool
	RMS             float64
	NoiseFloor      float64
	ActivationMS    int
	VADSpeechMS     int
	SuppressedFrame bool
}

type audioFrontendSnapshot struct {
	Frames                 int64
	Samples                int64
	AverageRMS             float64
	MaxRMS                 float64
	NoiseFloor             float64
	VADSpeechMS            int64
	MaxActivationMS        int
	LocalSpeechStarts      int64
	SuppressedFrames       int64
	LocalInterrupts        int64
	ProviderCoreInterrupts int64
}

type audioDiagnosticsLogger interface {
	Info(string, ...any)
}

// carrierAudioFrontend normalizes arbitrary carrier packet sizes into fixed
// 20 ms analysis frames. WebRTC VAD classifies speech while an adaptive energy
// floor rejects steady background noise before the local fallback can barge in.
type carrierAudioFrontend struct {
	sampleRate     int
	frameSamples   int
	vad            *webrtcvad.VAD
	mode           localBargeInMode
	suppressNoise  bool
	buffer         []int16
	noiseFloor     float64
	activationMS   int
	gapMS          int
	silenceMS      int
	active         bool
	dynamicMS      int
	featureValid   bool
	lastFeature    speechFrameFeature
	previousInput  float64
	previousOutput float64

	mu                     sync.Mutex
	frames                 int64
	samples                int64
	rmsSum                 float64
	maxRMS                 float64
	vadSpeechMS            int64
	maxActivationMS        int
	localSpeechStarts      int64
	suppressedFrames       int64
	localInterrupts        int64
	providerCoreInterrupts int64
	diagnosticNoiseFloor   float64
	lastLocalSignal        time.Time
}

func newCarrierAudioFrontend(sampleRate int) *carrierAudioFrontend {
	vad, err := webrtcvad.New(3)
	if err != nil {
		panic("initialize WebRTC VAD: " + err.Error())
	}
	return &carrierAudioFrontend{
		sampleRate:    sampleRate,
		frameSamples:  sampleRate * audioAnalysisFrameMS / 1000,
		vad:           vad,
		mode:          configuredLocalBargeInMode(),
		suppressNoise: noiseSuppressionEnabled(),
		noiseFloor:    defaultNoiseFloor,
	}
}

func (f *carrierAudioFrontend) process(pcm []int16) audioFrontendResult {
	if len(pcm) == 0 {
		return audioFrontendResult{NoiseFloor: f.noiseFloor}
	}
	f.buffer = append(f.buffer, pcm...)
	out := make([]int16, 0, len(f.buffer)/f.frameSamples*f.frameSamples)
	result := audioFrontendResult{NoiseFloor: f.noiseFloor}
	for len(f.buffer) >= f.frameSamples {
		frame := append([]int16(nil), f.buffer[:f.frameSamples]...)
		copy(f.buffer, f.buffer[f.frameSamples:])
		f.buffer = f.buffer[:len(f.buffer)-f.frameSamples]

		f.highPass(frame)
		rms := pcmRMS(frame)
		vadFrame := frame
		vadRate := f.sampleRate
		if vadRate == 24000 {
			vadFrame = resamplePCM(frame, 24000, 16000)
			vadRate = 16000
		}
		vadSpeech, err := f.vad.IsSpeechInt16(vadFrame, vadRate)
		if err != nil {
			vadSpeech = false
		}

		threshold := math.Max(minimumSpeechRMS, f.noiseFloor*speechToNoiseRatio)
		feature := analyzeSpeechFrame(vadFrame, vadRate)
		speechLike := vadSpeech && rms >= threshold && feature.periodicity >= 0.16
		dynamic := f.featureValid && feature.changedFrom(f.lastFeature)
		f.lastFeature = feature
		f.featureValid = true
		// Do not teach the noise estimator that a strong periodic signal is
		// background. This protects the adaptive floor from hold music, tones,
		// and speech-like audio that the aggressive VAD mode rejects.
		f.updateNoiseFloor(rms, vadSpeech || feature.periodicity >= 0.16)
		suppressed := f.suppress(frame, rms, speechLike)
		started := f.observeSpeechFrame(speechLike, dynamic)
		f.recordFrame(rms, vadSpeech, suppressed, started)

		out = append(out, frame...)
		result.SpeechStarted = result.SpeechStarted || started
		result.RMS = rms
		result.NoiseFloor = f.noiseFloor
		result.ActivationMS = f.activationMS
		if vadSpeech {
			result.VADSpeechMS += audioAnalysisFrameMS
		}
		result.SuppressedFrame = result.SuppressedFrame || suppressed
	}
	result.PCM = out
	return result
}

func (f *carrierAudioFrontend) highPass(frame []int16) {
	for i, sample := range frame {
		input := float64(sample)
		output := input - f.previousInput + 0.995*f.previousOutput
		f.previousInput = input
		f.previousOutput = output
		frame[i] = clampPCM16(output)
	}
}

func (f *carrierAudioFrontend) updateNoiseFloor(rms float64, speechLike bool) {
	if speechLike || rms <= 0 {
		return
	}
	alpha := 0.08
	if rms > f.noiseFloor {
		alpha = 0.015
	}
	f.noiseFloor += alpha * (rms - f.noiseFloor)
	f.noiseFloor = math.Max(0.001, math.Min(0.08, f.noiseFloor))
}

func (f *carrierAudioFrontend) suppress(frame []int16, rms float64, speechLike bool) bool {
	if !f.suppressNoise || speechLike || rms <= 0 {
		return false
	}
	upper := math.Max(minimumSpeechRMS, f.noiseFloor*speechToNoiseRatio)
	gain := noiseSuppressionSpeechGain
	if rms <= f.noiseFloor*1.25 {
		gain = noiseSuppressionFloorGain
	} else if rms < upper {
		position := (rms - f.noiseFloor*1.25) / math.Max(0.0001, upper-f.noiseFloor*1.25)
		gain = noiseSuppressionFloorGain + position*(0.7-noiseSuppressionFloorGain)
	}
	if gain >= noiseSuppressionSpeechGain {
		return false
	}
	for i := range frame {
		frame[i] = clampPCM16(float64(frame[i]) * gain)
	}
	return true
}

func (f *carrierAudioFrontend) observeSpeechFrame(speechLike, dynamic bool) bool {
	if speechLike {
		f.silenceMS = 0
		f.gapMS = 0
		if f.active {
			return false
		}
		f.activationMS += audioAnalysisFrameMS
		if dynamic {
			f.dynamicMS += audioAnalysisFrameMS
		}
		if f.activationMS >= defaultSpeechStartMS && f.dynamicMS >= 80 {
			f.active = true
			if f.mode == localBargeInFallback {
				return true
			}
		}
		return false
	}

	if f.active {
		f.silenceMS += audioAnalysisFrameMS
		if f.silenceMS >= defaultSpeechRearmMS {
			f.active = false
			f.activationMS = 0
			f.dynamicMS = 0
			f.gapMS = 0
			f.silenceMS = 0
		}
		return false
	}
	if f.activationMS > 0 {
		f.gapMS += audioAnalysisFrameMS
		if f.gapMS >= defaultSpeechGapMS {
			f.activationMS = 0
			f.dynamicMS = 0
			f.gapMS = 0
		}
	}
	return false
}

type speechFrameFeature struct {
	rms         float64
	zeroCross   float64
	periodicity float64
	pitchLag    int
}

func (f speechFrameFeature) changedFrom(previous speechFrameFeature) bool {
	rmsDelta := math.Abs(f.rms-previous.rms) / math.Max(0.0001, previous.rms)
	return rmsDelta >= 0.08 ||
		math.Abs(f.zeroCross-previous.zeroCross) >= 0.025 ||
		(f.pitchLag > 0 && previous.pitchLag > 0 && absInt(f.pitchLag-previous.pitchLag) >= 3)
}

func analyzeSpeechFrame(frame []int16, sampleRate int) speechFrameFeature {
	feature := speechFrameFeature{rms: pcmRMS(frame)}
	if len(frame) < 2 {
		return feature
	}
	crossings := 0
	for i := 1; i < len(frame); i++ {
		if (frame[i-1] < 0 && frame[i] >= 0) || (frame[i-1] >= 0 && frame[i] < 0) {
			crossings++
		}
	}
	feature.zeroCross = float64(crossings) / float64(len(frame)-1)

	minLag := max(1, sampleRate/400)
	maxLag := min(len(frame)-2, sampleRate/70)
	var energy float64
	for _, sample := range frame {
		value := float64(sample)
		energy += value * value
	}
	if energy <= 0 {
		return feature
	}
	for lag := minLag; lag <= maxLag; lag++ {
		var correlation, lagEnergy float64
		for i := lag; i < len(frame); i++ {
			current := float64(frame[i])
			delayed := float64(frame[i-lag])
			correlation += current * delayed
			lagEnergy += delayed * delayed
		}
		denominator := math.Sqrt(energy * lagEnergy)
		if denominator <= 0 {
			continue
		}
		normalized := correlation / denominator
		if normalized > feature.periodicity {
			feature.periodicity = normalized
			feature.pitchLag = lag
		}
	}
	return feature
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (f *carrierAudioFrontend) recordFrame(rms float64, vadSpeech, suppressed, started bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames++
	f.samples += int64(f.frameSamples)
	f.rmsSum += rms
	f.maxRMS = math.Max(f.maxRMS, rms)
	f.diagnosticNoiseFloor = f.noiseFloor
	if vadSpeech {
		f.vadSpeechMS += audioAnalysisFrameMS
	}
	f.maxActivationMS = max(f.maxActivationMS, f.activationMS)
	if suppressed {
		f.suppressedFrames++
	}
	if started {
		f.localSpeechStarts++
	}
}

func (f *carrierAudioFrontend) markLocalSignal() {
	f.mu.Lock()
	f.lastLocalSignal = time.Now()
	f.mu.Unlock()
}

func (f *carrierAudioFrontend) markInterrupt(source string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if source == "local" || (!f.lastLocalSignal.IsZero() && time.Since(f.lastLocalSignal) <= 2*time.Second) {
		f.localInterrupts++
		f.lastLocalSignal = time.Time{}
		return "local"
	}
	f.providerCoreInterrupts++
	return "provider_or_core"
}

func (f *carrierAudioFrontend) snapshot() audioFrontendSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	average := 0.0
	if f.frames > 0 {
		average = f.rmsSum / float64(f.frames)
	}
	return audioFrontendSnapshot{
		Frames:                 f.frames,
		Samples:                f.samples,
		AverageRMS:             average,
		MaxRMS:                 f.maxRMS,
		NoiseFloor:             firstPositive(f.diagnosticNoiseFloor, defaultNoiseFloor),
		VADSpeechMS:            f.vadSpeechMS,
		MaxActivationMS:        f.maxActivationMS,
		LocalSpeechStarts:      f.localSpeechStarts,
		SuppressedFrames:       f.suppressedFrames,
		LocalInterrupts:        f.localInterrupts,
		ProviderCoreInterrupts: f.providerCoreInterrupts,
	}
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func pcmRMS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range pcm {
		normalized := float64(sample) / math.MaxInt16
		sum += normalized * normalized
	}
	return math.Sqrt(sum / float64(len(pcm)))
}

func rmsDBFS(rms float64) float64 {
	if rms <= 0 {
		return -120
	}
	return 20 * math.Log10(rms)
}

func logAudioFrontendDiagnostics(logger audioDiagnosticsLogger, frontend *carrierAudioFrontend, row *callRow, provider, codec string, maxQueuedMS, droppedStaleMS int) {
	snapshot := frontend.snapshot()
	processing := "voice_frontend"
	if row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		processing = "passthrough"
	}
	logger.Info("carrier audio diagnostics",
		"provider", provider,
		"call", row.ID,
		"codec", codec,
		"sample_rate", frontend.sampleRate,
		"peer_kind", row.PeerKind,
		"processing", processing,
		"ingress_path", firstNonEmpty(row.IngressPath, "direct_or_unreported"),
		"forwarded", row.ForwardedFrom != "",
		"frames", snapshot.Frames,
		"input_rms_avg_dbfs", math.Round(rmsDBFS(snapshot.AverageRMS)*10)/10,
		"input_rms_max_dbfs", math.Round(rmsDBFS(snapshot.MaxRMS)*10)/10,
		"adaptive_noise_floor_dbfs", math.Round(rmsDBFS(snapshot.NoiseFloor)*10)/10,
		"vad_speech_ms", snapshot.VADSpeechMS,
		"local_vad_max_activation_ms", snapshot.MaxActivationMS,
		"local_speech_starts", snapshot.LocalSpeechStarts,
		"suppressed_frames", snapshot.SuppressedFrames,
		"local_interrupts", snapshot.LocalInterrupts,
		"provider_or_core_interrupts", snapshot.ProviderCoreInterrupts,
		"max_queued_ms", maxQueuedMS,
		"dropped_stale_ms", droppedStaleMS,
	)
}

func logLocalBargeIn(logger audioDiagnosticsLogger, provider, callID string, result audioFrontendResult) {
	logger.Info("local barge-in detected",
		"provider", provider,
		"call", callID,
		"activation_ms", result.ActivationMS,
		"input_rms_dbfs", math.Round(rmsDBFS(result.RMS)*10)/10,
		"adaptive_noise_floor_dbfs", math.Round(rmsDBFS(result.NoiseFloor)*10)/10,
	)
}
