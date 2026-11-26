// Package videoframes copies frames from a <video> on the current page at
// exact times. It runs inside the page (seek, draw to canvas, encode), so a
// popup covering the player does not matter and the frame is the video's own
// resolution rather than its on-screen size.
package videoframes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	MaxTimes         = 24
	DefaultMaxFrames = 12
	DefaultMaxEdge   = 1024
	MaxEdgeLimit     = 1920
	DefaultQuality   = 80
	DefaultTimeout   = 20 * time.Second
	MaxTimeout       = 30 * time.Second
	DefaultMaxBytes  = 16 << 20
	MaxSelectorChars = 1000
	// A seek gets at most this long, and never more than the time left.
	seekTimeout = 8 * time.Second
	pollEvery   = 250 * time.Millisecond
)

// Error carries a stable error_code for computer_use failures.
type Error struct {
	Code       string
	Message    string
	Candidates []computer.VideoCandidate
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Normalize fills defaults and rejects out-of-range options. It does not need
// the video, so callers can validate before touching the page.
func Normalize(opts computer.VideoFrameOptions) (computer.VideoFrameOptions, error) {
	if len(opts.Times) > 0 && opts.Every > 0 {
		return opts, fail("invalid_arguments", "pass at_seconds or every_seconds, not both")
	}
	if len(opts.Times) == 0 && opts.Every <= 0 {
		return opts, fail("invalid_arguments", "pass at_seconds (exact times) or every_seconds (interval)")
	}
	if len(opts.Times) > MaxTimes {
		return opts, fail("invalid_arguments", "at_seconds accepts at most %d times", MaxTimes)
	}
	for _, t := range opts.Times {
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return opts, fail("invalid_arguments", "at_seconds values must be finite and >= 0")
		}
	}
	if opts.Every < 0 || math.IsNaN(opts.Every) || math.IsInf(opts.Every, 0) {
		return opts, fail("invalid_arguments", "every_seconds must be > 0")
	}
	if opts.Every > 0 && opts.Every < 0.04 {
		return opts, fail("invalid_arguments", "every_seconds must be at least 0.04")
	}
	if opts.Start < 0 || math.IsNaN(opts.Start) || math.IsInf(opts.Start, 0) {
		return opts, fail("invalid_arguments", "start_seconds must be finite and >= 0")
	}
	if opts.End != nil && (math.IsNaN(*opts.End) || math.IsInf(*opts.End, 0) || *opts.End < opts.Start) {
		return opts, fail("invalid_arguments", "end_seconds must be finite and >= start_seconds")
	}
	if len(opts.Times) > 0 && (opts.Start != 0 || opts.End != nil) {
		return opts, fail("invalid_arguments", "start_seconds and end_seconds apply only to every_seconds")
	}
	if opts.MaxFrames == 0 {
		opts.MaxFrames = DefaultMaxFrames
	}
	if opts.MaxFrames < 1 || opts.MaxFrames > MaxTimes {
		return opts, fail("invalid_arguments", "max_frames must be 1-%d", MaxTimes)
	}
	if opts.MaxEdge == 0 {
		opts.MaxEdge = DefaultMaxEdge
	}
	if opts.MaxEdge < 64 || opts.MaxEdge > MaxEdgeLimit {
		return opts, fail("invalid_arguments", "max_edge must be 64-%d", MaxEdgeLimit)
	}
	opts.Format = strings.ToLower(strings.TrimSpace(opts.Format))
	switch opts.Format {
	case "":
		opts.Format = "jpeg"
	case "jpg":
		opts.Format = "jpeg"
	case "jpeg", "png":
	default:
		return opts, fail("invalid_arguments", "format must be jpeg or png")
	}
	if opts.Quality == 0 {
		opts.Quality = DefaultQuality
	}
	if opts.Quality < 1 || opts.Quality > 100 {
		return opts, fail("invalid_arguments", "quality must be 1-100")
	}
	if opts.MinHeight < 0 || opts.MinHeight > 4320 {
		return opts, fail("invalid_arguments", "min_height must be 0-4320")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Timeout < 500*time.Millisecond || opts.Timeout > MaxTimeout {
		return opts, fail("invalid_arguments", "timeout_ms must be 500-%d", MaxTimeout.Milliseconds())
	}
	if opts.MaxBytes <= 0 || opts.MaxBytes > DefaultMaxBytes {
		opts.MaxBytes = DefaultMaxBytes
	}
	if len(opts.Selector) > MaxSelectorChars {
		return opts, fail("invalid_target", "selector exceeds %d characters", MaxSelectorChars)
	}
	if opts.VideoIndex != nil && (*opts.VideoIndex < 0 || *opts.VideoIndex > 200) {
		return opts, fail("invalid_arguments", "video_index must be 0-200")
	}
	if opts.VideoIndex != nil && strings.TrimSpace(opts.Selector) != "" {
		return opts, fail("invalid_arguments", "pass selector or video_index, not both")
	}
	return opts, nil
}

// Plan turns normalized options into capture times for a video of the given
// duration. Times past the end are skipped; more than MaxFrames truncates.
func Plan(opts computer.VideoFrameOptions, duration float64) (times []float64, skipped []computer.VideoFrameSkip, truncated bool) {
	// The last decodable frame sits just before the reported duration.
	last := math.Max(0, duration-0.05)
	add := func(t float64) bool {
		if t > duration+1e-6 {
			skipped = append(skipped, computer.VideoFrameSkip{RequestedS: round3(t), Reason: "beyond_duration"})
			return true
		}
		if len(times) >= opts.MaxFrames {
			truncated = true
			return false
		}
		times = append(times, round3(math.Min(t, last)))
		return true
	}
	if len(opts.Times) > 0 {
		for _, t := range opts.Times {
			if !add(t) {
				break
			}
		}
		return
	}
	end := duration
	if opts.End != nil && *opts.End < end {
		end = *opts.End
	}
	if opts.Start > duration+1e-6 {
		skipped = append(skipped, computer.VideoFrameSkip{RequestedS: round3(opts.Start), Reason: "beyond_duration"})
		return
	}
	for i := 0; ; i++ {
		t := opts.Start + float64(i)*opts.Every
		if t > end+1e-6 {
			break
		}
		if !add(t) {
			break
		}
	}
	return
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

type listResult struct {
	Code          string                    `json:"code"`
	Message       string                    `json:"message"`
	Candidates    []computer.VideoCandidate `json:"candidates"`
	SelectorIndex int                       `json:"selector_index"`
}

type prepareResult struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type frameResult struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Actual  float64 `json:"actual"`
	VW      int     `json:"vw"`
	VH      int     `json:"vh"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	MIME    string  `json:"mime"`
	Data    string  `json:"data"`
}

type restoreResult struct {
	Restored bool   `json:"restored"`
	Reason   string `json:"reason"`
}

// Run selects the video, copies the planned frames and restores playback.
func Run(ctx context.Context, opts computer.VideoFrameOptions) (result computer.VideoFrameResult, err error) {
	opts, err = Normalize(opts)
	if err != nil {
		return result, err
	}
	deadline := time.Now().Add(opts.Timeout)
	chosen, err := selectVideo(ctx, opts, deadline)
	if err != nil {
		return result, err
	}
	result.Video.VideoCandidate = *chosen
	result.Video.SelectorUsed = strings.TrimSpace(opts.Selector)
	restore := func() {
		var r restoreResult
		// Restoring is best effort but must not be skipped when capture ran out
		// of time, so it gets its own short budget.
		rctx, cancel := cdputil.Context(ctx, 4*time.Second)
		defer cancel()
		if err := evaluate(rctx, restoreFunction, nil, &r); err != nil {
			result.Video.Restored, result.Video.RestoreReason = false, err.Error()
			return
		}
		result.Video.Restored, result.Video.RestoreReason = r.Restored, r.Reason
	}
	defer restore()

	times, skipped, truncated := Plan(opts, chosen.Duration)
	result.Skipped, result.Truncated = skipped, truncated
	mime := "image/" + opts.Format
	total := 0
	var low []int
	capture := func(t float64) (*computer.VideoFrame, *computer.VideoFrameSkip, error) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, &computer.VideoFrameSkip{RequestedS: t, Reason: "timeout"}, nil
		}
		wait := min(seekTimeout, remaining)
		fctx, cancel := cdputil.Context(ctx, wait+2*time.Second)
		defer cancel()
		var fr frameResult
		args := map[string]any{"t": t, "maxEdge": opts.MaxEdge, "mime": mime, "quality": float64(opts.Quality) / 100, "seekTimeoutMs": wait.Milliseconds()}
		if err := evaluate(fctx, captureFunction, args, &fr); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, &computer.VideoFrameSkip{RequestedS: t, Reason: "seek_timeout", Message: err.Error()}, nil
		}
		switch fr.Code {
		case "":
		case "frame_capture_blocked", "video_detached":
			return nil, nil, fail(fr.Code, "%s", firstNonEmpty(fr.Message, "the video can no longer be read"))
		default:
			return nil, &computer.VideoFrameSkip{RequestedS: t, Reason: fr.Code, Message: fr.Message}, nil
		}
		data, err := base64.StdEncoding.DecodeString(fr.Data)
		if err != nil || len(data) == 0 {
			return nil, &computer.VideoFrameSkip{RequestedS: t, Reason: "encode_failed"}, nil
		}
		sum := sha256.Sum256(data)
		return &computer.VideoFrame{
			RequestedS: t, ActualS: round3(fr.Actual), Width: fr.W, Height: fr.H,
			SourceWidth: fr.VW, SourceHeight: fr.VH, MIMEType: firstNonEmpty(fr.MIME, mime),
			SHA256: hex.EncodeToString(sum[:]), Data: data,
			LowResolution: opts.MinHeight > 0 && fr.VH < opts.MinHeight,
		}, nil, nil
	}
	for _, t := range times {
		frame, skip, err := capture(t)
		if err != nil {
			return result, err
		}
		if skip != nil {
			result.Skipped = append(result.Skipped, *skip)
			continue
		}
		if total+len(frame.Data) > opts.MaxBytes {
			result.Truncated = true
			break
		}
		total += len(frame.Data)
		frame.Index = len(result.Frames)
		if frame.LowResolution {
			low = append(low, frame.Index)
		}
		result.Frames = append(result.Frames, *frame)
	}
	// Streaming players often start at a low rendition; later frames load a
	// better one. One retry each, after the rest, usually picks it up.
	for _, i := range low {
		if time.Until(deadline) <= 0 {
			break
		}
		old := result.Frames[i]
		frame, skip, err := capture(old.RequestedS)
		if err != nil || skip != nil || frame.SourceHeight <= old.SourceHeight {
			continue
		}
		if total-len(old.Data)+len(frame.Data) > opts.MaxBytes {
			continue
		}
		total += len(frame.Data) - len(old.Data)
		frame.Index, frame.Retried = i, true
		result.Frames[i] = *frame
	}
	markDuplicates(result.Frames)
	if len(result.Frames) == 0 && len(times) > 0 {
		if len(result.Skipped) > 0 && result.Skipped[len(result.Skipped)-1].Reason != "beyond_duration" {
			last := result.Skipped[len(result.Skipped)-1]
			return result, fail(firstNonEmpty(last.Reason, "seek_timeout"), "no frame could be copied: %s", firstNonEmpty(last.Message, last.Reason))
		}
	}
	return result, nil
}

func markDuplicates(frames []computer.VideoFrame) {
	seen := map[string]int{}
	for i := range frames {
		if first, ok := seen[frames[i].SHA256]; ok {
			idx := first
			frames[i].DuplicateOf = &idx
			continue
		}
		seen[frames[i].SHA256] = frames[i].Index
	}
}

func qualified(c computer.VideoCandidate) bool {
	return c.ReadyState >= 1 && (c.Duration > 0 || c.Live)
}

// selectVideo lists page videos until the requested (or best) one has its
// metadata, then pauses and mutes it after saving its playback state.
func selectVideo(ctx context.Context, opts computer.VideoFrameOptions, deadline time.Time) (*computer.VideoCandidate, error) {
	selector := strings.TrimSpace(opts.Selector)
	for {
		lctx, cancel := cdputil.Context(ctx, 5*time.Second)
		var list listResult
		err := evaluate(lctx, listFunction, map[string]any{"selector": selector}, &list)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if list.Code != "" {
			return nil, &Error{Code: list.Code, Message: firstNonEmpty(list.Message, list.Code), Candidates: list.Candidates}
		}
		chosen, err := choose(list, opts)
		if err != nil {
			return nil, err
		}
		if chosen != nil {
			c := list.Candidates[*chosen]
			if c.DRM {
				return nil, fail("drm_protected", "the video is encrypted (DRM); its frames cannot be copied")
			}
			if c.Live {
				return nil, fail("live_stream_unsupported", "the video is a live stream with no fixed length")
			}
			pctx, cancel := cdputil.Context(ctx, 5*time.Second)
			var prep prepareResult
			err := evaluate(pctx, prepareFunction, map[string]any{"index": c.Index}, &prep)
			cancel()
			if err != nil {
				return nil, err
			}
			if prep.Code != "" {
				return nil, fail(prep.Code, "%s", firstNonEmpty(prep.Message, prep.Code))
			}
			return &c, nil
		}
		if time.Now().Add(pollEvery).After(deadline) {
			return nil, &Error{Code: "video_not_ready", Message: "no video loaded its metadata within the time budget", Candidates: list.Candidates}
		}
		if err := cdputil.Sleep(ctx, pollEvery); err != nil {
			return nil, err
		}
	}
}

// choose returns the candidate position to use, nil to keep waiting for
// metadata, or a terminal error.
func choose(list listResult, opts computer.VideoFrameOptions) (*int, error) {
	cands := list.Candidates
	if len(cands) == 0 {
		return nil, fail("video_not_found", "the page has no <video> element (cross-origin iframes are not searched)")
	}
	pick := -1
	switch {
	case strings.TrimSpace(opts.Selector) != "":
		pick = list.SelectorIndex
	case opts.VideoIndex != nil:
		if *opts.VideoIndex >= len(cands) {
			return nil, &Error{Code: "invalid_target", Message: fmt.Sprintf("video_index %d is out of range; the page has %d videos", *opts.VideoIndex, len(cands)), Candidates: cands}
		}
		pick = *opts.VideoIndex
	}
	if pick >= 0 {
		if !qualified(cands[pick]) {
			return nil, nil
		}
		return &pick, nil
	}
	var ready []computer.VideoCandidate
	for _, c := range cands {
		if qualified(c) {
			ready = append(ready, c)
		}
	}
	if len(ready) == 0 {
		return nil, nil
	}
	best, ambiguous := Rank(ready)
	if ambiguous {
		return nil, &Error{Code: "ambiguous_video", Message: "several videos are equally plausible; pass video_index (or selector) to pick one", Candidates: cands}
	}
	idx := ready[best].Index
	return &idx, nil
}

func evaluate(ctx context.Context, fn string, args any, out any) error {
	encoded := "undefined"
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		value, exception, err := cdpruntime.Evaluate("(" + fn + ")(" + encoded + ")").
			WithAwaitPromise(true).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			msg := exception.Text
			if exception.Exception != nil && exception.Exception.Description != "" {
				msg = exception.Exception.Description
			}
			return errors.New(msg)
		}
		if value == nil || len(value.Value) == 0 {
			return errors.New("page script returned nothing")
		}
		return json.Unmarshal(value.Value, out)
	}))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Rank orders candidates the way the page script does; exported for tests.
// It returns the best index and whether the top two are indistinguishable.
func Rank(cands []computer.VideoCandidate) (best int, ambiguous bool) {
	if len(cands) == 0 {
		return -1, false
	}
	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	key := func(c computer.VideoCandidate) [4]int {
		playing := 0
		if c.Playing {
			playing = 1
		}
		return [4]int{playing, c.VisibleArea, c.OnScreenW * c.OnScreenH, c.VideoWidth * c.VideoHeight}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ka, kb := key(cands[order[a]]), key(cands[order[b]])
		for i := range ka {
			if ka[i] != kb[i] {
				return ka[i] > kb[i]
			}
		}
		return false
	})
	if len(order) > 1 && key(cands[order[0]]) == key(cands[order[1]]) {
		return order[0], true
	}
	return order[0], false
}
