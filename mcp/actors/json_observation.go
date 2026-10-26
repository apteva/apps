package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
	"strconv"
	"strings"
)

// RFC 6901 paths select a document rendered by the authenticated browser.
// No scripts, cookies or state-changing requests are exposed.
type actorJSONDocument struct {
	Selector string                    `json:"selector"`
	Fields   map[string]actorJSONField `json:"fields"`
}
type actorJSONField struct {
	Unique   bool              `json:"unique,omitempty"`
	IsNull   bool              `json:"is_null,omitempty"`
	Path     string            `json:"path"`
	Required bool              `json:"required,omitempty"`
	Select   string            `json:"select,omitempty"`
	Resolve  *actorJSONResolve `json:"resolve,omitempty"`
}
type actorJSONResolve struct {
	Through  []string `json:"through,omitempty"`
	DropNull bool     `json:"drop_null,omitempty"`
	Pool     string   `json:"pool"`
	Keys     []string `json:"keys"`
}

func validJSONPointer(p string) bool {
	if p != "" && !strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '~' {
			if i+1 >= len(p) || (p[i+1] != '0' && p[i+1] != '1') {
				return false
			}
			i++
		}
	}
	return true
}
func validateJSONDocument(d *actorJSONDocument) error {
	if d == nil || d.Selector == "" || len(d.Fields) == 0 || len(d.Fields) > maxActorFields {
		return errors.New("observe_json requires one selector and 1-100 fields")
	}
	if _, err := cascadia.Compile(d.Selector); err != nil {
		return err
	}
	for k, f := range d.Fields {
		if k == "" || !validJSONPointer(f.Path) || !validJSONPointer(f.Select) {
			return errors.New("invalid JSON field or pointer")
		}
		if f.Resolve != nil {
			if f.Select == "" || !validJSONPointer(f.Resolve.Pool) || len(f.Resolve.Keys) == 0 || len(f.Resolve.Keys) > 10 || len(f.Resolve.Through) > 5 {
				return errors.New("JSON resolve requires a pool, keys and select pointer")
			}
			for _, key := range append(append([]string(nil), f.Resolve.Keys...), f.Resolve.Through...) {
				if key == "" || !validJSONPointer(key) {
					return errors.New("invalid JSON reference key")
				}
			}
		}
	}
	return nil
}
func jsonPointer(root any, path string) (any, bool) {
	v := root
	if path == "" {
		return v, true
	}
	for _, part := range strings.Split(path[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch n := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = n[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(n) || strconv.Itoa(i) != part {
				return nil, false
			}
			v = n[i]
		default:
			return nil, false
		}
	}
	return v, true
}
func jsonReferenceKey(v any, keys []string) (string, error) {
	parts := []any{}
	for _, k := range keys {
		value, ok := jsonPointer(v, k)
		if !ok || value == nil {
			return "", errors.New("reference key missing")
		}
		switch value.(type) {
		case string, float64:
		default:
			return "", errors.New("reference key must be scalar")
		}
		parts = append(parts, value)
	}
	b, _ := json.Marshal(parts)
	return string(b), nil
}
func extractJSONDocument(d *actorJSONDocument, raw string) (map[string]any, error) {
	var root any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, errors.New("rendered document is not complete valid JSON")
	}
	out := map[string]any{}
	var first error
	for name, f := range d.Fields {
		value, ok := jsonPointer(root, f.Path)
		if !ok {
			if f.Required && first == nil {
				first = fmt.Errorf("JSON field %s is missing at %s", name, f.Path)
			}
			continue
		}
		if f.IsNull {
			out[name] = value == nil
			continue
		}
		if f.Select == "" {
			out[name] = value
			continue
		}
		array, ok := value.([]any)
		if !ok {
			if first == nil {
				first = fmt.Errorf("JSON field %s must be an array", name)
			}
			continue
		}
		projected := []any{}
		var index map[string]any
		if f.Resolve != nil {
			pool, ok := jsonPointer(root, f.Resolve.Pool)
			entries, pok := pool.([]any)
			if !ok || !pok {
				if first == nil {
					first = fmt.Errorf("JSON field %s reference pool is missing", name)
				}
				continue
			}
			index = map[string]any{}
			for _, item := range entries {
				key, err := jsonReferenceKey(item, f.Resolve.Keys)
				if err != nil {
					if first == nil {
						first = err
					}
					continue
				}
				if _, exists := index[key]; exists {
					if first == nil {
						first = errors.New("ambiguous JSON reference pool")
					}
				}
				index[key] = item
			}
		}
		for _, item := range array {
			if index != nil {
				key, err := jsonReferenceKey(item, f.Resolve.Keys)
				resolved, exists := index[key]
				if err != nil || !exists {
					if first == nil {
						first = fmt.Errorf("JSON field %s has an unresolved reference", name)
					}
					break
				}
				item = resolved
				skip := false
				for _, pointer := range f.Resolve.Through {
					ref, ok := jsonPointer(item, pointer)
					if ok && ref == nil && f.Resolve.DropNull {
						skip = true
						break
					}
					if !ok {
						if first == nil {
							first = fmt.Errorf("JSON field %s reference chain is missing", name)
						}
						skip = true
						break
					}
					key, err := jsonReferenceKey(ref, f.Resolve.Keys)
					next, ok := index[key]
					if err != nil || !ok {
						if first == nil {
							first = fmt.Errorf("JSON field %s reference chain is unresolved", name)
						}
						skip = true
						break
					}
					item = next
				}
				if skip {
					continue
				}
			}
			v, exists := jsonPointer(item, f.Select)
			if !exists {
				if first == nil {
					first = fmt.Errorf("JSON field %s projection is missing", name)
				}
				break
			}
			projected = append(projected, v)
		}
		if f.Unique {
			seen := map[string]bool{}
			for _, v := range projected {
				b, _ := json.Marshal(v)
				key := string(b)
				if seen[key] && first == nil {
					first = fmt.Errorf("JSON field %s contains duplicate identities", name)
				}
				seen[key] = true
			}
		}
		out[name] = projected
	}
	return out, first
}
func (e *actorExecution) observeJSON(step actorStep) error {
	doc, err := e.completeRawDOM()
	if err != nil {
		return err
	}
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return err
	}
	nodes := cascadia.QueryAll(root, cascadia.MustCompile(step.JSONDocument.Selector))
	if len(nodes) != 1 {
		return fmt.Errorf("observe_json requires exactly one document, got %d", len(nodes))
	}
	var raw strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			raw.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(nodes[0])
	item, fieldErr := extractJSONDocument(step.JSONDocument, raw.String())
	if item == nil {
		return fieldErr
	}
	item["document_url"] = e.currentURL
	item["source_page_url"] = e.lastValues["current_url"]
	counts := map[string]int{}
	for k, v := range item {
		if array, ok := v.([]any); ok {
			counts[k] = len(array)
		}
	}
	item["field_counts"] = counts
	e.jsonEvidence = item
	b, _ := json.Marshal(item)
	if len(b) > maxActorItemBytes || e.datasetBytes+len(b)+1 > maxActorDatasetBytes || len(e.items) >= e.maxItems {
		return errors.New("JSON observation exceeds dataset limits")
	}
	if err := persistDatasetPage(e.ctx, e.run.ID, []map[string]any{item}); err != nil {
		return err
	}
	e.datasetBytes += len(b) + 1
	e.items = append(e.items, item)
	e.pageCount++
	for k, v := range item {
		e.lastValues[k] = v
	}
	return fieldErr
}
