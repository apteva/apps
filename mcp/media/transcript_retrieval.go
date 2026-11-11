package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type transcriptReadOptions struct {
	View         string `json:"view"`
	Start        *int64 `json:"start_ms,omitempty"`
	End          *int64 `json:"end_ms,omitempty"`
	Channel      *int64 `json:"channel,omitempty"`
	Speaker      *int64 `json:"speaker,omitempty"`
	Query        string `json:"query,omitempty"`
	Occurrence   int64  `json:"occurrence,omitempty"`
	Timebase     string `json:"timebase"`
	IncludeWords bool   `json:"include_words"`
	Limit        int    `json:"-"`
	Cursor       string `json:"-"`
}

type transcriptCursor struct {
	Revision string `json:"revision"`
	Filters  string `json:"filters"`
	Offset   int    `json:"offset"`
}

type indexedTranscriptWord struct {
	TranscriptWord
	Index int `json:"word_index"`
}
type transcriptMatch struct {
	Occurrence int                     `json:"occurrence"`
	StartMs    int64                   `json:"start_ms"`
	EndMs      int64                   `json:"end_ms"`
	Channel    int                     `json:"channel"`
	Words      []indexedTranscriptWord `json:"words"`
}

func transcriptReadSchema() map[string]any {
	return schemaObject(map[string]any{
		"file_id":  map[string]any{"type": "string"},
		"view":     map[string]any{"type": "string", "enum": []string{"full", "summary", "text", "segments", "words"}, "description": "Defaults to full for compatibility. Summary returns counts/readiness; segments/words are paginated."},
		"start_ms": map[string]any{"type": "integer", "minimum": 0}, "end_ms": map[string]any{"type": "integer", "minimum": 0},
		"channel": map[string]any{"type": "integer", "minimum": 0}, "speaker": map[string]any{"type": "integer", "minimum": 0},
		"query":         map[string]any{"type": "string", "description": "Exact normalized phrase, including across sentence boundaries, within one channel. Requires view=words; returns matches with occurrence and word indexes."},
		"occurrence":    map[string]any{"type": "integer", "minimum": 1, "description": "One-based phrase occurrence; requires query."},
		"timebase":      map[string]any{"type": "string", "enum": []string{"transcript_audio", "source"}, "description": "Default is the stored timebase. Source projects validated audio timestamps onto the original source timeline."},
		"include_words": map[string]any{"type": "boolean", "description": "Include nested words in the segments view (default false)."},
		"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "description": "Page size for words, matches, or segments; default 200."}, "cursor": map[string]any{"type": "string"},
	}, []string{"file_id"})
}

func transcriptReadArgs(q url.Values) map[string]any {
	args := map[string]any{}
	for k, v := range q {
		if len(v) > 0 {
			args[k] = v[0]
		}
	}
	if v, ok := args["include_words"].(string); ok {
		args["include_words"] = v == "true"
	}
	return args
}

func transcriptInteger(v any, name string) (int64, error) {
	var n float64
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		n = x
	case json.Number:
		parsed, e := strconv.ParseInt(string(x), 10, 64)
		return parsed, e
	case string:
		parsed, e := strconv.ParseInt(x, 10, 64)
		return parsed, e
	default:
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 0 || n > 1e12 {
		return 0, fmt.Errorf("%s must be a nonnegative integer", name)
	}
	return int64(n), nil
}

func parseTranscriptReadOptions(args map[string]any) (transcriptReadOptions, error) {
	o := transcriptReadOptions{View: "full", Limit: 200}
	if v, ok := args["view"].(string); ok && v != "" {
		o.View = v
	}
	switch o.View {
	case "full", "summary", "text", "segments", "words":
	default:
		return o, errors.New("invalid transcript view")
	}
	for _, item := range []struct {
		name string
		dst  **int64
	}{{"start_ms", &o.Start}, {"end_ms", &o.End}, {"channel", &o.Channel}, {"speaker", &o.Speaker}} {
		if v, ok := args[item.name]; ok {
			n, e := transcriptInteger(v, item.name)
			if e != nil || n < 0 {
				return o, fmt.Errorf("invalid %s", item.name)
			}
			*item.dst = &n
		}
	}
	if o.Start != nil && o.End != nil && *o.End <= *o.Start {
		return o, errors.New("end_ms must exceed start_ms")
	}
	if v, ok := args["limit"]; ok {
		n, e := transcriptInteger(v, "limit")
		if e != nil || n < 1 || n > 1000 {
			return o, errors.New("limit must be 1..1000")
		}
		o.Limit = int(n)
	}
	o.Query, _ = args["query"].(string)
	o.Query = strings.TrimSpace(o.Query)
	if len(o.Query) > 1000 || len(wordTokens(o.Query)) > 100 {
		return o, errors.New("query too long")
	}
	if o.Query != "" && (o.View != "words" || len(wordTokens(o.Query)) == 0) {
		return o, errors.New("query requires view=words and at least one word")
	}
	if v, ok := args["occurrence"]; ok {
		n, e := transcriptInteger(v, "occurrence")
		if e != nil || n < 1 || o.Query == "" {
			return o, errors.New("occurrence requires query and must be positive")
		}
		o.Occurrence = n
	}
	o.Cursor, _ = args["cursor"].(string)
	o.Timebase, _ = args["timebase"].(string)
	o.IncludeWords, _ = args["include_words"].(bool)
	if o.Timebase != "" && o.Timebase != "source" && o.Timebase != "transcript_audio" {
		return o, errors.New("invalid transcript timebase")
	}
	if o.View != "segments" && o.View != "words" && (o.Start != nil || o.End != nil || o.Channel != nil || o.Speaker != nil || o.Cursor != "") {
		return o, errors.New("filters and cursor require view=segments or view=words")
	}
	return o, nil
}

func (o transcriptReadOptions) wordMatches(w TranscriptWord) bool {
	if o.Start != nil && w.EndMs <= *o.Start {
		return false
	}
	if o.End != nil && w.StartMs >= *o.End {
		return false
	}
	if o.Channel != nil && int64(transcriptChannel(w)) != *o.Channel {
		return false
	}
	if o.Speaker != nil && (w.Speaker == nil || int64(*w.Speaker) != *o.Speaker) {
		return false
	}
	return true
}

func transcriptPhraseMatches(words []TranscriptWord, o transcriptReadOptions) []transcriptMatch {
	streams := map[int][]indexedTranscriptWord{}
	var ids []int
	for i, w := range words {
		c := transcriptChannel(w)
		if _, ok := streams[c]; !ok {
			ids = append(ids, c)
		}
		streams[c] = append(streams[c], indexedTranscriptWord{w, i})
	}
	needle := wordTokens(o.Query)
	matches := []transcriptMatch{}
	for _, channel := range ids {
		stream := streams[channel]
		for i := 0; i+len(needle) <= len(stream); i++ {
			match := true
			for j, token := range needle {
				tokens := wordTokens(stream[i+j].Text)
				if len(tokens) != 1 || tokens[0] != token || !o.wordMatches(stream[i+j].TranscriptWord) {
					match = false
					break
				}
			}
			if match {
				selected := append([]indexedTranscriptWord(nil), stream[i:i+len(needle)]...)
				matches = append(matches, transcriptMatch{StartMs: selected[0].StartMs, EndMs: selected[len(selected)-1].EndMs, Channel: channel, Words: selected})
			}
		}
	}
	// Stable chronological ordering is shared with the flat words view.
	sortTranscriptMatches(matches)
	for i := range matches {
		matches[i].Occurrence = i + 1
	}
	if o.Occurrence > 0 {
		if o.Occurrence > int64(len(matches)) {
			return []transcriptMatch{}
		}
		return []transcriptMatch{matches[o.Occurrence-1]}
	}
	return matches
}

func transcriptPage(o transcriptReadOptions, revision string, count int) (int, int, string, error) {
	filter, _ := json.Marshal(o)
	sum := sha256.Sum256(filter)
	fingerprint := hex.EncodeToString(sum[:])
	offset := 0
	if o.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(o.Cursor)
		if e != nil || len(b) > 2048 {
			return 0, 0, "", errors.New("invalid transcript cursor")
		}
		var c transcriptCursor
		if json.Unmarshal(b, &c) != nil || c.Offset < 0 {
			return 0, 0, "", errors.New("invalid transcript cursor")
		}
		if c.Revision != revision {
			return 0, 0, "", errors.New("transcript_cursor_stale: restart pagination")
		}
		if c.Filters != fingerprint {
			return 0, 0, "", errors.New("transcript cursor filters changed")
		}
		offset = c.Offset
	}
	if offset > count {
		return 0, 0, "", errors.New("transcript cursor offset out of range")
	}
	end := offset + o.Limit
	if end > count {
		end = count
	}
	next := ""
	if end < count {
		b, _ := json.Marshal(transcriptCursor{revision, fingerprint, end})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return offset, end, next, nil
}

func transcriptReadResponse(ctx *sdk.AppCtx, t *TranscriptRow, args map[string]any) (map[string]any, error) {
	o, err := parseTranscriptReadOptions(args)
	if err != nil {
		return nil, err
	}
	var segments []TranscriptSegment
	if len(t.Segments) > 0 {
		if err = json.Unmarshal(t.Segments, &segments); err != nil {
			return nil, err
		}
	}
	words := transcriptWords(segments)
	granularity := "text"
	if len(segments) > 0 {
		granularity = "segment"
	}
	if len(words) > 0 {
		granularity = "word"
	}
	timing := transcriptTimingOf(t)
	duration := t.DurationMs
	timebase := "source"
	if t.Provider == "deepgram" {
		timebase = "transcript_audio"
	}
	if timing != nil {
		timebase = timing.Timebase
		duration = timing.AudioDurationMs
	}
	timingStatus := "missing"
	if len(words) > 0 {
		timingStatus = "valid"
		if validateTranscriptWords(transcriptStoredWords(segments), duration) != nil {
			timingStatus = "invalid"
		}
	}
	media, mediaErr := getMedia(ctx.AppDB(), t.ProjectID, t.FileID)
	matched := mediaErr == nil && t.SourceSHA256 != "" && t.SourceSHA256 == media.SourceSHA256
	ready := false
	if matched && t.Status == "ok" && len(words) > 0 {
		_, e := transcriptSourceWords(t, media)
		ready = e == nil && (t.Provider != "deepgram" || timing != nil && timing.Validated)
	}
	channels := map[int]bool{}
	for _, w := range words {
		channels[transcriptChannel(w)] = true
	}
	if len(channels) > 1 {
		ready = false
	}
	if o.Timebase != "" && o.Timebase != timebase {
		if o.Timebase != "source" || timing == nil || !timing.Validated || !matched || timing.Scale != 1 || timing.SourceOffsetMs < 0 || timing.AudioFileID <= 0 || timing.AudioSHA256 == "" {
			return nil, errors.New("transcript timestamp mapping unavailable")
		}
		for i := range segments {
			segments[i].StartMs += timing.SourceOffsetMs
			segments[i].EndMs += timing.SourceOffsetMs
			for j := range segments[i].Words {
				segments[i].Words[j].StartMs += timing.SourceOffsetMs
				segments[i].Words[j].EndMs += timing.SourceOffsetMs
			}
		}
		words = transcriptWords(segments)
		timebase = "source"
	}
	b, _ := json.Marshal(t)
	var result map[string]any
	_ = json.Unmarshal(b, &result)
	result["revision"] = transcriptRevision(t)
	result["word_count"] = len(words)
	result["segment_count"] = len(segments)
	result["timing_granularity"] = granularity
	result["word_timing_status"] = timingStatus
	result["source_matches"] = matched
	result["redaction_ready"] = ready
	result["timebase"] = timebase
	if timing != nil {
		result["timing"] = timing
	}
	if o.View == "full" {
		if len(segments) > 0 {
			result["segments"] = segments
		}
		return map[string]any{"found": true, "transcript": result}, nil
	}
	delete(result, "segments")
	delete(result, "diagnostics")
	if o.View != "text" {
		delete(result, "text")
	}
	if o.View == "summary" || o.View == "text" {
		return map[string]any{"found": true, "transcript": result}, nil
	}
	count := 0
	if o.View == "segments" {
		selected := []TranscriptSegment{}
		for _, s := range segments {
			if o.Start != nil && s.EndMs <= *o.Start || o.End != nil && s.StartMs >= *o.End {
				continue
			}
			if o.Channel != nil && (s.Channel == nil && *o.Channel != 0 || s.Channel != nil && int64(*s.Channel) != *o.Channel) {
				continue
			}
			if o.Speaker != nil {
				found := false
				for _, w := range s.Words {
					if w.Speaker != nil && int64(*w.Speaker) == *o.Speaker {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			if !o.IncludeWords {
				s.Words = nil
			}
			selected = append(selected, s)
		}
		count = len(selected)
		start, end, next, e := transcriptPage(o, transcriptRevision(t), count)
		if e != nil {
			return nil, e
		}
		result["segments"] = selected[start:end]
		result["next_cursor"] = next
	} else if o.Query != "" {
		selected := transcriptPhraseMatches(words, o)
		count = len(selected)
		start, end, next, e := transcriptPage(o, transcriptRevision(t), count)
		if e != nil {
			return nil, e
		}
		result["matches"] = selected[start:end]
		result["next_cursor"] = next
	} else {
		selected := []indexedTranscriptWord{}
		for i, w := range words {
			if o.wordMatches(w) {
				selected = append(selected, indexedTranscriptWord{w, i})
			}
		}
		count = len(selected)
		start, end, next, e := transcriptPage(o, transcriptRevision(t), count)
		if e != nil {
			return nil, e
		}
		result["words"] = selected[start:end]
		result["next_cursor"] = next
	}
	start, end, _, e := transcriptPage(o, transcriptRevision(t), count)
	if e != nil {
		return nil, e
	}
	result["matched_count"] = count
	result["returned_count"] = end - start
	result["has_more"] = result["next_cursor"] != ""
	return map[string]any{"found": true, "transcript": result}, nil
}

func sortTranscriptMatches(matches []transcriptMatch) {
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].StartMs == matches[j].StartMs {
			return matches[i].Channel < matches[j].Channel
		}
		return matches[i].StartMs < matches[j].StartMs
	})
}
