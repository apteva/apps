package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Composition derivation — shared between templates and gigs. The
// composition (an ordered list of instruction bodies) is the source
// of truth; result_schema / media_manifest / checklist / variables
// are all derived from it.

// Known instruction kinds. Grouped for clarity but stored as plain
// strings in the DB (instructions.kind).
const (
	// Read.
	kindText     = "text"
	kindAudio    = "audio"
	kindVideo    = "video"
	kindImage    = "image"
	kindDocument = "document"
	kindLink     = "link"
	kindScript   = "script"
	kindWarning  = "warning"
	kindExample  = "example"
	// Do.
	kindChecklistItem = "checklist_item"
	kindConfirmation  = "confirmation"
	kindTimerHint     = "timer_hint"
	// Input.
	kindInputShortText      = "input_short_text"
	kindInputLongText       = "input_long_text"
	kindInputNumber         = "input_number"
	kindInputDate           = "input_date"
	kindInputChoice         = "input_choice"
	kindInputMultiChoice    = "input_multi_choice"
	kindInputRating         = "input_rating"
	kindInputYesNo          = "input_yes_no"
	kindInputPhoto          = "input_photo"
	kindInputAudioRecording = "input_audio_recording"
	kindInputVideoRecording = "input_video_recording"
	kindInputFile           = "input_file"
	kindInputSignature      = "input_signature"
	kindInputLocation       = "input_location"
)

var knownKinds = map[string]bool{
	kindText: true, kindAudio: true, kindVideo: true, kindImage: true,
	kindDocument: true, kindLink: true, kindScript: true, kindWarning: true,
	kindExample: true, kindChecklistItem: true, kindConfirmation: true,
	kindTimerHint: true, kindInputShortText: true, kindInputLongText: true,
	kindInputNumber: true, kindInputDate: true, kindInputChoice: true,
	kindInputMultiChoice: true, kindInputRating: true, kindInputYesNo: true,
	kindInputPhoto: true, kindInputAudioRecording: true, kindInputVideoRecording: true,
	kindInputFile: true, kindInputSignature: true, kindInputLocation: true,
}

func isInputKind(kind string) bool {
	return strings.HasPrefix(kind, "input_") || kind == kindChecklistItem || kind == kindConfirmation
}

func isMediaKind(kind string) bool {
	switch kind {
	case kindAudio, kindVideo, kindImage, kindDocument:
		return true
	}
	return false
}

// ─── Per-kind body validation + derivation ──────────────────────────

// validateBody enforces the minimum shape required by kind. We're
// lenient on extras — operators can attach freeform metadata.
func validateBody(kind string, body map[string]any) error {
	if !knownKinds[kind] {
		return fmt.Errorf("unknown kind %q", kind)
	}
	req := func(field string) error {
		if _, ok := body[field]; !ok {
			return fmt.Errorf("kind %q requires body.%s", kind, field)
		}
		return nil
	}
	switch kind {
	case kindText:
		return req("markdown")
	case kindAudio, kindVideo, kindImage, kindDocument:
		if _, ok := body["storage_file_id"]; !ok {
			return fmt.Errorf("kind %q requires body.storage_file_id", kind)
		}
	case kindLink:
		return req("url")
	case kindScript:
		return req("lines")
	case kindWarning:
		return req("text")
	case kindExample:
		// Either text or file flavour is fine.
		if body["good_text"] == nil && body["bad_text"] == nil &&
			body["good_file_id"] == nil && body["bad_file_id"] == nil {
			return errors.New("kind \"example\" requires at least one of good_text/bad_text/good_file_id/bad_file_id")
		}
	case kindChecklistItem, kindConfirmation:
		return req("text")
	case kindTimerHint:
		return req("seconds_suggested")
	default:
		if isInputKind(kind) {
			return req("label")
		}
	}
	return nil
}

// deriveResultField returns the JSON Schema fragment this
// instruction contributes to a gig's result. Non-result kinds
// (read-only instructions) return nil.
//
// The returned schema is what gets stuffed into the gig's
// derived_result_schema.properties[result_key].
func deriveResultField(kind string, body map[string]any) map[string]any {
	switch kind {
	case kindChecklistItem, kindConfirmation, kindInputYesNo:
		return map[string]any{"type": "boolean"}
	case kindInputShortText, kindInputLongText:
		f := map[string]any{"type": "string"}
		if m := intFromAny(body["max"]); m > 0 {
			f["maxLength"] = m
		}
		return f
	case kindInputNumber:
		f := map[string]any{"type": "number"}
		if v, ok := body["min"]; ok {
			f["minimum"] = v
		}
		if v, ok := body["max"]; ok {
			f["maximum"] = v
		}
		return f
	case kindInputDate:
		return map[string]any{"type": "string", "format": "date"}
	case kindInputChoice:
		opts := body["options"]
		return map[string]any{
			"type": "string",
			"enum": choiceEnumValues(opts),
		}
	case kindInputMultiChoice:
		opts := body["options"]
		return map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "string",
				"enum": choiceEnumValues(opts),
			},
		}
	case kindInputRating:
		scale := intFromAny(body["scale"])
		if scale <= 0 {
			scale = 5
		}
		return map[string]any{"type": "integer", "minimum": 1, "maximum": scale}
	case kindInputPhoto, kindInputAudioRecording, kindInputVideoRecording, kindInputSignature:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"storage_file_id": map[string]any{"type": "integer"},
			},
			"required": []string{"storage_file_id"},
		}
	case kindInputFile:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"storage_file_id": map[string]any{"type": "integer"},
				"filename":        map[string]any{"type": "string"},
				"mime":            map[string]any{"type": "string"},
			},
			"required": []string{"storage_file_id"},
		}
	case kindInputLocation:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lat":         map[string]any{"type": "number"},
				"lng":         map[string]any{"type": "number"},
				"accuracy_m":  map[string]any{"type": "number"},
			},
			"required": []string{"lat", "lng"},
		}
	}
	return nil
}

// deriveDeclaredVariables scans the body for `{{var}}` references in
// the text-shaped fields. Each kind exposes a different surface for
// interpolation — text uses .markdown, link uses .label, script
// scans .lines, etc.
func deriveDeclaredVariables(kind string, body map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, v := range declaredVariables(s) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	switch kind {
	case kindText:
		add(strOf(body["markdown"]))
	case kindLink:
		add(strOf(body["label"]))
		add(strOf(body["url"]))
	case kindScript:
		if lines, ok := body["lines"].([]any); ok {
			for _, l := range lines {
				add(strOf(l))
			}
		}
	case kindWarning, kindChecklistItem, kindConfirmation:
		add(strOf(body["text"]))
	case kindExample:
		add(strOf(body["good_text"]))
		add(strOf(body["bad_text"]))
	default:
		if isInputKind(kind) {
			add(strOf(body["label"]))
		}
	}
	return out
}

// defaultResultKey returns the keying suggestion for input/do kinds.
// Used when a template/composition doesn't override result_key.
func defaultResultKey(kind, slug string) string {
	if !isInputKind(kind) && kind != kindChecklistItem && kind != kindConfirmation {
		return ""
	}
	return slug
}

// ─── Composition rendering ──────────────────────────────────────────

// compositionItem is one row in a composition, post-resolution from
// the DB join. Used by templates and by the gigs dispatcher.
type compositionItem struct {
	SortOrder              int            `json:"sort_order"`
	InstructionID          int64          `json:"instruction_id"`
	InstructionVersionID   int64          `json:"instruction_version_id"`
	Kind                   string         `json:"kind"`
	Body                   map[string]any `json:"body"`              // version body_json, post-overrides
	DeclaredVariables      []string       `json:"declared_variables"`
	ResultKey              string         `json:"result_key,omitempty"`
}

// derivedComposition is what an agent sees when reading a template
// or pre-dispatch preview. Frozen onto the gig at dispatch time.
type derivedComposition struct {
	ResultSchema  map[string]any   `json:"result_schema"`
	MediaManifest []map[string]any `json:"media_manifest"`
	Checklist     []map[string]any `json:"checklist"`
	Variables     []map[string]any `json:"variables"`
}

// deriveFromComposition walks the items in sort_order and produces
// the four derived views.
func deriveFromComposition(items []compositionItem) derivedComposition {
	props := map[string]any{}
	required := []string{}
	media := []map[string]any{}
	checklist := []map[string]any{}
	varSeen := map[string]bool{}
	vars := []map[string]any{}

	for _, it := range items {
		// Result schema contribution.
		if field := deriveResultField(it.Kind, it.Body); field != nil {
			key := it.ResultKey
			if key == "" {
				key = defaultResultKey(it.Kind, fmt.Sprintf("instruction_%d", it.InstructionID))
			}
			if key == "" {
				key = fmt.Sprintf("instruction_%d", it.InstructionID)
			}
			props[key] = field
			if isRequired(it.Kind, it.Body) {
				required = append(required, key)
			}
			// Process kinds also contribute to the checklist.
			if it.Kind == kindChecklistItem || it.Kind == kindConfirmation {
				checklist = append(checklist, map[string]any{
					"result_key": key,
					"text":       strOf(it.Body["text"]),
					"required":   isRequired(it.Kind, it.Body),
					"kind":       it.Kind,
				})
			}
		}
		// Media manifest contribution.
		if isMediaKind(it.Kind) {
			entry := map[string]any{
				"sort_order":      it.SortOrder,
				"role":            it.Kind,
				"storage_file_id": it.Body["storage_file_id"],
			}
			if v, ok := it.Body["caption"]; ok {
				entry["label"] = v
			}
			if v, ok := it.Body["poster_file_id"]; ok {
				entry["poster_file_id"] = v
			}
			media = append(media, entry)
		}
		// Variables union.
		for _, v := range it.DeclaredVariables {
			if varSeen[v] {
				continue
			}
			varSeen[v] = true
			vars = append(vars, map[string]any{
				"name":     v,
				"type":     "text",
				"required": true,
			})
		}
	}

	sort.Strings(required)
	return derivedComposition{
		ResultSchema: schemaObject(props, required),
		MediaManifest: media,
		Checklist:     checklist,
		Variables:     vars,
	}
}

// isRequired reads body.required (defaulting to true for input/do
// kinds, false otherwise) so the result_schema knows which keys must
// be present at submission time.
func isRequired(kind string, body map[string]any) bool {
	v, ok := body["required"]
	if !ok {
		// Default: required for input/do kinds (the worker has to
		// fill them); false for everything else.
		return isInputKind(kind)
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true")
	}
	return false
}

// renderCompositionForGig interpolates `vars` into every text field
// of every instruction body. Returns a deep copy — the source items
// are not mutated.
func renderCompositionForGig(items []compositionItem, vars map[string]any) []compositionItem {
	out := make([]compositionItem, len(items))
	for i, it := range items {
		copy := it
		copy.Body = renderBody(it.Kind, it.Body, vars)
		out[i] = copy
	}
	return out
}

// renderBody returns a shallow-cloned body with all text-shaped
// fields interpolated. Storage refs and structural fields pass
// through unchanged.
func renderBody(kind string, body map[string]any, vars map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	interp := func(field string) {
		if s, ok := out[field].(string); ok {
			out[field] = interpolate(s, vars)
		}
	}
	switch kind {
	case kindText:
		interp("markdown")
	case kindLink:
		interp("label")
		interp("url")
	case kindScript:
		if lines, ok := out["lines"].([]any); ok {
			newLines := make([]any, len(lines))
			for i, l := range lines {
				if s, ok := l.(string); ok {
					newLines[i] = interpolate(s, vars)
				} else {
					newLines[i] = l
				}
			}
			out["lines"] = newLines
		}
	case kindWarning, kindChecklistItem, kindConfirmation:
		interp("text")
	case kindExample:
		interp("good_text")
		interp("bad_text")
	default:
		if isInputKind(kind) {
			interp("label")
			interp("placeholder")
		}
	}
	return out
}

// ─── Tiny helpers ───────────────────────────────────────────────────

func choiceEnumValues(opts any) []string {
	arr, ok := opts.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, o := range arr {
		switch v := o.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if s, ok := v["value"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		return intCast(n) // best-effort; uses helpers.go's int parsing path
	}
	return 0
}

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
