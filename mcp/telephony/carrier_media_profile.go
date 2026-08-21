package main

// carrierMediaProfile keeps provider wire details out of the generic bridge.
// The app's internal browser/realtime format remains PCM16LE mono at 24 kHz.
type carrierMediaProfile struct {
	Codec      string
	Provider   string
	SampleRate int
}

func mediaProfileForCarrier(provider string) carrierMediaProfile {
	switch provider {
	case "signalwire":
		return carrierMediaProfile{Codec: carrierCodecL16_24, Provider: "L16", SampleRate: 24000}
	case "telnyx":
		// Telnyx's WebSocket L16 payload is little-endian. Wideband L16 avoids
		// the extra narrowband PCMU encode/decode step on every browser call.
		return carrierMediaProfile{Codec: carrierCodecL16_16, Provider: "L16", SampleRate: 16000}
	default:
		return carrierMediaProfile{Codec: carrierCodecPCMU8, Provider: "PCMU", SampleRate: 8000}
	}
}

func applyTelnyxMediaProfile(input map[string]any) {
	profile := mediaProfileForCarrier("telnyx")
	input["stream_codec"] = profile.Provider
	input["stream_bidirectional_mode"] = "rtp"
	input["stream_bidirectional_codec"] = profile.Provider
	input["stream_bidirectional_sampling_rate"] = profile.SampleRate
}
