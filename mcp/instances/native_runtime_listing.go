package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (b *nativeBackend) projectAccess(project string) error {
	if len(project) > 256 || strings.ContainsAny(project, "\x00\r\n") || b.projectID != "" && project != b.projectID {
		return errors.New("backend project mismatch")
	}
	return nil
}
func (b *nativeBackend) requestEnvelope(r nativeRequest, action string) error {
	if r.StateTransfer != nil {
		if err := b.validateStatePolicy(*r.StateTransfer); err != nil {
			return err
		}
	}
	if err := b.projectAccess(r.ProjectID); err != nil {
		return err
	}
	if action != "inspect-target" {
		return nativeEnvelope(r)
	}
	if r.Contract != nativeContract || r.Target.Type != "instances.instance" || len(r.Environment) > 0 {
		return errors.New("invalid target inspection request")
	}
	if len(r.OperationID) > 256 || len(r.IdempotencyKey) > 512 {
		return errors.New("invalid inspection identity")
	}
	return nil
}

type nativeTargetRow struct {
	Ref       nativeTarget `json:"ref"`
	Name      string       `json:"name"`
	Status    string       `json:"status"`
	Available bool         `json:"available"`
	Platform  string       `json:"platform,omitempty"`
	Region    string       `json:"region,omitempty"`
}
type nativeTargetPage struct {
	Targets    []nativeTargetRow `json:"targets"`
	NextCursor string            `json:"next_cursor,omitempty"`
}
type nativeTargetCursor struct {
	After int64  `json:"after"`
	Query string `json:"query"`
}

func (b *nativeBackend) listNativeTargets(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project_id")
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if b.projectAccess(project) != nil {
		http.Error(w, "backend project mismatch", 403)
		return
	}
	if len(search) > 200 {
		http.Error(w, "search exceeds limit", 400)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 200 {
			http.Error(w, "limit must be 1–200", 400)
			return
		}
		limit = n
	}
	key := nativeHash(nativeJSON([]any{b.installID, project, search}))
	var cursor nativeTargetCursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 512 {
			http.Error(w, "invalid cursor", 400)
			return
		}
		data, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || json.Unmarshal(data, &cursor) != nil || cursor.After < 0 || cursor.Query != key {
			http.Error(w, "invalid cursor", 400)
			return
		}
	}
	escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(search)
	rows, e := b.db.Query(`SELECT id,name,status,platform,region FROM instances WHERE id>? AND provider!='local' AND status='ready' AND platform IN ('','linux') AND ssh_user IN ('','root') AND (lower(name) LIKE ? ESCAPE '\' OR lower(region) LIKE ? ESCAPE '\') ORDER BY id LIMIT ?`, cursor.After, "%"+escaped+"%", "%"+escaped+"%", limit+1)
	if e != nil {
		http.Error(w, "target catalog unavailable", 503)
		return
	}
	defer rows.Close()
	page := nativeTargetPage{Targets: []nativeTargetRow{}}
	for rows.Next() {
		var id int64
		var name, status, platform, region string
		if rows.Scan(&id, &name, &status, &platform, &region) != nil {
			http.Error(w, "target catalog unavailable", 503)
			return
		}
		if len(page.Targets) == limit {
			last, _ := strconv.ParseInt(page.Targets[limit-1].Ref.ID, 10, 64)
			page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(nativeJSON(nativeTargetCursor{last, key})))
			break
		}
		// Architecture is checked by read-only inspect-target, never guessed from OS.
		page.Targets = append(page.Targets, nativeTargetRow{Ref: nativeTarget{b.installID, "instances.instance", strconv.FormatInt(id, 10)}, Name: name, Status: status, Available: true, Region: region})
	}
	if rows.Err() != nil {
		http.Error(w, "target catalog unavailable", 503)
		return
	}
	writeNative(w, page)
}
