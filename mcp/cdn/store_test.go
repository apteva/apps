package main

import "testing"

func TestValidateHostname(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		why     string
	}{
		{"files.acme.com", false, "standard FQDN"},
		{"a.b", false, "two-label is fine"},
		{"", true, "empty"},
		{"acme", true, "no dot"},
		{"files acme.com", true, "whitespace"},
		{"https://files.acme.com", true, "scheme"},
		{"files.acme.com/path", true, "path"},
		{"files.acme.com:8080", true, "port"},
	}
	for _, c := range cases {
		err := validateHostname(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validateHostname(%q): err=%v wantErr=%v (%s)", c.in, err, c.wantErr, c.why)
		}
	}
}

func TestValidateOriginURL(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"http://127.0.0.1:8080", false},
		{"https://storage.internal", false},
		{"http://storage:8080/files", false}, // path is allowed
		{"", true},
		{"file:///etc/passwd", true},
		{"ftp://nope", true},
		{"http://", true}, // no host
	}
	for _, c := range cases {
		err := validateOriginURL(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validateOriginURL(%q): err=%v wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestSplitApex(t *testing.T) {
	cases := []struct {
		host string
		apex string
		sub  string
	}{
		{"acme.com", "acme.com", ""},
		{"files.acme.com", "acme.com", "files"},
		{"a.b.c.acme.com", "acme.com", "a.b.c"},
		// Known limitation — naive split; multi-label TLDs land in the
		// wrong apex. Test pinned so a future fix has to update both.
		{"foo.acme.co.uk", "co.uk", "foo.acme"},
	}
	for _, c := range cases {
		apex, sub := splitApex(c.host)
		if apex != c.apex || sub != c.sub {
			t.Errorf("splitApex(%q) = (%q,%q), want (%q,%q)", c.host, apex, sub, c.apex, c.sub)
		}
	}
}
