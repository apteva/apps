package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Scan every decoded picture, before any sampling of expensive visual metrics.
// The remote and local commands return bounded summaries instead of frame logs.
type frameValidation struct {
	WindowStartCovered  bool   `json:"window_start_covered,omitempty"`
	CoveringFrameMs     *int64 `json:"covering_frame_ms,omitempty"`
	CoveringFrameEndMs  *int64 `json:"covering_frame_end_ms,omitempty"`
	RangeStartMs        int64  `json:"range_start_ms"`
	FramesChecked       int64  `json:"frames_checked"`
	FirstFrameMs        int64  `json:"first_frame_ms"`
	LastFrameMs         int64  `json:"last_frame_ms"`
	LastFrameDurationMs int64  `json:"last_frame_duration_ms"`
	OpeningBlack        bool   `json:"opening_black"`
	EndingBlack         bool   `json:"ending_black"`
	BlackDetection      string `json:"black_detection"`
}

type trimValidation struct {
	DecodeOK      bool             `json:"decode_ok"`
	Video         *frameValidation `json:"video,omitempty"`
	AudioFirstMs  *int64           `json:"audio_first_ms,omitempty"`
	AudioEndMs    *int64           `json:"audio_end_ms,omitempty"`
	BlackSegments []timedSegment   `json:"black_segments"`
	Issues        []analysisIssue  `json:"issues"`
}

// Also used by read-only analysis, so signed input banners and per-frame
// checksums never reach the remote response. showinfo checksum=0 is cheap.
const compactFrameLogAWK = `
function field(prefix, i) { for (i=1;i<=NF;i++) if (index($i,prefix)==1) return substr($i,length(prefix)+1); return 0 }
{
 original=$0;
 if($0 ~ /showinfo@coverage/) {
  if(match($0,/pts_time:[-0-9.e+]+[[:space:]]+duration:[[:space:]]*[-0-9]+[[:space:]]+duration_time:/)) {
   $0=substr($0,RSTART); pt=field("pts_time:"); if(pt<0) { pc++; pl=pt; pd=field("duration_time:"); }
  }
  next;
 }
 if(match($0,/pts_time:[-0-9.e+]+[[:space:]]+fmt:[^ ]+[[:space:]]+channels:/)) {
  $0=substr($0,RSTART); if(ac==0) af=field("pts_time:"); ac++; al=field("pts_time:"); ar=field("rate:"); an=field("nb_samples:");
 }
 $0=original;
 if(match($0,/pts_time:[-0-9.e+]+[[:space:]]+duration:[[:space:]]*[-0-9]+[[:space:]]+duration_time:/)) {
  $0=substr($0,RSTART); if(vc==0) vf=field("pts_time:"); vc++; vl=field("pts_time:"); vd=field("duration_time:");
 }
 $0=original;
 if($0 ~ /Parsed_showinfo_.* n:/ && $0 !~ /duration:/) { if(vc==0) vf=field("pts_time:"); vc++; vl=field("pts_time:"); vd=field("duration_time:"); }
}
/lavfi\.(signalstats\.[A-Z]+|blur|block|freezedetect\.|black_end)|black_start:|^[[:space:]]*(I:|LRA:|Peak:)|Peak level dB:|RMS level dB:|Dynamic range:|DC offset:|silence_start:|silence_end:/ { print }
END {
 if(pc>0) printf "APTEVA_VIDEO_PREROLL last=%.9f duration=%.9f\n",pl,pd;
 if(vc>0) printf "APTEVA_VIDEO_SCAN count=%d first=%.9f last=%.9f duration=%.9f\n",vc,vf,vl,vd;
 if(ac>0) printf "APTEVA_AUDIO_SCAN count=%d first=%.9f end=%.9f\n",ac,af,al+(ar>0?an/ar:0);
 if(strict && expected>0 && vc>0 && vl+vd+(vd*2>0.05?vd*2:0.05)<expected) { print "trim_validation_failed: video ends before requested interval"; exit 1 }
 if(strict && ((vc+ac)==0 || (vc>0 && (vf>0.002 || vf< -0.002)) || (ac>0 && (af>0.002 || af< -0.002)))) { print "trim_validation_failed: missing decoded stream or nonzero opening timestamp"; exit 1 }
}`

var videoScanRE = regexp.MustCompile(`APTEVA_VIDEO_SCAN count=(\d+) first=([-0-9.e+]+) last=([-0-9.e+]+) duration=([-0-9.e+]+)`)
var videoPrerollRE = regexp.MustCompile(`APTEVA_VIDEO_PREROLL last=([-0-9.e+]+) duration=([-0-9.e+]+)`)
var audioScanRE = regexp.MustCompile(`APTEVA_AUDIO_SCAN count=(\d+) first=([-0-9.e+]+) end=([-0-9.e+]+)`)
var showFrameRE = regexp.MustCompile(`pts_time:([-0-9.e+]+)\s+(?:duration:\s*[-0-9]+\s+)?duration_time:([-0-9.e+]+)`)
var showAudioRE = regexp.MustCompile(`pts_time:([-0-9.e+]+)\s+fmt:\S+\s+channels:.*?rate:(\d+).*?nb_samples:(\d+)`)

type analysisLogCollector struct {
	log                          strings.Builder
	count                        int64
	first, last, duration        string
	audioCount                   int64
	audioEnd                     float64
	audioFirst                   string
	prerollLast, prerollDuration string
}

func (c *analysisLogCollector) add(line string) {
	if strings.Contains(line, "showinfo@coverage") {
		for _, m := range showFrameRE.FindAllStringSubmatch(line, -1) {
			pt, _ := strconv.ParseFloat(m[1], 64)
			if pt < 0 {
				c.prerollLast, c.prerollDuration = m[1], m[2]
			}
		}
		return
	}
	for _, m := range showFrameRE.FindAllStringSubmatch(line, -1) {
		if c.count == 0 {
			c.first = m[1]
		}
		c.count++
		c.last = m[1]
		c.duration = m[2]
	}
	for _, m := range showAudioRE.FindAllStringSubmatch(line, -1) {
		if c.audioCount == 0 {
			c.audioFirst = m[1]
		}
		c.audioCount++
		start, _ := strconv.ParseFloat(m[1], 64)
		rate, _ := strconv.ParseFloat(m[2], 64)
		samples, _ := strconv.ParseFloat(m[3], 64)
		if rate > 0 {
			c.audioEnd = start + samples/rate
		}
	}
	if strings.Contains(line, "lavfi.") || strings.Contains(line, "black_start:") || strings.HasPrefix(line, "APTEVA_") || integratedLUFSRE.MatchString(line) || loudnessRangeRE.MatchString(line) || truePeakRE.MatchString(line) || samplePeakRE.MatchString(line) || rmsRE.MatchString(line) || dynamicRangeRE.MatchString(line) || dcOffsetRE.MatchString(line) || silenceStartRE.MatchString(line) || silenceEndRE.MatchString(line) {
		c.log.WriteString(line)
		c.log.WriteByte('\n')
	}
}

func (c *analysisLogCollector) result() string {
	if c.prerollLast != "" {
		fmt.Fprintf(&c.log, "APTEVA_VIDEO_PREROLL last=%s duration=%s\n", c.prerollLast, c.prerollDuration)
	}
	if c.count > 0 {
		fmt.Fprintf(&c.log, "APTEVA_VIDEO_SCAN count=%d first=%s last=%s duration=%s\n", c.count, c.first, c.last, c.duration)
	}
	if c.audioCount > 0 {
		fmt.Fprintf(&c.log, "APTEVA_AUDIO_SCAN count=%d first=%s end=%.9f\n", c.audioCount, c.audioFirst, c.audioEnd)
	}
	return c.log.String()
}

func runCompactedFFmpeg(ctx context.Context, binary string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return "", err
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); writer.Close(); done <- err }()
	var logs analysisLogCollector
	scan := bufio.NewScanner(reader)
	scan.Buffer(make([]byte, 4096), 1024*1024)
	for scan.Scan() {
		logs.add(scan.Text())
	}
	reader.Close()
	err := <-done
	if scan.Err() != nil {
		return logs.result(), scan.Err()
	}
	if ctx.Err() != nil {
		return logs.result(), fmt.Errorf("ffmpeg analysis timed out: %w", ctx.Err())
	}
	return logs.result(), err
}

func parseFrameValidation(log string, offset int64) *frameValidation {
	m := videoScanRE.FindStringSubmatch(log)
	if m == nil {
		return nil
	}
	count, _ := strconv.ParseInt(m[1], 10, 64)
	fv := &frameValidation{RangeStartMs: offset, FramesChecked: count, FirstFrameMs: secondsToMs(m[2]) + offset, LastFrameMs: secondsToMs(m[3]) + offset, LastFrameDurationMs: secondsToMs(m[4]), BlackDetection: "every_decoded_frame_near_black_98_percent_pixels"}
	if p := videoPrerollRE.FindStringSubmatch(log); p != nil {
		start, _ := strconv.ParseFloat(p[1], 64)
		duration, _ := strconv.ParseFloat(p[2], 64)
		if start < 0 && duration > 0 && start+duration >= -0.000001 {
			a := secondsToMs(p[1]) + offset
			b := int64((start+duration)*1000+0.5) + offset
			fv.WindowStartCovered = true
			fv.CoveringFrameMs, fv.CoveringFrameEndMs = &a, &b
		}
	}
	return fv
}

func trimValidationArgs(source string) []string {
	return []string{"-filter_threads", "1", "-hide_banner", "-nostats", "-v", "info", "-xerror", "-i", source, "-map", "0:v:0?", "-map", "0:a:0?", "-vf", "blackdetect=d=0:pix_th=0.10,metadata=mode=print:key=lavfi.black_end,showinfo=checksum=0", "-af", "ashowinfo", "-sn", "-dn", "-f", "null", "-"}
}

func parseTrimValidation(log string) trimValidation {
	v := parseVisualAnalysis(log, 0, 0)
	r := trimValidation{DecodeOK: true, Video: v.FrameValidation, BlackSegments: v.BlackSegments, Issues: visualIssues(v)}
	if m := audioScanRE.FindStringSubmatch(log); m != nil {
		n := secondsToMs(m[2])
		r.AudioFirstMs = &n
		end := secondsToMs(m[3])
		r.AudioEndMs = &end
	}
	return r
}

func checkTrimValidation(v trimValidation, duration ...int64) error {
	if len(duration) > 0 && v.Video != nil {
		tolerance := v.Video.LastFrameDurationMs * 2
		if tolerance < 50 {
			tolerance = 50
		}
		if v.Video.LastFrameMs+v.Video.LastFrameDurationMs+tolerance < duration[0] {
			return trimValidationError("video ends before requested interval")
		}
	}
	if v.Video == nil && v.AudioFirstMs == nil {
		return trimValidationError("output has no decoded pictures or audio samples")
	}
	if v.Video != nil && (v.Video.FirstFrameMs > 2 || v.Video.FirstFrameMs < -2) {
		return trimValidationError("video does not start at zero")
	}
	if v.AudioFirstMs != nil && (*v.AudioFirstMs > 2 || *v.AudioFirstMs < -2) {
		return trimValidationError("audio does not start at zero")
	}
	return nil
}

// Cache hits carry the original validation in resolved_params, rather than
// implying that a reused output was scanned again.
func persistTrimValidation(app *sdk.AppCtx, row *RenderRow, validation trimValidation) error {
	var params map[string]any
	if err := json.Unmarshal(row.Params, &params); err != nil {
		return err
	}
	params["trim_validation"] = validation
	if diagnostics, ok := params["trim_diagnostics"].(map[string]any); ok {
		diagnostics["app_version"] = app.Manifest().Version
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := renderUpdateResolvedParams(app.AppDB(), row.ID, raw); err != nil {
		return err
	}
	row.Params = raw
	return nil
}

func trimValidationScript(binary, output string, durationMs int64) string {
	return shellCommand(binary, trimValidationArgs(output)) + " > trim-validation.log 2>&1 || { echo 'trim_validation_failed: output decode failed'; exit 1; }\n" +
		"awk -v strict=1 -v expected=" + formatSeconds(durationMs) + " " + shellQuote(compactFrameLogAWK) + " trim-validation.log\n"
}
