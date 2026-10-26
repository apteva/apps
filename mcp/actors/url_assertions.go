package main

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
)

// Wrappers and source query parameters are explicit definition policy. The
// engine knows no provider hosts; every supplied source parameter must match.
type actorURLAssertion struct {
	URL             string            `json:"url"`
	AllowExtraQuery []string          `json:"allow_extra_query,omitempty"`
	Wrappers        []actorURLWrapper `json:"wrappers,omitempty"`
}
type actorURLWrapper struct {
	URL        string   `json:"url"`
	Parameters []string `json:"parameters"`
}

func strictAssertionURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("expected absolute HTTP(S) URL without credentials or fragment")
	}
	if _, err = url.ParseQuery(u.RawQuery); err != nil {
		return nil, errors.New("invalid URL query")
	}
	return u, nil
}
func (a actorURLAssertion) validate() error {
	if a.URL == "" {
		return errors.New("equals_url.url is required")
	}
	if !actorTemplateValue(a.URL) {
		if _, err := strictAssertionURL(a.URL); err != nil {
			return err
		}
	}
	for _, k := range a.AllowExtraQuery {
		if strings.TrimSpace(k) == "" {
			return errors.New("empty allowed query key")
		}
	}
	for _, w := range a.Wrappers {
		u, err := strictAssertionURL(w.URL)
		if err != nil || u.RawQuery != "" {
			return errors.New("wrapper must be an absolute URL without query")
		}
		if len(w.Parameters) == 0 {
			return errors.New("wrapper source parameters are required")
		}
		for _, k := range w.Parameters {
			if strings.TrimSpace(k) == "" {
				return errors.New("empty wrapper source parameter")
			}
		}
	}
	return nil
}
func sameURLLocation(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) && a.EscapedPath() == b.EscapedPath()
}
func (a actorURLAssertion) compare(raw string) error {
	if err := a.validate(); err != nil {
		return err
	}
	expected, err := strictAssertionURL(a.URL)
	if err != nil {
		return err
	}
	actual, err := strictAssertionURL(raw)
	if err != nil {
		return err
	}
	direct := func(u *url.URL) error {
		if !sameURLLocation(u, expected) {
			return errors.New("URL origin or path does not match")
		}
		q, _ := url.ParseQuery(u.RawQuery)
		eq, _ := url.ParseQuery(expected.RawQuery)
		for k, v := range eq {
			if !reflect.DeepEqual(q[k], v) {
				return errors.New("URL query does not match")
			}
		}
		for k := range q {
			if _, ok := eq[k]; ok {
				continue
			}
			allowed := false
			for _, x := range a.AllowExtraQuery {
				if k == x {
					allowed = true
					break
				}
			}
			if !allowed {
				return errors.New("unexpected URL query key")
			}
		}
		return nil
	}
	if sameURLLocation(actual, expected) {
		return direct(actual)
	}
	for _, w := range a.Wrappers {
		wrapper, _ := strictAssertionURL(w.URL)
		if !sameURLLocation(actual, wrapper) {
			continue
		}
		q, _ := url.ParseQuery(actual.RawQuery)
		sources := 0
		for _, k := range w.Parameters {
			values, exists := q[k]
			if !exists {
				continue
			}
			if len(values) != 1 || values[0] == "" {
				return errors.New("missing or ambiguous wrapped source")
			}
			source, err := strictAssertionURL(values[0])
			if err != nil {
				return err
			}
			if err = direct(source); err != nil {
				return fmt.Errorf("wrapped source %s: %w", k, err)
			}
			sources++
		}
		if sources == 0 {
			return errors.New("wrapper has no verified source URL")
		}
		return nil
	}
	return errors.New("URL is neither the expected source nor an allowed wrapper")
}
