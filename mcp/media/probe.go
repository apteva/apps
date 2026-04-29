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
	FormatName  string
	DurationMs  int64
	Bitrate     int64
	HasVideo    bool
	HasAudio    bool
	IsImage     bool
	Width       int
	Height      int
	FPS         float64
	VideoCodec  string
	Channels    int
	SampleRate  int
	AudioCodec  string
	Raw         string // raw JSON, persisted as-is
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

func isImageCodec(c string) bool {
	switch c {
	case "mjpeg", "png", "gif", "webp", "bmp", "tiff", "heif", "heic":
		return true
	}
	return false
}
