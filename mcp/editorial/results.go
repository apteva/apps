package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
)

func detailProperties() map[string]any {
	p := properties([]string{"id"})
	for _, k := range []string{"include_history", "include_results"} {
		p[k] = map[string]any{"type": "boolean"}
	}
	p["field_keys"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return p
}
func releaseReadProperties() map[string]any {
	p := properties([]string{"id"})
	p["include_results"] = map[string]any{"type": "boolean"}
	p["results_keys"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return p
}
func optionBool(args map[string]any, k string, fallback bool) bool {
	v, ok := args[k]
	if !ok {
		return fallback
	}
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		if b, e := strconv.ParseBool(s); e == nil {
			return b
		}
	}
	return fallback
}
func selectedKeys(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := []string{}
		for _, v := range x {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func jsonObject(v any) map[string]any {
	raw, _ := json.Marshal(v)
	out := map[string]any{}
	json.Unmarshal(raw, &out)
	return out
}
func releaseView(r Release, args map[string]any) map[string]any {
	out := jsonObject(r)
	keys := selectedKeys(args["results_keys"])
	if _, ok := args["results_keys"]; ok {
		m := map[string]any{}
		for _, k := range keys {
			if v, ok := r.Results[k]; ok {
				m[k] = v
			}
		}
		out["results"] = m
		out["results_projected"] = true
	} else if !optionBool(args, "include_results", false) {
		delete(out, "results")
		out["results_omitted"] = true
	}
	return out
}
func detailView(i Item, rs []Release, history []map[string]any, truncated bool, args map[string]any) map[string]any {
	var item any = i
	if keys, ok := args["field_keys"]; ok {
		out := jsonObject(i)
		fields := map[string]any{}
		for _, k := range selectedKeys(keys) {
			if v, ok := i.Fields[k]; ok {
				fields[k] = v
			}
		}
		out["fields"] = fields
		available := []string{}
		for k := range i.Fields {
			available = append(available, k)
		}
		sort.Strings(available)
		out["available_field_keys"] = available
		item = out
	}
	if optionBool(args, "include_results", true) {
		return map[string]any{"item": item, "releases": rs, "history": history, "history_truncated": truncated}
	}
	releases := []map[string]any{}
	for _, r := range rs {
		releases = append(releases, releaseView(r, map[string]any{}))
	}
	return map[string]any{"item": item, "releases": releases, "history": history, "history_truncated": truncated}
}

// Explicit recursive merge: unspecified history survives, arrays replace, null is a value.
func mergeResultObject(old, patch map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range old {
		out[k] = v
	}
	for k, v := range patch {
		if obj, ok := v.(map[string]any); ok {
			prior, _ := old[k].(map[string]any)
			out[k] = mergeResultObject(prior, obj)
		} else {
			out[k] = v
		}
	}
	return out
}
func mergeReleaseResults(db *sql.DB, pid string, id, revision int64, patch map[string]any) (Release, error) {
	r, e := readRelease(db, pid, id)
	if e != nil {
		return r, e
	}
	if r.Revision != revision {
		return r, errConflict
	}
	raw, e := json.Marshal(patch)
	if e != nil {
		return r, e
	}
	if len(raw) > 64<<10 {
		return r, invalid("results merge exceeds 64 KiB; send only changed result keys")
	}
	return saveRelease(db, pid, id, 0, revision, map[string]any{"results": mergeResultObject(r.Results, patch)}, false)
}
func mergeReceipt(r Release, patch map[string]any) map[string]any {
	raw, _ := json.Marshal(r.Results)
	sum := sha256.Sum256(raw)
	return map[string]any{"id": r.ID, "item_id": r.ItemID, "revision": r.Revision, "status": r.Status, "results_sha256": hex.EncodeToString(sum[:]), "applied_merge": patch}
}

func validateReadOptions(args map[string]any) error {
	for _, k := range []string{"include_history", "include_results"} {
		if v, ok := args[k]; ok {
			switch x := v.(type) {
			case bool:
			case string:
				if _, e := strconv.ParseBool(x); e != nil {
					return invalid(k + " must be boolean")
				}
			default:
				return invalid(k + " must be boolean")
			}
		}
	}
	for _, k := range []string{"field_keys", "results_keys"} {
		if v, ok := args[k]; ok {
			switch x := v.(type) {
			case []string:
			case []any:
				for _, y := range x {
					if _, ok := y.(string); !ok {
						return invalid(k + " must contain strings")
					}
				}
			default:
				return invalid(k + " must be an array of strings")
			}
		}
	}
	return nil
}
