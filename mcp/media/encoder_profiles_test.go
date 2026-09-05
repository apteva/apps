package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestVideoQualityLegacyRemainsDefault(t *testing.T) {
	for op, options := range map[string]map[string]any{
		"transcode":    {"format": "mp4"},
		"resize":       {"width": 640, "keep_aspect": true},
		"crop":         {"width": 320, "height": 180},
		"extract_reel": {"start_ms": 0, "end_ms": 1000},
	} {
		t.Run(op, func(t *testing.T) {
			params, _ := json.Marshal(options)
			original, err := buildPlanBase(op, []string{"1"}, params, "", ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			implicit, err := buildPlan(op, []string{"1"}, params, "", ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			options["encoder_profile"] = "legacy"
			params, _ = json.Marshal(options)
			explicit, err := buildPlan(op, []string{"1"}, params, "", ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, implicit) || !reflect.DeepEqual(original, explicit) {
				t.Fatal("default or explicit Legacy changed the original render plan")
			}
		})
	}
}

func TestVideoQualityLevelsAndQueuedCompatibility(t *testing.T) {
	for _, level := range []struct{ name, alias, preset, crf string }{
		{"low", "preview", "veryfast", "28"},
		{"medium", "balanced", "medium", "23"},
		{"high", "quality", "slow", "18"},
	} {
		t.Run(level.name, func(t *testing.T) {
			makePlan := func(name string) *opPlan {
				raw, _ := json.Marshal(map[string]any{"format": "mp4", "encoder_profile": name})
				plan, err := buildPlan("transcode", []string{"1"}, raw, "", ".mp4")
				if err != nil {
					t.Fatal(err)
				}
				return plan
			}
			plan := makePlan(level.name)
			if !reflect.DeepEqual(plan, makePlan(level.alias)) {
				t.Fatal("queued profile compatibility changed")
			}
			options := map[string]string{}
			for i := 0; i+1 < len(plan.Args); i++ {
				options[plan.Args[i]] = plan.Args[i+1]
			}
			if options["-c:v"] != "libx264" || options["-crf"] != level.crf || options["-preset"] != level.preset {
				t.Fatalf("wrong quality settings: %v", plan.Args)
			}
		})
	}
	if encoderProfileSchema()["default"] != "legacy" {
		t.Fatal("tool schema does not default to Legacy")
	}
}
