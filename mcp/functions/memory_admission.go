package main

import (
	"os"
	"strings"
	"time"
)

const memorySampleTTL = 100 * time.Millisecond

func defaultMemoryMode() string {
	if mode := strings.TrimSpace(os.Getenv("APTEVA_FUNCTIONS_MEMORY_MODE")); mode != "" {
		return mode
	}
	return "soft"
}

type admissionSample struct {
	at     time.Time
	bytes  *int64
	source string
}
type admissionHostSample struct {
	at          time.Time
	availableMB int64
}
type admissionMemory struct {
	Mode            string         `json:"mode"`
	AccountedMB     int            `json:"accounted_memory_mb"`
	StartingMB      int            `json:"starting_allowance_mb"`
	FallbackWorkers int            `json:"fallback_workers"`
	ByClass         map[string]int `json:"accounted_by_class_mb"`
	HostAvailableMB *int64         `json:"host_available_memory_mb"`
	SampleMaxAgeMS  int            `json:"sample_max_age_ms"`
	workerCharges   map[*worker]int
}

// The hard cgroup allowance is unchanged. Admission uses current cgroup usage
// plus 25% (at least 16 MiB), capped at the allowance. RSS alone does not cover
// descendants, so missing authoritative measurements retain the full allowance.
func admissionCharge(memory int, sample admissionSample) (int, bool) {
	if sample.bytes == nil || sample.source != "cgroup_v2" {
		return memory, false
	}
	used := int((*sample.bytes + (1 << 20) - 1) >> 20)
	margin := max(16, (used+3)/4)
	return max(used, min(memory, used+margin)), true
}

// Called with p.mu held. Pending starts retain their entire allowance, making
// simultaneous cold-start admission atomic rather than relying on stale RSS.
func (p *pool) admissionMemoryLocked() admissionMemory {
	s := p.settingsLocked()
	m := admissionMemory{Mode: s.memoryMode(), ByClass: map[string]int{}, workerCharges: map[*worker]int{}, SampleMaxAgeMS: 100}
	for class, u := range p.classReservations {
		m.ByClass[class] = u[0]
	}
	m.StartingMB = p.liveMB
	if p.memorySamples == nil {
		p.memorySamples = map[*worker]admissionSample{}
	}
	now := time.Now()
	for w := range p.all {
		m.StartingMB -= w.memoryMB
		charge := w.memoryMB
		if m.Mode == "soft" {
			sample := p.memorySamples[w]
			if sample.at.IsZero() || now.Sub(sample.at) >= memorySampleTTL {
				read := p.memoryReader
				if read == nil {
					read = workerMemory
				}
				b, source, _ := read(w)
				sample = admissionSample{at: now, bytes: b, source: source}
				p.memorySamples[w] = sample
			}
			var measured bool
			charge, measured = admissionCharge(w.memoryMB, sample)
			if !measured {
				m.FallbackWorkers++
			}
		}
		m.workerCharges[w] = charge
		m.ByClass[w.capacityClass] += charge - w.memoryMB
	}
	for _, n := range m.ByClass {
		m.AccountedMB += n
	}
	if p.hostSample.at.IsZero() || now.Sub(p.hostSample.at) >= memorySampleTTL {
		read := p.hostMemoryReader
		if read == nil {
			read = hostMemoryAvailableMB
		}
		p.hostSample = admissionHostSample{at: now, availableMB: read()}
	}
	if n := p.hostSample.availableMB; n >= 0 {
		m.HostAvailableMB = &n
	}
	return m
}

func includedClass(request, existing string) bool {
	if request == "nested" {
		return existing == "nested"
	}
	if request == "background" || request == "preparation" {
		return existing == "background" || existing == "preparation"
	}
	return existing != "nested"
}
func (m admissionMemory) classUsage(class string) int {
	used := 0
	for c, n := range m.ByClass {
		if includedClass(class, c) {
			used += n
		}
	}
	return used
}

func (p *pool) memoryAdmissionErrorLocked(class string, memory int, m admissionMemory) *ResourceError {
	s := p.settingsLocked()
	limit, _ := p.admissionLimitLocked(class)
	if memory > s.MaxWorkerMemoryMB {
		return &ResourceError{Code: "worker_memory_limit", Reason: "worker allowance exceeds operator maximum", RequestedMB: memory, AvailableMB: s.MaxWorkerMemoryMB}
	}
	if memory > limit {
		return &ResourceError{Code: "memory_budget_exhausted", Reason: "worker startup allowance cannot fit in this class budget", RequestedMB: memory, AvailableMB: limit}
	}
	available := max(0, min(s.TotalMemoryMB-m.AccountedMB, limit-m.classUsage(class)))
	if memory > available {
		code, reason := "memory_budget_exhausted", "configured worker reservations fill the memory budget"
		if m.Mode == "soft" {
			code, reason = "memory_pressure", "measured worker usage, safety margins or unmeasured/startup allowances fill the memory target"
		}
		return &ResourceError{Code: code, Reason: reason, RequestedMB: memory, AvailableMB: available, Retryable: true}
	}
	// MemAvailable and enclosing cgroup headroom include other apps and builds.
	// Pending starts may not yet appear there, so retain their full allowances.
	if m.HostAvailableMB != nil {
		available := max(0, int(*m.HostAvailableMB)-s.HostHeadroomMB-m.StartingMB)
		if memory > available {
			return &ResourceError{Code: "host_memory_pressure", Reason: "host/container free memory is below required headroom and startup allowances", RequestedMB: memory, AvailableMB: available, Retryable: true}
		}
	}
	return nil
}
