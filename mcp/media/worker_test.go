package main

import "testing"

func TestIsMediaContentType(t *testing.T) {
	yes := []string{"audio/mpeg", "video/mp4", "image/png", "AUDIO/WAV"}
	for _, c := range yes {
		if !isMediaContentType(c) {
			t.Errorf("%q should be media", c)
		}
	}
	no := []string{"text/plain", "application/pdf", "", "audio", "video"}
	for _, c := range no {
		if isMediaContentType(c) {
			t.Errorf("%q should NOT be media", c)
		}
	}
}

func TestIsMediaByExt(t *testing.T) {
	yes := []string{"clip.mp4", "song.MP3", "shot.jpeg", "image.heic", "track.opus"}
	for _, n := range yes {
		if !isMediaByExt(n) {
			t.Errorf("%q should be media by ext", n)
		}
	}
	no := []string{"doc.pdf", "page.html", "data.csv", "noext"}
	for _, n := range no {
		if isMediaByExt(n) {
			t.Errorf("%q should NOT be media by ext", n)
		}
	}
}

func TestFilterMediaFiles(t *testing.T) {
	in := []StorageFile{
		{ID: 1, Name: "a.mp4", ContentType: "video/mp4"},
		{ID: 2, Name: "b.txt", ContentType: "text/plain"},
		{ID: 3, Name: "c.heic", ContentType: ""},      // ct missing, ext rescues
		{ID: 4, Name: "d.csv", ContentType: ""},        // not media either way
		{ID: 5, Name: "e.png", ContentType: "image/png"},
	}
	got := filterMediaFiles(in)
	if len(got) != 3 {
		t.Errorf("expected 3 media files, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.ID == 2 || f.ID == 4 {
			t.Errorf("non-media leaked: %+v", f)
		}
	}
}

// TestFilterMediaFiles_SkipsOwnDerivations is the regression test
// for the indexer loop bug — files in /.media/ subfolders must
// never be returned as candidates. Without this filter, every
// 30-second sweep generated a thumbnail of the previous thumbnail,
// growing storage by one file per source per tick forever.
func TestFilterMediaFiles_SkipsOwnDerivations(t *testing.T) {
	in := []StorageFile{
		// Real user uploads — eligible.
		{ID: 1, Folder: "/photos/", Name: "cat.jpg", ContentType: "image/jpeg"},
		{ID: 2, Folder: "/audio/", Name: "song.mp3", ContentType: "audio/mpeg"},
		// The indexer's own outputs — must be skipped on every sweep.
		{ID: 100, Folder: "/.media/thumbnail/", Name: "1.jpg", ContentType: "image/jpeg"},
		{ID: 101, Folder: "/.media/waveform/", Name: "2.png", ContentType: "image/png"},
		{ID: 102, Folder: "/.media/thumbnail/sub/", Name: "3.jpg", ContentType: "image/jpeg"},
	}
	got := filterMediaFiles(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 user files, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.ID == 100 || f.ID == 101 || f.ID == 102 {
			t.Errorf("derivation leaked into indexer: %+v", f)
		}
	}
}

func TestIsOwnDerivation(t *testing.T) {
	cases := map[string]bool{
		"/.media/thumbnail/":    true,
		"/.media/waveform/":     true,
		"/.media/thumbnail/x/":  true,  // sub-folders match
		"/.media":               false, // no trailing slash — leave it
		"/photos/.media/":       false, // dot folder must be top-level
		"/":                     false,
		"/photos/":              false,
		"":                      false,
	}
	for in, want := range cases {
		if got := isOwnDerivation(in); got != want {
			t.Errorf("isOwnDerivation(%q)=%v want %v", in, got, want)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"":           "file.bin",
		".":          "file.bin",
		"..":         "file.bin",
		"a/b/c.mp4":  "a_b_c.mp4",
		`a\b\c.mp4`:  "a_b_c.mp4",
		"normal.mp4": "normal.mp4",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q)=%q want %q", in, got, want)
		}
	}
}

// Config helper round-trips — these run against the typed `any`
// shape parseConfigInt/parseConfigFloat return so toInt + toFloat
// can read them back.

func TestParseConfigInt(t *testing.T) {
	if v := toInt(parseConfigInt("42", 0)); v != 42 {
		t.Errorf("got %d", v)
	}
	if v := toInt(parseConfigInt("", 99)); v != 99 {
		t.Errorf("default not used: %d", v)
	}
	if v := toInt(parseConfigInt("garbage", 7)); v != 7 {
		t.Errorf("bad input fallback failed: %d", v)
	}
}

func TestParseConfigFloat(t *testing.T) {
	if v := toFloat(parseConfigFloat("1.5", 0)); v != 1.5 {
		t.Errorf("got %v", v)
	}
	if v := toFloat(parseConfigFloat("", 2.0)); v != 2.0 {
		t.Errorf("default not used: %v", v)
	}
}
