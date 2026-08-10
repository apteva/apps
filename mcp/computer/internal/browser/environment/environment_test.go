package environment

import "testing"

func TestParseOmittedIsNoOp(t *testing.T) {
	got, err := Parse(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsEmpty() {
		t.Fatalf("omitted environment must be empty: %+v", got)
	}
}

func TestParseLegacyUserAgentAndDefaults(t *testing.T) {
	got, err := Parse(map[string]any{
		"locale":      "de-DE",
		"geolocation": map[string]any{"latitude": 52.52, "longitude": 13.405},
	}, "Legacy-UA")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserAgent != "Legacy-UA" || len(got.Languages) != 1 || got.Languages[0] != "de-DE" {
		t.Fatalf("normalized environment: %+v", got)
	}
	if got.Geolocation.Accuracy == nil || *got.Geolocation.Accuracy != 100 || got.Geolocation.Permission != "grant" {
		t.Fatalf("geolocation defaults: %+v", got.Geolocation)
	}
}

func TestParseRejectsUnknownAndConflictingFields(t *testing.T) {
	if _, err := Parse(map[string]any{"country": "DE"}, ""); err == nil {
		t.Fatal("unknown environment field accepted")
	}
	if _, err := Parse(map[string]any{"user_agent": "new"}, "old"); err == nil {
		t.Fatal("conflicting user agents accepted")
	}
}
