package api

import "errors"

// ErrSessionExpired signals that the backend lease can no longer accept input.
var ErrSessionExpired = errors.New("session expired")

// SessionTarget identifies the reusable environment behind a session. Device
// selectors may be used for ephemeral sessions; saved contexts bind exact devices.
type SessionTarget struct {
	Kind     string `json:"kind"`
	DeviceID string `json:"device_id,omitempty"`
	Platform string `json:"platform,omitempty"`
	Physical *bool  `json:"physical,omitempty"`
	Mode     string `json:"mode,omitempty"`
	AppID    string `json:"app_id,omitempty"`
}

// ContextCapabilities describe guarantees, not merely whether a backend
// accepts a context name. Physical devices retain shared data without isolation.
type ContextCapabilities struct {
	Reuse           bool `json:"reuse"`
	Persistence     bool `json:"persistence"`
	Isolation       bool `json:"isolation"`
	SnapshotRestore bool `json:"snapshot_restore"`
}

type Capabilities struct {
	TargetKind      string              `json:"target_kind"`
	CoordinateSpace string              `json:"coordinate_space"`
	Actions         []string            `json:"actions"`
	Keys            []string            `json:"keys,omitempty"`
	Observations    []string            `json:"observations"`
	Context         ContextCapabilities `json:"context"`
}

// CapabilityReporter is optional so existing browser implementations remain
// compatible. A reporting backend is validated before any action is dispatched.
type CapabilityReporter interface {
	Capabilities() Capabilities
	Target() SessionTarget
}
