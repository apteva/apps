package main

import "testing"

func TestMetaConversionsExcludeEngagementAndRollups(t *testing.T) {
	// The production report that motivated this: post_save and post_net_save
	// were reported as 2 conversions for a signup campaign with none.
	row := map[string]any{"actions": []any{
		map[string]any{"action_type": "onsite_conversion.post_save", "value": "1"},
		map[string]any{"action_type": "onsite_conversion.post_net_save", "value": "1"},
		map[string]any{"action_type": "onsite_conversion", "value": "2"},
		map[string]any{"action_type": "link_click", "value": "252"},
	}}
	if count, _ := metaRowConversions(row); count != 0 {
		t.Fatalf("engagement counted as conversions: %v", count)
	}
}

func TestMetaConversionsCountEachKindOnce(t *testing.T) {
	actions := map[string]float64{
		"lead":                                 5,
		"offsite_conversion.fb_pixel_lead":     3, // subset of lead
		"omni_purchase":                        2,
		"offsite_conversion.fb_pixel_purchase": 2, // subset of omni_purchase
		"offsite_conversion.fb_pixel_complete_registration": 4,
	}
	values := map[string]float64{"omni_purchase": 80.5, "offsite_conversion.fb_pixel_purchase": 80.5}
	count, value, used := resolveMetaConversions(actions, values, nil)
	if count != 11 || value != 80_500_000 {
		t.Fatalf("count=%v value=%v used=%v", count, value, used)
	}
	if len(used) != 3 {
		t.Fatalf("expected one action type per kind, got %v", used)
	}
}

func TestMetaConversionsExplicitSelection(t *testing.T) {
	actions := map[string]float64{
		"lead": 5, "offsite_conversion.fb_pixel_complete_registration": 4, "onsite_conversion.post_save": 1,
	}
	count, _, used := resolveMetaConversions(actions, nil, []string{"signup"})
	if count != 4 || len(used) != 1 || used[0] != "offsite_conversion.fb_pixel_complete_registration" {
		t.Fatalf("signup alias resolved wrong: count=%v used=%v", count, used)
	}
	count, _, _ = resolveMetaConversions(actions, nil, []string{"lead", "lead", "onsite_conversion.post_save"})
	if count != 6 {
		t.Fatalf("duplicate selection or exact type miscounted: %v", count)
	}
	definition := metaConversionDefinition([]string{"onsite_conversion.post_save"}, map[string]bool{})
	if len(definition["warnings"].([]string)) != 1 {
		t.Fatalf("engagement selection not flagged: %#v", definition)
	}
}

func TestParseConversionEventsRejectsRollups(t *testing.T) {
	if _, err := parseConversionEvents([]any{"offsite_conversion"}); err == nil {
		t.Fatal("rollup accepted")
	}
	got, err := parseConversionEvents([]any{" Lead ", "lead"})
	if err != nil || len(got) != 1 || got[0] != "lead" {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestRecountMetaConversionsCorrectsCachedRows(t *testing.T) {
	// Rows cached by the old substring logic stored post_save as a conversion.
	points := []analyticsPoint{{
		Platform:    "meta",
		Conversions: 2,
		ProviderMetrics: map[string]any{
			"actions": map[string]any{"onsite_conversion.post_save": float64(1), "onsite_conversion.post_net_save": float64(1)},
		},
	}, {Platform: "google", Conversions: 3}}
	definition := recountMetaConversions(points, nil)
	if points[0].Conversions != 0 || points[1].Conversions != 3 {
		t.Fatalf("recount wrong: %#v", points)
	}
	if definition["mode"] != "default" {
		t.Fatalf("definition=%#v", definition)
	}
}
