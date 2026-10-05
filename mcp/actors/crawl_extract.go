package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// Sources select either the current item, the page DOM, or queue metadata.
// $index in selectors expands to the one-based repeated item index.
type crawlField struct {
	actorField
	Source   string `json:"source,omitempty"`
	Value    any    `json:"value,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Group    int    `json:"group,omitempty"`
	Nullable bool   `json:"nullable,omitempty"`
}

type crawlRecord struct {
	Dataset string
	Key     string
	Item    map[string]any
}
type crawlLink struct {
	URL, Route, ParentKey string
	Depth                 int
}
type crawlPage struct {
	Records []crawlRecord
	Links   []crawlLink
}

func extractCrawlPage(c crawlDefinition, route crawlRoute, item *crawlQueueItem, doc *browserExtractResult) (*crawlPage, error) {
	if doc.Truncated {
		return nil, errors.New("Computer returned truncated HTML; reduce page size rather than committing incomplete data")
	}
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return nil, err
	}
	page := &crawlPage{}
	baseURL := firstNonEmpty(doc.CurrentURL, doc.URL, item.URL)
	for _, extract := range route.Extract {
		matcher, err := cascadia.Compile(extract.Items)
		if err != nil {
			return nil, err
		}
		nodes := cascadia.QueryAll(root, matcher)
		if extract.Required && len(nodes) == 0 {
			return nil, fmt.Errorf("required dataset %s matched no rows", extract.Dataset)
		}
		repeat := max(1, extract.Repeat)
		for rowIndex, node := range nodes {
			for side := 1; side <= repeat; side++ {
				record := map[string]any{}
				for name, field := range extract.Fields {
					value, err := extractCrawlField(node, root, field, item, baseURL, side)
					if err != nil {
						return nil, fmt.Errorf("dataset %s field %s: %w", extract.Dataset, name, err)
					}
					if transform := extract.Transforms[name]; transform != "" && value != nil {
						value, err = transformCrawlValue(fmt.Sprint(value), transform)
						if err != nil {
							return nil, fmt.Errorf("dataset %s field %s: %w", extract.Dataset, name, err)
						}
					}
					record[name] = value
				}
				schema := c.Datasets[extract.Dataset]
				if err := validateCrawlRecord(record, schema.Schema); err != nil {
					return nil, err
				}
				fields := schema.KeyFields
				if len(fields) == 0 {
					if key := firstNonEmpty(schema.Key, extract.Key); key != "" {
						fields = []string{key}
					}
				}
				key := ""
				if len(fields) > 0 {
					values := []any{}
					for _, field := range fields {
						value, ok := record[field]
						if !ok || value == nil || fmt.Sprint(value) == "" {
							return nil, fmt.Errorf("dataset %s key field %s is empty", extract.Dataset, field)
						}
						values = append(values, value)
					}
					raw, _ := json.Marshal(values)
					key = string(raw)
				} else {
					key = fmt.Sprintf("%s#%d:%d", canonicalCrawlURL(baseURL), rowIndex, side)
				}
				page.Records = append(page.Records, crawlRecord{extract.Dataset, key, record})
				if item.Depth < c.Frontier.MaxDepth {
					for _, follow := range route.Follow {
						if value, ok := record[follow.Field]; ok && value != nil && fmt.Sprint(value) != "" {
							target, err := resolveCrawlURL(fmt.Sprint(value), baseURL)
							if err != nil {
								return nil, err
							}
							page.Links = append(page.Links, crawlLink{target, follow.Route, key, item.Depth + 1})
						}
					}
				}
			}
		}
	}
	// Numbered pagination links enter the same durable frontier at the same depth.
	if route.Paginate != nil {
		matcher, err := cascadia.Compile(route.Paginate.Locator.Selector)
		if err != nil {
			return nil, err
		}
		for _, node := range cascadia.QueryAll(root, matcher) {
			raw, _ := htmlAttribute(node, "href")
			if raw == "" {
				continue
			}
			target, err := resolveCrawlURL(raw, baseURL)
			if err != nil {
				return nil, err
			}
			page.Links = append(page.Links, crawlLink{target, item.Route, item.ParentKey, item.Depth})
		}
	}
	return page, nil
}

func extractCrawlField(node, root *html.Node, field crawlField, item *crawlQueueItem, baseURL string, index int) (any, error) {
	if field.Source == "value" {
		return field.Value, nil
	}
	raw := ""
	switch field.Source {
	case "url":
		raw = baseURL
	case "parent_key":
		raw = item.ParentKey
	case "index":
		return int64(index), nil
	case "", "page":
		target := node
		if field.Source == "page" {
			target = root
		}
		if field.Selector != "" {
			selector := strings.ReplaceAll(field.Selector, "$index", strconv.Itoa(index))
			matcher, err := cascadia.Compile(selector)
			if err != nil {
				return nil, err
			}
			target = cascadia.Query(target, matcher)
		}
		if target == nil {
			if field.Required {
				return nil, errors.New("required selector matched no element")
			}
			return nil, nil
		}
		if field.Attribute != "" {
			raw, _ = htmlAttribute(target, field.Attribute)
		} else {
			raw = htmlNodeText(target)
		}
	default:
		return nil, fmt.Errorf("unknown field source %s", field.Source)
	}
	raw = strings.TrimSpace(raw)
	if field.Nullable && (raw == "" || raw == "--" || raw == "---" || raw == "N/A") {
		return nil, nil
	}
	if field.Required && raw == "" {
		return nil, errors.New("required field is empty")
	}
	if field.Pattern != "" {
		pattern, err := regexp.Compile(field.Pattern)
		if err != nil {
			return nil, err
		}
		matches := pattern.FindStringSubmatch(raw)
		if len(matches) <= field.Group {
			if field.Nullable && !field.Required {
				return nil, nil
			}
			return nil, fmt.Errorf("pattern did not match %q", raw)
		}
		raw = matches[field.Group]
	}
	return coerceActorValue(raw, field.Type, baseURL)
}

func resolveCrawlURL(raw, base string) (string, error) {
	ref, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	origin, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	target := origin.ResolveReference(ref).String()
	if err := validateHTTPURL(target); err != nil {
		return "", err
	}
	return canonicalCrawlURL(target), nil
}

func validateCrawlRecord(record map[string]any, schema map[string]string) error {
	for field, typ := range schema {
		value, exists := record[field]
		nullable := strings.HasSuffix(typ, "?")
		typ = strings.TrimSuffix(typ, "?")
		if (!exists || value == nil) && nullable {
			continue
		}
		if !exists || value == nil {
			return fmt.Errorf("dataset field %s is missing", field)
		}
		valid := false
		switch typ {
		case "string":
			_, valid = value.(string)
		case "date":
			s, ok := value.(string)
			_, err := time.Parse("2006-01-02", s)
			valid = ok && err == nil
		case "url":
			s, ok := value.(string)
			valid = ok && validateHTTPURL(s) == nil
		case "boolean":
			_, valid = value.(bool)
		case "integer":
			n, ok := numericValue(value)
			valid = ok && math.Trunc(n) == n
		case "number":
			_, valid = numericValue(value)
		case "object":
			_, valid = value.(map[string]any)
		case "array":
			_, valid = value.([]any)
		}
		if !valid {
			return fmt.Errorf("dataset field %s does not satisfy %s", field, typ)
		}
	}
	return nil
}

var crawlDurationPattern = regexp.MustCompile(`^(\d+):(\d{1,2})$`)
var crawlRatioPattern = regexp.MustCompile(`^\s*([0-9]+)\s+of\s+([0-9]+)\s*$`)
var crawlHeightPattern = regexp.MustCompile(`^(\d+)'\s*(\d+)"$`)

func transformCrawlValue(raw, transform string) (any, error) {
	raw = strings.TrimSpace(raw)
	switch transform {
	case "trim":
		return raw, nil
	case "lowercase":
		return strings.ToLower(raw), nil
	case "integer":
		return strconv.ParseInt(raw, 10, 64)
	case "number":
		return strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
	case "url_id":
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		return path.Base(strings.TrimSuffix(u.Path, "/")), nil
	case "percent":
		return strconv.ParseFloat(strings.TrimSuffix(raw, "%"), 64)
	case "weight_lbs":
		return strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "lbs.")), 64)
	case "height_inches":
		match := crawlHeightPattern.FindStringSubmatch(raw)
		if len(match) != 3 {
			return nil, fmt.Errorf("cannot parse height %q", raw)
		}
		feet, _ := strconv.Atoi(match[1])
		inches, _ := strconv.Atoi(match[2])
		if inches > 11 {
			return nil, errors.New("height inches out of range")
		}
		return int64(feet*12 + inches), nil
	case "date":
		for _, layout := range []string{"January 02, 2006", "January 2, 2006", "Jan. 02, 2006", "Jan. 2, 2006", "Jan 02, 2006", "Jan 2, 2006", "2006-01-02"} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.Format("2006-01-02"), nil
			}
		}
	case "duration_seconds":
		match := crawlDurationPattern.FindStringSubmatch(raw)
		if len(match) != 3 {
			break
		}
		minutes, _ := strconv.Atoi(match[1])
		seconds, _ := strconv.Atoi(match[2])
		if seconds > 59 {
			return nil, errors.New("duration seconds out of range")
		}
		return int64(minutes*60 + seconds), nil
	case "ratio_landed", "ratio_attempted":
		match := crawlRatioPattern.FindStringSubmatch(raw)
		if len(match) != 3 {
			break
		}
		index := 1
		if transform == "ratio_attempted" {
			index = 2
		}
		return strconv.ParseInt(match[index], 10, 64)
	default:
		return nil, fmt.Errorf("unsupported transform %q", transform)
	}
	return nil, fmt.Errorf("cannot parse %q as %s", raw, transform)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
