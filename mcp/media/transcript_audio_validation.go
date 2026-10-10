package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Decode the encoded proxy without integer conversion, which would conceal
// runaway float samples by clipping them. astats covers the complete stream;
// it uses bounded memory and does not need Python on local or remote hosts.
const transcriptAudioStatsFilter = "aformat=sample_fmts=dbl,astats=reset=0:measure_perchannel=none:measure_overall=Peak_level+RMS_level+Peak_count+Number_of_samples+Number_of_NaNs+Number_of_Infs"
const transcriptAudioMaxPeakDBFS = 0.5 // tolerate small MP3 reconstruction overshoot
const transcriptAudioMaxSaturatedFraction = 0.001

type transcriptAudioValidation struct {
	Method     string   `json:"method"`
	Recipe     string   `json:"recipe"`
	DecodeOK   bool     `json:"decode_ok"`
	Samples    int64    `json:"samples"`
	DurationMs int64    `json:"duration_ms"`
	PeakDBFS   *float64 `json:"peak_dbfs"`
	RMSDBFS    *float64 `json:"rms_dbfs"`
	PeakCount  float64  `json:"peak_count"`
	NaNs       float64  `json:"nan_samples"`
	Infs       float64  `json:"infinite_samples"`
}

type transcriptAudioEvidence struct {
	StorageFileID int64                      `json:"storage_file_id"`
	Validation    *transcriptAudioValidation `json:"validation"`
}

func transcriptAudioValidationArgs(path string) []string {
	return []string{"-hide_banner", "-nostats", "-nostdin", "-v", "info", "-xerror", "-err_detect", "explode", "-i", path, "-map", "0:a:0", "-vn", "-af", transcriptAudioStatsFilter, "-f", "null", "-"}
}

func validateTranscriptAudio(ctx context.Context, ffmpeg, path string, expectedMs int64) (*transcriptAudioValidation, error) {
	logs := &transcriptAudioLogBuffer{max: transcriptAudioLogMaxBytes}
	cmd := exec.CommandContext(ctx, ffmpeg, transcriptAudioValidationArgs(path)...)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("transcript_audio_invalid: decode: %s", transcriptAudioFFmpegFailure(err, logs.Bytes()))
	}
	v, err := parseTranscriptAudioValidation(string(logs.Bytes()))
	if err != nil {
		return nil, err
	}
	if err := v.check(expectedMs); err != nil {
		return nil, err
	}
	return v, nil
}

func parseTranscriptAudioValidation(log string) (*transcriptAudioValidation, error) {
	fields := map[string]float64{}
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "Parsed_astats_") {
			continue
		}
		_, line, ok := strings.Cut(line, "] ")
		if !ok {
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil {
			fields[key] = n
		}
	}
	keys := []string{"Peak level dB", "RMS level dB", "Peak count", "Number of samples", "Number of NaNs", "Number of Infs"}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("transcript_audio_invalid: missing full-stream statistic %q", key)
		}
	}
	for _, key := range keys {
		n := fields[key]
		if math.IsNaN(n) || math.IsInf(n, 1) || (math.IsInf(n, -1) && key != "Peak level dB" && key != "RMS level dB") {
			return nil, fmt.Errorf("transcript_audio_invalid: nonfinite statistic %q", key)
		}
	}
	v := &transcriptAudioValidation{Method: "full_decode_float_astats", Recipe: transcriptAudioRecipe, DecodeOK: true, Samples: int64(fields["Number of samples"]), PeakCount: fields["Peak count"], NaNs: fields["Number of NaNs"], Infs: fields["Number of Infs"]}
	if n := fields["Peak level dB"]; !math.IsInf(n, -1) {
		v.PeakDBFS = &n
	}
	if n := fields["RMS level dB"]; !math.IsInf(n, -1) {
		v.RMSDBFS = &n
	}
	v.DurationMs = v.Samples * 1000 / 16000
	return v, nil
}

func transcriptAudioDurationTolerance(expectedMs int64) float64 {
	return math.Max(250, math.Min(2000, float64(expectedMs)*0.001))
}

func (v *transcriptAudioValidation) check(expectedMs int64) error {
	if v == nil || !v.DecodeOK || v.Method != "full_decode_float_astats" || v.Recipe != transcriptAudioRecipe || v.Samples <= 0 || v.Samples > 1000000000000 || v.DurationMs != v.Samples*1000/16000 || v.NaNs != 0 || v.Infs != 0 || v.PeakCount < 0 || math.IsNaN(v.PeakCount) || math.IsInf(v.PeakCount, 0) {
		return errors.New("transcript_audio_invalid: incomplete or nonfinite decoded signal")
	}
	for _, n := range []*float64{v.PeakDBFS, v.RMSDBFS} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0)) {
			return errors.New("transcript_audio_invalid: nonfinite decoded level")
		}
	}
	if v.PeakDBFS != nil && (*v.PeakDBFS > transcriptAudioMaxPeakDBFS || (*v.PeakDBFS >= -0.001 && v.PeakCount/float64(v.Samples) > transcriptAudioMaxSaturatedFraction)) {
		return fmt.Errorf("transcript_audio_invalid: runaway/clipped decoded signal (peak=%.3f dBFS, peak samples=%.0f/%d)", *v.PeakDBFS, v.PeakCount, v.Samples)
	}
	if expectedMs > 0 && math.Abs(float64(v.DurationMs-expectedMs)) > transcriptAudioDurationTolerance(expectedMs) {
		return fmt.Errorf("transcript_audio_invalid: decoded duration %d ms differs from source %d ms", v.DurationMs, expectedMs)
	}
	return nil
}

// The same full decode and limits run before remote upload. Missing statistics
// fail closed. Silent sources (-inf levels) remain valid; speech completeness
// is a separate review, not something signal validation can prove.
func remoteTranscriptAudioValidator() string {
	return fmt.Sprintf(`validate_audio() {
  "$FFMPEG" -hide_banner -nostats -nostdin -v info -xerror -err_detect explode -i "$1" -map 0:a:0 -vn -af %s -f null - >validation.log 2>&1 || { tail -c "$AUDIO_LOG_TAIL_BYTES" validation.log >&2; return 1; }
  awk -v expected="$EXPECTED_DURATION_MS" '
  /Parsed_astats_/ {
    sub(/^.*\] /, ""); i=index($0, ": "); if(i) { k=substr($0,1,i-1); v=substr($0,i+2); seen[k]=1; stats[k]=v; }
  }
  END {
    n=split("Peak level dB|RMS level dB|Peak count|Number of samples|Number of NaNs|Number of Infs", keys, "|");
    for(i=1;i<=n;i++) { k=keys[i]; v=tolower(stats[k]); if(!seen[k] || (!(v=="-inf" && (k=="Peak level dB" || k=="RMS level dB")) && v !~ /^[-+]?[0-9]+([.][0-9]+)?([eE][-+]?[0-9]+)?$/)) { print "transcript_audio_invalid: missing/nonfinite statistic " k > "/dev/stderr"; exit 1; } }
    samples=stats["Number of samples"]+0; peak=stats["Peak level dB"]; count=stats["Peak count"]+0; duration=int(samples*1000/16000);
    tolerance=expected*0.001; if(tolerance<250) tolerance=250; if(tolerance>2000) tolerance=2000; delta=duration-expected; if(delta<0) delta=-delta;
    if(samples<=0 || samples>1000000000000 || stats["Number of NaNs"]+0!=0 || stats["Number of Infs"]+0!=0 || count<0 || (peak!="-inf" && (peak+0>%g || (peak+0>=-0.001 && count/samples>%g))) || (expected>0 && delta>tolerance)) { print "transcript_audio_invalid: invalid signal/duration; samples=" samples ", peak=" peak ", peak_count=" count ", duration_ms=" duration > "/dev/stderr"; exit 1; }
    rms=stats["RMS level dB"]; if(peak=="-inf") peak="null"; if(rms=="-inf") rms="null";
    printf "{\"method\":\"full_decode_float_astats\",\"recipe\":\"%s\",\"decode_ok\":true,\"samples\":%%.0f,\"duration_ms\":%%.0f,\"peak_dbfs\":%%s,\"rms_dbfs\":%%s,\"peak_count\":%%.0f,\"nan_samples\":0,\"infinite_samples\":0}\n", samples,duration,peak,rms,count;
  }' validation.log >validation.json
}
`, shellQuote(transcriptAudioStatsFilter), transcriptAudioMaxPeakDBFS, transcriptAudioMaxSaturatedFraction, transcriptAudioRecipe)
}

func transcriptAudioEvidenceJSON(e *transcriptAudioEvidence) string {
	b, _ := json.Marshal(e)
	return string(b)
}
