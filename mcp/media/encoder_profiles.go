package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// One public quality contract for all render tools. Earlier development names
// remain accepted by the planner so queued jobs keep their encoding settings.
func encoderProfileSchema() map[string]any {
	return map[string]any{
		"type": "string", "enum": []string{"legacy", "low", "medium", "high"}, "default": "legacy",
		"description": "Export video quality: legacy (default, existing encoding settings), low (smaller files, less detail), medium (balanced size/detail), high (more detail, larger files and slower encoding). Applies to H.264 MP4/MOV/MKV video encoding; resolution and frame rate are unchanged by this setting. Stream-copy and image/audio operations retain their existing behavior.",
	}
}

// Legacy remains the default. Explicit profiles make speed/size/quality choices
// reproducible; stream-copy and image outputs retain their existing behavior.
func buildPlan(op string, sources []string, params json.RawMessage, name, sourceExt string) (*opPlan, error) {
	plan, err := buildPlanBase(op, sources, params, name, sourceExt)
	if err != nil {
		return nil, err
	}
	var opts struct {
		EncoderProfile string `json:"encoder_profile"`
	}
	if err := json.Unmarshal(params, &opts); err != nil {
		return nil, err
	}
	profile := opts.EncoderProfile
	if profile == "" || profile == "legacy" {
		return plan, nil
	}
	preset, crf := "", ""
	switch profile {
	case "low", "preview":
		preset, crf = "veryfast", "28"
	case "medium", "balanced":
		preset, crf = "medium", "23"
	case "high", "quality":
		preset, crf = "slow", "18"
	default:
		return nil, fmt.Errorf("encoder_profile must be legacy, low, medium or high")
	}
	if strings.HasPrefix(plan.ContentType, "image/") {
		return plan, nil
	}
	if op == "trim" || op == "concat" || op == "extract_frame" || op == "audio_extract" || op == "audio_filter" {
		return plan, nil
	}
	ext := strings.ToLower(filepath.Ext(plan.Filename))
	if ext != ".mp4" && ext != ".mov" && ext != ".mkv" {
		return nil, fmt.Errorf("encoder_profile requires MP4, MOV or MKV output")
	}
	for i, a := range plan.Args {
		if (a == "-c:v" || a == "-vcodec") && i+1 < len(plan.Args) && plan.Args[i+1] != "libx264" {
			return nil, fmt.Errorf("encoder_profile conflicts with explicit codec %s", plan.Args[i+1])
		}
	}
	plan.Args = append(plan.Args, "-c:v", "libx264", "-preset", preset, "-crf", crf)
	return plan, nil
}
