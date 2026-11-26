package videoframes

import (
	"errors"
	"reflect"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func ptr[T any](v T) *T { return &v }

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestNormalizeRejects(t *testing.T) {
	cases := map[string]computer.VideoFrameOptions{
		"neither":         {},
		"both":            {Times: []float64{1}, Every: 1},
		"too many":        {Times: make([]float64, 25)},
		"negative time":   {Times: []float64{-1}},
		"tiny interval":   {Every: 0.01},
		"end before":      {Every: 1, Start: 5, End: ptr(2.0)},
		"start with list": {Times: []float64{1}, Start: 2},
		"frames":          {Every: 1, MaxFrames: 25},
		"edge":            {Every: 1, MaxEdge: 4000},
		"format":          {Every: 1, Format: "gif"},
		"quality":         {Every: 1, Quality: 101},
		"timeout":         {Every: 1, Timeout: 31 * time.Second},
		"index+selector":  {Every: 1, VideoIndex: ptr(1), Selector: "video"},
	}
	for name, opts := range cases {
		if _, err := Normalize(opts); code(err) == "" {
			t.Errorf("%s: want an error code, got %v", name, err)
		}
	}
}

func TestNormalizeDefaults(t *testing.T) {
	got, err := Normalize(computer.VideoFrameOptions{Every: 2, Format: "JPG"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxFrames != 12 || got.MaxEdge != 1024 || got.Format != "jpeg" || got.Quality != 80 || got.Timeout != 20*time.Second || got.MaxBytes != 16<<20 {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestPlanExactTimes(t *testing.T) {
	opts, _ := Normalize(computer.VideoFrameOptions{Times: []float64{0.5, 40, 10, 27.33}})
	times, skipped, truncated := Plan(opts, 27.33)
	// The exact end is clamped just inside the last frame.
	if !reflect.DeepEqual(times, []float64{0.5, 10, 27.28}) || truncated {
		t.Fatalf("times %v truncated %v", times, truncated)
	}
	if len(skipped) != 1 || skipped[0].RequestedS != 40 || skipped[0].Reason != "beyond_duration" {
		t.Fatalf("skipped %+v", skipped)
	}
}

func TestPlanInterval(t *testing.T) {
	opts, _ := Normalize(computer.VideoFrameOptions{Every: 2, MaxFrames: 24})
	times, _, truncated := Plan(opts, 7)
	if !reflect.DeepEqual(times, []float64{0, 2, 4, 6}) || truncated {
		t.Fatalf("times %v", times)
	}
	opts, _ = Normalize(computer.VideoFrameOptions{Every: 2, Start: 1, End: ptr(5.0)})
	if times, _, _ = Plan(opts, 30); !reflect.DeepEqual(times, []float64{1, 3, 5}) {
		t.Fatalf("start/end times %v", times)
	}
	opts, _ = Normalize(computer.VideoFrameOptions{Every: 1, MaxFrames: 3})
	if times, _, truncated = Plan(opts, 27.3); len(times) != 3 || !truncated {
		t.Fatalf("cap: %v truncated=%v", times, truncated)
	}
	opts, _ = Normalize(computer.VideoFrameOptions{Every: 1, Start: 50})
	if times, skipped, _ := Plan(opts, 10); len(times) != 0 || len(skipped) != 1 {
		t.Fatalf("start past end: %v %v", times, skipped)
	}
}

func TestChoose(t *testing.T) {
	ready := func(i, visible, w, h int, playing bool) computer.VideoCandidate {
		return computer.VideoCandidate{Index: i, VisibleArea: visible, OnScreenW: w, OnScreenH: h, VideoWidth: 720, VideoHeight: 1280, Duration: 27, ReadyState: 4, Playing: playing}
	}
	opts := computer.VideoFrameOptions{Every: 1}

	// A playing video hidden under a popup (0x0) beats a paused, visible one.
	got, err := choose(listResult{Candidates: []computer.VideoCandidate{ready(0, 90000, 300, 300, false), ready(1, 0, 0, 0, true)}}, opts)
	if err != nil || got == nil || *got != 1 {
		t.Fatalf("playing first: %v %v", got, err)
	}
	// Two identical 0x0 paused videos are ambiguous and listed.
	_, err = choose(listResult{Candidates: []computer.VideoCandidate{ready(0, 0, 0, 0, false), ready(1, 0, 0, 0, false)}}, opts)
	var e *Error
	if !errors.As(err, &e) || e.Code != "ambiguous_video" || len(e.Candidates) != 2 {
		t.Fatalf("tie: %v", err)
	}
	// video_index resolves a tie; out of range is invalid_target.
	opts.VideoIndex = ptr(1)
	if got, err = choose(listResult{Candidates: []computer.VideoCandidate{ready(0, 0, 0, 0, false), ready(1, 0, 0, 0, false)}}, opts); err != nil || *got != 1 {
		t.Fatalf("index: %v %v", got, err)
	}
	opts.VideoIndex = ptr(5)
	if _, err = choose(listResult{Candidates: []computer.VideoCandidate{ready(0, 0, 0, 0, false)}}, opts); code(err) != "invalid_target" {
		t.Fatalf("index range: %v", err)
	}
	// No metadata yet: keep waiting (nil, nil). No video at all: not found.
	opts.VideoIndex = nil
	if got, err = choose(listResult{Candidates: []computer.VideoCandidate{{Index: 0}}}, opts); got != nil || err != nil {
		t.Fatalf("not ready: %v %v", got, err)
	}
	if _, err = choose(listResult{}, opts); code(err) != "video_not_found" {
		t.Fatalf("none: %v", err)
	}
	// A selector match is used even when another video ranks higher.
	opts.Selector = "video.b"
	if got, _ = choose(listResult{SelectorIndex: 0, Candidates: []computer.VideoCandidate{ready(0, 0, 0, 0, false), ready(1, 90000, 300, 300, true)}}, opts); *got != 0 {
		t.Fatalf("selector: %v", *got)
	}
}

func TestMarkDuplicates(t *testing.T) {
	frames := []computer.VideoFrame{{Index: 0, SHA256: "a"}, {Index: 1, SHA256: "b"}, {Index: 2, SHA256: "a"}}
	markDuplicates(frames)
	if frames[0].DuplicateOf != nil || frames[1].DuplicateOf != nil || frames[2].DuplicateOf == nil || *frames[2].DuplicateOf != 0 {
		t.Fatalf("duplicates: %+v", frames)
	}
}
