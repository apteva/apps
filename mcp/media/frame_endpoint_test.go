package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestMissingDecodedDurationUsesBoundedVideoEndpoint(t *testing.T) {
	base := "APTEVA_VIDEO_SCAN count=757 first=0 last=25.208333 duration=0\nAPTEVA_AUDIO_SCAN count=1185 first=0 end=25.28\n"
	for _, tc := range []struct {
		name, endpoint string
		ok             bool
	}{{"B-frame drain", "25.270", true}, {"far endpoint", "26.270", false}, {"video too short", "25.208", false}, {"no metadata", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			log := base
			if tc.endpoint != "" {
				log += "APTEVA_VIDEO_ENDPOINT end=" + tc.endpoint + "\n"
			}
			v := parseTrimValidation(log)
			err := checkTrimValidation(v, 25270)
			if (err == nil) != tc.ok {
				t.Fatalf("validation=%+v err=%v", v, err)
			}
			if tc.ok && (v.Video.LastFrameDurationMs != 62 || v.Video.LastFrameDurationSource != "video_stream_endpoint") {
				t.Fatalf("duration=%+v", v.Video)
			}
		})
	}
	// Endpoint metadata must not clear a real opening gap or an explicit short
	// final picture. Only an absent duration can use the bounded inference.
	for _, log := range []string{strings.Replace(base, "first=0", "first=0.066", 1), strings.Replace(base, "duration=0", "duration=0.010", 1)} {
		if err := checkTrimValidation(parseTrimValidation(log+"APTEVA_VIDEO_ENDPOINT end=25.270\n"), 25270); err == nil {
			t.Fatal("invalid timeline accepted")
		}
	}
}
func TestRemoteMissingDurationEndpointGuard(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk unavailable")
	}
	var log strings.Builder
	for i := 0; i < 757; i++ {
		fmt.Fprintf(&log, "[Parsed_showinfo_0] n:%d pts_time:%.9f duration: 0 duration_time:0\n", i, float64(i)*25.208333/756)
	}
	log.WriteString("APTEVA_VIDEO_ENDPOINT end=25.270\n")
	cmd := exec.Command("awk", "-v", "strict=1", "-v", "expected=25.27", compactFrameLogAWK)
	cmd.Stdin = strings.NewReader(log.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard: %v %s", err, out)
	}
	v := parseTrimValidation(string(out))
	if checkTrimValidation(v, 25270) != nil || v.Video.LastFrameDurationSource != "video_stream_endpoint" {
		t.Fatalf("summary=%s", out)
	}
	bad := strings.Replace(log.String(), "end=25.270", "end=26.270", 1)
	cmd = exec.Command("awk", "-v", "strict=1", "-v", "expected=25.27", compactFrameLogAWK)
	cmd.Stdin = strings.NewReader(bad)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("unsupported hold passed: %s", out)
	}
}

func TestDecodedPictureCountIgnoresRepeatedLogFields(t *testing.T) {
	lines := []string{"[Parsed_showinfo_0] n: 0 pts_time:0 duration: 1 duration_time:0.033333", "[Parsed_showinfo_0] n: 1 pts_time:0.033333 duration: 1 duration_time:0.033333", "[Parsed_showinfo_0] n: 1 pts_time:0.033333 duration: 1 duration_time:0.033333"}
	var collector analysisLogCollector
	for _, l := range lines {
		collector.add(l)
	}
	if v := parseTrimValidation(collector.result()); v.Video.FramesChecked != 2 {
		t.Fatalf("count=%+v", v.Video)
	}
	cmd := exec.Command("awk", compactFrameLogAWK)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if v := parseTrimValidation(string(out)); v.Video.FramesChecked != 2 {
		t.Fatalf("remote count=%+v", v.Video)
	}
}

func TestValidationCountUsesPassthroughDecodeProgress(t *testing.T) {
	lines := []string{"[Parsed_showinfo_0] n: 0 pts_time:0 duration: 1 duration_time:0.033333", "[Parsed_showinfo_0] n: 1000 pts_time:0.033333 duration: 1 duration_time:0.033333", "frame=2"}
	var collector analysisLogCollector
	for _, l := range lines {
		collector.add(l)
	}
	if v := parseTrimValidation(collector.result()); v.Video.FramesChecked != 2 {
		t.Fatalf("count=%+v", v.Video)
	}
	cmd := exec.Command("awk", compactFrameLogAWK)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if v := parseTrimValidation(string(out)); v.Video.FramesChecked != 2 {
		t.Fatalf("remote count=%+v", v.Video)
	}
}
