package main

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"unicode"

	sdk "github.com/apteva/app-sdk"
)

type voiceRecognitionTurn struct {
	text    string
	final   bool
	blocked bool
}

// Stable transcript IDs revise the original turn, including partial-to-final
// updates. Provider-final transcripts on older Core have no metadata.
func voiceRecognitionTurns(events []sdk.RuntimeTelemetryEvent, threadID string) (assistant, user []voiceRecognitionTurn) {
	ordered := append([]sdk.RuntimeTelemetryEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })
	hasSpeakingState := false
	spokenItems := map[string]bool{}
	for _, event := range ordered {
		if event.ThreadID != threadID {
			continue
		}
		var data struct {
			State       string   `json:"state"`
			ItemID      string   `json:"item_id"`
			BridgeWrite *float64 `json:"provider_audio_to_bridge_write_ms"`
		}
		if json.Unmarshal(event.Data, &data) == nil {
			hasSpeakingState = hasSpeakingState || (event.Type == "realtime.state" && data.State == "speaking")
			if event.Type == "realtime.utterance_timing" && data.BridgeWrite != nil {
				spokenItems[data.ItemID] = true
			}
		}
	}
	indices := map[string]int{}
	assistantSpoke := false
	for _, event := range ordered {
		if event.ThreadID != threadID {
			continue
		}
		var data struct {
			State      string `json:"state"`
			Text       string `json:"text"`
			Transcript struct {
				ID     string `json:"id"`
				ItemID string `json:"item_id"`
				Status string `json:"status"`
			} `json:"transcript"`
		}
		_ = json.Unmarshal(event.Data, &data)
		if event.Type == "realtime.state" && data.State == "speaking" {
			assistantSpoke = true
		}
		if event.Type != "realtime.assistant" && event.Type != "realtime.user" {
			continue
		}
		turns := &user
		if event.Type == "realtime.assistant" {
			turns = &assistant
		}
		key := event.Type + ":" + data.Transcript.ID
		index, revision := indices[key]
		revision = revision && data.Transcript.ID != ""
		if event.Type == "realtime.assistant" {
			spoken := assistantSpoke || !hasSpeakingState || spokenItems[data.Transcript.ItemID] || revision
			assistantSpoke = false
			if !spoken {
				continue
			}
		}
		turn := voiceRecognitionTurn{text: data.Text, final: data.Transcript.Status == "" || data.Transcript.Status == "provider_final"}
		// A blocked revision cannot leave an earlier final counted as recognized.
		if data.Transcript.Status == "blocked" {
			turn = voiceRecognitionTurn{blocked: true}
		}
		if revision {
			(*turns)[index] = turn
		} else {
			if data.Transcript.ID != "" {
				indices[key] = len(*turns)
			}
			*turns = append(*turns, turn)
		}
	}
	return assistant, user
}

func voiceRecognitionTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// Match finalized content in order across receiver turn boundaries. Turn
// segmentation is provider-specific, so one received turn can cover several
// source utterances and several received turns can cover one source utterance.
// A mismatch describes recognition evidence only, never missing audio.
func matchVoiceRecognition(source, received []voiceRecognitionTurn) (pending int, status string) {
	if len(source) == 0 {
		return 0, "unknown"
	}
	var words []string
	for _, turn := range received {
		if turn.final {
			words = append(words, voiceRecognitionTokens(turn.text)...)
		}
	}
	status = "complete"
	offset := 0
	knownSource := false
	finalizedSource := false
	for _, turn := range source {
		if turn.blocked {
			continue
		}
		tokens := voiceRecognitionTokens(turn.text)
		knownSource = knownSource || len(tokens) > 0
		if !turn.final || len(tokens) == 0 {
			status = "partial"
			continue
		}
		finalizedSource = true
		matched := false
		for i := offset; i+len(tokens) <= len(words); i++ {
			if slices.Equal(tokens, words[i:i+len(tokens)]) {
				offset, matched = i+len(tokens), true
				break
			}
		}
		if !matched {
			pending++
			status = "partial"
		}
	}
	if !knownSource {
		return pending, "unknown"
	}
	if !finalizedSource {
		return pending, "partial"
	}
	if len(words) == 0 {
		status = "missing"
	}
	return pending, status
}
