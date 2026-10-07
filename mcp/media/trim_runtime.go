package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const trimAlgorithmVersion = "media-accurate-trim-1"

// Preserve indexed precision and color signaling when a phone source is HDR.
// Re-encoding is required for accurate cuts; this is not a lossless operation.
type trimVideoEncoding struct {
	Codec          string `json:"codec,omitempty"`
	PixelFormat    string `json:"pixel_format,omitempty"`
	ColorRange     string `json:"color_range,omitempty"`
	ColorSpace     string `json:"color_space,omitempty"`
	ColorTransfer  string `json:"color_transfer,omitempty"`
	ColorPrimaries string `json:"color_primaries,omitempty"`
	DynamicHDR     string `json:"dynamic_hdr,omitempty"`
}

func prepareTrimParams(db *sql.DB, project, op string, sources []string, raw json.RawMessage) json.RawMessage {
	if op != "trim" || db == nil || len(sources) != 1 {
		return raw
	}
	var params map[string]any
	if json.Unmarshal(raw, &params) != nil || params == nil {
		return raw
	}
	// Never trust private runtime fields supplied by a caller.
	delete(params, "_trim_source_video")
	if row, err := getMedia(db, project, sources[0]); err == nil {
		var probe struct {
			Streams []struct {
				Type           string `json:"codec_type"`
				Codec          string `json:"codec_name"`
				PixelFormat    string `json:"pix_fmt"`
				ColorRange     string `json:"color_range"`
				ColorSpace     string `json:"color_space"`
				ColorTransfer  string `json:"color_transfer"`
				ColorPrimaries string `json:"color_primaries"`
				SideData       []struct {
					Type string `json:"side_data_type"`
				} `json:"side_data_list"`
			} `json:"streams"`
		}
		if json.Unmarshal(row.RawProbe, &probe) == nil {
			for _, stream := range probe.Streams {
				if stream.Type == "video" {
					encoding := trimVideoEncoding{Codec: stream.Codec, PixelFormat: stream.PixelFormat, ColorRange: stream.ColorRange, ColorSpace: stream.ColorSpace, ColorTransfer: stream.ColorTransfer, ColorPrimaries: stream.ColorPrimaries}
					for _, data := range stream.SideData {
						if data.Type == "DOVI configuration record" {
							encoding.DynamicHDR = "dolby_vision"
						}
					}
					params["_trim_source_video"] = encoding
					break
				}
			}
		}
	}
	params["trim_diagnostics"] = map[string]any{"algorithm_version": trimAlgorithmVersion, "mode": "accurate", "interval": "start_inclusive_end_exclusive", "video_origin": "first_retained_frame", "audio_origin": "requested_start", "reencoded": true}
	if encoding, ok := params["_trim_source_video"].(trimVideoEncoding); ok && encoding.DynamicHDR != "" {
		params["trim_diagnostics"].(map[string]any)["limitations"] = []string{"Dolby Vision dynamic metadata is not retained by re-encoding; source bit depth and base-layer color signaling are retained."}
	}
	out, err := json.Marshal(params)
	if err != nil {
		return raw
	}
	return out
}

func trimEncodingArgs(name string, video trimVideoEncoding) ([]string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if isAudioExt(ext) {
		codec := map[string]string{".m4a": "aac", ".mp3": "libmp3lame", ".wav": "pcm_s24le", ".opus": "libopus", ".flac": "flac"}[ext]
		if codec == "" {
			return nil, invalidOutputFormat("trim: unsupported audio output %s", ext)
		}
		return []string{"-vn", "-c:a", codec}, nil
	}
	args := []string{}
	switch ext {
	case ".mp4", ".mov", ".mkv":
		codec := "libx264"
		if video.Codec == "hevc" || strings.Contains(video.PixelFormat, "10") || strings.Contains(video.PixelFormat, "12") || video.ColorTransfer == "arib-std-b67" || video.ColorTransfer == "smpte2084" {
			codec = "libx265"
		}
		args = append(args, "-c:v", codec, "-preset", "fast", "-crf", "18", "-c:a", "aac", "-b:a", "192k")
		if codec == "libx265" {
			// Match the worker's small thread budget; x265 otherwise creates
			// host-wide pools independent of FFmpeg's -threads setting.
			args = append(args, "-x265-params", "pools=2:frame-threads=2:log-level=error")
			if ext != ".mkv" {
				args = append(args, "-tag:v", "hvc1")
			}
		}
		if ext != ".mkv" {
			args = append(args, "-movflags", "+faststart")
		}
	case ".webm":
		args = append(args, "-c:v", "libvpx-vp9", "-crf", "18", "-b:v", "0", "-c:a", "libopus")
	default:
		return nil, invalidOutputFormat("trim: unsupported video output %s", ext)
	}
	if video.PixelFormat != "" {
		args = append(args, "-pix_fmt", video.PixelFormat)
	}
	for _, flag := range []struct{ name, value string }{
		{"-color_range", video.ColorRange}, {"-colorspace", video.ColorSpace}, {"-color_trc", video.ColorTransfer}, {"-color_primaries", video.ColorPrimaries},
	} {
		if flag.value != "" && flag.value != "unknown" && flag.value != "unspecified" {
			args = append(args, flag.name, flag.value)
		}
	}
	return args, nil
}

func trimValidationError(reason string) error {
	return fmt.Errorf("trim_validation_failed: %s", reason)
}
