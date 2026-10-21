package main

import "time"

// Decode can catch up in a burst after a scheduler/network pause. Keep a fixed
// 320ms reserve, then release at media cadence so a carrier doesn't receive a
// burst disguised as newly captured speech. Genuine old audio keeps its age.
type rtcReleaseFrame struct {
	pcm    []int16
	source time.Time
}
type rtcAudioRelease struct {
	frames      [16]rtcReleaseFrame
	storage     [16][480]int16
	head, count int
	next        time.Time
	tail        [120]int16
	tailN       int
	crossfade   bool
}

func (q *rtcAudioRelease) push(pcm []int16, at time.Time, stats *rtcMediaStats, direction string) {
	for len(pcm) > 0 {
		n := min(480, len(pcm))
		if q.count == len(q.frames) {
			old := q.frames[q.head]
			stats.drop("webrtc_capture_release_overflow", direction, 0, len(old.pcm)*1000/24000)
			q.frames[q.head] = rtcReleaseFrame{}
			q.head = (q.head + 1) % len(q.frames)
			q.count--
		}
		slot := (q.head + q.count) % len(q.frames)
		copy(q.storage[slot][:], pcm[:n])
		q.frames[slot] = rtcReleaseFrame{q.storage[slot][:n], at}
		q.count++
		atomicMax(&stats.captureMaxQueue, int64(q.count*20))
		pcm = pcm[n:]
		at = at.Add(time.Duration(n) * time.Second / 24000)
	}
}
func (q *rtcAudioRelease) pop(now time.Time, stats *rtcMediaStats, direction string) (rtcReleaseFrame, bool) {
	if now.Before(q.next) {
		return rtcReleaseFrame{}, false
	}
	// After a pause, a paced backlog can remain at the edge of the shared
	// 250ms source budget forever. Leave room for the loopback and carrier pacer
	// by catching up to the fresh end of a backlog. Never change source times;
	// a single genuinely delayed frame still uses the normal expiry rule.
	recoverBacklog := q.count > 1 && now.Sub(q.frames[q.head].source) >= 180*time.Millisecond
	for recoverBacklog && q.count > 1 && now.Sub(q.frames[q.head].source) > 80*time.Millisecond {
		old := q.frames[q.head]
		q.frames[q.head] = rtcReleaseFrame{}
		q.head = (q.head + 1) % len(q.frames)
		q.count--
		reason := "webrtc_capture_release_recovery"
		if now.Sub(old.source) >= liveAudioMaxAge {
			reason = "webrtc_capture_source_age"
		}
		stats.drop(reason, direction, 0, len(old.pcm)*1000/24000)
		q.crossfade = true
	}
	for q.count > 0 {
		f := q.frames[q.head]
		q.frames[q.head] = rtcReleaseFrame{}
		q.head = (q.head + 1) % len(q.frames)
		q.count--
		if now.Sub(f.source) >= liveAudioMaxAge {
			stats.drop("webrtc_capture_source_age", direction, 0, len(f.pcm)*1000/24000)
			q.crossfade = true
			continue
		}
		// Keep absolute media cadence under ordinary timer lateness. Reset only
		// after a full missed interval; a scheduler pause cannot trigger a
		// catch-up burst, and sub-frame lateness cannot accumulate clock drift.
		step := time.Duration(len(f.pcm)) * time.Second / 24000
		if q.next.IsZero() || now.Sub(q.next) >= step {
			q.next = now.Add(step)
		} else {
			q.next = q.next.Add(step)
		}
		if q.crossfade && q.tailN > 0 {
			applyPCMOverlapCrossfade(q.tail[:q.tailN], f.pcm, min(q.tailN, len(f.pcm)))
		}
		q.crossfade = false
		q.tailN = min(len(q.tail), len(f.pcm))
		copy(q.tail[:q.tailN], f.pcm[len(f.pcm)-q.tailN:])
		return f, true
	}
	return rtcReleaseFrame{}, false
}
