package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func isolatedTarget(env GeneratedEnvironment) *TargetConfig {
	return &TargetConfig{Mode: "isolated", Environment: &env}
}

func TestAppsByNameValidation(t *testing.T) {
	telnyx := []SimulatedIntegration{{App: "telephony", Role: "carrier", Slug: "telnyx", Simulate: true}}
	ok := []GeneratedEnvironment{
		{AppsByName: []string{"telephony", "storage"}},
		{Apps: []string{"functions"}, AppsByName: []string{"telephony"}, Integrations: telnyx,
			AppVersions: map[string]string{"telephony": "0.27.1"}, ReadTools: map[string][]string{"telephony": {"telephony_flows_list"}},
			AppConfig: map[string]map[string]string{"telephony": {"sip_transport": "udp"}}},
	}
	for i, env := range ok {
		target := isolatedTarget(env)
		target.Targets = map[string]TargetEndpoint{"phone": {App: "telephony", Path: "/api"}}
		if err := target.validate(); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	tooMany := make([]string, maxEnvironmentApps+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("app%d", i)
	}
	bad := map[string]GeneratedEnvironment{
		"in both environment.apps and environment.apps_by_name": {Apps: []string{"telephony"}, AppsByName: []string{"telephony"}},
		"duplicate environment app":                             {AppsByName: []string{"telephony", "telephony"}},
		"need 1–24 app names":                                   {AppsByName: tooMany},
		"not in environment.apps or apps_by_name":               {AppsByName: []string{"storage"}, AppConfig: map[string]map[string]string{"telephony": {"a": "b"}}},
		"setting name required":                                 {AppsByName: []string{"telephony"}, AppConfig: map[string]map[string]string{"telephony": {" ": "b"}}},
		"an app from environment.apps or apps_by_name":          {AppsByName: []string{"storage"}, Integrations: telnyx},
		"invalid version pin":                                   {AppsByName: []string{"storage"}, AppVersions: map[string]string{"telephony": "0.27.1"}},
	}
	for want, env := range bad {
		if err := isolatedTarget(env).validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v", want, err)
		}
	}
	// A target may only use an app the run contains.
	target := isolatedTarget(GeneratedEnvironment{AppsByName: []string{"storage"}})
	target.Targets = map[string]TargetEndpoint{"phone": {App: "telephony", Path: "/api"}}
	if err := target.validate(); err == nil || !strings.Contains(err.Error(), "not in the environment") {
		t.Fatal(err)
	}
}

func TestGeneratedSpecAppsByName(t *testing.T) {
	target := isolatedTarget(GeneratedEnvironment{
		Apps: []string{"functions"}, AppsByName: []string{"telephony", "storage"}, TTLSeconds: 600,
		AppVersions: map[string]string{"telephony": "0.27.1"},
		AppConfig:   map[string]map[string]string{"telephony": {"sip_transport": "udp"}},
	})
	// Apps by name need no project install; installed apps still resolve.
	spec, err := generatedSpec(target, map[string]int64{"functions": 7})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	for _, want := range []string{`"app_install_ids":[7]`, `"apps":[{"name":"telephony","version":"0.27.1"},{"name":"storage"}]`, `"app_config":{"telephony":{"sip_transport":"udp"}}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	// By name only: no empty install list.
	spec, err = generatedSpec(isolatedTarget(GeneratedEnvironment{AppsByName: []string{"telephony"}, TTLSeconds: 600}), map[string]int64{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec["app_install_ids"]; ok {
		t.Fatal(spec)
	}
	// An installed app that is missing still fails, as before.
	if _, err = generatedSpec(isolatedTarget(GeneratedEnvironment{Apps: []string{"api"}, TTLSeconds: 600}), map[string]int64{}); err == nil || !strings.Contains(err.Error(), "not installed in this project") {
		t.Fatal(err)
	}
}

func TestPinRefusesEnvironmentsThatDropAppsByName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		strip []string
		want  string
	}{
		{"current", nil, ""},
		{"before 0.14.0", []string{"apps"}, "Environments 0.14.0 or newer"},
		{"before 0.13.0", []string{"apps", "app_config"}, "Environments 0.14.0 or newer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var created map[string]any
			platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
				var reply any
				switch app + "/" + tool {
				case "environments/environment_catalog":
					reply = map[string]any{"apps": []any{map[string]any{"install_id": 3, "name": "functions"}}}
				case "environments/environment_create":
					created = args
					saved := map[string]any{}
					for k, v := range args["spec"].(map[string]any) {
						saved[k] = v
					}
					for _, field := range tc.strip {
						delete(saved, field)
					}
					reply = map[string]any{"id": "env_sip", "spec_version": 1, "spec_digest": strings.Repeat("a", 64), "spec": saved}
				default:
					return fmt.Errorf("unexpected %s/%s", app, tool)
				}
				raw, _ := json.Marshal(reply)
				return json.Unmarshal(raw, out)
			}}
			a, app, _ := fixture(t, tk.WithPlatform(platform))
			_, err := a.dispatch(context.Background(), app, "tests_suite_save", map[string]any{
				"name": "SIP",
				"target": map[string]any{"mode": "isolated",
					"targets": map[string]any{"phone": map[string]any{"app": "telephony", "path": "/api"}},
					"environment": map[string]any{"apps": []any{"functions"}, "apps_by_name": []any{"telephony"},
						"app_config":      map[string]any{"telephony": map[string]any{"sip_transport": "udp"}},
						"integrations":    []any{map[string]any{"app": "telephony", "role": "carrier", "slug": "telnyx", "simulate": true}},
						"candidate_probe": map[string]any{"app": "telephony", "tool": "telephony_flows_list", "path": "/flows/0/name"}}},
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				spec := created["spec"].(map[string]any)
				if fmt.Sprint(spec["app_install_ids"]) != "[3]" || spec["apps"] == nil || spec["app_config"] == nil {
					t.Fatal(spec)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
