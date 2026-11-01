package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var errNoProbe = errors.New("ffprobe is not installed")

type mediaProbe struct {
	DurationMS int64
	Width      int
	Height     int
	FPS        float64
	VideoCodec string
	AudioCodec string
	HasAudio   bool
	SizeBytes  int64
}

func (p mediaProbe) apply(r *Recording) {
	r.DurationMS, r.Width, r.Height, r.FPS = p.DurationMS, p.Width, p.Height, p.FPS
	r.VideoCodec, r.AudioCodec, r.Audio = p.VideoCodec, p.AudioCodec, p.HasAudio
}

// lookTool is replaced in tests.
var lookTool = exec.LookPath

func probeMedia(ctx context.Context, path string) (mediaProbe, error) {
	bin, err := lookTool("ffprobe")
	if err != nil {
		return mediaProbe{}, errNoProbe
	}
	out, err := runCommand(ctx, bin, "-v", "error", "-print_format", "json", "-show_streams", "-show_format", path)
	if err != nil {
		return mediaProbe{}, err
	}
	return parseProbe(out)
}

func parseProbe(out []byte) (mediaProbe, error) {
	var raw struct {
		Streams []struct {
			CodecType    string            `json:"codec_type"`
			CodecName    string            `json:"codec_name"`
			Width        int               `json:"width"`
			Height       int               `json:"height"`
			AvgFrameRate string            `json:"avg_frame_rate"`
			RFrameRate   string            `json:"r_frame_rate"`
			Duration     string            `json:"duration"`
			Tags         map[string]string `json:"tags"`
			SideData     []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return mediaProbe{}, errors.New("unreadable ffprobe output")
	}
	var p mediaProbe
	video := false
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if video {
				continue
			}
			video = true
			p.Width, p.Height, p.VideoCodec = s.Width, s.Height, s.CodecName
			p.FPS = frameRate(s.AvgFrameRate)
			if p.FPS == 0 {
				p.FPS = frameRate(s.RFrameRate)
			}
			rotation := 0.0
			if v, err := strconv.ParseFloat(s.Tags["rotate"], 64); err == nil {
				rotation = v
			}
			for _, sd := range s.SideData {
				if sd.Rotation != 0 {
					rotation = sd.Rotation
				}
			}
			if int(math.Abs(rotation))%180 == 90 {
				p.Width, p.Height = p.Height, p.Width
			}
		case "audio":
			if !p.HasAudio {
				p.HasAudio, p.AudioCodec = true, s.CodecName
			}
		}
	}
	if !video || p.Width <= 0 || p.Height <= 0 {
		return mediaProbe{}, errors.New("no video stream")
	}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		p.DurationMS = int64(math.Round(d * 1000))
	}
	p.SizeBytes, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	return p, nil
}

func frameRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if !ok {
		return n
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d == 0 {
		return 0
	}
	return math.Round(n/d*100) / 100
}

// Size is a portrait frame; landscape output swaps width and height.
type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

// Preset describes an export target as data. Arguments for ffmpeg are built
// from validated fields only, never from caller-supplied flags.
type Preset struct {
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Container        string          `json:"container"`
	VideoCodec       string          `json:"video_codec"`
	Sizes            map[string]Size `json:"sizes,omitempty"`
	DefaultVariant   string          `json:"default_variant,omitempty"`
	Orientations     []string        `json:"orientations,omitempty"`
	Fit              string          `json:"fit,omitempty"`
	MaxFPS           float64         `json:"max_fps,omitempty"`
	MinSeconds       float64         `json:"min_seconds,omitempty"`
	MaxSeconds       float64         `json:"max_seconds,omitempty"`
	MaxBytes         int64           `json:"max_bytes,omitempty"`
	VideoBitrateKbps int             `json:"video_bitrate_kbps,omitempty"`
	H264Level        string          `json:"h264_level,omitempty"`
	Audio            string          `json:"audio"`
	AudioBitrateKbps int             `json:"audio_bitrate_kbps,omitempty"`
	AudioRate        int             `json:"audio_rate,omitempty"`
	SilentIfMissing  bool            `json:"silent_if_missing,omitempty"`
}

func builtinPresets() []Preset {
	return []Preset{
		{Name: "raw", Description: "Trim only. Streams are copied without re-encoding, so cuts land on keyframes.", Container: "mp4", VideoCodec: "copy", Audio: "copy"},
		{Name: "app_store_preview", Description: "App Store Connect app preview: 15–30 s, H.264 High ≤ level 4.0, ≤30 fps, stereo 256 kbps AAC, ≤500 MB. Choose the display class with variant.", Container: "mp4", VideoCodec: "h264",
			Sizes: map[string]Size{
				"iphone":             {886, 1920},
				"iphone_home_large":  {1080, 1920},
				"iphone_home_medium": {750, 1334},
				"ipad":               {1200, 1600},
				"ipad_9_7":           {900, 1200},
				"mac":                {1080, 1920},
				"apple_tv":           {1080, 1920},
			},
			DefaultVariant: "iphone", Fit: "pad", MaxFPS: 30, MinSeconds: 15, MaxSeconds: 30, MaxBytes: 500 << 20, VideoBitrateKbps: 11000, H264Level: "4.0", Audio: "aac", AudioBitrateKbps: 256, AudioRate: 48000, SilentIfMissing: true},
		{Name: "play_promo", Description: "1080p H.264 for a YouTube upload linked as a Google Play promo video.", Container: "mp4", VideoCodec: "h264", Sizes: map[string]Size{"1080p": {1080, 1920}}, DefaultVariant: "1080p", Fit: "pad", MaxFPS: 60, VideoBitrateKbps: 12000, H264Level: "4.2", Audio: "aac", AudioBitrateKbps: 192, AudioRate: 48000, SilentIfMissing: true},
		{Name: "social_vertical", Description: "1080×1920 vertical video for short-form social posts.", Container: "mp4", VideoCodec: "h264", Sizes: map[string]Size{"1080": {1080, 1920}}, DefaultVariant: "1080", Orientations: []string{"portrait"}, Fit: "pad", MaxFPS: 60, VideoBitrateKbps: 10000, H264Level: "4.2", Audio: "aac", AudioBitrateKbps: 192, AudioRate: 48000},
		{Name: "social_square", Description: "1080×1080 square video.", Container: "mp4", VideoCodec: "h264", Sizes: map[string]Size{"1080": {1080, 1080}}, DefaultVariant: "1080", Fit: "pad", MaxFPS: 60, VideoBitrateKbps: 8000, H264Level: "4.2", Audio: "aac", AudioBitrateKbps: 192, AudioRate: 48000},
	}
}

// landscapeOnly lists variants that App Store Connect accepts only in landscape.
var landscapeOnly = map[string]bool{"mac": true, "apple_tv": true}

var presetName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validatePreset(p Preset) error {
	bad := func(msg string) error { return problem("invalid_argument", "preset "+p.Name+": "+msg) }
	if !presetName.MatchString(p.Name) {
		return problem("invalid_argument", "preset name must be lowercase letters, digits, _ or -")
	}
	if p.Container != "mp4" && p.Container != "mov" {
		return bad("container must be mp4 or mov")
	}
	if p.VideoCodec != "h264" && p.VideoCodec != "copy" {
		return bad("video_codec must be h264 or copy")
	}
	if p.Audio != "aac" && p.Audio != "none" && p.Audio != "copy" {
		return bad("audio must be aac, none or copy")
	}
	if p.VideoCodec == "copy" && (len(p.Sizes) > 0 || p.MaxFPS > 0) {
		return bad("copy cannot resize or change frame rate")
	}
	if p.VideoCodec == "h264" && len(p.Sizes) == 0 {
		return bad("h264 presets need at least one size")
	}
	for name, s := range p.Sizes {
		if !presetName.MatchString(name) || s.W < 16 || s.H < 16 || s.W > 4096 || s.H > 4096 || s.W%2 != 0 || s.H%2 != 0 {
			return bad("sizes must be even numbers between 16 and 4096")
		}
	}
	if p.DefaultVariant != "" {
		if _, ok := p.Sizes[p.DefaultVariant]; !ok {
			return bad("default_variant must name a size")
		}
	}
	for _, o := range p.Orientations {
		if o != "portrait" && o != "landscape" {
			return bad("orientations must be portrait or landscape")
		}
	}
	switch p.Fit {
	case "", "pad", "crop", "stretch":
	default:
		return bad("fit must be pad, crop or stretch")
	}
	switch p.H264Level {
	case "", "3.1", "4.0", "4.1", "4.2", "5.0", "5.1", "5.2":
	default:
		return bad("h264_level is not supported")
	}
	if p.MaxFPS < 0 || p.MaxFPS > 120 || p.MinSeconds < 0 || p.MaxSeconds < 0 || p.MaxSeconds > 3600 || (p.MaxSeconds > 0 && p.MinSeconds > p.MaxSeconds) {
		return bad("frame rate or duration limits are out of range")
	}
	if p.VideoBitrateKbps < 0 || p.VideoBitrateKbps > 100000 || p.AudioBitrateKbps < 0 || p.AudioBitrateKbps > 512 || p.MaxBytes < 0 {
		return bad("bitrates or size limit are out of range")
	}
	if p.AudioRate != 0 && p.AudioRate != 44100 && p.AudioRate != 48000 {
		return bad("audio_rate must be 44100 or 48000")
	}
	if len(p.Description) > 500 {
		return bad("description is too long")
	}
	return nil
}

func findPreset(name string, extra []Preset) (Preset, bool) {
	for _, p := range append(extra, builtinPresets()...) {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

type exportPlan struct {
	Preset      Preset
	Variant     string
	Size        Size
	Orientation string
	Fit         string
	FromSec     float64
	DurSec      float64
	FPS         float64
	SourceAudio bool
}

func planExport(src Recording, p Preset, args map[string]any) (exportPlan, error) {
	x := exportPlan{Preset: p, Fit: p.Fit, SourceAudio: src.Audio}
	if x.Fit == "" {
		x.Fit = "pad"
	}
	if f := str(args, "fit"); f != "" {
		if f != "pad" && f != "crop" && f != "stretch" {
			return x, problem("invalid_argument", "fit must be pad, crop or stretch")
		}
		x.Fit = f
	}
	total := float64(src.DurationMS) / 1000
	from, to := 0.0, total
	var err error
	if from, err = trimPoint(src, args, "from", 0); err != nil {
		return x, err
	}
	if to, err = trimPoint(src, args, "to", total); err != nil {
		return x, err
	}
	_, explicitTo := args["to"]
	explicitTo = explicitTo || str(args, "to_mark") != ""
	if total > 0 && to > total {
		to = total
	}
	x.FromSec = from
	if total > 0 || explicitTo {
		if to <= from {
			return x, problem("invalid_argument", "trim end must be after its start")
		}
		x.DurSec = to - from
	}
	if x.DurSec > 0 {
		if p.MaxSeconds > 0 && x.DurSec > p.MaxSeconds+0.001 {
			return x, problem("invalid_argument", fmt.Sprintf("selected duration %.1f s exceeds the %s maximum of %.0f s; set from/to or from_mark/to_mark", x.DurSec, p.Name, p.MaxSeconds))
		}
		if x.DurSec < p.MinSeconds-0.001 {
			return x, problem("invalid_argument", fmt.Sprintf("selected duration %.1f s is below the %s minimum of %.0f s", x.DurSec, p.Name, p.MinSeconds))
		}
	} else if p.MaxSeconds > 0 {
		return x, problem("invalid_argument", "the source duration is unknown (install ffprobe), so set an explicit to/to_mark for this preset")
	}
	if p.VideoCodec == "copy" {
		return x, nil
	}
	x.Variant = str(args, "variant")
	if x.Variant == "" {
		x.Variant = p.DefaultVariant
	}
	if x.Variant == "" && len(p.Sizes) == 1 {
		for k := range p.Sizes {
			x.Variant = k
		}
	}
	size, ok := p.Sizes[x.Variant]
	if !ok {
		return x, problem("invalid_argument", "unknown variant for preset "+p.Name+"; choose one of "+strings.Join(sortedKeys(p.Sizes), ", "))
	}
	x.Orientation = str(args, "orientation")
	if x.Orientation == "" {
		x.Orientation = "portrait"
		if src.Width > src.Height {
			x.Orientation = "landscape"
		}
	}
	if x.Orientation != "portrait" && x.Orientation != "landscape" {
		return x, problem("invalid_argument", "orientation must be portrait or landscape")
	}
	allowed := p.Orientations
	if landscapeOnly[x.Variant] && p.Name == "app_store_preview" {
		allowed = []string{"landscape"}
	}
	if len(allowed) > 0 && !contains(allowed, x.Orientation) {
		return x, problem("invalid_argument", x.Variant+" output must be "+strings.Join(allowed, " or "))
	}
	x.Size = size
	if x.Orientation == "landscape" && size.W < size.H || x.Orientation == "portrait" && size.W > size.H {
		x.Size = Size{size.H, size.W}
	}
	x.FPS = p.MaxFPS
	if x.FPS > 30 && src.FPS > 0 && src.FPS <= 30.5 {
		x.FPS = 30
	}
	return x, nil
}

func trimPoint(src Recording, args map[string]any, field string, def float64) (float64, error) {
	if mark := str(args, field+"_mark"); mark != "" {
		for _, m := range src.Markers {
			if m.Name == mark {
				return float64(m.AtMS) / 1000, nil
			}
		}
		return 0, problem("invalid_argument", "marker not found: "+mark)
	}
	if _, ok := args[field]; !ok {
		return def, nil
	}
	v, ok := number(args[field])
	if !ok || v < 0 || v > 86400 {
		return 0, problem("invalid_argument", field+" must be seconds from the start of the recording")
	}
	return v, nil
}

func secs(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// ffmpegArgs builds the export command. Every value comes from a validated
// plan; nothing is copied from caller input as a flag.
func ffmpegArgs(src, out string, x exportPlan) []string {
	p := x.Preset
	args := []string{"-hide_banner", "-nostdin", "-y", "-v", "error"}
	if x.FromSec > 0 {
		args = append(args, "-ss", secs(x.FromSec))
	}
	args = append(args, "-i", src)
	silent := p.Audio == "aac" && !x.SourceAudio && p.SilentIfMissing
	if silent {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("anullsrc=r=%d:cl=stereo", orInt(p.AudioRate, 48000)))
	}
	args = append(args, "-map", "0:v:0")
	switch {
	case silent:
		args = append(args, "-map", "1:a:0")
	case p.Audio != "none":
		args = append(args, "-map", "0:a:0?")
	}
	if x.DurSec > 0 {
		args = append(args, "-t", secs(x.DurSec))
	}
	if p.VideoCodec == "copy" {
		args = append(args, "-c:v", "copy")
	} else {
		w, h := x.Size.W, x.Size.H
		var filters []string
		switch x.Fit {
		case "crop":
			filters = append(filters, fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", w, h), fmt.Sprintf("crop=%d:%d", w, h))
		case "stretch":
			filters = append(filters, fmt.Sprintf("scale=%d:%d", w, h))
		default:
			filters = append(filters, fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", w, h), fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", w, h))
		}
		filters = append(filters, "setsar=1")
		if x.FPS > 0 {
			filters = append(filters, "fps="+strconv.FormatFloat(x.FPS, 'f', -1, 64))
		}
		filters = append(filters, "format=yuv420p")
		args = append(args, "-vf", strings.Join(filters, ","), "-c:v", "libx264", "-profile:v", "high", "-preset", "slow", "-pix_fmt", "yuv420p")
		if p.H264Level != "" {
			args = append(args, "-level:v", p.H264Level)
		}
		if p.VideoBitrateKbps > 0 {
			kb := p.VideoBitrateKbps
			args = append(args, "-b:v", fmt.Sprintf("%dk", kb), "-maxrate", fmt.Sprintf("%dk", kb*11/10), "-bufsize", fmt.Sprintf("%dk", kb*2))
		} else {
			args = append(args, "-crf", "18")
		}
	}
	switch p.Audio {
	case "aac":
		args = append(args, "-c:a", "aac", "-ac", "2", "-ar", strconv.Itoa(orInt(p.AudioRate, 48000)), "-b:a", fmt.Sprintf("%dk", orInt(p.AudioBitrateKbps, 192)))
		if silent {
			args = append(args, "-shortest")
		}
	case "copy":
		args = append(args, "-c:a", "copy")
	default:
		args = append(args, "-an")
	}
	return append(args, "-movflags", "+faststart", out)
}

func checkExport(x exportPlan, probe mediaProbe) []Check {
	p := x.Preset
	checks := []Check{}
	add := func(name string, ok bool, detail string) { checks = append(checks, Check{name, ok, detail}) }
	d := float64(probe.DurationMS) / 1000
	if p.MinSeconds > 0 || p.MaxSeconds > 0 {
		ok := d >= p.MinSeconds-0.05 && (p.MaxSeconds == 0 || d <= p.MaxSeconds+0.05)
		add("duration", ok, fmt.Sprintf("%.2f s (allowed %.0f–%.0f s)", d, p.MinSeconds, p.MaxSeconds))
	}
	if p.VideoCodec == "h264" {
		add("resolution", probe.Width == x.Size.W && probe.Height == x.Size.H, fmt.Sprintf("%d×%d (expected %d×%d)", probe.Width, probe.Height, x.Size.W, x.Size.H))
		add("video_codec", probe.VideoCodec == "h264", probe.VideoCodec)
	}
	if p.MaxFPS > 0 {
		add("frame_rate", probe.FPS > 0 && probe.FPS <= p.MaxFPS+0.01, fmt.Sprintf("%.2f fps (max %.0f)", probe.FPS, p.MaxFPS))
	}
	switch p.Audio {
	case "aac":
		add("audio", probe.HasAudio && probe.AudioCodec == "aac", map[bool]string{true: "AAC stereo", false: "missing AAC track"}[probe.HasAudio && probe.AudioCodec == "aac"])
	case "none":
		add("audio", !probe.HasAudio, map[bool]string{true: "no audio", false: "unexpected audio track"}[!probe.HasAudio])
	}
	if p.MaxBytes > 0 {
		add("file_size", probe.SizeBytes > 0 && probe.SizeBytes <= p.MaxBytes, fmt.Sprintf("%.1f MB (max %.0f MB)", float64(probe.SizeBytes)/(1<<20), float64(p.MaxBytes)/(1<<20)))
	}
	return checks
}

// export starts an asynchronous transcode of a finished recording.
func (e *engine) export(pid string, args map[string]any) (Recording, error) {
	src, err := e.recording(pid, str(args, "recording_id"))
	if err != nil {
		return src, err
	}
	if src.Status != "ready" {
		return src, problem("invalid_argument", "only ready recordings can be exported; this one is "+src.Status)
	}
	srcPath := e.recordingPath(src)
	if _, err := os.Stat(srcPath); err != nil {
		return src, problem("not_found", "the source video is no longer on the runner")
	}
	var p Preset
	if def, ok := args["preset_definition"]; ok {
		if err := decodeMap(def, &p); err != nil {
			return src, problem("invalid_argument", "invalid preset definition")
		}
	} else {
		var found bool
		if p, found = findPreset(str(args, "preset"), nil); !found {
			return src, problem("invalid_argument", "unknown preset; list presets with mobile_record(action=presets)")
		}
	}
	if err := validatePreset(p); err != nil {
		return src, err
	}
	x, err := planExport(src, p, args)
	if err != nil {
		return src, err
	}
	ffmpeg, lookErr := lookTool("ffmpeg")
	if lookErr != nil {
		return src, problem("setup_required", "ffmpeg is required on the runner to export recordings")
	}
	now := time.Now()
	ext := "." + p.Container
	label := p.Name
	if x.Variant != "" {
		label += "-" + x.Variant
	}
	r := Recording{ID: id("rec_"), ProjectID: pid, Kind: "export", ParentID: src.ID, SessionID: src.SessionID, RunID: src.RunID, DeviceID: src.DeviceID, Platform: src.Platform, Method: "ffmpeg", Preset: label, Status: "processing", ContentType: contentType(ext), Markers: []Marker{}, Checks: []Check{}, CreatedAt: now.Unix(), StartedAt: now.Unix()}
	r.FileName = strings.TrimSuffix(src.FileName, filepath.Ext(src.FileName)) + "-" + label + ext
	for _, m := range src.Markers {
		at := float64(m.AtMS)/1000 - x.FromSec
		if at >= 0 && (x.DurSec == 0 || at <= x.DurSec) {
			r.Markers = append(r.Markers, Marker{Name: m.Name, AtMS: int64(at * 1000)})
		}
	}
	e.mu.Lock()
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		return r, problem("infrastructure_error", "runner is shutting down")
	}
	if e.exporting >= 2 {
		e.mu.Unlock()
		return r, problem("device_unavailable", "two exports are already running; try again shortly")
	}
	e.exporting++
	e.mu.Unlock()
	if err := e.insertRecording(r); err != nil {
		e.mu.Lock()
		e.exporting--
		e.mu.Unlock()
		return r, err
	}
	out := e.recordingPath(r)
	e.recWG.Add(1)
	go func(r Recording) {
		defer e.recWG.Done()
		defer func() { e.mu.Lock(); e.exporting--; e.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(e.ctx, 30*time.Minute)
		defer cancel()
		var stderr limitedBuffer
		stderr.limit = 8192
		cmd := exec.CommandContext(ctx, ffmpeg, ffmpegArgs(srcPath, out, x)...)
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		finished := time.Now().Unix()
		r.FinishedAt = &finished
		if runErr != nil {
			r.Status = "failed"
			r.Error = "ffmpeg failed: " + lastLine(stderr.String())
			if ctx.Err() != nil {
				r.Error = "export was interrupted"
			}
			_ = os.Remove(out)
			_ = e.saveRecording(r)
			return
		}
		probe, err := probeMedia(ctx, out)
		if err != nil {
			info, statErr := os.Stat(out)
			if statErr == nil {
				r.SizeBytes = info.Size()
			}
			r.Status = "ready"
			r.Error = "output could not be verified: " + err.Error()
			_ = e.saveRecording(r)
			return
		}
		probe.apply(&r)
		r.SizeBytes = probe.SizeBytes
		if info, err := os.Stat(out); err == nil {
			r.SizeBytes = info.Size()
			probe.SizeBytes = info.Size()
		}
		r.Checks = checkExport(x, probe)
		r.Status = "ready"
		var failed []string
		for _, c := range r.Checks {
			if !c.OK {
				failed = append(failed, c.Name+" "+c.Detail)
			}
		}
		if len(failed) > 0 {
			r.Error = "output does not meet the preset: " + strings.Join(failed, "; ")
		}
		_ = e.saveRecording(r)
	}(r)
	return r, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return "no error output"
	}
	return lines[len(lines)-1]
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]Size) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
