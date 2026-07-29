package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntegrationPublicYouTubeTransfer(t *testing.T) {
	if os.Getenv("RUN_MEDIA_DOWNLOAD_TESTS") != "1" {
		t.Skip("set RUN_MEDIA_DOWNLOAD_TESTS=1 to run a real yt-dlp transfer")
	}
	ytdlp, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := startSafeProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := downloadRequest{
		URL:              "https://www.youtube.com/watch?v=EUUhYw9xsCE",
		Mode:             "video",
		Quality:          "720p",
		FFmpegLocation:   ffmpeg,
		YoutubePlayer:    "android",
		ProxyURL:         proxy.url,
		MaxDownloadBytes: 512 * 1024 * 1024,
		YTDLPExtraArgs:   []string{"--download-sections", "*00:00-00:00:03", "--force-keyframes-at-cuts"},
	}
	var printed []string
	var stderr []string
	var mu sync.Mutex
	err = (osCommandRunner{}).Run(ctx, ytdlp, buildDownloadArgs(req, t.TempDir(), ""), func(line string) {
		if strings.HasPrefix(line, "__APTEVA_FILE__") {
			mu.Lock()
			printed = append(printed, line)
			mu.Unlock()
		}
	}, func(line string) {
		mu.Lock()
		stderr = append(stderr, line)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("yt-dlp transfer failed: %v\n%s", err, strings.Join(stderr, "\n"))
	}
	if len(printed) != 1 {
		t.Fatalf("output markers = %v, want one", printed)
	}
}

func TestIntegrationYouTubeSearch(t *testing.T) {
	if os.Getenv("RUN_MEDIA_DOWNLOAD_TESTS") != "1" {
		t.Skip("set RUN_MEDIA_DOWNLOAD_TESTS=1 to run a real yt-dlp search")
	}
	ytdlp, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := startSafeProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := searchYouTube(ctx, osCommandRunner{}, ytdlp, "coffee documentary", "", 2, nil, proxy.url)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("search returned %d results, want 2", len(results))
	}
	for _, result := range results {
		if result.ID == "" || result.Title == "" || !strings.HasPrefix(result.URL, "https://www.youtube.com/watch?v=") {
			t.Fatalf("invalid search result: %#v", result)
		}
	}
}
