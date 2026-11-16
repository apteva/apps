package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// checkFormData validates answers against a form schema. Missing
// required fields are reported separately so intake can accept partial
// answers (the agent keeps asking) while a submitted checklist cannot.
func checkFormData(f *Form, data map[string]any, strict bool) (missing, invalid []string) {
	known := map[string]bool{}
	for _, fld := range f.Schema.Fields {
		known[fld.Key] = true
		v, ok := data[fld.Key]
		if !ok || v == nil || v == "" {
			if fld.Required {
				missing = append(missing, fld.Key)
			}
			continue
		}
		if msg := checkField(fld, v); msg != "" {
			invalid = append(invalid, fld.Key+": "+msg)
		}
	}
	if strict {
		for k := range data {
			if !known[k] {
				invalid = append(invalid, k+": not a field of form "+f.Key)
			}
		}
	}
	return missing, invalid
}

func checkField(fld FormField, v any) string {
	switch fld.Type {
	case "number":
		n := floatArg(map[string]any{"v": v}, "v", -1e308)
		if n == -1e308 {
			return "must be a number"
		}
		if fld.Min != nil && n < *fld.Min {
			return fmt.Sprintf("must be >= %v", *fld.Min)
		}
		if fld.Max != nil && n > *fld.Max {
			return fmt.Sprintf("must be <= %v", *fld.Max)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return "must be true or false"
		}
	case "choice":
		s, ok := v.(string)
		if !ok || !contains(fld.Options, s) {
			return "must be one of " + strings.Join(fld.Options, ", ")
		}
	case "multi_choice":
		arr, ok := v.([]any)
		if !ok {
			return "must be a list"
		}
		for _, x := range arr {
			s, ok := x.(string)
			if !ok || !contains(fld.Options, s) {
				return "values must be from " + strings.Join(fld.Options, ", ")
			}
		}
	case "photo", "signature":
		// A storage file id, a list of them, or a URL.
		switch x := v.(type) {
		case float64, string:
		case []any:
			if len(x) == 0 {
				return "at least one file required"
			}
		default:
			return "must be a storage file id, a list of ids, or a URL"
		}
	case "date":
		if _, err := parseTime(fmt.Sprint(v)); err != nil {
			return "must be a date"
		}
	case "text":
		if _, ok := v.(string); !ok {
			return "must be text"
		}
	}
	return ""
}

type FormResponse struct {
	ID          int64          `json:"id"`
	FormKey     string         `json:"form_key"`
	FormVersion int            `json:"form_version"`
	WorkID      *int64         `json:"work_id,omitempty"`
	SlotID      *int64         `json:"slot_id,omitempty"`
	SiteID      *int64         `json:"site_id,omitempty"`
	ObjectID    *int64         `json:"object_id,omitempty"`
	Responder   string         `json:"responder"`
	Data        map[string]any `json:"data"`
	SubmittedAt string         `json:"submitted_at"`
}

func submitForm(db *sql.DB, pid string, args map[string]any) (*FormResponse, error) {
	data := objectArg(args, "data")
	if data == nil {
		return nil, errors.New("data required")
	}
	workID, slotID, siteID, objectID := int64Arg(args, "work_id"), int64Arg(args, "slot_id"), int64Arg(args, "site_id"), int64Arg(args, "object_id")
	if slotID > 0 && workID == 0 {
		_ = db.QueryRow(`SELECT work_id FROM slots WHERE project_id=? AND id=?`, pid, slotID).Scan(&workID)
	}
	// The form is the one of the work's brand (or the site's brand for a
	// site log), falling back to the shared form.
	var brandID int64
	if workID > 0 {
		w, err := getWork(db, pid, workID)
		if err != nil {
			return nil, notFound(err, "work")
		}
		brandID = w.BrandID
		if siteID == 0 && w.SiteID != nil {
			siteID = *w.SiteID
		}
	}
	if workID == 0 && siteID == 0 {
		return nil, errors.New("work_id, slot_id or site_id required (site_id for site logs such as temperature checks)")
	}
	if workID == 0 {
		s, err := getSite(db, pid, siteID)
		if err != nil {
			return nil, notFound(err, "site")
		}
		brandID = s.BrandID
	}
	f, err := getForm(db, pid, stringArg(args, "form_key"), brandID)
	if err != nil {
		return nil, notFound(err, "form")
	}
	missing, invalid := checkFormData(f, data, true)
	if len(missing) > 0 {
		invalid = append(invalid, "missing required: "+strings.Join(missing, ", "))
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("form %s rejected: %s", f.Key, strings.Join(invalid, "; "))
	}
	now := nowUTC()
	r, err := db.Exec(`INSERT INTO form_responses(project_id,form_key,form_version,work_id,slot_id,site_id,object_id,responder,data_json,submitted_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, pid, f.Key, f.Version, nilIfZero(workID), nilIfZero(slotID), nilIfZero(siteID), nilIfZero(objectID),
		firstNonEmpty(stringArg(args, "responder"), "agent"), mustJSON(data), now)
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	if workID > 0 {
		_ = logEvent(db, pid, workID, stringArg(args, "responder"), "form", "", f.Key, map[string]any{"response_id": id})
	}
	resp := &FormResponse{ID: id, FormKey: f.Key, FormVersion: f.Version, Responder: firstNonEmpty(stringArg(args, "responder"), "agent"), Data: data, SubmittedAt: now}
	if workID > 0 {
		resp.WorkID = &workID
	}
	if slotID > 0 {
		resp.SlotID = &slotID
	}
	if siteID > 0 {
		resp.SiteID = &siteID
	}
	if objectID > 0 {
		resp.ObjectID = &objectID
	}
	return resp, nil
}

func listFormResponses(db *sql.DB, pid string, args map[string]any) ([]FormResponse, error) {
	q := `SELECT id,form_key,form_version,work_id,slot_id,site_id,object_id,responder,data_json,submitted_at FROM form_responses WHERE project_id=?`
	params := []any{pid}
	for _, k := range []string{"work_id", "site_id", "object_id"} {
		if v := int64Arg(args, k); v > 0 {
			q += " AND " + k + "=?"
			params = append(params, v)
		}
	}
	if v := stringArg(args, "form_key"); v != "" {
		q += " AND form_key=?"
		params = append(params, v)
	}
	if v := stringArg(args, "since"); v != "" {
		if t, err := parseTime(v); err == nil {
			q += " AND submitted_at>=?"
			params = append(params, fmtTime(t))
		}
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.Query(q+fmt.Sprintf(" ORDER BY submitted_at DESC LIMIT %d", limit), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FormResponse{}
	for rows.Next() {
		var r FormResponse
		var work, slot, site, obj sql.NullInt64
		var data string
		if err := rows.Scan(&r.ID, &r.FormKey, &r.FormVersion, &work, &slot, &site, &obj, &r.Responder, &data, &r.SubmittedAt); err != nil {
			return nil, err
		}
		if work.Valid {
			r.WorkID = &work.Int64
		}
		if slot.Valid {
			r.SlotID = &slot.Int64
		}
		if site.Valid {
			r.SiteID = &site.Int64
		}
		if obj.Valid {
			r.ObjectID = &obj.Int64
		}
		decodeInto(data, &r.Data)
		out = append(out, r)
	}
	return out, rows.Err()
}
