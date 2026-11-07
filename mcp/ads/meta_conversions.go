package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Meta reports overlapping action types for the same real-world event:
// omni_purchase already contains offsite_conversion.fb_pixel_purchase, lead
// contains pixel and on-Facebook leads, and the onsite_conversion.* family
// mixes engagement such as post_save and post_net_save with real
// conversions. Summing every action whose name contains "conversion" counted
// a saved post as a signup, so a conversion total is now built from explicit
// event kinds, each resolved to exactly one action type.

type metaConversionKind struct {
	Name string
	// Preference order: the first type present in a row is the only one
	// counted for that kind, because later types are subsets of earlier ones.
	ActionTypes []string
}

var metaConversionKinds = []metaConversionKind{
	{Name: "purchase", ActionTypes: []string{
		"omni_purchase", "purchase", "offsite_conversion.fb_pixel_purchase",
		"onsite_web_purchase", "app_custom_event.fb_mobile_purchase",
	}},
	{Name: "lead", ActionTypes: []string{
		"lead", "offsite_conversion.fb_pixel_lead", "onsite_conversion.lead_grouped", "leadgen_grouped",
	}},
	{Name: "complete_registration", ActionTypes: []string{
		"omni_complete_registration", "complete_registration",
		"offsite_conversion.fb_pixel_complete_registration", "app_custom_event.fb_mobile_complete_registration",
	}},
}

var metaConversionKindAliases = map[string]string{
	"registration": "complete_registration",
	"signup":       "complete_registration",
	"sign_up":      "complete_registration",
}

var metaDefaultConversionKinds = []string{"purchase", "lead", "complete_registration"}

// Engagement actions Meta files under onsite_conversion.*; they are never a
// conversion by default and are flagged when a caller selects them.
var metaEngagementActionTypes = map[string]bool{
	"onsite_conversion.post_save":     true,
	"onsite_conversion.post_net_save": true,
	"onsite_conversion.post_unsave":   true,
	"onsite_conversion.post_net_like": true,
	"post_engagement":                 true,
	"page_engagement":                 true,
	"post_reaction":                   true,
	"link_click":                      true,
	"landing_page_view":               true,
	"video_view":                      true,
}

// Rollups that sum many unrelated action types. Counting them alongside a
// specific event double counts it.
var metaRollupActionTypes = map[string]bool{
	"offsite_conversion": true,
	"onsite_conversion":  true,
	"conversion":         true,
}

func metaConversionKindByName(name string) (metaConversionKind, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if alias, ok := metaConversionKindAliases[name]; ok {
		name = alias
	}
	for _, kind := range metaConversionKinds {
		if kind.Name == name {
			return kind, true
		}
	}
	return metaConversionKind{}, false
}

// parseConversionEvents validates conversion_events: each entry is a kind
// (purchase, lead, complete_registration) or an exact Meta action type.
func parseConversionEvents(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		if strs, isStrings := raw.([]string); isStrings {
			for _, s := range strs {
				items = append(items, s)
			}
		} else {
			return nil, fmt.Errorf("conversion_events must be an array of strings")
		}
	}
	if len(items) > 20 {
		return nil, fmt.Errorf("conversion_events accepts at most 20 entries")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		value, isString := item.(string)
		value = strings.ToLower(strings.TrimSpace(value))
		if !isString || value == "" {
			return nil, fmt.Errorf("conversion_events entries must be non-empty strings")
		}
		if metaRollupActionTypes[value] {
			return nil, fmt.Errorf("conversion_events cannot include the %q rollup; it overlaps every specific event", value)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

// resolveMetaConversions counts each selected kind once and each exact action
// type once, returning the action types that contributed to the count. Value
// is resolved the same way against action_values, which Meta can report for a
// kind whose count row is absent.
func resolveMetaConversions(actions, values map[string]float64, selection []string) (float64, int64, []string) {
	if len(selection) == 0 {
		selection = metaDefaultConversionKinds
	}
	countTypes := selectMetaActionTypes(actions, selection)
	total := 0.0
	for _, actionType := range countTypes {
		total += actions[actionType]
	}
	value := 0.0
	for _, actionType := range selectMetaActionTypes(values, selection) {
		value += values[actionType]
	}
	return total, int64(math.Round(value * 1e6)), countTypes
}

func selectMetaActionTypes(present map[string]float64, selection []string) []string {
	used := []string{}
	counted := map[string]bool{}
	add := func(actionType string) {
		if !counted[actionType] {
			counted[actionType] = true
			used = append(used, actionType)
		}
	}
	for _, item := range selection {
		if kind, ok := metaConversionKindByName(item); ok {
			for _, actionType := range kind.ActionTypes {
				if _, found := present[actionType]; found {
					add(actionType)
					break
				}
			}
			continue
		}
		if _, found := present[item]; found {
			add(item)
		}
	}
	return used
}

func metaRowConversions(row map[string]any) (float64, int64) {
	count, value, _ := resolveMetaConversions(actionValues(row["actions"]), actionValues(row["action_values"]), nil)
	return count, value
}

// recountMetaConversions re-derives conversions for Meta points from their
// stored action breakdown. It applies an explicit selection and also corrects
// cached rows written by the earlier substring-matching logic.
func recountMetaConversions(points []analyticsPoint, selection []string) map[string]any {
	usedSet := map[string]bool{}
	metaPoints := 0
	for i := range points {
		if points[i].Platform != "meta" {
			continue
		}
		metaPoints++
		actions := actionMetricValues(points[i].ProviderMetrics["actions"])
		values := actionMetricValues(points[i].ProviderMetrics["action_values"])
		count, value, used := resolveMetaConversions(actions, values, selection)
		points[i].Conversions = count
		points[i].ConversionValueMicros = value
		for _, actionType := range used {
			usedSet[actionType] = true
		}
	}
	if metaPoints == 0 {
		return nil
	}
	return metaConversionDefinition(selection, usedSet)
}

func metaConversionDefinition(selection []string, usedSet map[string]bool) map[string]any {
	mode := "explicit"
	requested := selection
	if len(selection) == 0 {
		mode = "default"
		requested = metaDefaultConversionKinds
	}
	used := make([]string, 0, len(usedSet))
	warnings := []string{}
	for actionType := range usedSet {
		used = append(used, actionType)
	}
	sort.Strings(used)
	for _, item := range requested {
		if metaEngagementActionTypes[item] {
			warnings = append(warnings, item+" is an engagement action, not a website or app conversion")
		}
	}
	kinds := map[string]any{}
	for _, kind := range metaConversionKinds {
		kinds[kind.Name] = kind.ActionTypes
	}
	return map[string]any{
		"mode":                 mode,
		"requested":            requested,
		"counted_action_types": used,
		"kind_resolution":      kinds,
		"warnings":             warnings,
		"rule":                 "Each kind counts only the first action type present per row, in kind_resolution order, because later types are subsets of earlier ones. Engagement (onsite_conversion.post_save, post_net_save, etc.) and rollups (offsite_conversion, onsite_conversion) are never counted by default. Pass conversion_events to choose kinds or exact action types.",
	}
}
