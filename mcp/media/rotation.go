package main

// Rotation handling for renders.
//
// Sources with a displaymatrix rotation tag (most phone-recorded
// videos — iPhone records landscape internally, tags rotation=90 for
// portrait display) need explicit transpose at render time. The
// indexer already stores DISPLAY-space Width/Height (post-rotation),
// but ffmpeg's filter chain operates on the codec frame unless we
// either:
//   (a) rely on auto-rotation — fragile across `-ss` placement,
//       ffmpeg build options, and demuxer/decoder coupling, or
//   (b) take ownership: pass -noautorotate, prepend the right
//       transpose filter ourselves.
//
// We do (b). This file is the small piece of glue.
//
// What "rotation=N" means here: ffprobe extracts a signed degrees
// value from the displaymatrix side_data. The indexer normalises it
// to one of {0, 90, 180, 270} (positive convention; -90 becomes
// 270). The interpretation: the codec frame should be rotated by
// +N degrees (counterclockwise in standard math convention) to
// produce the displayed image.
//
// transpose filter cheatsheet:
//   transpose=1 → 90° clockwise   (i.e. rotate by -90°)
//   transpose=2 → 90° counterclockwise (i.e. rotate by +90°)
//
// So rotation=90 (= +90° CCW) → transpose=2.
//    rotation=270 (= -90° / 270° CCW) → transpose=1.
//    rotation=180 → transpose=1,transpose=1 (or hflip+vflip, same result).

import (
	"database/sql"
	"strings"
)

// transposeFilterFor returns the ffmpeg `-vf` fragment that bakes
// the source's rotation into the output frame. Empty string when
// rotation is 0 — caller should skip the filter entirely rather
// than inserting an empty token.
func transposeFilterFor(rotation int) string {
	switch rotation {
	case 90:
		return "transpose=2"
	case 180:
		return "transpose=1,transpose=1"
	case 270:
		return "transpose=1"
	default:
		return ""
	}
}

// lookupSourceRotation returns the rotation degrees of the FIRST
// source file in the renders.source_file_ids list, or 0 when the
// row hasn't been probed yet / the row is missing / concat (where
// inputs may have heterogeneous orientations and the planner uses
// the demuxer concat protocol that doesn't accept per-input
// filters).
//
// Project-scoped lookup matches how the renderer resolves source
// bytes; cross-project source IDs aren't a thing in this app.
func lookupSourceRotation(db *sql.DB, projectID string, sourceFileIDs []string) int {
	if len(sourceFileIDs) == 0 || projectID == "" {
		return 0
	}
	// Concat takes multiple inputs at different orientations; the
	// concat-demuxer protocol doesn't accept a per-input filter, so
	// applying transpose to "the first source" would silently
	// misrender the others. Skip — operator's responsibility to
	// pre-normalise concat inputs.
	if len(sourceFileIDs) > 1 {
		return 0
	}
	var r int
	if err := db.QueryRow(
		`SELECT COALESCE(rotation, 0) FROM media WHERE project_id = ? AND file_id = ?`,
		projectID, sourceFileIDs[0],
	).Scan(&r); err != nil {
		return 0
	}
	return r
}

// applyRotation mutates a built ffmpeg arg list to bake the source's
// rotation into the output. Two changes:
//
//   1. Insert "-noautorotate" before each "-i" so ffmpeg doesn't
//      apply rotation on top of what we're about to do. Without
//      this we'd risk double-rotation, depending on the build's
//      autorotate default.
//
//   2. Prepend the transpose filter to any existing "-vf" chain.
//      Composes via ","; ffmpeg processes filters left-to-right so
//      the transpose runs first, then crop/scale see a frame that
//      matches the indexer's stored Width/Height.
//
// No-op when rotation == 0. Safe to call on every plan.
//
// For ops that produce video output but DON'T have a -vf chain
// (e.g. plain trim with -c:v copy), we don't inject one — the
// codec stream stays unchanged and inherits the source's rotation
// tag, which preserves correct display in any compliant player. If
// an op needs baked-in rotation but doesn't have a -vf chain
// today, switch it to a real re-encode and add this call's
// downstream branch will handle it automatically.
func applyRotation(args []string, rotation int) []string {
	if rotation == 0 {
		return args
	}
	transpose := transposeFilterFor(rotation)
	if transpose == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-i":
			// Inject -noautorotate before -i, then copy "-i" + URL.
			out = append(out, "-noautorotate", "-i")
			if i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			}
		case args[i] == "-vf" && i+1 < len(args):
			// Prepend transpose to the existing -vf chain.
			out = append(out, "-vf", transpose+","+args[i+1])
			i++
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// hasVF returns true if the args list contains a "-vf" flag.
// Useful for the audio_extract case where we shouldn't be adding a
// video filter at all (it has -vn instead).
func hasVF(args []string) bool {
	for _, a := range args {
		if a == "-vf" {
			return true
		}
	}
	return false
}

// shouldRotate decides whether to apply rotation for a given op.
// We only rotate ops that emit video AND have a -vf chain already
// — anything else either has no video (audio_extract), no filter
// chain (trim with stream-copy), or is a multi-input op (concat).
func shouldRotate(op string, args []string) bool {
	if op == "concat" || op == "audio_extract" {
		return false
	}
	return hasVF(args)
}

// canonicalRotation maps any int to {0, 90, 180, 270} or 0 on
// malformed input. Defends against future schema drift where the
// rotation column might accept arbitrary integers.
func canonicalRotation(r int) int {
	r = ((r % 360) + 360) % 360
	switch r {
	case 90, 180, 270:
		return r
	}
	return 0
}

// transposeCommaJoin is a tiny convenience used by tests + the
// debug log line: turns a slice of -vf filters into a single string
// the way ffmpeg expects.
func transposeCommaJoin(fs []string) string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		if strings.TrimSpace(f) == "" {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, ",")
}
