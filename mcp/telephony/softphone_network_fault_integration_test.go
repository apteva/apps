//go:build integration

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"
)

// Packet faults, never fake getStats counters: damage only the original direct
// SRTP path. STUN/DTLS keep it connected; the alternate TURN route stays healthy.
type networkFaultState struct {
	mu      sync.Mutex
	direct  string
	packets int
	armed   atomic.Bool
	dropped atomic.Int64
}

func (s *networkFaultState) drop(data []byte, remote net.Addr) bool {
	if len(data) < 12 || data[0]&0xc0 != 0x80 || data[1]&0x7f != 111 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.direct == "" && !net.ParseIP(strings.Split(remote.String(), ":")[0]).IsLoopback() {
		s.direct = remote.String()
	}
	if !s.armed.Load() || s.direct != remote.String() {
		return false
	}
	s.packets++
	discard := s.packets%50 < 15 || s.packets%3 == 0
	if discard {
		s.dropped.Add(1)
	}
	return discard
}

type networkFaultNet struct {
	transport.Net
	state *networkFaultState
}
type networkFaultUDP struct {
	transport.UDPConn
	state *networkFaultState
}

func (n *networkFaultNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	return &networkFaultUDP{c, n.state}, nil
}
func (c *networkFaultUDP) WriteTo(p []byte, a net.Addr) (int, error) {
	if c.state.drop(p, a) {
		return len(p), nil
	}
	return c.UDPConn.WriteTo(p, a)
}
func (c *networkFaultUDP) WriteToUDP(p []byte, a *net.UDPAddr) (int, error) {
	if c.state.drop(p, a) {
		return len(p), nil
	}
	return c.UDPConn.WriteToUDP(p, a)
}

func TestNetworkBrowserDirectVersusRelayRealPacketLoss(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "fault.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "frontend/tests/network-fault-browser-entry.ts", "--target=browser", "--outfile="+bundle)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	source, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	worklet, _ := os.ReadFile("ui/softphone-worklet.js")
	endpoint, _ := localTURNTestRelay(t, "udp")
	var results []json.RawMessage
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "impaired-direct", true: "preselected-healthy-relay"}[enabled], func(t *testing.T) {
			state := &networkFaultState{}
			policy := defaultPhoneNetworkPolicy()
			base, err := stdnet.NewNet()
			if err != nil {
				t.Fatal(err)
			}
			network := &networkFaultNet{base, state}
			c := softphoneRTCConfig{Enabled: true, Bitrate: 32000, NetworkPolicy: &policy, ICEPolicy: "all", ClientICEServers: []webrtc.ICEServer{{URLs: []string{endpoint}, Username: "temporary-v1", Credential: "local-test-password"}}}
			if enabled {
				c.ICEPolicy = "relay"
			}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config":
					writeJSON(w, map[string]any{"enabled": enabled, "media_url": "ws" + strings.TrimPrefix(server.URL, "http") + "/media"})
				case "/arm":
					state.armed.Store(true)
					w.WriteHeader(204)
				case "/module.js":
					w.Header().Set("Content-Type", "text/javascript")
					w.Write(source)
				case "/worklet.js":
					w.Header().Set("Content-Type", "text/javascript")
					w.Write(worklet)
				case "/media":
					conn, _, _, err := ws.UpgradeHTTP(r, w)
					if err != nil {
						return
					}
					hub, stop, err := connectSoftphoneRTC(conn, conn, c, func(e *webrtc.SettingEngine) {
						e.SetNet(network)
						e.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
						e.SetInterfaceFilter(func(name string) bool { return name == "lo0" || name == "lo" })
					})
					if err != nil {
						t.Log("fixture setup", err)
						return
					}
					defer stop()
					go func() {
						for {
							if _, _, err := wsutil.ReadClientData(hub); err != nil {
								return
							}
						}
					}()
					for _, body := range []string{`{"type":"ready"}`, `{"type":"peer.connected"}`, `{"type":"call.status","status":"answered"}`} {
						if wsutil.WriteServerText(hub, []byte(body)) != nil {
							return
						}
					}
					tick := time.NewTicker(20 * time.Millisecond)
					defer tick.Stop()
					for {
						select {
						case <-r.Context().Done():
							return
						case <-tick.C:
							if wsutil.WriteServerBinary(hub, pcm16ToBytes(sinePCM(24000, 440, 480))) != nil {
								return
							}
						}
					}
				default:
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte(`<script type="module" src="/module.js"></script>`))
				}
			}))
			defer server.Close()
			command := exec.CommandContext(t.Context(), "bun", "frontend/tests/network-fault-browser-run.ts")
			command.Env = append(os.Environ(), "TELEPHONY_NETWORK_BROWSER_ORIGIN="+server.URL)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("browser real packet faults: %v\n%s", err, output)
			}
			var result map[string]any
			if json.Unmarshal(output, &result) != nil || (!enabled && state.dropped.Load() < 100) {
				t.Fatal("packet fault not exercised", string(output), state.dropped.Load())
			}
			result["actual_discarded_datagrams"] = state.dropped.Load()
			raw, _ := json.Marshal(result)
			results = append(results, raw)
			t.Log(string(raw))
		})
	}
	if len(results) == 2 {
		var before, after map[string]any
		json.Unmarshal(results[0], &before)
		json.Unmarshal(results[1], &after)
		if after["lost_packets"].(float64) >= before["lost_packets"].(float64)*.7 {
			t.Fatal("selecting a healthy path did not materially reduce observed packet loss", string(results[0]), string(results[1]))
		}
		if path := os.Getenv("TELEPHONY_NETWORK_BROWSER_RESULTS"); path != "" {
			raw, _ := json.MarshalIndent(map[string]any{"scope": "real Chromium, production WebRTC bridge, actual direct SRTP packet drops; impaired direct vs healthy relay selected before calling; no mid-call route switch", "before": before, "after": after}, "", "  ")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
