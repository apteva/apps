package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var errConflict = errors.New("record changed; reload it before saving")

type validationError struct{ message string }

func (e validationError) Error() string { return e.message }
func invalid(s string) error            { return validationError{s} }

type ItemData struct {
	BrandID     string         `json:"brand_id"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Format      string         `json:"format"`
	Status      string         `json:"status"`
	Owner       string         `json:"owner"`
	Deadline    string         `json:"deadline"`
	PlannedAt   string         `json:"planned_at"`
	Approval    string         `json:"approval"`
	Reviewer    string         `json:"reviewer"`
	Campaign    string         `json:"campaign"`
	Sources     []string       `json:"sources"`
	Attachments []string       `json:"attachments"`
	Tags        []string       `json:"tags"`
	Fields      map[string]any `json:"fields"`
	Archived    bool           `json:"archived"`
}
type Item struct {
	ItemData
	ID        int64  `json:"id"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
type ReleaseData struct {
	Channel     string         `json:"channel"`
	PlannedAt   string         `json:"planned_at"`
	PublishedAt string         `json:"published_at"`
	URL         string         `json:"url"`
	Status      string         `json:"status"`
	Notes       string         `json:"notes"`
	App         string         `json:"app"`
	ExternalID  int64          `json:"external_id"`
	Results     map[string]any `json:"results"`
	SyncedAt    string         `json:"synced_at"`
	Archived    bool           `json:"archived"`
}
type Release struct {
	ReleaseData
	ID        int64  `json:"id"`
	ItemID    int64  `json:"item_id"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
type Brand struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Color            string  `json:"color"`
	LogoURL          string  `json:"logo_url"`
	SocialAccountIDs []int64 `json:"social_account_ids"`
	CampaignIDs      []int64 `json:"campaign_ids"`
}

func findBrand(s Settings, id string) *Brand {
	for _, b := range s.Brands {
		if b.ID == id {
			return &b
		}
	}
	return nil
}

type Settings struct {
	Brands   []Brand  `json:"brands"`
	Statuses []string `json:"statuses"`
	Formats  []string `json:"formats"`
	Channels []string `json:"channels"`
	Revision int64    `json:"revision"`
}

func defaultSettings() Settings {
	return Settings{Brands: []Brand{}, Statuses: []string{"idea", "brief", "in_progress", "review", "ready", "published"}, Formats: []string{"idea", "brief", "article", "video", "podcast", "social_post", "newsletter", "campaign", "refresh"}, Channels: []string{"Website", "Newsletter", "LinkedIn", "Instagram", "YouTube", "Podcast"}}
}

type queryer interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func getSettings(db queryer, pid string) (Settings, error) {
	s := defaultSettings()
	var raw string
	err := db.QueryRow("SELECT revision,data FROM editorial_settings WHERE project_id=?", pid).Scan(&s.Revision, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}
func has(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func validateDate(s string) error {
	if s == "" {
		return nil
	}
	if _, e := time.Parse("2006-01-02", s); e == nil {
		return nil
	}
	if _, e := time.Parse(time.RFC3339, s); e == nil {
		return nil
	}
	return invalid("dates must be YYYY-MM-DD or RFC3339 with a timezone")
}
func validateURL(s string) error {
	if s == "" {
		return nil
	}
	u, e := url.Parse(s)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return invalid("links must use http or https")
	}
	return nil
}
func validateItem(d *ItemData, s Settings) error {
	if d.BrandID != "" && findBrand(s, d.BrandID) == nil {
		return invalid("unknown brand in this project")
	}
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		return invalid("title is required")
	}
	if len(d.Title) > 500 {
		return invalid("title is too long")
	}
	if !has(s.Statuses, d.Status) {
		return invalid("unknown workflow status")
	}
	if !has(s.Formats, d.Format) {
		return invalid("unknown format")
	}
	if !has([]string{"not_required", "pending", "approved", "changes_requested"}, d.Approval) {
		return invalid("invalid approval state")
	}
	if d.Approval == "approved" && strings.TrimSpace(d.Reviewer) == "" {
		return invalid("reviewer is required for approval")
	}
	for _, v := range []string{d.Deadline, d.PlannedAt} {
		if e := validateDate(v); e != nil {
			return e
		}
	}
	for _, v := range append(append([]string{}, d.Sources...), d.Attachments...) {
		if e := validateURL(v); e != nil {
			return e
		}
	}
	if d.Sources == nil {
		d.Sources = []string{}
	}
	if d.Attachments == nil {
		d.Attachments = []string{}
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if d.Fields == nil {
		d.Fields = map[string]any{}
	}
	return nil
}
func validateRelease(d *ReleaseData) error {
	if strings.TrimSpace(d.Channel) == "" {
		return invalid("channel is required")
	}
	if !has([]string{"planned", "scheduled", "published", "failed", "cancelled"}, d.Status) {
		return invalid("invalid release status")
	}
	if !has([]string{"", "social", "campaigns"}, d.App) {
		return invalid("app must be social or campaigns")
	}
	if (d.App == "") != (d.ExternalID == 0) || d.ExternalID < 0 {
		return invalid("app and a positive external_id must be set together")
	}
	for _, v := range []string{d.PlannedAt, d.PublishedAt} {
		if e := validateDate(v); e != nil {
			return e
		}
	}
	if e := validateURL(d.URL); e != nil {
		return e
	}
	if d.Results == nil {
		d.Results = map[string]any{}
	}
	return nil
}
func patchJSON(dst any, patch map[string]any, allowed []string) error {
	if len(patch) == 0 {
		return invalid("patch cannot be empty")
	}
	for k, v := range patch {
		if !has(allowed, k) {
			return invalid("unknown or read-only field: " + k)
		}
		if v == nil {
			return invalid("use an empty string, array or object to clear " + k)
		}
	}
	raw, e := json.Marshal(dst)
	if e != nil {
		return e
	}
	merged := map[string]any{}
	if e = json.Unmarshal(raw, &merged); e != nil {
		return e
	}
	for k, v := range patch {
		merged[k] = v
	}
	raw, e = json.Marshal(merged)
	if e != nil {
		return invalid(e.Error())
	}
	fresh := reflect.New(reflect.TypeOf(dst).Elem())
	if e = json.Unmarshal(raw, fresh.Interface()); e != nil {
		return invalid(e.Error())
	}
	reflect.ValueOf(dst).Elem().Set(fresh.Elem())
	return nil
}

var itemFields = []string{"brand_id", "title", "body", "format", "status", "owner", "deadline", "planned_at", "approval", "reviewer", "campaign", "sources", "attachments", "tags", "fields", "archived"}
var releaseFields = []string{"channel", "planned_at", "published_at", "url", "status", "notes", "app", "external_id", "results", "archived"}

func readItem(db queryer, pid string, id int64) (Item, error) {
	var i Item
	var raw string
	e := db.QueryRow("SELECT id,revision,data,created_at,updated_at FROM editorial_items WHERE project_id=? AND id=?", pid, id).Scan(&i.ID, &i.Revision, &raw, &i.CreatedAt, &i.UpdatedAt)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &i.ItemData)
	}
	return i, e
}
func readRelease(db queryer, pid string, id int64) (Release, error) {
	var r Release
	var raw string
	e := db.QueryRow("SELECT id,item_id,revision,data,created_at,updated_at FROM editorial_releases WHERE project_id=? AND id=?", pid, id).Scan(&r.ID, &r.ItemID, &r.Revision, &raw, &r.CreatedAt, &r.UpdatedAt)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &r.ReleaseData)
	}
	return r, e
}
func history(tx *sql.Tx, pid string, id int64, action string, data any) error {
	raw, e := json.Marshal(data)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO editorial_history(project_id,item_id,action,snapshot) VALUES(?,?,?,?)", pid, id, action, string(raw))
	return e
}
func saveItem(db *sql.DB, pid string, id, revision int64, patch map[string]any) (Item, error) {
	tx, e := db.Begin()
	if e != nil {
		return Item{}, e
	}
	defer tx.Rollback()
	s, e := getSettings(tx, pid)
	if e != nil {
		return Item{}, e
	}
	i := Item{ItemData: ItemData{Format: s.Formats[0], Status: s.Statuses[0], Approval: "not_required"}}
	if id > 0 {
		i, e = readItem(tx, pid, id)
		if e != nil {
			return i, e
		}
		if i.Revision != revision {
			return i, errConflict
		}
	}
	before := i.ItemData
	if e = patchJSON(&i.ItemData, patch, itemFields); e != nil {
		return i, e
	}
	// Approval applies to the reviewed content, not a later edit. A second save is
	// required to approve changed content, even if approval was supplied in the patch.
	if before.Approval == "approved" && (before.BrandID != i.BrandID || before.Title != i.Title || before.Body != i.Body || before.Format != i.Format || !reflect.DeepEqual(before.Sources, i.Sources) || !reflect.DeepEqual(before.Attachments, i.Attachments) || !reflect.DeepEqual(before.Fields, i.Fields)) {
		i.Approval = "pending"
	}
	if e = validateItem(&i.ItemData, s); e != nil {
		return i, e
	}
	raw, e := json.Marshal(i.ItemData)
	if e != nil {
		return i, e
	}
	action := "item.created"
	if id == 0 {
		res, e := tx.Exec("INSERT INTO editorial_items(project_id,data) VALUES(?,?)", pid, string(raw))
		if e != nil {
			return i, e
		}
		id, e = res.LastInsertId()
		if e != nil {
			return i, e
		}
	} else {
		action = "item.updated"
		res, e := tx.Exec("UPDATE editorial_items SET data=?,revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE project_id=? AND id=? AND revision=?", string(raw), pid, id, revision)
		if e != nil {
			return i, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return i, e
		}
		if n != 1 {
			return i, errConflict
		}
	}
	i, e = readItem(tx, pid, id)
	if e != nil {
		return i, e
	}
	if e = history(tx, pid, id, action, i); e != nil {
		return i, e
	}
	return i, tx.Commit()
}
func saveRelease(db *sql.DB, pid string, id, itemID, revision int64, patch map[string]any, internal bool) (Release, error) {
	tx, e := db.Begin()
	if e != nil {
		return Release{}, e
	}
	defer tx.Rollback()
	r := Release{ItemID: itemID, ReleaseData: ReleaseData{Status: "planned"}}
	if id > 0 {
		r, e = readRelease(tx, pid, id)
		if e != nil {
			return r, e
		}
		if r.Revision != revision {
			return r, errConflict
		}
	}
	parent, e := readItem(tx, pid, r.ItemID)
	if e != nil {
		return r, e
	}
	if parent.Archived {
		return r, invalid("restore the archived item before editing releases")
	}
	allowed := append([]string{}, releaseFields...)
	if internal {
		allowed = append(allowed, "synced_at")
	}
	oldApp, oldID := r.App, r.ExternalID
	if e = patchJSON(&r.ReleaseData, patch, allowed); e != nil {
		return r, e
	}
	if oldApp != r.App || oldID != r.ExternalID {
		r.Results = map[string]any{}
		r.SyncedAt = ""
	}
	if e = validateRelease(&r.ReleaseData); e != nil {
		return r, e
	}
	raw, e := json.Marshal(r.ReleaseData)
	if e != nil {
		return r, e
	}
	action := "release.created"
	if id == 0 {
		res, e := tx.Exec("INSERT INTO editorial_releases(project_id,item_id,data) VALUES(?,?,?)", pid, r.ItemID, string(raw))
		if e != nil {
			return r, e
		}
		id, e = res.LastInsertId()
		if e != nil {
			return r, e
		}
	} else {
		action = "release.updated"
		if internal {
			action = "release.refreshed"
		}
		res, e := tx.Exec("UPDATE editorial_releases SET data=?,revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE project_id=? AND id=? AND revision=?", string(raw), pid, id, revision)
		if e != nil {
			return r, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return r, e
		}
		if n != 1 {
			return r, errConflict
		}
	}
	r, e = readRelease(tx, pid, id)
	if e != nil {
		return r, e
	}
	if e = history(tx, pid, r.ItemID, action, r); e != nil {
		return r, e
	}
	return r, tx.Commit()
}
func listItems(db *sql.DB, pid string, args map[string]any) (any, error) {
	where := "project_id=?"
	params := []any{pid}
	if str(args, "archived") != "all" {
		where += " AND json_extract(data,'$.archived')=?"
		params = append(params, str(args, "archived") == "true")
	}
	for _, k := range []string{"status", "format", "owner", "campaign", "approval"} {
		if v := str(args, k); v != "" {
			where += " AND json_extract(data,'$." + k + "')=?"
			params = append(params, v)
		}
	}
	if brand := str(args, "brand_id"); brand != "" {
		if brand == "unassigned" {
			brand = ""
		}
		where += " AND COALESCE(json_extract(data,'$.brand_id'),'')=?"
		params = append(params, brand)
	}
	if q := str(args, "q"); q != "" {
		where += " AND (instr(lower(json_extract(data,'$.title')),lower(?))>0 OR instr(lower(json_extract(data,'$.body')),lower(?))>0)"
		params = append(params, q, q)
	}
	var total int
	if e := db.QueryRow("SELECT count(*) FROM editorial_items WHERE "+where, params...).Scan(&total); e != nil {
		return nil, e
	}
	limit := number(args, "limit")
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := int64(0)
	if _, ok := args["offset"]; ok {
		offset = number(args, "offset")
	}
	if offset < 0 {
		return nil, invalid("offset cannot be negative")
	}
	rows, e := db.Query("SELECT id,revision,data,created_at,updated_at FROM editorial_items WHERE "+where+" ORDER BY id DESC LIMIT ? OFFSET ?", append(params, limit, offset)...)
	if e != nil {
		return nil, e
	}
	items := []Item{}
	for rows.Next() {
		var i Item
		var raw string
		if e = rows.Scan(&i.ID, &i.Revision, &raw, &i.CreatedAt, &i.UpdatedAt); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &i.ItemData); e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, i)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	// Releases are returned for the current page only; the panel loads every page.
	releases := []Release{}
	if len(items) > 0 {
		marks := []string{}
		p := []any{pid}
		for _, i := range items {
			marks = append(marks, "?")
			p = append(p, i.ID)
		}
		rows, e = db.Query("SELECT id,item_id,revision,data,created_at,updated_at FROM editorial_releases WHERE project_id=? AND item_id IN ("+strings.Join(marks, ",")+") ORDER BY id", p...)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var r Release
			var raw string
			if e = rows.Scan(&r.ID, &r.ItemID, &r.Revision, &raw, &r.CreatedAt, &r.UpdatedAt); e != nil {
				return nil, e
			}
			if e = json.Unmarshal([]byte(raw), &r.ReleaseData); e != nil {
				return nil, e
			}
			releases = append(releases, r)
		}
		if e = rows.Err(); e != nil {
			return nil, e
		}
	}
	return map[string]any{"items": items, "releases": releases, "total": total, "limit": limit, "offset": offset}, nil
}
func itemDetail(db *sql.DB, pid string, id int64) (any, error) {
	i, e := readItem(db, pid, id)
	if e != nil {
		return nil, e
	}
	rows, e := db.Query("SELECT id FROM editorial_releases WHERE project_id=? AND item_id=? ORDER BY id", pid, id)
	if e != nil {
		return nil, e
	}
	ids := []int64{}
	for rows.Next() {
		var n int64
		if e = rows.Scan(&n); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	releases := []Release{}
	for _, n := range ids {
		r, e := readRelease(db, pid, n)
		if e != nil {
			return nil, e
		}
		releases = append(releases, r)
	}
	rows, e = db.Query("SELECT id,action,snapshot,created_at FROM editorial_history WHERE project_id=? AND item_id=? ORDER BY id DESC LIMIT 101", pid, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	log := []map[string]any{}
	for rows.Next() {
		var n int64
		var action, raw, date string
		if e = rows.Scan(&n, &action, &raw, &date); e != nil {
			return nil, e
		}
		log = append(log, map[string]any{"id": n, "action": action, "snapshot": json.RawMessage(raw), "created_at": date})
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	truncated := len(log) > 100
	if truncated {
		log = log[:100]
	}
	return map[string]any{"item": i, "releases": releases, "history": log, "history_truncated": truncated}, nil
}
func saveSettings(db *sql.DB, pid string, args map[string]any) (Settings, error) {
	tx, e := db.Begin()
	if e != nil {
		return Settings{}, e
	}
	defer tx.Rollback()
	old, e := getSettings(tx, pid)
	if e != nil {
		return old, e
	}
	if number(args, "revision") != old.Revision {
		return old, errConflict
	}
	s := old
	if e = patchJSON(&s, args, []string{"brands", "statuses", "formats", "channels", "revision"}); e != nil {
		return s, e
	}
	if len(s.Brands) > 100 {
		return s, invalid("at most 100 brands per project")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for n := range s.Brands {
		b := &s.Brands[n]
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`).MatchString(b.ID) || b.ID == "unassigned" || ids[b.ID] {
			return s, invalid("brand IDs must be unique stable identifiers")
		}
		if b.Name != strings.TrimSpace(b.Name) || b.Name == "" || len(b.Name) > 80 || names[strings.ToLower(b.Name)] {
			return s, invalid("brand names must be unique and 1–80 characters")
		}
		if b.Color != "" && !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(b.Color) {
			return s, invalid("brand color must be a six-digit hex color")
		}
		if e := validateURL(b.LogoURL); e != nil {
			return s, e
		}
		for _, list := range [][]int64{b.SocialAccountIDs, b.CampaignIDs} {
			seen := map[int64]bool{}
			for _, id := range list {
				if id <= 0 || seen[id] {
					return s, invalid("connection IDs must be unique positive integers")
				}
				seen[id] = true
			}
		}
		ids[b.ID] = true
		names[strings.ToLower(b.Name)] = true
	}
	for _, b := range old.Brands {
		if ids[b.ID] {
			continue
		}
		var count int
		if e := tx.QueryRow("SELECT count(*) FROM editorial_items WHERE project_id=? AND json_extract(data,'$.brand_id')=?", pid, b.ID).Scan(&count); e != nil {
			return s, e
		}
		if count > 0 {
			return s, invalid("reassign all content, including archived items, before removing brand " + b.Name)
		}
	}
	for _, values := range [][]string{s.Statuses, s.Formats, s.Channels} {
		if len(values) == 0 || len(values) > 50 {
			return s, invalid("settings lists must have 1–50 entries")
		}
		seen := map[string]bool{}
		for _, v := range values {
			if strings.TrimSpace(v) != v || v == "" || len(v) > 80 || seen[v] {
				return s, invalid("settings values must be unique nonempty names of at most 80 characters")
			}
			seen[v] = true
		}
	}
	for key, allowed := range map[string][]string{"status": s.Statuses, "format": s.Formats} {
		rows, e := tx.Query("SELECT DISTINCT json_extract(data,'$."+key+"') FROM editorial_items WHERE project_id=?", pid)
		if e != nil {
			return s, e
		}
		for rows.Next() {
			var value string
			if e = rows.Scan(&value); e != nil {
				rows.Close()
				return s, e
			}
			if !has(allowed, value) {
				rows.Close()
				return s, invalid(fmt.Sprintf("%s %q is still used by an item", key, value))
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return s, e
		}
	}
	s.Revision = old.Revision + 1
	raw, e := json.Marshal(s)
	if e != nil {
		return s, e
	}
	_, e = tx.Exec("INSERT INTO editorial_settings(project_id,revision,data) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET revision=excluded.revision,data=excluded.data", pid, s.Revision, string(raw))
	if e != nil {
		return s, e
	}
	return s, tx.Commit()
}
