package main

import "testing"

func TestBrandProfileIdentityOptionalAndRoundTrip(t *testing.T) {
	a, c := testApp(t, nil)
	profiles := map[string]any{"pin": map[string]any{"asset_type": "image", "required_provider": "openai-codex"}}
	brands := []Brand{{ID: "faceless", Name: "Faceless", OperatingProfile: map[string]any{"audience": "Home cooks", "production_profiles": profiles}}, {ID: "teacher", Name: "Teacher", PersonaID: 3, OperatingProfile: map[string]any{"production_profiles": map[string]any{"lesson": map[string]any{"asset_type": "avatar", "required_provider": "heygen", "persona_style_profile_id": 15}}}}}
	if _, err := a.dispatch(c, "settings_update", map[string]any{"revision": 0, "brands": brands}); err != nil {
		t.Fatal(err)
	}
	s, err := getSettings(c.AppDB(), "alpha")
	if err != nil || len(s.Brands) != 2 {
		t.Fatalf("settings: %+v %v", s, err)
	}
	if s.Brands[0].PersonaID != 0 || s.Brands[1].PersonaID != 3 || s.Brands[0].OperatingProfile["audience"] != "Home cooks" {
		t.Fatalf("profile lost: %+v", s.Brands)
	}
	if _, err := a.dispatch(c.WithProject("beta"), "items_create", map[string]any{"title": "wrong scope", "brand_id": "teacher"}); err == nil {
		t.Fatal("cross-project brand accepted")
	}
}
func TestBrandProfileRejectsWrongPresenterMethod(t *testing.T) {
	for _, b := range []Brand{{PersonaID: -1}, {OperatingProfile: map[string]any{"audience": false}}, {OperatingProfile: map[string]any{"production_profiles": map[string]any{"lesson": map[string]any{"asset_type": "video", "required_provider": "heygen"}}}}} {
		if validateBrandProfile(b) == nil {
			t.Fatalf("invalid profile accepted: %+v", b)
		}
	}
}
