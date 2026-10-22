package main

import (
	"math"
	"runtime/metrics"
	"sync"
	"time"
)

// Process-wide interval evidence, sampled lazily by the existing diagnostics
// collector. No worker/ticker, MemStats stop-the-world read or per-packet work.
type sipRuntimeEvidence struct {
	At               string  `json:"at"`
	Available        bool    `json:"available"`
	IntervalMS       float64 `json:"interval_ms"`
	GCPauses         uint64  `json:"gc_pauses"`
	GCPauseUpperMS   float64 `json:"gc_pause_upper_bound_ms"`
	SchedulerSamples uint64  `json:"scheduler_samples"`
	SchedulerUpperMS float64 `json:"scheduler_latency_upper_bound_ms"`
	GCCPUSeconds     float64 `json:"gc_cpu_seconds"`
}

type sipRuntimeCollector struct {
	sync.Mutex
	at       time.Time
	samples  [3]metrics.Sample
	previous [2][]uint64
	gcCPU    float64
	evidence sipRuntimeEvidence
}

var sipProcessRuntime sipRuntimeCollector

func sipRuntimeTiming() sipRuntimeEvidence { return sipProcessRuntime.read(time.Now()) }

func (c *sipRuntimeCollector) read(now time.Time) sipRuntimeEvidence {
	c.Lock()
	defer c.Unlock()
	if !c.at.IsZero() && now.Sub(c.at) < time.Second {
		return c.evidence
	}
	if c.samples[0].Name == "" {
		c.samples[0].Name = "/gc/pauses:seconds"
		c.samples[1].Name = "/sched/latencies:seconds"
		c.samples[2].Name = "/cpu/classes/gc/total:cpu-seconds"
	}
	metrics.Read(c.samples[:])
	e := sipRuntimeEvidence{At: ringTime(now), Available: true}
	if !c.at.IsZero() {
		e.IntervalMS = float64(now.Sub(c.at)) / float64(time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		if c.samples[i].Value.Kind() != metrics.KindFloat64Histogram {
			e.Available = false
			continue
		}
		h := c.samples[i].Value.Float64Histogram()
		var count uint64
		upper := 0.0
		if len(c.previous[i]) == len(h.Counts) {
			for bucket, n := range h.Counts {
				if n > c.previous[i][bucket] {
					count += n - c.previous[i][bucket]
					if v := h.Buckets[bucket+1]; !math.IsInf(v, 0) && !math.IsNaN(v) {
						upper = max(upper, v*1000)
					}
				}
			}
		} else {
			c.previous[i] = make([]uint64, len(h.Counts))
		}
		copy(c.previous[i], h.Counts)
		if i == 0 {
			e.GCPauses = count
			e.GCPauseUpperMS = upper
		} else {
			e.SchedulerSamples = count
			e.SchedulerUpperMS = upper
		}
	}
	if c.samples[2].Value.Kind() == metrics.KindFloat64 {
		value := c.samples[2].Value.Float64()
		if !c.at.IsZero() {
			e.GCCPUSeconds = max(0, value-c.gcCPU)
		}
		c.gcCPU = value
	} else {
		e.Available = false
	}
	c.at, c.evidence = now, e
	return e
}
