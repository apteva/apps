package main

import "testing"

func TestNormalisePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"app/page.tsx", "app/page.tsx", false},
		{"/app/page.tsx", "app/page.tsx", false},
		{"./app/page.tsx", "app/page.tsx", false},
		{"app//page.tsx", "app/page.tsx", false}, // path.Clean collapses
		{"package.json", "package.json", false},
		{"a/b/../c", "a/c", false}, // path.Clean resolves; doesn't escape

		// Rejected — must not pass.
		{"", "", true},
		{"   ", "", true},
		{"..", "", true},
		{"../etc/passwd", "", true},
		{"app/../../../etc/passwd", "", true},
		{"app/page\x00", "", true},
		{"app\\page.tsx", "", true},
	}
	for _, c := range cases {
		got, err := normalisePath(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalisePath(%q) want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalisePath(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalisePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitDirBase(t *testing.T) {
	cases := []struct {
		in        string
		dir, base string
	}{
		{"app/page.tsx", "app", "page.tsx"},
		{"page.tsx", "", "page.tsx"},
		{"a/b/c/d.txt", "a/b/c", "d.txt"},
	}
	for _, c := range cases {
		dir, base := splitDirBase(c.in)
		if dir != c.dir || base != c.base {
			t.Errorf("splitDirBase(%q) = (%q,%q), want (%q,%q)", c.in, dir, base, c.dir, c.base)
		}
	}
}

func TestIsLikelyText(t *testing.T) {
	if !isLikelyText([]byte("hello world\n")) {
		t.Error("plain text should be detected")
	}
	if isLikelyText([]byte{0xff, 0x00, 0xab, 0xcd}) {
		t.Error("binary bytes shouldn't pass as text")
	}
	if !isLikelyText([]byte{}) {
		t.Error("empty file should pass as text (vacuously)")
	}
}
