// ssdp.go — SSDP (Simple Service Discovery Protocol) responder.
//
// SSDP is the discovery half of UPnP. It runs over multicast UDP on
// 239.255.255.250:1900. Two flows we participate in:
//
//   1. CLIENT M-SEARCH (active discovery): a TV sends a multicast
//      datagram with `MAN: "ssdp:discover"` and an ST (search target)
//      header. We unicast back HTTP/1.1 200 OK including a LOCATION
//      header pointing at our /device.xml.
//
//   2. SERVER NOTIFY (passive announcement): every ~30 minutes we
//      send a multicast NOTIFY with `NTS: ssdp:alive` for each of our
//      service types. On shutdown we send `NTS: ssdp:byebye` so TVs
//      don't keep us in their picker for hours.
//
// Service types we advertise (one alive per type, repeated):
//
//   upnp:rootdevice
//   urn:schemas-upnp-org:device:MediaServer:1
//   urn:schemas-upnp-org:service:ContentDirectory:1
//   urn:schemas-upnp-org:service:ConnectionManager:1
//   uuid:{device_uuid}
//
// Multicast networking caveat: this requires `network_mode: host` (or
// equivalent CNI multicast support). In default Docker bridge mode,
// the kernel drops multicast at the bridge and TVs never see us.
//
// LAN IP detection: we need an IP a TV can actually reach. We pull
// it from APTEVA_LAN_IP if set; otherwise scan interfaces for the
// first non-loopback private-range IPv4. If the host is multi-homed,
// pin via the lan_ip config.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	ssdpAddr     = "239.255.255.250:1900"
	ssdpHost     = "239.255.255.250"
	ssdpPort     = 1900
	notifyPeriod = 30 * time.Minute
	maxAge       = 1800 // seconds; matches notifyPeriod
)

// SSDPServer multicasts NOTIFY beacons and replies to M-SEARCH probes.
// Lifecycle is bound to the worker context — Stop is called by the
// framework on shutdown and sends byebye.
type SSDPServer struct {
	UUID         string
	FriendlyName func() string // late-bound; the friendly name can change at runtime
	HTTPPort     int           // for LOCATION URLs
	LANIP        string

	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	doneCh     chan struct{}
	conn       *net.UDPConn
	logFn      func(string, string)
	onMSearch  func(remote string, st string)
}

func newSSDPServer(uuid string, port int, lanIP string, friendly func() string, log func(string, string)) *SSDPServer {
	if log == nil {
		log = func(string, string) {}
	}
	return &SSDPServer{
		UUID:         uuid,
		FriendlyName: friendly,
		HTTPPort:     port,
		LANIP:        lanIP,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		logFn:        log,
	}
}

// allTargets is the set of (NT, USN) pairs we announce on. The TV's
// M-SEARCH ST is matched against these to decide which response(s)
// to send back.
func (s *SSDPServer) allTargets() [][2]string {
	uuid := "uuid:" + s.UUID
	root := "upnp:rootdevice"
	dev := "urn:schemas-upnp-org:device:MediaServer:1"
	cd := "urn:schemas-upnp-org:service:ContentDirectory:1"
	cm := "urn:schemas-upnp-org:service:ConnectionManager:1"
	return [][2]string{
		// {NT, USN}
		{root, uuid + "::" + root},
		{uuid, uuid},
		{dev, uuid + "::" + dev},
		{cd, uuid + "::" + cd},
		{cm, uuid + "::" + cm},
	}
}

// Run is the worker entrypoint. Blocks until ctx is cancelled.
func (s *SSDPServer) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("ssdp: already running")
	}
	addr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("ssdp: multicast listen: %w (host networking required)", err)
	}
	if err := conn.SetReadBuffer(64 << 10); err != nil {
		// non-fatal
		s.logFn("ssdp", "set-read-buffer: "+err.Error())
	}
	s.conn = conn
	s.running = true
	s.mu.Unlock()

	// Initial alive burst — TVs that were already listening get added
	// straight away rather than waiting up to notifyPeriod.
	s.broadcastAlive()

	// One reader goroutine, one ticker.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 2048)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					select {
					case <-s.stopCh:
						return
					default:
						continue
					}
				}
				return
			}
			s.handleDatagram(src, buf[:n])
		}
	}()

	t := time.NewTicker(notifyPeriod)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			<-readerDone
			close(s.doneCh)
			return nil
		case <-s.stopCh:
			s.shutdown()
			<-readerDone
			close(s.doneCh)
			return nil
		case <-t.C:
			s.broadcastAlive()
		}
	}
}

func (s *SSDPServer) shutdown() {
	s.broadcastByebye()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.running = false
}

// IsRunning lets the panel show a live "● broadcasting" indicator.
func (s *SSDPServer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// handleDatagram parses an inbound multicast/unicast HTTPU message
// and either replies (M-SEARCH) or ignores (other NOTIFYs).
func (s *SSDPServer) handleDatagram(src *net.UDPAddr, raw []byte) {
	text := string(raw)
	if !strings.HasPrefix(text, "M-SEARCH ") {
		return // we don't track other servers' announcements
	}
	headers := parseHeaders(text)
	st := headers["ST"]
	man := strings.Trim(headers["MAN"], `"`)
	if man != "ssdp:discover" {
		return
	}
	// Match the search target against our advertised types. ST="ssdp:all"
	// means "send one response per type".
	for _, target := range s.allTargets() {
		if st == "ssdp:all" || st == target[0] {
			s.respondMSearch(src, target[0], target[1])
		}
	}
	if s.onMSearch != nil {
		s.onMSearch(src.IP.String(), st)
	}
}

// respondMSearch unicasts a 200 OK back to the searcher. The
// LOCATION header is the URL the TV will then GET to fetch our
// device.xml.
func (s *SSDPServer) respondMSearch(dst *net.UDPAddr, nt, usn string) {
	location := fmt.Sprintf("http://%s:%d/device.xml", s.LANIP, s.HTTPPort)
	resp := strings.Join([]string{
		"HTTP/1.1 200 OK",
		fmt.Sprintf("CACHE-CONTROL: max-age=%d", maxAge),
		"DATE: " + time.Now().UTC().Format(time.RFC1123),
		"EXT:",
		"LOCATION: " + location,
		"SERVER: Apteva/1.0 UPnP/1.0 dlna/0.1",
		"ST: " + nt,
		"USN: " + usn,
		"BOOTID.UPNP.ORG: 1",
		"CONFIGID.UPNP.ORG: 1",
		"", "",
	}, "\r\n")

	conn, err := net.DialUDP("udp4", nil, dst)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(resp))
}

// broadcastAlive multicasts one NOTIFY/alive per advertised type. TVs
// are expected to refresh their server list on receipt — until the
// next alive (within max-age) the entry stays.
func (s *SSDPServer) broadcastAlive() {
	location := fmt.Sprintf("http://%s:%d/device.xml", s.LANIP, s.HTTPPort)
	for _, t := range s.allTargets() {
		nt, usn := t[0], t[1]
		msg := strings.Join([]string{
			"NOTIFY * HTTP/1.1",
			"HOST: " + ssdpAddr,
			fmt.Sprintf("CACHE-CONTROL: max-age=%d", maxAge),
			"LOCATION: " + location,
			"NT: " + nt,
			"NTS: ssdp:alive",
			"SERVER: Apteva/1.0 UPnP/1.0 dlna/0.1",
			"USN: " + usn,
			"BOOTID.UPNP.ORG: 1",
			"CONFIGID.UPNP.ORG: 1",
			"", "",
		}, "\r\n")
		s.sendMulticast(msg)
	}
}

// broadcastByebye is the courtesy farewell — without this, TVs keep
// the entry until max-age expires and users wonder why "Apteva
// (homeserver)" is still listed after they stopped the service.
func (s *SSDPServer) broadcastByebye() {
	for _, t := range s.allTargets() {
		nt, usn := t[0], t[1]
		msg := strings.Join([]string{
			"NOTIFY * HTTP/1.1",
			"HOST: " + ssdpAddr,
			"NT: " + nt,
			"NTS: ssdp:byebye",
			"USN: " + usn,
			"", "",
		}, "\r\n")
		s.sendMulticast(msg)
	}
}

// sendMulticast opens a fresh UDP socket per call so we don't have
// to worry about thread-safety on a shared writer. SSDP is low
// volume — ~5 packets per period — so the socket churn is fine.
func (s *SSDPServer) sendMulticast(msg string) {
	addr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		s.logFn("ssdp", "dial multicast: "+err.Error())
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(msg))
}

// parseHeaders is a tolerant HTTPU header parser. Real SSDP traffic
// is irregular: TVs send lower-cased keys, mixed line endings, extra
// blank lines. We canonicalise to upper-case keys and trim values.
func parseHeaders(raw string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(raw, "\n")
	for i, ln := range lines {
		ln = strings.TrimRight(ln, "\r")
		if i == 0 {
			continue // request-line, e.g. "M-SEARCH * HTTP/1.1"
		}
		if ln == "" {
			break
		}
		idx := strings.Index(ln, ":")
		if idx < 0 {
			continue
		}
		k := strings.ToUpper(strings.TrimSpace(ln[:idx]))
		v := strings.TrimSpace(ln[idx+1:])
		out[k] = v
	}
	return out
}

// detectLANIP picks a sensible LAN IP from interface addresses. Order
// of preference: env override, then the first non-loopback private
// IPv4, then any non-loopback IPv4. Returns "" if nothing is found.
func detectLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var firstAny string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			if firstAny == "" {
				firstAny = ip.String()
			}
			if ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return firstAny
}
