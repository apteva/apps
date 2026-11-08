package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
)

// durableStore owns the transactional boundary. A failed write latches unhealthy
// until restart; continuing from mutated volatile state could lose obligations.
type durableStore struct {
	app                                    *App
	mu                                     sync.Mutex
	failed                                 atomic.Bool
	ready                                  atomic.Bool
	maxMessages, maxBytes, maxSessionBytes int
}

func newDurableStore(a *App) (*durableStore, error) {
	db := a.ctx.AppDB()
	// PRAGMAs are per connection. One connection makes FULL synchronous writes and
	// transactions apply consistently, including migrations and consumer leases.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA synchronous=FULL`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return nil, err
	}
	if _, err := strictInt(a.ctx, "journal_retention_hours", 168, 1, 87600); err != nil {
		return nil, err
	}
	d := &durableStore{app: a}
	var err error
	d.maxMessages, err = strictInt(a.ctx, "max_journal_messages_per_scope", 100000, 1, 10000000)
	if err != nil {
		return nil, err
	}
	d.maxBytes, err = strictInt(a.ctx, "max_journal_bytes_per_scope", 67108864, 1024, 2147483647)
	if err != nil {
		return nil, err
	}
	d.maxSessionBytes, err = strictInt(a.ctx, "max_session_bytes_per_scope", 67108864, 1024, 2147483647)
	if err != nil {
		return nil, err
	}
	return d, nil
}
func (d *durableStore) Failure(s *mqtt.Server, err error) error { return d.fault(err, s) }

func (d *durableStore) fault(err error, s *mqtt.Server) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mqtt.ErrDurableCapacity) || errors.Is(err, packets.ErrNotAuthorized) {
		return err
	}
	if !d.failed.Swap(true) {
		d.app.ctx.Logger().Error("MQTT durable storage unavailable; restart after repair", "error", err.Error())
	}
	d.ready.Store(false)
	if s != nil {
		for _, cl := range s.Clients.GetAll() {
			if !cl.Net.Inline {
				cl.Stop(errors.New("durable storage unavailable"))
			}
		}
	}
	return fmt.Errorf("MQTT durable storage unavailable: %w", err)
}
func (d *durableStore) Commit(s *mqtt.Server, sender *mqtt.Client, pk *packets.Packet, plan []mqtt.Delivery) (err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed.Load() {
		return errors.New("MQTT durable storage unavailable; restart after repair")
	}
	quotaScope := ""
	defer func() {
		var q *quotaError
		if errors.As(err, &q) && d.app.usage != nil && quotaScope != "" {
			if e := d.app.usage.rejected(quotaScope); e != nil {
				err = e
			}
		}
		err = d.fault(err, s)
	}()
	tx, err := d.app.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if pk != nil {
		scope := topicScope(pk.TopicName)
		quotaScope = scope
		if !validScope(scope) {
			return errors.New("durable ownership scope required")
		}
		username := ""
		if !sender.Net.Inline {
			identity := d.app.clientIdentity(sender)
			if identity == nil {
				return errors.New("authenticated identity required")
			}
			var valid int
			if err = tx.QueryRow(`SELECT count(*) FROM mqtt_users WHERE id=? AND scope_id=? AND revision=? AND enabled=1`, identity.credentialID, scope, identity.revision).Scan(&valid); err != nil {
				return err
			}
			if valid != 1 {
				return packets.ErrNotAuthorized
			}
			username = identity.username
		}
		oldCount, oldBytes := int64(0), int64(0)
		if d.app.usage != nil {
			if err = d.app.usage.accepted(tx, scope, *pk); err != nil {
				return err
			}
			if pk.FixedHeader.Retain {
				oldCount, oldBytes, err = retainedUsage(tx, scope)
				if err != nil {
					return err
				}
			}
		}
		data, e := json.Marshal(pk)
		if e != nil {
			return e
		}
		var count, size int
		if err = tx.QueryRow(`SELECT coalesce((SELECT messages FROM mqtt_journal_usage WHERE scope_id=?),0),coalesce((SELECT bytes FROM mqtt_journal_usage WHERE scope_id=?),0)`, scope, scope).Scan(&count, &size); err != nil {
			return err
		}
		if count >= d.maxMessages || size+len(data) > d.maxBytes {
			return mqtt.ErrDurableCapacity
		}
		if _, err = tx.Exec(`INSERT INTO mqtt_journal(message_id,scope_id,topic,packet,username,accepted_at) VALUES(?,?,?,?,?,?)`, mqtt.MessageID(*pk), scope, pk.TopicName, data, username, now); err != nil {
			return err
		}
		if pk.FixedHeader.Retain {
			if len(pk.Payload) == 0 {
				_, err = tx.Exec(`DELETE FROM mqtt_retained WHERE scope_id=? AND topic=?`, scope, pk.TopicName)
			} else {
				props, e := json.Marshal(storageProperties(pk.Properties))
				if e != nil {
					return e
				}
				_, err = tx.Exec(`INSERT INTO mqtt_retained(scope_id,topic,payload,qos,properties,updated_at,expires_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP,?) ON CONFLICT(topic) DO UPDATE SET payload=excluded.payload,qos=excluded.qos,properties=excluded.properties,updated_at=CURRENT_TIMESTAMP,expires_at=excluded.expires_at`, scope, pk.TopicName, pk.Payload, pk.FixedHeader.Qos, string(props), pk.Expiry)
			}
			if err != nil {
				return err
			}
			if d.app.usage != nil {
				if err = d.app.usage.retained(tx, scope, oldCount, oldBytes); err != nil {
					return err
				}
			}
		}
	}
	// Snapshot replacements and deletion of stale sessions share the acceptance
	// transaction. Unchanged rows aren't rewritten on every publish.
	stale := map[string]bool{}
	rows, e := tx.Query(`SELECT client_id FROM mqtt_sessions`)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		stale[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	sizes := map[string]int{}
	for _, snapshot := range s.Sessions(plan) {
		cl, _ := s.Clients.Get(snapshot.ID)
		identity := d.app.clientIdentity(cl)
		if identity == nil {
			continue
		}
		var valid int
		if err = tx.QueryRow(`SELECT count(*) FROM mqtt_users WHERE id=? AND scope_id=? AND revision=? AND enabled=1`, identity.credentialID, identity.scope, identity.revision).Scan(&valid); err != nil {
			return err
		}
		if valid != 1 {
			cl.Stop(packets.ErrNotAuthorized)
			continue
		}
		data, e := json.Marshal(snapshot)
		if e != nil {
			return e
		}
		sizes[identity.scope] += len(data)
		if pk != nil && sizes[identity.scope] > d.maxSessionBytes {
			return mqtt.ErrDurableCapacity
		}
		delete(stale, snapshot.ID)
		connected := 0
		if snapshot.Disconnected == 0 {
			connected = 1
		}
		if _, err = tx.Exec(`INSERT INTO mqtt_sessions(client_id,scope_id,credential_id,revision,snapshot,last_seen,connected) VALUES(?,?,?,?,?,?,?) ON CONFLICT(client_id) DO UPDATE SET scope_id=excluded.scope_id,credential_id=excluded.credential_id,revision=excluded.revision,snapshot=excluded.snapshot,last_seen=excluded.last_seen,connected=excluded.connected WHERE mqtt_sessions.snapshot!=excluded.snapshot OR (excluded.connected=1 AND mqtt_sessions.last_seen<excluded.last_seen)`, snapshot.ID, identity.scope, identity.credentialID, identity.revision, data, now, connected); err != nil {
			return err
		}
	}
	for id := range stale {
		if _, err = tx.Exec(`DELETE FROM mqtt_sessions WHERE client_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sessionLifetime(s *mqtt.Server, v mqtt.Session) int64 {
	if v.Properties.ProtocolVersion >= 5 {
		return min(int64(v.Properties.Props.SessionExpiryInterval), int64(s.Options.Capabilities.MaximumSessionExpiryInterval))
	}
	if v.Properties.Clean {
		return 0
	}
	return int64(s.Options.Capabilities.MaximumSessionExpiryInterval)
}
func (d *durableStore) Restore(s *mqtt.Server) (err error) {
	defer func() { err = d.fault(err, s) }()
	rows, err := d.app.ctx.AppDB().Query(`SELECT client_id,scope_id,credential_id,revision,snapshot,last_seen FROM mqtt_sessions ORDER BY client_id`)
	if err != nil {
		return err
	}
	type record struct {
		v        mqtt.Session
		identity authenticatedIdentity
		seen     int64
	}
	records := []record{}
	for rows.Next() {
		var id, scope string
		var credential, revision, seen int64
		var data []byte
		if err = rows.Scan(&id, &scope, &credential, &revision, &data, &seen); err != nil {
			rows.Close()
			return err
		}
		var v mqtt.Session
		if err = json.Unmarshal(data, &v); err != nil {
			rows.Close()
			return err
		}
		if id != v.ID || !validScope(scope) {
			rows.Close()
			return errors.New("invalid session ownership")
		}
		records = append(records, record{v, authenticatedIdentity{credentialID: credential, revision: revision, scope: scope, username: string(v.Properties.Username)}, seen})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	expired := []string{}
	for _, r := range records {
		u := d.app.cachedUser(r.identity.username)
		if u == nil || !u.Enabled || u.ID != r.identity.credentialID || u.Revision != r.identity.revision || u.ScopeID != r.identity.scope {
			continue
		}
		active := r.v.Disconnected == 0
		if active {
			r.v.Disconnected = r.seen
		} else if !r.v.PendingWill {
			r.v.Properties.Will.Flag = 0
		}
		lifetime := sessionLifetime(s, r.v)
		if lifetime == 0 || r.v.Disconnected+lifetime <= now {
			r.v.Subscriptions = nil
			r.v.Outgoing = nil
			r.v.Incoming = nil
			if !r.v.PendingWill {
				expired = append(expired, r.v.ID)
			}
		}
		kept := []packets.Packet{}
		for _, pk := range r.v.Outgoing {
			if pk.FixedHeader.Type != packets.Publish || pk.Expiry == 0 || pk.Expiry > now {
				kept = append(kept, pk)
			}
		}
		r.v.Outgoing = kept
		cl := s.RestoreSession(r.v)
		d.app.authenticatedClients.Store(cl, &r.identity)
		// Revalidate persisted subscriptions and queued data against current policy.
		for _, sub := range r.v.Subscriptions {
			if !authorizedFilter(u, sub.Filter, false) {
				return errors.New("persisted subscription outside credential permissions")
			}
		}
		for _, pk := range r.v.Outgoing {
			if pk.FixedHeader.Type == packets.Publish && !authorizedFilter(u, pk.TopicName, false) {
				return errors.New("persisted delivery outside credential permissions")
			}
		}
	}
	if err = s.RecoverWills(); err != nil {
		return err
	}
	for _, id := range expired {
		s.Clients.Delete(id)
	}
	if err = d.Commit(s, nil, nil, nil); err != nil {
		return err
	}
	d.ready.Store(true)
	return nil
}

// Consumers pin journal history. Unconsumed messages are never evicted to make
// room: storage exhaustion is explicit backpressure. Expired MQTT messages stay
// readable as historical application records, but aren't redelivered by MQTT.
func (a *App) pruneJournal() error {
	hours := clampConfigInt(a.ctx, "journal_retention_hours", 168, 1, 87600)
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	// Only delete a contiguous prefix per scope, so every replay gap is detectable.
	if _, err = tx.Exec(`INSERT INTO mqtt_journal_floor(scope_id,seq) SELECT j.scope_id,max(j.seq) FROM mqtt_journal j WHERE j.accepted_at<? AND j.seq<coalesce((SELECT min(x.seq) FROM mqtt_journal x WHERE x.scope_id=j.scope_id AND x.accepted_at>=?),9223372036854775807) AND j.seq<=coalesce((SELECT min(cursor) FROM mqtt_consumers c WHERE c.scope_id=j.scope_id),j.seq) GROUP BY j.scope_id ON CONFLICT(scope_id) DO UPDATE SET seq=max(seq,excluded.seq)`, cutoff, cutoff); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM mqtt_journal WHERE seq<=coalesce((SELECT seq FROM mqtt_journal_floor f WHERE f.scope_id=mqtt_journal.scope_id),0)`); err != nil {
		return err
	}
	return tx.Commit()
}

// Compile-time guarantee that the app implements the local core boundary.
var _ mqtt.Persistence = (*durableStore)(nil)
