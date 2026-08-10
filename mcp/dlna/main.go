// Apteva DLNA app — UPnP MediaServer for the LAN, backed by the
// `storage` and `media` apps.
//
// Holds three small tables (published_folders allowlist, app_settings,
// and client_log)
// and runs three workers:
//  1. ssdp        — multicast discovery responder (UDP 1900)
//  2. client-prune — purges old client_log rows
//  3. (the SSDP server's internal NOTIFY ticker; not a separate worker)
//
// Browse SOAP calls use a short event-invalidated catalog built from
// storage.files_list and, when configured, cached media.media_get
// enrichment. There is no second durable media index in this app.
package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

const (
	defaultHTTPPort = 8200
	clientPruneInt  = 1 * time.Hour
)

// resolveHTTPPort returns the port the SDK actually bound this sidecar
// to. APTEVA_APP_PORT is the platform-injected override (a free port
// per install on shared hosts); without it the SDK falls back to the
// manifest's runtime.port. SSDP advertisements have to use the *real*
// port — TVs reach this app directly on the LAN, not through the
// platform proxy, so a stale 8200 leaves clients GET'ing nothing.
func resolveHTTPPort() int {
	if v := os.Getenv("APTEVA_APP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultHTTPPort
}

var (
	globalApp *App
	once      sync.Once
)

type App struct {
	httpPort  int
	mu        sync.RWMutex
	ctx       *sdk.AppCtx
	ssdp      *SSDPServer
	deviceID  string // uuid stripped of "uuid:" prefix
	lanIP     string
	projectID string
	updateID  atomic.Uint32
	updateMu  sync.Mutex

	cfgFriendlyName string

	catalogMu sync.Mutex
	catalog   catalogCache

	mediaMu           sync.Mutex
	mediaCache        map[int64]mediaCacheEntry
	mediaBackoffUntil time.Time
	mediaSem          chan struct{}

	healthMu sync.Mutex
	health   healthCache
}

// ─── lifecycle ──────────────────────────────────────────────────────

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("dlna: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("dlna: requires a db block")
	}
	a.ctx = ctx
	globalApp = a
	a.projectID = strings.TrimSpace(ctx.CurrentProject())
	if a.projectID == "" {
		return errors.New("dlna: project-scoped install required; a TV request has no project context, so global scope would be ambiguous")
	}
	a.mediaCache = make(map[int64]mediaCacheEntry)
	a.mediaSem = make(chan struct{}, 4)

	a.deviceID = a.resolveDeviceUUID()
	a.lanIP = a.resolveLANIP()
	a.cfgFriendlyName = a.resolveFriendlyName()
	a.httpPort = resolveHTTPPort()
	a.updateID.Store(a.loadUpdateID())

	a.ssdp = newSSDPServer(
		a.deviceID, a.httpPort, a.lanIP,
		func() string { return a.friendlyName() },
		func(scope, msg string) { ctx.Logger().Info(scope, "msg", msg) },
	)
	ctx.Logger().Info("dlna mounted",
		"project_id", a.projectID, "uuid", a.deviceID, "lan_ip", a.lanIP, "port", a.httpPort,
		"friendly", a.cfgFriendlyName)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "file.added", Handler: a.handleStorageChanged},
		{Event: "file.updated", Handler: a.handleStorageChanged},
		{Event: "file.deleted", Handler: a.handleStorageChanged},
	}
}

func (a *App) handleStorageChanged(ctx *sdk.AppCtx, event sdk.Event) error {
	if pid := strings.TrimSpace(event.ProjectID); pid != "" && pid != a.projectID {
		return nil
	}
	a.invalidateCatalog()
	a.bumpUpdateID()
	return nil
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name: "ssdp",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				if err := a.ssdp.Run(ctx); err != nil {
					app.Logger().Warn("ssdp run", "err", err.Error())
					return err
				}
				return nil
			},
		},
		{
			Name: "client-prune",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				t := time.NewTicker(clientPruneInt)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return nil
					case <-t.C:
						a.pruneClientLog()
					}
				}
			},
		},
	}
}

// ─── HTTP surface ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// UPnP wire — these are the URLs SSDP advertises to TVs on
		// the LAN. TVs can't carry an APTEVA_APP_TOKEN, so every
		// route here MUST set NoAuth. The auth boundary for DLNA is
		// the LAN itself; the operator is expected to keep this
		// install off any internet-routable interface.
		{Pattern: "/device.xml", Handler: a.handleDeviceXML, NoAuth: true},
		{Pattern: "/ContentDirectory.xml", Handler: a.handleContentDirectorySCPD, NoAuth: true},
		{Pattern: "/ConnectionManager.xml", Handler: a.handleConnectionManagerSCPD, NoAuth: true},
		{Pattern: "/ContentDirectory/control", Handler: a.handleControlContentDirectory, NoAuth: true},
		{Pattern: "/ConnectionManager/control", Handler: a.handleControlConnectionManager, NoAuth: true},
		{Pattern: "/ContentDirectory/event", Handler: stubEvent, NoAuth: true},
		{Pattern: "/ConnectionManager/event", Handler: stubEvent, NoAuth: true},
		// TVs need both forms. Exact /media (no trailing slash) is the
		// canonical URL we advertise in DIDL; /media/ subtree stays for
		// any legacy listings TVs may have cached. Pre-v0.1.17 the
		// app advertised /media/<id> and relied on http.ServeMux's
		// subtree-match — but the actual routing chain (SDK
		// withTokenAuth + the SDK's mux registration on older
		// app-sdk pins) was strict-suffix and returned 404 for
		// /media/21 with no trailing slash, which TVs interpret as
		// "server gone" and report as device-disconnected. Two
		// registrations + a query-string id make this robust against
		// every variant we've observed.
		{Pattern: "/media", Handler: a.handleMediaRedirect, NoAuth: true},
		{Pattern: "/media/", Handler: a.handleMediaRedirect, NoAuth: true},

		// Panel reads — proxied through the dashboard with the
		// install's APTEVA_APP_TOKEN, so they keep the default auth.
		{Pattern: "/published_folders", Handler: a.handlePublishedFolders},
		{Pattern: "/published_folders/", Handler: a.handlePublishedFoldersItem},
		{Pattern: "/clients", Handler: a.handleClientsRecent},
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/storage_folders", Handler: a.handleStorageFolders},
		{Pattern: "/settings", Handler: a.handleSettings},
		{Pattern: "/announce", Handler: a.handleAnnounce},
	}
}

// stubEvent implements the subscription handshake expected by TVs.
// The current UpdateID is available through SOAP and SSDP announces;
// we do not retain callback URLs or push NOTIFY payloads yet.
func stubEvent(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "SUBSCRIBE":
		sid := strings.TrimSpace(r.Header.Get("SID"))
		if sid == "" {
			if strings.TrimSpace(r.Header.Get("CALLBACK")) == "" || !strings.EqualFold(strings.TrimSpace(r.Header.Get("NT")), "upnp:event") {
				http.Error(w, "CALLBACK and NT required", http.StatusPreconditionFailed)
				return
			}
			sid = "uuid:" + uuid.NewString()
		}
		w.Header().Set("SID", sid)
		w.Header().Set("TIMEOUT", "Second-1800")
		w.WriteHeader(http.StatusOK)
	case "UNSUBSCRIBE":
		if strings.TrimSpace(r.Header.Get("SID")) == "" {
			http.Error(w, "SID required", http.StatusPreconditionFailed)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "SUBSCRIBE, UNSUBSCRIBE")
		http.Error(w, "SUBSCRIBE or UNSUBSCRIBE", http.StatusMethodNotAllowed)
	}
}

// handleDeviceXML returns the root device descriptor. Friendly name
// is read live so renames propagate without restart.
func (a *App) handleDeviceXML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	fmt.Fprintf(w, deviceXMLTemplate,
		xmlText(a.friendlyName()),
		"Apteva", "https://apteva.io",
		"DLNA Server", "Apteva DLNA",
		"1.0", a.deviceID,
	)
}

const deviceXMLTemplate = `<?xml version="1.0" encoding="utf-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
 <specVersion><major>1</major><minor>0</minor></specVersion>
 <device>
  <deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
  <friendlyName>%s</friendlyName>
  <manufacturer>%s</manufacturer>
  <manufacturerURL>%s</manufacturerURL>
  <modelDescription>%s</modelDescription>
  <modelName>%s</modelName>
  <modelNumber>%s</modelNumber>
  <UDN>uuid:%s</UDN>
  <serviceList>
   <service>
    <serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>
    <serviceId>urn:upnp-org:serviceId:ContentDirectory</serviceId>
    <SCPDURL>/ContentDirectory.xml</SCPDURL>
    <controlURL>/ContentDirectory/control</controlURL>
    <eventSubURL>/ContentDirectory/event</eventSubURL>
   </service>
   <service>
    <serviceType>urn:schemas-upnp-org:service:ConnectionManager:1</serviceType>
    <serviceId>urn:upnp-org:serviceId:ConnectionManager</serviceId>
    <SCPDURL>/ConnectionManager.xml</SCPDURL>
    <controlURL>/ConnectionManager/control</controlURL>
    <eventSubURL>/ConnectionManager/event</eventSubURL>
   </service>
  </serviceList>
 </device>
</root>
`

func (a *App) handleContentDirectorySCPD(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Write([]byte(scpdContentDirectory))
}

func (a *App) handleConnectionManagerSCPD(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Write([]byte(scpdConnectionManager))
}

// handleMediaRedirect: TVs hit this URL (advertised in DIDL <res>)
// with range/seek; we want bytes flowing Mac→LAN→TV without any
// detour through the public internet.
//
// Earlier versions 302'd to storage's signed URL — but that signed
// URL is built from publicBase() (e.g. https://tunnel.example.com),
// which is unreliable for LAN playback (offline tunnels, NAT
// hairpinning, slower than local). The tunnel was the live failure
// mode: 530 from Cloudflare, "device disconnected" on the TV.
//
// Instead, mint the same signed URL but rewrite the host to point
// at the platform's local gateway (APTEVA_GATEWAY_URL, set by the
// SDK at boot), fetch the bytes through dlna's own listener
// (which is on the LAN), and stream them back. Range / If-Range /
// If-None-Match all pass through so seeking works. The signed-URL
// query params (sig=, exp=) ride along untouched — the gateway's
// auth middleware carves out signed paths.
func (a *App) handleMediaRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET or HEAD", http.StatusMethodNotAllowed)
		return
	}

	// id can arrive two ways:
	//   1. v0.1.17+: as ?id=N in the query string (canonical, since
	//      the advertised URL is /media?id=N&n=name.ext — no path
	//      segments means no trailing-slash routing ambiguity).
	//   2. legacy: as the last path segment in /media/N or /media/N.ext
	//      (TVs may have cached an old listing).
	var idStr string
	if v := strings.TrimSpace(r.URL.Query().Get("id")); v != "" {
		idStr = v
	} else {
		idStr = strings.TrimPrefix(r.URL.Path, "/media/")
		idStr = strings.TrimSuffix(idStr, "/")
		idStr = strings.TrimSuffix(idStr, "."+strings.ToLower(extOf(idStr)))
	}
	fileID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || fileID <= 0 {
		http.Error(w, "bad file id", 400)
		return
	}
	file, err := a.storageGetFile(r.Context(), fileID)
	if err != nil {
		a.ctx.Logger().Warn("dlna media lookup failed", "file_id", fileID, "client_ip", clientIP(r), "err", err.Error())
		http.NotFound(w, r)
		return
	}
	if file == nil || !a.isFilePublished(*file) {
		// Do not reveal whether an unpublished storage ID exists.
		a.ctx.Logger().Warn("dlna media file unavailable", "file_id", fileID, "client_ip", clientIP(r))
		http.NotFound(w, r)
		return
	}
	ttl := a.configInt("signed_url_ttl_seconds", 60, 10, 3600)
	signed, err := a.storageGetURL(r.Context(), fileID, ttl)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	a.logClientFromRequest(r, "media:"+strconv.FormatInt(fileID, 10))

	// Reroute via local gateway. If APTEVA_GATEWAY_URL isn't set
	// (unlikely but possible for older platforms), fall back to the
	// original 302 — bytes go through whatever public path storage
	// configured. Better than failing outright.
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if gateway == "" {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, signed, http.StatusFound)
		return
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		http.Error(w, "bad signed url: "+err.Error(), 502)
		return
	}
	target := gateway + parsed.EscapedPath()
	if parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.Error(w, "build proxy request: "+err.Error(), 500)
		return
	}
	for _, h := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// Long timeout: TVs may keep a single connection open across
	// seeks during a multi-hour movie. The handler context cancels
	// when the TV disconnects.
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "storage fetch: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	// Forward storage's response headers (Content-Type,
	// Content-Length, Content-Range, ETag, Accept-Ranges, …) so the
	// TV can seek and resume. Strip hop-by-hop headers Go's stdlib
	// would otherwise complain about.
	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		case "connection", "transfer-encoding", "keep-alive",
			"proxy-authenticate", "proxy-authorization", "te",
			"trailer", "upgrade":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

var mediaHTTPClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   32,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 20 * time.Second,
}}

func extOf(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return ""
}

// ─── panel reads ────────────────────────────────────────────────────

func (a *App) handlePublishedFolders(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	pid := a.projectID
	switch r.Method {
	case http.MethodGet:
		out, err := listPublishedFolders(a.ctx.AppDB(), pid)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct{ Folder, Label string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		out, err := addPublishedFolder(a.ctx.AppDB(), pid, body.Folder, body.Label)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.invalidateCatalog()
		a.bumpUpdateID()
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handlePublishedFoldersItem(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/published_folders/"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad folder id", 400)
		return
	}
	res, err := a.ctx.AppDB().Exec(
		`DELETE FROM published_folders WHERE id = ? AND project_id = ?`,
		id, a.projectID,
	)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.NotFound(w, r)
		return
	}
	a.invalidateCatalog()
	a.bumpUpdateID()
	w.WriteHeader(204)
}

func (a *App) handleClientsRecent(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := listClientLog(a.ctx.AppDB(), a.projectID, limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.statusSnapshot()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// handleStorageFolders proxies the panel's folder-picker to storage via
// PlatformAPI.CallAppResult. The panel can't call /api/apps/storage/folders
// directly because storage's HTTP handler demands a ?project_id= the panel
// doesn't have when storage is installed at global scope; CallAppResult
// goes through the MCP gateway which carries the calling install's
// project context for it.
func (a *App) handleStorageFolders(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	parent := r.URL.Query().Get("parent")
	if parent == "" {
		parent = "/"
	}
	subs, err := a.storageListFolders(r.Context(), parent)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	names := make([]string, 0, len(subs))
	for _, s := range subs {
		names = append(names, s.Name)
	}
	writeJSON(w, map[string]any{"folders": names, "parent": parent})
}

func (a *App) requirePanelProject(w http.ResponseWriter, r *http.Request) bool {
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" && pid != a.projectID {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (a *App) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	if a.ssdp == nil || !a.ssdp.IsRunning() {
		http.Error(w, "SSDP is not running", http.StatusServiceUnavailable)
		return
	}
	a.ssdp.Announce()
	writeJSON(w, map[string]any{"announced": true})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !a.requirePanelProject(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, a.settingsSnapshot())
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updates := make(map[string]string, len(body))
	for key, raw := range body {
		var value string
		switch key {
		case "friendly_name":
			name, ok := raw.(string)
			if !ok {
				http.Error(w, "friendly_name must be a string", 400)
				return
			}
			name = strings.TrimSpace(name)
			if len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
				http.Error(w, "friendly_name must be at most 128 characters and one line", 400)
				return
			}
			value = name
		case "publish_root_by_default", "media_metadata":
			flag, ok := raw.(bool)
			if !ok {
				http.Error(w, key+" must be boolean", 400)
				return
			}
			value = strconv.FormatBool(flag)
		case "client_log_retention_hours":
			n, err := jsonInt(raw)
			if err != nil || n < 1 || n > 24*365 {
				http.Error(w, "client_log_retention_hours must be between 1 and 8760", 400)
				return
			}
			value = strconv.Itoa(n)
		case "signed_url_ttl_seconds":
			n, err := jsonInt(raw)
			if err != nil || n < 10 || n > 3600 {
				http.Error(w, "signed_url_ttl_seconds must be between 10 and 3600", 400)
				return
			}
			value = strconv.Itoa(n)
		case "catalog_cache_seconds":
			n, err := jsonInt(raw)
			if err != nil || n < 1 || n > 300 {
				http.Error(w, "catalog_cache_seconds must be between 1 and 300", 400)
				return
			}
			value = strconv.Itoa(n)
		default:
			http.Error(w, "unsupported setting: "+key, 400)
			return
		}
		updates[key] = value
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for key, value := range updates {
		if _, err := tx.Exec(
			`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			_ = tx.Rollback()
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.mu.Lock()
	a.cfgFriendlyName = a.configString("friendly_name", "")
	a.mu.Unlock()
	a.invalidateCatalog()
	a.clearMediaCache()
	a.bumpUpdateID()
	if a.ssdp != nil {
		a.ssdp.Announce()
	}
	writeJSON(w, a.settingsSnapshot())
}

func (a *App) settingsSnapshot() map[string]any {
	return map[string]any{
		"friendly_name":              a.configString("friendly_name", ""),
		"publish_root_by_default":    a.configFlag("publish_root_by_default", false),
		"media_metadata":             a.configFlag("media_metadata", true),
		"client_log_retention_hours": a.configInt("client_log_retention_hours", 24, 1, 24*365),
		"signed_url_ttl_seconds":     a.configInt("signed_url_ttl_seconds", 60, 10, 3600),
		"catalog_cache_seconds":      a.configInt("catalog_cache_seconds", 30, 1, 300),
	}
}

func jsonInt(v any) (int, error) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.Atoi(n.String())
		return i, err
	case float64:
		return int(n), nil
	case int:
		return n, nil
	default:
		return 0, errors.New("not an integer")
	}
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	obj := func(p map[string]any, req []string) map[string]any {
		s := map[string]any{"type": "object", "properties": p}
		if len(req) > 0 {
			s["required"] = req
		}
		return s
	}
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}

	return []sdk.Tool{
		{Name: "dlna_status",
			Description: "Status of the DLNA broadcaster: friendly_name, uuid, lan_ip, port, broadcasting flag, published-folder count, recent-client count.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolStatus},
		{Name: "dlna_set_friendly_name",
			Description: "Persistently rename the device. Empty name resets to 'Apteva ({hostname})'. An SSDP alive burst is sent immediately.",
			InputSchema: obj(map[string]any{"name": str}, []string{"name"}),
			Handler:     a.toolSetFriendlyName},
		{Name: "dlna_publish_folder",
			Description: "Publish a storage folder to the DLNA library. Args: folder (storage path, e.g. /movies/kids), label? (display override).",
			InputSchema: obj(map[string]any{"folder": str, "label": str}, []string{"folder"}),
			Handler:     a.toolPublishFolder},
		{Name: "dlna_unpublish_folder",
			Description: "Stop publishing a folder. Args: folder.",
			InputSchema: obj(map[string]any{"folder": str}, []string{"folder"}),
			Handler:     a.toolUnpublishFolder},
		{Name: "dlna_clients_recent",
			Description: "List clients that browsed in the last 24h. Args: limit (default 50).",
			InputSchema: obj(map[string]any{"limit": num}, nil),
			Handler:     a.toolClientsRecent},
		{Name: "dlna_announce",
			Description: "Force an immediate SSDP alive burst — broadcasts NOTIFY packets right now instead of waiting for the next periodic cycle. Useful when a TV just powered on and operators don't want to wait the full notifyPeriod. Returns {announced: true} on success.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolAnnounce},
	}
}

// toolAnnounce triggers an immediate SSDP alive burst. The actual
// broadcast runs on the SSDP server's main loop, so we don't block
// the MCP call on the multicast socket.
func (a *App) toolAnnounce(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	a.mu.Lock()
	srv := a.ssdp
	a.mu.Unlock()
	if srv == nil || !srv.IsRunning() {
		return map[string]any{
			"announced": false,
			"reason":    "ssdp server not running — check dlna_status for the current state",
		}, nil
	}
	srv.Announce()
	return map[string]any{"announced": true}, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return a.statusSnapshot()
}

func (a *App) toolSetFriendlyName(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		return nil, errors.New("name must be at most 128 characters and one line")
	}
	if err := a.setSetting("friendly_name", name); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cfgFriendlyName = name
	a.mu.Unlock()
	if a.ssdp != nil {
		a.ssdp.Announce()
	}
	return map[string]any{"friendly_name": a.friendlyName()}, nil
}

func (a *App) toolPublishFolder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	folder, _ := args["folder"].(string)
	label, _ := args["label"].(string)
	if folder == "" {
		return nil, errors.New("folder required")
	}
	out, err := addPublishedFolder(ctx.AppDB(), a.projectID, folder, label)
	if err == nil {
		a.invalidateCatalog()
		a.bumpUpdateID()
	}
	return out, err
}

func (a *App) toolUnpublishFolder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	folder, _ := args["folder"].(string)
	if folder == "" {
		return nil, errors.New("folder required")
	}
	folder, err := normalisePublishedPath(folder)
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(
		`DELETE FROM published_folders WHERE folder = ? AND project_id = ?`,
		folder, a.projectID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		a.invalidateCatalog()
		a.bumpUpdateID()
	}
	return map[string]any{"removed": n}, nil
}

func (a *App) toolClientsRecent(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := int(toInt64(args["limit"]))
	return listClientLog(ctx.AppDB(), a.projectID, limit)
}

// ─── status / config resolution ────────────────────────────────────

type Status struct {
	ProjectID        string `json:"project_id"`
	FriendlyName     string `json:"friendly_name"`
	UUID             string `json:"uuid"`
	LANIP            string `json:"lan_ip"`
	HTTPPort         int    `json:"http_port"`
	Broadcasting     bool   `json:"broadcasting"`
	PublishedFolders int    `json:"published_folders"`
	RecentClients    int    `json:"recent_clients"`
	StorageReachable bool   `json:"storage_reachable"`
	MediaReachable   bool   `json:"media_reachable"`
	CatalogTruncated bool   `json:"catalog_truncated"`
	UpdateID         uint32 `json:"update_id"`
}

func (a *App) statusSnapshot() (*Status, error) {
	pid := a.projectID
	pubs, err := countTable(a.ctx.AppDB(),
		`SELECT COUNT(*) FROM published_folders WHERE project_id = ?`, pid)
	if err != nil {
		return nil, err
	}
	clis, err := countTable(a.ctx.AppDB(),
		`SELECT COUNT(*) FROM client_log WHERE project_id = ?`, pid)
	if err != nil {
		return nil, err
	}
	storageOK, mediaOK := a.dependencyHealth()
	a.catalogMu.Lock()
	truncated := a.catalog.truncated
	a.catalogMu.Unlock()
	return &Status{
		ProjectID:        pid,
		FriendlyName:     a.friendlyName(),
		UUID:             a.deviceID,
		LANIP:            a.lanIP,
		HTTPPort:         a.httpPort,
		Broadcasting:     a.ssdp != nil && a.ssdp.IsRunning(),
		PublishedFolders: pubs,
		RecentClients:    clis,
		StorageReachable: storageOK,
		MediaReachable:   mediaOK,
		CatalogTruncated: truncated,
		UpdateID:         a.updateID.Load(),
	}, nil
}

func (a *App) friendlyName() string {
	a.mu.RLock()
	if a.cfgFriendlyName != "" {
		v := a.cfgFriendlyName
		a.mu.RUnlock()
		return v
	}
	a.mu.RUnlock()
	host, _ := os.Hostname()
	if host == "" {
		host = "homeserver"
	}
	return "Apteva (" + host + ")"
}

func (a *App) resolveFriendlyName() string {
	if v := a.configString("friendly_name", ""); v != "" {
		return v
	}
	return ""
}

func (a *App) resolveDeviceUUID() string {
	if v := strings.TrimPrefix(strings.TrimSpace(configString(a.ctx, "device_uuid", "")), "uuid:"); v != "" {
		if _, err := uuid.Parse(v); err == nil {
			return v
		}
		a.ctx.Logger().Warn("dlna: ignoring invalid configured device_uuid", "value", v)
	}
	if v, ok := a.getSetting("device_uuid"); ok {
		if _, err := uuid.Parse(v); err == nil {
			return v
		}
	}
	id := uuid.New().String()
	if err := a.setSetting("device_uuid", id); err != nil {
		a.ctx.Logger().Warn("dlna: persist generated device uuid", "err", err.Error())
	}
	return id
}

func (a *App) resolveLANIP() string {
	if v := configString(a.ctx, "lan_ip", ""); v != "" {
		return v
	}
	if v := os.Getenv("APTEVA_LAN_IP"); v != "" {
		return v
	}
	if ip := detectLANIP(); ip != "" {
		return ip
	}
	a.ctx.Logger().Warn("dlna: could not detect LAN IP — set lan_ip in config")
	return "127.0.0.1"
}

// ─── DB layer ───────────────────────────────────────────────────────

type PublishedFolder struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	Folder    string `json:"folder"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

// Display picks the human-readable name for the folder — label if
// set, else the folder's last path segment, else the literal path.
func (p PublishedFolder) Display() string {
	if p.Label != "" {
		return p.Label
	}
	if i := strings.LastIndex(p.Folder, "/"); i >= 0 && i+1 < len(p.Folder) {
		return p.Folder[i+1:]
	}
	if p.Folder == "/" || p.Folder == "" {
		return "Root"
	}
	return p.Folder
}

func addPublishedFolder(db *sql.DB, pid, folder, label string) (*PublishedFolder, error) {
	if strings.TrimSpace(pid) == "" {
		return nil, errors.New("project scope required")
	}
	var err error
	folder, err = normalisePublishedPath(folder)
	if err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if len(label) > 128 || strings.ContainsAny(label, "\r\n\x00") {
		return nil, errors.New("label must be at most 128 characters and one line")
	}
	_, err = db.Exec(
		`INSERT INTO published_folders (project_id, folder, label) VALUES (?,?,?)
		 ON CONFLICT(project_id, folder) DO UPDATE SET label = excluded.label`,
		pid, folder, label)
	if err != nil {
		return nil, err
	}
	var p PublishedFolder
	err = db.QueryRow(
		`SELECT id, project_id, folder, label, created_at
		   FROM published_folders WHERE project_id = ? AND folder = ?`, pid, folder,
	).Scan(&p.ID, &p.ProjectID, &p.Folder, &p.Label, &p.CreatedAt)
	return &p, err
}

func normalisePublishedPath(folder string) (string, error) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return "", errors.New("folder required")
	}
	if strings.ContainsRune(folder, '\x00') {
		return "", errors.New("folder contains NUL")
	}
	if !strings.HasPrefix(folder, "/") {
		folder = "/" + folder
	}
	for _, segment := range strings.Split(folder, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("folder must not contain . or .. segments")
		}
	}
	folder = path.Clean(folder)
	if folder == "." {
		folder = "/"
	}
	return folder, nil
}

func getPublishedFolder(db *sql.DB, pid string, id int64) (*PublishedFolder, error) {
	var p PublishedFolder
	err := db.QueryRow(
		`SELECT id, project_id, folder, label, created_at
		   FROM published_folders WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&p.ID, &p.ProjectID, &p.Folder, &p.Label, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func listPublishedFolders(db *sql.DB, pid string) ([]PublishedFolder, error) {
	rows, err := db.Query(
		`SELECT id, project_id, folder, label, created_at
		   FROM published_folders WHERE project_id = ?
		   ORDER BY sort_order, id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublishedFolder{}
	for rows.Next() {
		var p PublishedFolder
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Folder, &p.Label, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *App) publishedFoldersAsContainers() ([]didlContainer, error) {
	pubs, err := listPublishedFolders(a.ctx.AppDB(), a.projectID)
	if err != nil {
		return nil, err
	}
	out := make([]didlContainer, 0, len(pubs))
	for _, p := range pubs {
		out = append(out, didlContainer{
			ID:       encodeFolderID(p.ID, ""),
			ParentID: "0/folders",
			Title:    p.Display(),
			Class:    "object.container.storageFolder",
			Count:    -1,
		})
	}
	return out, nil
}

func (a *App) publishedFolderRow(id int64) (*PublishedFolder, error) {
	return getPublishedFolder(a.ctx.AppDB(), a.projectID, id)
}

func (a *App) publishedFolderPath(id int64) (string, error) {
	p, err := a.publishedFolderRow(id)
	if err != nil {
		return "", err
	}
	return p.Folder, nil
}

// ─── client log ─────────────────────────────────────────────────────

type ClientLogEntry struct {
	IP           string `json:"ip"`
	UserAgent    string `json:"user_agent"`
	LastObjectID string `json:"last_object_id"`
	LastActionAt string `json:"last_action_at"`
	BrowseCount  int    `json:"browse_count"`
}

func (a *App) logClient(r *http.Request) {
	a.logClientFromRequest(r, "")
}

func (a *App) logClientFromRequest(r *http.Request, objectID string) {
	ip := clientIP(r)
	ua := r.Header.Get("User-Agent")
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = a.ctx.AppDB().Exec(
		`INSERT INTO client_log (project_id, ip, user_agent, last_object_id, last_action_at, browse_count)
		 VALUES (?,?,?,?,?,1)
		 ON CONFLICT(project_id, ip, user_agent) DO UPDATE SET
		   last_object_id = excluded.last_object_id,
		   last_action_at = excluded.last_action_at,
		   browse_count   = client_log.browse_count + 1`,
		a.projectID, ip, ua, objectID, now)
}

func (a *App) pruneClientLog() {
	hours := a.configInt("client_log_retention_hours", 24, 1, 24*365)
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
	_, _ = a.ctx.AppDB().Exec(`DELETE FROM client_log WHERE project_id = ? AND last_action_at < ?`, a.projectID, cutoff)
}

func listClientLog(db *sql.DB, pid string, limit int) ([]ClientLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT ip, user_agent, last_object_id, last_action_at, browse_count
		   FROM client_log WHERE project_id = ?
		   ORDER BY last_action_at DESC LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientLogEntry{}
	for rows.Next() {
		var e ClientLogEntry
		if err := rows.Scan(&e.IP, &e.UserAgent, &e.LastObjectID, &e.LastActionAt, &e.BrowseCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		if i := strings.Index(x, ","); i > 0 {
			return strings.TrimSpace(x[:i])
		}
		return strings.TrimSpace(x)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ─── storage / media call helpers ──────────────────────────────────
//
// These wrappers use PlatformAPI().CallAppResult and preserve errors
// so the SOAP layer can return a standards-shaped UPnP fault.

type storageFile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Folder      string `json:"folder"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
}

type catalogCache struct {
	files     []storageFile
	loadedAt  time.Time
	truncated bool
}

type mediaCacheEntry struct {
	meta      *mediaMeta
	expiresAt time.Time
}

type healthCache struct {
	storage   bool
	media     bool
	checkedAt time.Time
}

type storageSubfolder struct {
	Name  string `json:"name"`
	Count int    `json:"file_count"`
}

// Storage's MCP tools return enveloped responses, not bare arrays:
//
//   files_list_folders → {folders: ["a","b"], count, parent}   (names only)
//   files_list         → {files: [{id, name, …}, …], count, …}
//   files_get          → {found: bool, file: {id, name, …}}    (current)
//                      or {id, name, …}                         (legacy)
//
// All cross-app calls below go through PlatformAPI.CallAppResult
// (added in app-sdk v0.1.8) which strips the JSON-RPC envelope and
// decodes the tool's inner JSON directly into the destination
// struct. No more "cannot unmarshal object into Go value of type
// []main.X" crashes the way earlier dlna versions hit.

func (a *App) storageListFolders(ctx context.Context, parent string) ([]storageSubfolder, error) {
	var env struct {
		Folders []string `json:"folders"`
	}
	if err := a.callApp("storage", "files_list_folders", map[string]any{
		"parent": parent,
	}, &env); err != nil {
		return nil, err
	}
	out := make([]storageSubfolder, 0, len(env.Folders))
	for _, name := range env.Folders {
		// Storage doesn't return per-folder file counts (would need
		// a separate query). Count=-1 makes DIDL omit childCount;
		// advertising zero would make strict TVs treat it as empty.
		out = append(out, storageSubfolder{Name: name, Count: -1})
	}
	return out, nil
}

func (a *App) storageListFiles(ctx context.Context, folder string, recursive bool) ([]storageFile, error) {
	var env struct {
		Files []storageFile `json:"files"`
	}
	if err := a.callApp("storage", "files_list", map[string]any{
		"folder":    folder,
		"recursive": recursive,
		"limit":     500,
	}, &env); err != nil {
		return nil, err
	}
	return env.Files, nil
}

func (a *App) storageGetFile(ctx context.Context, id int64) (*storageFile, error) {
	var payload json.RawMessage
	if err := a.callApp("storage", "files_get", map[string]any{"id": id}, &payload); err != nil {
		return nil, err
	}
	var wrapped struct {
		Found *bool        `json:"found"`
		File  *storageFile `json:"file"`
	}
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		return nil, fmt.Errorf("decode storage files_get response: %w", err)
	}
	if wrapped.Found != nil || wrapped.File != nil {
		if wrapped.Found != nil && !*wrapped.Found {
			return nil, nil
		}
		if wrapped.File == nil || wrapped.File.ID <= 0 {
			return nil, errors.New("storage files_get returned found without a valid file")
		}
		return wrapped.File, nil
	}

	// Storage versions before v0.10 returned the file as a bare object.
	// Keep accepting that shape so a DLNA update does not force a lockstep
	// storage upgrade on existing installations.
	var legacy storageFile
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return nil, fmt.Errorf("decode legacy storage files_get response: %w", err)
	}
	if legacy.ID <= 0 {
		return nil, errors.New("storage files_get returned an unrecognized response")
	}
	return &legacy, nil
}

// storageGetURL mints the short-lived signed path used by the DLNA
// LAN proxy. Storage handles ranges and ETags, which the proxy
// preserves for seeking.
// callApp wraps PlatformAPI.CallAppResult and injects _project_id so
// the target app's MCP gateway routes to the install that actually
// holds this project's data.
//
// Why this is needed for dlna in particular: dlna is bound to a LAN
// port and serves requests that arrive without any MCP agent context
// (TVs discovering it via SSDP, then dialing the advertised URLs).
// The SDK's project-scoped client wrapper only injects _project_id
// when a project is active in the calling ctx; for dlna's HTTP
// handlers there is no such ctx, so every CallAppResult arrived at
// storage/media without project info and got routed to whichever
// install the gateway picked by default — often the wrong one.
//
// Pre-v0.1.18 each call site was raw CallAppResult, every Browse +
// stream attempt routed to the wrong storage install and returned
// "file not found" → 502 from dlna → TVs reported "device
// disconnected". Same anti-pattern flagged in the workspace's
// [[feedback_project_id_global_calls]] memory for the bit-3 apps.
//
// Pulls APTEVA_PROJECT_ID from env (set by apteva-server at spawn
// time per project-scoped install). On global installs (apteva-server
// doesn't pin a project) the env is empty and we fall back to the
// gateway's default routing — those installs have a single sibling
// of each dep anyway, so the routing is unambiguous.
func (a *App) callApp(target, tool string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	if _, set := args["_project_id"]; !set {
		args["_project_id"] = a.projectID
	}
	return a.ctx.PlatformAPI().CallAppResult(target, tool, args, out)
}

func (a *App) callAppTimeout(ctx context.Context, timeout time.Duration, target, tool string, args map[string]any, out any) error {
	done := make(chan error, 1)
	go func() { done <- a.callApp(target, tool, args, out) }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return fmt.Errorf("%s.%s timed out after %s", target, tool, timeout)
	}
}

func (a *App) storageGetURL(ctx context.Context, id int64, ttlSec int) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := a.callApp("storage", "files_get_url", map[string]any{
		"id":          id,
		"ttl_seconds": ttlSec,
	}, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("storage returned empty url")
	}
	return out.URL, nil
}

func (a *App) catalogFiles(ctx context.Context) ([]storageFile, bool, error) {
	ttl := time.Duration(a.configInt("catalog_cache_seconds", 30, 1, 300)) * time.Second
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	if !a.catalog.loadedAt.IsZero() && time.Since(a.catalog.loadedAt) < ttl {
		return append([]storageFile(nil), a.catalog.files...), a.catalog.truncated, nil
	}
	roots := a.publishedFolderPaths()
	if a.configFlag("publish_root_by_default", false) {
		roots = []string{"/"}
	}
	roots = minimalPublishedRoots(roots)
	byID := make(map[int64]storageFile)
	truncated := false
	for _, root := range roots {
		files, rootTruncated, err := a.walkPublishedRoot(ctx, root)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || rootTruncated
		for _, f := range files {
			if f.ID > 0 && folderWithin(root, f.Folder) {
				byID[f.ID] = f
			}
		}
	}
	files := make([]storageFile, 0, len(byID))
	for _, f := range byID {
		files = append(files, f)
	}
	a.catalog = catalogCache{files: files, loadedAt: time.Now(), truncated: truncated}
	return append([]storageFile(nil), files...), truncated, nil
}

// walkPublishedRoot enumerates one physical folder at a time. Storage's
// existing files_list contract caps a response at 500 entries and does not
// expose an offset, so a single recursive call silently hid everything after
// item 500. Walking the folder tree keeps the cap local to an individual
// directory and lets ordinary large libraries remain complete without any
// change to the storage app.
func (a *App) walkPublishedRoot(ctx context.Context, root string) ([]storageFile, bool, error) {
	queue := []string{root}
	visited := map[string]bool{}
	files := []storageFile{}
	truncated := false
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		folder := queue[0]
		queue = queue[1:]
		if visited[folder] {
			continue
		}
		visited[folder] = true
		page, err := a.storageListFiles(ctx, folder, false)
		if err != nil {
			return nil, false, err
		}
		if len(page) >= 500 {
			truncated = true
		}
		for _, f := range page {
			if folderWithin(root, f.Folder) {
				files = append(files, f)
			}
		}
		subs, err := a.storageListFolders(ctx, folder)
		if err != nil {
			return nil, false, err
		}
		for _, sub := range subs {
			rel, err := secureRelativePath(sub.Name)
			if err != nil {
				continue
			}
			child := path.Join(folder, rel)
			if folderWithin(root, child) && !visited[child] {
				queue = append(queue, child)
			}
		}
	}
	return files, truncated, nil
}

func minimalPublishedRoots(roots []string) []string {
	clean := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		root, err := normalisePublishedPath(root)
		if err == nil && !seen[root] {
			seen[root] = true
			clean = append(clean, root)
		}
	}
	sort.Slice(clean, func(i, j int) bool { return len(clean[i]) < len(clean[j]) })
	out := make([]string, 0, len(clean))
	for _, candidate := range clean {
		covered := false
		for _, root := range out {
			if folderWithin(root, candidate) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, candidate)
		}
	}
	return out
}

func (a *App) invalidateCatalog() {
	a.catalogMu.Lock()
	a.catalog = catalogCache{}
	a.catalogMu.Unlock()
}

func (a *App) searchStorage(ctx context.Context, contentTypePrefix, query, folder string, start, count int, sortCriteria string) ([]didlItem, int, error) {
	files, _, err := a.catalogFiles(ctx)
	if err != nil {
		return nil, 0, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	filtered := make([]storageFile, 0, len(files))
	for _, f := range files {
		if contentTypePrefix != "" && !strings.HasPrefix(strings.ToLower(f.ContentType), contentTypePrefix) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(f.Name), needle) {
			continue
		}
		if folder != "" && !folderWithin(folder, f.Folder) {
			continue
		}
		filtered = append(filtered, f)
	}
	sortCatalogFiles(filtered, sortCriteria)
	total := len(filtered)
	filtered = paginateFiles(filtered, start, count)
	parent := "0/recent"
	if contentTypePrefix != "" {
		parent = "0/" + strings.TrimSuffix(contentTypePrefix, "/")
		if parent == "0/image" {
			parent = "0/photos"
		}
	}
	items := a.filesToDIDL(ctx, filtered, parent)
	return items, total, nil
}

func (a *App) searchByContentTypePrefix(ctx context.Context, prefix string, start, count int, sortCriteria string) ([]didlItem, int, error) {
	return a.searchStorage(ctx, prefix, "", "", start, count, sortCriteria)
}

func (a *App) recentItems(ctx context.Context, start, count int) ([]didlItem, int, error) {
	return a.searchStorage(ctx, "", "", "", start, count, "-dc:date")
}

func sortCatalogFiles(files []storageFile, criteria string) {
	criteria = strings.TrimSpace(criteria)
	desc := strings.HasPrefix(criteria, "-")
	field := strings.TrimLeft(criteria, "+-")
	if field == "" {
		field = "dc:title"
	}
	sort.SliceStable(files, func(i, j int) bool {
		cmp := 0
		switch field {
		case "dc:date":
			cmp = strings.Compare(files[i].CreatedAt, files[j].CreatedAt)
		default:
			li, lj := strings.ToLower(files[i].Name), strings.ToLower(files[j].Name)
			cmp = strings.Compare(li, lj)
		}
		if cmp == 0 {
			switch {
			case files[i].ID < files[j].ID:
				cmp = -1
			case files[i].ID > files[j].ID:
				cmp = 1
			}
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func paginateFiles(files []storageFile, start, count int) []storageFile {
	if start < 0 || start >= len(files) {
		return nil
	}
	end := start + count
	if end > len(files) {
		end = len(files)
	}
	return files[start:end]
}

func (a *App) publishedFolderPaths() []string {
	pubs, err := listPublishedFolders(a.ctx.AppDB(), a.projectID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(pubs))
	for _, p := range pubs {
		out = append(out, p.Folder)
	}
	return out
}

func folderWithin(root, folder string) bool {
	root, err := normalisePublishedPath(root)
	if err != nil {
		return false
	}
	folder, err = normalisePublishedPath(folder)
	if err != nil {
		return false
	}
	return root == "/" || folder == root || strings.HasPrefix(folder, root+"/")
}

func (a *App) isFilePublished(file storageFile) bool {
	if file.ID <= 0 {
		return false
	}
	if a.configFlag("publish_root_by_default", false) {
		return true
	}
	for _, root := range a.publishedFolderPaths() {
		if folderWithin(root, file.Folder) {
			return true
		}
	}
	return false
}

// fileToDIDL builds the base DIDL item. Optional media enrichment is
// applied in a bounded, cached batch by filesToDIDL.
func (a *App) fileToDIDL(ctx context.Context, f storageFile, parent string) didlItem {
	class := classFor(f.ContentType)
	// Use ?id=N rather than /media/N to dodge the SDK's strict-suffix
	// route matching that returned 404 for /media/N (no trailing slash)
	// pre-v0.1.17. The &n=<filename> tail is purely cosmetic — some TVs
	// inspect the URL to pick a display name when the DIDL <dc:title>
	// is hidden by the UI; including the original name keeps that
	// working without changing the route.
	mediaURL := fmt.Sprintf("http://%s:%d/media?id=%d&n=%s",
		a.lanIP, a.httpPort, f.ID, url.QueryEscape(f.Name))
	it := didlItem{
		ID:          fmt.Sprintf("i:%d", f.ID),
		ParentID:    parent,
		Title:       f.Name,
		Class:       class,
		Size:        f.SizeBytes,
		ContentType: f.ContentType,
		URL:         mediaURL,
	}
	return it
}

func (a *App) filesToDIDL(ctx context.Context, files []storageFile, parent string) []didlItem {
	items := make([]didlItem, len(files))
	for i, f := range files {
		items[i] = a.fileToDIDL(ctx, f, parent)
	}
	if !a.configFlag("media_metadata", true) || len(files) == 0 {
		return items
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := 4
	if len(files) < workers {
		workers = len(files)
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if meta := a.mediaProbe(ctx, files[i].ID); meta != nil {
					if meta.DurationMs > 0 {
						items[i].Duration = formatDuration(int((meta.DurationMs + 500) / 1000))
					}
					if meta.Width > 0 && meta.Height > 0 {
						items[i].Resolution = fmt.Sprintf("%dx%d", meta.Width, meta.Height)
					}
				}
			}
		}()
	}
	for i := range files {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return items
		}
	}
	close(jobs)
	wg.Wait()
	return items
}

func classFor(ct string) string {
	switch {
	case strings.HasPrefix(ct, "audio/"):
		return "object.item.audioItem.musicTrack"
	case strings.HasPrefix(ct, "video/"):
		return "object.item.videoItem"
	case strings.HasPrefix(ct, "image/"):
		return "object.item.imageItem.photo"
	default:
		return "object.item"
	}
}

// ─── media app (optional) ───────────────────────────────────────────

type mediaMeta struct {
	DurationMs int64 `json:"duration_ms"`
	Width      int   `json:"width"`
	Height     int   `json:"height"`
}

// mediaProbe is best-effort. If `media` isn't installed, isn't
// reachable, or doesn't know about this file, we silently return nil
// and leave the DIDL fields blank — clients tolerate that.
func (a *App) mediaProbe(ctx context.Context, fileID int64) *mediaMeta {
	now := time.Now()
	a.mediaMu.Lock()
	if now.Before(a.mediaBackoffUntil) {
		a.mediaMu.Unlock()
		return nil
	}
	if cached, ok := a.mediaCache[fileID]; ok && now.Before(cached.expiresAt) {
		a.mediaMu.Unlock()
		return cached.meta
	}
	a.mediaMu.Unlock()
	select {
	case a.mediaSem <- struct{}{}:
	case <-ctx.Done():
		return nil
	}
	var env struct {
		Found bool       `json:"found"`
		Media *mediaMeta `json:"media"`
	}
	done := make(chan error, 1)
	go func() {
		defer func() { <-a.mediaSem }()
		done <- a.callApp("media", "media_get", map[string]any{
			"file_id": strconv.FormatInt(fileID, 10),
		}, &env)
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-time.After(3 * time.Second):
		err = errors.New("media.media_get timed out after 3s")
	}
	a.mediaMu.Lock()
	defer a.mediaMu.Unlock()
	if err != nil {
		a.mediaBackoffUntil = time.Now().Add(30 * time.Second)
		return nil
	}
	ttl := 10 * time.Minute
	if !env.Found || env.Media == nil {
		ttl = time.Minute
	}
	a.mediaCache[fileID] = mediaCacheEntry{meta: env.Media, expiresAt: time.Now().Add(ttl)}
	return env.Media
}

func (a *App) clearMediaCache() {
	a.mediaMu.Lock()
	a.mediaCache = make(map[int64]mediaCacheEntry)
	a.mediaBackoffUntil = time.Time{}
	a.mediaMu.Unlock()
}

func (a *App) dependencyHealth() (bool, bool) {
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	if time.Since(a.health.checkedAt) < 30*time.Second {
		return a.health.storage, a.health.media
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var folders struct {
		Folders []string `json:"folders"`
	}
	storageOK := a.callAppTimeout(ctx, 5*time.Second, "storage", "files_list_folders", map[string]any{"parent": "/"}, &folders) == nil
	mediaOK := false
	if a.configFlag("media_metadata", true) {
		var status map[string]any
		mediaOK = a.callAppTimeout(ctx, 5*time.Second, "media", "media_index_status", map[string]any{}, &status) == nil
	}
	a.health = healthCache{storage: storageOK, media: mediaOK, checkedAt: time.Now()}
	return storageOK, mediaOK
}

// ─── helpers ────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok && v != "" {
		return v
	}
	return def
}

func (a *App) getSetting(key string) (string, bool) {
	if a == nil || a.ctx == nil || a.ctx.AppDB() == nil {
		return "", false
	}
	var value string
	err := a.ctx.AppDB().QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		a.ctx.Logger().Warn("dlna: read setting", "key", key, "err", err.Error())
		return "", false
	}
	return value, true
}

func (a *App) setSetting(key, value string) error {
	if a == nil || a.ctx == nil || a.ctx.AppDB() == nil {
		return errors.New("dlna: settings database unavailable")
	}
	_, err := a.ctx.AppDB().Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (a *App) configString(key, def string) string {
	if value, ok := a.getSetting(key); ok {
		return value
	}
	return configString(a.ctx, key, def)
}

func (a *App) configInt(key string, def, min, max int) int {
	s := a.configString(key, "")
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return def
	}
	return n
}

func (a *App) configFlag(key string, def bool) bool {
	s := strings.ToLower(strings.TrimSpace(a.configString(key, "")))
	switch s {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

func (a *App) loadUpdateID() uint32 {
	if value, ok := a.getSetting("system_update_id"); ok {
		if n, err := strconv.ParseUint(value, 10, 32); err == nil && n > 0 {
			return uint32(n)
		}
	}
	_ = a.setSetting("system_update_id", "1")
	return 1
}

func (a *App) bumpUpdateID() uint32 {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	n := a.updateID.Add(1)
	if n == 0 {
		a.updateID.Store(1)
		n = 1
	}
	if err := a.setSetting("system_update_id", strconv.FormatUint(uint64(n), 10)); err != nil && a.ctx != nil {
		a.ctx.Logger().Warn("dlna: persist system update id", "err", err.Error())
	}
	return n
}

func countTable(db *sql.DB, q string, args ...any) (int, error) {
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// urlEscape is here to keep the linter quiet about an unused import
// when we tweak the redirect handler. Cheap to keep.
var _ = url.PathEscape

// ─── SCPDs (service description XML) ───────────────────────────────
//
// SCPDs describe each service's actions + state variables to a
// control point. Most TVs barely look at these; we ship the minimum
// that satisfies UPnP validators.

const scpdContentDirectory = `<?xml version="1.0" encoding="utf-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
 <specVersion><major>1</major><minor>0</minor></specVersion>
 <actionList>
  <action>
   <name>Browse</name>
   <argumentList>
    <argument><name>ObjectID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_ObjectID</relatedStateVariable></argument>
    <argument><name>BrowseFlag</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_BrowseFlag</relatedStateVariable></argument>
    <argument><name>Filter</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Filter</relatedStateVariable></argument>
    <argument><name>StartingIndex</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Index</relatedStateVariable></argument>
    <argument><name>RequestedCount</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
    <argument><name>SortCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SortCriteria</relatedStateVariable></argument>
    <argument><name>Result</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Result</relatedStateVariable></argument>
    <argument><name>NumberReturned</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
    <argument><name>TotalMatches</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
    <argument><name>UpdateID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_UpdateID</relatedStateVariable></argument>
   </argumentList>
  </action>
  <action>
   <name>Search</name>
   <argumentList>
    <argument><name>ContainerID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_ObjectID</relatedStateVariable></argument>
    <argument><name>SearchCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SearchCriteria</relatedStateVariable></argument>
    <argument><name>Filter</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Filter</relatedStateVariable></argument>
    <argument><name>StartingIndex</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Index</relatedStateVariable></argument>
    <argument><name>RequestedCount</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
    <argument><name>SortCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SortCriteria</relatedStateVariable></argument>
    <argument><name>Result</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Result</relatedStateVariable></argument>
    <argument><name>NumberReturned</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
    <argument><name>TotalMatches</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
    <argument><name>UpdateID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_UpdateID</relatedStateVariable></argument>
   </argumentList>
  </action>
  <action><name>GetSearchCapabilities</name></action>
  <action><name>GetSortCapabilities</name></action>
  <action><name>GetSystemUpdateID</name></action>
 </actionList>
 <serviceStateTable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_ObjectID</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Result</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_BrowseFlag</name><dataType>string</dataType><allowedValueList><allowedValue>BrowseMetadata</allowedValue><allowedValue>BrowseDirectChildren</allowedValue></allowedValueList></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Filter</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_SearchCriteria</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_SortCriteria</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Index</name><dataType>ui4</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Count</name><dataType>ui4</dataType></stateVariable>
  <stateVariable sendEvents="yes"><name>A_ARG_TYPE_UpdateID</name><dataType>ui4</dataType></stateVariable>
 </serviceStateTable>
</scpd>`

const scpdConnectionManager = `<?xml version="1.0" encoding="utf-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
 <specVersion><major>1</major><minor>0</minor></specVersion>
 <actionList>
  <action><name>GetProtocolInfo</name><argumentList>
   <argument><name>Source</name><direction>out</direction><relatedStateVariable>SourceProtocolInfo</relatedStateVariable></argument>
   <argument><name>Sink</name><direction>out</direction><relatedStateVariable>SinkProtocolInfo</relatedStateVariable></argument>
  </argumentList></action>
  <action><name>GetCurrentConnectionIDs</name><argumentList>
   <argument><name>ConnectionIDs</name><direction>out</direction><relatedStateVariable>CurrentConnectionIDs</relatedStateVariable></argument>
  </argumentList></action>
  <action><name>GetCurrentConnectionInfo</name><argumentList>
   <argument><name>ConnectionID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_ConnectionID</relatedStateVariable></argument>
   <argument><name>RcsID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_RcsID</relatedStateVariable></argument>
   <argument><name>AVTransportID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_AVTransportID</relatedStateVariable></argument>
   <argument><name>ProtocolInfo</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_ProtocolInfo</relatedStateVariable></argument>
   <argument><name>PeerConnectionManager</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_ConnectionManager</relatedStateVariable></argument>
   <argument><name>PeerConnectionID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_ConnectionID</relatedStateVariable></argument>
   <argument><name>Direction</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Direction</relatedStateVariable></argument>
   <argument><name>Status</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_ConnectionStatus</relatedStateVariable></argument>
  </argumentList></action>
 </actionList>
 <serviceStateTable>
  <stateVariable sendEvents="yes"><name>SourceProtocolInfo</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="yes"><name>SinkProtocolInfo</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="yes"><name>CurrentConnectionIDs</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_ConnectionStatus</name><dataType>string</dataType><allowedValueList><allowedValue>OK</allowedValue><allowedValue>ContentFormatMismatch</allowedValue><allowedValue>InsufficientBandwidth</allowedValue><allowedValue>UnreliableChannel</allowedValue><allowedValue>Unknown</allowedValue></allowedValueList></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_ConnectionManager</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Direction</name><dataType>string</dataType><allowedValueList><allowedValue>Input</allowedValue><allowedValue>Output</allowedValue></allowedValueList></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_ProtocolInfo</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_ConnectionID</name><dataType>i4</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_AVTransportID</name><dataType>i4</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_RcsID</name><dataType>i4</dataType></stateVariable>
 </serviceStateTable>
</scpd>`

// ─── main ───────────────────────────────────────────────────────────

func main() {
	once.Do(func() {
		globalApp = &App{}
	})
	sdk.Run(globalApp)
}
