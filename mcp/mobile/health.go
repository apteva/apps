package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DeviceHealth is the latest snapshot of an Android phone, read with one
// adb shell call. Pointer fields are omitted when the phone did not report them.
type DeviceHealth struct {
	CheckedAt        string   `json:"checked_at"`
	BatteryLevel     *int     `json:"battery_level,omitempty"`
	BatteryTempC     *float64 `json:"battery_temp_c,omitempty"`
	BatteryHealth    string   `json:"battery_health,omitempty"`
	Charging         *bool    `json:"charging,omitempty"`
	ThermalStatus    *int     `json:"thermal_status,omitempty"` // 0 none .. 6 shutdown
	WifiEnabled      *bool    `json:"wifi_enabled,omitempty"`
	WifiConnected    *bool    `json:"wifi_connected,omitempty"`
	SignalLevel      *int     `json:"signal_level,omitempty"` // 0..4
	ScreenOn         *bool    `json:"screen_on,omitempty"`
	FreeStorageBytes *int64   `json:"free_storage_bytes,omitempty"`
	UptimeSeconds    *int64   `json:"uptime_seconds,omitempty"`
	Error            string   `json:"error,omitempty"`
	Alerts           []string `json:"alerts"`
}

const (
	hotBatteryC     = 42.0
	lowBattery      = 15
	lowStorageBytes = int64(2) << 30
)

type phoneState struct {
	online       bool
	adbState     string
	transport    string // adb transport_id; a new one means the phone re-attached
	lastSeen     time.Time
	offlineSince time.Time
	health       *DeviceHealth
	reconnecting bool
}

type healthMonitor struct {
	e      *engine
	mu     sync.Mutex
	phones map[string]*phoneState // by ADB serial
	primed bool
}

func newHealthMonitor(e *engine) *healthMonitor {
	return &healthMonitor{e: e, phones: map[string]*phoneState{}}
}

// start runs presence checks (fast, for offline detection) and health reads.
// Tests leave the intervals at zero, which disables both loops.
func (m *healthMonitor) start() {
	cfg := m.e.drivers.cfg
	if cfg.PresenceInterval <= 0 {
		return
	}
	m.e.wg.Add(1)
	go func() {
		defer m.e.wg.Done()
		presence := time.NewTicker(cfg.PresenceInterval)
		defer presence.Stop()
		var health <-chan time.Time
		if cfg.HealthInterval > 0 {
			t := time.NewTicker(cfg.HealthInterval)
			defer t.Stop()
			health = t.C
		}
		m.scan()
		m.readAll()
		for {
			select {
			case <-m.e.ctx.Done():
				return
			case <-presence.C:
				m.scan()
				m.expirePaused()
			case <-health:
				m.readAll()
			}
		}
	}()
}

type adbListing struct{ state, transport string }

// parseADBDevices reads "adb devices -l": serial, state and transport_id.
func parseADBDevices(out string) map[string]adbListing {
	found := map[string]adbListing{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "List" || strings.HasPrefix(f[0], "*") {
			continue
		}
		l := adbListing{state: f[1]}
		for _, kv := range f[2:] {
			if t, ok := strings.CutPrefix(kv, "transport_id:"); ok {
				l.transport = t
			}
		}
		found[f[0]] = l
	}
	return found
}

// scan compares adb's device list with the last one and handles transitions.
func (m *healthMonitor) scan() {
	ctx, cancel := context.WithTimeout(m.e.ctx, 5*time.Second)
	defer cancel()
	out, err := m.e.drivers.command(ctx, "adb", "devices", "-l")
	if err != nil {
		return // adb itself unavailable: not evidence that phones left
	}
	found := parseADBDevices(string(out))
	now := time.Now()
	var wentOffline, cameBack []string
	m.mu.Lock()
	for serial, listing := range found {
		p := m.phones[serial]
		if p == nil {
			p = &phoneState{}
			m.phones[serial] = p
		}
		state := listing.state
		p.adbState = state
		online := state == "device"
		if online {
			p.lastSeen = now
		}
		// A new transport id while online means the phone left and came back
		// between two checks: handle it as a drop so sessions and streams
		// reconnect instead of keeping dead adb connections.
		if m.primed && online && p.online && listing.transport != "" && p.transport != "" && listing.transport != p.transport {
			wentOffline = append(wentOffline, serial)
			cameBack = append(cameBack, serial)
		}
		if listing.transport != "" {
			p.transport = listing.transport
		}
		if m.primed && online && !p.online {
			cameBack = append(cameBack, serial)
		}
		if m.primed && !online && p.online {
			wentOffline = append(wentOffline, serial)
		}
		if !online && p.online {
			p.offlineSince = now
		}
		p.online = online
	}
	for serial, p := range m.phones {
		if _, ok := found[serial]; !ok {
			if p.online {
				p.offlineSince = now
				if m.primed {
					wentOffline = append(wentOffline, serial)
				}
			}
			p.online = false
			p.adbState = "missing"
		}
	}
	m.primed = true
	m.mu.Unlock()
	for _, serial := range wentOffline {
		m.offline(serial)
	}
	for _, serial := range cameBack {
		m.online(serial)
	}
}

func (m *healthMonitor) deviceID(serial string) string {
	for _, s := range m.adbSessions(serial) {
		return s.DeviceID
	}
	return "android:" + serial
}

func (m *healthMonitor) adbSessions(serial string) []Session {
	all, err := m.e.allActive()
	if err != nil {
		return nil
	}
	out := []Session{}
	for _, s := range all {
		if s.Device.Platform == "android" && s.Device.Driver == "adb" && s.Device.UDID == serial {
			out = append(out, s)
		}
	}
	return out
}

// offline pauses the phone's session (it keeps the phone) and retries.
func (m *healthMonitor) offline(serial string) {
	now := time.Now()
	for _, s := range m.adbSessions(serial) {
		if s.Status != "active" {
			continue
		}
		_, _ = m.e.db.Exec(`UPDATE mobile_sessions SET status='paused' WHERE id=? AND status='active'`, s.ID)
		m.e.streams.suspend(s.ID)
		_, _ = m.e.db.Exec(`INSERT INTO mobile_session_meta(session_id,paused_at) VALUES (?,?) ON CONFLICT(session_id) DO UPDATE SET paused_at=excluded.paused_at`, s.ID, now.Unix())
		m.e.ops.cancelAll(s.ID)
	}
	m.e.events.add("", "mobile.device.offline", map[string]any{"device_id": m.deviceID(serial), "time": now.UTC().Format(time.RFC3339)})
	m.reconnect(serial)
}

// online resumes paused sessions and restarts their streams.
func (m *healthMonitor) online(serial string) {
	for _, s := range m.adbSessions(serial) {
		if s.Status != "paused" {
			continue
		}
		_, _ = m.e.db.Exec(`UPDATE mobile_sessions SET status='active' WHERE id=? AND status='paused'`, s.ID)
		_, _ = m.e.db.Exec(`UPDATE mobile_session_meta SET paused_at=NULL WHERE session_id=?`, s.ID)
		m.e.streams.deviceBack(s.ID)
	}
	m.e.events.add("", "mobile.device.online", map[string]any{"device_id": m.deviceID(serial), "time": time.Now().UTC().Format(time.RFC3339)})
	go m.read(serial)
}

// reconnect asks adb to re-attach a phone it lists as offline, backing off
// from 0.5 s to 10 s. A phone that left USB entirely comes back through scan.
func (m *healthMonitor) reconnect(serial string) {
	m.mu.Lock()
	p := m.phones[serial]
	if p == nil || p.reconnecting {
		m.mu.Unlock()
		return
	}
	p.reconnecting = true
	m.mu.Unlock()
	m.e.wg.Add(1)
	go func() {
		defer m.e.wg.Done()
		defer func() { m.mu.Lock(); p.reconnecting = false; m.mu.Unlock() }()
		delay := 500 * time.Millisecond
		for {
			timer := time.NewTimer(delay)
			select {
			case <-m.e.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			m.mu.Lock()
			online, state := p.online, p.adbState
			m.mu.Unlock()
			if online {
				return
			}
			if m.e.drivers.cfg.OfflineGrace > 0 && len(m.pausedSessions(serial)) == 0 && time.Since(p.offlineSince) > m.e.drivers.cfg.OfflineGrace {
				return // nothing waits on this phone any more; scan still notices it
			}
			ctx, cancel := context.WithTimeout(m.e.ctx, 5*time.Second)
			if state == "offline" {
				_, _ = m.e.drivers.command(ctx, "adb", "-s", serial, "reconnect")
			}
			cancel()
			m.scan()
			if delay < 10*time.Second {
				delay *= 2
				if delay > 10*time.Second {
					delay = 10 * time.Second
				}
			}
		}
	}()
}

func (m *healthMonitor) pausedSessions(serial string) []Session {
	out := []Session{}
	for _, s := range m.adbSessions(serial) {
		if s.Status == "paused" {
			out = append(out, s)
		}
	}
	return out
}

// expirePaused ends sessions whose phone stayed away past offline_grace.
func (m *healthMonitor) expirePaused() {
	grace := m.e.drivers.cfg.OfflineGrace
	if grace <= 0 {
		return
	}
	all, err := m.e.allActive()
	if err != nil {
		return
	}
	for _, s := range all {
		if s.Status != "paused" || s.PausedAt == nil || time.Since(time.Unix(*s.PausedAt, 0)) < grace {
			continue
		}
		g := m.e.gate(s.DeviceID)
		if !g.mu.TryLock() {
			continue
		}
		ctx, cancel := context.WithTimeout(m.e.ctx, 10*time.Second)
		if fresh, err := m.e.session(s.ProjectID, s.ID); err == nil && fresh.Status == "paused" {
			_ = m.e.finishWait(ctx, fresh, g, "device_lost", 5*time.Second, "", "")
		}
		cancel()
		g.mu.Unlock()
	}
}

func (m *healthMonitor) readAll() {
	m.mu.Lock()
	serials := []string{}
	for serial, p := range m.phones {
		if p.online {
			serials = append(serials, serial)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, serial := range serials {
		wg.Add(1)
		go func(serial string) { defer wg.Done(); m.read(serial) }(serial)
	}
	wg.Wait()
}

const healthScript = `echo @battery; dumpsys battery; echo @thermal; dumpsys thermalservice 2>/dev/null | grep -m1 'Thermal Status'; echo @wifi; cmd wifi status 2>/dev/null | grep -E '^Wifi is (enabled|disabled|connected|not connected)'; echo @signal; dumpsys telephony.registry 2>/dev/null | grep -m1 mSignalStrength; echo @screen; dumpsys power | grep -m1 -E 'mWakefulness='; echo @storage; df -k /data | tail -n 1; echo @uptime; cat /proc/uptime`

func (m *healthMonitor) read(serial string) {
	ctx, cancel := context.WithTimeout(m.e.ctx, 8*time.Second)
	defer cancel()
	out, err := m.e.drivers.adb(ctx, serial, "shell", healthScript)
	h := parseHealth(string(out))
	if err != nil {
		h = &DeviceHealth{Error: "health check failed"}
	}
	h.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	h.Alerts = healthAlerts(h)
	m.mu.Lock()
	if p := m.phones[serial]; p != nil {
		p.health = h
	}
	m.mu.Unlock()
}

var (
	reKV       = regexp.MustCompile(`^\s*([A-Za-z ]+):\s*(\S+)`)
	reThermal  = regexp.MustCompile(`Thermal Status:\s*(\d+)`)
	reSignal   = regexp.MustCompile(`level=(\d)`)
	reWake     = regexp.MustCompile(`mWakefulness=(\w+)`)
	batteryMap = map[string]string{"1": "unknown", "2": "good", "3": "overheat", "4": "dead", "5": "over_voltage", "6": "failure", "7": "cold"}
)

func parseHealth(out string) *DeviceHealth {
	h := &DeviceHealth{Alerts: []string{}}
	section := ""
	charging := false
	sawPower := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "@") {
			section = strings.TrimPrefix(line, "@")
			continue
		}
		switch section {
		case "battery":
			m := reKV.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			k, v := strings.TrimSpace(m[1]), m[2]
			switch k {
			case "AC powered", "USB powered", "Wireless powered", "Dock powered":
				sawPower = true
				if v == "true" {
					charging = true
				}
			case "level":
				if n, err := strconv.Atoi(v); err == nil {
					h.BatteryLevel = &n
				}
			case "temperature":
				if n, err := strconv.Atoi(v); err == nil {
					c := float64(n) / 10
					h.BatteryTempC = &c
				}
			case "health":
				h.BatteryHealth = batteryMap[v]
			}
		case "thermal":
			if m := reThermal.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				h.ThermalStatus = &n
			}
		case "wifi":
			l := strings.ToLower(line)
			if strings.Contains(l, "wifi is enabled") {
				t := true
				h.WifiEnabled = &t
			}
			if strings.Contains(l, "wifi is disabled") {
				f := false
				h.WifiEnabled, h.WifiConnected = &f, &f
			}
			if strings.Contains(l, "wifi is connected") {
				t := true
				h.WifiConnected = &t
			}
			if strings.Contains(l, "wifi is not connected") {
				f := false
				h.WifiConnected = &f
			}
		case "signal":
			if m := reSignal.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				h.SignalLevel = &n
			}
		case "screen":
			if m := reWake.FindStringSubmatch(line); m != nil {
				on := m[1] == "Awake"
				h.ScreenOn = &on
			}
		case "storage":
			f := strings.Fields(line)
			if len(f) >= 4 {
				if kb, err := strconv.ParseInt(f[3], 10, 64); err == nil {
					b := kb * 1024
					h.FreeStorageBytes = &b
				}
			}
		case "uptime":
			f := strings.Fields(line)
			if len(f) >= 1 {
				if sec, err := strconv.ParseFloat(f[0], 64); err == nil {
					u := int64(sec)
					h.UptimeSeconds = &u
				}
			}
		}
	}
	if sawPower {
		h.Charging = &charging
	}
	if h.BatteryLevel == nil && h.UptimeSeconds == nil {
		h.Error = "health check failed"
	}
	return h
}

func healthAlerts(h *DeviceHealth) []string {
	alerts := []string{}
	if h.Error != "" {
		return append(alerts, "health_check_failed")
	}
	if h.BatteryTempC != nil && *h.BatteryTempC >= hotBatteryC {
		alerts = append(alerts, "hot")
	}
	if h.BatteryLevel != nil && *h.BatteryLevel <= lowBattery && h.Charging != nil && !*h.Charging {
		alerts = append(alerts, "low_battery")
	}
	if (h.WifiEnabled != nil && !*h.WifiEnabled) || (h.WifiConnected != nil && !*h.WifiConnected) {
		alerts = append(alerts, "wifi_down")
	}
	if h.FreeStorageBytes != nil && *h.FreeStorageBytes < lowStorageBytes {
		alerts = append(alerts, "low_storage")
	}
	return alerts
}

// decorate adds the latest health and last-seen time to an ADB device.
func (m *healthMonitor) decorate(d *Device) {
	if d.Platform != "android" || d.Driver != "adb" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.phones[d.UDID]
	if p == nil {
		return
	}
	if !p.lastSeen.IsZero() {
		d.LastSeenAt = p.lastSeen.UTC().Format(time.RFC3339)
	}
	d.Health = p.health
}

// snapshot reports every phone the monitor knows, for the runner's health.
func (m *healthMonitor) snapshot() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for serial, p := range m.phones {
		row := map[string]any{"device_id": "android:" + serial, "online": p.online, "adb_state": p.adbState, "health": p.health}
		if !p.lastSeen.IsZero() {
			row["last_seen_at"] = p.lastSeen.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}
