package softphonebench

import "math"

// Four 30ms tones, separated by 20ms guards, each 500ms: sync, two ID nibbles,
// checksum. IDs 0..127 identify microphone audio; 128..255 caller audio.
func Sample(sample, rate, offset int) int16 {
	if sample < 0 {
		return 0
	}
	ms := float64(sample) * 1000 / float64(rate)
	id := int(ms/500) + offset
	within := math.Mod(ms, 500)
	slot := int(within / 50)
	if id > 255 || slot > 3 || math.Mod(within, 50) >= 30 {
		return 0
	}
	frequency := 3000.
	hi, lo := (id>>4)&15, id&15
	if slot > 0 {
		symbol := []int{hi, lo, hi ^ lo ^ 10}[slot-1]
		frequency = 600 + float64(symbol)*100
	}
	return int16(math.Sin(2*math.Pi*frequency*float64(sample)/float64(rate)) * .25 * 32767)
}

type Marker struct {
	ID        int     `json:"id"`
	AtMS      float64 `json:"at_ms"`
	LevelDBFS float64 `json:"level_dbfs"`
}
type Decoder struct {
	Rate               int
	buffer             []int16
	lastSymbol, stable int
	segment            bool
	symbols            []int
	startMS, level     float64
	Markers            []Marker
}

func NewDecoder(rate int) *Decoder { return &Decoder{Rate: rate, lastSymbol: -2} }
func (d *Decoder) Push(pcm []int16, atMS float64) {
	d.buffer = append(d.buffer, pcm...)
	window := d.Rate / 100
	for len(d.buffer) >= window {
		remaining := len(d.buffer) - window
		end := atMS - float64(remaining)*1000/float64(d.Rate)
		frame := d.buffer[:window]
		symbol, level := Tone(frame, d.Rate)
		if symbol < 0 {
			d.segment = false
			d.stable = 0
			d.lastSymbol = -2
		} else {
			if symbol == d.lastSymbol {
				d.stable++
			} else {
				d.lastSymbol = symbol
				d.stable = 1
			}
			if d.stable >= 2 && !d.segment {
				d.segment = true
				if symbol == 16 {
					d.symbols = nil
					d.startMS = end - 20
					d.level = level
				} else if end-d.startMS < 260 {
					d.symbols = append(d.symbols, symbol)
					if len(d.symbols) == 3 {
						a, b, c := d.symbols[0], d.symbols[1], d.symbols[2]
						if a^b^10 == c {
							d.Markers = append(d.Markers, Marker{a*16 + b, d.startMS, d.level})
						}
						d.symbols = nil
						d.startMS = -1000
					}
				}
			}
		}
		d.buffer = d.buffer[window:]
	}
}
func Tone(pcm []int16, rate int) (int, float64) {
	var energy float64
	for _, x := range pcm {
		energy += float64(x) * float64(x)
	}
	if energy/float64(len(pcm)) < 100*100 {
		return -1, -120
	}
	best, symbol := 0., -1
	for n := 0; n < 17; n++ {
		freq := 600 + float64(n)*100
		if n == 16 {
			freq = 3000
		}
		coef := 2 * math.Cos(2*math.Pi*freq/float64(rate))
		a, b := 0., 0.
		for _, x := range pcm {
			v := float64(x) + coef*a - b
			b = a
			a = v
		}
		power := a*a + b*b - coef*a*b
		if power > best {
			best = power
			symbol = n
		}
	}
	if best < energy*float64(len(pcm))*.3 {
		return -1, -120
	}
	return symbol, 20 * math.Log10(math.Sqrt(energy/float64(len(pcm)))/32768)
}
