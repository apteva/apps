package main

import "time"

// Called with mu held, for a contiguous audio sequence whose RTP timestamp is
// ahead of the playback cursor. This does not assert intentional DTX. Original
// arrival/age, decoder state and source-clock evidence remain unchanged.
func (b *rtpJitterBuffer) popSourceTimestampGap(now time.Time, seq uint16, ts uint32, needed int) bool {
	gap := int(int32(ts - b.expectedTimestamp))
	if !b.sourceGapKnown || b.sourceGapTarget != ts {
		b.sourceGapKnown, b.sourceGapTarget = true, ts
		b.sourceGapDeclaredSamples += gap
		b.dtxResumes++ // Retained compatibility counter, not proof of intentional DTX.
		b.event(now, "source_silence", seq, ts, 0, 0)
		b.events[(b.eventHead+63)%len(b.events)].SourceGapMS += float64(gap) / 8
	}
	gapTime := time.Duration(gap) * time.Second / 8000
	if b.policy.Receiver == "reference" || gap > b.policy.CapacityMS*8 || b.packetAge(now, b.arrivals[seq], ts)+gapTime > time.Duration(b.policy.MaxAgeMS)*time.Millisecond {
		// Never wait through a large/ambiguous source discontinuity or expire
		// fresh queued speech to reproduce an obsolete timestamp interval.
		b.sourceGapSkippedSamples += gap
		b.expectedTimestamp = ts
		return false
	}
	n := min(gap, b.packetSamples)
	if needed >= 0 {
		n = min(n, max(1, needed))
	}
	b.lastPoppedGapSamples = n
	b.lastPoppedMissing = false
	b.lastPoppedTimestamp = b.expectedTimestamp
	b.lastPoppedArrival = b.arrivals[seq]
	b.lastPoppedAt = b.lastPoppedArrival
	b.lastPoppedSource = b.sourceTime(b.expectedTimestamp)
	b.lastPoppedPlayout = b.playoutTime(b.expectedTimestamp)
	b.expectedTimestamp += uint32(n)
	b.missing = 0
	b.sourceGapFilledSamples.Add(int64(n))
	if needed >= 0 {
		b.pendingPCMSamples.Add(int64(max(0, n-needed)))
	}
	return true
}
