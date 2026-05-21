package main

import "testing"

// porkbunRecordUnchanged decides whether an edit would be a true no-op
// (the case Porkbun rejects with EDIT_ERROR). It must only short-circuit
// when value, ttl, and prio all already match.
func TestPorkbunRecordUnchanged(t *testing.T) {
	base := DNSRecord{Value: "v.dkim.amazonses.com", TTL: 1800, Prio: 0}

	cases := []struct {
		name     string
		existing DNSRecord
		content  string
		ttl      int
		prio     int
		want     bool
	}{
		{"identical", base, "v.dkim.amazonses.com", 1800, 0, true},
		{"value differs", base, "other.dkim.amazonses.com", 1800, 0, false},
		{"ttl differs", base, "v.dkim.amazonses.com", 3600, 0, false},
		{"prio differs", DNSRecord{Value: "mx.example.com", TTL: 1800, Prio: 10}, "mx.example.com", 1800, 20, false},
		{"value case + whitespace insensitive", DNSRecord{Value: " V.DKIM.amazonSES.com ", TTL: 1800}, "v.dkim.amazonses.com", 1800, 0, true},
	}
	for _, tc := range cases {
		if got := porkbunRecordUnchanged(tc.existing, tc.content, tc.ttl, tc.prio); got != tc.want {
			t.Errorf("%s: porkbunRecordUnchanged = %v, want %v", tc.name, got, tc.want)
		}
	}
}
