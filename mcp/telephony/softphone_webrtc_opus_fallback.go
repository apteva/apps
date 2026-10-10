//go:build !((linux || darwin) && (amd64 || arm64))

package main

import "errors"

func newRTCNativeEncoder(int) (rtcOpusEncoder, error) {
	return nil, errors.New("native Opus unavailable on this platform")
}
func newRTCNativeDecoder() (rtcOpusDecoder, error) {
	return nil, errors.New("native Opus unavailable on this platform")
}
