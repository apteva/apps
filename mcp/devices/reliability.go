package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrCursorExpired = errors.New("cursor expired; restart from retained history")
var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("idempotency conflict")
var ErrLimit = errors.New("resource limit exceeded")

func (a *App) topicRoot() string {
	return "scopes/" + base64.RawURLEncoding.EncodeToString([]byte(a.projectID)) + "/"
}
func (a *App) wireUsername(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(a.projectID)) + ":" + id
}
func (a *App) authorizeCaller(call context.Context) error {
	c := sdk.CallerFrom(call)
	if os.Getenv("APTEVA_APP_TOKEN") == "" || c == nil || c.ProjectID == "" || c.ProjectID != a.projectID {
		return errors.New("authenticated ownership scope required")
	}
	return nil
}
func (a *App) scopedHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("APTEVA_APP_TOKEN") == "" {
			writeError(w, 403, errors.New("authenticated management requires install token"))
			return
		}
		principal, err := sdk.PrincipalFromRequest(r)
		if err != nil || (principal != nil && principal.ProjectID != a.projectID) {
			writeError(w, 403, errors.New("invalid authenticated ownership scope"))
			return
		}
		if p := r.Header.Get("X-Apteva-Project-ID"); p != "" && p != a.projectID {
			writeError(w, 403, errors.New("ownership scope mismatch"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}
func fingerprint(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}
func newID(prefix string) string             { return prefix + strings.ReplaceAll(uuid.NewString(), "-", "") }
func (a *App) limit(key string, def int) int { return configInt(a.ctx, key, def, 1, limitMaximum(key)) }
func (a *App) migrateTransport() error {
	if _, err := a.ctx.AppDB().Exec(`INSERT INTO device_installation(singleton,project_id)VALUES(1,?) ON CONFLICT DO NOTHING`, a.projectID); err != nil {
		return err
	}
	var owner string
	if err := a.ctx.AppDB().QueryRow(`SELECT project_id FROM device_installation WHERE singleton=1`).Scan(&owner); err != nil {
		return err
	}
	if owner != a.projectID {
		return errors.New("database belongs to a different ownership scope")
	}

	var foreign int
	if err := a.ctx.AppDB().QueryRow(`SELECT (SELECT COUNT(*) FROM devices WHERE project_id<>?) + (SELECT COUNT(*) FROM device_operations WHERE project_id<>?)`, a.projectID, a.projectID).Scan(&foreign); err != nil {
		return err
	}
	if foreign > 0 {
		return errors.New("database belongs to a different ownership scope")
	}
	_, err := a.ctx.AppDB().Exec(`UPDATE devices SET mqtt_username='',topic_root=? WHERE project_id=? AND topic_root=''`, a.topicRoot(), a.projectID)
	if err != nil {
		return err
	}
	_, err = a.ctx.AppDB().Exec(`UPDATE devices SET mqtt_username=?||id WHERE project_id=? AND mqtt_username=''`, base64.RawURLEncoding.EncodeToString([]byte(a.projectID))+":", a.projectID)
	if err != nil {
		return err
	}
	rows, err := a.ctx.AppDB().Query(`SELECT id FROM device_commands WHERE fingerprint='' AND device_id IN(SELECT id FROM devices WHERE project_id=?)`, a.projectID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		c, err := getCommand(a.ctx.AppDB(), id)
		if err != nil {
			return err
		}
		hash := fingerprint(map[string]any{"operation": c.Operation, "target": c.Target, "arguments": c.Arguments, "timeout_ms": c.TimeoutMS})
		if _, err = a.ctx.AppDB().Exec(`UPDATE device_commands SET fingerprint=? WHERE id=?`, hash, id); err != nil {
			return err
		}
	}
	return nil
}

// Independent of the install token: keep this mode-0600 key with DB backups.
func (a *App) loadRecoveryKey() error {
	var dbPath string
	rows, err := a.ctx.AppDB().Query(`PRAGMA database_list`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n int
		var name, path string
		if err = rows.Scan(&n, &name, &path); err != nil {
			rows.Close()
			return err
		}
		if name == "main" {
			dbPath = path
		}
	}
	rows.Close()
	if dbPath == "" {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		a.secretKey = key
		return nil
	}
	keyPath := filepath.Join(filepath.Dir(dbPath), "devices-recovery.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		var secrets int
		if e := a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_operations WHERE secret_cipher<>''`).Scan(&secrets); e != nil {
			return e
		}
		if secrets > 0 {
			return errors.New("devices recovery key is missing; restore it with the database")
		}

		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		f, e := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(key)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	if len(key) != 32 {
		return errors.New("invalid devices recovery key")
	}
	a.secretKey = key
	return nil
}
func (a *App) cryptSecret(id, secret string, encrypt bool) (string, error) {
	b, err := aes.NewCipher(a.secretKey)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return "", err
	}
	if encrypt {
		n := make([]byte, g.NonceSize())
		if _, err = rand.Read(n); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(g.Seal(n, n, []byte(secret), []byte(id))), nil
	}
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(raw) < g.NonceSize() {
		return "", errors.New("invalid recovery ciphertext")
	}
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(id))
	return string(plain), err
}

type sqlWriter interface {
	Exec(string, ...any) (sql.Result, error)
}

func (a *App) event(w sqlWriter, deviceID, topic string, data map[string]any) error {
	id, at := newID("evt_"), nowText()
	body := cloneMap(data)
	body["event_id"] = id
	body["scope_id"] = a.projectID
	body["device_id"] = deviceID
	body["occurred_at"] = at
	body["schema_version"] = 1
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = w.Exec(`INSERT INTO device_outbox(event_id,project_id,device_id,topic,data_json,created_at)VALUES(?,?,?,?,?,?)`, id, a.projectID, deviceID, topic, string(raw), at)
	return err
}
func (a *App) usage(w sqlWriter, deviceID, measurement, source string, quantity int) error {
	_, err := w.Exec(`INSERT INTO device_usage(project_id,device_id,measurement,quantity,source_id,created_at)VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, a.projectID, deviceID, measurement, quantity, source, nowText())
	return err
}
func (a *App) flushOutbox(ctx context.Context) error {
	a.outboxMu.Lock()
	defer a.outboxMu.Unlock()
	rows, err := a.ctx.AppDB().Query(`SELECT seq,topic,data_json FROM device_outbox WHERE project_id=? AND delivered_at IS NULL ORDER BY seq LIMIT 100`, a.projectID)
	if err != nil {
		return err
	}
	type item struct {
		seq        int64
		topic, raw string
	}
	items := []item{}
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.seq, &x.topic, &x.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, x := range items {
		if err = a.ctx.EmitWithProjectAck(ctx, x.topic, a.projectID, jsonObject(x.raw)); err != nil {
			return err
		}
		if _, err = a.ctx.AppDB().Exec(`UPDATE device_outbox SET delivered_at=? WHERE seq=? AND project_id=?`, nowText(), x.seq, a.projectID); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) runReconciliation(ctx context.Context, _ *sdk.AppCtx) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-a.outboxWake:
			if err := a.reconcileOperations(); err != nil {
				a.ctx.Logger().Warn("devices lifecycle recovery", "err", err.Error())
			}
			if err := a.dispatchCommands(); err != nil {
				a.ctx.Logger().Warn("devices dispatch", "err", err.Error())
			}
			if err := a.flushOutbox(ctx); err != nil {
				a.ctx.Logger().Warn("devices outbox", "err", err.Error())
			}
		}
	}
}
func (a *App) scopedCommands(id, status string, limit int) ([]Command, error) {
	if id != "" {
		if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
			return nil, err
		}
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + commandColumns + ` FROM device_commands WHERE device_id IN (SELECT id FROM devices WHERE project_id=?)`
	args := []any{a.projectID}
	if id != "" {
		query += ` AND device_id=?`
		args = append(args, id)
	}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	rows, err := a.ctx.AppDB().Query(query+` ORDER BY created_at DESC,id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *App) wakeOutbox() {
	select {
	case a.outboxWake <- struct{}{}:
	default:
	}
}

func (a *App) notifyTerminalWaiters() {
	a.waitMu.Lock()
	ids := make([]string, 0, len(a.waiters))
	for id := range a.waiters {
		ids = append(ids, id)
	}
	a.waitMu.Unlock()
	for _, id := range ids {
		c, err := getCommand(a.ctx.AppDB(), id)
		if errors.Is(err, ErrNotFound) || (err == nil && commandTerminal(c.Status)) {
			a.notifyWaiters(id)
		}
	}
}

func limitMaximum(key string) int {
	switch key {
	case "max_device_payload_bytes":
		return 131072
	case "telemetry_retention_hours":
		return 87600
	case "event_retention_days", "usage_retention_days", "command_retention_days", "operation_retention_days":
		return 3650
	case "secret_recovery_hours":
		return 168
	case "max_clock_skew_seconds":
		return 86400
	case "command_timeout_ms":
		return 86400000
	default:
		return 1000000000
	}
}
func (a *App) validateLimits() error {
	for key := range a.activeLimits() {
		value := strings.TrimSpace(a.ctx.Config().Get(key))
		if value == "" {
			continue
		}
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || n > limitMaximum(key) {
			return fmt.Errorf("invalid %s: expected integer between 1 and %d", key, limitMaximum(key))
		}
	}
	if v := a.ctx.Config().Get("command_timeout_ms"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 500 || n > 86400000 {
			return errors.New("command_timeout_ms must be between 500 and 86400000")
		}
	}
	return nil
}
