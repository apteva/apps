package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	sdk "github.com/apteva/app-sdk"
	"golang.org/x/net/html"
)

// View traversal is site-neutral. It only follows observed anchors, scrolls,
// and optionally clicks pagination controls whose fresh SOM effect is navigation_only.
var errReadCollectionBound = errors.New("read collection bound reached")
var errReadPaginationStalled = errors.New("pagination did not advance after verified wait")

type actorReadViews struct {
	AllowPartial bool              `json:"allow_partial,omitempty"`
	EntryQuery   map[string]string `json:"entry_query,omitempty"`
	EntryURL     string            `json:"entry_url"`
	IdentityURL  string            `json:"identity_url"`
	Identity     actorField        `json:"identity"`
	Views        []actorReadView   `json:"views"`
	TimeoutMS    int               `json:"timeout_ms,omitempty"`
}
type actorReadView struct {
	UseEntryPage     bool                        `json:"use_entry_page,omitempty"`
	Total            *actorField                 `json:"total,omitempty"`
	Name             string                      `json:"name"`
	Covers           []string                    `json:"covers"`
	LinkSelector     string                      `json:"link_selector"`
	URLPattern       string                      `json:"url_pattern"`
	ReadySelector    string                      `json:"ready_selector"`
	EmptyTextPattern string                      `json:"empty_text_pattern,omitempty"`
	EmptySelector    string                      `json:"empty_selector"`
	LoadingSelector  string                      `json:"loading_selector"`
	ErrorSelector    string                      `json:"error_selector,omitempty"`
	Items            string                      `json:"items"`
	Fields           map[string]actorField       `json:"fields"`
	Defaults         map[string]any              `json:"field_defaults,omitempty"`
	Rewrites         map[string]actorReadRewrite `json:"field_rewrites,omitempty"`
	KeyField         string                      `json:"key_field"`
	Pagination       actorReadPagination         `json:"pagination"`
}
type actorReadRewrite struct {
	From        string `json:"from"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	WhenField   string `json:"when_field,omitempty"`
	Equals      string `json:"equals,omitempty"`
}
type actorReadPagination struct {
	OnStall           string       `json:"on_stall,omitempty"`
	AdvanceTimeoutMS  int          `json:"advance_timeout_ms,omitempty"`
	RevealFrom        string       `json:"reveal_from,omitempty"`
	EndWhenNextAbsent bool         `json:"end_when_next_absent,omitempty"`
	Mode              string       `json:"mode"`
	Next              actorLocator `json:"next,omitempty"`
	EndSelector       string       `json:"end_selector,omitempty"`
	ScrollTargetName  string       `json:"scroll_target_name,omitempty"`
	Amount            int          `json:"amount,omitempty"`
	MaxPages          any          `json:"max_pages,omitempty"`
	StableRounds      int          `json:"stable_rounds,omitempty"`
	SettleMS          int          `json:"settle_ms,omitempty"`
}
type actorReadCoverage struct {
	Complete      bool                 `json:"inspection_complete"`
	ReadOnly      bool                 `json:"read_only"`
	MoreRemaining bool                 `json:"more_results_remaining"`
	CheckedViews  []string             `json:"checked_views"`
	RequiredViews []string             `json:"required_views"`
	Views         []*actorViewCoverage `json:"views"`
	IdentityURL   string               `json:"creator_url,omitempty"`
	IdentityToken string               `json:"creator_identity,omitempty"`
	ContextID     string               `json:"context_id,omitempty"`
}
type actorViewCoverage struct {
	ExpectedTotal *int     `json:"expected_total,omitempty"`
	Name          string   `json:"name"`
	Covers        []string `json:"covers"`
	URL           string   `json:"url,omitempty"`
	Checked       bool     `json:"checked"`
	Complete      bool     `json:"inspection_complete"`
	MoreRemaining bool     `json:"more_results_remaining"`
	Pages         int      `json:"pages_checked"`
	Items         int      `json:"unique_items"`
	EndEvidence   string   `json:"end_evidence,omitempty"`
	Error         string   `json:"error,omitempty"`
}

func readOnlyActorAction(action string) bool {
	switch action {
	case "goto", "wait", "wait_for", "extract", "assert_element", "assert_url", "assert_values", "screenshot", "inspect_views", "observe_page", "select_record":
		return true
	}
	return false
}
func validateReadViews(c *actorReadViews) error {
	if c == nil || c.EntryURL == "" || c.IdentityURL == "" || c.Identity.Selector == "" || c.Identity.Attribute != "href" || !c.Identity.Required {
		return errors.New("read_views requires entry_url, identity_url and a required identity href field")
	}
	if c.Identity.Pattern != "" {
		if _, err := regexp.Compile(c.Identity.Pattern); err != nil {
			return err
		}
	}
	if len(c.Views) == 0 || len(c.Views) > 20 {
		return errors.New("read_views requires 1-20 views")
	}
	names := map[string]bool{}
	for _, v := range c.Views {
		if v.Pagination.RevealFrom != "" && v.Pagination.RevealFrom != "start" && v.Pagination.RevealFrom != "end" {
			return errors.New("pagination reveal_from must be start or end")
		}
		if v.Pagination.OnStall != "" && v.Pagination.OnStall != "fail" && v.Pagination.OnStall != "partial" {
			return errors.New("pagination on_stall must be fail or partial")
		}
		if v.Pagination.OnStall == "partial" && !c.AllowPartial {
			return errors.New("partial stalled pagination requires allow_partial")
		}
		if v.Pagination.AdvanceTimeoutMS != 0 && (v.Pagination.AdvanceTimeoutMS < 500 || v.Pagination.AdvanceTimeoutMS > 60000) {
			return errors.New("pagination advance_timeout_ms must be between 500 and 60000")
		}
		if v.Name == "" || names[v.Name] || len(v.Covers) == 0 || len(v.Covers) > 20 || v.KeyField == "" || len(v.Fields) == 0 || len(v.Fields) > maxActorFields {
			return errors.New("read view requires unique name, covers, key_field and bounded fields")
		}
		names[v.Name] = true
		if f, ok := v.Fields[v.KeyField]; !ok || !f.Required {
			return errors.New("read view key field must be required")
		}
		requiredSelectors := []string{c.Identity.Selector, v.ReadySelector, v.EmptySelector, v.LoadingSelector, v.Items}
		if !v.UseEntryPage {
			requiredSelectors = append(requiredSelectors, v.LinkSelector)
		}
		for _, sel := range requiredSelectors {
			if sel == "" {
				return errors.New("read view requires link, ready, empty, loading and items selectors")
			}
			if _, err := cascadia.Compile(sel); err != nil {
				return err
			}
		}
		for _, sel := range []string{v.ErrorSelector, v.Pagination.EndSelector} {
			if sel != "" {
				if _, err := cascadia.Compile(sel); err != nil {
					return err
				}
			}
		}
		if v.URLPattern == "" {
			return errors.New("read view url_pattern required")
		}
		if _, err := regexp.Compile(v.URLPattern); err != nil {
			return err
		}
		if v.EmptyTextPattern != "" {
			if _, err := regexp.Compile(v.EmptyTextPattern); err != nil {
				return err
			}
		}
		validationFields := map[string]actorField{}
		for k, f := range v.Fields {
			validationFields[k] = f
		}
		if v.Total != nil {
			if !v.Total.Required || v.Total.Type != "number" {
				return errors.New("read view total must be a required number field")
			}
			validationFields["__total"] = *v.Total
		}
		for _, f := range validationFields {
			if f.Pattern != "" {
				if _, err := regexp.Compile(f.Pattern); err != nil {
					return err
				}
			}
			if f.Selector != "" {
				if _, err := cascadia.Compile(f.Selector); err != nil {
					return err
				}
			}
		}
		for _, r := range v.Rewrites {
			if r.From == "" {
				return errors.New("field rewrite requires from")
			}
			if _, err := regexp.Compile(r.Pattern); err != nil {
				return err
			}
		}
		if len(v.Rewrites) > maxActorFields || len(v.Defaults) > maxActorFields {
			return errors.New("read view defaults/rewrites exceed field limit")
		}
		switch v.Pagination.Mode {
		case "scroll":
			if v.Pagination.Next.Text != "" && (!v.Pagination.Next.SOMOnly || !v.Pagination.Next.Exact) {
				return errors.New("scroll next requires exact som_only locator")
			}
			if v.Pagination.ScrollTargetName == "" {
				return errors.New("scroll pagination requires scroll_target_name")
			}
		case "next":
			if !v.Pagination.Next.SOMOnly || !v.Pagination.Next.Exact || v.Pagination.Next.Text == "" || (v.Pagination.EndSelector == "" && !v.Pagination.EndWhenNextAbsent) {
				return errors.New("next pagination requires exact som_only next and explicit end evidence")
			}
		default:
			return errors.New("read view pagination mode must be scroll or next")
		}
		if v.Pagination.EndWhenNextAbsent {
			if v.Pagination.Mode != "next" || v.Pagination.Next.Selector == "" {
				return errors.New("end_when_next_absent requires next pagination with a DOM selector")
			}
			if _, err := cascadia.Compile(v.Pagination.Next.Selector); err != nil {
				return err
			}
		}
		if (!actorTemplateValue(stringFromAny(v.Pagination.MaxPages)) && (templateInt(v.Pagination.MaxPages) < 1 || templateInt(v.Pagination.MaxPages) > maxActorPages)) || v.Pagination.StableRounds < 2 || v.Pagination.StableRounds > 10 || v.Pagination.SettleMS < 500 || v.Pagination.SettleMS > 10000 {
			return errors.New("invalid read view pagination bounds")
		}
	}
	if c.TimeoutMS < 1000 || c.TimeoutMS > 60000 {
		return errors.New("read_views timeout_ms must be 1000-60000")
	}
	return nil
}
func readHas(root *html.Node, selector string) bool {
	if selector == "" {
		return false
	}
	sel, err := cascadia.Compile(selector)
	return err == nil && cascadia.Query(root, sel) != nil
}
func readVerifiedEmpty(root *html.Node, v actorReadView) bool {
	sel, err := cascadia.Compile(v.EmptySelector)
	if err != nil {
		return false
	}
	for _, node := range cascadia.QueryAll(root, sel) {
		if v.EmptyTextPattern == "" || regexp.MustCompile(v.EmptyTextPattern).MatchString(htmlNodeText(node)) {
			return true
		}
	}
	return false
}
func identityToken(raw, pattern, base string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	raw = b.ResolveReference(u).String()
	if pattern != "" {
		m := regexp.MustCompile(pattern).FindStringSubmatch(raw)
		if len(m) < 2 || m[1] == "" {
			return "", errors.New("creator identity pattern did not match")
		}
		return m[1], nil
	}
	return raw, nil
}
func verifyReadIdentity(c actorReadViews, root *html.Node, base string) (string, error) {
	selector, _ := cascadia.Compile(c.Identity.Selector)
	nodes := cascadia.QueryAll(root, selector)
	if len(nodes) != 1 {
		return "", fmt.Errorf("creator identity selector matched %d links", len(nodes))
	}
	raw, _ := htmlAttribute(nodes[0], "href")
	got, err := identityToken(raw, c.Identity.Pattern, base)
	if err != nil {
		return "", err
	}
	want, err := identityToken(c.IdentityURL, c.Identity.Pattern, base)
	if err != nil {
		return "", err
	}
	gu, _ := url.Parse(raw)
	bu, _ := url.Parse(base)
	eu, _ := url.Parse(c.IdentityURL)
	if got != want || bu.ResolveReference(gu).Host != eu.Host {
		return "", fmt.Errorf("creator identity mismatch: got %q, expected %q", got, want)
	}
	return got, nil
}
func (e *actorExecution) readViewDOM(c actorReadViews, v actorReadView) (*html.Node, error) {
	until := time.Now().Add(time.Duration(c.TimeoutMS) * time.Millisecond)
	for {
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
		var doc *browserExtractResult
		var err error
		for _, limit := range []int{200000, 400000, 800000, 1000000} {
			doc, err = e.extractDOMOptions(map[string]any{"formats": []string{"html"}, "readability": false, "max_chars": limit, "wait_ms": 250})
			if err != nil {
				return nil, err
			}
			if !doc.Truncated {
				break
			}
		}
		if doc.Truncated {
			return nil, errors.New("read view HTML truncated at 1 MB; coverage incomplete")
		}
		root, err := html.Parse(strings.NewReader(doc.HTML))
		if err != nil {
			return nil, err
		}

		if v.ErrorSelector != "" && readHas(root, v.ErrorSelector) {
			return nil, errors.New("view displays an error; coverage incomplete")
		}
		if !regexp.MustCompile(v.URLPattern).MatchString(e.currentURL) {
			return nil, fmt.Errorf("unverified view URL %s", e.currentURL)
		}
		if readHas(root, v.ReadySelector) && !readHas(root, v.LoadingSelector) {
			token, err := verifyReadIdentity(c, root, e.currentURL)
			if err != nil {
				return nil, err
			}
			e.coverage.IdentityToken = token
			return root, nil
		}
		if time.Now().After(until) {
			return nil, errors.New("view not ready or still loading; coverage incomplete")
		}
		if err := e.readPause(500); err != nil {
			return nil, err
		}
	}
}
func (e *actorExecution) readPause(ms int) error {
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-e.workerCtx.Done():
		return e.workerCtx.Err()
	case <-timer.C:
		return e.checkpoint()
	}
}
func (e *actorExecution) inspectViews(c actorReadViews) error {
	if len(c.EntryQuery) > 0 {
		u, err := url.Parse(c.EntryURL)
		if err != nil {
			return err
		}
		q := u.Query()
		for k, v := range c.EntryQuery {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		c.EntryURL = u.String()
	}
	cov := &actorReadCoverage{ReadOnly: true, MoreRemaining: true, Views: []*actorViewCoverage{}, CheckedViews: []string{}, RequiredViews: []string{}, ContextID: e.definition.Browser.ContextID, IdentityURL: c.IdentityURL}
	e.coverage = cov
	for _, v := range c.Views {
		cov.Views = append(cov.Views, &actorViewCoverage{Name: v.Name, Covers: v.Covers, MoreRemaining: true})
		cov.RequiredViews = append(cov.RequiredViews, v.Covers...)
	}
	seen := map[string]map[string]any{}
	failed := false
	partialOnly := true
	for i, v := range c.Views {
		result := cov.Views[i]
		err := e.inspectOneView(c, v, result, seen)
		if err != nil {
			result.Error = err.Error()
			failed = true
			verifiedPartial := errors.Is(err, errReadCollectionBound) || (errors.Is(err, errReadPaginationStalled) && v.Pagination.OnStall == "partial" && result.Items > 0)
			if !verifiedPartial || !result.Checked {
				partialOnly = false
			}
			e.addTrace("inspect_views.view", "incomplete", err.Error(), map[string]any{"view": v.Name})
			if errors.Is(err, errActorCancelled) || e.workerCtx.Err() != nil {
				return err
			}
		}
		if result.Checked {
			cov.CheckedViews = append(cov.CheckedViews, v.Covers...)
		}
	}
	cov.Complete = !failed
	cov.MoreRemaining = false
	for _, v := range cov.Views {
		cov.Complete = cov.Complete && v.Complete
		cov.MoreRemaining = cov.MoreRemaining || v.MoreRemaining
	}
	if !cov.Complete && !(c.AllowPartial && partialOnly) {
		return errors.New("read-only view coverage incomplete; do not infer absence or create new content")
	}
	return nil
}
func (e *actorExecution) inspectOneView(c actorReadViews, v actorReadView, result *actorViewCoverage, seen map[string]map[string]any) error {
	if err := e.gotoURL(c.EntryURL); err != nil {
		return err
	}
	var root *html.Node
	var err error
	if !v.UseEntryPage {
		// Navigation links must be observed in the assigned context, never guessed.
		entry := v
		entry.URLPattern = "^" + regexp.QuoteMeta(c.EntryURL) + "/?$"
		root, err = e.readViewDOM(c, entry)
		if err != nil {
			return err
		}
		sel, _ := cascadia.Compile(v.LinkSelector)
		links := cascadia.QueryAll(root, sel)
		if len(links) != 1 || links[0].Data != "a" {
			return fmt.Errorf("verified navigation link matched %d anchors", len(links))
		}
		href, _ := htmlAttribute(links[0], "href")
		if href == "" {
			return errors.New("view navigation href missing")
		}
		targetAny, err := coerceActorValue(href, "url", e.currentURL)
		if err != nil {
			return err
		}
		target := stringFromAny(targetAny)
		if err := e.gotoURL(target); err != nil {
			return err
		}
	}
	result.URL = e.currentURL
	limit := minInt(templateInt(v.Pagination.MaxPages), e.maxPages)
	viewSeen := map[string]bool{}
	stable := 0
	lastSignature := ""
	lastGeometry := ""
	pendingNextSignature := ""
	var pendingNextDeadline time.Time
	for page := 0; page < limit; page++ {
		if e.pageCount >= e.maxPages {
			return fmt.Errorf("%w: global page limit; more results may remain", errReadCollectionBound)
		}
		root, err = e.readViewDOM(c, v)
		if err != nil {
			return err
		}
		result.Checked = true
		result.URL = e.currentURL
		selector, _ := cascadia.Compile(v.Items)
		nodes := cascadia.QueryAll(root, selector)
		if len(nodes) == 0 && !readVerifiedEmpty(root, v) {
			return errors.New("empty collection has no verified empty-state marker")
		}
		var keys []string
		var newItems []map[string]any
		collectionBound := false
		for _, node := range nodes {
			item, err := extractNodeItem(node, v.Fields, e.currentURL)
			if err != nil {
				return err
			}
			for k, val := range v.Defaults {
				if stringFromAny(item[k]) == "" {
					item[k] = val
				}
			}
			sourceItem := map[string]any{}
			for k, val := range item {
				sourceItem[k] = val
			}
			for k, r := range v.Rewrites {
				if r.WhenField != "" && stringFromAny(sourceItem[r.WhenField]) != r.Equals {
					continue
				}
				raw, ok := sourceItem[r.From]
				if !ok {
					return fmt.Errorf("rewrite source %s missing", r.From)
				}
				item[k] = regexp.MustCompile(r.Pattern).ReplaceAllString(stringFromAny(raw), r.Replacement)
			}
			key := stringFromAny(item[v.KeyField])
			if key == "" {
				return errors.New("empty deduplication key")
			}
			keys = append(keys, key)
			if old, ok := seen[key]; ok {
				oldJSON, _ := json.Marshal(old)
				newJSON, _ := json.Marshal(item)
				if string(oldJSON) != string(newJSON) {
					return errors.New("collection changed during inspection; reconcile overlapping statuses before writing")
				}
				viewSeen[key] = true
				continue
			}
			item, err = validateOutputItem(item, e.definition.OutputSchema)
			if err != nil {
				return err
			}
			b, _ := json.Marshal(item)
			if len(b) > maxActorItemBytes {
				return errors.New("item byte limit reached")
			}
			if len(e.items)+len(newItems) >= e.maxItems || e.datasetBytes+len(b)+1 > maxActorDatasetBytes {
				collectionBound = true
				break
			}
			viewSeen[key] = true
			e.datasetBytes += len(b) + 1
			seen[key] = item
			newItems = append(newItems, item)
		}
		sort.Strings(keys) // DOM reordering alone is not pagination progress
		if pendingNextSignature != "" {
			if strings.Join(keys, ",") == pendingNextSignature {
				// An unchanged first AJAX response is not an exhausted collection.
				// Re-read the verified identity/view without clicking again. Explicit
				// terminal DOM evidence takes precedence over a stalled signature.
				ended := (v.Pagination.EndSelector != "" && readHas(root, v.Pagination.EndSelector)) || (v.Pagination.Mode == "next" && v.Pagination.EndWhenNextAbsent && !readHas(root, v.Pagination.Next.Selector))
				if !ended {
					if !time.Now().Before(pendingNextDeadline) {
						return fmt.Errorf("%w; coverage incomplete", errReadPaginationStalled)
					}
					if err := e.readPause(500); err != nil {
						return err
					}
					page-- // waiting does not consume a dataset page or page bound
					continue
				}
			}
			pendingNextSignature = ""
		}
		if err := persistDatasetPage(e.ctx, e.run.ID, newItems); err != nil {
			return err
		}
		e.items = append(e.items, newItems...)
		e.pageCount++
		result.Pages++
		result.Items = len(viewSeen)
		e.persistProgress("inspecting " + v.Name)
		if collectionBound {
			return fmt.Errorf("%w: dataset limit; more results remain", errReadCollectionBound)
		}
		if v.Pagination.EndSelector != "" && readHas(root, v.Pagination.EndSelector) {
			return completeReadView(v, result, root, len(viewSeen), e.currentURL, "explicit_end_selector")
		}
		if v.Pagination.Mode == "next" {
			if v.Pagination.EndWhenNextAbsent && !readHas(root, v.Pagination.Next.Selector) {
				if strings.Join(keys, ",") == lastSignature {
					stable++
				} else {
					stable = 0
				}
				lastSignature = strings.Join(keys, ",")
				if stable >= v.Pagination.StableRounds {
					return completeReadView(v, result, root, len(viewSeen), e.currentURL, "verified_next_absent_stable_no_loading")
				}
				if err := e.readPause(v.Pagination.SettleMS); err != nil {
					return err
				}
				continue
			}
			if page+1 >= limit {
				return fmt.Errorf("%w: pagination limit; more results may remain", errReadCollectionBound)
			}
			stable = 0
			pendingNextSignature = strings.Join(keys, ",")
			if err := e.clickReadNavigation(v.Pagination.Next, v.Pagination.RevealFrom); err != nil {
				return err
			}
			pendingNextDeadline = time.Now().Add(time.Duration(boundedInt(v.Pagination.AdvanceTimeoutMS, 10000, 500, 60000)) * time.Millisecond)
		} else {
			shot, err := e.readScrollSnapshot()
			if err != nil {
				return err
			}
			var target *readScrollRegion
			for j := range shot.Regions {
				r := &shot.Regions[j]
				if r.Name == v.Pagination.ScrollTargetName {
					if target != nil {
						return errors.New("ambiguous scroll region")
					}
					target = r
				}
			}
			if target == nil {
				return errors.New("verified scroll region not found")
			}
			signature := strings.Join(keys, ",")
			geometry := fmt.Sprintf("%.0f:%.0f", target.Top, target.MaxY)
			atEnd := target.Top >= target.MaxY-1
			if atEnd && v.Pagination.Next.Text != "" {
				var matches []setOfMarkTarget
				for _, t := range shot.SOM {
					if somTargetMatches(v.Pagination.Next, t) {
						matches = append(matches, t)
					}
				}
				if len(matches) > 1 {
					return errors.New("ambiguous pagination control; coverage incomplete")
				}
				if len(matches) == 1 && !matches[0].Disabled {
					if err := e.clickReadNavigation(v.Pagination.Next, v.Pagination.RevealFrom); err != nil {
						return err
					}
					pendingNextSignature = signature
					pendingNextDeadline = time.Now().Add(time.Duration(boundedInt(v.Pagination.AdvanceTimeoutMS, 10000, 500, 60000)) * time.Millisecond)
					stable, lastSignature, lastGeometry = 0, "", ""
					if err := e.readPause(v.Pagination.SettleMS); err != nil {
						return err
					}
					continue
				}
			}
			if atEnd && signature == lastSignature && geometry == lastGeometry && len(newItems) == 0 {
				stable++
			} else {
				stable = 0
			}
			lastSignature = signature
			lastGeometry = geometry
			if stable >= v.Pagination.StableRounds {
				return completeReadView(v, result, root, len(viewSeen), e.currentURL, "verified_scroll_end_stable_no_loading")
			}
			amount := v.Pagination.Amount
			if amount <= 0 {
				amount = 600
			}
			var out map[string]any
			args := withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "scroll", "target_id": target.ID, "som_revision": shot.Revision, "expected_name": target.Name, "expected_role": target.Role, "direction": "down", "amount": amount})
			if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", args, &out); err != nil {
				return err
			}
			sc := mapFromAny(out["scroll"])
			if sc["wrong_target"] == true || sc["ambiguous"] == true || stringFromAny(sc["actual_target_id"]) != target.ID {
				return errors.New("scroll target was not verified")
			}
		}
		if err := e.readPause(v.Pagination.SettleMS); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: pagination limit; more results may remain", errReadCollectionBound)
}

type readScrollRegion struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Role   string  `json:"role"`
	Top    float64 `json:"scroll_top"`
	MaxY   float64 `json:"max_scroll_y"`
	Height float64 `json:"h"`
}
type readScrollShot struct {
	MediaIframeSrc     string             `json:"media_iframe_src"`
	MediaEmbedStatus   string             `json:"media_embed_status"`
	MediaProvider      string             `json:"media_provider"`
	MediaErrorText     string             `json:"media_error_text"`
	MediaPlayerVisible bool               `json:"media_player_visible"`
	MediaIframeVisible bool               `json:"media_iframe_visible"`
	DraftSaveState     string             `json:"draft_save_state"`
	DraftSaveText      string             `json:"draft_save_text"`
	CurrentURL         string             `json:"current_url"`
	SOM                []setOfMarkTarget  `json:"som"`
	Revision           any                `json:"som_revision"`
	Regions            []readScrollRegion `json:"scroll_regions"`
}

func (e *actorExecution) readScrollSnapshot() (*readScrollShot, error) {
	var shot readScrollShot
	err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "screenshot", "annotate": true, "include_som": true}), &shot)
	if err == nil && shot.CurrentURL != "" && shot.CurrentURL != e.currentURL {
		return nil, errors.New("page changed between DOM and semantic observation; coverage incomplete")
	}
	return &shot, err
}
func (e *actorExecution) clickReadNavigation(locator actorLocator, revealFrom ...string) error {
	// The selector supplies DOM end evidence; fresh SOM identity uses exact name/role.
	locator.Selector = ""
	var matches []setOfMarkTarget
	var shot *readScrollShot
	direction := "up"
	if len(revealFrom) > 0 && revealFrom[0] == "end" {
		direction = "down"
	}
	scanning := false
	for attempts := 0; attempts < 40; attempts++ {
		var err error
		shot, err = e.readScrollSnapshot()
		if err != nil {
			return err
		}
		matches = nil
		for _, t := range shot.SOM {
			if !t.Disabled && somTargetMatches(locator, t) {
				matches = append(matches, t)
			}
		}
		if len(matches) > 0 {
			break
		}
		var region *readScrollRegion
		for j := range shot.Regions {
			if shot.Regions[j].Name == "Document" {
				if region != nil {
					return errors.New("ambiguous pagination scroll region")
				}
				region = &shot.Regions[j]
			}
		}
		if region == nil {
			break
		}
		atBoundary := (direction == "up" && region.Top <= 0) || (direction == "down" && region.Top >= region.MaxY-1)
		if atBoundary {
			if scanning {
				break
			}
			scanning = true
			if direction == "up" {
				direction = "down"
			} else {
				direction = "up"
			}
		}
		amount := 600
		if region.Height > 0 {
			// Overlap successive viewports so a short control cannot fall
			// into a gap between semantic observations.
			amount = max(1, min(600, int(region.Height*0.65)))
		}
		if !scanning {
			amount = 10000
		}
		var out map[string]any
		args := withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "scroll", "target_id": region.ID, "som_revision": shot.Revision, "expected_name": region.Name, "expected_role": region.Role, "direction": direction, "amount": amount})
		if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", args, &out); err != nil {
			return err
		}
		sc := mapFromAny(out["scroll"])
		if sc["wrong_target"] == true || sc["ambiguous"] == true || stringFromAny(sc["actual_target_id"]) != region.ID {
			return errors.New("pagination reveal scroll target was not verified")
		}
	}
	if len(matches) != 1 {
		return errors.New("pagination control missing or ambiguous; coverage incomplete")
	}

	target := matches[0]
	if target.Dangerous || target.Loading || (target.Effect != "" && target.Effect != "navigation_only") {
		return errors.New("pagination control is not verified navigation_only")
	}

	// Computer revalidates the live target and expected navigation effect before clicking,
	// including clickable non-button elements whose SOM omits an effect.
	var out map[string]any
	err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "target_id": matches[0].ID, "som_revision": shot.Revision, "expected_text": locator.Text, "expected_effect": "navigation_only"}), &out)
	if err != nil {
		return err
	}
	return e.finishInteraction(out)
}

func (e *actorExecution) completeRawDOM() (*browserExtractResult, error) {
	for _, limit := range []int{200000, 400000, 800000, 1000000} {
		doc, err := e.extractDOMOptions(map[string]any{"formats": []string{"html", "metadata"}, "readability": false, "max_chars": limit, "wait_ms": 250})
		if err != nil {
			return nil, err
		}
		if !doc.Truncated {
			return doc, nil
		}
	}
	return nil, errors.New("rendered HTML truncated at 1 MB; inspection incomplete")
}

// Return actual visible SOM labels alongside exact observed anchor URLs. Raw
// DOM data remains available in separate extraction rows for form inspection.
func (e *actorExecution) observePage(step actorStep) error {
	doc, err := e.completeRawDOM()
	if err != nil {
		return err
	}
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return err
	}
	matcher, err := cascadia.Compile(step.Items)
	if err != nil {
		return err
	}
	nodes := cascadia.QueryAll(root, matcher)
	if len(nodes) != 1 {
		return fmt.Errorf("observe_page requires exactly one root item, got %d", len(nodes))
	}
	item, err := extractNodeItem(nodes[0], step.Fields, e.currentURL)
	if err != nil {
		return err
	}
	shot, err := e.readScrollSnapshot()
	if err != nil {
		return err
	}
	item["current_url"] = e.currentURL
	item["page_title"] = doc.Title
	item["media_iframe_src"] = shot.MediaIframeSrc
	item["media_embed_status"] = shot.MediaEmbedStatus
	item["media_provider"] = shot.MediaProvider
	item["media_error_text"] = shot.MediaErrorText
	item["media_player_visible"] = shot.MediaPlayerVisible
	item["media_iframe_visible"] = shot.MediaIframeVisible
	item["draft_save_state"] = shot.DraftSaveState
	item["draft_save_text"] = shot.DraftSaveText
	controls := []map[string]any{}
	links := []map[string]any{}
	linkSeen := map[string]bool{}
	anchors := cascadia.QueryAll(root, cascadia.MustCompile("a[href]"))
	for _, target := range shot.SOM {
		label := firstNonEmpty(target.AccessibleName, target.Text)
		controls = append(controls, map[string]any{"target_id": target.ID, "label": label, "role": firstNonEmpty(target.Role, target.Tag), "disabled": target.Disabled})
		if target.Tag != "a" && target.Role != "link" {
			continue
		}
		for _, anchor := range anchors {
			aria, _ := htmlAttribute(anchor, "aria-label")
			name := firstNonEmpty(aria, htmlNodeText(anchor))
			if name != label {
				continue
			}
			raw, _ := htmlAttribute(anchor, "href")
			value, err := coerceActorValue(raw, "url", e.currentURL)
			if err != nil {
				continue
			}
			href := stringFromAny(value)
			key := label + "\n" + href
			if linkSeen[key] {
				continue
			}
			linkSeen[key] = true
			links = append(links, map[string]any{"label": label, "url": href, "target_id": target.ID})
		}
	}
	item["visible_navigation"] = links
	item["visible_controls"] = controls
	b, _ := json.Marshal(item)
	if len(b) > maxActorItemBytes || e.datasetBytes+len(b)+1 > maxActorDatasetBytes || len(e.items) >= e.maxItems {
		return errors.New("page observation exceeds dataset limits")
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
	return nil
}

func completeReadView(v actorReadView, result *actorViewCoverage, root *html.Node, count int, base, evidence string) error {
	if v.Total != nil {
		expected := 0
		if !(count == 0 && readHas(root, v.EmptySelector)) {
			item, err := extractNodeItem(root, map[string]actorField{"total": *v.Total}, base)
			if err != nil {
				return fmt.Errorf("end-of-list total unavailable: %w", err)
			}
			n, ok := actorNumber(item["total"])
			if !ok || n < 0 || n != float64(int(n)) {
				return errors.New("invalid collection total")
			}
			expected = int(n)
		}
		result.ExpectedTotal = &expected
		if count != expected {
			return fmt.Errorf("collected %d unique items but view reports %d; coverage incomplete", count, expected)
		}
		evidence += "_count_match"
	}
	result.Complete = true
	result.MoreRemaining = false
	result.EndEvidence = evidence
	return nil
}

// Select exactly one record from a complete verified collection, including
// records no longer present in the DOM after pagination. No navigation or writes.
func (e *actorExecution) selectRecord(step actorStep) error {
	if e.coverage == nil || !e.coverage.Complete || !e.coverage.ReadOnly || e.coverage.MoreRemaining {
		return errors.New("select_record requires complete verified read coverage")
	}
	var matches []map[string]any
	for _, item := range e.items {
		if stringFromAny(item[step.VerifiedField]) == step.Value {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("select_record expected one match, got %d", len(matches))
	}
	for k, v := range matches[0] {
		e.lastValues[k] = v
	}
	return nil
}
