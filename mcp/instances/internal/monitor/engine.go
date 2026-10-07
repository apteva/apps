package monitor

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxSpoolBytes = 32 << 20
const IncidentBudgetBytes = 16 << 20
const maxIncidentCount = 50
const maxPoints = 3600

type Engine struct {
	mu            sync.Mutex
	Latest        *Metrics   `json:"latest"`
	Points        []Point    `json:"points"`
	Incidents     []Incident `json:"incidents"`
	current       Point
	pre           []Reading
	active        string
	calmSince     int64
	coreSince     int64
	pressureSince int64
}

func NewEngine() *Engine { return &Engine{Points: []Point{}, Incidents: []Incident{}} }
func (e *Engine) Gap(m Metrics) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Latest = &m
	e.coreSince = 0
	e.pressureSince = 0
}
func (e *Engine) SetLatest(m Metrics) { e.mu.Lock(); defer e.mu.Unlock(); e.Latest = &m }
func readingPoint(m Metrics, at, duration int64) Point {
	p := Point{Time: at / 1000 * 1000, Step: 1000, ObservedMS: duration, Samples: 1, Values: map[string]Aggregate{}}
	values := map[string]float64{"cpu": m.CPU.TotalPct, "core": m.CPU.BusiestCorePct, "cpu_user": m.CPU.UserPct, "cpu_system": m.CPU.SystemPct, "iowait": m.CPU.IOWaitPct, "steal": m.CPU.StealPct}
	resourceAt, _ := time.Parse(time.RFC3339Nano, m.ResourceTimestamp)
	if m.Mem.TotalBytes > 0 && m.ResourceError == "" && at-resourceAt.UnixMilli() < 3000 {
		values["memory"] = 100 * float64(m.Mem.UsedBytes) / float64(m.Mem.TotalBytes)
		values["memory_available"] = float64(m.Mem.AvailableBytes)
		values["swap"] = float64(m.Mem.SwapUsedBytes)
	}
	if at-resourceAt.UnixMilli() < 3000 {
		values["load"] = m.Load.L1
		var rx, tx, read, write float64
		for _, n := range m.Net {
			rx += n.RxBPS
			tx += n.TxBPS
		}
		// Max device throughput avoids counting parent disks and partitions twice.
		for _, d := range m.IO {
			if d.ReadBPS > read {
				read = d.ReadBPS
			}
			if d.WriteBPS > write {
				write = d.WriteBPS
			}
		}
		values["network_rx"] = rx
		values["network_tx"] = tx
		values["disk_read"] = read
		values["disk_write"] = write
		for _, d := range m.Disk {
			if d.Mount == "/" {
				values["disk_used"] = d.UsedPct
				break
			}
		}
	}
	for k, v := range values {
		peakAt := m.CPU.PeakAt
		if k != "cpu" && k != "core" && k != "cpu_user" && k != "cpu_system" && k != "iowait" && k != "steal" {
			peakAt = resourceAt.UnixMilli()
		}
		p.Values[k] = Aggregate{ObservedMS: duration, Sum: v * float64(duration), Min: v, Max: v, PeakAt: peakAt}
	}
	if m.CPU.TotalPct >= 80 {
		p.Above80MS = duration
	}
	if m.CPU.TotalPct >= 95 {
		p.Above95MS = duration
	}
	return p
}
func (e *Engine) Add(m Metrics) string {
	at, err := time.Parse(time.RFC3339Nano, m.Timestamp)
	if err != nil || m.CPU.IntervalMS <= 0 {
		return ""
	}
	ts := at.UnixMilli()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Latest = &m
	// Attribute elapsed CPU counters to contiguous wall-clock buckets. A delayed
	// sample is filtered by Run; splitting avoids overfull seconds at boundaries.
	start := ts - m.CPU.IntervalMS
	for start < ts {
		end := (start/1000 + 1) * 1000
		if end > ts {
			end = ts
		}
		p := readingPoint(m, start, end-start)
		if e.current.Samples > 0 && e.current.Time != p.Time {
			e.Points = append(e.Points, e.current)
			e.current = Point{}
		}
		if e.current.Samples == 0 {
			e.current.Time = p.Time
			e.current.Step = 1000
		}
		e.current.Merge(p)
		start = end
	}
	if len(e.Points) > maxPoints {
		e.Points = append([]Point{}, e.Points[len(e.Points)-maxPoints:]...)
	}
	var memory *float64
	resourceAt, _ := time.Parse(time.RFC3339Nano, m.ResourceTimestamp)
	if m.Mem.TotalBytes > 0 && m.ResourceError == "" && ts-resourceAt.UnixMilli() < 3000 {
		pct := 100 * float64(m.Mem.UsedBytes) / float64(m.Mem.TotalBytes)
		memory = &pct
	}
	r := Reading{Time: ts, CPU: m.CPU.TotalPct, Core: m.CPU.BusiestCorePct, IOWait: m.CPU.IOWaitPct, Memory: memory, IntervalMS: m.CPU.IntervalMS}
	e.pre = append(e.pre, r)
	cut := 0
	for cut < len(e.pre) && e.pre[cut].Time < ts-120000 {
		cut++
	}
	if cut > 0 {
		e.pre = append([]Reading{}, e.pre[cut:]...)
	}
	if r.Core >= 95 {
		if e.coreSince == 0 {
			e.coreSince = ts
		}
	} else {
		e.coreSince = 0
	}
	pressure := r.IOWait >= 30 || (memory != nil && float64(m.Mem.AvailableBytes)/float64(m.Mem.TotalBytes) <= 0.1)
	if pressure {
		if e.pressureSince == 0 {
			e.pressureSince = ts
		}
	} else {
		e.pressureSince = 0
	}
	reason := ""
	if r.CPU >= 90 {
		reason = "CPU ≥90%"
	} else if e.coreSince > 0 && ts-e.coreSince >= 1000 {
		reason = "Core ≥95% for 1s"
	} else if e.pressureSince > 0 && ts-e.pressureSince >= 1000 {
		reason = "Memory pressure or I/O wait for 1s"
	}
	newID := ""
	if e.active == "" && reason != "" {
		newID = fmt.Sprintf("%d", ts)
		e.active = newID
		e.calmSince = 0
		e.Incidents = append(e.Incidents, Incident{ID: newID, Start: ts, Updated: ts, Peak: r.CPU, PeakAt: ts, CorePeak: r.Core, Reason: reason, Recordings: append([]Reading{}, e.pre...)})
	}
	for i := range e.Incidents {
		ev := &e.Incidents[i]
		if ev.ID != e.active {
			continue
		}
		if newID == "" {
			ev.Recordings = append(ev.Recordings, r)
		}
		ev.Updated = ts
		if r.CPU > ev.Peak {
			ev.Peak = r.CPU
			ev.PeakAt = ts
		}
		if r.Core > ev.CorePeak {
			ev.CorePeak = r.Core
		}
		// Hysteresis keeps a fluctuating spike in one incident; retain 5min after
		// recovery, with a hard 10min recording window during sustained overload.
		calm := r.CPU < 70 && r.Core < 80 && r.IOWait < 20 && !pressure
		if calm {
			if e.calmSince == 0 {
				e.calmSince = ts
			}
		} else {
			e.calmSince = 0
		}
		if (e.calmSince > 0 && ts-e.calmSince >= 300000) || ts-ev.Start >= 480000 {
			ev.End = ts
			e.active = ""
			e.calmSince = 0
		}
	}
	e.trimIncidents(ts)
	return newID
}
func (e *Engine) trimIncidents(now int64) {
	for len(e.Incidents) > maxIncidentCount || (len(e.Incidents) > 0 && e.Incidents[0].Updated < now-int64(30*24*time.Hour/time.Millisecond)) {
		e.Incidents = e.Incidents[1:]
	}
	size := 0
	for i := len(e.Incidents) - 1; i >= 0; i-- {
		size += len(e.Incidents[i].Recordings) * 64
		if size > IncidentBudgetBytes {
			e.Incidents[i].Recordings = nil
			e.Incidents[i].DetailExpired = true
		}
	}
}
func (e *Engine) AttachProcesses(id string, processes []Process) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.Incidents {
		if e.Incidents[i].ID == id {
			e.Incidents[i].Processes = processes
			e.Incidents[i].Updated = time.Now().UnixMilli()
			break
		}
	}
}
func (e *Engine) Export(since int64) Batch {
	e.mu.Lock()
	defer e.mu.Unlock()
	b := Batch{Version: CollectorVersion, Latest: e.Latest, Since: since, Points: []Point{}, Incidents: []Incident{}}
	if len(e.Points) > 0 {
		b.Oldest = e.Points[0].Time
	}
	for _, p := range e.Points {
		if p.Time <= since {
			continue
		}
		if len(b.Points) == 120 {
			b.More = true
			break
		}
		b.Points = append(b.Points, p)
		b.Since = p.Time
	}
	// Export at most the 50 retained incident updates. EncodeBatch bounds wire
	// bytes and explicitly marks detail omitted during large catch-up exports.
	for _, ev := range e.Incidents {
		if ev.Updated > since {
			copy := ev
			copy.Recordings = append([]Reading{}, ev.Recordings...)
			copy.Processes = append([]Process{}, ev.Processes...)
			b.Incidents = append(b.Incidents, copy)
		}
	}
	return b
}
func Encode(v any) ([]byte, error) {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func Decode(data []byte, out any) error {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer r.Close()
	return json.NewDecoder(io.LimitReader(r, 64<<20)).Decode(out)
}
func (e *Engine) Save(path string) error {
	e.mu.Lock()
	state := struct {
		Latest    *Metrics   `json:"latest"`
		Points    []Point    `json:"points"`
		Incidents []Incident `json:"incidents"`
	}{Latest: e.Latest, Points: append([]Point{}, e.Points...), Incidents: append([]Incident{}, e.Incidents...)}
	for i := range state.Incidents {
		state.Incidents[i].Recordings = append([]Reading{}, state.Incidents[i].Recordings...)
		state.Incidents[i].Processes = append([]Process{}, state.Incidents[i].Processes...)
	}
	e.mu.Unlock()
	data, err := Encode(state)
	if err != nil {
		return err
	}
	if len(data) > maxSpoolBytes {
		return errors.New("collector spool exceeds 32MiB limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
func Load(path string) (*Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxSpoolBytes {
		return nil, errors.New("collector spool too large")
	}
	e := NewEngine()
	if err := Decode(data, e); err != nil {
		return nil, err
	}
	e.trimIncidents(time.Now().UnixMilli())
	if len(e.Points) > maxPoints {
		e.Points = e.Points[len(e.Points)-maxPoints:]
	}

	return e, nil
}

// Recover marks recordings interrupted by an actual daemon restart as closed.
// Read-only spool exports must not infer a restart or close a running incident.
func (e *Engine) Recover(now int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.Incidents {
		if e.Incidents[i].End == 0 {
			e.Incidents[i].End = e.Incidents[i].Updated
			e.Incidents[i].Updated = now
		}
	}
}
