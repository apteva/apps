package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestMediaAskUsesNearestExistingKeyframeWithoutWriting(t *testing.T) {
	stub := boundOpenAI()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK("The label is readable.")}
	ctx := newTestCtxWithPlatform(t, stub)
	probe := sampleVideoProbe()
	probe.DurationMs = 20_000
	if err := upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha", "/clips/", "clip.mp4"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id  int64
		pos int64
	}{{101, 1_000}, {102, 5_000}, {103, 10_000}} {
		if err := upsertDerivation(ctx.AppDB(), testProj, "1", "keyframe", item.id, 320, 180, item.pos); err != nil {
			t.Fatal(err)
		}
	}

	out, err := (&App{}).toolAsk(ctx, map[string]any{
		"file_id": "1", "question": "Is the label readable?", "at_ms": float64(4_600),
	})
	if err != nil {
		t.Fatalf("toolAsk: %v", err)
	}
	result := out.(map[string]any)
	evidence := result["evidence"].([]askEvidence)
	if len(evidence) != 1 || evidence[0].StorageFileID != "102" || evidence[0].PositionMs != 5_000 {
		t.Fatalf("evidence=%+v", evidence)
	}
	coverage := result["coverage"].(askCoverage)
	if coverage.ArtifactsCreated || coverage.Method != "nearest_existing_keyframe" {
		t.Fatalf("coverage=%+v", coverage)
	}
	if len(stub.ExecuteCalls) != 1 {
		t.Fatalf("integration calls=%d", len(stub.ExecuteCalls))
	}
	messages := stub.ExecuteCalls[0].Input["messages"].([]map[string]any)
	parts := messages[1]["content"].([]map[string]any)
	joined := ""
	for _, p := range parts {
		if text, _ := p["text"].(string); text != "" {
			joined += text
		}
	}
	if !strings.Contains(joined, "5000 ms") || strings.Contains(joined, "4600 ms") {
		t.Fatalf("prompt did not identify actual cached evidence: %q", joined)
	}
	row, _ := getMedia(ctx.AppDB(), testProj, "1")
	if row.Description != "" || len(row.Derivations) != 3 {
		t.Fatalf("media_ask mutated media: description=%q derivations=%d", row.Description, len(row.Derivations))
	}
}

func TestMediaAskAtMsRefusesToGenerateMissingFrame(t *testing.T) {
	stub := boundOpenAI()
	ctx := newTestCtxWithPlatform(t, stub)
	probe := sampleVideoProbe()
	probe.DurationMs = 20_000
	upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha", "", "clip.mp4")
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 99, 320, 180, 0)

	_, err := (&App{}).toolAsk(ctx, map[string]any{
		"file_id": "1", "question": "What is visible?", "at_ms": float64(4_600),
	})
	if err == nil || !strings.Contains(err.Error(), "will not extract or generate") {
		t.Fatalf("expected explicit no-generation error, got %v", err)
	}
	if len(stub.ExecuteCalls) != 0 {
		t.Fatalf("integration should not run without evidence, calls=%d", len(stub.ExecuteCalls))
	}
}

func TestMediaAskWholeVideoSamplesOnlyExistingDerivations(t *testing.T) {
	stub := boundOpenAI()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK("A person demonstrates a product.")}
	ctx := newTestCtxWithPlatform(t, stub)
	probe := sampleVideoProbe()
	probe.DurationMs = 30_000
	upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha", "", "clip.mp4")
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 99, 320, 180, 0)
	for i, pos := range []int64{1_000, 5_000, 10_000, 15_000, 20_000, 25_000} {
		upsertDerivation(ctx.AppDB(), testProj, "1", "keyframe", int64(100+i), 320, 180, pos)
	}

	out, err := (&App{}).toolAsk(ctx, map[string]any{
		"file_id": "1", "question": "What happens?", "frame_count": float64(4), "include_transcript": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := out.(map[string]any)["evidence"].([]askEvidence)
	if len(evidence) != 4 || evidence[0].Kind != "thumbnail" {
		t.Fatalf("evidence=%+v", evidence)
	}
	for _, e := range evidence {
		if e.Selection != "existing_canonical_thumbnail" && e.Selection != "existing_storyboard_sample" {
			t.Fatalf("unexpected generated evidence marker: %+v", e)
		}
	}
}
