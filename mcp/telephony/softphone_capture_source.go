package main

import (
	"encoding/binary"
	"time"
)

// Server-only provenance, distinct from browser clock headers. Only the RTC
// in-memory connection may originate this envelope; carrier peers consume it
// only on the authenticated human/external loopback. Never forwarded to Core.
const microphoneSourceMagic uint64 = 0x3143504d31494d54
const microphoneSourceHeader = 16

func encodeMicrophoneSource(pcm []byte, at time.Time) []byte {
	out := make([]byte, microphoneSourceHeader+len(pcm))
	binary.LittleEndian.PutUint64(out, microphoneSourceMagic)
	binary.LittleEndian.PutUint64(out[8:], uint64(at.UnixNano()))
	copy(out[microphoneSourceHeader:], pcm)
	return out
}
func decodeMicrophoneSource(data []byte) ([]byte, time.Time, bool) {
	if len(data) < microphoneSourceHeader || binary.LittleEndian.Uint64(data) != microphoneSourceMagic {
		return data, time.Time{}, false
	}
	at := time.Unix(0, int64(binary.LittleEndian.Uint64(data[8:])))
	// Both ends are in the same Telephony process. Reject impossible future time;
	// do not refresh old audio's age if it waited behind a socket.
	if at.After(time.Now().Add(time.Second)) || at.UnixNano() <= 0 {
		return nil, time.Time{}, true
	}
	return data[microphoneSourceHeader:], at, true
}
func carrierMicrophoneSource(row *callRow, data []byte) ([]byte, time.Time) {
	if row != nil && (row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal) {
		pcm, at, _ := decodeMicrophoneSource(data)
		return pcm, at
	}
	return data, time.Time{}
}
func queueMicrophoneSource(w *websocketWriterPump, pcm []byte, at time.Time, sourced bool) {
	if sourced {
		w.queueAudioAt(encodeMicrophoneSource(pcm, at), microphoneSourceHeader, at)
	} else {
		w.queueAudioAt(pcm, 0, at)
	}
}
