package main

import (
	"errors"
	"math"
	"unicode/utf8"
)

const maxInputTextBytes = 4096

// Platform-agnostic input event shape. The stream handler builds these
// from inbound WS control messages; android_input.go / ios_input.go
// translate them into adb / idb invocations.
//
// Coordinates are normalized 0..1 against the device's logical screen,
// so the panel doesn't need to know pixel dimensions. The platform
// backend scales them using the device's reported size at send time.

type inputEvent struct {
	Kind       string  `json:"kind"` // "tap" | "swipe" | "key" | "text"
	X          float64 `json:"x"`    // normalized 0..1 (tap, swipe start)
	Y          float64 `json:"y"`
	X2         float64 `json:"x2"` // normalized 0..1 (swipe end)
	Y2         float64 `json:"y2"`
	DurationMS int     `json:"ms"`   // swipe duration
	Key        string  `json:"key"`  // logical key name: BACK | HOME | APP_SWITCH | ENTER | DEL
	Text       string  `json:"text"` // literal text to type
}

func validateInputEvent(ev inputEvent) error {
	switch ev.Kind {
	case "tap":
		if !normalized(ev.X) || !normalized(ev.Y) {
			return errors.New("tap coordinates must be finite values in 0..1")
		}
	case "swipe":
		if !normalized(ev.X) || !normalized(ev.Y) || !normalized(ev.X2) || !normalized(ev.Y2) {
			return errors.New("swipe coordinates must be finite values in 0..1")
		}
		if ev.DurationMS < 0 || ev.DurationMS > 60_000 {
			return errors.New("swipe duration must be between 0 and 60000ms")
		}
	case "key":
		if len(ev.Key) > 64 {
			return errors.New("key is too long")
		}
	case "text":
		if len(ev.Text) > maxInputTextBytes || !utf8.ValidString(ev.Text) {
			return errors.New("text must be valid UTF-8 and at most 4096 bytes")
		}
	default:
		return errors.New("unknown input kind")
	}
	return nil
}

func normalized(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}
