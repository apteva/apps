package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
)

func validScope(scope string) bool {
	return scope != "" && len(scope) <= 512 && utf8.ValidString(scope) && strings.TrimSpace(scope) == scope && !strings.ContainsRune(scope, 0)
}

// Roots are collision-free and contain no MQTT wildcard characters. Scope ids
// are opaque; only the integration boundary maps them to SDK project ids.
func scopeTopicRoot(scope string) string {
	return "scopes/" + base64.RawURLEncoding.EncodeToString([]byte(scope)) + "/"
}

func topicScope(topic string) string {
	parts := strings.SplitN(topic, "/", 3)
	if len(parts) != 3 || parts[0] != "scopes" || parts[2] == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	scope := string(raw)
	if err != nil || !validScope(scope) || !strings.HasPrefix(topic, scopeTopicRoot(scope)) {
		return ""
	}
	return scope
}

func scopedFilter(scope, filter string) bool {
	return validScope(scope) && strings.HasPrefix(filter, scopeTopicRoot(scope)) &&
		len(filter) > len(scopeTopicRoot(scope)) && validMQTTFilter(filter)
}

func validMQTTFilter(filter string) bool {
	if len(filter) == 0 || len(filter) > 65535 || !utf8.ValidString(filter) || strings.ContainsRune(filter, 0) || !mqtt.IsValidFilter(filter, false) {
		return false
	}
	parts := strings.Split(filter, "/")
	for i, part := range parts {
		if strings.ContainsAny(part, "+#") && part != "+" && (part != "#" || i != len(parts)-1) {
			return false
		}
	}
	return true
}

func scopedTopic(scope, topic string) bool {
	return scopedFilter(scope, topic) && mqtt.IsValidFilter(topic, true)
}

func subscriptionFilter(filter string) string {
	if !strings.HasPrefix(filter, "$share/") {
		return filter
	}
	parts := strings.SplitN(filter, "/", 3)
	if len(parts) != 3 || parts[1] == "" || strings.ContainsAny(parts[1], "+#\x00") {
		return ""
	}
	return parts[2]
}

// A subscription must be a subset of an allowed filter, not merely a string
// that matches it: foo/+ must never authorize foo/#.
func filterCovered(allowed, requested string) bool {
	a, r := strings.Split(allowed, "/"), strings.Split(requested, "/")
	for i, part := range a {
		if part == "#" {
			return true
		}
		if i >= len(r) || r[i] == "#" {
			return false
		}
		if part != "+" && part != r[i] {
			return false
		}
	}
	return len(a) == len(r)
}

func authorizedFilter(u *MQTTUser, topic string, write bool) bool {
	if u == nil || !u.Enabled {
		return false
	}
	filters := u.AllowPublishTopics
	if !write {
		filters = u.AllowSubscribeTopics
		topic = subscriptionFilter(topic)
	}
	if !scopedFilter(u.ScopeID, topic) {
		return false
	}
	if write && !scopedTopic(u.ScopeID, topic) {
		return false
	}
	for _, filter := range filters {
		if scopedFilter(u.ScopeID, filter) && filterCovered(filter, topic) {
			return true
		}
	}
	return false
}

func managementScope(ctx *sdk.AppCtx) (string, error) {
	scope := ctx.CurrentProject()
	if !validScope(scope) {
		return "", errors.New("authenticated ownership scope required")
	}
	return scope, nil
}

func (a *App) toolScope(call context.Context) (string, error) {
	if os.Getenv("APTEVA_APP_TOKEN") == "" {
		return "", errors.New("authenticated management requires an install token")
	}
	fixed := a.ctx.CurrentProject()
	caller := sdk.CallerFrom(call)
	if caller == nil || !validScope(caller.ProjectID) {
		return "", errors.New("authenticated ownership scope required")
	}
	scope := caller.ProjectID
	if fixed != "" && scope != fixed {
		return "", errors.New("authenticated ownership scope required")
	}
	return scope, nil
}

type requestScopeKey struct{}

func (a *App) scopedHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("APTEVA_APP_TOKEN") == "" {
			httpErr(w, http.StatusForbidden, "authenticated management requires an install token")
			return
		}
		fixed := a.ctx.CurrentProject()
		scope := fixed
		principal, err := sdk.PrincipalFromRequest(r)
		if err != nil {
			httpErr(w, http.StatusForbidden, "invalid authenticated ownership scope")
			return
		}
		if principal != nil {
			scope = principal.ProjectID
		} else if operator, _ := strconv.ParseInt(r.Header.Get("X-Apteva-Operator-ID"), 10, 64); operator > 0 && os.Getenv("APTEVA_APP_TOKEN") != "" {
			// The SDK authenticates the install token; the platform scrubs and
			// replaces operator/project headers before forwarding operator requests.
			scope = r.Header.Get("X-Apteva-Project-ID")
		}
		if !validScope(scope) || (fixed != "" && scope != fixed) {
			httpErr(w, http.StatusForbidden, "authenticated ownership scope required")
			return
		}
		// SDK token authentication protects these routes. Global installs require
		// a signed principal or a gateway-authenticated operator/project pair.
		w.Header().Set("Cache-Control", "no-store")
		next(w, r.WithContext(context.WithValue(r.Context(), requestScopeKey{}, a.ctx.WithProject(scope))))
	}
}

func (a *App) requestContext(r *http.Request) *sdk.AppCtx {
	ctx, _ := r.Context().Value(requestScopeKey{}).(*sdk.AppCtx)
	return ctx
}

type authenticatedIdentity struct {
	revision        int64
	credentialID    int64
	scope, username string
}

func (a *App) clientIdentity(cl *mqtt.Client) *authenticatedIdentity {
	if cl == nil || cl.Net.Inline {
		return nil
	}
	value, ok := a.authenticatedClients.Load(cl)
	if !ok {
		return nil
	}
	return value.(*authenticatedIdentity)
}

func (a *App) currentClientUser(cl *mqtt.Client) *MQTTUser {
	identity := a.clientIdentity(cl)
	if identity == nil {
		return nil
	}
	u := a.cachedUser(identity.username)
	if u == nil || u.ID != identity.credentialID || u.Revision != identity.revision || u.ScopeID != identity.scope {
		return nil
	}
	return u
}

func (a *App) bindClientID(clientID string, u *MQTTUser) error {
	if clientID == "" {
		return errors.New("client id required")
	}
	result, err := a.ctx.AppDB().Exec(`INSERT INTO mqtt_client_owners(client_id, scope_id, credential_id)
        VALUES (?, ?, ?) ON CONFLICT(client_id) DO UPDATE SET client_id = excluded.client_id
        WHERE scope_id = excluded.scope_id AND credential_id = excluded.credential_id`, clientID, u.ScopeID, u.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("client id belongs to another identity")
	}
	return nil
}

type scopeCounters struct {
	droppedLogs, droppedEvents, rateLimited, authRejected, aclRejected, eventsEmitted atomic.Uint64
}

func (a *App) counters(scope string) *scopeCounters {
	v, _ := a.scopeCounters.LoadOrStore(scope, &scopeCounters{})
	return v.(*scopeCounters)
}

func requireTopicScope(ctx *sdk.AppCtx, topic string) error {
	scope, err := managementScope(ctx)
	if err != nil {
		return err
	}
	if !scopedTopic(scope, topic) {
		return fmt.Errorf("topic must be under %s", scopeTopicRoot(scope))
	}
	return nil
}

// Wire usernames include the scope token. This avoids both cross-scope name
// collisions and the existence oracle caused by globally unique local names.
func credentialName(scope, name string) (string, error) {
	if !validScope(scope) {
		return "", errors.New("ownership scope required")
	}
	prefix := base64.RawURLEncoding.EncodeToString([]byte(scope)) + ":"
	if strings.Contains(name, ":") {
		if !strings.HasPrefix(name, prefix) {
			return "", errors.New("credential not found")
		}
	} else {
		name = prefix + name
	}
	if len(name) <= len(prefix) || len(name) > 1024 || strings.ContainsRune(name, 0) {
		return "", errors.New("invalid username")
	}
	return name, nil
}
