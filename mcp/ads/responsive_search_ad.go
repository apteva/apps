package main

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Google has served Responsive Search Ads only since 2022. An RSA carries up
// to 15 headlines and 4 descriptions on the ad itself rather than the single
// headline/description pair a Meta creative holds, so it cannot be expressed
// through creative_create. These normalized fields build one without the
// caller hand-assembling a native payload.

const (
	rsaMinHeadlines        = 3
	rsaMaxHeadlines        = 15
	rsaMinDescriptions     = 2
	rsaMaxDescriptions     = 4
	rsaHeadlineMaxRunes    = 30
	rsaDescriptionMaxRunes = 90
	rsaPathMaxRunes        = 15
)

var rsaPinnedFields = map[string]string{
	"headline_1":    "HEADLINE_1",
	"headline_2":    "HEADLINE_2",
	"headline_3":    "HEADLINE_3",
	"description_1": "DESCRIPTION_1",
	"description_2": "DESCRIPTION_2",
}

// rsaTextAssets normalizes one headline or description list. Entries may be
// plain strings or {text, pinned_field} objects.
func rsaTextAssets(raw any, field string, minCount, maxCount, maxRunes int) ([]any, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", field)
	}
	if len(list) < minCount || len(list) > maxCount {
		return nil, fmt.Errorf("%s must contain between %d and %d entries, got %d",
			field, minCount, maxCount, len(list))
	}
	kind := strings.TrimSuffix(field, "s")
	assets := make([]any, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, entry := range list {
		item := asMap(entry)
		if len(item) == 0 {
			if text := strings.TrimSpace(toString(entry)); text != "" {
				item = map[string]any{"text": text}
			}
		}
		text := strings.TrimSpace(toString(item["text"]))
		if text == "" {
			return nil, fmt.Errorf("%s[%d].text is required", field, i)
		}
		if utf8.RuneCountInString(text) > maxRunes {
			return nil, fmt.Errorf("%s[%d].text must be %d characters or fewer", field, i, maxRunes)
		}
		// Google rejects an RSA whose assets repeat, so catch it here where the
		// message can name the offending index.
		if seen[strings.ToLower(text)] {
			return nil, fmt.Errorf("%s[%d].text duplicates an earlier entry", field, i)
		}
		seen[strings.ToLower(text)] = true
		asset := map[string]any{"text": text}
		if pin := strings.TrimSpace(strings.ToLower(toString(item["pinned_field"]))); pin != "" {
			mapped, ok := rsaPinnedFields[pin]
			if !ok {
				return nil, fmt.Errorf("%s[%d].pinned_field must be one of headline_1, headline_2, headline_3, description_1, description_2", field, i)
			}
			if !strings.HasPrefix(pin, kind+"_") {
				return nil, fmt.Errorf("%s[%d].pinned_field %s cannot pin a %s", field, i, pin, kind)
			}
			asset["pinnedField"] = mapped
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func rsaFinalURLs(raw any) ([]any, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("final_urls must contain at least one landing page URL")
	}
	urls := make([]any, 0, len(list))
	for i, entry := range list {
		value := strings.TrimSpace(toString(entry))
		if value == "" {
			return nil, fmt.Errorf("final_urls[%d] is empty", i)
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("final_urls[%d] must be an absolute http(s) URL", i)
		}
		urls = append(urls, value)
	}
	return urls, nil
}

// googleResponsiveSearchAd builds the native ad payload for ad_mutate from the
// normalized RSA fields.
func googleResponsiveSearchAd(args map[string]any) (map[string]any, error) {
	headlines, err := rsaTextAssets(args["headlines"], "headlines", rsaMinHeadlines, rsaMaxHeadlines, rsaHeadlineMaxRunes)
	if err != nil {
		return nil, err
	}
	descriptions, err := rsaTextAssets(args["descriptions"], "descriptions", rsaMinDescriptions, rsaMaxDescriptions, rsaDescriptionMaxRunes)
	if err != nil {
		return nil, err
	}
	finalURLs, err := rsaFinalURLs(args["final_urls"])
	if err != nil {
		return nil, err
	}
	rsa := map[string]any{"headlines": headlines, "descriptions": descriptions}
	path1 := strings.TrimSpace(stringArgAny(args, "path1"))
	path2 := strings.TrimSpace(stringArgAny(args, "path2"))
	if path2 != "" && path1 == "" {
		return nil, fmt.Errorf("path2 requires path1")
	}
	for name, value := range map[string]string{"path1": path1, "path2": path2} {
		if value == "" {
			continue
		}
		if utf8.RuneCountInString(value) > rsaPathMaxRunes {
			return nil, fmt.Errorf("%s must be %d characters or fewer", name, rsaPathMaxRunes)
		}
		if strings.ContainsAny(value, "/ ?&#") {
			return nil, fmt.Errorf("%s is a display path segment and cannot contain /, spaces or query syntax", name)
		}
		rsa[name] = value
	}
	ad := map[string]any{"responsiveSearchAd": rsa, "finalUrls": finalURLs}
	if name := strings.TrimSpace(stringArgAny(args, "name")); name != "" {
		ad["name"] = name
	}
	return ad, nil
}

// responsiveSearchAdRequested reports whether the caller supplied any
// normalized RSA field, so a partial payload fails loudly instead of falling
// through to the "supply platform_options.ad" message.
func responsiveSearchAdRequested(args map[string]any) bool {
	for _, key := range []string{"headlines", "descriptions", "final_urls", "path1", "path2"} {
		if args[key] != nil {
			return true
		}
	}
	return false
}
