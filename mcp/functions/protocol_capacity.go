package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol reservations are separate from worker memory admission. Soft mode
// allows a bounded burst; every frame and callback still charges the hard cap.
type protocolBudget struct {
	mode         string
	target, hard int64
	wait         time.Duration
	headroom     int64
	mu           sync.Mutex
	sampled      time.Time
	available    int64
	readHost     func() int64
	bursts       atomic.Int64
}

func (s *CapacitySettings) normalizeProtocol() {
	if s.ProtocolMode == "" {
		s.ProtocolMode = strings.TrimSpace(os.Getenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MODE"))
		if s.ProtocolMode == "" {
			s.ProtocolMode = "soft"
		}
	}
	legacy := envInt("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", 128, 16, 1024)
	if s.ProtocolTargetMB == 0 {
		s.ProtocolTargetMB = envInt("APTEVA_FUNCTIONS_PROTOCOL_TARGET_MB", legacy, 16, 1024)
	}
	if s.ProtocolHardMB == 0 {
		ceiling := max(256, s.ProtocolTargetMB)
		// An explicitly configured legacy hard limit remains a hard limit.
		if os.Getenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB") != "" {
			ceiling = legacy
		}
		s.ProtocolHardMB = envInt("APTEVA_FUNCTIONS_PROTOCOL_HARD_LIMIT_MB", ceiling, 16, 1024)
	}
	if s.ProtocolWaitMS == 0 {
		s.ProtocolWaitMS = envInt("APTEVA_FUNCTIONS_PROTOCOL_WAIT_TIMEOUT_MS", 1000, 1, 30000)
	}
}
func (s CapacitySettings) validateProtocol() error {
	s.normalizeProtocol()
	if s.ProtocolMode != "soft" && s.ProtocolMode != "strict" {
		return errors.New("protocol_memory_mode must be soft or strict")
	}
	if s.ProtocolTargetMB < 16 || s.ProtocolHardMB > 1024 || s.ProtocolTargetMB > s.ProtocolHardMB || s.ProtocolWaitMS < 1 || s.ProtocolWaitMS > 30000 {
		return errors.New("protocol target/hard limits must satisfy 16 <= target <= hard <= 1024 MiB; wait timeout must be 1..30000 ms")
	}
	return nil
}
func newProtocolBudget(s CapacitySettings) *protocolBudget {
	s.normalizeProtocol()
	hard := s.ProtocolHardMB
	if s.ProtocolMode == "strict" {
		hard = s.ProtocolTargetMB
	}
	return &protocolBudget{mode: s.ProtocolMode, target: int64(s.ProtocolTargetMB) << 20, hard: int64(hard) << 20, wait: time.Duration(s.ProtocolWaitMS) * time.Millisecond, headroom: int64(s.HostHeadroomMB), readHost: hostMemoryAvailableMB}
}
func (p *pool) protocolConfig() *protocolBudget {
	if b := p.protocolBudget.Load(); b != nil {
		return b
	}
	return newProtocolBudget(defaultCapacity())
}
func currentProtocolBudget() *protocolBudget {
	if p := currentPool(); p != nil {
		return p.protocolConfig()
	}
	return newProtocolBudget(defaultCapacity())
}
func (b *protocolBudget) hostAvailable() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sampled.IsZero() || time.Since(b.sampled) >= memorySampleTTL {
		b.available = b.readHost()
		b.sampled = time.Now()
	}
	return b.available
}

// Returns a distinguishable refusal reason; no bytes are charged on failure.
// The caller may force a headroom check when borrowing a class's protected share
// even if total reservations have not crossed the global target yet.
var protocolAdmissionMu sync.Mutex

func reserveCurrentProtocol(n int64) string {
	protocolAdmissionMu.Lock()
	defer protocolAdmissionMu.Unlock()
	return reserveProtocolBudgetLocked(currentProtocolBudget(), n, false)
}
func reserveProtocolBudget(b *protocolBudget, n int64, classBurst bool) string {
	protocolAdmissionMu.Lock()
	defer protocolAdmissionMu.Unlock()
	return reserveProtocolBudgetLocked(b, n, classBurst)
}
func reserveProtocolBudgetLocked(b *protocolBudget, n int64, classBurst bool) string {
	if n < 0 || n > b.hard {
		return "protocol_memory_limit"
	}
	for {
		old := protocolBytes.Load()
		if old > b.hard-n {
			return "protocol_memory_limit"
		}
		burst := classBurst || old+n > b.target
		if burst {
			if b.mode != "soft" {
				return "protocol_memory_limit"
			}
			free := b.hostAvailable()
			// Include outstanding reservations in the check, not just the next request:
			// callback allowances may not have materialized as resident memory yet.
			required := max(n, old+n-b.target)
			if free < 0 || free-b.headroom < (required+(1<<20)-1)>>20 {
				return "protocol_host_memory_pressure"
			}
		}
		if protocolBytes.CompareAndSwap(old, old+n) {
			if burst {
				b.bursts.Add(1)
			}
			return ""
		}
	}
}
func protocolClassLimit(limit int64, class string) int64 {
	if class == "nested" {
		return limit
	}
	if class == "background" {
		return limit - limit/8 - limit/4
	}
	return limit - limit/8
}
