package textinput

import "testing"

func TestNormalizeTemporalValue(t *testing.T) {
	tests := []struct {
		kind string
		raw  string
		want string
		ok   bool
	}{
		{kind: "time", raw: "08:00 PM", want: "20:00", ok: true},
		{kind: "time", raw: "8 PM", want: "20:00", ok: true},
		{kind: "time", raw: "08:30", want: "08:30", ok: true},
		{kind: "time", raw: "8", ok: false},
		{kind: "date", raw: "2026-06-05", want: "2026-06-05", ok: true},
		{kind: "date", raw: "6/5/2026", want: "2026-06-05", ok: true},
		{kind: "datetime-local", raw: "2026-06-05 08:00 PM", want: "2026-06-05T20:00", ok: true},
		{kind: "month", raw: "June 2026", want: "2026-06", ok: true},
		{kind: "week", raw: "2026-W23", want: "2026-W23", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.kind+" "+tt.raw, func(t *testing.T) {
			got, ok := NormalizeTemporalValue(tt.kind, tt.raw)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("NormalizeTemporalValue(%q, %q): want (%q,%v), got (%q,%v)", tt.kind, tt.raw, tt.want, tt.ok, got, ok)
			}
		})
	}
}
