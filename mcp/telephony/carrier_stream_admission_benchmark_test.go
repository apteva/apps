package main

import (
	"encoding/json"
	"testing"
)

type carrierMediaFrameBeforeErrorFix struct {
	Event     string `json:"event"`
	EventType string `json:"eventType,omitempty"`
	Track     string `json:"track,omitempty"`
	Payload   string `json:"payload,omitempty"`
	Metadata  *struct {
		CallID   string `json:"callId"`
		StreamID string `json:"streamId"`
		Tracks   []struct {
			Name        string `json:"name"`
			MediaFormat struct {
				Encoding   string `json:"encoding"`
				SampleRate int    `json:"sampleRate"`
			} `json:"mediaFormat"`
		} `json:"tracks"`
	} `json:"metadata,omitempty"`
	StreamSID           string `json:"streamSid,omitempty"`
	StreamID            string `json:"stream_id,omitempty"`
	SequenceNumber      any    `json:"sequenceNumber,omitempty"`
	SequenceNumberSnake any    `json:"sequence_number,omitempty"`
	Start               *struct {
		CallSID       string `json:"callSid,omitempty"`
		CallID        string `json:"callId,omitempty"`
		CallUUID      string `json:"callUuid,omitempty"`
		CallControlID string `json:"call_control_id,omitempty"`
		StreamSID     string `json:"streamSid,omitempty"`
		StreamID      string `json:"streamId,omitempty"`
		MediaFormat   struct {
			Encoding   string `json:"encoding,omitempty"`
			SampleRate int    `json:"sample_rate,omitempty"`
			Channels   int    `json:"channels,omitempty"`
		} `json:"media_format,omitempty"`
	} `json:"start,omitempty"`
	Media *struct {
		Payload   string `json:"payload"`
		Timestamp any    `json:"timestamp,omitempty"`
		Chunk     any    `json:"chunk,omitempty"`
	} `json:"media,omitempty"`
	DTMF *struct {
		Digit string `json:"digit"`
	} `json:"dtmf,omitempty"`
	Mark *struct {
		Name string `json:"name"`
	} `json:"mark,omitempty"`
	Error any `json:"error,omitempty"`
}

func BenchmarkCarrierStreamPriorDecode(b *testing.B) {
	raw := []byte(`{"event":"media","sequence_number":"2","stream_id":"stream","media":{"payload":"AAAA","timestamp":"20","chunk":"1"}}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var f carrierMediaFrameBeforeErrorFix
		if err := json.Unmarshal(raw, &f); err != nil {
			b.Fatal(err)
		}
	}
}
