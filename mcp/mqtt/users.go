package main

import (
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
	"golang.org/x/crypto/bcrypt"
)

// MQTTUser represents one row of mqtt_users, in-memory.
type MQTTUser struct {
	Revision             int64    `json:"-"`
	ScopeID              string   `json:"scope_id"`
	ID                   int64    `json:"id"`
	Username             string   `json:"username"`
	AllowPublishTopics   []string `json:"allow_publish"`
	AllowSubscribeTopics []string `json:"allow_subscribe"`
	Enabled              bool     `json:"enabled"`
	CreatedAt            string   `json:"created_at"`
	// passwordHash is intentionally not serialised.
	passwordHash string
}

// aclHook implements mqtt.Hook. We embed mqtt.HookBase so we only
// have to override the methods we care about and Provides() returns
// the right bitmask.
type aclHook struct {
	mqtt.HookBase
	app *App
}

func (h *aclHook) ID() string { return "apteva-acl" }

// Provides bitmasks the hooks the broker should dispatch to us.
// Order matters: declare every one we override.
func (h *aclHook) Provides(b byte) bool {
	switch b {
	case mqtt.OnConnectAuthenticate, mqtt.OnACLCheck, mqtt.OnPacketRead, mqtt.OnPacketEncode, mqtt.OnWill:
		return true
	}
	return false
}

func (h *aclHook) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	h.app.credentialsMu.RLock()
	defer h.app.credentialsMu.RUnlock()
	username := string(pk.Connect.Username)
	u := h.app.cachedUser(username)
	reject := func(reason string) bool {
		h.logACL(username, "connect", "", false, reason)
		h.app.authRejected.Add(1)
		if u != nil {
			h.app.counters(u.ScopeID).authRejected.Add(1)
		}
		return false
	}
	if u == nil || !u.Enabled || !validScope(u.ScopeID) {
		return reject("authentication refused")
	}
	if fixed := h.app.ctx.CurrentProject(); fixed != "" && u.ScopeID != fixed {
		return reject("authentication refused")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.passwordHash), pk.Connect.Password); err != nil {
		return reject("authentication refused")
	}
	if pk.Connect.WillFlag {
		if !authorizedFilter(u, pk.Connect.WillTopic, true) ||
			(pk.Connect.WillProperties.ResponseTopic != "" && !scopedTopic(u.ScopeID, pk.Connect.WillProperties.ResponseTopic)) ||
			pk.Connect.WillProperties.WillDelayInterval > 0 {
			return reject("will topic not permitted or delayed will unsupported")
		}
	}
	if err := h.app.bindClientID(cl.ID, u); err != nil {
		return reject("client id unavailable")
	}
	cl.Properties.Props.TopicAliasMaximum = 0
	h.app.authenticatedClients.Store(cl, &authenticatedIdentity{credentialID: u.ID, scope: u.ScopeID, username: u.Username, revision: u.Revision})
	if transport, ok := cl.Net.Conn.(*tlsAdmissionConn); ok {
		transport.authenticated()
	}
	h.logACL(username, "connect", "", true, "")
	return true
}

func (h *aclHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	if cl.Net.Inline {
		return true
	}
	h.app.credentialsMu.RLock()
	defer h.app.credentialsMu.RUnlock()
	u := h.app.currentClientUser(cl)
	allowed := authorizedFilter(u, topic, write)
	action := "subscribe"
	if write {
		action = "publish"
	}
	username := ""
	scope := ""
	if identity := h.app.clientIdentity(cl); identity != nil {
		username = identity.username
		scope = identity.scope
	}
	reason := ""
	if !allowed {
		reason = "topic not permitted"
		h.app.aclRejected.Add(1)
		h.app.counters(scope).aclRejected.Add(1)
	}
	h.logACL(username, action, topic, allowed, reason)
	return allowed
}

// Validate MQTT 5 response topics before processing. The broker's later ACL
// check authorizes the resolved publish topic, including incoming aliases.
// OnPublish errors are not fatal for all protocol/QoS combinations in Mochi.
func (h *aclHook) OnPacketRead(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if cl.Net.Inline || pk.FixedHeader.Type == packets.Connect {
		return pk, nil
	}
	h.app.credentialsMu.RLock()
	defer h.app.credentialsMu.RUnlock()
	u := h.app.currentClientUser(cl)
	if u == nil || !u.Enabled || (pk.FixedHeader.Type == packets.Publish && pk.Properties.ResponseTopic != "" && !scopedTopic(u.ScopeID, pk.Properties.ResponseTopic)) {
		// Hooks.OnPacketRead ignores ordinary errors. ErrRejectPacket is the
		// broker's explicit fatal read rejection, before routing or PUBACK.
		return pk, packets.ErrRejectPacket
	}
	if pk.FixedHeader.Type == packets.Publish && pk.Properties.TopicAliasFlag {
		// Mochi checks ACLs before resolving aliases. Resolve the client-local
		// alias here so both authorization and retained storage see a full name.
		alias := pk.Properties.TopicAlias
		maximum := uint16(65535)
		if h.app.broker != nil && h.app.broker.server != nil {
			maximum = h.app.broker.server.Options.Capabilities.TopicAliasMaximum
		}
		if alias == 0 || alias > maximum || cl.State.TopicAliases.Inbound == nil {
			return pk, packets.ErrRejectPacket
		}
		topic := pk.TopicName
		if topic == "" {
			topic = cl.State.TopicAliases.Inbound.Set(alias, "")
		}
		if !authorizedFilter(u, topic, true) {
			return pk, packets.ErrRejectPacket
		}
		cl.State.TopicAliases.Inbound.Set(alias, topic)
		pk.TopicName = topic
		// An inbound alias belongs only to its connection, never to a retained
		// message or a different subscriber's outbound connection.
		pk.Properties.TopicAlias, pk.Properties.TopicAliasFlag = 0, false
	}
	return pk, nil
}

// Also guard queued/inflight retransmissions, which bypass publishToClient's
// ACL check. Disable outbound topic aliases so the check sees the real topic.
func (h *aclHook) OnPacketEncode(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	if cl.Net.Inline || pk.FixedHeader.Type != packets.Publish {
		return pk
	}
	h.app.credentialsMu.RLock()
	defer h.app.credentialsMu.RUnlock()
	if !authorizedFilter(h.app.currentClientUser(cl), pk.TopicName, false) {
		cl.Stop(packets.ErrNotAuthorized)
		return packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Disconnect}, ProtocolVersion: pk.ProtocolVersion, ReasonCode: packets.ErrNotAuthorized.Code}
	}
	return pk
}

// Mochi routes wills outside OnPublish. Neutralize a revoked will rather than
// returning an error (Mochi logs OnWill errors and continues with the old will).
func (h *aclHook) OnWill(cl *mqtt.Client, will mqtt.Will) (mqtt.Will, error) {
	h.app.credentialsMu.RLock()
	defer h.app.credentialsMu.RUnlock()
	if !authorizedFilter(h.app.currentClientUser(cl), will.TopicName, true) {
		return mqtt.Will{}, nil
	}
	return will, nil
}

// mqttTopicMatch — left-anchored level-by-level match. Returns true
// when topic conforms to the filter. Standard MQTT 3.1.1 semantics:
// "+" matches exactly one level; "#" matches the remainder and must
// be the last segment.
func mqttTopicMatch(filter, topic string) bool {
	if strings.HasPrefix(topic, "$") && !strings.HasPrefix(filter, "$") {
		return false
	}
	fParts := strings.Split(filter, "/")
	tParts := strings.Split(topic, "/")
	for i, f := range fParts {
		if f == "#" {
			return true
		}
		if i >= len(tParts) {
			return false
		}
		if f == "+" {
			continue
		}
		if f != tParts[i] {
			return false
		}
	}
	return len(fParts) == len(tParts)
}

// ─── DB layer ───────────────────────────────────────────────────────

func getUser(db *sql.DB, scope, username string) (*MQTTUser, error) {
	username, nameErr := credentialName(scope, username)
	if nameErr != nil {
		return nil, nameErr
	}
	var u MQTTUser
	var pubJSON, subJSON string
	var en int
	err := db.QueryRow(
		`SELECT id, scope_id, revision, username, password_hash, allow_publish_topics_json,
		        allow_subscribe_topics_json, enabled, created_at
		   FROM mqtt_users WHERE scope_id = ? AND username = ?`, scope, username,
	).Scan(&u.ID, &u.ScopeID, &u.Revision, &u.Username, &u.passwordHash, &pubJSON, &subJSON, &en, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.Enabled = en == 1
	if err := json.Unmarshal([]byte(pubJSON), &u.AllowPublishTopics); err != nil {
		return nil, fmt.Errorf("decode publish ACL for %s: %w", username, err)
	}
	if err := json.Unmarshal([]byte(subJSON), &u.AllowSubscribeTopics); err != nil {
		return nil, fmt.Errorf("decode subscribe ACL for %s: %w", username, err)
	}
	return &u, nil
}

func listUsers(db *sql.DB, scope string) ([]MQTTUser, error) {
	if !validScope(scope) {
		return nil, fmt.Errorf("ownership scope required")
	}
	return readUsers(db, scope, false)
}

func readUsers(db *sql.DB, scope string, all bool) ([]MQTTUser, error) {
	query := `SELECT id, scope_id, revision, username, password_hash, allow_publish_topics_json,
 allow_subscribe_topics_json, enabled, created_at FROM mqtt_users`
	args := []any{}
	if !all {
		query += ` WHERE scope_id = ?`
		args = append(args, scope)
	}
	query += ` ORDER BY id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MQTTUser{}
	for rows.Next() {
		var u MQTTUser
		var pubJSON, subJSON string
		var en int
		if err := rows.Scan(&u.ID, &u.ScopeID, &u.Revision, &u.Username, &u.passwordHash, &pubJSON, &subJSON, &en, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Enabled = en == 1
		if err := json.Unmarshal([]byte(pubJSON), &u.AllowPublishTopics); err != nil {
			return nil, fmt.Errorf("decode publish ACL for %s: %w", u.Username, err)
		}
		if err := json.Unmarshal([]byte(subJSON), &u.AllowSubscribeTopics); err != nil {
			return nil, fmt.Errorf("decode subscribe ACL for %s: %w", u.Username, err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// The legacy name is create-only: credentials are never implicitly replaced.
func addUser(db *sql.DB, scope, username, password string, allowPub, allowSub []string) error {
	return createUser(db, scope, username, password, allowPub, allowSub)
}

func createUser(db *sql.DB, scope, username, password string, allowPub, allowSub []string) error {
	return writeUser(db, scope, username, password, allowPub, allowSub)
}

func writeUser(db *sql.DB, scope, username, password string, allowPub, allowSub []string) error {
	if !validScope(scope) {
		return fmt.Errorf("ownership scope required")
	}
	if allowPub == nil || allowSub == nil {
		return fmt.Errorf("explicit allow_publish and allow_subscribe arrays required; [] denies access")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}
	var nameErr error
	username, nameErr = credentialName(scope, username)
	if nameErr != nil {
		return nameErr
	}
	if password == "" {
		return fmt.Errorf("password required")
	}
	if len(password) > 72 {
		return fmt.Errorf("password must be at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	allowPub, err = normalizeScopedFilters(scope, allowPub)
	if err != nil {
		return err
	}
	allowSub, err = normalizeScopedFilters(scope, allowSub)
	if err != nil {
		return err
	}
	pubJSON, _ := json.Marshal(allowPub)
	subJSON, _ := json.Marshal(allowSub)
	query := `INSERT INTO mqtt_users
		   (scope_id, username, password_hash, allow_publish_topics_json,
		    allow_subscribe_topics_json, enabled)
		 VALUES (?, ?, ?, ?, ?, 1)`
	_, err = db.Exec(query,
		scope, username, string(hash), string(pubJSON), string(subJSON))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("username unavailable")
	}
	return err
}

func updateUserACL(db *sql.DB, scope, username string, allowPub, allowSub []string) error {
	var nameErr error
	username, nameErr = credentialName(scope, strings.TrimSpace(username))
	if nameErr != nil {
		return nameErr
	}

	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}
	if allowPub == nil && allowSub == nil {
		return fmt.Errorf("allow_publish or allow_subscribe required")
	}
	var (
		sets []string
		args []any
	)
	if allowPub != nil {
		normalized, err := normalizeScopedFilters(scope, allowPub)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(normalized)
		sets = append(sets, "allow_publish_topics_json = ?")
		args = append(args, string(encoded))
	}
	if allowSub != nil {
		normalized, err := normalizeScopedFilters(scope, allowSub)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(normalized)
		sets = append(sets, "allow_subscribe_topics_json = ?")
		args = append(args, string(encoded))
	}
	args = append(args, scope, username)
	result, err := db.Exec(`UPDATE mqtt_users SET revision=revision+1, `+strings.Join(sets, ", ")+` WHERE scope_id = ? AND username = ?`, args...)
	if err != nil {
		return err
	}
	return requireAffectedUser(result, username)
}

func rotateUserPassword(db *sql.DB, scope, username, password string) error {
	var nameErr error
	username, nameErr = credentialName(scope, strings.TrimSpace(username))
	if nameErr != nil {
		return nameErr
	}

	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}
	if password == "" {
		return fmt.Errorf("password required")
	}
	if len(password) > 72 {
		return fmt.Errorf("password must be at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := db.Exec(`UPDATE mqtt_users SET revision=revision+1, password_hash = ? WHERE scope_id = ? AND username = ?`, string(hash), scope, username)
	if err != nil {
		return err
	}
	return requireAffectedUser(result, username)
}

func requireAffectedUser(result sql.Result, username string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("mqtt user %q not found", username)
	}
	return nil
}

func normalizeTopicFilters(filters []string) ([]string, error) {
	out := make([]string, 0, len(filters))
	for _, raw := range filters {
		filter := strings.TrimSpace(raw)
		if !mqtt.IsValidFilter(filter, false) {
			return nil, fmt.Errorf("invalid MQTT topic filter %q", raw)
		}
		out = append(out, filter)
	}
	return out, nil
}

func (a *App) reloadUserCache() error {
	users, err := readUsers(a.ctx.AppDB(), "", true)
	if err != nil {
		return err
	}
	cache := make(map[string]*MQTTUser, len(users))
	for i := range users {
		u := users[i]
		cache[u.Username] = &u
	}
	a.usersMu.Lock()
	a.users = cache
	a.usersMu.Unlock()
	return nil
}

func (a *App) cachedUser(username string) *MQTTUser {
	a.usersMu.RLock()
	u := a.users[username]
	if u == nil {
		a.usersMu.RUnlock()
		return nil
	}
	copy := *u
	copy.AllowPublishTopics = append([]string(nil), u.AllowPublishTopics...)
	copy.AllowSubscribeTopics = append([]string(nil), u.AllowSubscribeTopics...)
	a.usersMu.RUnlock()
	return &copy
}

func deleteUser(db *sql.DB, scope, username string) error {
	var nameErr error
	username, nameErr = credentialName(scope, strings.TrimSpace(username))
	if nameErr != nil {
		return nameErr
	}

	result, err := db.Exec(`DELETE FROM mqtt_users WHERE scope_id = ? AND username = ?`, scope, username)
	if err != nil {
		return err
	}
	return requireAffectedUser(result, username)
}

func setUserEnabled(db *sql.DB, scope, username string, enabled bool) error {
	var nameErr error
	username, nameErr = credentialName(scope, strings.TrimSpace(username))
	if nameErr != nil {
		return nameErr
	}

	v := 0
	if enabled {
		v = 1
	}
	result, err := db.Exec(`UPDATE mqtt_users SET revision=revision+1, enabled = ? WHERE scope_id = ? AND username = ?`, v, scope, username)
	if err != nil {
		return err
	}
	return requireAffectedUser(result, username)
}

func randomPassword(n int) string {
	buf := make([]byte, n)
	if _, err := cryptorand.Read(buf); err != nil {
		// crypto/rand.Read shouldn't fail on a healthy host. Don't
		// risk a deterministic fallback — abort.
		panic("randomPassword: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:n]
}

// ─── ACL log ────────────────────────────────────────────────────────

func (h *aclHook) logACL(username, action, topic string, allowed bool, reason string) {
	scope := ""
	if u := h.app.cachedUser(username); u != nil {
		scope = u.ScopeID
	}
	record := aclRecord{scope: scope, username: username, action: action, topic: topic, allowed: allowed, reason: reason}
	select {
	case h.app.aclLogCh <- record:
	default:
		h.app.droppedLogs.Add(1)
		h.app.counters(scope).droppedLogs.Add(1)
	}
}

func normalizeScopedFilters(scope string, filters []string) ([]string, error) {
	normalized, err := normalizeTopicFilters(filters)
	if err != nil {
		return nil, err
	}
	for _, filter := range normalized {
		if !scopedFilter(scope, filter) {
			return nil, fmt.Errorf("ACL filters must be under %s", scopeTopicRoot(scope))
		}
	}
	return normalized, nil
}

func (a *App) revokeUser(username string) {
	if a.broker == nil {
		return
	}
	_ = a.broker.server.MutateSessions(func() {
		for _, cl := range a.broker.server.Clients.GetAll() {
			identity := a.clientIdentity(cl)
			if identity == nil || identity.username != username {
				continue
			}
			// Stop first: writing DISCONNECT to a slow peer must not delay revocation
			// while holding the credential lock. OnWill rechecks the revision after
			// this mutation releases that lock, so a revoked will is neutralized.
			cl.Stop(packets.ErrNotAuthorized)
			a.broker.server.UnsubscribeClient(cl)
			cl.ClearInflights()
			a.broker.server.Clients.Delete(cl.ID)
			a.authenticatedClients.Delete(cl)
		}
	})
}
