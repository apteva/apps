package main

// JSON-response parsing + audience-rating persistence for the
// v0.13.0 describer rework. The prompt asks the LLM to return one
// JSON object with description + audience_rating + audience_reasoning;
// in practice LLMs occasionally wrap the JSON in ```json fences,
// emit a brief preamble ("Here is the JSON:"), or refuse outright.
// parseDescribeJSON tolerates the first two; refusal handling is
// in the describer call site.

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// parsedDescribe is the lifted shape of the LLM's response. Fields
// are best-effort — any missing or invalid value falls back to the
// zero value (empty string for audience_rating, which the caller
// treats as "leave column at unrated").
type parsedDescribe struct {
	Description       string
	AudienceRating    string // general | mature | adult | ""
	AudienceReasoning string
}

// parseDescribeJSON extracts the structured fields from the LLM
// response. Three strategies tried in order:
//
//  1. Raw JSON unmarshal — happy path, model followed instructions.
//  2. Strip ```json fences (markdown code blocks) and retry.
//  3. Regex-extract the first balanced {...} block and try.
//
// If all three fail, we treat the entire raw response as a free-text
// description (back-compat with the pre-v0.13.0 plain-text prompt)
// and leave audience_rating empty. The describer call site checks
// strings.TrimSpace(Description) and treats empty as a refusal.
func parseDescribeJSON(raw string) parsedDescribe {
	// Strategy 1: direct unmarshal.
	if p, ok := tryParseDescribeJSON(raw); ok {
		return p
	}
	// Strategy 2: strip ```json...``` or ``` fences.
	if stripped := stripCodeFence(raw); stripped != raw {
		if p, ok := tryParseDescribeJSON(stripped); ok {
			return p
		}
	}
	// Strategy 3: extract first {...} block.
	if m := firstJSONObject(raw); m != "" {
		if p, ok := tryParseDescribeJSON(m); ok {
			return p
		}
	}
	// Last-resort fallback: treat the whole thing as description.
	// Audience stays empty → caller leaves the row at 'unrated' and
	// the next sweep re-tries.
	return parsedDescribe{Description: strings.TrimSpace(raw)}
}

// tryParseDescribeJSON does the actual json.Unmarshal + field
// validation. Returns (parsed, true) on success, (zero, false) on
// any error.
func tryParseDescribeJSON(s string) (parsedDescribe, bool) {
	var raw struct {
		Description       string `json:"description"`
		AudienceRating    string `json:"audience_rating"`
		AudienceReasoning string `json:"audience_reasoning"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &raw); err != nil {
		return parsedDescribe{}, false
	}
	if strings.TrimSpace(raw.Description) == "" {
		// JSON parsed but no description — counts as a failure.
		// Caller's refusal-detection still gets a shot via the raw
		// content path.
		return parsedDescribe{}, false
	}
	return parsedDescribe{
		Description:       strings.TrimSpace(raw.Description),
		AudienceRating:    canonicaliseAudienceRating(raw.AudienceRating),
		AudienceReasoning: strings.TrimSpace(raw.AudienceReasoning),
	}, true
}

// stripCodeFence removes ```json ... ``` or ``` ... ``` wrapping,
// keeping only the inner text. Tolerates a leading "Here is the
// JSON:" preamble before the fence — LLMs occasionally explain
// what they're about to output despite the prompt asking them not to.
func stripCodeFence(s string) string {
	// Find the first ```. Anything before is preamble we discard.
	open := strings.Index(s, "```")
	if open < 0 {
		return s
	}
	// Skip past ``` and an optional language tag (`json`).
	tail := s[open+3:]
	if newline := strings.IndexAny(tail, "\r\n"); newline >= 0 {
		tail = tail[newline+1:]
	}
	// Find closing ```.
	close := strings.Index(tail, "```")
	if close < 0 {
		// No close fence — return what we have (json.Unmarshal will
		// fail and we'll fall through to the next strategy).
		return tail
	}
	return tail[:close]
}

// firstJSONObject extracts the first balanced {...} block from a
// string by counting braces. Returns "" if no balanced block found.
// Used as the last-resort extraction strategy when ``` fence
// detection fails (e.g. the model wrapped in single backticks or
// used a custom delimiter).
func firstJSONObject(s string) string {
	depth := 0
	start := -1
	for i, r := range s {
		switch r {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// canonicaliseAudienceRating maps the LLM's freeform value into one
// of the four allowed states. Anything unrecognised becomes ""
// (caller leaves the column at 'unrated'). Case-insensitive +
// tolerates common alias values (the rubric in the prompt uses
// general/mature/adult but LLMs sometimes default to ESRB-style
// 'everyone' / 'teen' / etc).
func canonicaliseAudienceRating(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "general", "everyone", "g":
		return "general"
	case "mature", "teen", "pg-13", "pg13", "13+", "ma":
		return "mature"
	case "adult", "18+", "explicit", "nsfw", "r", "nc-17":
		return "adult"
	case "unrated", "":
		return ""
	}
	return ""
}

// looksLikeRefusal is a heuristic for "the model declined to process
// the content." LLM safety tuning sometimes causes refusal-shaped
// responses ("I can't describe…", "I'm unable to analyze…") even
// for legitimate audience-rating requests. We detect these and
// default the rating to 'adult' (most conservative) so downstream
// filters don't accidentally treat refused content as
// general-audience-safe.
var refusalPhrases = []string{
	"i can't describe",
	"i cannot describe",
	"i can't analyze",
	"i cannot analyze",
	"i'm unable to",
	"i am unable to",
	"i won't",
	"i will not",
	"i don't feel comfortable",
	"i'm not able to",
	"unable to help with this",
	"can't help with that",
	"i'm sorry, but",
	"i apologize, but",
}

func looksLikeRefusal(s string) bool {
	low := strings.ToLower(s)
	for _, phrase := range refusalPhrases {
		if strings.Contains(low, phrase) {
			return true
		}
	}
	return false
}

// setAudienceRating persists the LLM's audience verdict to the
// media row. Idempotent + cheap; safe to call from every describer
// pass. rating must be one of {general, mature, adult, unrated};
// any other value is rejected silently to keep bad LLM output from
// polluting the column.
func setAudienceRating(db *sql.DB, projectID, fileID, rating, reasoning string) error {
	switch rating {
	case "general", "mature", "adult", "unrated":
		// valid
	default:
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`UPDATE media
		    SET audience_rating = ?, audience_reasoning = ?, audience_updated_at = ?
		  WHERE project_id = ? AND file_id = ?`,
		rating, reasoning, now, projectID, fileID,
	)
	return err
}
