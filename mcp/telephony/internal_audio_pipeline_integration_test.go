//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

type internalAudioReceipt struct {
	Sequence uint32  `json:"sequence"`
	AgeMS    float64 `json:"age_ms"`
	AtNS     int64   `json:"at_ns"`
	Corrupt  bool    `json:"corrupt"`
}
type internalAudioDirection struct {
	Expected   int     `json:"expected_frames"`
	Received   int     `json:"received_frames"`
	Missing    int     `json:"missing_frames"`
	Duplicates int     `json:"duplicates"`
	OutOfOrder int     `json:"out_of_order"`
	Corrupt    int     `json:"corrupt_frames"`
	P95MS      float64 `json:"p95_ms"`
	MaxMS      float64 `json:"max_ms"`
	Over250MS  int     `json:"frames_older_than_250_ms"`
	Tail       int     `json:"last_second_frames"`
}
type internalCoreStats struct {
	Receipts                []internalAudioReceipt `json:"receipts"`
	Attempts                int                    `json:"output_attempts"`
	ProducerSchedulingMaxMS float64                `json:"producer_scheduling_max_ms"`
	ProducerDrops           int                    `json:"fixture_producer_drops"`
	MaxInputQueue           int                    `json:"max_input_queue_frames"`
	MaxOutputQueue          int                    `json:"max_output_queue_frames"`
}
type internalAudioConnection struct {
	conn   net.Conn
	writer *websocketWriterPump
	done   chan struct{}
}
type internalAudioStream struct {
	stopOnce        sync.Once
	readPauseAtNS   int64
	readPauseMS     int
	sendID          string
	receiveID       string
	id              string
	mu              sync.Mutex
	current         *internalAudioConnection
	connections     []*internalAudioConnection
	receipts        []internalAudioReceipt
	closures        []map[string]any
	attempts        int
	schedulingMaxMS float64
}
type internalAudioResult struct {
	Profile     string           `json:"profile"`
	Calls       int              `json:"calls"`
	DurationMS  int              `json:"duration_ms"`
	Expectation string           `json:"expectation"`
	Streams     []map[string]any `json:"streams"`
	Findings    []string         `json:"findings,omitempty"`
	Failures    []string         `json:"gate_failures,omitempty"`
}
type internalAudioHelper struct {
	stopOnce sync.Once
	url      string
	cmd      *exec.Cmd
	done     chan error
	log      *os.File
}

func internalAudioHTTP(t *testing.T, method, target string, body, out any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal("fixture request URL")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 4 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("local fixture request: %T", err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		t.Fatalf("fixture request status %d", res.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}
func startInternalAudioHelper(t *testing.T, component, output, coreURL string) *internalAudioHelper {
	t.Helper()
	binary := os.Getenv("TELEPHONY_INTERNAL_" + strings.ToUpper(component) + "_HELPER")
	if binary == "" {
		t.Fatal("build helper with benchmark:internal-audio")
	}
	ready := filepath.Join(t.TempDir(), "ready")
	log, err := os.Create(filepath.Join(output, component+"-fixture.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run", "^TestTelephonyInternalAudio"+strings.ToUpper(component[:1])+component[1:]+"Fixture$", "-test.timeout", "12m", "-test.v")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "TELEPHONY_INTERNAL_READY="+ready, "TELEPHONY_INTERNAL_CORE_URL="+coreURL, "TELEMETRY_URL=", "TELEMETRY_LIVE_URL=", "SERVER_URL=")
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	helper := &internalAudioHelper{cmd: cmd, log: log, done: make(chan error, 1)}
	go func() { helper.done <- cmd.Wait() }()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(ready); err == nil {
			helper.url = string(raw)
			u, _ := url.Parse(helper.url)
			if u == nil || u.Hostname() != "127.0.0.1" {
				t.Fatal("helper must listen on loopback")
			}
			break
		}
		select {
		case err := <-helper.done:
			log.Close()
			t.Fatalf("%s fixture exited before readiness: %v; see its log", component, err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if helper.url == "" {
		_ = cmd.Process.Kill()
		log.Close()
		t.Fatal(component + " fixture did not start")
	}
	t.Cleanup(func() { helper.stop() })
	return helper
}
func (h *internalAudioHelper) stop() {
	h.stopOnce.Do(func() {
		client := &http.Client{Timeout: time.Second}
		res, err := client.Get(h.url + "/shutdown")
		if err == nil {
			res.Body.Close()
		}
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.done
		}
		_ = h.log.Close()
	})
}

func internalAudioSignature(id string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return h.Sum32()
}
func internalAudioPCM(id string, sequence uint32, at time.Time) []byte {
	p := make([]byte, 960)
	binary.LittleEndian.PutUint32(p, 0x50424d41)
	binary.LittleEndian.PutUint32(p[4:], sequence)
	binary.LittleEndian.PutUint64(p[8:], uint64(at.UnixNano()))
	binary.LittleEndian.PutUint32(p[16:], internalAudioSignature(id))
	for i := 20; i < len(p); i++ {
		p[i] = byte((int(sequence)*31 + i) % 251)
	}
	return p
}
func internalAudioObserve(id string, p []byte) internalAudioReceipt {
	now := time.Now()
	r := internalAudioReceipt{AtNS: now.UnixNano(), Corrupt: len(p) != 960}
	if len(p) < 20 {
		return r
	}
	r.Sequence = binary.LittleEndian.Uint32(p[4:])
	r.AgeMS = float64(now.UnixNano()-int64(binary.LittleEndian.Uint64(p[8:]))) / 1e6
	if binary.LittleEndian.Uint32(p) != 0x50424d41 || binary.LittleEndian.Uint32(p[16:]) != internalAudioSignature(id) {
		r.Corrupt = true
	}
	for i := 20; i < len(p); i++ {
		if p[i] != byte((int(r.Sequence)*31+i)%251) {
			r.Corrupt = true
			break
		}
	}
	return r
}
func (p *internalAudioStream) attach(t *testing.T, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	dialer := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"X-Apteva-Call-ID": {p.id}})}
	c, buffered, _, err := dialer.Dial(ctx, target)
	if err != nil {
		t.Fatalf("local media attachment failed (%T)", err)
	}
	if buffered != nil {
		c = hijackedConn{Conn: c, reader: buffered}
	}
	entry := &internalAudioConnection{conn: c, writer: newWebSocketWriterPump(c, ws.StateClientSide), done: make(chan struct{})}
	t.Cleanup(p.stop)
	p.mu.Lock()
	p.current = entry
	p.connections = append(p.connections, entry)
	p.mu.Unlock()
	go func() {
		defer close(entry.done)
		paused := false
		for {
			if !paused && p.readPauseMS > 0 && time.Now().UnixNano() >= p.readPauseAtNS {
				paused = true
				time.Sleep(time.Duration(p.readPauseMS) * time.Millisecond)
			}
			data, op, err := readWebSocketData(c, ws.StateClientSide, entry.writer)
			if err != nil {
				p.mu.Lock()
				p.closures = append(p.closures, map[string]any{"at": time.Now().UTC(), "error_class": fmt.Sprintf("%T", err), "detail": transportEvidenceError(err)})
				p.mu.Unlock()
				return
			}
			if op == ws.OpBinary {
				r := internalAudioObserve(p.receiveID, data)
				p.mu.Lock()
				p.receipts = append(p.receipts, r)
				p.mu.Unlock()
			}
		}
	}()
}
func (p *internalAudioStream) send(start time.Time, durationMS int) {
	time.Sleep(max(0, time.Until(start)))
	for sequence := uint32(0); sequence < uint32(durationMS/20); sequence++ {
		due := start.Add(time.Duration(sequence) * 20 * time.Millisecond)
		time.Sleep(max(0, time.Until(due)))
		now := time.Now()
		p.mu.Lock()
		entry := p.current
		p.attempts++
		p.schedulingMaxMS = max(p.schedulingMaxMS, float64(now.Sub(due))/float64(time.Millisecond))
		p.mu.Unlock()
		if entry != nil {
			entry.writer.QueueAudio(internalAudioPCM(p.sendID, sequence, now))
		}
	}
}
func (p *internalAudioStream) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		entries := append([]*internalAudioConnection(nil), p.connections...)
		p.mu.Unlock()
		for _, e := range entries {
			newGracefulWebSocket(e.conn, e.writer).Close(ws.StatusNormalClosure, "local benchmark finished")
			e.writer.Stop()
			_ = e.conn.Close()
			<-e.done
		}
	})
}
func internalAudioAnalyze(receipts []internalAudioReceipt, expected, durationMS int) internalAudioDirection {
	result := internalAudioDirection{Expected: expected}
	seen := map[uint32]bool{}
	ages := []float64{}
	var last uint32
	for _, r := range receipts {
		if r.Corrupt || int(r.Sequence) >= expected {
			result.Corrupt++
			continue
		}
		if seen[r.Sequence] {
			result.Duplicates++
			continue
		}
		seen[r.Sequence] = true
		if result.Received > 0 && r.Sequence < last {
			result.OutOfOrder++
		}
		last = r.Sequence
		result.Received++
		ages = append(ages, r.AgeMS)
		if r.AgeMS > 250 {
			result.Over250MS++
		}
		if int(r.Sequence)*20 >= durationMS-1000 {
			result.Tail++
		}
	}
	result.Missing = max(0, expected-result.Received)
	sort.Float64s(ages)
	if len(ages) > 0 {
		result.P95MS = ages[min(len(ages)-1, int(float64(len(ages))*.95))]
		result.MaxMS = ages[len(ages)-1]
	}
	return result
}
func internalAudioGate(result *internalAudioResult, stream string, directions ...internalAudioDirection) {
	for i, d := range directions {
		if d.Corrupt+d.Duplicates+d.OutOfOrder > 0 {
			result.Failures = append(result.Failures, fmt.Sprintf("%s direction %d: corruption/duplicate/order failure", stream, i))
		}
		if result.Expectation == "healthy" && (d.Missing > 0 || d.P95MS > 150 || d.MaxMS > 500) {
			result.Failures = append(result.Failures, fmt.Sprintf("%s direction %d: healthy loss/latency gate failed", stream, i))
		}
	}
}

func runInternalAIProfile(t *testing.T, core, server *internalAudioHelper, name string, calls, durationMS, inputPause, writePause, reset int, direct bool) internalAudioResult {
	result := internalAudioResult{Profile: name, Calls: calls, DurationMS: durationMS, Expectation: "healthy"}
	if inputPause+writePause+reset > 0 {
		result.Expectation = "fault_observation"
	}
	start := time.Now().Add(time.Second)
	streams := make([]*internalAudioStream, calls)
	var senders sync.WaitGroup
	var reconnect func()
	for i := range streams {
		id := fmt.Sprintf("%s-%d", name, i)
		config := map[string]any{"id": id, "start_ns": start.UnixNano(), "duration_ms": durationMS, "pause_at_ms": 1000}
		if i == 0 {
			config["input_pause_ms"] = inputPause
			config["write_pause_ms"] = writePause
			config["reset_at_ms"] = reset
		}
		var attachment map[string]string
		internalAudioHTTP(t, "POST", core.url+"/new", config, &attachment)
		buildURL := func(token string) string {
			base := server.url + "/api/realtime/audio"
			q := url.Values{"agent_id": {"42"}, "thread": {id}, "token": {token}}
			if direct {
				base = core.url + "/realtime/audio"
				q.Del("agent_id")
			}
			return "ws" + strings.TrimPrefix(base, "http") + "?" + q.Encode()
		}
		stream := &internalAudioStream{id: id, sendID: id + "/up", receiveID: id + "/down"}
		stream.attach(t, buildURL(attachment["token"]))
		streams[i] = stream
		senders.Add(1)
		go func() { defer senders.Done(); stream.send(start, durationMS) }()
		if i == 0 && (reset > 0 || inputPause >= 2000) {
			reconnect = func() {
				at := start.Add(time.Duration(1000+inputPause+reset+500) * time.Millisecond)
				time.Sleep(max(0, time.Until(at)))
				var fresh map[string]string
				internalAudioHTTP(t, "POST", core.url+"/renew?id="+id, nil, &fresh)
				stream.attach(t, buildURL(fresh["token"]))
			}
		}
	}
	if reconnect != nil {
		reconnect()
	}
	senders.Wait()
	time.Sleep(500 * time.Millisecond)
	for i, p := range streams {
		var stats internalCoreStats
		internalAudioHTTP(t, "GET", core.url+"/stats?id="+p.id, nil, &stats)
		p.mu.Lock()
		receipts := append([]internalAudioReceipt(nil), p.receipts...)
		attempts := p.attempts
		scheduling := p.schedulingMaxMS
		entries := append([]*internalAudioConnection(nil), p.connections...)
		p.mu.Unlock()
		up := internalAudioAnalyze(stats.Receipts, attempts, durationMS)
		down := internalAudioAnalyze(receipts, stats.Attempts, durationMS)
		internalAudioGate(&result, p.id, up, down)
		if result.Expectation == "fault_observation" && i > 0 && (up.Missing+down.Missing > 0 || up.MaxMS > 500 || down.MaxMS > 500) {
			result.Failures = append(result.Failures, "healthy parallel stream affected by fault")
		}
		if result.Expectation == "fault_observation" && i == 0 {
			if inputPause+writePause > 0 && max(up.MaxMS, down.MaxMS) < 250 {
				result.Failures = append(result.Failures, "injected pause produced no observed latency evidence")
			}
			if up.Tail < 10 || down.Tail < 10 {
				result.Failures = append(result.Failures, "stream did not recover in final second")
			}
			if max(up.MaxMS, down.MaxMS) > 250 {
				result.Findings = append(result.Findings, "frames older than 250 ms reached a consumer; Telephony writer limits alone do not bound the entire path")
			}
		}
		queues := []liveAudioQueueSnapshot{}
		for _, e := range entries {
			queues = append(queues, e.writer.audioSnapshot())
		}
		p.stop()
		p.mu.Lock()
		closures := append([]map[string]any(nil), p.closures...)
		p.mu.Unlock()
		result.Streams = append(result.Streams, map[string]any{"id": p.id, "telephony_to_core": up, "core_to_telephony": down, "telephony_writer": queues, "fixture_producer_drops": stats.ProducerDrops, "core_producer_scheduling_max_ms": stats.ProducerSchedulingMaxMS, "core_input_queue_max_frames": stats.MaxInputQueue, "core_output_queue_max_frames": stats.MaxOutputQueue, "sender_scheduling_max_ms": scheduling, "connections": len(entries), "closures": closures, "incoming_receipts": receipts, "core_receipts": stats.Receipts})
	}
	return result
}

func runInternalHumanProfile(t *testing.T, server *internalAudioHelper, name string, calls, durationMS int, proxy bool, receiverPauseMS ...int) internalAudioResult {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	appServer := softphoneTestServer(t, a)
	internalAudioHTTP(t, "POST", server.url+"/human-target?url="+url.QueryEscape(appServer.URL), nil, nil)
	result := internalAudioResult{Profile: name, Calls: calls, DurationMS: durationMS, Expectation: "healthy"}
	pauseMS := 0
	if len(receiverPauseMS) > 0 {
		pauseMS = receiverPauseMS[0]
	}
	if pauseMS > 0 {
		result.Expectation = "fault_observation"
	}
	start := time.Now().Add(time.Second)
	var senders sync.WaitGroup
	type pair struct {
		browser, peer *internalAudioStream
		call          string
	}
	pairs := []pair{}
	for i := 0; i < calls; i++ {
		id := fmt.Sprintf("%s-%d", name, i)
		row := testCall(id, "in-progress")
		row.Direction = "inbound"
		row.PeerKind = peerKindHuman
		row.PeerToken = "fixture-only-browser"
		row.CallbackSecret = "fixture-only-carrier"
		if err := a.db().insertCall(row); err != nil {
			t.Fatal(err)
		}
		peer := &internalAudioStream{id: id, sendID: id + "/down", receiveID: id + "/up"}
		peer.attach(t, "ws"+strings.TrimPrefix(appServer.URL, "http")+"/peer/"+id+"/"+row.CallbackSecret)
		base := appServer.URL
		if proxy {
			base = server.url + "/human"
		}
		browser := &internalAudioStream{id: id, sendID: id + "/up", receiveID: id + "/down"}
		if i == 0 {
			browser.readPauseMS = pauseMS
			browser.readPauseAtNS = start.Add(time.Second).UnixNano()
		}
		browser.attach(t, "ws"+strings.TrimPrefix(base, "http")+"/softphone/media/"+id+"/"+row.PeerToken)
		pairs = append(pairs, pair{browser, peer, id})
		for _, p := range []*internalAudioStream{browser, peer} {
			senders.Add(1)
			go func() { defer senders.Done(); p.send(start, durationMS) }()
		}
	}
	senders.Wait()
	time.Sleep(500 * time.Millisecond)
	for index, pair := range pairs {
		pair.peer.mu.Lock()
		up := internalAudioAnalyze(pair.peer.receipts, pair.browser.attempts, durationMS)
		pair.peer.mu.Unlock()
		pair.browser.mu.Lock()
		down := internalAudioAnalyze(pair.browser.receipts, pair.peer.attempts, durationMS)
		pair.browser.mu.Unlock()
		internalAudioGate(&result, pair.call, up, down)
		if pauseMS > 0 {
			if index == 0 {
				if down.MaxMS < 250 || down.Tail < 10 {
					result.Failures = append(result.Failures, "browser pause/recovery not observed")
				}
				result.Findings = append(result.Findings, "browser receiver pause can accumulate old audio outside Telephony queues; receipt age exposes it before browser playback protection")
			} else if up.Missing+down.Missing > 0 || max(up.MaxMS, down.MaxMS) > 500 {
				result.Failures = append(result.Failures, "browser stall affected healthy parallel stream")
			}
		}
		diag := a.softphones.hubFor(pair.call).serverAudioSnapshot()
		result.Streams = append(result.Streams, map[string]any{"id": pair.call, "browser_to_peer": up, "peer_to_browser": down, "telephony_diagnostics": diag})
		pair.browser.stop()
		pair.peer.stop()
	}
	return result
}

func TestInternalAudioPipelineBenchmark(t *testing.T) {
	output := os.Getenv("TELEPHONY_INTERNAL_OUTPUT")
	if output == "" {
		t.Skip("opt-in local benchmark: bun run benchmark:internal-audio")
	}
	calls, _ := strconv.Atoi(os.Getenv("TELEPHONY_INTERNAL_CALLS"))
	seconds, _ := strconv.Atoi(os.Getenv("TELEPHONY_INTERNAL_SECONDS"))
	if calls < 2 || calls > 64 || seconds < 6 || seconds > 30 {
		t.Fatal("invalid benchmark bounds")
	}
	core := startInternalAudioHelper(t, "core", output, "")
	server := startInternalAudioHelper(t, "server", output, core.url)
	var results []internalAudioResult
	for _, proxy := range []bool{false, true} {
		name := "human_loopback"
		if proxy {
			name = "human_server_proxy"
		}
		t.Run(name, func(t *testing.T) {
			r := runInternalHumanProfile(t, server, name, calls, seconds*1000, proxy)
			results = append(results, r)
			for _, failure := range r.Failures {
				t.Error(failure)
			}
		})
	}
	for _, pause := range []int{400, 3000} {
		name := fmt.Sprintf("human_browser_pause_%dms", pause)
		t.Run(name, func(t *testing.T) {
			r := runInternalHumanProfile(t, server, name, 2, max(8000, seconds*1000), true, pause)
			results = append(results, r)
			for _, failure := range r.Failures {
				t.Error(failure)
			}
		})
	}
	for _, direct := range []bool{true, false} {
		name := "ai_server_proxy"
		if direct {
			name = "ai_direct_core"
		}
		t.Run(name, func(t *testing.T) {
			r := runInternalAIProfile(t, core, server, name, calls, seconds*1000, 0, 0, 0, direct)
			results = append(results, r)
			for _, failure := range r.Failures {
				t.Error(failure)
			}
		})
	}
	for _, fault := range []struct {
		name                string
		input, write, reset int
	}{{"core_input_pause_400ms", 400, 0, 0}, {"core_input_pause_3000ms", 3000, 0, 0}, {"core_write_pause_400ms", 0, 400, 0}, {"core_write_pause_3000ms", 0, 3000, 0}, {"core_transport_reset", 0, 0, 1000}} {
		t.Run(fault.name, func(t *testing.T) {
			r := runInternalAIProfile(t, core, server, fault.name, 2, max(8000, seconds*1000), fault.input, fault.write, fault.reset, false)
			results = append(results, r)
			for _, failure := range r.Failures {
				t.Error(failure)
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	var proxyEvents []json.RawMessage
	internalAudioHTTP(t, "GET", server.url+"/stats", nil, &proxyEvents)
	server.stop()
	core.stop()
	processCPU := map[string]any{}
	for name, h := range map[string]*internalAudioHelper{"core": core, "server": server} {
		if h.cmd.ProcessState != nil {
			processCPU[name] = map[string]any{"user_cpu_ms": h.cmd.ProcessState.UserTime().Milliseconds(), "system_cpu_ms": h.cmd.ProcessState.SystemTime().Milliseconds(), "success": h.cmd.ProcessState.Success()}
		}
	}
	report := map[string]any{"schema": "telephony-internal-audio/v1", "created_at": time.Now().UTC(), "scope": "production Telephony writer and human socket handlers, actual Apteva realtime/app proxies and actual Core audio handler; synthetic PCM and fake AI provider; no real carrier/browser/AI provider; local source versions recorded separately", "limits": "transport/component benchmark, not total call latency or a production capacity proof; same-host wall timestamps; producer fixture drops tracked separately; pauses are injected at consumer or Core socket writer, not attributed to a real network", "results": results, "proxy_events": proxyEvents, "helper_process_cpu": processCPU}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "results.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("benchmark report saved; adverse profiles expose problems rather than relabelling them healthy")
}

// Keep benchmark integrity/accounting honest independently of socket timing.
func TestInternalAudioProbeIntegrity(t *testing.T) {
	p := internalAudioPCM("call/up", 3, time.Now())
	if internalAudioObserve("call/up", p).Corrupt {
		t.Fatal("valid PCM rejected")
	}
	if !internalAudioObserve("call/down", p).Corrupt {
		t.Fatal("cross-direction corruption not detected")
	}
	p[400] ^= 1
	if !internalAudioObserve("call/up", p).Corrupt {
		t.Fatal("sample corruption not detected")
	}
	if !internalAudioObserve("call/up", p[:10]).Corrupt {
		t.Fatal("short frame not detected")
	}
}
func TestInternalAudioProbeAccounting(t *testing.T) {
	result := internalAudioAnalyze([]internalAudioReceipt{
		{Sequence: 0, AgeMS: 1}, {Sequence: 2, AgeMS: 300},
		{Sequence: 2, AgeMS: 301}, {Sequence: 1, AgeMS: 2},
		{Sequence: 3, Corrupt: true}, {Sequence: 99},
	}, 4, 80)
	if result.Received != 3 || result.Missing != 1 || result.Duplicates != 1 || result.OutOfOrder != 1 || result.Corrupt != 2 || result.Over250MS != 1 || result.MaxMS != 300 {
		t.Fatalf("incorrect accounting: %+v", result)
	}
	healthy := internalAudioResult{Expectation: "healthy"}
	internalAudioGate(&healthy, "test", result)
	if len(healthy.Failures) != 2 {
		t.Fatal("healthy profile hid loss/integrity failure")
	}
	fault := internalAudioResult{Expectation: "fault_observation"}
	internalAudioGate(&fault, "test", internalAudioDirection{Missing: 3, MaxMS: 3000})
	if len(fault.Failures) != 0 {
		t.Fatal("expected injected fault incorrectly treated as healthy requirement")
	}
}
