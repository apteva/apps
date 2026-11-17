package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Trigger discovery: which app events can start a given assignment.
//
// Processes asks the platform for every event the project's apps publish,
// then scores each event against the assignment: documented payload fields
// that can fill its parameters, plus words from the agent's query and the
// procedure's own text matched against the app and event. Payload docs are
// loose (field -> type label), so suggestions are checked with
// trigger_preview before activation.

const maxTriggerCandidates = 25

type TriggerCandidate struct {
	InstallID         int64             `json:"source_install_id"`
	App               string            `json:"app"`
	Source            string            `json:"source"`
	Topic             string            `json:"topic"`
	Description       string            `json:"description,omitempty"`
	Payload           map[string]string `json:"payload,omitempty"`
	FieldsDocumented  bool              `json:"fields_documented"`
	SuggestedMappings map[string]string `json:"suggested_mappings"`
	Covered           []string          `json:"covered_parameters"`
	Missing           []string          `json:"missing_required"`
	Ready             bool              `json:"ready"`
	Reasons           []string          `json:"reasons"`
	OtherInstalls     []int64           `json:"other_installs_of_app,omitempty"`
	score             float64
}

// words splits identifiers and prose into lowercase tokens: "lead_email",
// "lead.qualified" and "Lead email" all give lead + email.
func words(s string) []string {
	out := []string{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if w = strings.TrimSuffix(w, "s"); len(w) > 1 && !stopWord[w] {
			out = append(out, w)
		}
	}
	return out
}

var stopWord = map[string]bool{"the": true, "and": true, "for": true, "when": true, "with": true, "this": true, "that": true, "from": true, "new": true, "run": true, "an": true, "on": true, "of": true, "to": true, "in": true, "is": true, "it": true, "be": true, "or": true, "every": true, "whenever": true, "start": true, "assignment": true, "proces": true, "procedure": true, "step": true, "event": true, "trigger": true}

func wordSet(parts ...string) map[string]bool {
	set := map[string]bool{}
	for _, p := range parts {
		for _, w := range words(p) {
			set[w] = true
		}
	}
	return set
}

// typeFits reports whether a documented payload type label can fill a
// parameter type. Unknown or prose labels are accepted: docs are loose.
func typeFits(param, label string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	param = strings.ToLower(param)
	known := map[string]bool{"string": true, "integer": true, "number": true, "boolean": true, "object": true, "array": true}
	if !known[label] {
		return true
	}
	switch param {
	case "", "string", "enum", "text":
		return label == "string" || label == "integer" || label == "number"
	case "number", "integer":
		return label == "integer" || label == "number"
	case "boolean":
		return label == "boolean"
	}
	return true
}

// fieldScore rates how well a payload field name fits a parameter: exact
// key 3, all key words in the field (lead_email for email) 2, any shared
// word between field and key or label 1.
func fieldScore(p Parameter, field string) int {
	norm := func(s string) string { return strings.Join(words(s), "") }
	if norm(field) != "" && norm(field) == norm(p.Key) {
		return 3
	}
	fw := wordSet(field)
	kw := words(p.Key)
	all := len(kw) > 0
	for _, w := range kw {
		all = all && fw[w]
	}
	if all {
		return 2
	}
	for w := range wordSet(p.Key, p.Label) {
		if fw[w] && w != "id" {
			return 1
		}
	}
	return 0
}

func procedureText(d Definition) string {
	parts := []string{d.Name, d.Instructions, d.CompletionCriteria}
	for _, s := range d.Steps {
		parts = append(parts, s.Name, s.Instructions)
	}
	return strings.Join(parts, " ")
}

func scoreCandidate(src sdk.AppEventSource, ev sdk.EventDecl, d Definition, x Assignment, query, procedure map[string]bool) TriggerCandidate {
	c := TriggerCandidate{InstallID: src.InstallID, App: src.App, Source: src.Name, Topic: ev.Name, Description: ev.Description, Payload: ev.Payload, FieldsDocumented: len(ev.Payload) > 0, SuggestedMappings: map[string]string{}, Covered: []string{}, Missing: []string{}, Reasons: []string{}}
	fields := make([]string, 0, len(ev.Payload))
	for f := range ev.Payload {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	mapped := 0
	for _, p := range d.Parameters {
		best, bestScore := "", 0
		for _, f := range fields {
			if s := fieldScore(p, f); s > bestScore && typeFits(p.Type, ev.Payload[f]) {
				best, bestScore = f, s
			}
		}
		_, fixed := x.Parameters[p.Key]
		if best != "" {
			c.SuggestedMappings[p.Key] = "data." + best
			c.Covered = append(c.Covered, p.Key)
			c.score += float64(bestScore)
			mapped++
			continue
		}
		if fixed || p.Default != nil {
			c.Covered = append(c.Covered, p.Key)
			continue
		}
		if p.Required {
			c.Missing = append(c.Missing, p.Key)
		}
	}
	c.Ready = len(c.Missing) == 0
	if mapped > 0 {
		c.Reasons = append(c.Reasons, fmt.Sprintf("fills %d parameter(s) from documented fields", mapped))
	}
	text := wordSet(src.App, src.Name, ev.Name, ev.Description)
	hits := func(set map[string]bool) []string {
		out := []string{}
		for w := range set {
			if len(w) > 2 && text[w] {
				out = append(out, w)
			}
		}
		sort.Strings(out)
		return out
	}
	// The agent's query says what should start the work; it outweighs field
	// matches. The procedure's own text is a weaker hint.
	if q := hits(query); len(q) > 0 {
		c.score += 4 * float64(len(q))
		c.Reasons = append(c.Reasons, "matches query: "+strings.Join(q, ", "))
	}
	if p := hits(procedure); len(p) > 0 {
		c.score += float64(len(p))
		c.Reasons = append(c.Reasons, "procedure mentions: "+strings.Join(p, ", "))
	}
	if !c.FieldsDocumented {
		c.Reasons = append(c.Reasons, "fields not documented: check mappings with trigger_preview")
	}
	return c
}

func sourceMatches(src sdk.AppEventSource, app string) bool {
	app = strings.ToLower(strings.TrimSpace(app))
	return app == "" || strings.ToLower(src.App) == app || strings.ToLower(src.Name) == app || fmt.Sprint(src.InstallID) == app
}

func (a *App) eventSources(project string) ([]sdk.AppEventSource, error) {
	api := a.ctx.EventBusAPI()
	if api == nil {
		return nil, errors.New("event subscription API unavailable")
	}
	return api.ListAppEventSources(project)
}

// triggerSources answers trigger_sources. Without an assignment it lists the
// project's event sources (names only unless narrowed by app); with one it
// returns ranked candidates with suggested mappings.
func (a *App) triggerSources(project string, args map[string]any) (any, error) {
	sources, e := a.eventSources(project)
	if e != nil {
		return nil, e
	}
	app, queryText := str(args, "app"), str(args, "query")
	process, assignment := str(args, "process_id"), str(args, "assignment_id")
	if assignment == "" {
		return listSources(sources, app, queryText), nil
	}
	if process == "" {
		return nil, errors.New("process_id required with assignment_id")
	}
	if _, e = a.get(project, process); e != nil {
		return nil, e
	}
	x, e := a.assignment(project, process, assignment)
	if e != nil {
		return nil, e
	}
	d, e := a.definition(process, x.ProcedureVersion)
	if e != nil {
		return nil, e
	}
	installs := map[string][]int64{}
	for _, src := range sources {
		if len(src.Events) > 0 {
			installs[src.App] = append(installs[src.App], src.InstallID)
		}
	}
	query, procedure := wordSet(queryText), wordSet(procedureText(d))
	candidates := []TriggerCandidate{}
	considered := 0
	for _, src := range sources {
		if !sourceMatches(src, app) {
			continue
		}
		for _, ev := range src.Events {
			considered++
			c := scoreCandidate(src, ev, d, x, query, procedure)
			if c.score <= 0 {
				continue
			}
			for _, id := range installs[src.App] {
				if id != src.InstallID {
					c.OtherInstalls = append(c.OtherInstalls, id)
				}
			}
			candidates = append(candidates, c)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Ready != candidates[j].Ready {
			return candidates[i].Ready
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].FieldsDocumented && !candidates[j].FieldsDocumented
	})
	if len(candidates) > maxTriggerCandidates {
		candidates = candidates[:maxTriggerCandidates]
	}
	params := []map[string]any{}
	for _, p := range d.Parameters {
		_, fixed := x.Parameters[p.Key]
		params = append(params, map[string]any{"key": p.Key, "type": p.Type, "required": p.Required, "set_by_assignment": fixed, "has_default": p.Default != nil})
	}
	out := map[string]any{"process_id": process, "assignment_id": assignment, "parameters": params, "considered_events": considered, "candidates": candidates,
		"next": "Pick the candidate that matches what should start the work (not just the top score). trigger_create it paused with source_install_id, topic and suggested_mappings (add filters if only some events should start runs), trigger_preview a sample event, then trigger_status active and check sync_pending."}
	if len(candidates) == 0 {
		out["hint"] = "No event fits. Retry with a query describing what should start the work, or app=<name> to see that app's events and fields."
	}
	return out, nil
}

func listSources(sources []sdk.AppEventSource, app, queryText string) map[string]any {
	query := wordSet(queryText)
	out := []map[string]any{}
	for _, src := range sources {
		if len(src.Events) == 0 || !sourceMatches(src, app) {
			continue
		}
		events := []any{}
		for _, ev := range src.Events {
			if len(query) > 0 {
				text, hit := wordSet(src.App, src.Name, ev.Name, ev.Description), false
				for w := range query {
					hit = hit || text[w]
				}
				if !hit {
					continue
				}
			}
			if app != "" {
				events = append(events, ev)
			} else {
				events = append(events, ev.Name)
			}
		}
		if len(events) > 0 {
			out = append(out, map[string]any{"install_id": src.InstallID, "app": src.App, "name": src.Name, "events": events})
		}
	}
	hint := "Pass process_id and assignment_id for ranked events with suggested mappings; app=<name> for one app's event fields; query to filter."
	return map[string]any{"sources": out, "hint": hint}
}

// checkTriggerSource rejects a trigger whose source install does not publish
// its topic and warns about mapping paths outside the documented payload.
// When the platform cannot list sources the trigger is saved unchecked.
func (a *App) checkTriggerSource(project string, c TriggerConfig) ([]string, error) {
	sources, e := a.eventSources(project)
	if e != nil {
		return []string{"event sources unavailable; source and topic not checked"}, nil
	}
	for _, src := range sources {
		if src.InstallID != c.SourceInstallID {
			continue
		}
		for _, ev := range src.Events {
			if !topicMatch(ev.Name, c.Topic) && !topicMatch(c.Topic, ev.Name) {
				continue
			}
			warnings := []string{}
			if len(ev.Payload) > 0 {
				keys := make([]string, 0, len(c.Mappings))
				for k := range c.Mappings {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					path := c.Mappings[k]
					if field, ok := strings.CutPrefix(path, "data."); ok {
						if _, documented := ev.Payload[strings.SplitN(field, ".", 2)[0]]; !documented {
							warnings = append(warnings, fmt.Sprintf("%s maps from %s, which %s does not document; check with trigger_preview", k, path, ev.Name))
						}
					}
				}
			}
			return warnings, nil
		}
		names := []string{}
		for _, ev := range src.Events {
			names = append(names, ev.Name)
		}
		return nil, fmt.Errorf("install %d (%s) does not publish %s; it publishes: %s", src.InstallID, src.App, c.Topic, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("source install %d is not an event source in this project; see trigger_sources", c.SourceInstallID)
}

// missingTriggerParameters lists required parameters a run started by this
// trigger could not fill: no mapping, no assignment value and no default.
func missingTriggerParameters(d Definition, x Assignment, c TriggerConfig) []string {
	missing := []string{}
	for _, p := range d.Parameters {
		_, fixed := x.Parameters[p.Key]
		if p.Required && !fixed && p.Default == nil && strings.TrimSpace(c.Mappings[p.Key]) == "" {
			missing = append(missing, p.Key)
		}
	}
	return missing
}
