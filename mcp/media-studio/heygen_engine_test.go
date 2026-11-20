package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func heygenArgs(t *testing.T, opts map[string]any) map[string]any {
	t.Helper()
	got, err := buildAvatarArgs(map[string]any{
		"avatar": "look-1", "prompt": "hello", "voice": "vo-1", "options": opts,
	}, "heygen", "avatar.generate")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBuildHeyGenAvatarArgs_Avatar6MovesExpressivenessIntoEngine(t *testing.T) {
	got := heygenArgs(t, map[string]any{
		"engine": "heygen-avatar-6", "resolution": "2k",
		"motion_prompt": "calm, open hands", "expressiveness": "low",
	})
	engine := got["engine"].(map[string]any)
	if engine["type"] != "heygen-avatar-6" || engine["motion_expressiveness"] != "less" {
		t.Errorf("engine = %+v, want avatar 6 with motion_expressiveness=less", engine)
	}
	if _, ok := got["expressiveness"]; ok {
		t.Errorf("top-level expressiveness must not be sent with Avatar 6: %+v", got)
	}
	if got["motion_prompt"] != "calm, open hands" || got["resolution"] != "2k" {
		t.Errorf("motion_prompt/resolution not forwarded: %+v", got)
	}
}

func TestBuildHeyGenAvatarArgs_Avatar6ExplicitMotionExpressiveness(t *testing.T) {
	got := heygenArgs(t, map[string]any{
		"engine":                "heygen-avatar-6-turbo",
		"motion_expressiveness": "default",
		"expressiveness":        "low",
	})
	engine := got["engine"].(map[string]any)
	if engine["motion_expressiveness"] != "default" {
		t.Errorf("explicit motion_expressiveness should win: %+v", engine)
	}
}

func TestBuildHeyGenAvatarArgs_EngineObjectKeepsItsExpressiveness(t *testing.T) {
	got := heygenArgs(t, map[string]any{
		"engine":         map[string]any{"type": "heygen-avatar-6", "motion_expressiveness": "default"},
		"expressiveness": "low",
	})
	if got["engine"].(map[string]any)["motion_expressiveness"] != "default" {
		t.Errorf("engine object value overwritten: %+v", got["engine"])
	}
}

func TestBuildHeyGenAvatarArgs_AvatarIVKeepsTopLevelExpressiveness(t *testing.T) {
	got := heygenArgs(t, map[string]any{"engine": "avatar_iv", "expressiveness": "high", "motion_prompt": "wave"})
	if got["expressiveness"] != "high" || got["motion_prompt"] != "wave" {
		t.Errorf("avatar_iv should forward both: %+v", got)
	}
	if _, ok := got["engine"].(map[string]any)["motion_expressiveness"]; ok {
		t.Errorf("avatar_iv must not get engine.motion_expressiveness: %+v", got["engine"])
	}
}

func TestBuildHeyGenAvatarArgs_AvatarIIIStripsMotion(t *testing.T) {
	got := heygenArgs(t, map[string]any{"engine": "avatar_iii", "expressiveness": "high", "motion_prompt": "wave"})
	if _, ok := got["expressiveness"]; ok {
		t.Errorf("avatar_iii rejects expressiveness: %+v", got)
	}
	if _, ok := got["motion_prompt"]; ok {
		t.Errorf("avatar_iii rejects motion_prompt: %+v", got)
	}
}

func TestBuildHeyGenAvatarArgs_UnknownEnginePassesThrough(t *testing.T) {
	got := heygenArgs(t, map[string]any{"engine": "heygen-avatar-7", "expressiveness": "high", "motion_prompt": "wave"})
	if got["engine"].(map[string]any)["type"] != "heygen-avatar-7" {
		t.Errorf("engine not passed through: %+v", got["engine"])
	}
	if got["expressiveness"] != "high" || got["motion_prompt"] != "wave" {
		t.Errorf("unknown engines should forward motion fields as-is: %+v", got)
	}
}

func lookResult(engines ...string) *sdk.ExecuteResult {
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"supported_api_engines": engines}})
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: body}
}

func TestValidateHeyGenAvatarEngine(t *testing.T) {
	cases := []struct {
		name     string
		avatar   string
		opts     map[string]any
		look     *sdk.ExecuteResult
		wantErr  string
		wantLook bool
	}{
		{name: "avatar 6 advertised", avatar: "look-1",
			opts: map[string]any{"engine": "heygen-avatar-6", "resolution": "2k"},
			look: lookResult("avatar_iv", "heygen-avatar-6"), wantLook: true},
		{name: "avatar 6 not advertised", avatar: "look-1",
			opts:    map[string]any{"engine": "heygen-avatar-6"},
			look:    lookResult("avatar_iv"),
			wantErr: "supported_api_engines=[heygen-avatar-6]", wantLook: true},
		{name: "avatar 6 empty list refused", avatar: "look-1",
			opts:    map[string]any{"engine": "heygen-avatar-6-turbo"},
			look:    lookResult(),
			wantErr: "heygen-avatar-6-turbo", wantLook: true},
		{name: "avatar 6 rejects 4k before any call", avatar: "look-1",
			opts:    map[string]any{"engine": "heygen-avatar-6", "resolution": "4k"},
			wantErr: "1080p or 2k"},
		{name: "avatar 6 rejects raw image", avatar: "",
			opts:    map[string]any{"engine": "heygen-avatar-6"},
			wantErr: "raw source_image"},
		{name: "avatar 6 motion prompt too long", avatar: "look-1",
			opts:    map[string]any{"engine": "heygen-avatar-6", "motion_prompt": strings.Repeat("a", 1201)},
			wantErr: "1200"},
		{name: "avatar v empty list refused", avatar: "look-1",
			opts:    map[string]any{"engine": "avatar_v"},
			look:    lookResult(),
			wantErr: "avatar_v", wantLook: true},
		{name: "avatar iv on legacy look allowed", avatar: "look-1",
			opts: map[string]any{"engine": "avatar_iv"},
			look: lookResult(), wantLook: true},
		{name: "unknown engine checked against list", avatar: "look-1",
			opts:    map[string]any{"engine": "heygen-avatar-7"},
			look:    lookResult("heygen-avatar-6"),
			wantErr: "heygen-avatar-7", wantLook: true},
		{name: "unknown engine advertised", avatar: "look-1",
			opts: map[string]any{"engine": "heygen-avatar-7", "resolution": "8k"},
			look: lookResult("heygen-avatar-7"), wantLook: true},
		{name: "auto skips preflight", avatar: "look-1",
			opts: map[string]any{"engine": "auto"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pf := newRecordingPlatform()
			pf.appSlug = "heygen"
			pf.nextExecuteResult = tc.look
			ctx := newMediaStudioCtx(t, pf)
			args := map[string]any{"avatar": tc.avatar, "prompt": "hi", "options": tc.opts}
			err := validateHeyGenAvatarEngine(ctx, &sdk.BoundIntegration{ConnectionID: 56, AppSlug: "heygen"}, args)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			calledLook := len(pf.executeCalls) > 0 && pf.executeCalls[0].Tool == "get_avatar_look"
			if calledLook != tc.wantLook {
				t.Errorf("get_avatar_look called = %v, want %v", calledLook, tc.wantLook)
			}
		})
	}
}

func TestHeyGenAvatarQuote_PerEngine(t *testing.T) {
	quote := func(opts map[string]any) float64 {
		opts["estimated_duration_seconds"] = 10.0
		return heygenAvatarQuote("heygen", map[string]any{"prompt": "hi", "options": opts})
	}
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
	if got := quote(map[string]any{"engine": "heygen-avatar-6-turbo", "resolution": "1080p"}); !near(got, 0.5) {
		t.Errorf("turbo 1080p = %v, want 0.5", got)
	}
	for _, opts := range []map[string]any{
		{"engine": "heygen-avatar-6-turbo", "resolution": "2k"},
		{"engine": "heygen-avatar-6", "resolution": "1080p"},
		{"engine": "heygen-avatar-7"},
	} {
		if got := quote(opts); got != 0 {
			t.Errorf("%v quoted %v, want no quote", opts, got)
		}
	}
	if got := quote(map[string]any{"engine": "avatar_iv"}); !near(got, 0.5) {
		t.Errorf("avatar_iv photo 1080p = %v, want 0.5", got)
	}
	if got := quote(map[string]any{}); !near(got, 0.5) {
		t.Errorf("auto photo 1080p = %v, want 0.5", got)
	}
}
