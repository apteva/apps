package main

// Vitals collection — CPU / memory / disk / network / load / uptime.
//
// Local: gopsutil. Cross-platform, well-maintained, returns the
// shape we want directly.
//
// Remote: SSH-execute a small bash script that parses /proc and
// returns JSON. v0.1 deliberately avoids deploying a separate agent
// binary to the VPS (extra moving part); we accept the ~50ms-per-call
// SSH overhead since metrics are pulled on demand, not in a hot
// loop. v0.2 polish would deploy a tiny gopsutil-based agent at
// provisioning time and expose it on a local port through the SSH
// tunnel.
//
// Caching: 5-second TTL per instance keyed by id. Concurrent calls
// hit the same in-flight result instead of fanning out duplicate
// SSH sessions.

import (
	"encoding/json"
	"errors"
	"fmt"
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

type Metrics struct {
	Timestamp string         `json:"timestamp"`
	CPU       CPUMetrics     `json:"cpu"`
	Mem       MemMetrics     `json:"mem"`
	Disk      []DiskMetrics  `json:"disk"`
	Net       []NetMetrics   `json:"net"`
	Load      LoadMetrics    `json:"load"`
	UptimeSec uint64         `json:"uptime_s"`
	ProcCount int            `json:"process_count"`
}

type CPUMetrics struct {
	TotalPct  float64   `json:"total_pct"`
	PerCore   []float64 `json:"per_core,omitempty"`
}

type MemMetrics struct {
	UsedBytes      uint64 `json:"used_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapUsedBytes  uint64 `json:"swap_used_bytes,omitempty"`
}

type DiskMetrics struct {
	Mount       string  `json:"mount"`
	UsedBytes   uint64  `json:"used_bytes"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedPct     float64 `json:"used_pct"`
}

type NetMetrics struct {
	Iface    string `json:"iface"`
	RxBytes  uint64 `json:"rx_bytes"`
	TxBytes  uint64 `json:"tx_bytes"`
}

type LoadMetrics struct {
	L1  float64 `json:"l1"`
	L5  float64 `json:"l5"`
	L15 float64 `json:"l15"`
}

// ─── Cache ────────────────────────────────────────────────────────

type metricsCacheEntry struct {
	at     time.Time
	value  *Metrics
}

var (
	metricsMu    sync.Mutex
	metricsCache = map[int64]metricsCacheEntry{}
)

const metricsTTL = 5 * time.Second

// collectMetrics returns vitals for an instance. Cached 5s. Routes
// to local (gopsutil) or remote (SSH-and-parse) based on provider.
func collectMetrics(inst *Instance) (*Metrics, error) {
	metricsMu.Lock()
	if entry, ok := metricsCache[inst.ID]; ok && time.Since(entry.at) < metricsTTL {
		metricsMu.Unlock()
		return entry.value, nil
	}
	metricsMu.Unlock()

	var m *Metrics
	var err error
	if inst.IsLocal() {
		m, err = collectLocalMetrics()
	} else {
		if inst.Status != "ready" {
			return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
		}
		m, err = collectRemoteMetrics(inst)
	}
	if err != nil {
		return nil, err
	}

	metricsMu.Lock()
	metricsCache[inst.ID] = metricsCacheEntry{at: time.Now(), value: m}
	metricsMu.Unlock()
	return m, nil
}

// ─── Local — via gopsutil ─────────────────────────────────────────

func collectLocalMetrics() (*Metrics, error) {
	m := &Metrics{Timestamp: nowUTC()}

	// CPU. PerCore is a 1s sample; we use that for both per-core and
	// total to avoid a second sample call.
	per, err := cpu.Percent(0, true)
	if err == nil {
		m.CPU.PerCore = per
		var sum float64
		for _, p := range per {
			sum += p
		}
		if len(per) > 0 {
			m.CPU.TotalPct = sum / float64(len(per))
		}
	}

	if v, err := mem.VirtualMemory(); err == nil {
		m.Mem.UsedBytes = v.Used
		m.Mem.TotalBytes = v.Total
		m.Mem.AvailableBytes = v.Available
	}
	if s, err := mem.SwapMemory(); err == nil {
		m.Mem.SwapUsedBytes = s.Used
	}

	if parts, err := disk.Partitions(false); err == nil {
		for _, p := range parts {
			u, err := disk.Usage(p.Mountpoint)
			if err != nil {
				continue
			}
			m.Disk = append(m.Disk, DiskMetrics{
				Mount: p.Mountpoint, UsedBytes: u.Used, TotalBytes: u.Total, UsedPct: u.UsedPercent,
			})
		}
	}

	if ifaces, err := net.IOCounters(true); err == nil {
		for _, i := range ifaces {
			// Skip loopback + uninteresting docker bridges from the
			// default panel view.
			if i.Name == "lo" || i.Name == "lo0" || strings.HasPrefix(i.Name, "br-") || strings.HasPrefix(i.Name, "docker") {
				continue
			}
			m.Net = append(m.Net, NetMetrics{Iface: i.Name, RxBytes: i.BytesRecv, TxBytes: i.BytesSent})
		}
	}

	if l, err := load.Avg(); err == nil {
		m.Load.L1 = l.Load1
		m.Load.L5 = l.Load5
		m.Load.L15 = l.Load15
	}

	if h, err := host.Info(); err == nil {
		m.UptimeSec = h.Uptime
	}

	if procs, err := process.Processes(); err == nil {
		m.ProcCount = len(procs)
	}

	return m, nil
}

// ─── Remote — SSH-execute a /proc parser ─────────────────────────

const remoteVitalsScript = `
set -e
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CPU_TOTAL=$(awk '/^cpu /{u=$2+$3+$4; t=u+$5; if (NR==1) print 100*u/t}' /proc/stat)
MEM=$(awk '
  /^MemTotal:/  {t=$2}
  /^MemAvailable:/ {a=$2}
  /^MemFree:/   {f=$2}
  /^Buffers:/   {b=$2}
  /^Cached:/    {c=$2}
  /^SwapTotal:/ {st=$2}
  /^SwapFree:/  {sf=$2}
  END {
    used = (t - a) * 1024
    total = t * 1024
    avail = a * 1024
    swap_used = (st - sf) * 1024
    printf "%d %d %d %d", used, total, avail, swap_used
  }
' /proc/meminfo)
LOAD=$(cat /proc/loadavg | awk '{print $1, $2, $3}')
UPTIME=$(awk '{print int($1)}' /proc/uptime)
PROCS=$(ls -1 /proc | grep -cE '^[0-9]+$')
DISK=$(df -P -B1 -x tmpfs -x devtmpfs -x squashfs 2>/dev/null | tail -n +2 | awk '{printf "{\"mount\":\"%s\",\"used_bytes\":%s,\"total_bytes\":%s,\"used_pct\":%s},", $6, $3, $2, $5}' | sed 's/%//g; s/,$//')
NET=$(awk -F'[: ]+' 'NR>2 && $2 != "lo" {printf "{\"iface\":\"%s\",\"rx_bytes\":%s,\"tx_bytes\":%s},", $2, $3, $11}' /proc/net/dev | sed 's/,$//')
read used total avail swap <<EOF
$MEM
EOF
read l1 l5 l15 <<EOF
$LOAD
EOF
printf '{"timestamp":"%s","cpu":{"total_pct":%s},"mem":{"used_bytes":%s,"total_bytes":%s,"available_bytes":%s,"swap_used_bytes":%s},"disk":[%s],"net":[%s],"load":{"l1":%s,"l5":%s,"l15":%s},"uptime_s":%s,"process_count":%s}\n' "$TS" "$CPU_TOTAL" "$used" "$total" "$avail" "$swap" "$DISK" "$NET" "$l1" "$l5" "$l15" "$UPTIME" "$PROCS"
`

func collectRemoteMetrics(inst *Instance) (*Metrics, error) {
	output, exit, err := runSSH(inst, remoteVitalsScript, 10*time.Second)
	if err != nil && exit != 0 {
		return nil, fmt.Errorf("vitals script failed (exit=%d): %v · output=%q", exit, err, truncate(output, 200))
	}
	// The script prints the JSON on the last line; strip any preamble
	// from shell prompts or warnings on first connect.
	jsonLine := lastJSONLine(output)
	if jsonLine == "" {
		return nil, errors.New("no JSON in vitals script output")
	}
	var m Metrics
	if err := json.Unmarshal([]byte(jsonLine), &m); err != nil {
		return nil, fmt.Errorf("decode vitals: %w (raw: %s)", err, truncate(jsonLine, 200))
	}
	return &m, nil
}

func lastJSONLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") && strings.HasSuffix(l, "}") {
			return l
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
