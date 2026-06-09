package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout func(string), stderr func(string)) error
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args []string, stdout func(string), stderr func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go scanPipe(&wg, outPipe, stdout)
	go scanPipe(&wg, errPipe, stderr)
	waitErr := cmd.Wait()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return waitErr
}

func scanPipe(wg *sync.WaitGroup, r io.Reader, fn func(string)) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		if fn != nil {
			fn(scanner.Text())
		}
	}
}

func buildProbeArgs(rawURL, cookieFile string) []string {
	args := []string{"--dump-single-json", "--no-playlist", "--no-warnings"}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	args = append(args, rawURL)
	return args
}

func buildDownloadArgs(req downloadRequest, jobDir, cookieFile string) []string {
	args := []string{
		"--newline",
		"--progress",
		"--print", "after_move:filepath",
		"--restrict-filenames",
		"-P", jobDir,
		"-o", "%(title).200B-%(id)s.%(ext)s",
	}
	if req.NoPlaylist {
		args = append(args, "--no-playlist")
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	if strings.TrimSpace(req.FFmpegLocation) != "" {
		args = append(args, "--ffmpeg-location", strings.TrimSpace(req.FFmpegLocation))
	}
	if req.Mode == "audio" {
		format := strings.TrimSpace(req.AudioFormat)
		if format == "" {
			format = "mp3"
		}
		args = append(args, "-x", "--audio-format", format)
	} else if selector := formatSelector(req); selector != "" {
		args = append(args, "-f", selector)
	}
	args = append(args, req.URL)
	return args
}

func formatSelector(req downloadRequest) string {
	if strings.TrimSpace(req.FormatID) != "" {
		return strings.TrimSpace(req.FormatID)
	}
	switch strings.ToLower(strings.TrimSpace(req.Quality)) {
	case "", "best":
		return "bv*+ba/b"
	case "1080p":
		return "bv*[height<=1080]+ba/b[height<=1080]"
	case "720p":
		return "bv*[height<=720]+ba/b[height<=720]"
	case "480p":
		return "bv*[height<=480]+ba/b[height<=480]"
	case "360p":
		return "bv*[height<=360]+ba/b[height<=360]"
	case "worst":
		return "wv*+wa/w"
	default:
		return strings.TrimSpace(req.Quality)
	}
}

var progressRE = regexp.MustCompile(`\[download\]\s+([0-9]+(?:\.[0-9]+)?)%`)

func parseProgressLine(line string) (float64, bool) {
	m := progressRE.FindStringSubmatch(line)
	if len(m) != 2 {
		return 0, false
	}
	var whole, frac int
	if strings.Contains(m[1], ".") {
		parts := strings.SplitN(m[1], ".", 2)
		fmt.Sscanf(parts[0], "%d", &whole)
		fmt.Sscanf(parts[1], "%d", &frac)
		div := 1
		for range parts[1] {
			div *= 10
		}
		return float64(whole) + float64(frac)/float64(div), true
	}
	fmt.Sscanf(m[1], "%d", &whole)
	return float64(whole), true
}

func probeMedia(ctx context.Context, runner commandRunner, ytdlpPath, rawURL, cookieFile string) (map[string]any, error) {
	var stdout strings.Builder
	var stderr strings.Builder
	err := runner.Run(ctx, ytdlpPath, buildProbeArgs(rawURL, cookieFile), func(line string) {
		stdout.WriteString(line)
		stdout.WriteByte('\n')
	}, func(line string) {
		stderr.WriteString(line)
		stderr.WriteByte('\n')
	})
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return nil, fmt.Errorf("parse yt-dlp metadata: %w", err)
	}
	return out, nil
}

func findOutputFile(jobDir string, printed []string) (string, error) {
	for i := len(printed) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(printed[i])
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(jobDir, candidate)
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	var files []string
	if err := filepath.WalkDir(jobDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) == "cookies.txt" {
			return err
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool {
		ai, _ := os.Stat(files[i])
		aj, _ := os.Stat(files[j])
		return ai.ModTime().After(aj.ModTime())
	})
	if len(files) == 0 {
		return "", errors.New("yt-dlp completed but no output file was found")
	}
	return files[0], nil
}
