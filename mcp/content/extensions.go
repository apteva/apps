package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Extension is a generic, app-provided public surface. The manifest remains
// opaque JSON in storage; these types only describe the safe runtime contract.
type Extension struct {
	ID                int64             `json:"id"`
	ProjectID         string            `json:"project_id,omitempty"`
	SiteID            int64             `json:"site_id"`
	Key               string            `json:"key"`
	ProviderApp       string            `json:"provider_app"`
	DisplayName       string            `json:"display_name"`
	Version           string            `json:"version"`
	Status            string            `json:"status"`
	HasDraftChanges   bool              `json:"has_draft_changes"`
	DraftManifest     ExtensionManifest `json:"draft_manifest"`
	PublishedManifest ExtensionManifest `json:"published_manifest"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
	PublishedAt       string            `json:"published_at,omitempty"`
}

type ExtensionManifest struct {
	Name           string                     `json:"name"`
	Version        string                     `json:"version"`
	Routes         []ExtensionRoute           `json:"routes"`
	DataSources    map[string]ExtensionCall   `json:"data_sources,omitempty"`
	Actions        map[string]ExtensionAction `json:"actions,omitempty"`
	Templates      map[string]string          `json:"templates"`
	Assets         map[string]string          `json:"assets,omitempty"`
	Settings       map[string]any             `json:"settings,omitempty"`
	SettingsSchema []ExtensionSetting         `json:"settings_schema,omitempty"`
	BrowserPolicy  ExtensionBrowserPolicy     `json:"browser_policy,omitempty"`
}

type ExtensionBrowserPolicy struct {
	ScriptOrigins  []string `json:"script_origins,omitempty"`
	FrameOrigins   []string `json:"frame_origins,omitempty"`
	ConnectOrigins []string `json:"connect_origins,omitempty"`
	ImageOrigins   []string `json:"image_origins,omitempty"`
}

type ExtensionRoute struct {
	Name        string   `json:"name"`
	Pattern     string   `json:"pattern"`
	Template    string   `json:"template"`
	DataSources []string `json:"data_sources,omitempty"`
}

type ExtensionCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

type ExtensionAction struct {
	AllowedInput  []string        `json:"allowed_input,omitempty"`
	Steps         []ExtensionCall `json:"steps"`
	RotateSession bool            `json:"rotate_session_on_success,omitempty"`
}

type ExtensionSetting struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Default any    `json:"default,omitempty"`
}

type extensionPageData struct {
	SiteTitle     string
	SiteTagline   string
	Locale        string
	URLPrefix     string
	ResourceQuery string
	ExtensionKey  string
	RouteName     string
	Params        map[string]any
	Query         map[string]any
	Data          map[string]any
	Settings      map[string]any
}

var extensionKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)

func validateExtensionManifest(key, provider string, manifest ExtensionManifest) error {
	if !extensionKeyRE.MatchString(key) {
		return errors.New("extension key must start with a letter and contain only lowercase letters, digits, _ or -")
	}
	if strings.TrimSpace(provider) == "" {
		return errors.New("provider_app required")
	}
	if len(manifest.Routes) == 0 || len(manifest.Templates) == 0 {
		return errors.New("manifest requires routes and templates")
	}
	seenPatterns := map[string]bool{}
	for _, route := range manifest.Routes {
		if route.Name == "" || route.Pattern == "" || route.Template == "" {
			return errors.New("every route requires name, pattern, and template")
		}
		if !strings.HasPrefix(route.Pattern, "/") || strings.HasPrefix(route.Pattern, "/_") ||
			strings.HasPrefix(route.Pattern, "/admin") || strings.Contains(route.Pattern, "?") {
			return fmt.Errorf("route pattern %q is reserved or invalid", route.Pattern)
		}
		if seenPatterns[route.Pattern] {
			return fmt.Errorf("duplicate route pattern %q", route.Pattern)
		}
		seenPatterns[route.Pattern] = true
		if _, ok := manifest.Templates[route.Template]; !ok {
			return fmt.Errorf("route %q references missing template %q", route.Name, route.Template)
		}
		for _, source := range route.DataSources {
			if _, ok := manifest.DataSources[source]; !ok {
				return fmt.Errorf("route %q references missing data source %q", route.Name, source)
			}
		}
	}
	for name, source := range manifest.DataSources {
		if name == "" || strings.TrimSpace(source.Tool) == "" {
			return errors.New("data sources require names and tools")
		}
	}
	for name, action := range manifest.Actions {
		if name == "" || len(action.Steps) == 0 {
			return errors.New("actions require names and at least one step")
		}
		for _, step := range action.Steps {
			if strings.TrimSpace(step.Tool) == "" {
				return fmt.Errorf("action %q has a step without a tool", name)
			}
		}
	}
	for name := range manifest.Assets {
		clean := path.Clean("/" + name)
		if strings.Contains(name, "..") || clean == "/" {
			return fmt.Errorf("invalid asset path %q", name)
		}
	}
	for directive, origins := range map[string][]string{
		"script_origins":  manifest.BrowserPolicy.ScriptOrigins,
		"frame_origins":   manifest.BrowserPolicy.FrameOrigins,
		"connect_origins": manifest.BrowserPolicy.ConnectOrigins,
		"image_origins":   manifest.BrowserPolicy.ImageOrigins,
	} {
		for _, origin := range origins {
			if err := validateBrowserOrigin(origin); err != nil {
				return fmt.Errorf("%s: %w", directive, err)
			}
		}
	}
	return nil
}

func validateBrowserOrigin(origin string) error {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("invalid HTTPS origin %q", origin)
	}
	host := parsed.Hostname()
	if strings.Contains(host, "*") && !strings.HasPrefix(host, "*.") {
		return fmt.Errorf("wildcard origin %q must use a leading *.", origin)
	}
	return nil
}

func extensionContentSecurityPolicy(policy ExtensionBrowserPolicy) string {
	join := func(base string, origins []string) string {
		if len(origins) == 0 {
			return base
		}
		return base + " " + strings.Join(origins, " ")
	}
	frameSource := "frame-src 'none'"
	if len(policy.FrameOrigins) > 0 {
		frameSource = "frame-src " + strings.Join(policy.FrameOrigins, " ")
	}
	return strings.Join([]string{
		"default-src 'self'",
		join("script-src 'self'", policy.ScriptOrigins),
		"style-src 'self' 'unsafe-inline'",
		join("connect-src 'self'", policy.ConnectOrigins),
		frameSource,
		join("img-src 'self' data: https:", policy.ImageOrigins),
		"font-src 'self' data:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}, "; ")
}

func dbExtensionsList(db *sql.DB, pid string, siteID int64) ([]Extension, error) {
	rows, err := db.Query(`SELECT id, project_id, site_id, extension_key, provider_app, display_name,
		version, status, draft_manifest, published_manifest, created_at, updated_at, published_at
		FROM content_extensions WHERE project_id=? AND site_id=? ORDER BY display_name, extension_key`, pid, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Extension
	for rows.Next() {
		ext, err := scanExtension(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ext)
	}
	return out, rows.Err()
}

func dbExtensionGet(db *sql.DB, pid string, siteID int64, key string) (*Extension, error) {
	row := db.QueryRow(`SELECT id, project_id, site_id, extension_key, provider_app, display_name,
		version, status, draft_manifest, published_manifest, created_at, updated_at, published_at
		FROM content_extensions WHERE project_id=? AND site_id=? AND extension_key=?`, pid, siteID, key)
	return scanExtension(row)
}

func scanExtension(row rowScanner) (*Extension, error) {
	var ext Extension
	var draftJSON, publishedJSON string
	var publishedAt sql.NullString
	if err := row.Scan(&ext.ID, &ext.ProjectID, &ext.SiteID, &ext.Key, &ext.ProviderApp,
		&ext.DisplayName, &ext.Version, &ext.Status, &draftJSON, &publishedJSON,
		&ext.CreatedAt, &ext.UpdatedAt, &publishedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(draftJSON), &ext.DraftManifest); err != nil {
		return nil, fmt.Errorf("decode draft extension %q: %w", ext.Key, err)
	}
	if publishedJSON != "" && publishedJSON != "{}" {
		if err := json.Unmarshal([]byte(publishedJSON), &ext.PublishedManifest); err != nil {
			return nil, fmt.Errorf("decode published extension %q: %w", ext.Key, err)
		}
	}
	ext.HasDraftChanges = draftJSON != publishedJSON
	if publishedAt.Valid {
		ext.PublishedAt = publishedAt.String
	}
	return &ext, nil
}

func dbExtensionUpsert(db *sql.DB, pid string, siteID int64, key, provider string, manifest ExtensionManifest, publish bool) (*Extension, error) {
	if err := validateExtensionManifest(key, provider, manifest); err != nil {
		return nil, err
	}
	if err := validateExtensionRouteOwnership(db, pid, siteID, key, manifest); err != nil {
		return nil, err
	}
	existing, lookupErr := dbExtensionGet(db, pid, siteID, key)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return nil, lookupErr
	}
	if existing != nil && existing.ProviderApp != provider {
		return nil, errors.New("extension key is already owned by another provider")
	}
	if existing != nil {
		if manifest.Settings == nil {
			manifest.Settings = map[string]any{}
		}
		allowed := extensionSettingKeys(manifest.SettingsSchema)
		for settingKey, value := range existing.DraftManifest.Settings {
			if len(allowed) == 0 || allowed[settingKey] {
				manifest.Settings[settingKey] = value
			}
		}
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	display := firstNonEmpty(manifest.Name, key)
	status := "draft"
	published := "{}"
	var publishedAt any
	if publish {
		status, published, publishedAt = "published", string(body), nowStamp()
	}
	_, err = db.Exec(`INSERT INTO content_extensions
		(project_id, site_id, extension_key, provider_app, display_name, version, status,
		 draft_manifest, published_manifest, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, site_id, extension_key) DO UPDATE SET
		 provider_app=excluded.provider_app, display_name=excluded.display_name,
		 version=excluded.version, draft_manifest=excluded.draft_manifest,
		 published_manifest=CASE WHEN excluded.status='published' THEN excluded.published_manifest ELSE content_extensions.published_manifest END,
		 status=CASE WHEN excluded.status='published' THEN 'published' ELSE content_extensions.status END,
		 published_at=CASE WHEN excluded.status='published' THEN excluded.published_at ELSE content_extensions.published_at END,
		 updated_at=CURRENT_TIMESTAMP`,
		pid, siteID, key, provider, display, manifest.Version, status, string(body), published, publishedAt)
	if err != nil {
		return nil, err
	}
	ext, err := dbExtensionGet(db, pid, siteID, key)
	if err != nil {
		return nil, err
	}
	if publish {
		if _, err := db.Exec(`INSERT INTO content_extension_versions(extension_id, version, manifest)
			VALUES (?, ?, ?)`, ext.ID, manifest.Version, string(body)); err != nil {
			return nil, err
		}
	}
	return ext, nil
}

func validateExtensionRouteOwnership(db *sql.DB, pid string, siteID int64, key string, manifest ExtensionManifest) error {
	extensions, err := dbExtensionsList(db, pid, siteID)
	if err != nil {
		return err
	}
	claimed := map[string]string{}
	for _, ext := range extensions {
		if ext.Key == key {
			continue
		}
		for _, route := range ext.DraftManifest.Routes {
			claimed[extensionRouteShape(route.Pattern)] = ext.Key
		}
	}
	for _, route := range manifest.Routes {
		if owner := claimed[extensionRouteShape(route.Pattern)]; owner != "" {
			return fmt.Errorf("route %q conflicts with extension %q", route.Pattern, owner)
		}
	}
	return nil
}

func extensionRouteShape(pattern string) string {
	parts := splitRoutePath(pattern)
	for index, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[index] = ":"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func extensionSettingKeys(schema []ExtensionSetting) map[string]bool {
	keys := make(map[string]bool, len(schema))
	for _, setting := range schema {
		if setting.Key != "" {
			keys[setting.Key] = true
		}
	}
	return keys
}

func dbExtensionUpdateSettings(db *sql.DB, pid string, siteID int64, key string, settings map[string]any) (*Extension, error) {
	ext, err := dbExtensionGet(db, pid, siteID, key)
	if err != nil {
		return nil, err
	}
	allowed := extensionSettingKeys(ext.DraftManifest.SettingsSchema)
	if ext.DraftManifest.Settings == nil {
		ext.DraftManifest.Settings = map[string]any{}
	}
	for settingKey, value := range settings {
		if !allowed[settingKey] {
			return nil, fmt.Errorf("unknown extension setting %q", settingKey)
		}
		ext.DraftManifest.Settings[settingKey] = value
	}
	body, err := json.Marshal(ext.DraftManifest)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE content_extensions SET draft_manifest=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND site_id=? AND extension_key=?`, string(body), pid, siteID, key); err != nil {
		return nil, err
	}
	return dbExtensionGet(db, pid, siteID, key)
}

func dbExtensionPublish(db *sql.DB, pid string, siteID int64, key string) (*Extension, error) {
	ext, err := dbExtensionGet(db, pid, siteID, key)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(ext.DraftManifest)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE content_extensions SET status='published',
		published_manifest=draft_manifest, published_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, ext.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO content_extension_versions(extension_id, version, manifest)
		VALUES (?, ?, ?)`, ext.ID, ext.DraftManifest.Version, string(body)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbExtensionGet(db, pid, siteID, key)
}

func (a *App) toolExtensionsUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	key, provider := asString(args["key"]), asString(args["provider_app"])
	raw, ok := args["manifest"]
	if !ok {
		return nil, errors.New("manifest required")
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var manifest ExtensionManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	publish, _ := args["publish"].(bool)
	ext, err := dbExtensionUpsert(ctx.AppDB(), pid, siteID, key, provider, manifest, publish)
	if err == nil {
		invalidatePageCacheForSite(siteID)
		ctx.Emit("content.extension.updated", map[string]any{"site_id": siteID, "key": key, "provider_app": provider})
	}
	return map[string]any{"extension": ext}, err
}

func (a *App) toolExtensionsRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	key := asString(args["key"])
	result, err := ctx.AppDB().Exec(`DELETE FROM content_extensions WHERE project_id=? AND site_id=? AND extension_key=?`, pid, siteID, key)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	invalidatePageCacheForSite(siteID)
	return map[string]any{"ok": n > 0}, nil
}

func (a *App) toolExtensionsInvalidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	invalidatePageCacheForSite(siteID)
	return map[string]any{"ok": true, "site_id": siteID}, nil
}

func matchExtensionPattern(pattern, requestPath string) (map[string]any, bool) {
	patternParts := splitRoutePath(pattern)
	pathParts := splitRoutePath(requestPath)
	if len(patternParts) != len(pathParts) {
		return nil, false
	}
	params := map[string]any{}
	for i, part := range patternParts {
		if strings.HasPrefix(part, ":") {
			name := strings.TrimPrefix(part, ":")
			if name == "" || pathParts[i] == "" {
				return nil, false
			}
			params[name] = pathParts[i]
			continue
		}
		if part != pathParts[i] {
			return nil, false
		}
	}
	return params, true
}

func splitRoutePath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func requestQueryMap(r *http.Request) map[string]any {
	out := map[string]any{}
	for key, values := range r.URL.Query() {
		if key == "project_id" || key == "site" {
			continue
		}
		if len(values) == 1 {
			out[key] = values[0]
		} else {
			items := make([]any, len(values))
			for i := range values {
				items[i] = values[i]
			}
			out[key] = items
		}
	}
	return out
}

func extensionSessionCookieName(pid string, siteID int64, extensionKey string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", pid, siteID, extensionKey)))
	return "apteva_content_" + base64.RawURLEncoding.EncodeToString(sum[:9])
}

func extensionSession(w http.ResponseWriter, r *http.Request, cookieName string, forceNew bool) string {
	if cookie, err := r.Cookie(cookieName); err == nil && !forceNew {
		if token, ok := verifyExtensionSession(cookie.Value); ok {
			return token
		}
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: signExtensionSession(token), Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: 60 * 60 * 24 * 30,
	})
	return token
}

var extensionSessionFallbackKey = func() []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return key
}()

func extensionSessionKey() []byte {
	if value := os.Getenv("APTEVA_APP_TOKEN"); value != "" {
		sum := sha256.Sum256([]byte(value))
		return sum[:]
	}
	return extensionSessionFallbackKey
}

func signExtensionSession(token string) string {
	mac := hmac.New(sha256.New, extensionSessionKey())
	_, _ = mac.Write([]byte(token))
	return token + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyExtensionSession(value string) (string, bool) {
	token, signature, ok := strings.Cut(value, ".")
	if !ok || len(token) < 24 {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, extensionSessionKey())
	_, _ = mac.Write([]byte(token))
	return token, hmac.Equal(decoded, mac.Sum(nil))
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func invokeExtensionCall(ctx *sdk.AppCtx, pid, provider string, call ExtensionCall, vars map[string]any) (any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	args, _ := substituteTemplates(call.Args, vars).(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	args["_project_id"] = pid
	var out any
	if err := ctx.PlatformAPI().CallAppResult(provider, call.Tool, args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) tryHandleExtensionRoute(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64) bool {
	extensions, err := dbExtensionsList(ctx.AppDB(), pid, siteID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return true
	}
	for _, ext := range extensions {
		if ext.Status != "published" {
			continue
		}
		manifest := ext.PublishedManifest
		for _, route := range manifest.Routes {
			params, ok := matchExtensionPattern(route.Pattern, r.URL.Path)
			if !ok {
				continue
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return true
			}
			session := extensionSession(w, r, extensionSessionCookieName(pid, siteID, ext.Key), false)
			query := requestQueryMap(r)
			vars := map[string]any{
				"route": params, "query": query,
				"session":  map[string]any{"token": session},
				"settings": manifest.Settings,
			}
			data := map[string]any{}
			for _, name := range route.DataSources {
				source := manifest.DataSources[name]
				value, callErr := invokeExtensionCall(ctx, pid, ext.ProviderApp, source, vars)
				if callErr != nil {
					http.Error(w, "storefront data unavailable", http.StatusBadGateway)
					return true
				}
				data[name] = value
				vars["data"] = data
			}
			body, renderErr := renderExtensionTemplate(ext, route, extensionPageData{
				SiteTitle: firstNonEmpty(manifest.Name, ext.DisplayName),
				Locale:    "en", URLPrefix: computeURLPrefix(r), ResourceQuery: resourceQuery(r),
				ExtensionKey: ext.Key, RouteName: route.Name, Params: params,
				Query: query, Data: data, Settings: manifest.Settings,
			})
			if renderErr != nil {
				http.Error(w, "storefront render failed", http.StatusInternalServerError)
				return true
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "private, no-cache")
			if len(manifest.BrowserPolicy.ScriptOrigins)+len(manifest.BrowserPolicy.FrameOrigins)+
				len(manifest.BrowserPolicy.ConnectOrigins)+len(manifest.BrowserPolicy.ImageOrigins) > 0 {
				w.Header().Set("Content-Security-Policy", extensionContentSecurityPolicy(manifest.BrowserPolicy))
			}
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(body))
			}
			return true
		}
	}
	return false
}

func resourceQuery(r *http.Request) string {
	values := url.Values{}
	for _, key := range []string{"project_id", "site"} {
		if value := r.URL.Query().Get(key); value != "" {
			values.Set(key, value)
		}
	}
	if encoded := values.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

func renderExtensionTemplate(ext Extension, route ExtensionRoute, data extensionPageData) (string, error) {
	source := ext.PublishedManifest.Templates[route.Template]
	funcs := template.FuncMap{
		"asset": func(name string) string {
			return data.URLPrefix + "_extensions/" + url.PathEscape(ext.Key) + "/assets/" + strings.TrimPrefix(name, "/") + data.ResourceQuery
		},
		"action": func(name string) string {
			return data.URLPrefix + "_actions/" + url.PathEscape(ext.Key) + "/" + url.PathEscape(name) + data.ResourceQuery
		},
		"href": func(target string) string {
			return data.URLPrefix + strings.TrimPrefix(target, "/") + data.ResourceQuery
		},
		"get": func(value any, key string) any {
			switch typed := value.(type) {
			case map[string]any:
				return typed[key]
			default:
				return nil
			}
		},
		"first": func(value any) any {
			switch typed := value.(type) {
			case []any:
				if len(typed) > 0 {
					return typed[0]
				}
			}
			return nil
		},
		"text": func(value any) string {
			if value == nil {
				return ""
			}
			return fmt.Sprint(value)
		},
		"default": func(fallback string, value any) string {
			if text := strings.TrimSpace(fmt.Sprint(value)); value != nil && text != "" && text != "<nil>" {
				return text
			}
			return fallback
		},
		"money": func(value any, currency any) string {
			cents, _ := asInt64(value)
			return strings.ToUpper(fmt.Sprint(currency)) + " " + fmt.Sprintf("%.2f", float64(cents)/100)
		},
		"json": func(value any) template.JS {
			body, _ := json.Marshal(value)
			return template.JS(body)
		},
		"safeHTML": sanitizeHTML,
	}
	tpl, err := template.New(ext.Key + ":" + route.Template).Funcs(funcs).Parse(source)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (a *App) handleExtensionAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/_extensions/")
	parts := strings.SplitN(rest, "/assets/", 2)
	if len(parts) != 2 || strings.Contains(parts[1], "..") {
		http.NotFound(w, r)
		return
	}
	ext, err := dbExtensionGet(ctx.AppDB(), pid, siteID, parts[0])
	if err != nil || ext.Status != "published" {
		http.NotFound(w, r)
		return
	}
	body, ok := ext.PublishedManifest.Assets[parts[1]]
	if !ok {
		http.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(parts[1]))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

var (
	extensionRateMu  sync.Mutex
	extensionRateLog = map[string][]int64{}
)

func extensionRateLimitOK(key string) bool {
	now := time.Now().Unix()
	cutoff := now - 60
	extensionRateMu.Lock()
	defer extensionRateMu.Unlock()
	entries := extensionRateLog[key][:0]
	for _, stamp := range extensionRateLog[key] {
		if stamp >= cutoff {
			entries = append(entries, stamp)
		}
	}
	if len(entries) >= 30 {
		extensionRateLog[key] = entries
		return false
	}
	extensionRateLog[key] = append(entries, now)
	return true
}

func validActionOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, r.Host)
	}
	return r.Header.Get("X-Requested-With") == "storefront"
}

func (a *App) handleExtensionAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !validActionOrigin(r) {
		httpErr(w, http.StatusForbidden, "origin rejected")
		return
	}
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/_actions/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	ext, err := dbExtensionGet(ctx.AppDB(), pid, siteID, parts[0])
	if err != nil || ext.Status != "published" {
		http.NotFound(w, r)
		return
	}
	action, ok := ext.PublishedManifest.Actions[parts[1]]
	if !ok {
		http.NotFound(w, r)
		return
	}
	rateKey := extractIPHash(r) + "|" + ext.Key + "|" + parts[1]
	if !extensionRateLimitOK(rateKey) {
		httpErr(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	var input map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	allowed := map[string]bool{}
	for _, name := range action.AllowedInput {
		allowed[name] = true
	}
	for name := range input {
		if !allowed[name] {
			httpErr(w, http.StatusBadRequest, "input field not allowed: "+name)
			return
		}
	}
	cookieName := extensionSessionCookieName(pid, siteID, ext.Key)
	session := extensionSession(w, r, cookieName, false)
	vars := map[string]any{
		"input":    input,
		"session":  map[string]any{"token": session},
		"settings": ext.PublishedManifest.Settings,
		"steps":    []any{},
	}
	results := make([]any, 0, len(action.Steps))
	for _, step := range action.Steps {
		value, callErr := invokeExtensionCall(ctx, pid, ext.ProviderApp, step, vars)
		if callErr != nil {
			ctx.Logger().Error("extension action failed", "extension", ext.Key, "action", parts[1], "error", callErr)
			httpErr(w, http.StatusBadGateway, "storefront action unavailable")
			return
		}
		results = append(results, value)
		vars["steps"] = results
	}
	if action.RotateSession {
		_ = extensionSession(w, r, cookieName, true)
	}
	httpJSON(w, map[string]any{"ok": true, "result": results[len(results)-1], "steps": results})
}

func (a *App) handleHTTPExtensions(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		extensions, err := dbExtensionsList(ctx.AppDB(), pid, siteID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"extensions": extensions})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if settings, ok := body["settings"]; ok {
			key := strings.TrimSpace(fmt.Sprint(body["key"]))
			if key == "" {
				httpErr(w, http.StatusBadRequest, "extension key required")
				return
			}
			typed, ok := settings.(map[string]any)
			if !ok {
				httpErr(w, http.StatusBadRequest, "settings must be an object")
				return
			}
			ext, err := dbExtensionUpdateSettings(ctx.AppDB(), pid, siteID, key, typed)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, map[string]any{"extension": ext})
			return
		}
		body["_project_id"], body["_site_id"] = pid, siteID
		out, err := a.toolExtensionsUpsert(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPExtensionItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/extensions/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "extension key required")
		return
	}
	key := parts[0]
	if len(parts) == 2 && parts[1] == "publish" && r.Method == http.MethodPost {
		ext, err := dbExtensionPublish(ctx.AppDB(), pid, siteID, key)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		invalidatePageCacheForSite(siteID)
		httpJSON(w, map[string]any{"extension": ext})
		return
	}
	switch r.Method {
	case http.MethodGet:
		ext, err := dbExtensionGet(ctx.AppDB(), pid, siteID, key)
		if err != nil {
			httpErr(w, http.StatusNotFound, "extension not found")
			return
		}
		httpJSON(w, map[string]any{"extension": ext})
	case http.MethodDelete:
		out, err := a.toolExtensionsRemove(ctx, map[string]any{"_project_id": pid, "_site_id": siteID, "key": key})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
