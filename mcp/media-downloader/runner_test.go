package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureRunner struct {
	stdout []string
	stderr []string
	err    error
	args   []string
}

func (r *fixtureRunner) Run(_ context.Context, _ string, args []string, stdout func(string), stderr func(string)) error {
	r.args = append([]string(nil), args...)
	for _, line := range r.stdout {
		stdout(line)
	}
	for _, line := range r.stderr {
		stderr(line)
	}
	return r.err
}

func TestBuildDownloadArgsPrivateAudio(t *testing.T) {
	req := downloadRequest{
		URL:              "https://www.youtube.com/watch?v=abc",
		Mode:             "audio",
		AudioFormat:      "m4a",
		FFmpegLocation:   "/usr/bin/ffmpeg",
		YoutubePlayer:    "android",
		YTDLPExtraArgs:   []string{"--js-runtimes", "node:/usr/bin/node"},
		ProxyURL:         "http://127.0.0.1:1234",
		MaxDownloadBytes: 1048576,
	}
	args := buildDownloadArgs(req, "/tmp/job", "/tmp/cookies.txt")
	got := stringsJoin(args)
	for _, want := range []string{"--js-runtimes node:/usr/bin/node", "--proxy http://127.0.0.1:1234", "--max-filesize 1048576", "--cookies /tmp/cookies.txt", "--ffmpeg-location /usr/bin/ffmpeg", "--print before_dl:__APTEVA_META__", "--print after_move:__APTEVA_FILE__", "-f ba/b[acodec!=none][height<=360]/b[acodec!=none]/b", "-x --audio-format m4a", "--no-playlist"} {
		if !containsArgSequence(got, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
	if strings.Contains(got, "youtube:player_client=android") {
		t.Fatalf("authenticated YouTube downloads should let yt-dlp choose a cookie-capable client: %v", args)
	}
}

func TestBuildDownloadArgsUsesConfiguredYouTubeClientWithoutCookies(t *testing.T) {
	req := downloadRequest{
		URL:           "https://www.youtube.com/watch?v=abc",
		Mode:          "video",
		Quality:       "best",
		YoutubePlayer: "android",
	}
	args := buildDownloadArgs(req, "/tmp/job", "")
	if !containsArgSequence(stringsJoin(args), "--extractor-args youtube:player_client=android") {
		t.Fatalf("anonymous YouTube downloads should keep configured player client: %v", args)
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

func TestBuildProbeArgsIncludesExtraArgs(t *testing.T) {
	args := buildProbeArgs("https://www.youtube.com/watch?v=abc", "/tmp/cookies.txt", []string{"--js-runtimes", "node:/usr/bin/node"}, "http://127.0.0.1:1234")
	got := stringsJoin(args)
	for _, want := range []string{"--dump-single-json", "--js-runtimes node:/usr/bin/node", "--proxy http://127.0.0.1:1234", "--cookies /tmp/cookies.txt"} {
		if !containsArgSequence(got, want) {
			t.Fatalf("probe args missing %q: %v", want, args)
		}
	}
}

func TestBuildSearchArgs(t *testing.T) {
	args := buildSearchArgs("coffee documentary", 50, "/tmp/cookies.txt", []string{"--remote-components", "ejs:github"}, "http://127.0.0.1:1234")
	got := stringsJoin(args)
	for _, want := range []string{
		"--flat-playlist",
		"--dump-single-json",
		"--playlist-end 25",
		"--remote-components ejs:github",
		"--proxy http://127.0.0.1:1234",
		"--cookies /tmp/cookies.txt",
		"ytsearch25:coffee documentary",
	} {
		if !containsArgSequence(got, want) {
			t.Fatalf("search args missing %q: %v", want, args)
		}
	}
}

func TestSearchYouTubeNormalizesResults(t *testing.T) {
	runner := &fixtureRunner{stdout: []string{`{
		"entries": [
			{
				"id": "abc-123",
				"title": "Coffee Story",
				"channel": "Example Channel",
				"duration": 125.5,
				"age_limit": 18,
				"thumbnails": [
					{"url": "small.jpg", "width": 120, "height": 90},
					{"url": "large.jpg", "width": 1280, "height": 720}
				]
			},
			{"id": "second", "title": "Second Result"}
		]
	}`}}
	results, err := searchYouTube(context.Background(), runner, "yt-dlp", " coffee documentary ", "", 1, nil, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one normalized result", results)
	}
	got := results[0]
	if got.ID != "abc-123" || got.Title != "Coffee Story" || got.Channel != "Example Channel" || got.URL != "https://www.youtube.com/watch?v=abc-123" || got.Thumbnail != "large.jpg" || got.DurationSeconds != 125.5 || got.AgeLimit != 18 {
		t.Fatalf("normalized result = %#v", got)
	}
	if !containsArgSequence(stringsJoin(runner.args), "ytsearch1:coffee documentary") {
		t.Fatalf("runner args = %v", runner.args)
	}
}

func TestSearchYouTubeValidatesQuery(t *testing.T) {
	if _, err := searchYouTube(context.Background(), &fixtureRunner{}, "yt-dlp", "", "", 10, nil, ""); err == nil {
		t.Fatal("expected empty query to fail")
	}
	if _, err := searchYouTube(context.Background(), &fixtureRunner{}, "yt-dlp", strings.Repeat("x", 301), "", 10, nil, ""); err == nil {
		t.Fatal("expected long query to fail")
	}
}

func TestParseExtraArgs(t *testing.T) {
	got := parseExtraArgs("  --js-runtimes node:/usr/bin/node   --remote-components ejs:github ")
	want := []string{"--js-runtimes", "node:/usr/bin/node", "--remote-components", "ejs:github"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("parse extra args = %#v, want %#v", got, want)
	}
}

func TestAudioFormatSelector(t *testing.T) {
	if got := audioFormatSelector(downloadRequest{Quality: "best"}); got != "ba/b[acodec!=none][height<=360]/b[acodec!=none]/b" {
		t.Fatalf("audio best selector = %q", got)
	}
	if got := audioFormatSelector(downloadRequest{Quality: "720p"}); got != "ba/b[acodec!=none][height<=720]/b[acodec!=none]/b" {
		t.Fatalf("audio 720p selector = %q", got)
	}
	if got := audioFormatSelector(downloadRequest{FormatID: "18", Quality: "best"}); got != "18" {
		t.Fatalf("audio format_id should win, got %q", got)
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
	got, err := findOutputFile(dir, []string{"__APTEVA_FILE__" + path})
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("got %q, want %q", got, path)
	}
}

func stringsJoin(args []string) string { return strings.Join(args, " ") }

func containsArgSequence(haystack, needle string) bool { return strings.Contains(haystack, needle) }
