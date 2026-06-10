package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDownloadArgsPrivateAudio(t *testing.T) {
	req := downloadRequest{
		URL:            "https://www.youtube.com/watch?v=abc",
		Mode:           "audio",
		AudioFormat:    "m4a",
		NoPlaylist:     true,
		FFmpegLocation: "/usr/bin/ffmpeg",
		YoutubePlayer:  "android",
	}
	args := buildDownloadArgs(req, "/tmp/job", "/tmp/cookies.txt")
	got := stringsJoin(args)
	for _, want := range []string{"--cookies /tmp/cookies.txt", "--ffmpeg-location /usr/bin/ffmpeg", "--extractor-args youtube:player_client=android", "-x --audio-format m4a", "--no-playlist"} {
		if !containsArgSequence(got, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestBuildDownloadArgsSkipsYouTubeArgsForOtherHosts(t *testing.T) {
	req := downloadRequest{
		URL:           "https://vimeo.com/123",
		Mode:          "video",
		Quality:       "720p",
		YoutubePlayer: "android",
	}
	args := buildDownloadArgs(req, "/tmp/job", "")
	if strings.Contains(stringsJoin(args), "youtube:player_client") {
		t.Fatalf("non-YouTube args should not include youtube extractor args: %v", args)
	}
}

func TestFormatSelector(t *testing.T) {
	cases := map[string]string{
		"":      "bv*+ba/b",
		"best":  "bv*+ba/b",
		"720p":  "bv*[height<=720]+ba/b[height<=720]",
		"worst": "wv*+wa/w",
	}
	for q, want := range cases {
		if got := formatSelector(downloadRequest{Quality: q}); got != want {
			t.Fatalf("quality %q => %q, want %q", q, got, want)
		}
	}
	if got := formatSelector(downloadRequest{FormatID: "137+140", Quality: "best"}); got != "137+140" {
		t.Fatalf("format_id should win, got %q", got)
	}
}

func TestParseProgressLine(t *testing.T) {
	got, ok := parseProgressLine("[download]  42.7% of 10.00MiB at 1.00MiB/s ETA 00:01")
	if !ok || got != 42.7 {
		t.Fatalf("progress = %v %v, want 42.7 true", got, ok)
	}
}

func TestFindOutputFileUsesPrintedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := findOutputFile(dir, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("got %q, want %q", got, path)
	}
}

func stringsJoin(args []string) string { return strings.Join(args, " ") }

func containsArgSequence(haystack, needle string) bool { return strings.Contains(haystack, needle) }
