package main

import "testing"

func TestNormaliseDomainName(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"acme.com", "acme.com", false},
		{"  ACME.com  ", "acme.com", false},
		{"https://acme.com/foo", "acme.com", false},
		{"acme.com.", "acme.com", false}, // trailing dot trimmed
		{"sub.acme.com", "sub.acme.com", false},
		{"", "", true},
		{"no-tld", "", true},
		{".leading", "", true},
		{"trailing.", "", true},
		{"double..dot.com", "", true},
		{"with space.com", "", true},
		{"foo@bar.com", "", true},
	}
	for _, tc := range cases {
		got, err := normaliseDomainName(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("normaliseDomainName(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseDomainName(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normaliseDomainName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormaliseRecordType(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"A", "A", false},
		{"a", "A", false},
		{"  cname  ", "CNAME", false},
		{"mx", "MX", false},
		{"unknown", "", true},
	}
	for _, tc := range cases {
		got, err := normaliseRecordType(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("normaliseRecordType(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseRecordType(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normaliseRecordType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormaliseSubaddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"@", ""},
		{"", ""},
		{"  www  ", "www"},
		{"MAIL", "mail"},
	}
	for _, tc := range cases {
		if got := normaliseSubaddress(tc.in); got != tc.want {
			t.Errorf("normaliseSubaddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
