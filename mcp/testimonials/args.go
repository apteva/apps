package main

import (
	"encoding/json"
	"fmt"
	"strconv"
)

func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	default:
		return false
	}
}

func intArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	return intFromAny(v)
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	n, ok := intArg(args, key)
	return int64(n), ok
}

func intFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		n, err := x.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	default:
		return 0, false
	}
}

func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return []string{}
	}
	switch x := v.(type) {
	case []string:
		return cleanStrings(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if item != nil {
				out = append(out, fmt.Sprint(item))
			}
		}
		return cleanStrings(out)
	case string:
		if x == "" {
			return []string{}
		}
		return cleanStrings([]string{x})
	default:
		return []string{}
	}
}

func cleanStrings(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func mapArg(args map[string]any, key string) (map[string]any, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false
	}
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

func testimonialFromArgs(args map[string]any) Testimonial {
	t := Testimonial{
		Status:          strArg(args, "status"),
		Kind:            strArg(args, "kind"),
		Source:          strArg(args, "source"),
		Title:           strArg(args, "title"),
		Quote:           strArg(args, "quote"),
		Body:            strArg(args, "body"),
		AuthorName:      strArg(args, "author_name"),
		AuthorRole:      strArg(args, "author_role"),
		AuthorCompany:   strArg(args, "author_company"),
		AuthorEmail:     strArg(args, "author_email"),
		MediaFileID:     strArg(args, "media_file_id"),
		MediaURL:        strArg(args, "media_url"),
		ConsentStatus:   strArg(args, "consent_status"),
		PermissionScope: strArg(args, "permission_scope"),
		Tags:            stringSliceArg(args, "tags"),
	}
	if n, ok := intArg(args, "rating"); ok {
		t.Rating = &n
	}
	if m, ok := mapArg(args, "metadata"); ok {
		t.Metadata = m
	}
	return t
}
