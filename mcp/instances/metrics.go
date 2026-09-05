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
	Timestamp string        `json:"timestamp"`
	CPU       CPUMetrics    `json:"cpu"`
	Mem       MemMetrics    `json:"mem"`
	Disk      []DiskMetrics `json:"disk"`
	Net       []NetMetrics  `json:"net"`
	Load      LoadMetrics   `json:"load"`
	UptimeSec uint64        `json:"uptime_s"`
	ProcCount int           `json:"process_count"`
}

type CPUMetrics struct {
	TotalPct float64   `json:"total_pct"`
	PerCore  []float64 `json:"per_core,omitempty"`
	Cores    int       `json:"cores,omitempty"`
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
	Iface   string `json:"iface"`
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

type LoadMetrics struct {
	L1  float64 `json:"l1"`
	L5  float64 `json:"l5"`
	L15 float64 `json:"l15"`
}

// ─── Cache ────────────────────────────────────────────────────────

type metricsCacheEntry struct {
	at    time.Time
	value *Metrics
}

type metricsCacheKey struct {
	id         int64
	provider   string
	providerID string
	host       string
	port       int
}

type metricsFlight struct {
	done  chan struct{}
	value *Metrics
	err   error
}

var (
	metricsMu       sync.Mutex
	metricsCache    = map[metricsCacheKey]metricsCacheEntry{}
	metricsInFlight = map[metricsCacheKey]*metricsFlight{}
)

var metricsSlots = make(chan struct{}, 8)

const metricsTTL = 5 * time.Second

func clearMetricsCache(id int64) {
	metricsMu.Lock()
	for key := range metricsCache {
		if key.id == id {
			delete(metricsCache, key)
		}
	}
	metricsMu.Unlock()
}

func metricsKey(inst *Instance) metricsCacheKey {
	host := inst.SSHHost
	if host == "" {
		host = inst.PublicIPv4
	}
	if host == "" {
		host = inst.PublicIPv6
	}
	return metricsCacheKey{id: inst.ID, provider: inst.Provider, providerID: inst.ProviderID, host: host, port: inst.SSHPort}
}

// collectMetrics returns vitals for an instance. Cached 5s. Routes
// to local (gopsutil) or remote (SSH-and-parse) based on provider.
func collectMetrics(inst *Instance) (*Metrics, error) {
	return collectMetricsCached(inst, func() (*Metrics, error) {
		if inst.IsLocal() {
			return collectLocalMetrics()
		}
		if inst.Status != "ready" {
			return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
		}
		return collectRemoteMetrics(inst)
	})
}

func collectMetricsCached(inst *Instance, collect func() (*Metrics, error)) (*Metrics, error) {
	key := metricsKey(inst)
	metricsMu.Lock()
	if entry, ok := metricsCache[key]; ok && time.Since(entry.at) < metricsTTL {
		metricsMu.Unlock()
		return entry.value, nil
	}
	if flight, ok := metricsInFlight[key]; ok {
		metricsMu.Unlock()
		<-flight.done
		return flight.value, flight.err
	}
	flight := &metricsFlight{done: make(chan struct{})}
	metricsInFlight[key] = flight
	metricsMu.Unlock()

	metricsSlots <- struct{}{}
	m, err := collect()
	<-metricsSlots
	if m != nil {
		if m.Disk == nil {
			m.Disk = []DiskMetrics{}
		}
		if m.Net == nil {
			m.Net = []NetMetrics{}
		}
	}
	metricsMu.Lock()
	if err == nil {
		metricsCache[key] = metricsCacheEntry{at: time.Now(), value: m}
	}
	flight.value = m
	flight.err = err
	delete(metricsInFlight, key)
	close(flight.done)
	metricsMu.Unlock()
	return m, err
}

// ─── Local — via gopsutil ─────────────────────────────────────────

func collectLocalMetrics() (*Metrics, error) {
	m := &Metrics{Timestamp: nowUTC()}

	// CPU. PerCore is a short interval sample; we use that for both per-core and
	// total to avoid a second sample call.
	per, err := cpu.Percent(250*time.Millisecond, true)
	if err == nil {
		m.CPU.PerCore = per
		m.CPU.Cores = len(per)
		var sum float64
		for _, p := range per {
			sum += p
		}
		if len(per) > 0 {
			m.CPU.TotalPct = sum / float64(len(per))
		}
	} else if cores, err := cpu.Counts(true); err == nil {
		m.CPU.Cores = cores
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
read CPU_IDLE_1 CPU_TOTAL_1 <<EOF
$(awk '/^cpu /{idle=$5+$6; total=0; for(i=2;i<=NF;i++) total+=$i; print idle, total; exit}' /proc/stat)
EOF
sleep 0.25
read CPU_IDLE_2 CPU_TOTAL_2 <<EOF
$(awk '/^cpu /{idle=$5+$6; total=0; for(i=2;i<=NF;i++) total+=$i; print idle, total; exit}' /proc/stat)
EOF
CPU_CORES=$(grep -cE '^processor[[:space:]]*:' /proc/cpuinfo 2>/dev/null || true)
if [ -z "$CPU_CORES" ] || [ "$CPU_CORES" -le 0 ]; then CPU_CORES=1; fi
CPU_TOTAL=$(awk -v i1="$CPU_IDLE_1" -v t1="$CPU_TOTAL_1" -v i2="$CPU_IDLE_2" -v t2="$CPU_TOTAL_2" 'BEGIN {dt=t2-t1; di=i2-i1; if (dt <= 0) print 0; else printf "%.1f", 100*(dt-di)/dt}')
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
printf '{"timestamp":"%s","cpu":{"total_pct":%s,"cores":%s},"mem":{"used_bytes":%s,"total_bytes":%s,"available_bytes":%s,"swap_used_bytes":%s},"disk":[%s],"net":[%s],"load":{"l1":%s,"l5":%s,"l15":%s},"uptime_s":%s,"process_count":%s}\n' "$TS" "$CPU_TOTAL" "$CPU_CORES" "$used" "$total" "$avail" "$swap" "$DISK" "$NET" "$l1" "$l5" "$l15" "$UPTIME" "$PROCS"
`

const remoteMacOSVitalsScript = `
set -e
export LC_ALL=C
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CPU_TOTAL=$(top -l 2 -n 0 -s 1 | awk '/CPU usage:/ {idle=$7; gsub(/%/, "", idle); used=100-idle} END {if (used < 0 || used > 100) used=0; printf "%.1f", used}')
CPU_CORES=$(sysctl -n hw.logicalcpu 2>/dev/null || printf 1)
MEM_TOTAL=$(sysctl -n hw.memsize)
PAGE_SIZE=$(sysctl -n hw.pagesize)
MEM_AVAILABLE=$(vm_stat | awk -v page="$PAGE_SIZE" '
  /Pages free:/ {gsub(/\./, "", $3); free=$3}
  /Pages inactive:/ {gsub(/\./, "", $3); inactive=$3}
  /Pages speculative:/ {gsub(/\./, "", $3); speculative=$3}
  END {printf "%.0f", (free+inactive+speculative)*page}
')
MEM_USED=$(awk -v total="$MEM_TOTAL" -v available="$MEM_AVAILABLE" 'BEGIN {used=total-available; if (used < 0) used=0; printf "%.0f", used}')
set -- $(sysctl -n vm.loadavg | tr -d '{}')
L1=${1:-0}; L5=${2:-0}; L15=${3:-0}
BOOT=$(sysctl -n kern.boottime | awk -F'[=,]' '{gsub(/ /, "", $2); print $2}')
NOW=$(date +%s)
UPTIME=$((NOW-BOOT))
PROCS=$(ps -ax -o pid= | wc -l | tr -d ' ')
DISK=$(df -kP -l 2>/dev/null | tail -n +2 | awk '
  {mount=$6; for(i=7;i<=NF;i++) mount=mount " " $i; gsub(/\\/, "\\\\", mount); gsub(/"/, "\\\"", mount); pct=$5; gsub(/%/, "", pct); printf "{\"mount\":\"%s\",\"used_bytes\":%.0f,\"total_bytes\":%.0f,\"used_pct\":%s},", mount, $3*1024, $2*1024, pct}
' | sed 's/,$//')
NET=$(netstat -ibn 2>/dev/null | awk '
  NR>1 && $1 != "lo0" && $7 ~ /^[0-9]+$/ && $10 ~ /^[0-9]+$/ {if ($7 > rx[$1]) rx[$1]=$7; if ($10 > tx[$1]) tx[$1]=$10}
  END {for (iface in rx) printf "{\"iface\":\"%s\",\"rx_bytes\":%.0f,\"tx_bytes\":%.0f},", iface, rx[iface], tx[iface]}
' | sed 's/,$//')
printf '{"timestamp":"%s","cpu":{"total_pct":%s,"cores":%s},"mem":{"used_bytes":%s,"total_bytes":%s,"available_bytes":%s,"swap_used_bytes":0},"disk":[%s],"net":[%s],"load":{"l1":%s,"l5":%s,"l15":%s},"uptime_s":%s,"process_count":%s}\n' "$TS" "$CPU_TOTAL" "$CPU_CORES" "$MEM_USED" "$MEM_TOTAL" "$MEM_AVAILABLE" "$DISK" "$NET" "$L1" "$L5" "$L15" "$UPTIME" "$PROCS"
`

func collectRemoteMetrics(inst *Instance) (*Metrics, error) {
	script := remoteVitalsScript
	if strings.EqualFold(inst.Platform, "macos") {
		script = remoteMacOSVitalsScript
	}
	output, exit, err := runSSH(inst, script, 10*time.Second)
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
