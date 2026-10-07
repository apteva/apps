package main

import "time"

// Capture omission metadata is diagnostic only. It cannot mute media, relax
// authorization, change call state or reset the capture transit-age guard.
type captureMutedRange struct {
	First uint32
	Last  uint32
}

type captureMutedEvent struct {
	At            string `json:"at"`
	ConnectionID  string `json:"connection_id,omitempty"`
	FirstSequence uint32 `json:"first_sequence"`
	LastSequence  uint32 `json:"last_sequence"`
	Frames        uint64 `json:"frames"`
	DurationMS    uint64 `json:"duration_ms"`
}

func (h *softphoneHub) observeMutedCaptureRange(w *websocketWriterPump, first, last uint32, frames uint64, connectionID string) {
	// Worker reports bounded, contiguous 20ms source packets. Reject malformed
	// or implausibly large ranges; never let metadata affect audio forwarding.
	if last < first || frames == 0 || frames > 1000000 || frames != uint64(last)-uint64(first)+1 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.browser != w || h.closed {
		return
	}
	if h.captureMuteSeen {
		distance := first - h.captureMuteLast
		if distance == 0 || distance >= 1<<31 {
			return // Duplicate/stale range, including after its gap was consumed.
		}
	}
	if h.captureSequenceSet && last < h.captureExpected {
		return // Already received media cannot later be declared intentionally omitted.
	}
	if h.captureSequenceSet && first < h.captureExpected {
		first = h.captureExpected
		frames = uint64(last) - uint64(first) + 1
	}
	h.captureMuteSeen, h.captureMuteLast = true, last
	h.captureMutedFrames += frames
	ranges := h.captureMutedRanges
	if len(ranges) > 0 && uint64(ranges[len(ranges)-1].Last)+1 == uint64(first) {
		ranges[len(ranges)-1].Last = last
	} else {
		ranges = append(ranges, captureMutedRange{first, last})
	}
	if len(ranges) > 64 {
		ranges = ranges[len(ranges)-64:]
	}
	h.captureMutedRanges = ranges
	h.captureMutedEvents = append(h.captureMutedEvents, captureMutedEvent{
		At: time.Now().UTC().Format(time.RFC3339Nano), ConnectionID: connectionID,
		FirstSequence: first, LastSequence: last, Frames: frames, DurationMS: frames * 20,
	})
	if len(h.captureMutedEvents) > 32 {
		h.captureMutedEvents = h.captureMutedEvents[len(h.captureMutedEvents)-32:]
	}
}

// Called with h.mu held. Consume the reported omissions intersecting this gap,
// preserving unrelated loss before, after, or between intentional periods.
func (h *softphoneHub) mutedFramesInGap(first, last uint32) (uint64, uint32) {
	var omitted uint64
	firstUnexpected := first
	kept := h.captureMutedRanges[:0]
	for _, r := range h.captureMutedRanges {
		lo, hi := max(first, r.First), min(last, r.Last)
		if lo <= hi {
			omitted += uint64(hi) - uint64(lo) + 1
			if r.First <= firstUnexpected && r.Last >= firstUnexpected {
				firstUnexpected = min(r.Last, last) + 1
			}
		}
		if r.Last > last {
			r.First = max(r.First, last+1)
			kept = append(kept, r)
		}
	}
	h.captureMutedRanges = kept
	return omitted, firstUnexpected
}
