package main

import (
	"encoding/json"
	"testing"
)

func TestBrandPresenterPolicyRejectsGenericVideoAndWrongProvider(t *testing.T) {
	p := brandProductionPolicy{AssetType: "avatar", Provider: "heygen"}
	for _, args := range []map[string]any{{"kind": "video"}, {"kind": "avatar", "provider": "venice-ai"}} {
		if applyBrandProductionPolicy(p, args) == nil {
			t.Fatal("incompatible request accepted", args)
		}
	}
	args := map[string]any{"kind": "avatar"}
	if err := applyBrandProductionPolicy(p, args); err != nil || args["provider"] != "heygen" {
		t.Fatal(args, err)
	}
}
func TestBrandProductionIdentityResolvedBeforeProviderExecution(t *testing.T) {
	pf := newRecordingPlatform()
	pf.perAppCallResults = map[string]json.RawMessage{
		"editorial:editorial_settings_get": json.RawMessage(`{"brands":[{"id":"marco","persona_id":3,"operating_profile":{"production_profiles":{"lesson":{"asset_type":"avatar","required_provider":"heygen","persona_style_profile_id":15}}}}]}`),
		"persona-studio:persona_get":       json.RawMessage(`{"persona":{"id":3,"default_avatar_id":"marco-avatar"},"style_profiles":[{"id":15,"asset_type":"avatar","provider_settings":{"provider":"heygen","voice":"marco-voice"}}]}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	args := map[string]any{"brand_id": "marco", "production_profile": "lesson", "kind": "avatar", "cache_key": "same"}
	p, err := resolveBrandProductionPolicy(ctx, args)
	if err != nil || p.Provider != "heygen" || args["avatar"] != "marco-avatar" || args["voice"] != "marco-voice" {
		t.Fatalf("policy: %+v %+v %v", p, args, err)
	}
	if args["cache_key"] == "same" {
		t.Fatal("cache was not bound to brand policy")
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("policy preflight generated media")
	}
	bad := map[string]any{"brand_id": "marco", "production_profile": "lesson", "kind": "avatar", "avatar": "stock-person"}
	if _, err := resolveBrandProductionPolicy(ctx, bad); err == nil {
		t.Fatal("unapproved avatar accepted")
	}
	if _, err := resolveBrandProductionPolicy(ctx, map[string]any{"brand_id": "unknown", "production_profile": "lesson", "kind": "avatar"}); err == nil {
		t.Fatal("unknown brand accepted")
	}
	if _, err := resolveBrandProductionPolicy(ctx, map[string]any{"brand_id": "marco"}); err == nil {
		t.Fatal("partial policy bypass accepted")
	}
	legacy := map[string]any{"kind": "image"}
	if p, err := resolveBrandProductionPolicy(ctx, legacy); err != nil || p != nil {
		t.Fatal("legacy path broken", p, err)
	}
}
