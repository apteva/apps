package monitor

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

func CPUDelta(previous, current []cpu.TimesStat, elapsed time.Duration) (CPUMetrics, bool) {
	result := CPUMetrics{Cores: len(current), IntervalMS: elapsed.Milliseconds()}
	if len(previous) != len(current) || len(current) == 0 || elapsed <= 0 {
		return result, false
	}
	var user, system, wait, steal, total float64
	for i, c := range current {
		p := previous[i]
		// guest and guest_nice are already included in user/nice on Linux.
		// Subtract matching counters before adding deltas. Summing cumulative
		// counters first loses precision on long-running hosts and can produce
		// tiny negative deltas for unchanged counters, rejecting idle samples.
		du := (c.User - p.User) + (c.Nice - p.Nice)
		ds := (c.System - p.System) + (c.Irq - p.Irq) + (c.Softirq - p.Softirq)
		dw := c.Iowait - p.Iowait
		dst := c.Steal - p.Steal
		idle := c.Idle - p.Idle
		dt := du + ds + dw + dst + idle
		if du < 0 || ds < 0 || dst < 0 || idle < 0 || dt <= 0 {
			return result, false
		}
		// Linux iowait may decrease; never turn a counter reset into a fake spike.
		dw = math.Max(0, dw)
		dt = du + ds + dw + dst + idle
		pct := 100 * (du + ds) / dt
		result.PerCore = append(result.PerCore, pct)
		result.BusiestCorePct = math.Max(result.BusiestCorePct, pct)
		user += du
		system += ds
		wait += dw
		steal += dst
		total += dt
	}
	result.UserPct = 100 * user / total
	result.SystemPct = 100 * system / total
	result.IOWaitPct = 100 * wait / total
	result.StealPct = 100 * steal / total
	result.TotalPct = result.UserPct + result.SystemPct
	result.PeakPct = result.TotalPct
	return result, true
}

// Run keeps CPU sampling separate from slower filesystem/process observations.
func Run(ctx context.Context, engine *Engine) {
	var mu sync.Mutex
	resources := Metrics{Disk: []DiskMetrics{}, Net: []NetMetrics{}}
	resourceDone := make(chan struct{})
	go func() {
		defer close(resourceDone)
		var previousNet map[string]net.IOCountersStat
		var previousIO map[string]disk.IOCountersStat
		var previousAt time.Time
		var disks []DiskMetrics
		var lastDisk time.Time
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			now := time.Now()
			m := Metrics{Disk: disks, Net: []NetMetrics{}}
			if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
				m.Mem = MemMetrics{UsedBytes: v.Used, TotalBytes: v.Total, AvailableBytes: v.Available}
			} else {
				m.ResourceError = err.Error()
			}
			if v, err := mem.SwapMemoryWithContext(ctx); err == nil {
				m.Mem.SwapUsedBytes = v.Used
			}
			if time.Since(lastDisk) >= 30*time.Second {
				disks = []DiskMetrics{}
				if parts, err := disk.PartitionsWithContext(ctx, false); err == nil {
					for _, p := range parts {
						if v, err := disk.UsageWithContext(ctx, p.Mountpoint); err == nil {
							disks = append(disks, DiskMetrics{p.Mountpoint, v.Used, v.Total, v.UsedPercent})
						}
					}
				}
				lastDisk = now
				m.Disk = disks
			}
			seconds := now.Sub(previousAt).Seconds()
			if values, err := net.IOCountersWithContext(ctx, true); err == nil {
				next := map[string]net.IOCountersStat{}
				for _, v := range values {
					next[v.Name] = v
					if v.Name == "lo" || v.Name == "lo0" || strings.HasPrefix(v.Name, "br-") || strings.HasPrefix(v.Name, "docker") {
						continue
					}
					n := NetMetrics{Iface: v.Name, RxBytes: v.BytesRecv, TxBytes: v.BytesSent}
					if p, ok := previousNet[v.Name]; ok && seconds > 0 && v.BytesRecv >= p.BytesRecv && v.BytesSent >= p.BytesSent {
						n.RxBPS = float64(v.BytesRecv-p.BytesRecv) / seconds
						n.TxBPS = float64(v.BytesSent-p.BytesSent) / seconds
					}
					m.Net = append(m.Net, n)
				}
				previousNet = next
			}
			if values, err := disk.IOCountersWithContext(ctx); err == nil {
				for name, v := range values {
					if p, ok := previousIO[name]; ok && seconds > 0 && v.ReadBytes >= p.ReadBytes && v.WriteBytes >= p.WriteBytes {
						m.IO = append(m.IO, IOMetrics{name, float64(v.ReadBytes-p.ReadBytes) / seconds, float64(v.WriteBytes-p.WriteBytes) / seconds})
					}
				}
				previousIO = values
			}
			if v, err := load.AvgWithContext(ctx); err == nil {
				m.Load = LoadMetrics{v.Load1, v.Load5, v.Load15}
			}
			m.UptimeSec, _ = host.UptimeWithContext(ctx)
			if p, err := process.PidsWithContext(ctx); err == nil {
				m.ProcCount = len(p)
			}
			m.ResourceTimestamp = now.UTC().Format(time.RFC3339Nano)
			mu.Lock()
			resources = m
			mu.Unlock()
			previousAt = now
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	previous, _ := cpu.TimesWithContext(ctx, true)
	last := time.Now()
	ticker := time.NewTicker(SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			<-resourceDone
			return
		case <-ticker.C:
			now := time.Now()
			current, err := cpu.TimesWithContext(ctx, true)
			if err != nil {
				continue
			}
			sampleStart := last.UnixMilli()
			elapsed := now.Sub(last)
			c, valid := CPUDelta(previous, current, elapsed)
			previous = current
			last = now
			if !valid {
				continue
			}
			c.PeakAt = now.UnixMilli()
			c.IntervalMS = now.UnixMilli() - sampleStart
			mu.Lock()
			m := resources
			mu.Unlock()
			m.CPU = c
			m.Timestamp = now.UTC().Format(time.RFC3339Nano)
			// Long scheduling gaps are reported by timestamp/coverage, not classified as
			// 250ms peaks. Averaging that gap would manufacture misleading precision.
			if elapsed > 2*SampleInterval {
				engine.Gap(m)
				continue
			}
			if id := engine.Add(m); id != "" {
				go func() { engine.AttachProcesses(id, TopProcesses(ctx)) }()
			}
		}
	}
}

// Capture attribution over an actual interval, never lifetime CPU averages.
func TopProcesses(ctx context.Context) []Process {
	first := map[int32]float64{}
	started := time.Now()
	processes, _ := process.ProcessesWithContext(ctx)
	for _, p := range processes {
		if t, err := p.TimesWithContext(ctx); err == nil {
			first[p.Pid] = t.User + t.System
		}
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C:
	}
	seconds := time.Since(started).Seconds()
	result := []Process{}
	for _, p := range processes {
		before, ok := first[p.Pid]
		if !ok {
			continue
		}
		t, err := p.TimesWithContext(ctx)
		if err != nil || t.User+t.System < before {
			continue
		}
		name, _ := p.NameWithContext(ctx)
		rss, _ := p.MemoryInfoWithContext(ctx)
		r := Process{PID: p.Pid, Name: name, CPU: 100 * (t.User + t.System - before) / seconds}
		if rss != nil {
			r.Memory = rss.RSS
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CPU > result[j].CPU })
	if len(result) > 5 {
		result = result[:5]
	}
	return result
}
func ValidateMetrics(m *Metrics) error {
	if m == nil {
		return fmt.Errorf("missing latest metrics")
	}
	_, err := time.Parse(time.RFC3339Nano, m.Timestamp)
	return err
}
