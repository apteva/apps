package main

import "time"

// A bounded rolling distribution of relative delay, not absolute one-way
// latency. The 100 ms buckets prevent one catch-up batch from dominating it.
// The 10 second window forgets old conditions; no allocations on media ingress.
type audioDelayDistribution struct {
	slots [100]struct {
		at        int64
		low, high float64
		valid     bool
	}
	lastChange time.Time
	target     int
}

func (d *audioDelayDistribution) observe(now time.Time, relativeMS float64, floor, ceiling int) int {
	bucket := now.UnixNano() / int64(100*time.Millisecond)
	index := int((bucket%100 + 100) % 100)
	s := &d.slots[index]
	if !s.valid || s.at != bucket {
		s.at = bucket
		s.low = relativeMS
		s.high = relativeMS
		s.valid = true
	} else {
		s.low = min(s.low, relativeMS)
		s.high = max(s.high, relativeMS)
	}
	low := relativeMS
	for _, v := range d.slots {
		if v.valid && bucket-v.at >= 0 && bucket-v.at < 100 {
			low = min(low, v.low)
		}
	}
	var histogram [41]int
	count := 0
	for _, v := range d.slots {
		if v.valid && bucket-v.at >= 0 && bucket-v.at < 100 {
			n := min(40, max(0, int((v.high-low+4.999)/5)))
			histogram[n]++
			count++
		}
	}
	quantile := (count*95 + 99) / 100
	selected, total := 0, 0
	for i, n := range histogram {
		total += n
		if total >= quantile {
			selected = i * 5
			break
		}
	}
	wanted := min(ceiling, max(floor, 20+selected))
	if d.target == 0 {
		d.target = floor
	}
	if wanted > d.target {
		d.target = wanted
		d.lastChange = now
	} else if wanted < d.target && now.Sub(d.lastChange) >= time.Second {
		d.target = max(wanted, d.target-5)
		d.lastChange = now
	}
	return d.target
}
