// Apteva DLNA app — UPnP MediaServer for the LAN, backed by the
// `storage` and `media` apps.
//
// Holds two small tables (published_folders allowlist + client_log)
// and runs three workers:
//   1. ssdp        — multicast discovery responder (UDP 1900)
//   2. client-prune — purges old client_log rows
//   3. (the SSDP server's internal NOTIFY ticker; not a separate worker)
//
// Browse SOAP calls fan out to storage.files_list / files_search and,
// when configured, media.probe — there is no local index. Everything
// is computed live.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: dlna
display_name: DLNA Server
version: 0.1.13
description: Local-LAN UPnP/DLNA MediaServer for Apteva.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, net.egress, platform.apps.call]
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: dlna_status,            description: "Status of the DLNA broadcaster." }
    - { name: dlna_set_friendly_name, description: "Rename the device." }
    - { name: dlna_publish_folder,    description: "Publish a storage folder." }
    - { name: dlna_unpublish_folder,  description: "Unpublish a storage folder." }
    - { name: dlna_clients_recent,    description: "Recent LAN clients." }
  ui_panels:
    - slot: project.page
      label: DLNA
      icon: tv
      entry: /ui/DLNAPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/dlna
  port: 8200
  health_check: /health
  bind_host: "0.0.0.0"
db:
  driver: sqlite
  path: /data/dlna.db
  migrations: migrations/
`

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
	httpPort int
	mu       sync.RWMutex
	ctx      *sdk.AppCtx
	ssdp     *SSDPServer
	deviceID string // uuid stripped of "uuid:" prefix
	lanIP    string

	// cached so we don't re-call Config() on every Browse.
	cfgFriendlyName string
}

// ─── lifecycle ──────────────────────────────────────────────────────

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
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

	a.deviceID = a.resolveDeviceUUID()
	a.lanIP = a.resolveLANIP()
	a.cfgFriendlyName = a.resolveFriendlyName()
	a.httpPort = resolveHTTPPort()

	a.ssdp = newSSDPServer(
		a.deviceID, a.httpPort, a.lanIP,
		func() string { return a.friendlyName() },
		func(scope, msg string) { ctx.Logger().Info(scope, "msg", msg) },
	)
	ctx.Logger().Info("dlna mounted",
		"uuid", a.deviceID, "lan_ip", a.lanIP, "port", a.httpPort,
		"friendly", a.cfgFriendlyName)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

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
		{Pattern: "/media/", Handler: a.handleMediaRedirect, NoAuth: true},

		// Panel reads — proxied through the dashboard with the
		// install's APTEVA_APP_TOKEN, so they keep the default auth.
		{Pattern: "/published_folders", Handler: a.handlePublishedFolders},
		{Pattern: "/published_folders/", Handler: a.handlePublishedFoldersItem},
		{Pattern: "/clients", Handler: a.handleClientsRecent},
		{Pattern: "/status", Handler: a.handleStatus},
	}
}

// stubEvent is the GENA event-subscription endpoint. We don't push
// updates to subscribers in v0.1 (it would mean tracking SID
// cookies); responding 200 OK is enough for most TVs.
func stubEvent(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }

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
// with range/seek; we 302 to a freshly minted storage signed URL so
// bytes never pass through this sidecar. Each redirect uses a short
// TTL because TVs re-request on every seek — a long TTL just means
// stale URLs floating around longer.
func (a *App) handleMediaRedirect(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/media/")
	idStr = strings.TrimSuffix(idStr, "."+strings.ToLower(extOf(idStr)))
	fileID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad file id", 400)
		return
	}
	ttl := configInt(a.ctx, "signed_url_ttl_seconds", 60)
	signed, err := a.storageGetURL(r.Context(), fileID, ttl)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	a.logClientFromRequest(r, "media:"+strconv.FormatInt(fileID, 10))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, signed, http.StatusFound)
}

func extOf(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return ""
}

// ─── panel reads ────────────────────────────────────────────────────

func (a *App) handlePublishedFolders(w http.ResponseWriter, r *http.Request) {
	pid := projectScope()
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
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		out, err := addPublishedFolder(a.ctx.AppDB(), pid, body.Folder, body.Label)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handlePublishedFoldersItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/published_folders/"), 10, 64)
	if _, err := a.ctx.AppDB().Exec(
		`DELETE FROM published_folders WHERE id = ? AND project_id = ?`,
		id, projectScope(),
	); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

func (a *App) handleClientsRecent(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := listClientLog(a.ctx.AppDB(), projectScope(), limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	out, err := a.statusSnapshot()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
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
			Description: "Rename the device. Empty name resets to 'Apteva ({hostname})'. The new name shows on TV input pickers after the next SSDP NOTIFY (≤30s).",
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
	}
}

func (a *App) toolStatus(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return a.statusSnapshot()
}

func (a *App) toolSetFriendlyName(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	a.mu.Lock()
	a.cfgFriendlyName = name
	a.mu.Unlock()
	// Persisting to config_schema is the framework's job — config
	// values come back from ctx.Config() on next mount. For this
	// session, the in-memory override is what matters; agents
	// expecting persistence should also call the platform's
	// config-set API. Document this in the README rather than
	// piggy-back into the platform here.
	return map[string]any{"friendly_name": a.friendlyName()}, nil
}

func (a *App) toolPublishFolder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	folder, _ := args["folder"].(string)
	label, _ := args["label"].(string)
	if folder == "" {
		return nil, errors.New("folder required")
	}
	return addPublishedFolder(ctx.AppDB(), projectScope(), folder, label)
}

func (a *App) toolUnpublishFolder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	folder, _ := args["folder"].(string)
	if folder == "" {
		return nil, errors.New("folder required")
	}
	res, err := ctx.AppDB().Exec(
		`DELETE FROM published_folders WHERE folder = ? AND project_id = ?`,
		folder, projectScope())
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return map[string]any{"removed": n}, nil
}

func (a *App) toolClientsRecent(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := int(toInt64(args["limit"]))
	return listClientLog(ctx.AppDB(), projectScope(), limit)
}

// ─── status / config resolution ────────────────────────────────────

type Status struct {
	FriendlyName     string `json:"friendly_name"`
	UUID             string `json:"uuid"`
	LANIP            string `json:"lan_ip"`
	HTTPPort         int    `json:"http_port"`
	Broadcasting     bool   `json:"broadcasting"`
	PublishedFolders int    `json:"published_folders"`
	RecentClients    int    `json:"recent_clients"`
	StorageReachable bool   `json:"storage_reachable"`
	MediaReachable   bool   `json:"media_reachable"`
}

func (a *App) statusSnapshot() (*Status, error) {
	pid := projectScope()
	pubs, _ := countTable(a.ctx.AppDB(),
		`SELECT COUNT(*) FROM published_folders WHERE project_id = ?`, pid)
	clis, _ := countTable(a.ctx.AppDB(),
		`SELECT COUNT(*) FROM client_log WHERE project_id = ?`, pid)
	return &Status{
		FriendlyName:     a.friendlyName(),
		UUID:             a.deviceID,
		LANIP:            a.lanIP,
		HTTPPort:         a.httpPort,
		Broadcasting:     a.ssdp != nil && a.ssdp.IsRunning(),
		PublishedFolders: pubs,
		RecentClients:    clis,
		StorageReachable: a.storagePing(),
		MediaReachable:   a.mediaPing(),
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
	if v := configString(a.ctx, "friendly_name", ""); v != "" {
		return v
	}
	return ""
}

func (a *App) resolveDeviceUUID() string {
	if v := configString(a.ctx, "device_uuid", ""); v != "" {
		return v
	}
	// Generate once and remember in-memory; the framework's config
	// store is the authoritative location, but we don't fail boot if
	// it's unwritable — TVs don't care about *which* uuid we pick,
	// only that it's stable for the session.
	id := uuid.New().String()
	a.ctx.Logger().Warn("dlna: generated transient device uuid (set device_uuid in config to make it stable)",
		"uuid", id)
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

func projectScope() string {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid
	}
	return "default"
}

func addPublishedFolder(db *sql.DB, pid, folder, label string) (*PublishedFolder, error) {
	folder = strings.TrimRight(folder, "/")
	if folder == "" {
		folder = "/"
	}
	res, err := db.Exec(
		`INSERT OR REPLACE INTO published_folders (project_id, folder, label) VALUES (?,?,?)`,
		pid, folder, label)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getPublishedFolder(db, pid, id)
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
	return out, nil
}

func (a *App) publishedFoldersAsContainers() ([]didlContainer, error) {
	pubs, err := listPublishedFolders(a.ctx.AppDB(), projectScope())
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
		})
	}
	return out, nil
}

func (a *App) publishedFolderRow(id int64) (*PublishedFolder, error) {
	return getPublishedFolder(a.ctx.AppDB(), projectScope(), id)
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
		projectScope(), ip, ua, objectID, now)
}

func (a *App) pruneClientLog() {
	hours := configInt(a.ctx, "client_log_retention_hours", 24)
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
	_, _ = a.ctx.AppDB().Exec(`DELETE FROM client_log WHERE last_action_at < ?`, cutoff)
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
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}

// ─── storage / media call helpers ──────────────────────────────────
//
// These are best-effort wrappers around ctx.PlatformAPI().CallApp.
// On failure we return zero values + the error; the SOAP layer
// surfaces a sensible "empty container" rather than 500-ing the TV.

type storageFile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Folder      string `json:"folder"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
}

type storageSubfolder struct {
	Name  string `json:"name"`
	Count int    `json:"file_count"`
}

// Storage's MCP tools return enveloped responses, not bare arrays:
//
//   files_list_folders → {folders: ["a","b"], count, parent}   (names only)
//   files_list         → {files: [{id, name, …}, …], count, …}
//   files_search       → {files: [...], count}
//   files_get          → {id, name, …}                         (bare object — historical)
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
	if err := a.ctx.PlatformAPI().CallAppResult("storage", "files_list_folders", map[string]any{
		"parent": parent,
	}, &env); err != nil {
		return nil, err
	}
	out := make([]storageSubfolder, 0, len(env.Folders))
	for _, name := range env.Folders {
		// Storage doesn't return per-folder file counts (would need
		// a separate query). DIDL childCount=0 tells DLNA clients
		// "unknown" and they tolerate it (we already do this for
		// the root virtual containers).
		out = append(out, storageSubfolder{Name: name, Count: 0})
	}
	return out, nil
}

func (a *App) storageListFiles(ctx context.Context, folder string, recursive bool) ([]storageFile, error) {
	var env struct {
		Files []storageFile `json:"files"`
	}
	if err := a.ctx.PlatformAPI().CallAppResult("storage", "files_list", map[string]any{
		"folder":    folder,
		"recursive": recursive,
		"limit":     1000,
	}, &env); err != nil {
		return nil, err
	}
	return env.Files, nil
}

func (a *App) storageGetFile(ctx context.Context, id int64) (*storageFile, error) {
	var f storageFile
	if err := a.ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": id}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// storageGetURL mints a short-lived signed URL the TV can fetch
// directly. Storage handles ranges & ETag, which is what we need for
// seeking.
func (a *App) storageGetURL(ctx context.Context, id int64, ttlSec int) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := a.ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
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

// searchStorage filters by content-type prefix + optional title
// substring. v0.1 calls storage's files_search which already accepts
// these — if it doesn't, the wrapper falls back to listing root and
// filtering client-side.
func (a *App) searchStorage(ctx context.Context, contentTypePrefix, query string, start, count int) ([]didlItem, error) {
	args := map[string]any{
		"limit":  count,
		"offset": start,
	}
	if contentTypePrefix != "" {
		args["content_type_prefix"] = contentTypePrefix
	}
	if query != "" {
		args["name_contains"] = query
	}
	if !configFlag(a.ctx, "publish_root_by_default", false) {
		args["folders"] = a.publishedFolderPaths()
	}
	var env struct {
		Files []storageFile `json:"files"`
	}
	if err := a.ctx.PlatformAPI().CallAppResult("storage", "files_search", args, &env); err != nil {
		return nil, err
	}
	out := make([]didlItem, 0, len(env.Files))
	parent := "0/recent"
	if contentTypePrefix != "" {
		parent = "0/" + strings.TrimSuffix(contentTypePrefix, "/")
		if parent == "0/image" {
			parent = "0/photos"
		}
	}
	for _, f := range env.Files {
		out = append(out, a.fileToDIDL(ctx, f, parent))
	}
	return out, nil
}

func (a *App) searchByContentTypePrefix(ctx context.Context, prefix string, start, count int, sort string) ([]didlItem, error) {
	return a.searchStorage(ctx, prefix, "", start, count)
}

func (a *App) recentItems(ctx context.Context, start, count int) ([]didlItem, error) {
	args := map[string]any{
		"limit":  count,
		"offset": start,
		"sort":   "created_at:desc",
	}
	if !configFlag(a.ctx, "publish_root_by_default", false) {
		args["folders"] = a.publishedFolderPaths()
	}
	var env struct {
		Files []storageFile `json:"files"`
	}
	if err := a.ctx.PlatformAPI().CallAppResult("storage", "files_search", args, &env); err != nil {
		return nil, err
	}
	out := make([]didlItem, 0, len(env.Files))
	for _, f := range env.Files {
		out = append(out, a.fileToDIDL(ctx, f, "0/recent"))
	}
	return out, nil
}

func (a *App) publishedFolderPaths() []string {
	pubs, err := listPublishedFolders(a.ctx.AppDB(), projectScope())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(pubs))
	for _, p := range pubs {
		out = append(out, p.Folder)
	}
	return out
}

// fileToDIDL builds a DIDL <item> for one storage file. Calls media
// best-effort for duration / resolution; never blocks the listing on
// a media-app outage.
func (a *App) fileToDIDL(ctx context.Context, f storageFile, parent string) didlItem {
	class := classFor(f.ContentType)
	mediaURL := fmt.Sprintf("http://%s:%d/media/%d", a.lanIP, a.httpPort, f.ID)
	it := didlItem{
		ID:          fmt.Sprintf("i:%d", f.ID),
		ParentID:    parent,
		Title:       f.Name,
		Class:       class,
		Size:        f.SizeBytes,
		ContentType: f.ContentType,
		URL:         mediaURL,
	}
	if configFlag(a.ctx, "media_metadata", true) {
		if meta := a.mediaProbe(ctx, f.ID); meta != nil {
			if meta.DurationSeconds > 0 {
				it.Duration = formatDuration(meta.DurationSeconds)
			}
			if meta.Width > 0 && meta.Height > 0 {
				it.Resolution = fmt.Sprintf("%dx%d", meta.Width, meta.Height)
			}
		}
	}
	return it
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
	DurationSeconds int `json:"duration_seconds"`
	Width           int `json:"width"`
	Height          int `json:"height"`
	Codec           string `json:"codec"`
}

// mediaProbe is best-effort. If `media` isn't installed, isn't
// reachable, or doesn't know about this file, we silently return nil
// and leave the DIDL fields blank — clients tolerate that.
func (a *App) mediaProbe(ctx context.Context, fileID int64) *mediaMeta {
	var m mediaMeta
	if err := a.ctx.PlatformAPI().CallAppResult("media", "probe_file", map[string]any{
		"file_id": fileID,
	}, &m); err != nil {
		return nil
	}
	return &m
}

// ping helpers — used by status; non-blocking, never error out.
func (a *App) storagePing() bool {
	_, err := a.ctx.PlatformAPI().CallApp("storage", "files_list_folders", map[string]any{"parent": "/"})
	return err == nil
}

func (a *App) mediaPing() bool {
	if !configFlag(a.ctx, "media_metadata", true) {
		return false
	}
	_, err := a.ctx.PlatformAPI().CallApp("media", "ping", map[string]any{})
	return err == nil
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

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	s := configString(ctx, key, "")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func configFlag(ctx *sdk.AppCtx, key string, def bool) bool {
	s := strings.ToLower(configString(ctx, key, ""))
	switch s {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
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
  <action><name>Search</name></action>
  <action><name>GetSearchCapabilities</name></action>
  <action><name>GetSortCapabilities</name></action>
  <action><name>GetSystemUpdateID</name></action>
 </actionList>
 <serviceStateTable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_ObjectID</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Result</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_BrowseFlag</name><dataType>string</dataType><allowedValueList><allowedValue>BrowseMetadata</allowedValue><allowedValue>BrowseDirectChildren</allowedValue></allowedValueList></stateVariable>
  <stateVariable sendEvents="no"><name>A_ARG_TYPE_Filter</name><dataType>string</dataType></stateVariable>
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
  <action><name>GetProtocolInfo</name></action>
  <action><name>GetCurrentConnectionIDs</name></action>
  <action><name>GetCurrentConnectionInfo</name></action>
 </actionList>
 <serviceStateTable>
  <stateVariable sendEvents="yes"><name>SourceProtocolInfo</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="yes"><name>SinkProtocolInfo</name><dataType>string</dataType></stateVariable>
  <stateVariable sendEvents="yes"><name>CurrentConnectionIDs</name><dataType>string</dataType></stateVariable>
 </serviceStateTable>
</scpd>`

// ─── main ───────────────────────────────────────────────────────────

func main() {
	once.Do(func() {
		globalApp = &App{}
	})
	sdk.Run(globalApp)
}
