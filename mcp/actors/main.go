package main

import (
	"context"
	_ "embed"
	_ "modernc.org/sqlite"

	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"golang.org/x/net/html"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxChars         = 20000
	defaultHistoryRetention = 90
)

var blockedNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type App struct{}

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type browserSession struct {
	SessionID        string            `json:"session_id"`
	Backend          string            `json:"backend"`
	RequestedBackend *string           `json:"requested_backend"`
	EffectiveBackend string            `json:"effective_backend"`
	CurrentURL       string            `json:"current_url"`
	DebugURL         string            `json:"debug_url,omitempty"`
	StreamURL        string            `json:"stream_url,omitempty"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	Proxy            browserProxyState `json:"proxy"`
}

type browserProxyState struct {
	Mode        string `json:"mode"`
	Provider    string `json:"provider,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	Country     string `json:"country,omitempty"`
	StickyScope string `json:"sticky_scope,omitempty"`
}

type browserExtractResult struct {
	SessionID         string          `json:"session_id"`
	Backend           string          `json:"backend"`
	CurrentURL        string          `json:"current_url"`
	URL               string          `json:"url"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	Text              string          `json:"text"`
	Markdown          string          `json:"markdown"`
	HTML              string          `json:"html"`
	Links             []linkInfo      `json:"links"`
	Images            []string        `json:"images"`
	Regions           []browserRegion `json:"regions"`
	Metadata          map[string]any  `json:"metadata"`
	StructuredData    map[string]any  `json:"structured_data"`
	Rendered          bool            `json:"rendered"`
	Truncated         bool            `json:"truncated"`
	ExtractionBackend string          `json:"extraction_backend"`
	Width             int             `json:"width"`
	Height            int             `json:"height"`
}

type browserRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type browserRegion struct {
	ID              string      `json:"id"`
	Tag             string      `json:"tag,omitempty"`
	Role            string      `json:"role,omitempty"`
	Selector        string      `json:"selector,omitempty"`
	Heading         string      `json:"heading,omitempty"`
	Text            string      `json:"text,omitempty"`
	Rect            browserRect `json:"rect"`
	ViewportRect    browserRect `json:"viewport_rect"`
	CoordinateFrame string      `json:"coordinate_frame"`
	Visible         bool        `json:"visible"`
	LinkCount       int         `json:"link_count,omitempty"`
	ImageCount      int         `json:"image_count,omitempty"`
}

type browserScreenshot struct {
	PNGB64     string `json:"png_b64"`
	CurrentURL string `json:"current_url"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type computerSOMScreenshot struct {
	CurrentURL string            `json:"current_url"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	SOM        []setOfMarkTarget `json:"som"`
}

type setOfMarkTarget struct {
	Label int    `json:"label"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	Tag   string `json:"tag"`
	Role  string `json:"role"`
	Text  string `json:"text"`
	Type  string `json:"type"`
}

type linkInfo struct {
	URL  string `json:"url"`
	Text string `json:"text,omitempty"`
}

type artifactSummary struct {
	ID        int64  `json:"id"`
	StorageID int64  `json:"storage_id,omitempty"`
	URL       string `json:"url,omitempty"`
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (a *App) openBrowser(callCtx context.Context, ctx *sdk.AppCtx, target string, args map[string]any) (*browserSession, error) {
	if err := validateBrowserTarget(ctx, target); err != nil {
		return nil, err
	}
	openArgs := map[string]any{"url": target}
	requestedBackend := ""
	if rawBackend := stringArg(args, "backend"); rawBackend != "" {
		b := strings.ToLower(strings.TrimSpace(rawBackend))
		if !validBrowserBackend(b) {
			return nil, fmt.Errorf("unsupported browser backend %q; omit backend to use Computer's configured default, or choose one of: local, browserbase, steel, browser-engine, service", rawBackend)
		}
		requestedBackend = b
		openArgs["backend"] = b
	}
	for _, key := range []string{"viewport", "environment", "context_id", "persist", "timeout", "proxy", "proxy_mode", "proxy_profile", "proxy_country", "proxy_sticky"} {
		if v, ok := args[key]; ok {
			openArgs[key] = v
		}
	}
	var out browserSession
	if err := sdk.CallAppResultContext(callCtx, ctx.PlatformAPI(), "computer", "browser_open", withProjectID(ctx, openArgs), &out); err != nil {
		if requestedBackend == "" {
			return nil, fmt.Errorf("computer.browser_open using Computer's configured default failed: %w; configure Computer's default backend and provider binding", err)
		}
		return nil, fmt.Errorf("computer.browser_open: %w", err)
	}
	if out.SessionID == "" {
		return nil, errors.New("computer.browser_open returned empty session_id")
	}
	out.Backend = strings.ToLower(strings.TrimSpace(out.Backend))
	if out.Backend == "" {
		a.closeBrowser(ctx, out.SessionID)
		return nil, errors.New("computer.browser_open returned no effective backend; configure Computer's default backend and provider binding")
	}
	if requestedBackend != "" {
		out.RequestedBackend = &requestedBackend
	}
	out.EffectiveBackend = out.Backend
	return &out, nil
}

func (a *App) closeBrowser(ctx *sdk.AppCtx, sessionID string) {
	if sessionID == "" {
		return
	}
	var out struct{}
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_close", withProjectID(ctx, map[string]any{"session_id": sessionID}), &out); err != nil {
		ctx.Logger().Warn("computer.browser_close failed", "session_id", sessionID, "err", err.Error())
	}
}

func (a *App) extractBrowserDOM(callCtx context.Context, ctx *sdk.AppCtx, sessionID string, args map[string]any, includeText bool) (*browserExtractResult, error) {
	formats := stringSliceArg(args, "formats")
	if len(formats) == 0 {
		formats = []string{"metadata", "structured_data", "links", "images"}
		if includeText {
			formats = append([]string{"text", "markdown"}, formats...)
		}
	}
	extractArgs := withProjectID(ctx, map[string]any{
		"session_id":  sessionID,
		"formats":     formats,
		"max_chars":   boundedInt(intArg(args, "max_chars"), defaultMaxChars, 1000, 200000),
		"readability": true,
	})
	if waitMS := intArg(args, "wait_ms"); waitMS > 0 {
		extractArgs["wait_ms"] = waitMS
	}
	var out browserExtractResult
	if err := sdk.CallAppResultContext(callCtx, ctx.PlatformAPI(), "computer", "browser_extract", extractArgs, &out); err != nil {
		return nil, fmt.Errorf("computer.browser_extract: %w", err)
	}
	if out.ExtractionBackend == "" {
		out.ExtractionBackend = "browser_dom"
	}
	return &out, nil
}

func stringSliceFromAny(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := stringFromAny(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func recoverInterruptedRuns(ctx *sdk.AppCtx) error {
	_, err := ctx.AppDB().Exec(
		`UPDATE actors_runs
		    SET status='failed', error='interrupted by app restart', completed_at=?
		  WHERE status='running'`,
		time.Now().UTC(),
	)
	return err
}

func pruneHistory(ctx *sdk.AppCtx) error {
	days := defaultHistoryRetention
	if raw := configString(ctx, "history_retention_days"); raw != "" {
		days = configInt(ctx, "history_retention_days")
	}
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	if _, err := ctx.AppDB().Exec(`DELETE FROM actors_artifacts WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM actors_runs WHERE created_at < ?`, cutoff)
	return err
}

func rollbackStoredFile(ctx *sdk.AppCtx, storageID int64) {
	if ctx == nil || storageID <= 0 {
		return
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_delete", withProjectID(ctx, map[string]any{"id": storageID}), &out); err != nil {
		ctx.Logger().Warn("rollback uploaded Actors artifact failed", "storage_id", storageID, "err", err.Error())
	}
}

func compactArtifactMetadata(kind, artifactURL, title, contentType string, size int) map[string]any {
	return map[string]any{
		"kind":         kind,
		"url":          artifactURL,
		"title":        title,
		"content_type": contentType,
		"bytes":        size,
	}
}

func insertArtifact(ctx *sdk.AppCtx, runID int64, kind, artifactURL, title string, storageID int64, storageURL, contentType string, size int, metadata any) (*artifactSummary, error) {
	metaBytes, _ := json.Marshal(metadata)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO actors_artifacts (project_id, run_id, kind, url, title, storage_id, storage_url, content_type, bytes, metadata_json)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		projectID(ctx), nullInt64(runID), kind, nullIfEmpty(artifactURL), nullIfEmpty(title), nullInt64(storageID), nullIfEmpty(storageURL), contentType, size, string(metaBytes),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &artifactSummary{ID: id, StorageID: storageID, URL: storageURL}, nil
}

func listRuns(ctx *sdk.AppCtx, limit, offset int) ([]map[string]any, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, kind, status, COALESCE(error,''), COALESCE(summary,''),
		        created_at, completed_at, output_json, COALESCE(actor_id,0),
		        COALESCE(actor_revision,0), cancel_requested_at
		 FROM actors_runs
		 WHERE project_id = ?
		 ORDER BY created_at DESC
		 LIMIT ? OFFSET ?`,
		projectID(ctx), limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var kind, status, errText, summary string
		var created time.Time
		var completed sql.NullTime
		var outputJSON sql.NullString
		var actorID, actorRevision int64
		var cancelRequested sql.NullTime
		if err := rows.Scan(&id, &kind, &status, &errText, &summary, &created, &completed, &outputJSON, &actorID, &actorRevision, &cancelRequested); err != nil {
			return nil, err
		}
		run := map[string]any{
			"id":         id,
			"kind":       kind,
			"status":     status,
			"error":      errText,
			"summary":    summary,
			"created_at": created.Format(time.RFC3339),
		}
		if completed.Valid {
			run["completed_at"] = completed.Time.UTC().Format(time.RFC3339)
			run["duration_ms"] = maxInt64(0, completed.Time.Sub(created).Milliseconds())
		}
		if actorID > 0 {
			run["actor_id"] = actorID
			run["actor_revision"] = actorRevision
		}
		if cancelRequested.Valid {
			run["cancel_requested_at"] = cancelRequested.Time.UTC().Format(time.RFC3339)
		}
		if outputJSON.Valid && outputJSON.String != "" {
			var details map[string]any
			if json.Unmarshal([]byte(outputJSON.String), &details) == nil {
				run["details"] = details
			}
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func countRuns(ctx *sdk.AppCtx) (int, error) {
	var count int
	err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_runs WHERE project_id=?`, projectID(ctx)).Scan(&count)
	return count, err
}

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "actors app is not mounted")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	ctx, contextErr := actorHTTPContext(r)
	if contextErr != nil {
		httpErr(w, http.StatusForbidden, contextErr.Error())
		return
	}
	runs, err := listRuns(ctx, limit, offset)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	total, err := countRuns(ctx)
	writeJSON(w, map[string]any{"runs": runs, "total": total, "limit": limit, "offset": offset}, err)
}

func schemaObject(props map[string]any, req []string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(req) > 0 {
		m["required"] = req
	}
	return m
}

func validBrowserBackend(backend string) bool {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "local", "browserbase", "steel", "browser-engine", "service":
		return true
	default:
		return false
	}
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("url host required")
	}
	if u.User != nil {
		return errors.New("url credentials are not allowed")
	}
	return nil
}

func validateBrowserTarget(ctx *sdk.AppCtx, raw string) error {
	if err := validateHTTPURL(raw); err != nil {
		return err
	}
	if allowPrivateNetworks(ctx) {
		return nil
	}
	u, _ := url.Parse(raw)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("private network URL blocked: %s", host)
	}
	if addr, err := netip.ParseAddr(host); err == nil && isBlockedAddress(addr) {
		return fmt.Errorf("private network URL blocked: %s", host)
	}
	return nil
}

func allowPrivateNetworks(ctx *sdk.AppCtx) bool {
	return boolArgDefault(map[string]any{"enabled": configString(ctx, "allow_private_networks")}, "enabled", false)
}

func isBlockedAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.WithZone("").Unmap()
	if !addr.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedNetworkPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func stringArg(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		switch x := v.(type) {
		case string:
			return x
		case fmt.Stringer:
			return x.String()
		}
	}
	return ""
}

func intArg(m map[string]any, k string) int {
	v, ok := m[k]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func boolArgDefault(m map[string]any, k string, def bool) bool {
	v, ok := m[k]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func stringSliceArg(m map[string]any, k string) []string {
	v, ok := m[k]
	if !ok {
		return nil
	}
	var out []string
	switch x := v.(type) {
	case []string:
		out = append(out, x...)
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

func boundedInt(got, def, min, max int) int {
	if got == 0 {
		got = def
	}
	if got < min {
		return min
	}
	if got > max {
		return max
	}
	return got
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return ""
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := strconv.Atoi(x.String())
		return n
	default:
		return 0
	}
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

func dedupeStrings(in []string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func withProjectID(ctx *sdk.AppCtx, args map[string]any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	if pid := ctx.CurrentProject(); pid != "" {
		out["_project_id"] = pid
	}
	return out
}

func projectID(ctx *sdk.AppCtx) string {
	if pid := ctx.CurrentProject(); pid != "" {
		return pid
	}
	return "global"
}

func configString(ctx *sdk.AppCtx, key string) string {
	if ctx == nil || ctx.Config() == nil {
		return ""
	}
	return strings.TrimSpace(ctx.Config().Get(key))
}

func configInt(ctx *sdk.AppCtx, key string) int {
	v := configString(ctx, key)
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}

func randName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func safeFilename(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '/':
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "actors"
	}
	return out
}

func mapFromAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func writeJSON(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("actors requires a database")
	}
	globalCtx = ctx
	return recoverInterruptedRuns(ctx)
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "actor-runner", Schedule: "@every 2s", Run: a.runActorWorker}}
}
func (a *App) MCPTools() []sdk.Tool { return append(a.actorTools(), a.platformTools()...) }
func (a *App) HTTPRoutes() []sdk.Route {
	return append([]sdk.Route{
		{Method: "GET", Pattern: "/runs", Handler: a.handleRuns},
		{Method: "GET", Pattern: "/runs/{id}", Handler: a.handleRunItem},
		{Method: "POST", Pattern: "/runs/{id}/cancel", Handler: a.handleRunCancel},
		{Method: "POST", Pattern: "/runs/{id}/retry", Handler: a.handleRunRetry},
		{Method: "GET", Pattern: "/actors", Handler: a.handleActors},
		{Method: "POST", Pattern: "/actors", Handler: a.handleActorSave},
		{Method: "DELETE", Pattern: "/actors/{id}", Handler: a.handleActorDelete},
		{Method: "POST", Pattern: "/actors/run", Handler: a.handleActorRun},
		{Method: "GET", Pattern: "/schedules", Handler: a.handleActorSchedules},
		{Method: "POST", Pattern: "/schedules", Handler: a.handleActorSchedule},
		{Method: "POST", Pattern: "/schedules/{id}/run", Handler: a.handleActorScheduleRunNow},
		{Method: "POST", Pattern: "/schedules/{id}/cancel", Handler: a.handleActorUnschedule},
	}, a.platformRoutes()...)
}
