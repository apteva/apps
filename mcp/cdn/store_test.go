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

func TestValidateOriginApp(t *testing.T) {
	good := []string{"storage", "media", "media-studio", "app1", "x"}
	for _, s := range good {
		if err := validateOriginApp(s); err != nil {
			t.Errorf("validateOriginApp(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{"", "Storage", "stor age", "app/x", "app:8080", "http://x", "-app", "app-", "app_name"}
	for _, s := range bad {
		if err := validateOriginApp(s); err == nil {
			t.Errorf("validateOriginApp(%q) = nil, want error", s)
		}
	}
}

func TestRouteTarget(t *testing.T) {
	if got := routeTarget(&Zone{OriginApp: "storage"}); got != "app://storage" {
		t.Errorf("app-origin (no project) target = %q, want app://storage", got)
	}
	if got := routeTarget(&Zone{OriginApp: "storage", ProjectID: "p1"}); got != "app://storage?project_id=p1" {
		t.Errorf("app-origin target = %q, want app://storage?project_id=p1", got)
	}
	if got := routeTarget(&Zone{OriginURL: "http://127.0.0.1:8080"}); got != "http://127.0.0.1:8080" {
		t.Errorf("url-origin target = %q, want the url", got)
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
