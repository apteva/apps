package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
)

var stdEnc = base64.StdEncoding

// TestReadGitHubTarball_HappyPath: a tarball shaped like one GitHub
// produces (entries prefixed with `<repo>-<sha>/`) is unpacked into a
// path → bytes map with the leading directory stripped.
func TestReadGitHubTarball_HappyPath(t *testing.T) {
	body := mustGzipTarball(t, []tarEntry{
		{name: "octocat-Hello-World-deadbeef/", isDir: true},
		{name: "octocat-Hello-World-deadbeef/README.md", body: []byte("# hello")},
		{name: "octocat-Hello-World-deadbeef/src/main.go", body: []byte("package main\n")},
	})
	files, err := readGitHubTarball(body)
	if err != nil {
		t.Fatalf("readGitHubTarball: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("file count = %d, want 2 (dir entry should be skipped). files=%v", len(files), keys(files))
	}
	if string(files["README.md"]) != "# hello" {
		t.Errorf("README content = %q", files["README.md"])
	}
	if string(files["src/main.go"]) != "package main\n" {
		t.Errorf("src/main.go content = %q", files["src/main.go"])
	}
}

// TestReadGitHubTarball_RejectsTraversal: an entry that, after the
// leading directory is stripped, escapes the root must be rejected.
// Without this guard a malicious tarball could write outside the
// FileStore's repo dir.
func TestReadGitHubTarball_RejectsTraversal(t *testing.T) {
	body := mustGzipTarball(t, []tarEntry{
		{name: "octocat-evil-deadbeef/../etc/passwd", body: []byte("nope")},
	})
	if _, err := readGitHubTarball(body); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("expected escapes-root error, got %v", err)
	}
}

// TestReadGitHubTarball_SkipsSymlinks: GitHub doesn't typically include
// symlinks but the FileStore couldn't represent them anyway. Skip
// silently rather than corrupting the import.
func TestReadGitHubTarball_SkipsSymlinks(t *testing.T) {
	body := mustGzipTarball(t, []tarEntry{
		{name: "octocat-Hello-World-deadbeef/", isDir: true},
		{name: "octocat-Hello-World-deadbeef/README.md", body: []byte("plain")},
		{name: "octocat-Hello-World-deadbeef/link", typeflag: tar.TypeSymlink, linkname: "README.md"},
	})
	files, err := readGitHubTarball(body)
	if err != nil {
		t.Fatalf("readGitHubTarball: %v", err)
	}
	if _, ok := files["link"]; ok {
		t.Errorf("symlink should have been skipped, got %v", files)
	}
	if string(files["README.md"]) != "plain" {
		t.Errorf("README missing or wrong: %q", files["README.md"])
	}
}

// TestStripLeadingDir covers the helper's three meaningful cases —
// nothing to strip (no slash → "" returned to skip), the standard
// "<repo>-<sha>/path" shape, and a "./" prefix some tooling adds.
func TestStripLeadingDir(t *testing.T) {
	cases := map[string]string{
		"":                                       "",
		"loose":                                  "",
		"octocat-Hello-deadbeef/":                "",
		"octocat-Hello-deadbeef/README.md":       "README.md",
		"octocat-Hello-deadbeef/src/main.go":     "src/main.go",
		"./octocat-Hello-deadbeef/README.md":     "README.md",
	}
	for in, want := range cases {
		if got := stripLeadingDir(in); got != want {
			t.Errorf("stripLeadingDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDetectImportFramework pins the auto-detect heuristics against
// the real signatures Code's framework set knows about. Drives the
// "framework: (auto-detect)" panel option after import.
func TestDetectImportFramework(t *testing.T) {
	cases := []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{"empty", map[string][]byte{}, ""},
		{"go", map[string][]byte{"go.mod": []byte("module x")}, "go"},
		{"nextjs", map[string][]byte{"package.json": []byte(`{"dependencies":{"next":"^14"}}`)}, "nextjs"},
		{"node-other", map[string][]byte{"package.json": []byte(`{"dependencies":{"express":"^4"}}`)}, "blank"},
		{"static", map[string][]byte{"index.html": []byte("<html></html>")}, "static"},
		{"python-req", map[string][]byte{"requirements.txt": []byte("flask\n")}, "python"},
		{"python-pyproject", map[string][]byte{"pyproject.toml": []byte("[project]\n")}, "python"},
		// go.mod outranks index.html — simulates a Go server that ships
		// a static admin UI alongside its source. This pins the order
		// in detectImportFramework against accidental swap.
		{"go-beats-static", map[string][]byte{"go.mod": []byte("m"), "index.html": []byte("<html></html>")}, "go"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := detectImportFramework(tc.files); got != tc.want {
				t.Errorf("detectImportFramework() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDecodeBinaryEnvelope_Roundtrip verifies the {_binary, base64,
// mimeType, size} shape the apteva-server integration runner produces
// is decoded correctly. Anything else (raw JSON, missing _binary)
// must error so the import flow surfaces a useful message instead of
// silently writing zero bytes.
func TestDecodeBinaryEnvelope_Roundtrip(t *testing.T) {
	payload := []byte{0x1f, 0x8b, 0xde, 0xad, 0xbe, 0xef}
	envJSON := []byte(`{"_binary":true,"base64":"` + base64Std(payload) + `","mimeType":"application/x-gzip","size":6}`)
	got, err := decodeBinaryEnvelope(envJSON)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded = %x, want %x", got, payload)
	}

	if _, err := decodeBinaryEnvelope([]byte(`{"_binary":false,"base64":""}`)); err == nil {
		t.Error("expected error for non-binary envelope")
	}
	if _, err := decodeBinaryEnvelope([]byte(``)); err == nil {
		t.Error("expected error for empty input")
	}
}

func base64Std(b []byte) string {
	return stdEnc.EncodeToString(b)
}

// ─── tarball test helpers ─────────────────────────────────────────

type tarEntry struct {
	name     string
	body     []byte
	isDir    bool
	typeflag byte
	linkname string
}

func mustGzipTarball(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o644,
			Size:     int64(len(e.body)),
			Linkname: e.linkname,
		}
		switch {
		case e.isDir:
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
		case e.typeflag != 0:
			hdr.Typeflag = e.typeflag
			hdr.Size = 0
		default:
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if hdr.Typeflag == tar.TypeReg && len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
