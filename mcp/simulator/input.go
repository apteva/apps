package main

// Platform-agnostic input event shape. The stream handler builds these
// from inbound WS control messages; android_input.go / ios_input.go
// translate them into adb / idb invocations.
//
// Coordinates are normalized 0..1 against the device's logical screen,
// so the panel doesn't need to know pixel dimensions. The platform
// backend scales them using the device's reported size at send time.

type inputEvent struct {
	Kind       string  // "tap" | "swipe" | "key" | "text"
	X, Y       float64 // normalized 0..1 (tap, swipe start)
	X2, Y2     float64 // normalized 0..1 (swipe end)
	DurationMS int     // swipe duration
	Key        string  // logical key name: BACK | HOME | APP_SWITCH | ENTER | DEL
	Text       string  // literal text to type
}
