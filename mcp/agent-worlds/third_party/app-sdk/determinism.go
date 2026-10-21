package sdk

// determinism.go — opt-in determinism helpers for Environment test runs.
//
// External egress and storage are virtualised by the platform with zero app
// changes (HTTP edge + throwaway data dir). The one thing the platform
// can't control from outside is the values an app generates internally:
// the wall clock, ids, and randomness. Apps that route those through
// ctx.Now() / ctx.NewID() / ctx.Rand() become byte-for-byte repeatable
// inside an Environment — when the platform sets APTEVA_FAKE_TIME / APTEVA_SEED —
// while behaving exactly as before in production (env unset → real time,
// crypto-random ids).
//
// Adoption is incremental: an app that keeps calling time.Now()/rand
// directly still runs fine in an Environment, it's just nondeterministic in those
// spots. Nothing here changes existing behavior unless the env is set.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	mrand "math/rand"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

var (
	idCounter  atomic.Uint64
	rndCounter atomic.Uint64
)

// CurrentEnvironmentID returns the id of the environment this app is
// currently running inside, or "" for a normal production/dev sidecar.
func CurrentEnvironmentID() string {
	return os.Getenv("APTEVA_ENVIRONMENT_ID")
}

// detSeed reads APTEVA_SEED on each call (so the env can be set per run /
// per test). Returns ok=false when unset or unparseable → production
// behavior (real randomness).
func detSeed() (int64, bool) {
	if s := os.Getenv("APTEVA_SEED"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// Now returns the current time, or a frozen time when APTEVA_FAKE_TIME is
// set (RFC3339, or unix seconds). Use this instead of time.Now() anywhere
// the value ends up in app output you'd want to assert on.
func (c *AppCtx) Now() time.Time {
	if v := os.Getenv("APTEVA_FAKE_TIME"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Now()
}

// NewID returns a unique id — deterministic + monotonic when APTEVA_SEED is
// set (so a Environment run is repeatable), otherwise a 128-bit random hex string.
func (c *AppCtx) NewID() string {
	if _, ok := detSeed(); ok {
		return fmt.Sprintf("id-%016x", idCounter.Add(1))
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Rand returns a *math/rand.Rand. When APTEVA_SEED is set each call returns
// a generator seeded deterministically from (seed, call index), so repeated
// Environment runs produce identical sequences; otherwise it's time-seeded. Each
// returned *Rand is independent and safe to use within one goroutine.
func (c *AppCtx) Rand() *mrand.Rand {
	if seed, ok := detSeed(); ok {
		return mrand.New(mrand.NewSource(seed + int64(rndCounter.Add(1))))
	}
	return mrand.New(mrand.NewSource(time.Now().UnixNano()))
}
