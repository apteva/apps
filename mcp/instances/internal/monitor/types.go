// Package monitor implements host-side sampling and bounded monitoring history.
package monitor

import "time"

const CollectorVersion = "0.6.4"
const SampleInterval = 250 * time.Millisecond

type Metrics struct {
	Timestamp         string        `json:"timestamp"`
	CPU               CPUMetrics    `json:"cpu"`
	Mem               MemMetrics    `json:"mem"`
	Disk              []DiskMetrics `json:"disk"`
	Net               []NetMetrics  `json:"net"`
	IO                []IOMetrics   `json:"io,omitempty"`
	Load              LoadMetrics   `json:"load"`
	UptimeSec         uint64        `json:"uptime_s"`
	ProcCount         int           `json:"process_count"`
	ResourceTimestamp string        `json:"resource_timestamp,omitempty"`
	ResourceError     string        `json:"resource_error,omitempty"`
}
type CPUMetrics struct {
	TotalPct       float64   `json:"total_pct"`
	PerCore        []float64 `json:"per_core,omitempty"`
	Cores          int       `json:"cores,omitempty"`
	UserPct        float64   `json:"user_pct"`
	SystemPct      float64   `json:"system_pct"`
	IOWaitPct      float64   `json:"iowait_pct"`
	StealPct       float64   `json:"steal_pct"`
	PeakPct        float64   `json:"peak_pct"`
	PeakAt         int64     `json:"peak_at,omitempty"`
	BusiestCorePct float64   `json:"busiest_core_pct"`
	IntervalMS     int64     `json:"interval_ms,omitempty"`
}
type MemMetrics struct {
	UsedBytes      uint64 `json:"used_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapUsedBytes  uint64 `json:"swap_used_bytes,omitempty"`
}
type DiskMetrics struct {
	Mount      string  `json:"mount"`
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	UsedPct    float64 `json:"used_pct"`
}
type NetMetrics struct {
	Iface   string  `json:"iface"`
	RxBytes uint64  `json:"rx_bytes"`
	TxBytes uint64  `json:"tx_bytes"`
	RxBPS   float64 `json:"rx_bps"`
	TxBPS   float64 `json:"tx_bps"`
}
type IOMetrics struct {
	Device   string  `json:"device"`
	ReadBPS  float64 `json:"read_bps"`
	WriteBPS float64 `json:"write_bps"`
}
type LoadMetrics struct {
	L1  float64 `json:"l1"`
	L5  float64 `json:"l5"`
	L15 float64 `json:"l15"`
}
type Aggregate struct {
	ObservedMS int64   `json:"observed_ms"`
	Sum        float64 `json:"sum"` // value * observed milliseconds
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
	PeakAt     int64   `json:"peak_at"`
}

func (a Aggregate) Average(ms int64) float64 {
	if ms == 0 {
		return 0
	}
	if a.ObservedMS > 0 {
		ms = a.ObservedMS
	}
	return a.Sum / float64(ms)
}

type Point struct {
	Time       int64                `json:"time"` // UTC Unix milliseconds, beginning of bucket
	Step       int64                `json:"step_ms"`
	ObservedMS int64                `json:"observed_ms"`
	Samples    int64                `json:"samples"`
	Above80MS  int64                `json:"above_80_ms"`
	Above95MS  int64                `json:"above_95_ms"`
	Values     map[string]Aggregate `json:"values"`
}

func (p *Point) Merge(other Point) {
	first := p.Samples == 0
	if p.Values == nil {
		p.Values = map[string]Aggregate{}
	}
	for k, b := range other.Values {
		a, exists := p.Values[k]
		if first || !exists {
			a = b
		} else {
			a.Sum += b.Sum
			a.ObservedMS += b.ObservedMS
			if b.Min < a.Min {
				a.Min = b.Min
			}
			if b.Max > a.Max || (b.Max == a.Max && b.PeakAt < a.PeakAt) {
				a.Max = b.Max
				a.PeakAt = b.PeakAt
			}
		}
		p.Values[k] = a
	}
	p.Samples += other.Samples
	p.ObservedMS += other.ObservedMS
	p.Above80MS += other.Above80MS
	p.Above95MS += other.Above95MS
}

type Reading struct {
	Time       int64    `json:"time"`
	CPU        float64  `json:"cpu"`
	Core       float64  `json:"core"`
	IOWait     float64  `json:"iowait"`
	Memory     *float64 `json:"memory,omitempty"`
	IntervalMS int64    `json:"interval_ms"`
}
type Process struct {
	PID    int32   `json:"pid"`
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu_pct"`
	Memory uint64  `json:"memory_bytes"`
}
type Incident struct {
	ID            string    `json:"id"`
	Start         int64     `json:"start"`
	End           int64     `json:"end,omitempty"`
	Updated       int64     `json:"updated"`
	Peak          float64   `json:"peak"`
	PeakAt        int64     `json:"peak_at"`
	CorePeak      float64   `json:"core_peak"`
	Reason        string    `json:"reason"`
	Recordings    []Reading `json:"recordings,omitempty"`
	Processes     []Process `json:"processes,omitempty"`
	DetailExpired bool      `json:"detail_expired,omitempty"`
}
type Batch struct {
	Version   string     `json:"version"`
	Latest    *Metrics   `json:"latest"`
	Points    []Point    `json:"points"`
	Incidents []Incident `json:"incidents"`
	Since     int64      `json:"since"`
	Oldest    int64      `json:"oldest"`
	More      bool       `json:"more"`
}

var Tiers = []struct {
	Step      int64
	Retention time.Duration
	Name      string
}{
	{1000, time.Hour, "1s"}, {60000, 48 * time.Hour, "1m"}, {300000, 14 * 24 * time.Hour, "5m"},
	{3600000, 90 * 24 * time.Hour, "1h"}, {86400000, 365 * 24 * time.Hour, "1d"},
}
