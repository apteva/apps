package softphonebench

import "testing"

// Marker loss is intentionally sensitive to corruption, not a percentage of
// missing samples. Keep the original acceptance gate, but quantify this limit.
func TestMarkerCorruptionIsNotSpeechLossPercentage(t *testing.T) {
	clean, damaged := NewDecoder(16000), NewDecoder(16000)
	removed := 0
	for frame := 0; frame < 3000; frame++ {
		a, b := make([]int16, 320), make([]int16, 320)
		for i := range a {
			n := frame*320 + i
			a[i] = Sample(n, 16000, 0)
			b[i] = a[i]
			// Delete only the first 20ms sync tone of every tenth marker.
			if n/8000%10 == 5 && n%8000 < 320 {
				b[i] = 0
				removed++
			}
		}
		clean.Push(a, float64(frame+1)*20)
		damaged.Push(b, float64(frame+1)*20)
	}
	missing := len(clean.Markers) - len(damaged.Markers)
	pct := float64(missing) * 100 / float64(len(clean.Markers))
	samplesPct := float64(removed) * 100 / (60 * 16000)
	t.Logf("%.1f%% markers missing after erasing %.1f%% of PCM duration; these are different metrics", pct, samplesPct)
	if len(clean.Markers) != 120 || missing != 12 || samplesPct > .41 {
		t.Fatal(len(clean.Markers), missing, samplesPct)
	}
}
