// analytics_planner — decides how each requested breakdown runs on a
// provider before anything is sent: directly, one query per post when the
// provider cannot combine the breakdown with the filter, or not at all,
// with the reason, instead of a query the provider rejects or silently
// answers unfiltered.
package main

import (
	"fmt"
	"sort"
	"strings"
)

const (
	breakdownDirect      = "direct"
	breakdownPerPost     = "per_post"
	breakdownUnsupported = "unsupported"

	perPostBreakdownDefault = 10
	perPostBreakdownMax     = 25
)

type breakdownPlan struct {
	Mode   string
	Filter string // per_post: the filter applied to each post's own query
	Reason string
}

// providerBreakdownFilters lists the filters each breakdown accepts.
// YouTube's pairs were verified against a live channel on 2026-11-02;
// every other pairing returns "The query is not supported". Instagram
// follower demographics take no filters at all.
var providerBreakdownFilters = map[string]map[string][]string{
	"youtube": {
		"device":          {"os", "country", "audience_type", "video"},
		"os":              {"device", "country", "audience_type", "video"},
		"country":         {"audience_type", "video"},
		"traffic_source":  {"country", "audience_type", "video"},
		"audience_type":   {"country", "video"},
		"content_type":    {"country", "audience_type", "video"},
		"sharing_service": {"country", "audience_type", "video"},
		"video":           {"country", "audience_type"},
	},
	"instagram": {},
}

func planBreakdown(platform, dimension string, filters map[string][]string) breakdownPlan {
	accepted := providerBreakdownFilters[platform][dimension]
	rejected := []string{}
	for filter := range filters {
		if filter != dimension && !containsFold(accepted, filter) {
			rejected = append(rejected, filter)
		}
	}
	if len(rejected) == 0 {
		return breakdownPlan{Mode: breakdownDirect}
	}
	sort.Strings(rejected)
	// Per-post numbers for a filter the post ranking cannot take: rank
	// posts first, then read each post's own breakdown of the filter.
	if dimension == "video" && len(rejected) == 1 && containsFold(providerBreakdownFilters[platform][rejected[0]], "video") {
		return breakdownPlan{Mode: breakdownPerPost, Filter: rejected[0]}
	}
	reason := fmt.Sprintf("%s cannot break down %s filtered by %s.", platformDisplayName(platform), dimensionLabel(dimension), dimensionLabel(strings.Join(rejected, ", ")))
	if len(accepted) > 0 {
		labels := make([]string, 0, len(accepted))
		for _, filter := range accepted {
			labels = append(labels, dimensionLabel(filter))
		}
		reason += " Supported filters: " + strings.Join(labels, ", ") + "."
	} else {
		reason += " This breakdown takes no filters."
	}
	return breakdownPlan{Mode: breakdownUnsupported, Reason: reason}
}

func perPostBreakdownLimit(query analyticsQuery) int {
	switch {
	case query.Limit <= 0:
		return perPostBreakdownDefault
	case query.Limit > perPostBreakdownMax:
		return perPostBreakdownMax
	}
	return query.Limit
}

func dimensionLabel(dimension string) string {
	return strings.ReplaceAll(dimension, "_", " ")
}

func platformDisplayName(platform string) string {
	if def, ok := platforms[platform]; ok && def.DisplayName != "" {
		return def.DisplayName
	}
	return platform
}
