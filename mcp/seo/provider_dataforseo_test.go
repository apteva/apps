package main

import (
	"encoding/json"
	"testing"
)

func TestDecodeKeywordVolumeItem_DirectResultRow(t *testing.T) {
	raw := json.RawMessage(`{
		"keyword": "seo",
		"location_code": 2840,
		"language_code": "en",
		"search_volume": 110000,
		"competition_index": 28,
		"cpc": 25.48,
		"monthly_searches": [
			{"year": 2026, "month": 5, "search_volume": 60500}
		]
	}`)

	item, err := decodeKeywordVolumeItem(raw, "seo")
	if err != nil {
		t.Fatalf("decodeKeywordVolumeItem returned error: %v", err)
	}
	if item.Keyword != "seo" {
		t.Fatalf("keyword = %q, want seo", item.Keyword)
	}
	if item.SearchVolume == nil || *item.SearchVolume != 110000 {
		t.Fatalf("search_volume = %v, want 110000", item.SearchVolume)
	}
	if len(item.MonthlySearches) != 1 || item.MonthlySearches[0].Volume != 60500 {
		t.Fatalf("monthly_searches = %+v, want one 60500 row", item.MonthlySearches)
	}
}

func TestDecodeKeywordVolumeItem_ItemsWrapper(t *testing.T) {
	raw := json.RawMessage(`{
		"items": [{
			"keyword": "seo",
			"location_code": 2840,
			"language_code": "en",
			"search_volume": 110000
		}]
	}`)

	item, err := decodeKeywordVolumeItem(raw, "seo")
	if err != nil {
		t.Fatalf("decodeKeywordVolumeItem returned error: %v", err)
	}
	if item.Keyword != "seo" {
		t.Fatalf("keyword = %q, want seo", item.Keyword)
	}
	if item.SearchVolume == nil || *item.SearchVolume != 110000 {
		t.Fatalf("search_volume = %v, want 110000", item.SearchVolume)
	}
}

func TestDecodeKeywordVolumeItem_Empty(t *testing.T) {
	if _, err := decodeKeywordVolumeItem(json.RawMessage(`{}`), "seo"); err == nil {
		t.Fatal("expected empty keyword volume row to fail")
	}
}
