package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

type parsedTranscript struct {
	Text               string
	Language           string
	Segments           []TranscriptSegment
	Metadata           json.RawMessage
	ProviderDurationMs int64
	RequestID          string
	WordCount          int
	LastWordEndMs      int64
	WordSource         string
}

type deepgramWord struct {
	Start      *float64 `json:"start"`
	End        *float64 `json:"end"`
	Word       string   `json:"word"`
	Punctuated string   `json:"punctuated_word"`
	Confidence *float64 `json:"confidence"`
	Speaker    *int     `json:"speaker"`
	Channel    *int     `json:"channel"`
}

type deepgramSentence struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

func providerMilliseconds(n float64) (int64, error) {
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1e10 {
		return 0, errors.New("invalid provider timestamp")
	}
	return int64(math.Round(n * 1000)), nil
}

// Read one best alternative per channel, or the utterance word stream when it
// contains more words. These are alternative representations, never concatenated.
func parseDeepgramResponse(data json.RawMessage) (*parsedTranscript, error) {
	var env struct {
		Metadata struct {
			DetectedLanguage string  `json:"detected_language"`
			Duration         float64 `json:"duration"`
			RequestID        string  `json:"request_id"`
		} `json:"metadata"`
		Results struct {
			Channels []struct {
				DetectedLanguage string `json:"detected_language"`
				Alternatives     []struct {
					Transcript string         `json:"transcript"`
					Words      []deepgramWord `json:"words"`
					Paragraphs struct {
						Paragraphs []struct {
							Sentences []deepgramSentence `json:"sentences"`
						} `json:"paragraphs"`
					} `json:"paragraphs"`
				} `json:"alternatives"`
			} `json:"channels"`
			Utterances []struct {
				Start      float64        `json:"start"`
				End        float64        `json:"end"`
				Transcript string         `json:"transcript"`
				Channel    int            `json:"channel"`
				Speaker    *int           `json:"speaker"`
				Words      []deepgramWord `json:"words"`
			} `json:"utterances"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if len(env.Results.Channels) == 0 && len(env.Results.Utterances) == 0 {
		return nil, errors.New("deepgram response has no channels or utterances")
	}
	duration, err := providerMilliseconds(env.Metadata.Duration)
	if err != nil {
		return nil, err
	}
	out := &parsedTranscript{Language: env.Metadata.DetectedLanguage, ProviderDurationMs: duration, RequestID: env.Metadata.RequestID}
	var meta map[string]json.RawMessage
	_ = json.Unmarshal(data, &meta)
	out.Metadata = meta["metadata"]
	utterWords := map[int][]deepgramWord{}
	utterSegments := map[int][]TranscriptSegment{}
	for _, u := range env.Results.Utterances {
		if u.Channel < 0 {
			return nil, errors.New("negative utterance channel")
		}
		channel := u.Channel
		for _, w := range u.Words {
			if w.Channel == nil {
				w.Channel = &channel
			}
			if w.Speaker == nil {
				w.Speaker = u.Speaker
			}
			utterWords[channel] = append(utterWords[channel], w)
		}
		start, e := providerMilliseconds(u.Start)
		if e != nil {
			return nil, e
		}
		end, e := providerMilliseconds(u.End)
		if e != nil {
			return nil, e
		}
		utterSegments[channel] = append(utterSegments[channel], TranscriptSegment{StartMs: start, EndMs: end, Text: u.Transcript, Channel: &channel})
	}
	channels := map[int]bool{}
	for i := range env.Results.Channels {
		channels[i] = true
	}
	for i := range utterWords {
		channels[i] = true
	}
	for i := range utterSegments {
		channels[i] = true
	}
	ids := []int{}
	for i := range channels {
		ids = append(ids, i)
	}
	sort.Ints(ids)
	sources := map[string]bool{}
	for _, channel := range ids {
		var rawWords []deepgramWord
		var segments []TranscriptSegment
		var text string
		if channel < len(env.Results.Channels) {
			c := env.Results.Channels[channel]
			if out.Language == "" {
				out.Language = c.DetectedLanguage
			}
			if len(c.Alternatives) > 0 {
				alt := c.Alternatives[0]
				text = strings.TrimSpace(alt.Transcript)
				rawWords = alt.Words
				for _, p := range alt.Paragraphs.Paragraphs {
					for _, s := range p.Sentences {
						start, e := providerMilliseconds(s.Start)
						if e != nil {
							return nil, e
						}
						end, e := providerMilliseconds(s.End)
						if e != nil {
							return nil, e
						}
						if end < start {
							return nil, errors.New("invalid sentence interval")
						}
						id := channel
						segments = append(segments, TranscriptSegment{StartMs: start, EndMs: end, Text: s.Text, Channel: &id})
					}
				}
			}
		}
		source := "channels"
		if len(utterWords[channel]) > len(rawWords) {
			rawWords = utterWords[channel]
			source = "utterances"
		}
		if len(rawWords) > 0 {
			sources[source] = true
		}
		if len(segments) == 0 {
			segments = utterSegments[channel]
		}
		if text == "" {
			for _, s := range segments {
				if text != "" {
					text += " "
				}
				text += s.Text
			}
		}
		var words []TranscriptWord
		for _, w := range rawWords {
			if w.Start == nil || w.End == nil {
				return nil, errors.New("word timestamps missing")
			}
			start, e := providerMilliseconds(*w.Start)
			if e != nil {
				return nil, e
			}
			end, e := providerMilliseconds(*w.End)
			if e != nil {
				return nil, e
			}
			id := channel
			if w.Channel != nil && *w.Channel != channel {
				return nil, errors.New("word channel mismatch")
			}
			if w.Confidence != nil && (math.IsNaN(*w.Confidence) || math.IsInf(*w.Confidence, 0) || *w.Confidence < 0 || *w.Confidence > 1) {
				return nil, errors.New("invalid word confidence")
			}
			if w.Speaker != nil && *w.Speaker < 0 {
				return nil, errors.New("negative speaker")
			}
			word := TranscriptWord{StartMs: start, EndMs: end, Text: firstNonEmpty(w.Punctuated, w.Word), Confidence: w.Confidence, Speaker: w.Speaker, Channel: &id}
			words = append(words, word)
		}
		if err := validateTranscriptWords(words, duration); err != nil {
			return nil, err
		}
		if text == "" && len(words) > 0 {
			parts := make([]string, len(words))
			for i, w := range words {
				parts[i] = w.Text
			}
			text = strings.Join(parts, " ")
		}
		for i, s := range segments {
			if s.EndMs < s.StartMs || (i > 0 && s.StartMs < segments[i-1].StartMs) {
				return nil, errors.New("unordered sentence intervals")
			}
		}
		if len(words) > 0 && len(segments) == 0 {
			id := channel
			segments = []TranscriptSegment{{StartMs: words[0].StartMs, EndMs: words[len(words)-1].EndMs, Text: text, Channel: &id}}
		}
		// Assign every word once; never deduplicate by text (repeated words matter).
		for _, w := range words {
			index := len(segments) - 1
			for i, s := range segments {
				if w.StartMs < s.EndMs {
					index = i
					break
				}
			}
			segments[index].Words = append(segments[index].Words, w)
			out.WordCount++
			if w.EndMs > out.LastWordEndMs {
				out.LastWordEndMs = w.EndMs
			}
		}
		out.Segments = append(out.Segments, segments...)
		if text != "" {
			if out.Text != "" {
				out.Text += "\n"
			}
			out.Text += text
		}
	}
	if len(sources) == 1 {
		for source := range sources {
			out.WordSource = source
		}
	} else if len(sources) > 1 {
		out.WordSource = "per_channel"
	}
	if out.Text == "" && out.WordCount == 0 && len(env.Results.Channels) > 0 && len(env.Results.Channels[0].Alternatives) == 0 {
		return nil, errors.New("deepgram response has no alternatives")
	}
	return out, nil
}
