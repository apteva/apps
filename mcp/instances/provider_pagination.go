package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

func executePagedProviderTool(ctx *sdk.AppCtx, connection int64, provider, tool string, input map[string]any) (json.RawMessage, error) {
	args := cloneFields(input)
	var merged map[string]any
	seen := map[string]bool{}
	for page := 1; page <= 100; page++ {
		data, err := executeProviderToolUncached(ctx, connection, provider, tool, args)
		if err != nil {
			return nil, err
		}
		var root map[string]any
		if err = json.Unmarshal(data, &root); err != nil {
			if page == 1 {
				return data, nil
			}
			return nil, err
		}
		if page == 1 {
			merged = root
		} else {
			mergeProviderPages(merged, root)
		}
		next := nextProviderPage(root, args)
		if len(next) == 0 {
			return json.Marshal(merged)
		}
		encoded, _ := json.Marshal(next)
		if seen[string(encoded)] {
			return nil, fmt.Errorf("%s.%s repeated pagination cursor", provider, tool)
		}
		seen[string(encoded)] = true
		for key, value := range next {
			args[key] = value
		}
	}
	return nil, fmt.Errorf("%s.%s exceeded 100 pages; inventory is incomplete", provider, tool)
}

func mergeProviderPages(dst, src map[string]any) {
	for key, value := range src {
		switch v := value.(type) {
		case []any:
			if existing, ok := dst[key].([]any); ok {
				dst[key] = append(existing, v...)
			} else {
				dst[key] = v
			}
		case map[string]any:
			if key == "meta" || key == "links" {
				dst[key] = v
				continue
			}
			if existing, ok := dst[key].(map[string]any); ok {
				mergeProviderPages(existing, v)
			} else {
				dst[key] = v
			}
		default:
			dst[key] = value
		}
	}
}

func nextProviderPage(root map[string]any, args map[string]any) map[string]any {
	if token := findJSONScalarIn(root, "nextToken"); token != "" {
		return map[string]any{"NextToken": token}
	}
	if next := nestedInt(root, "meta", "pagination", "next_page"); next > 0 {
		return map[string]any{"page": next}
	}
	if pages := anyToInt(root["pages"]); pages > anyToInt(root["page"]) {
		current := anyToInt(root["page"])
		if current > 0 {
			return map[string]any{"page": current + 1}
		}
	}
	next := firstNonEmpty(nestedMapString(root, "links", "pages", "next"), nestedMapString(root, "meta", "links", "next"))
	if next != "" {
		if u, err := url.Parse(next); err == nil {
			out := map[string]any{}
			for _, key := range []string{"page", "cursor", "page_token"} {
				if v := u.Query().Get(key); v != "" {
					out[key] = v
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		return map[string]any{"cursor": next}
	}
	perPage := anyToInt(args["per_page"])
	if perPage == 0 {
		perPage = anyToInt(args["page_size"])
	}
	page := anyToInt(args["page"])
	if page < 1 {
		page = 1
	}
	total := anyToInt(root["total_count"])
	if total == 0 {
		total, _ = strconv.Atoi(findJSONScalarIn(root, "total_entries"))
	}
	if perPage > 0 && page*perPage < total {
		return map[string]any{"page": page + 1}
	}
	return nil
}
