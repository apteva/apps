package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

const networkCheckTTL = 25 * time.Second
const networkCheckPacketBytes = 96

// Diagnostics cannot occupy the credential-service slots reserved for calls.
var networkCheckCredentialRequests = make(chan struct{}, 1)

// At most four diagnostic peers, one per principal, independent of call slots.
// No call, recording, media claim, worker or audio codec is created.
type networkCheckRegistry struct {
	mu       sync.Mutex
	sessions map[string]*networkCheckSession
	recent   map[string]time.Time
}
type networkCheckSession struct {
	reservedMin, reservedMax uint16
	release                  func()
	mu                       sync.Mutex
	id, owner, project       string
	config                   softphoneRTCConfig
	pc                       *webrtc.PeerConnection
	timer                    *time.Timer
	done                     chan struct{}
	closed                   bool
	offers                   bool
}

func (r *networkCheckRegistry) create(project, owner string, c softphoneRTCConfig, now time.Time, paths ...string) (*networkCheckSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*networkCheckSession{}
		r.recent = map[string]time.Time{}
	}
	for key, at := range r.recent {
		if now.Sub(at) > time.Minute {
			delete(r.recent, key)
		}
	}
	key := project + "\x00" + owner
	if len(paths) > 0 {
		key += "\x00" + paths[0]
	}
	if len(r.sessions) >= 4 || len(r.recent) >= 512 || now.Sub(r.recent[key]) < 15*time.Second {
		return nil, errors.New("network checks busy; calls remain available")
	}
	for _, s := range r.sessions {
		if s.project == project && s.owner == owner {
			return nil, errors.New("network check already running")
		}
	}
	s := &networkCheckSession{id: newAudioSessionID(), project: project, owner: owner, config: c, reservedMin: c.PortMin, reservedMax: c.PortMax, done: make(chan struct{})}
	r.sessions[s.id] = s
	r.recent[key] = now
	s.timer = time.AfterFunc(networkCheckTTL, func() { r.remove(s) })
	return s, nil
}
func (r *networkCheckRegistry) remove(s *networkCheckSession) {
	r.mu.Lock()
	if r.sessions[s.id] == s {
		delete(r.sessions, s.id)
	}
	r.mu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	if s.timer != nil {
		s.timer.Stop()
	}
	pc := s.pc
	release := s.release
	s.release = nil
	s.mu.Unlock()
	if pc != nil {
		go func() {
			_ = pc.Close()
			if release != nil {
				release()
			}
		}()
	} else if release != nil {
		release()
	}
}
func (r *networkCheckRegistry) stopOwner(project, owner string) {
	r.mu.Lock()
	var stopped []*networkCheckSession
	for _, s := range r.sessions {
		if s.project == project && s.owner == owner {
			stopped = append(stopped, s)
		}
	}
	r.mu.Unlock()
	for _, s := range stopped {
		r.remove(s)
	}
}
func networkCheckOwner(r *http.Request) string {
	if p := phoneUserFrom(r); p != nil {
		return p.Identity.key()
	}
	return "operator"
}
func (a *App) handleNetworkCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app unavailable", 503)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", 403)
		return
	}
	var input struct {
		Action string `json:"action"`
		ID     string `json:"id"`
		Path   string `json:"path"`
		SDP    string `json:"sdp"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid network check", 400)
		return
	}
	owner := networkCheckOwner(r)
	if input.Action == "prepare" {
		if input.Path != "configured" && input.Path != "direct" && input.Path != "relay" {
			http.Error(w, "invalid path", 400)
			return
		}
		var identity phoneIdentity
		if p := phoneUserFrom(r); p != nil {
			identity = p.Identity
		}
		// Never add a diagnostic load to an adviser's occupied media path.
		if identity.valid() {
			var occupied bool
			err = a.db().db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM calls c INDEXED BY idx_calls_live_occupancy CROSS JOIN telephony_call_owners o ON o.call_id=c.id AND o.project_id=c.project_id WHERE c.project_id=? AND c.peer_kind='human' AND c.status NOT IN ('completed','failed','busy','no-answer','canceled') AND o.principal=?)`, project, identity.key()).Scan(&occupied)
			if err != nil {
				http.Error(w, "occupancy unavailable", 503)
				return
			}
			if occupied {
				http.Error(w, "network checks pause during calls", 409)
				return
			}
		}
		config, err := parseSoftphoneRTCConfig(globalCtx.WithProject(project).Config())
		attachRTCResourcePolicy(&config, globalCtx)
		if err != nil || !config.Enabled {
			writeJSONStatus(w, 409, map[string]any{"code": "webrtc_unavailable"})
			return
		}
		// Reserve before credential RPC, so even stalled requests are capacity-bounded.
		release, admissionErr := a.runtimeBegin(true)
		if admissionErr != nil {
			http.Error(w, "runtime not admitting", 503)
			return
		}
		session, err := a.networkChecks.create(project, owner, config, time.Now(), input.Path)
		if err != nil {
			release()
			http.Error(w, err.Error(), 429)
			return
		}
		session.mu.Lock()
		closed := session.closed
		if !closed {
			session.release = release
		}
		session.mu.Unlock()
		if closed {
			release()
			http.Error(w, "probe expired", 410)
			return
		}
		config, err = a.networkCheckConfig(r.Context(), project, identity, input.Path, config)
		if err == nil {
			err = networkCheckPorts(&config, globalCtx.WithProject(project).Config())
		}
		if err != nil {
			a.networkChecks.remove(session)
			http.Error(w, "network test path unavailable", 409)
			return
		}
		session.mu.Lock()
		if session.closed {
			session.mu.Unlock()
			http.Error(w, "network check expired", 410)
			return
		}
		session.config = config
		session.mu.Unlock()
		if r.Context().Err() != nil {
			a.networkChecks.remove(session)
			return
		}
		writeJSON(w, map[string]any{"id": session.id, "ice_servers": config.ClientICEServers, "ice_transport_policy": config.ICEPolicy, "expires_in_ms": 25000, "sample_ms": 6000, "packet_bytes": networkCheckPacketBytes, "interval_ms": 20, "kind": "unreliable_datachannel", "path": input.Path})
		return
	}
	a.networkChecks.mu.Lock()
	s := a.networkChecks.sessions[input.ID]
	a.networkChecks.mu.Unlock()
	if s == nil || s.owner != owner || s.project != project {
		http.NotFound(w, r)
		return
	}
	if input.Action == "cancel" {
		a.networkChecks.remove(s)
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if input.Action != "offer" {
		http.Error(w, "invalid network check action", 400)
		return
	}
	answer, err := a.answerNetworkCheck(r.Context(), s, input.SDP)
	if err != nil {
		a.networkChecks.remove(s)
		http.Error(w, "network check negotiation failed", 400)
		return
	}
	writeJSON(w, map[string]any{"sdp": answer})
}
func (a *App) networkCheckConfig(ctx context.Context, project string, identity phoneIdentity, path string, c softphoneRTCConfig) (softphoneRTCConfig, error) {
	policy, err := a.resolvedTURNPolicy(project, identity, nil)
	if err != nil {
		return c, err
	}
	if path != "configured" && path != "direct" && path != "relay" {
		return c, errors.New("invalid test path")
	}
	if path == "direct" && policy.ICEPolicy == "relay" {
		return c, errors.New("relay required")
	}
	c.ICEPolicy = policy.ICEPolicy
	if path == "relay" {
		c.ICEPolicy = "relay"
	}
	c.ClientICEServers = selectTURNTransport(c.ICEServers, policy.Transport, policy.TLSPort)
	if path == "direct" {
		c.ClientICEServers = selectSTUN(c.ClientICEServers)
	} else if policy.Source == "app" {
		appCtx := globalCtx.WithProject(project)
		if appCtx.IntegrationFor("turn") == nil {
			return c, errors.New("TURN unbound")
		}
		provider := rtcTURNProvider{policy: policy, fetch: func(request context.Context) (rtcTURNCredentials, error) {
			var credentials rtcTURNCredentials
			args := map[string]any{"session_id": "probe-" + newAudioSessionID(), "ttl_seconds": 60, "transport": policy.Transport}
			if policy.TLSPort != 0 {
				args["tls_port"] = policy.TLSPort
			}
			err := sdk.CallAppResultContext(request, appCtx.PlatformAPI(), "turn", "turn_credentials", args, &credentials)
			return credentials, err
		}}
		select {
		case networkCheckCredentialRequests <- struct{}{}:
		default:
			return c, errors.New("network check credential capacity unavailable")
		}
		request, cancel := context.WithTimeout(ctx, 2*time.Second)
		credentials, e := provider.fetch(request)
		cancel()
		<-networkCheckCredentialRequests
		if e == nil {
			credentials, e = provider.validated(credentials)
		}
		if e != nil {
			return c, e
		}
		c.ClientICEServers = credentials.ICEServers
	}
	if c.ClientICEServers == nil {
		c.ClientICEServers = []webrtc.ICEServer{}
	}
	if c.ICEPolicy == "relay" && !hasTURN(c.ClientICEServers) {
		return c, errors.New("relay unavailable")
	}
	// Only the browser uses managed TURN, just as it does during real calls.
	c.ICEServers = selectSTUN(c.ICEServers)
	return c, nil
}
func (a *App) answerNetworkCheck(ctx context.Context, s *networkCheckSession, offer string) (string, error) {
	var description sdp.SessionDescription
	if len(offer) > 24000 || description.Unmarshal([]byte(offer)) != nil || len(description.MediaDescriptions) != 1 || description.MediaDescriptions[0].MediaName.Media != "application" {
		return "", errors.New("datachannel SDP required")
	}
	s.mu.Lock()
	if s.closed || s.offers {
		s.mu.Unlock()
		return "", errors.New("probe already used")
	}
	s.offers = true
	config := s.config
	s.mu.Unlock()
	network, err := networkCheckNetwork(s.reservedMin, s.reservedMax)
	if err != nil {
		return "", err
	}
	api, err := softphoneRTCAPI(config, func(settings *webrtc.SettingEngine) {
		settings.SetNet(network)
		settings.SetSCTPMaxMessageSize(512)
		settings.SetSCTPMaxReceiveBufferSize(32768)
	})
	if err != nil {
		return "", err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: config.ICEServers})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		pc.Close()
		return "", errors.New("expired")
	}
	s.pc = pc
	s.mu.Unlock()
	var channelOnce sync.Once
	pc.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != "telephony-network-check" || channel.Ordered() || channel.MaxRetransmits() == nil || *channel.MaxRetransmits() != 0 {
			a.networkChecks.remove(s)
			return
		}
		admitted := false
		channelOnce.Do(func() { admitted = true })
		if !admitted {
			a.networkChecks.remove(s)
			return
		}
		var mu sync.Mutex
		var received, highest uint32
		var stamp uint64
		var jitter float64
		var last time.Time
		var window time.Time
		var windowPackets int
		var seen [1000]bool
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if len(message.Data) != networkCheckPacketBytes || binary.LittleEndian.Uint32(message.Data) != 0x31504e54 {
				a.networkChecks.remove(s)
				return
			}
			now := time.Now()
			mu.Lock()
			if now.Sub(window) > time.Second {
				window = now
				windowPackets = 0
			}
			windowPackets++
			tooFast := windowPackets > 75 || received >= 1000
			seq := binary.LittleEndian.Uint32(message.Data[4:])
			if seq >= 1000 {
				tooFast = true
			} else if !seen[seq] {
				seen[seq] = true
				received++
				sampleStamp := binary.LittleEndian.Uint64(message.Data[8:])
				if received == 1 || seq > highest {
					highest = seq
				}
				if !last.IsZero() {
					delta := math.Abs(float64(now.Sub(last))/float64(time.Millisecond) - float64(int64(sampleStamp)-int64(stamp))/1000)
					jitter += (delta - jitter) / 16
				}
				stamp = sampleStamp
				last = now
			}
			mu.Unlock()
			if tooFast {
				a.networkChecks.remove(s)
			}
		})
		channel.OnOpen(func() {
			go func() {
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				duration := time.NewTimer(8 * time.Second)
				defer duration.Stop()
				started := time.Now()
				var seq uint32
				packet := make([]byte, networkCheckPacketBytes)
				for {
					select {
					case <-s.done:
						return
					case <-duration.C:
						a.networkChecks.remove(s)
						return
					case <-ticker.C:
						if channel.BufferedAmount() > 4*networkCheckPacketBytes {
							continue
						}
						binary.LittleEndian.PutUint32(packet, 0x31504e54)
						binary.LittleEndian.PutUint32(packet[4:], seq)
						binary.LittleEndian.PutUint64(packet[8:], uint64(time.Since(started).Microseconds()))
						mu.Lock()
						binary.LittleEndian.PutUint32(packet[16:], received)
						binary.LittleEndian.PutUint32(packet[20:], highest)
						binary.LittleEndian.PutUint64(packet[24:], stamp)
						binary.LittleEndian.PutUint32(packet[32:], uint32(min(60000., jitter)*1000))
						mu.Unlock()
						if channel.Send(packet) != nil {
							return
						}
						seq++
					}
				}
			}()
		})
	})
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	select {
	case <-gather:
		return pc.LocalDescription().SDP, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", errors.New("expired")
	case <-timer.C:
		return "", errors.New("gathering timed out")
	}
}

func (r *networkCheckRegistry) stopAll() {
	r.mu.Lock()
	var sessions []*networkCheckSession
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	for _, s := range sessions {
		r.remove(s)
	}
}
