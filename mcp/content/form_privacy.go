package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func sensitiveFormKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
	for _, part := range []string{"password", "passwd", "passcode", "passphrase", "secret", "token", "authorization", "credential", "cookie"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "apikey" || key == "pin" || key == "otp"
}
func redactFormValue(value any, sensitive map[string]bool) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			if sensitiveFormKey(k) || sensitive[k] {
				out[k] = "[redacted]"
			} else {
				out[k] = redactFormValue(item, sensitive)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactFormValue(item, sensitive)
		}
		return out
	default:
		return value
	}
}
func passwordFieldNames(fields []any) map[string]bool {
	out := map[string]bool{}
	for _, raw := range fields {
		if f, ok := raw.(map[string]any); ok {
			if strings.EqualFold(asString(f["type"]), "password") {
				out[asString(f["name"])] = true
			}
		}
	}
	return out
}
func privateFormPayload(payload map[string]any, fields []any) map[string]any {
	return redactFormValue(payload, passwordFieldNames(fields)).(map[string]any)
}

// Persist action status only. Provider responses and errors can contain arbitrary
// credentials, including values without recognizable field names.
func privateFormResults(results []ActionResult) []ActionResult {
	out := make([]ActionResult, len(results))
	for i, r := range results {
		out[i] = ActionResult{App: r.App, Tool: r.Tool, OK: r.OK}
		if !r.OK {
			out[i].Error = "action failed"
		}
	}
	return out
}

// One-time upgrade cleanup. Read batches fully before writing, so the SDK's
// single SQLite connection is never held by nested queries.
func scrubHistoricalFormSecrets(db *sql.DB) error {
	var done int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_maintenance WHERE key='form_privacy_v1'`).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	sensitive := map[string]bool{}
	rows, err := db.Query(`SELECT body_blocks FROM posts WHERE body_blocks LIKE '%core/form%'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		doc, e := parseDocument(body)
		if e != nil {
			continue
		}
		collectFormBlocks(doc.Blocks, func(b Block) {
			fields, _ := b.Attrs["fields"].([]any)
			for k := range passwordFieldNames(fields) {
				sensitive[k] = true
			}
		})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for last := int64(0); ; {
		rows, err := db.Query(`SELECT id,payload,results FROM form_submissions WHERE id>? ORDER BY id LIMIT 200`, last)
		if err != nil {
			return err
		}
		type row struct {
			id               int64
			payload, results string
		}
		var batch []row
		for rows.Next() {
			var r row
			if err = rows.Scan(&r.id, &r.payload, &r.results); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, r := range batch {
			var payload any
			var results []ActionResult
			if json.Unmarshal([]byte(r.payload), &payload) != nil {
				payload = map[string]any{}
			}
			_ = json.Unmarshal([]byte(r.results), &results)
			p, _ := json.Marshal(redactFormValue(payload, sensitive))
			rs, _ := json.Marshal(privateFormResults(results))
			_, err = tx.Exec(`UPDATE form_submissions SET payload=?,results=?,error=CASE WHEN error<>'' THEN 'submission failed' ELSE '' END WHERE id=?`, string(p), string(rs), r.id)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("redact submission: %w", err)
			}
			last = r.id
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO content_maintenance(key) VALUES ('form_privacy_v1')`)
	return err
}
