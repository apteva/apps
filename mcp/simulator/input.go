package main

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
