//go:build !((linux || darwin) && (amd64 || arm64))

package main

import "errors"

func newRTCNativeEncoder(int) (rtcOpusEncoder, error) {
	return nil, errors.New("native Opus unavailable on this platform")
}
func newRTCNativeDecoder() (rtcOpusDecoder, error) {
	return nil, errors.New("native Opus unavailable on this platform")
}

func newRTCNativeEncoderWithFEC(int, bool) (rtcOpusEncoder, error) {
	return nil, errors.New("native Opus unavailable on this platform")
}
func loadRTCOpusLibrary() (any, error) {
	return nil, errors.New("native Opus unavailable on this platform")
}

func rtcNativeAvailability() (string, string, error) {
	_, err := loadRTCOpusLibrary()
	return "portable", "", err
}

func newRTCNativeEncoderAtRate(int, bool, int) (rtcOpusEncoder, error) {
	return nil, errors.New("native Opus unavailable")
}
