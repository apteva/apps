package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

func TestSmartCropSupplementPositionsLongSourceStill(t *testing.T) {
	const fourHours = int64(4 * 60 * 60 * 1000)
	focus := int64(3 * 60 * 60 * 1000)
	got := smartCropSupplementPositions(smartCropTarget{FocusMs: focus}, fourHours)
	if len(got) != smartCropTemporalMaxSamples {
		t.Fatalf("positions=%d want=%d: %v", len(got), smartCropTemporalMaxSamples, got)
	}
	if got[0] != focus-20_000 || got[len(got)-1] != focus+20_000 {
		t.Fatalf("still context=%v", got)
	}
	if got[len(got)/2] != focus {
		t.Fatalf("exact requested frame missing: %v", got)
	}
}

func TestSmartCropSupplementPositionsLongSourceShortReel(t *testing.T) {
	const eightHours = int64(8 * 60 * 60 * 1000)
	start := int64(6 * 60 * 60 * 1000)
	end := start + 30_000
	got := smartCropSupplementPositions(smartCropTarget{StartMs: start, EndMs: end}, eightHours)
	if len(got) != 7 {
		t.Fatalf("positions=%d want=7: %v", len(got), got)
	}
	if got[0] != start || got[len(got)-1] != end {
		t.Fatalf("reel boundaries missing: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i]-got[i-1] > smartCropSupplementIntervalMs {
			t.Fatalf("supplemental gap=%d exceeds %d: %v", got[i]-got[i-1], smartCropSupplementIntervalMs, got)
		}
	}
}

func TestSmartCropSupplementPositionsCapsLongReelWork(t *testing.T) {
	start := int64(60 * 60 * 1000)
	end := start + 10*60*1000
	got := smartCropSupplementPositions(smartCropTarget{StartMs: start, EndMs: end}, 4*60*60*1000)
	if len(got) != smartCropV2MaxSamples {
		t.Fatalf("positions=%d want=%d", len(got), smartCropV2MaxSamples)
	}
	if got[0] != start || got[len(got)-1] != end {
		t.Fatalf("capped reel boundaries missing: %v", got)
	}
}

func TestSmartCropSupplementPositionsClampAtSourceEdges(t *testing.T) {
	got := smartCropSupplementPositions(smartCropTarget{FocusMs: 2_000}, 12_000)
	if got[0] != 0 || got[len(got)-1] != 11_900 {
		t.Fatalf("edge-clamped positions=%v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("positions are not unique and sorted: %v", got)
		}
	}
}

func TestBuildRemoteSmartCropSampleScript(t *testing.T) {
	url := "https://storage.example/video.mp4?signature=it's-safe&part=1"
	script, err := buildRemoteSmartCropSampleScript("/opt/apteva/ffmpeg", url,
		[]int64{187_410, 162_495, 174_000, 174_000})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FFMPEG=" + shellQuote("/opt/apteva/ffmpeg"),
		"SIGNED_URL=" + shellQuote(url),
		"scale=320:-2",
		remoteSmartCropMarker,
		"for POS_MS in 162495 174000 187410",
		"ACTIVE=0",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("remote sample script missing %q:\n%s", want, script)
		}
	}
	if _, err := buildRemoteSmartCropSampleScript("ffmpeg", url, []int64{1}); err == nil {
		t.Fatal("one remote sample position should be rejected")
	}
	if _, err := buildRemoteSmartCropSampleScript("ffmpeg", url, []int64{-1, 2}); err == nil {
		t.Fatal("negative remote sample position should be rejected")
	}
}

func TestBuildRemoteSmartCropSampleScriptKeepsTrackingCadence(t *testing.T) {
	positions := make([]int64, 27)
	for i := range positions {
		positions[i] = int64(i+1) * 1_000
	}
	script, err := buildRemoteSmartCropSampleScript("ffmpeg", "https://storage.example/video.mp4", positions)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "for POS_MS in 1000 2000 3000 4000 5000") ||
		!strings.Contains(script, " 25000 26000 27000; do") {
		t.Fatalf("remote script dropped requested tracking frames:\n%s", script)
	}
}

func TestParseRemoteSmartCropSamples(t *testing.T) {
	positions := []int64{1_000, 2_000, 3_000}
	frame := func(subjectX int) string {
		img := image.NewRGBA(image.Rect(0, 0, 256, 144))
		for y := 0; y < 144; y++ {
			for x := 0; x < 256; x++ {
				img.Set(x, y, color.RGBA{R: 90, G: 100, B: 110, A: 255})
			}
		}
		for y := 24; y < 140; y++ {
			for x := subjectX; x < minInt(256, subjectX+45); x++ {
				img.Set(x, y, color.RGBA{R: 220, G: 130, B: 85, A: 255})
			}
		}
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 70}); err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(encoded.Bytes())
	}
	out := strings.Join([]string{
		"unrelated remote output",
		remoteSmartCropMarker + "3000:" + frame(180),
		remoteSmartCropMarker + "not-a-time:AAAA",
		remoteSmartCropMarker + "1000:" + frame(20),
		remoteSmartCropMarker + "9000:" + frame(100),
	}, "\n")
	samples, err := parseRemoteSmartCropSamples(out, positions, 1280, 720, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples=%d want=2", len(samples))
	}
	if samples[0].point.AtMs != 1_000 || samples[1].point.AtMs != 3_000 {
		t.Fatalf("samples are not sorted or allowed: %+v", samples)
	}
	if _, err := parseRemoteSmartCropSamples(remoteSmartCropMarker+"1000:invalid", positions,
		1280, 720, 9, 16); err == nil {
		t.Fatal("insufficient decodable remote samples should fail")
	}
}

func TestParseRemoteSmartCropSamplesRejectsOversizedOutput(t *testing.T) {
	out := strings.Repeat("x", remoteSmartCropMaxOutputBytes+1)
	if _, err := parseRemoteSmartCropSamples(out, []int64{1, 2}, 1280, 720, 9, 16); err == nil {
		t.Fatal("oversized remote sample output should fail")
	}
}
