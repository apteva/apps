// Commons v0.1 — user-owned ActivityPub node for Apteva.
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: commons
display_name: Commons
version: 0.1.2
description: User-owned public social node for the open web.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.ingress.read
    - platform.ingress.write
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: commons_profile_upsert, description: "Create or update a local public Commons profile." }
    - { name: commons_post_create, description: "Publish a local public post and queue federation delivery to followers." }
    - { name: commons_timeline_read, description: "Read recent local posts." }
    - { name: commons_follow_add, description: "Add a remote follower inbox manually." }
    - { name: commons_block_add, description: "Block a remote actor or domain." }
    - { name: commons_export, description: "Export profiles, posts, follows, and blocks as JSON." }
    - { name: commons_domain_expose, description: "Expose Commons on a public hostname through server-native ingress." }
    - { name: commons_domain_unexpose, description: "Remove a Commons public hostname from server-native ingress." }
  ui_panels:
    - slot: project.page
      label: Commons
      icon: network
      entry: /ui/CommonsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/commons
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/commons.db
  migrations: migrations/
config_schema:
  - name: default_domain
    label: Default public domain
    type: text
    required: false
upgrade_policy: auto-patch
`

const activityContext = "https://www.w3.org/ns/activitystreams"
const publicAudience = "https://www.w3.org/ns/activitystreams#Public"

var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{1,62}$`)

type App struct{}

type Profile struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Summary     string `json:"summary"`
	Domain      string `json:"domain"`
	ActorURL    string `json:"actor_url"`
	InboxURL    string `json:"inbox_url"`
	OutboxURL   string `json:"outbox_url"`
	PublicKey   string `json:"public_key_pem,omitempty"`
	PrivateKey  string `json:"-"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Post struct {
	ID           int64  `json:"id"`
	ProfileID    int64  `json:"profile_id"`
	Username     string `json:"username,omitempty"`
	Body         string `json:"body"`
	Visibility   string `json:"visibility"`
	ActivityID   string `json:"activity_id"`
	ObjectID     string `json:"object_id"`
	ActivityJSON string `json:"activity_json,omitempty"`
	ObjectJSON   string `json:"object_json,omitempty"`
	PublishedAt  string `json:"published_at"`
	CreatedAt    string `json:"created_at"`
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("commons app requires a db block")
	}
	ctx.Logger().Info("commons app mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/.well-known/webfinger", Handler: a.handleWebFinger, NoAuth: true},
		{Pattern: "/users/", Handler: a.handleUsers, NoAuth: true},
		{Pattern: "/objects/", Handler: a.handleObject, NoAuth: true},
		{Pattern: "/inbox", Handler: a.handleSharedInbox, NoAuth: true},
		{Pattern: "/@", Handler: a.handleProfilePage, NoAuth: true},
		{Pattern: "/api/", Handler: a.handleAPI},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "commons_profile_upsert",
			Description: "Create or update a local public Commons profile. Args: username, domain (optional if default_domain config is set), display_name?, summary?.",
			InputSchema: objectSchema(map[string]any{
				"username":     map[string]any{"type": "string"},
				"domain":       map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"summary":      map[string]any{"type": "string"},
			}, []string{"username"}),
			Handler: a.toolProfileUpsert,
		},
		{
			Name:        "commons_post_create",
			Description: "Publish a local public post and queue signed ActivityPub delivery to known followers. Args: username, body.",
			InputSchema: objectSchema(map[string]any{
				"username": map[string]any{"type": "string"},
				"body":     map[string]any{"type": "string"},
			}, []string{"username", "body"}),
			Handler: a.toolPostCreate,
		},
		{
			Name:        "commons_timeline_read",
			Description: "Read recent local posts. Args: username optional, limit optional.",
			InputSchema: objectSchema(map[string]any{
				"username": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolTimelineRead,
		},
		{
			Name:        "commons_follow_add",
			Description: "Manually add a remote follower inbox. Args: username, remote_actor, remote_inbox, remote_name optional.",
			InputSchema: objectSchema(map[string]any{
				"username":     map[string]any{"type": "string"},
				"remote_actor": map[string]any{"type": "string"},
				"remote_inbox": map[string]any{"type": "string"},
				"remote_name":  map[string]any{"type": "string"},
			}, []string{"username", "remote_actor", "remote_inbox"}),
			Handler: a.toolFollowAdd,
		},
		{
			Name:        "commons_block_add",
			Description: "Block a remote actor or domain. Args: username optional, target, kind actor|domain, reason optional.",
			InputSchema: objectSchema(map[string]any{
				"username": map[string]any{"type": "string"},
				"target":   map[string]any{"type": "string"},
				"kind":     map[string]any{"type": "string", "enum": []string{"actor", "domain"}},
				"reason":   map[string]any{"type": "string"},
			}, []string{"target", "kind"}),
			Handler: a.toolBlockAdd,
		},
		{
			Name:        "commons_export",
			Description: "Export Commons profiles, posts, follows, and blocks. Args: username optional.",
			InputSchema: objectSchema(map[string]any{
				"username": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolExport,
		},
		{
			Name:        "commons_domain_expose",
			Description: "Expose this Commons install on a public hostname through Apteva server-native ingress. Args: hostname, project_id optional, allow_http optional.",
			InputSchema: objectSchema(map[string]any{
				"hostname":   map[string]any{"type": "string"},
				"project_id": map[string]any{"type": "string"},
				"allow_http": map[string]any{"type": "boolean"},
			}, []string{"hostname"}),
			Handler: a.toolDomainExpose,
		},
		{
			Name:        "commons_domain_unexpose",
			Description: "Remove a Commons public hostname from Apteva server-native ingress. Args: hostname.",
			InputSchema: objectSchema(map[string]any{
				"hostname": map[string]any{"type": "string"},
			}, []string{"hostname"}),
			Handler: a.toolDomainUnexpose,
		},
	}
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "federation_delivery",
		Schedule: "@every 15s",
		Run: func(ctx context.Context, app *sdk.AppCtx) error {
			return deliverPending(ctx, app)
		},
	}}
}

// --- HTTP: ActivityPub public surface --------------------------------------

func (a *App) handleWebFinger(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimSpace(r.URL.Query().Get("resource"))
	if !strings.HasPrefix(resource, "acct:") {
		http.Error(w, "resource must be acct:user@domain", http.StatusBadRequest)
		return
	}
	acct := strings.TrimPrefix(resource, "acct:")
	user, domain, ok := strings.Cut(acct, "@")
	if !ok || user == "" || domain == "" {
		http.Error(w, "resource must be acct:user@domain", http.StatusBadRequest)
		return
	}
	p, err := dbProfileByUsername(globalCtx.AppDB(), user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil || normalizeDomain(p.Domain) != normalizeDomain(domain) {
		http.NotFound(w, r)
		return
	}
	writeJSONContent(w, "application/jrd+json", map[string]any{
		"subject": "acct:" + p.Username + "@" + normalizeDomain(p.Domain),
		"aliases": []string{p.ActorURL, profilePageURL(p)},
		"links": []map[string]string{{
			"rel":  "self",
			"type": "application/activity+json",
			"href": p.ActorURL,
		}},
	})
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/users/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	p, err := dbProfileByUsername(globalCtx.AppDB(), parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeActivityJSON(w, actorDocument(p))
		return
	}
	if len(parts) == 2 && parts[1] == "outbox" && r.Method == http.MethodGet {
		posts, err := dbListPosts(globalCtx.AppDB(), p.Username, 20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := []any{}
		for _, post := range posts {
			var activity map[string]any
			_ = json.Unmarshal([]byte(post.ActivityJSON), &activity)
			items = append(items, activity)
		}
		writeActivityJSON(w, map[string]any{
			"@context":     activityContext,
			"id":           p.OutboxURL,
			"type":         "OrderedCollection",
			"totalItems":   len(items),
			"orderedItems": items,
		})
		return
	}
	if len(parts) == 2 && parts[1] == "inbox" && r.Method == http.MethodPost {
		a.handleInboxForProfile(w, r, p)
		return
	}
	http.NotFound(w, r)
}

func (a *App) handleSharedInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	a.handleInboxForProfile(w, r, nil)
}

func (a *App) handleInboxForProfile(w http.ResponseWriter, r *http.Request, explicit *Profile) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var activity map[string]any
	if err := json.Unmarshal(raw, &activity); err != nil {
		http.Error(w, "invalid activity json", http.StatusBadRequest)
		return
	}
	typ := stringField(activity, "type")
	actor := stringField(activity, "actor")
	activityID := stringField(activity, "id")
	p := explicit
	if p == nil {
		p = profileForInboxObject(globalCtx.AppDB(), activity)
	}
	status := "stored"
	if p != nil && typ == "Follow" && actor != "" && !isBlocked(globalCtx.AppDB(), p.ID, actor) {
		status = "accepted"
		remote, _ := fetchRemoteActor(r.Context(), actor)
		inbox := remote.Inbox
		if inbox == "" {
			inbox = actor
		}
		_ = dbAddFollow(globalCtx.AppDB(), p.ID, actor, inbox, remote.Name)
		accept := map[string]any{
			"@context": activityContext,
			"id":       p.ActorURL + "#accept-" + randomID(),
			"type":     "Accept",
			"actor":    p.ActorURL,
			"object":   activity,
			"to":       []string{actor},
		}
		if inbox != "" {
			_ = dbQueueDelivery(globalCtx.AppDB(), p.ID, inbox, stringField(accept, "id"), accept)
		}
	}
	profileID := int64(0)
	if p != nil {
		profileID = p.ID
	}
	if err := dbStoreInbox(globalCtx.AppDB(), profileID, actor, activityID, typ, string(raw), status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) handleObject(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/objects/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	raw, err := dbObjectJSON(globalCtx.AppDB(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if raw == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/activity+json")
	_, _ = w.Write([]byte(raw))
}

func (a *App) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimPrefix(r.URL.Path, "/@")
	username = strings.Trim(username, "/")
	p, err := dbProfileByUsername(globalCtx.AppDB(), username)
	if err != nil || p == nil {
		http.NotFound(w, r)
		return
	}
	posts, _ := dbListPosts(globalCtx.AppDB(), p.Username, 20)
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8><title>")
	b.WriteString(htmlEscape(displayName(p)))
	b.WriteString("</title><body style=\"font:16px system-ui;max-width:720px;margin:48px auto;padding:0 20px;line-height:1.5\">")
	b.WriteString("<h1>")
	b.WriteString(htmlEscape(displayName(p)))
	b.WriteString("</h1><p><code>@")
	b.WriteString(htmlEscape(p.Username + "@" + normalizeDomain(p.Domain)))
	b.WriteString("</code></p><p>")
	b.WriteString(htmlEscape(p.Summary))
	b.WriteString("</p><hr>")
	for _, post := range posts {
		b.WriteString("<article><p>")
		b.WriteString(htmlEscape(post.Body))
		b.WriteString("</p><small>")
		b.WriteString(htmlEscape(post.PublishedAt))
		b.WriteString("</small></article><hr>")
	}
	b.WriteString("</body>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// --- HTTP: dashboard/API surface -------------------------------------------

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	switch {
	case path == "profiles" && r.Method == http.MethodGet:
		profiles, err := dbListProfiles(globalCtx.AppDB())
		respond(w, profiles, err)
	case path == "profiles" && r.Method == http.MethodPost:
		var body struct {
			Username    string `json:"username"`
			Domain      string `json:"domain"`
			DisplayName string `json:"display_name"`
			Summary     string `json:"summary"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Domain == "" {
			body.Domain = domainFromRequest(r)
		}
		profile, err := dbUpsertProfile(globalCtx.AppDB(), body.Username, body.Domain, body.DisplayName, body.Summary)
		respond(w, profile, err)
	case path == "posts" && r.Method == http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Body     string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		post, err := createPost(globalCtx.AppDB(), body.Username, body.Body)
		respond(w, post, err)
	case path == "timeline" && r.Method == http.MethodGet:
		limit := intFromString(r.URL.Query().Get("limit"), 50)
		posts, err := dbListPosts(globalCtx.AppDB(), r.URL.Query().Get("username"), limit)
		respond(w, posts, err)
	case path == "follows" && r.Method == http.MethodPost:
		var body struct {
			Username    string `json:"username"`
			RemoteActor string `json:"remote_actor"`
			RemoteInbox string `json:"remote_inbox"`
			RemoteName  string `json:"remote_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		result, err := addFollowByUsername(globalCtx.AppDB(), body.Username, body.RemoteActor, body.RemoteInbox, body.RemoteName)
		respond(w, result, err)
	case path == "blocks" && r.Method == http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Target   string `json:"target"`
			Kind     string `json:"kind"`
			Reason   string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		result, err := addBlockByUsername(globalCtx.AppDB(), body.Username, body.Target, body.Kind, body.Reason)
		respond(w, result, err)
	case path == "export" && r.Method == http.MethodGet:
		result, err := exportData(globalCtx.AppDB(), r.URL.Query().Get("username"))
		respond(w, result, err)
	case path == "ingress" && r.Method == http.MethodGet:
		routes, err := globalCtx.PlatformAPI().ListIngressRoutes()
		respond(w, map[string]any{"routes": routes}, err)
	case path == "ingress" && r.Method == http.MethodPost:
		var body struct {
			Hostname  string `json:"hostname"`
			ProjectID string `json:"project_id"`
			AllowHTTP bool   `json:"allow_http"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		route, err := exposeCommonsIngress(globalCtx, body.Hostname, body.ProjectID, body.AllowHTTP)
		respond(w, map[string]any{"route": route}, err)
	case path == "ingress" && r.Method == http.MethodDelete:
		var body struct {
			Hostname string `json:"hostname"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		err := unexposeCommonsIngress(globalCtx, body.Hostname)
		respond(w, map[string]any{"status": "removed", "hostname": body.Hostname}, err)
	default:
		http.NotFound(w, r)
	}
}

// --- MCP tool handlers ------------------------------------------------------

func (a *App) toolProfileUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := stringArg(args, "username")
	domain := stringArg(args, "domain")
	if domain == "" {
		domain = ctx.Config()["default_domain"]
	}
	return dbUpsertProfile(ctx.AppDB(), username, domain, stringArg(args, "display_name"), stringArg(args, "summary"))
}

func (a *App) toolPostCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createPost(ctx.AppDB(), stringArg(args, "username"), stringArg(args, "body"))
}

func (a *App) toolTimelineRead(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return dbListPosts(ctx.AppDB(), stringArg(args, "username"), intArg(args, "limit", 50))
}

func (a *App) toolFollowAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return addFollowByUsername(ctx.AppDB(), stringArg(args, "username"), stringArg(args, "remote_actor"), stringArg(args, "remote_inbox"), stringArg(args, "remote_name"))
}

func (a *App) toolBlockAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return addBlockByUsername(ctx.AppDB(), stringArg(args, "username"), stringArg(args, "target"), stringArg(args, "kind"), stringArg(args, "reason"))
}

func (a *App) toolExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return exportData(ctx.AppDB(), stringArg(args, "username"))
}

func (a *App) toolDomainExpose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return exposeCommonsIngress(ctx, stringArg(args, "hostname"), stringArg(args, "project_id"), boolArg(args, "allow_http"))
}

func (a *App) toolDomainUnexpose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := unexposeCommonsIngress(ctx, stringArg(args, "hostname")); err != nil {
		return nil, err
	}
	return map[string]any{"status": "removed", "hostname": stringArg(args, "hostname")}, nil
}

func exposeCommonsIngress(ctx *sdk.AppCtx, hostname, projectID string, allowHTTP bool) (*sdk.IngressRoute, error) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return nil, errors.New("hostname required")
	}
	if projectID == "" && ctx != nil {
		projectID = ctx.CurrentProject()
	}
	target := "app://commons"
	if projectID != "" {
		target += "?project_id=" + url.QueryEscape(projectID)
	}
	return ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    target,
		ProjectID: projectID,
		OwnerKind: "commons",
		AllowHTTP: allowHTTP,
		TLSMode:   "auto",
	})
}

func unexposeCommonsIngress(ctx *sdk.AppCtx, hostname string) error {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return errors.New("hostname required")
	}
	return ctx.PlatformAPI().UnexposeIngress(hostname)
}

// --- DB and federation logic ------------------------------------------------

func dbUpsertProfile(db *sql.DB, username, domain, displayName, summary string) (*Profile, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernameRE.MatchString(username) {
		return nil, errors.New("username must be 2-63 characters and contain only letters, numbers, _, . or -")
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, errors.New("domain required")
	}
	base := baseURL(domain)
	actorURL := base + "/users/" + url.PathEscape(username)
	existing, err := dbProfileByUsername(db, username)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if existing != nil {
		_, err = db.Exec(`UPDATE commons_profiles SET display_name=?, summary=?, domain=?, actor_url=?, inbox_url=?, outbox_url=?, updated_at=? WHERE id=?`,
			displayName, summary, domain, actorURL, actorURL+"/inbox", actorURL+"/outbox", now, existing.ID)
		if err != nil {
			return nil, err
		}
		return dbProfileByUsername(db, username)
	}
	pub, priv, err := generateKeyPair()
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`INSERT INTO commons_profiles (username, display_name, summary, domain, actor_url, inbox_url, outbox_url, public_key_pem, private_key_pem, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		username, displayName, summary, domain, actorURL, actorURL+"/inbox", actorURL+"/outbox", pub, priv, now, now)
	if err != nil {
		return nil, err
	}
	return dbProfileByUsername(db, username)
}

func dbProfileByUsername(db *sql.DB, username string) (*Profile, error) {
	var p Profile
	err := db.QueryRow(`SELECT id, username, display_name, summary, domain, actor_url, inbox_url, outbox_url, public_key_pem, private_key_pem, created_at, updated_at
		FROM commons_profiles WHERE username=?`, strings.ToLower(username)).
		Scan(&p.ID, &p.Username, &p.DisplayName, &p.Summary, &p.Domain, &p.ActorURL, &p.InboxURL, &p.OutboxURL, &p.PublicKey, &p.PrivateKey, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func dbProfileByActor(db *sql.DB, actor string) (*Profile, error) {
	var username string
	err := db.QueryRow(`SELECT username FROM commons_profiles WHERE actor_url=?`, actor).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbProfileByUsername(db, username)
}

func dbListProfiles(db *sql.DB) ([]Profile, error) {
	rows, err := db.Query(`SELECT id, username, display_name, summary, domain, actor_url, inbox_url, outbox_url, public_key_pem, created_at, updated_at FROM commons_profiles ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.Username, &p.DisplayName, &p.Summary, &p.Domain, &p.ActorURL, &p.InboxURL, &p.OutboxURL, &p.PublicKey, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func createPost(db *sql.DB, username, body string) (*Post, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("body required")
	}
	p, err := dbProfileByUsername(db, username)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("profile %q not found", username)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := randomID()
	objectID := baseURL(p.Domain) + "/objects/" + id
	activityID := p.ActorURL + "/activities/" + id
	obj := map[string]any{
		"id":           objectID,
		"type":         "Note",
		"attributedTo": p.ActorURL,
		"content":      plainToHTML(body),
		"published":    now,
		"to":           []string{publicAudience},
		"cc":           []string{p.ActorURL + "/followers"},
	}
	activity := map[string]any{
		"@context": activityContext,
		"id":       activityID,
		"type":     "Create",
		"actor":    p.ActorURL,
		"object":   obj,
		"to":       []string{publicAudience},
		"cc":       []string{p.ActorURL + "/followers"},
	}
	objectJSON := mustJSON(obj)
	activityJSON := mustJSON(activity)
	res, err := db.Exec(`INSERT INTO commons_posts (profile_id, body, visibility, activity_id, object_id, activity_json, object_json, published_at)
		VALUES (?, ?, 'public', ?, ?, ?, ?, ?)`, p.ID, body, activityID, objectID, activityJSON, objectJSON, now)
	if err != nil {
		return nil, err
	}
	postID, _ := res.LastInsertId()
	if err := queuePostToFollowers(db, p.ID, activityID, activity); err != nil {
		return nil, err
	}
	return &Post{ID: postID, ProfileID: p.ID, Username: p.Username, Body: body, Visibility: "public", ActivityID: activityID, ObjectID: objectID, ActivityJSON: activityJSON, ObjectJSON: objectJSON, PublishedAt: now, CreatedAt: now}, nil
}

func queuePostToFollowers(db *sql.DB, profileID int64, activityID string, activity map[string]any) error {
	rows, err := db.Query(`SELECT remote_inbox FROM commons_follows WHERE profile_id=? AND accepted=1`, profileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var inbox string
		if rows.Scan(&inbox) == nil && inbox != "" {
			if err := dbQueueDelivery(db, profileID, inbox, activityID, activity); err != nil {
				return err
			}
		}
	}
	return nil
}

func dbQueueDelivery(db *sql.DB, profileID int64, inbox, activityID string, payload map[string]any) error {
	_, err := db.Exec(`INSERT INTO commons_deliveries (profile_id, target_inbox, activity_id, payload_json) VALUES (?, ?, ?, ?)`,
		profileID, inbox, activityID, mustJSON(payload))
	return err
}

func dbListPosts(db *sql.DB, username string, limit int) ([]Post, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT p.id, p.profile_id, pr.username, p.body, p.visibility, p.activity_id, p.object_id, p.activity_json, p.object_json, p.published_at, p.created_at
		FROM commons_posts p JOIN commons_profiles pr ON pr.id=p.profile_id`
	args := []any{}
	if username != "" {
		q += ` WHERE pr.username=?`
		args = append(args, strings.ToLower(username))
	}
	q += ` ORDER BY p.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		var p Post
		if err := rows.Scan(&p.ID, &p.ProfileID, &p.Username, &p.Body, &p.Visibility, &p.ActivityID, &p.ObjectID, &p.ActivityJSON, &p.ObjectJSON, &p.PublishedAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func dbObjectJSON(db *sql.DB, pathID string) (string, error) {
	var raw string
	err := db.QueryRow(`SELECT object_json FROM commons_posts WHERE object_id LIKE ?`, "%/objects/"+pathID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

func addFollowByUsername(db *sql.DB, username, remoteActor, remoteInbox, remoteName string) (map[string]any, error) {
	p, err := dbProfileByUsername(db, username)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("profile %q not found", username)
	}
	if err := dbAddFollow(db, p.ID, remoteActor, remoteInbox, remoteName); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "profile": p.Username, "remote_actor": remoteActor}, nil
}

func dbAddFollow(db *sql.DB, profileID int64, remoteActor, remoteInbox, remoteName string) error {
	if remoteActor == "" || remoteInbox == "" {
		return errors.New("remote_actor and remote_inbox required")
	}
	_, err := db.Exec(`INSERT INTO commons_follows (profile_id, remote_actor, remote_inbox, remote_name, accepted)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(profile_id, remote_actor) DO UPDATE SET remote_inbox=excluded.remote_inbox, remote_name=excluded.remote_name, accepted=1`,
		profileID, remoteActor, remoteInbox, remoteName)
	return err
}

func addBlockByUsername(db *sql.DB, username, target, kind, reason string) (map[string]any, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("target required")
	}
	if kind != "actor" && kind != "domain" {
		return nil, errors.New("kind must be actor or domain")
	}
	var profileID any
	if username != "" {
		p, err := dbProfileByUsername(db, username)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("profile %q not found", username)
		}
		profileID = p.ID
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO commons_blocks (profile_id, target, kind, reason) VALUES (?, ?, ?, ?)`, profileID, target, kind, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "target": target, "kind": kind}, nil
}

func dbStoreInbox(db *sql.DB, profileID int64, actor, activityID, typ, raw, status string) error {
	var pid any
	if profileID > 0 {
		pid = profileID
	}
	_, err := db.Exec(`INSERT INTO commons_inbox_events (profile_id, remote_actor, activity_id, activity_type, raw_json, status) VALUES (?, ?, ?, ?, ?, ?)`,
		pid, actor, activityID, typ, raw, status)
	return err
}

func exportData(db *sql.DB, username string) (map[string]any, error) {
	profiles, err := dbListProfiles(db)
	if err != nil {
		return nil, err
	}
	posts, err := dbListPosts(db, username, 200)
	if err != nil {
		return nil, err
	}
	follows, err := rowsAsMaps(db, `SELECT f.* FROM commons_follows f JOIN commons_profiles p ON p.id=f.profile_id WHERE ?='' OR p.username=? ORDER BY f.created_at`, username, username)
	if err != nil {
		return nil, err
	}
	blocks, err := rowsAsMaps(db, `SELECT b.* FROM commons_blocks b LEFT JOIN commons_profiles p ON p.id=b.profile_id WHERE ?='' OR p.username=? ORDER BY b.created_at`, username, username)
	if err != nil {
		return nil, err
	}
	if username != "" {
		filtered := []Profile{}
		for _, p := range profiles {
			if p.Username == strings.ToLower(username) {
				filtered = append(filtered, p)
			}
		}
		profiles = filtered
	}
	return map[string]any{"schema": "commons-export/v1", "profiles": profiles, "posts": posts, "follows": follows, "blocks": blocks}, nil
}

func deliverPending(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT d.id, d.profile_id, d.target_inbox, d.payload_json, p.actor_url, p.private_key_pem
		FROM commons_deliveries d JOIN commons_profiles p ON p.id=d.profile_id
		WHERE d.status='pending' AND d.next_attempt_at <= CURRENT_TIMESTAMP
		ORDER BY d.created_at LIMIT 20`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID, ProfileID                     int64
		Inbox, Payload, Actor, PrivateKey string
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.ProfileID, &it.Inbox, &it.Payload, &it.Actor, &it.PrivateKey); err != nil {
			return err
		}
		items = append(items, it)
	}
	for _, it := range items {
		err := signedPost(ctx, it.Inbox, it.Actor+"#main-key", it.PrivateKey, []byte(it.Payload))
		if err == nil {
			_, _ = app.AppDB().Exec(`UPDATE commons_deliveries SET status='sent', attempts=attempts+1, last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=?`, it.ID)
			continue
		}
		_, _ = app.AppDB().Exec(`UPDATE commons_deliveries SET attempts=attempts+1, last_error=?, status=CASE WHEN attempts >= 9 THEN 'failed' ELSE 'pending' END, next_attempt_at=datetime('now', '+' || MIN(attempts + 1, 10) || ' minutes'), updated_at=CURRENT_TIMESTAMP WHERE id=?`, err.Error(), it.ID)
	}
	return nil
}

// --- ActivityPub documents and signing -------------------------------------

func actorDocument(p *Profile) map[string]any {
	return map[string]any{
		"@context":          []any{activityContext, "https://w3id.org/security/v1"},
		"id":                p.ActorURL,
		"type":              "Person",
		"preferredUsername": p.Username,
		"name":              displayName(p),
		"summary":           p.Summary,
		"inbox":             p.InboxURL,
		"outbox":            p.OutboxURL,
		"followers":         p.ActorURL + "/followers",
		"publicKey": map[string]any{
			"id":           p.ActorURL + "#main-key",
			"owner":        p.ActorURL,
			"publicKeyPem": p.PublicKey,
		},
	}
}

func signedPost(ctx context.Context, target, keyID, privatePEM string, payload []byte) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	priv, err := parsePrivateKey(privatePEM)
	if err != nil {
		return err
	}
	digestSum := sha256.Sum256(payload)
	digest := "SHA-256=" + base64.StdEncoding.EncodeToString(digestSum[:])
	date := time.Now().UTC().Format(http.TimeFormat)
	requestTarget := "post " + u.RequestURI()
	signing := "(request-target): " + requestTarget + "\n" +
		"host: " + u.Host + "\n" +
		"date: " + date + "\n" +
		"digest: " + digest
	hash := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hash[:])
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Accept", "application/activity+json")
	req.Header.Set("Date", date)
	req.Header.Set("Digest", digest)
	req.Header.Set("Signature", fmt.Sprintf(`keyId="%s",algorithm="rsa-sha256",headers="(request-target) host date digest",signature="%s"`, keyID, base64.StdEncoding.EncodeToString(sig)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delivery http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type remoteActor struct {
	Inbox string
	Name  string
}

func fetchRemoteActor(ctx context.Context, actorURL string) (remoteActor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURL, nil)
	if err != nil {
		return remoteActor{}, err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return remoteActor{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return remoteActor{}, fmt.Errorf("actor fetch http %d", resp.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&doc); err != nil {
		return remoteActor{}, err
	}
	inbox := stringField(doc, "inbox")
	if endpoints, ok := doc["endpoints"].(map[string]any); ok {
		if shared, _ := endpoints["sharedInbox"].(string); shared != "" {
			inbox = shared
		}
	}
	name := stringField(doc, "name")
	if name == "" {
		name = stringField(doc, "preferredUsername")
	}
	return remoteActor{Inbox: inbox, Name: name}, nil
}

// --- helpers ----------------------------------------------------------------

func profileForInboxObject(db *sql.DB, activity map[string]any) *Profile {
	obj := stringField(activity, "object")
	if obj == "" {
		if m, ok := activity["object"].(map[string]any); ok {
			obj = stringField(m, "id")
		}
	}
	p, _ := dbProfileByActor(db, obj)
	return p
}

func isBlocked(db *sql.DB, profileID int64, actor string) bool {
	host := ""
	if u, err := url.Parse(actor); err == nil {
		host = u.Host
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM commons_blocks WHERE (profile_id IS NULL OR profile_id=?) AND ((kind='actor' AND target=?) OR (kind='domain' AND target=?))`, profileID, actor, host).Scan(&n)
	return n > 0
}

func generateKeyPair() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	privDER := x509.MarshalPKCS1PrivateKey(key)
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER}))
	return pub, priv, nil
}

func parsePrivateKey(s string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("invalid private key pem")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func baseURL(domain string) string {
	domain = strings.TrimSpace(domain)
	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		return strings.TrimRight(domain, "/")
	}
	return "https://" + strings.TrimRight(domain, "/")
}

func normalizeDomain(domain string) string {
	u, err := url.Parse(baseURL(domain))
	if err == nil && u.Host != "" {
		return strings.ToLower(u.Host)
	}
	return strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://"))
}

func domainFromRequest(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "https"
		if strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1") {
			proto = "http"
		}
	}
	return proto + "://" + r.Host
}

func profilePageURL(p *Profile) string { return baseURL(p.Domain) + "/@" + url.PathEscape(p.Username) }

func displayName(p *Profile) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Username
}

func plainToHTML(s string) string {
	return strings.ReplaceAll(htmlEscape(s), "\n", "<br>")
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		return intFromString(v, def)
	}
	return def
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func intFromString(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func writeJSONContent(w http.ResponseWriter, ct string, v any) {
	w.Header().Set("Content-Type", ct)
	_ = json.NewEncoder(w).Encode(v)
}

func writeActivityJSON(w http.ResponseWriter, v any) {
	writeJSONContent(w, "application/activity+json", v)
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONContent(w, "application/json", v)
}

func rowsAsMaps(db *sql.DB, q string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[c] = string(v)
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out, nil
}

var globalCtx *sdk.AppCtx

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest            { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error     { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error     { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route           { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool              { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory    { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker             { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler { return w.app.EventHandlers() }

func main() { sdk.Run(&wrapApp{app: &App{}}) }
