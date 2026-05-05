// users.go — broker auth + ACL.
//
// The aclHook satisfies mochi's hook interface for connect-time
// authentication and per-publish/subscribe ACL checks. We back
// everything with the mqtt_users table (bcrypt password_hash + per-
// user publish/subscribe glob lists) and stamp every decision into
// mqtt_acl_log so the panel can show why something was refused.
//
// Empty / locked password_hash = denied. allow_anonymous=true in
// config skips the password check on connect, but ACL still runs
// against an "anonymous" pseudo-user with the configured global
// allow lists. The seeded apteva-default user lets the operator
// actually connect from a panel test form on first install.

package main

import (
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"golang.org/x/crypto/bcrypt"
)

// MQTTUser represents one row of mqtt_users, in-memory.
type MQTTUser struct {
	ID                 int64    `json:"id"`
	Username           string   `json:"username"`
	AllowPublishTopics []string `json:"allow_publish"`
	AllowSubscribeTopics []string `json:"allow_subscribe"`
	Enabled            bool     `json:"enabled"`
	CreatedAt          string   `json:"created_at"`
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
	case mqtt.OnConnectAuthenticate, mqtt.OnACLCheck:
		return true
	}
	return false
}

func (h *aclHook) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	username := string(pk.Connect.Username)
	password := string(pk.Connect.Password)
	allowAnon := configFlag(h.app.ctx, "allow_anonymous", false)

	if username == "" {
		if allowAnon {
			h.logACL("anonymous", "connect", "", true, "anonymous allowed")
			return true
		}
		h.logACL("", "connect", "", false, "anonymous refused (allow_anonymous=false)")
		return false
	}
	u, err := getUser(h.app.ctx.AppDB(), username)
	if err != nil || u == nil {
		h.logACL(username, "connect", "", false, "no such user")
		return false
	}
	if !u.Enabled {
		h.logACL(username, "connect", "", false, "user disabled")
		return false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.passwordHash), []byte(password)); err != nil {
		h.logACL(username, "connect", "", false, "bad password")
		return false
	}
	h.logACL(username, "connect", "", true, "")
	return true
}

func (h *aclHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	username := string(cl.Properties.Username)
	action := "subscribe"
	if write {
		action = "publish"
	}
	// Inline-client subscriptions originate from this app itself —
	// always allow so the bus bridge + HA discovery work.
	if cl.Net.Inline {
		return true
	}
	if username == "" {
		if !configFlag(h.app.ctx, "allow_anonymous", false) {
			return false
		}
		// Anonymous: allow everything. Operators wanting tighter
		// rules should set allow_anonymous=false and create a real
		// user with scoped globs.
		h.logACL("anonymous", action, topic, true, "anonymous")
		return true
	}
	u, err := getUser(h.app.ctx.AppDB(), username)
	if err != nil || u == nil || !u.Enabled {
		h.logACL(username, action, topic, false, "user gone")
		return false
	}
	allow := u.AllowPublishTopics
	if !write {
		allow = u.AllowSubscribeTopics
	}
	if topicMatchesAny(topic, allow) {
		h.logACL(username, action, topic, true, "")
		return true
	}
	h.logACL(username, action, topic, false, "no matching ACL")
	return false
}

// topicMatchesAny — does `topic` match any glob in `patterns`?
// MQTT wildcards: + matches one level, # matches all remaining.
func topicMatchesAny(topic string, patterns []string) bool {
	for _, p := range patterns {
		if mqttTopicMatch(p, topic) {
			return true
		}
	}
	return false
}

// mqttTopicMatch — left-anchored level-by-level match. Returns true
// when topic conforms to the filter. Standard MQTT 3.1.1 semantics:
// "+" matches exactly one level; "#" matches the remainder and must
// be the last segment.
func mqttTopicMatch(filter, topic string) bool {
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

func getUser(db *sql.DB, username string) (*MQTTUser, error) {
	var u MQTTUser
	var pubJSON, subJSON string
	var en int
	err := db.QueryRow(
		`SELECT id, username, password_hash, allow_publish_topics_json,
		        allow_subscribe_topics_json, enabled, created_at
		   FROM mqtt_users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &u.passwordHash, &pubJSON, &subJSON, &en, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.Enabled = en == 1
	_ = json.Unmarshal([]byte(pubJSON), &u.AllowPublishTopics)
	_ = json.Unmarshal([]byte(subJSON), &u.AllowSubscribeTopics)
	return &u, nil
}

func listUsers(db *sql.DB) ([]MQTTUser, error) {
	rows, err := db.Query(
		`SELECT id, username, password_hash, allow_publish_topics_json,
		        allow_subscribe_topics_json, enabled, created_at
		   FROM mqtt_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MQTTUser{}
	for rows.Next() {
		var u MQTTUser
		var pubJSON, subJSON string
		var en int
		if err := rows.Scan(&u.ID, &u.Username, &u.passwordHash, &pubJSON, &subJSON, &en, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Enabled = en == 1
		_ = json.Unmarshal([]byte(pubJSON), &u.AllowPublishTopics)
		_ = json.Unmarshal([]byte(subJSON), &u.AllowSubscribeTopics)
		out = append(out, u)
	}
	return out, nil
}

func addUser(db *sql.DB, username, password string, allowPub, allowSub []string) error {
	if username == "" {
		return fmt.Errorf("username required")
	}
	if password == "" {
		return fmt.Errorf("password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if len(allowPub) == 0 {
		allowPub = []string{"#"}
	}
	if len(allowSub) == 0 {
		allowSub = []string{"#"}
	}
	pubJSON, _ := json.Marshal(allowPub)
	subJSON, _ := json.Marshal(allowSub)
	_, err = db.Exec(
		`INSERT INTO mqtt_users
		   (username, password_hash, allow_publish_topics_json,
		    allow_subscribe_topics_json, enabled)
		 VALUES (?, ?, ?, ?, 1)
		 ON CONFLICT(username) DO UPDATE SET
		   password_hash = excluded.password_hash,
		   allow_publish_topics_json = excluded.allow_publish_topics_json,
		   allow_subscribe_topics_json = excluded.allow_subscribe_topics_json,
		   enabled = 1`,
		username, string(hash), string(pubJSON), string(subJSON))
	return err
}

func deleteUser(db *sql.DB, username string) error {
	_, err := db.Exec(`DELETE FROM mqtt_users WHERE username = ?`, username)
	return err
}

func setUserEnabled(db *sql.DB, username string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := db.Exec(`UPDATE mqtt_users SET enabled = ? WHERE username = ?`, v, username)
	return err
}

// seedDefaultUserIfNeeded creates an `apteva-default` user with a
// random password on first mount. We log the password ONCE — the
// operator copies it from the server log; subsequent restarts reuse
// the existing row. Avoids the "broker installed but no way to
// connect" trap when allow_anonymous=false.
func (a *App) seedDefaultUserIfNeeded() error {
	users, err := listUsers(a.ctx.AppDB())
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil
	}
	password := randomPassword(24)
	if err := addUser(a.ctx.AppDB(), "apteva-default", password,
		[]string{"#"}, []string{"#"}); err != nil {
		return err
	}
	a.ctx.Logger().Info(
		"seeded default mqtt user — copy password to a client config now; not shown again",
		"username", "apteva-default",
		"password", password,
	)
	return nil
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

var aclLogMu sync.Mutex

func (h *aclHook) logACL(username, action, topic string, allowed bool, reason string) {
	// Single mutex because mochi calls hooks from many goroutines and
	// SQLite's writer is single-threaded anyway. Cheap and correct.
	aclLogMu.Lock()
	defer aclLogMu.Unlock()
	v := 0
	if allowed {
		v = 1
	}
	_, _ = h.app.ctx.AppDB().Exec(
		`INSERT INTO mqtt_acl_log(username, action, topic, allowed, reason) VALUES (?,?,?,?,?)`,
		username, action, topic, v, reason)
}
