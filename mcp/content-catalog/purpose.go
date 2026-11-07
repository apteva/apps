package main

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

var outputTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type purposeOptions struct {
	Role, OutputType     string
	IncludeIntermediates bool
}

func parsePurposeOptions(args map[string]any) (purposeOptions, error) {
	o := purposeOptions{Role: str(args, "role"), OutputType: str(args, "output_type")}
	if o.Role != "" && !oneOf(o.Role, "unspecified", "main", "derivative", "intermediate") {
		return o, errors.New("role must be unspecified, main, derivative, or intermediate")
	}
	if o.OutputType != "" && !outputTypePattern.MatchString(o.OutputType) {
		return o, errors.New("output_type must be a lowercase token of at most 64 characters")
	}
	if raw := args["include_intermediates"]; raw != nil {
		v, ok := raw.(bool)
		if !ok {
			return o, errors.New("include_intermediates must be boolean")
		}
		o.IncludeIntermediates = v
	}
	return o, nil
}

// Values are validated tokens; quote them here so callers can compose this
// constant predicate without changing the existing positional SQL bindings.
func (o purposeOptions) predicate(alias string) string {
	q := ""
	if !o.IncludeIntermediates && o.Role != "intermediate" {
		q += " AND " + alias + ".role<>'intermediate'"
	}
	if o.Role != "" {
		q += " AND " + alias + ".role='" + o.Role + "'"
	}
	if o.OutputType != "" {
		q += " AND " + alias + ".output_type='" + o.OutputType + "'"
	}
	return q
}
func addPurposeSchema(s map[string]any) {
	props, ok := s["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		s["properties"] = props
	}
	props["role"] = map[string]any{"type": "string", "enum": []string{"unspecified", "main", "derivative", "intermediate"}}
	props["output_type"] = map[string]any{"type": "string", "pattern": outputTypePattern.String()}
	props["include_intermediates"] = map[string]any{"type": "boolean", "default": false}
}
func normalizeOutputType(raw any) (string, error) {
	v, ok := raw.(string)
	if !ok {
		return "", errors.New("output_type must be text")
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if v != "" && !outputTypePattern.MatchString(v) {
		return "", errors.New("invalid output_type")
	}
	return v, nil
}

func assetListSchema(required ...string) map[string]any {
	s := lifecycleListSchema(required...)
	addPurposeSchema(s)
	return s
}

// Session reads use id for the session; asset lists use it for an exact child.
// Keep these contracts distinct instead of inheriting a vague shared id field.
func sessionAssetListSchema() map[string]any {
	s := assetListSchema("session_id")
	props := s["properties"].(map[string]any)
	delete(props, "brand_id")
	props["id"] = map[string]any{"type": "string", "minLength": 1, "description": "Optional exact Catalog asset ID (not a Storage file ID). Returns at most one match within session_id and the selected lifecycle/purpose filters; missing or excluded IDs return an empty assets array. For direct inspection use content_catalog_assets_get(id)."}
	return s
}

func purposeHistory(db *sql.DB, pid, id string) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT previous_role,role,previous_output_type,output_type,created_at FROM asset_purpose_events WHERE project_id=? AND asset_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, pid, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var before, after, oldType, newType, at string
		if err := rows.Scan(&before, &after, &oldType, &newType, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"previous_role": before, "role": after, "previous_output_type": oldType, "output_type": newType, "created_at": at})
	}
	return out, rows.Err()
}
