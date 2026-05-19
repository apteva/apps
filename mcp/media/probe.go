package main

// ffprobe wrapper: runs the binary, parses its JSON, distills the
// fields we keep as columns. raw_probe carries the full output for
// power users + future schema additions.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Probe struct {
	FormatName string
	DurationMs int64
	Bitrate    int64
	HasVideo   bool
	HasAudio   bool
	IsImage    bool
	// Width/Height are reported in DISPLAY-space — what users
	// actually see in a player. For sources with a rotation tag
	// (e.g. iPhone landscape recordings tagged rotation=90 to
	// display portrait), the codec frame's pixel dims and the
	// displayed dims differ: parseProbeBytes swaps Width↔Height
	// when Rotation is 90 or 270. Downstream consumers (panels,
	// agents, the crop math in renders) all treat Width/Height as
	// "what the user sees" — keeping the abstraction consistent
	// regardless of rotation metadata.
	Width  int
	Height int
	// Rotation in {0, 90, 180, 270}. ffprobe reports this as a
	// signed float in the displaymatrix side_data; we normalise
	// to one of the four 90°-aligned values. Renderers use this to
	// prepend a transpose filter + pass -noautorotate to ffmpeg so
	// the filter chain sees a frame matching the displayed
	// orientation, not the raw codec orientation.
	Rotation   int
	FPS        float64
	VideoCodec string
	Channels   int
	SampleRate int
	AudioCodec string
	Raw        string // raw JSON, persisted as-is
}

type ffprobeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	CodecType    string `json:"codec_type"` // video | audio | subtitle | data
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	RFrameRate   string `json:"r_frame_rate,omitempty"`
	Channels     int    `json:"channels,omitempty"`
	SampleRate   string `json:"sample_rate,omitempty"`
	NbFrames     string `json:"nb_frames,omitempty"`
	Duration     string `json:"duration,omitempty"`
	// SideDataList holds the display-matrix metadata that carries
	// rotation. Only the "Display Matrix" side_data_type entry
	// matters for our purposes; everything else is ignored.
	SideDataList []ffprobeSideData `json:"side_data_list,omitempty"`
}

// ffprobeSideData lifts only the rotation field we care about. The
// real side_data structure is much richer (HDR metadata, mastering
// info, etc.); we don't touch the rest.
type ffprobeSideData struct {
	SideDataType string  `json:"side_data_type"`
	Rotation     float64 `json:"rotation,omitempty"`
}

// runProbe shells out to ffprobe on a local file path. Returns parsed
// + raw JSON. Errors are wrapped with the binary's stderr trimmed for
// readability — ffprobe is verbose.
func runProbe(ctx context.Context, ffprobePath, file string) (*Probe, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		file,
	)
	out, err := cmd.Output()
	if err != nil {
		ee := &exec.ExitError{}
		stderr := ""
		if asErr, ok := err.(*exec.ExitError); ok {
			ee = asErr
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr == "" {
			return nil, fmt.Errorf("ffprobe: %w", err)
		}
		return nil, fmt.Errorf("ffprobe: %s", stderr)
	}
	return parseProbeBytes(out)
}

// parseProbeBytes turns raw ffprobe JSON output into a Probe. Shared
// between the local runProbe and the remote-indexer path which gets
// the same JSON shape back over SSH.
func parseProbeBytes(out []byte) (*Probe, error) {
	var raw ffprobeOut
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe json parse: %w", err)
	}
	p := Probe{
		FormatName: raw.Format.FormatName,
		DurationMs: parseDurationMs(raw.Format.Duration),
		Bitrate:    parseInt64(raw.Format.BitRate),
		Raw:        string(out),
	}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if !p.HasVideo {
				p.HasVideo = true
				p.Width = s.Width
				p.Height = s.Height
				p.Rotation = normaliseRotation(extractRotation(s.SideDataList))
				// Display-space: when the source is tagged with a
				// 90° or 270° rotation, the displayed orientation
				// is the codec frame rotated quarter-turn. Swap
				// W↔H so every downstream consumer (UI, agents,
				// crop math) sees the orientation the user
				// actually sees. The Rotation column tells the
				// renderer how to bake this in at render time
				// via a transpose filter.
				if p.Rotation == 90 || p.Rotation == 270 {
					p.Width, p.Height = p.Height, p.Width
				}
				p.FPS = parseRational(s.RFrameRate)
				p.VideoCodec = s.CodecName
				// Detect "image" — single-frame video stream,
				// no audio, common image container codecs.
				if isImageCodec(s.CodecName) || s.NbFrames == "1" {
					p.IsImage = true
				}
			}
		case "audio":
			if !p.HasAudio {
				p.HasAudio = true
				p.Channels = s.Channels
				p.SampleRate = parseInt(s.SampleRate)
				p.AudioCodec = s.CodecName
			}
		}
	}
	// An image is only an image if there's no audio — otherwise we
	// likely have a static-frame video clip, treat as video.
	if p.HasAudio {
		p.IsImage = false
	}
	return &p, nil
}

func parseDurationMs(s string) int64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * 1000)
}

func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	v, _ := strconv.Atoi(s)
	return v
}

// parseRational accepts "30/1", "30000/1001", "30", returns the value.
// ffprobe emits rationals to keep precision on NTSC-style frame rates.
func parseRational(s string) float64 {
	if s == "" {
		return 0
	}
	if i := strings.IndexByte(s, '/'); i > 0 {
		num, errN := strconv.ParseFloat(s[:i], 64)
		den, errD := strconv.ParseFloat(s[i+1:], 64)
		if errN == nil && errD == nil && den != 0 {
			return num / den
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// extractRotation pulls the rotation value out of an ffprobe stream's
// side_data_list. ffprobe emits multiple side_data entries per
// stream; only the "Display Matrix" entry has the rotation field
// we care about. Returns 0 when absent.
func extractRotation(entries []ffprobeSideData) float64 {
	for _, e := range entries {
		if e.SideDataType == "Display Matrix" {
			return e.Rotation
		}
	}
	return 0
}

// normaliseRotation quantises ffprobe's signed float rotation into
// one of {0, 90, 180, 270}. ffprobe reports rotation as a signed
// degrees value: -90 (90° clockwise from display) is equivalent to
// 270, -180 == 180, etc. We canonicalise to the positive equivalent
// so the renderer's transpose-direction lookup table only has to
// handle four cases.
//
// Off-axis rotations (e.g. 45°) get rounded to the nearest 90°;
// real-world camera footage never produces those, and even if it did
// ffmpeg's transpose filter only does 90° increments — better an
// imperfect rotation than a silent no-op.
func normaliseRotation(deg float64) int {
	// Bring into [0, 360). Go's math.Mod can return negatives.
	r := deg
	for r < 0 {
		r += 360
	}
	for r >= 360 {
		r -= 360
	}
	switch {
	case r < 45 || r >= 315:
		return 0
	case r < 135:
		return 90
	case r < 225:
		return 180
	default:
		return 270
	}
}

func isImageCodec(c string) bool {
	switch c {
	case "mjpeg", "png", "gif", "webp", "bmp", "tiff", "heif", "heic":
		return true
	}
	return false
}
