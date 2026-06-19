package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type RenderQA struct {
	DurationSeconds float64  `json:"duration_seconds,omitempty"`
	PeakDB          float64  `json:"peak_db,omitempty"`
	RMSDB           float64  `json:"rms_db,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

func analyzeRender(path string, edit *Edit) RenderQA {
	qa := RenderQA{Warnings: timelineWarnings(edit)}
	if strings.TrimSpace(path) != "" {
		qa.DurationSeconds = probeRenderDuration(path)
		peak, rms := probeRenderVolume(path)
		if !math.IsNaN(peak) {
			qa.PeakDB = peak
		}
		if !math.IsNaN(rms) {
			qa.RMSDB = rms
		}
	}
	return qa
}

func encodeRenderQA(qa RenderQA) string {
	b, err := json.Marshal(qa)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeRenderQA(s string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func timelineWarnings(edit *Edit) []string {
	if edit == nil {
		return nil
	}
	var warnings []string
	for _, c := range audioTimelineClips(edit) {
		if c.AI != nil {
			peak := c.AI.PeakDB
			if peak == 0 && c.AI.AudioAnalysis != nil {
				peak = c.AI.AudioAnalysis.PeakDB
			}
			if peak < -35 && peak != 0 {
				warnings = append(warnings, "AI audio clip "+clipLabel(c)+" is very quiet (peak "+trimFloat(peak)+" dB)")
			}
		}
	}
	type interval struct {
		start float64
		end   float64
	}
	var intervals []interval
	for _, c := range audioTimelineClips(edit) {
		if clipAssetType(c, "audio") == "silence" {
			continue
		}
		intervals = append(intervals, interval{start: c.Start, end: c.Start + clipDuration(c)})
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	for i := 1; i < len(intervals); i++ {
		if gap := intervals[i].start - intervals[i-1].end; gap >= 10 {
			warnings = append(warnings, "long audio gap of "+trimFloat(gap)+"s before "+trimFloat(intervals[i].start)+"s")
		}
	}
	return warnings
}

func clipLabel(c Clip) string {
	if c.UID != "" {
		return c.UID
	}
	if c.AI != nil && c.AI.MediaKind != "" {
		return c.AI.MediaKind
	}
	return c.Asset.Src
}

func probeRenderDuration(path string) float64 {
	cmd := exec.Command(ffprobePath(),
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func probeRenderVolume(path string) (float64, float64) {
	cmd := exec.Command(ffmpegPath(), "-hide_banner", "-nostats", "-i", path, "-af", "volumedetect", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return math.NaN(), math.NaN()
	}
	return parseComposerVolumeDetect(stderr.String())
}

func parseComposerVolumeDetect(s string) (float64, float64) {
	peak := math.NaN()
	rms := math.NaN()
	meanRe := regexp.MustCompile(`mean_volume:\s*([-0-9.]+)\s*dB`)
	maxRe := regexp.MustCompile(`max_volume:\s*([-0-9.]+)\s*dB`)
	if m := meanRe.FindStringSubmatch(s); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			rms = v
		}
	}
	if m := maxRe.FindStringSubmatch(s); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			peak = v
		}
	}
	return peak, rms
}
