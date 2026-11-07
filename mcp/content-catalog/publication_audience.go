package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type publicationDB interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type PublisherReference struct {
	App       string `json:"app"`
	InstallID int64  `json:"install_id,omitempty"`
	RecordID  string `json:"record_id"`
}
type PublicationAudience struct {
	AudienceType string              `json:"audience_type"`
	AccessPolicy string              `json:"access_policy"`
	GroupScope   string              `json:"group_scope"`
	GroupIDs     []string            `json:"group_ids"`
	GroupLabels  map[string]string   `json:"group_labels"`
	PublisherRef *PublisherReference `json:"publisher_ref"`
}
type publicationFilter struct{ Destination, AccountRef, AudienceType, AccessPolicy, GroupScope, GroupID, Status, ExternalPostID string }

func parsePublicationFilter(args map[string]any) (publicationFilter, error) {
	f := publicationFilter{strings.ToLower(str(args, "destination")), str(args, "account_ref"), str(args, "audience_type"), str(args, "access_policy"), str(args, "group_scope"), str(args, "group_id"), str(args, "status"), str(args, "external_post_id")}
	if f.AudienceType != "" && !oneOf(f.AudienceType, "public", "restricted", "private", "unknown") {
		return f, errors.New("invalid audience_type")
	}
	if f.GroupScope != "" && !oneOf(f.GroupScope, "all_eligible", "selected", "unknown") {
		return f, errors.New("invalid group_scope")
	}
	if f.AccountRef != "" && f.Destination == "" {
		return f, errors.New("account_ref requires destination")
	}
	if (f.GroupID != "" || f.GroupScope != "" || f.AccessPolicy != "") && (f.Destination == "" || f.AccountRef == "") {
		return f, errors.New("access policy/group filters require destination and account_ref")
	}
	if f.ExternalPostID != "" && (f.Destination == "" || f.AccountRef == "") {
		return f, errors.New("external_post_id filter requires destination and account_ref")
	}
	if f.Status != "" && !oneOf(f.Status, "planned", "scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown") {
		return f, errors.New("invalid publication status")
	}
	return f, nil
}
func (f publicationFilter) scoped() bool {
	return f.Destination != "" || f.AccountRef != "" || f.AudienceType != "" || f.AccessPolicy != "" || f.GroupScope != "" || f.GroupID != ""
}

// The same audience predicate is used by lists and asset availability. For
// readiness only, unknown compatible scope blocks automatic duplicate selection.
func (f publicationFilter) audiencePredicate(alias string, uncertain bool) (string, []any) {
	q := ""
	v := []any{}
	add := func(column, value, unknown string) {
		if value == "" {
			return
		}
		if uncertain && value != unknown {
			q += " AND (" + alias + "." + column + "=? OR " + alias + "." + column + "=?)"
			v = append(v, value, unknown)
		} else {
			q += " AND " + alias + "." + column + "=?"
			v = append(v, value)
		}
	}
	add("audience_type", f.AudienceType, "unknown")
	add("access_policy", f.AccessPolicy, "")
	add("group_scope", f.GroupScope, "unknown")
	if f.GroupID != "" {
		q += " AND (" + alias + ".group_scope='all_eligible' OR (" + alias + ".group_scope='selected' AND EXISTS(SELECT 1 FROM json_each(" + alias + ".group_ids_json) g WHERE g.value=?))"
		v = append(v, f.GroupID)
		if uncertain {
			q += " OR " + alias + ".group_scope='unknown'"
		}
		q += ")"
	}
	return q, v
}
func (f publicationFilter) predicate(alias string) (string, []any) {
	q, v := f.audiencePredicate(alias, false)
	if f.Destination != "" {
		q += " AND LOWER(" + alias + ".destination)=?"
		v = append(v, f.Destination)
	}
	if f.AccountRef != "" {
		q += " AND " + alias + ".account_ref=?"
		v = append(v, f.AccountRef)
	}
	if f.ExternalPostID != "" {
		q += " AND " + alias + ".external_post_id=?"
		v = append(v, f.ExternalPostID)
	}
	if f.Status != "" {
		q += " AND " + alias + ".status=?"
		v = append(v, f.Status)
	}
	return q, v
}
func applyPublicationAudience(args map[string]any, p *Publication) error {
	if p.AudienceType == "" {
		p.AudienceType = "unknown"
	}
	if p.GroupScope == "" {
		p.GroupScope = "unknown"
	}
	if p.GroupIDs == nil {
		p.GroupIDs = []string{}
	}
	if p.GroupLabels == nil {
		p.GroupLabels = map[string]string{}
	}
	for key, field := range map[string]*string{"audience_type": &p.AudienceType, "access_policy": &p.AccessPolicy, "group_scope": &p.GroupScope} {
		if raw, ok := args[key]; ok {
			v, ok := raw.(string)
			if !ok {
				return fmt.Errorf("%s must be text", key)
			}
			*field = strings.TrimSpace(v)
		}
	}
	if raw, ok := args["group_ids"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var ids []string
		if err = json.Unmarshal(b, &ids); err != nil || ids == nil {
			return errors.New("group_ids must be an array of strings")
		}
		if len(ids) > 100 {
			return errors.New("at most 100 group IDs")
		}
		seen := map[string]bool{}
		p.GroupIDs = []string{}
		for _, id := range ids {
			if id == "" || id != strings.TrimSpace(id) || len(id) > 200 || seen[id] {
				return errors.New("group IDs must be distinct nonempty provider IDs")
			}
			seen[id] = true
			p.GroupIDs = append(p.GroupIDs, id)
		}
		sort.Strings(p.GroupIDs)
	}
	if raw, ok := args["group_labels"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var labels map[string]string
		if err = json.Unmarshal(b, &labels); err != nil || labels == nil {
			return errors.New("group_labels must map provider IDs to provider names")
		}
		p.GroupLabels = labels
	}
	if raw, ok := args["publisher_ref"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(b, &p.PublisherRef); err != nil {
			return errors.New("invalid publisher_ref")
		}
	}
	if !oneOf(p.AudienceType, "public", "restricted", "private", "unknown") || !oneOf(p.GroupScope, "all_eligible", "selected", "unknown") {
		return errors.New("invalid audience type or group scope")
	}
	if len(p.AccessPolicy) > 100 {
		return errors.New("access_policy max 100 characters")
	}
	if p.GroupScope == "selected" && len(p.GroupIDs) == 0 {
		return errors.New("selected group scope requires group_ids")
	}
	if p.GroupScope != "selected" && len(p.GroupIDs) > 0 {
		return errors.New("only selected group scope accepts group_ids")
	}
	if p.AudienceType != "restricted" && (p.AccessPolicy != "" || p.GroupScope != "unknown" || len(p.GroupIDs) > 0) {
		return errors.New("access policy/groups require restricted audience")
	}
	if (p.GroupScope != "unknown" || p.AccessPolicy != "") && p.AccountRef == "" {
		return errors.New("access policy/groups require account_ref")
	}
	for id, label := range p.GroupLabels {
		found := false
		for _, g := range p.GroupIDs {
			if g == id {
				found = true
			}
		}
		if !found || strings.TrimSpace(label) == "" || len(label) > 200 {
			return errors.New("group_labels must name only selected provider IDs")
		}
	}
	if p.PublisherRef != nil && (strings.TrimSpace(p.PublisherRef.App) == "" || strings.TrimSpace(p.PublisherRef.RecordID) == "" || p.PublisherRef.InstallID < 0) {
		return errors.New("publisher_ref requires app and record_id")
	}
	return nil
}
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func publicationAudienceSchema(s map[string]any, filters bool) map[string]any {
	props, ok := s["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		s["properties"] = props
	}
	props["destination"] = map[string]any{"type": "string", "description": "Exact platform identifier; never prefix matching"}
	props["account_ref"] = map[string]any{"type": "string", "description": "Stable account reference within the platform"}
	props["audience_type"] = map[string]any{"type": "string", "enum": []string{"public", "restricted", "private", "unknown"}}
	props["access_policy"] = map[string]any{"type": "string", "description": "Provider-defined policy such as membership; requires platform/account"}
	props["group_scope"] = map[string]any{"type": "string", "enum": []string{"all_eligible", "selected", "unknown"}, "description": "Unknown never means all groups"}
	if filters {
		props["group_id"] = map[string]any{"type": "string", "description": "Exact provider group ID in this account; matches selected IDs or confirmed all_eligible, never unknown scope"}
	} else {
		props["group_ids"] = map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string"}, "description": "Provider IDs for selected groups only"}
		props["group_labels"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Optional provider-supplied names keyed by selected IDs; never inferred"}
		props["publisher_ref"] = map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"app": map[string]any{"type": "string"}, "install_id": map[string]any{"type": "integer", "minimum": 0}, "record_id": map[string]any{"type": "string"}}, "required": []string{"app", "record_id"}}
		props["expected_revision"] = map[string]any{"type": "integer", "minimum": 1, "description": "Required for changes to existing audience or external identity; read revision first"}
		props["observed_at"] = map[string]any{"type": "string", "description": "RFC3339 verification time, independent of actual_at. Defaults to current observation time for new observed outcomes"}
	}
	return s
}
func postRecordSchema(single bool) map[string]any {
	s := schema("status")
	if single {
		s = schema("asset_id", "status")
	}
	s = publicationAudienceSchema(s, false)
	props := s["properties"].(map[string]any)
	for _, key := range []string{"post_id", "publication_id", "asset_id", "title", "audience", "status", "planned_at", "actual_at", "external_post_id", "external_url", "evidence_source", "failure_details"} {
		props[key] = map[string]any{"type": "string"}
	}
	props["actual_at"] = map[string]any{"type": "string", "description": "Original publication time, RFC3339; omit or empty when unknown. Never substitute upload or observation time"}
	props["asset_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 100}
	return s
}
func publicationHistory(db publicationDB, pid, id string) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id,status,observed_at,recorded_at,revision,before_json,after_json FROM post_events WHERE project_id=? AND post_id=? ORDER BY rowid DESC LIMIT 100`, pid, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var eid, status, observed, recorded, b, a string
		var rev int64
		if err = rows.Scan(&eid, &status, &observed, &recorded, &rev, &b, &a); err != nil {
			return nil, err
		}
		var before, after any
		if err = json.Unmarshal([]byte(b), &before); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(a), &after); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": eid, "status": status, "observed_at": observed, "recorded_at": recorded, "revision": rev, "before": before, "after": after})
	}
	return out, rows.Err()
}

// Read verified audience snapshots for an entire result page in one request.
// Old events remain explicitly unknown; never borrow the post's current audience.
func appendAudience(scopes []PublicationAudience, a PublicationAudience) []PublicationAudience {
	for _, saved := range scopes {
		if jsonText(saved) == jsonText(a) {
			return scopes
		}
	}
	return append(scopes, a)
}
func loadVerifiedAudiences(db publicationDB, pid string, posts []*Publication) error {
	if len(posts) == 0 {
		return nil
	}
	marks := []string{}
	values := []any{pid}
	byID := map[string][]*Publication{}
	for _, p := range posts {
		p.HistoricallyVerified = p.Status == "verified_published"
		p.VerifiedAudiences = []PublicationAudience{}
		p.RecordedAudiences = []PublicationAudience{}
		if _, ok := byID[p.ID]; !ok {
			marks = append(marks, "?")
			values = append(values, p.ID)
		}
		byID[p.ID] = append(byID[p.ID], p)
	}
	rows, err := db.Query(`SELECT post_id,status,audience_type,access_policy,group_scope,group_ids_json,group_labels_json,publisher_ref_json FROM post_events WHERE project_id=? AND post_id IN (`+strings.Join(marks, ",")+`) ORDER BY rowid DESC`, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, ids, labels, ref string
		var a PublicationAudience
		if err = rows.Scan(&id, &status, &a.AudienceType, &a.AccessPolicy, &a.GroupScope, &ids, &labels, &ref); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(ids), &a.GroupIDs); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(labels), &a.GroupLabels); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(ref), &a.PublisherRef); err != nil {
			return err
		}
		for _, p := range byID[id] {
			p.RecordedAudiences = appendAudience(p.RecordedAudiences, a)
			if status == "verified_published" {
				p.HistoricallyVerified = true
				p.VerifiedAudiences = appendAudience(p.VerifiedAudiences, a)
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, p := range posts {
		p.RecordedAudiences = appendAudience(p.RecordedAudiences, p.PublicationAudience)
		if p.Status == "verified_published" {
			p.VerifiedAudiences = appendAudience(p.VerifiedAudiences, p.PublicationAudience)
		}
	}
	return nil
}

func postListSchema(single bool) map[string]any {
	s := lifecycleListSchema()
	if single {
		s = lifecycleListSchema("asset_id")
	}
	s = publicationAudienceSchema(s, true)
	props := s["properties"].(map[string]any)
	for _, key := range []string{"asset_id", "session_id", "brand_id", "status", "cursor", "external_post_id"} {
		props[key] = map[string]any{"type": "string"}
	}
	props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Page size; follow next_cursor with the same filters"}
	return s
}

func loadPostMemberships(db publicationDB, pid string, posts []*Publication) error {
	if len(posts) == 0 {
		return nil
	}
	byID := map[string][]*Publication{}
	marks := []string{}
	values := []any{pid}
	for _, p := range posts {
		p.AssetIDs = []string{}
		if _, ok := byID[p.ID]; !ok {
			marks = append(marks, "?")
			values = append(values, p.ID)
		}
		byID[p.ID] = append(byID[p.ID], p)
	}
	rows, err := db.Query(`SELECT post_id,asset_id FROM post_assets WHERE project_id=? AND post_id IN (`+strings.Join(marks, ",")+`) ORDER BY post_id,position`, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, asset string
		if err = rows.Scan(&id, &asset); err != nil {
			return err
		}
		for _, p := range byID[id] {
			p.AssetIDs = append(p.AssetIDs, asset)
		}
	}
	return rows.Err()
}
