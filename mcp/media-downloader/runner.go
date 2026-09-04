package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout func(string), stderr func(string)) error
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args []string, stdout func(string), stderr func(string)) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
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
	scanErrs := make(chan error, 2)
	wg.Add(2)
	go scanPipe(&wg, outPipe, stdout, scanErrs, cancel)
	go scanPipe(&wg, errPipe, stderr, scanErrs, cancel)
	waitErr := cmd.Wait()
	wg.Wait()
	close(scanErrs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for scanErr := range scanErrs {
		if scanErr != nil {
			return fmt.Errorf("read yt-dlp output: %w", scanErr)
		}
	}
	return waitErr
}

func scanPipe(wg *sync.WaitGroup, r io.Reader, fn func(string), errs chan<- error, cancel context.CancelFunc) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)
	for scanner.Scan() {
		if fn != nil {
			fn(scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		errs <- err
		cancel()
	}
}

func buildProbeArgs(rawURL, cookieFile string, extraArgs []string, proxyURL string) []string {
	args := []string{"--dump-single-json", "--no-playlist", "--no-warnings"}
	args = append(args, extraArgs...)
	if proxyURL != "" {
		args = append(args, "--proxy", proxyURL)
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	args = append(args, rawURL)
	return args
}

func buildSearchArgs(query string, limit int, cookieFile string, extraArgs []string, proxyURL string) []string {
	limit = searchLimit(limit)
	args := []string{
		"--flat-playlist",
		"--dump-single-json",
		"--no-warnings",
		"--playlist-end", strconv.Itoa(limit),
	}
	args = append(args, extraArgs...)
	if proxyURL != "" {
		args = append(args, "--proxy", proxyURL)
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	args = append(args, fmt.Sprintf("ytsearch%d:%s", limit, strings.TrimSpace(query)))
	return args
}

func searchLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 25 {
		return 25
	}
	return limit
}

func buildDownloadArgs(req downloadRequest, jobDir, cookieFile string) []string {
	args := []string{
		"--newline",
		"--progress",
		"--print", "before_dl:__APTEVA_META__%(title).200B|%(extractor)s",
		"--print", "after_move:__APTEVA_FILE__%(filepath)s",
		"--restrict-filenames",
		"-P", jobDir,
		"-o", "%(title).200B-%(id)s.%(ext)s",
	}
	args = append(args, req.YTDLPExtraArgs...)
	if req.ProxyURL != "" {
		args = append(args, "--proxy", req.ProxyURL)
	}
	if req.MaxDownloadBytes > 0 {
		args = append(args, "--max-filesize", strconv.FormatInt(req.MaxDownloadBytes, 10))
	}
	args = append(args, "--no-playlist")
	if req.Ingest {
		args = append(args, "--write-thumbnail")
		if len(req.CaptionTracks) > 0 {
			manual, automatic := false, false
			languages := make([]string, 0, len(req.CaptionTracks))
			for _, track := range req.CaptionTracks {
				languages = append(languages, track.Language)
				manual = manual || track.Source == "manual"
				automatic = automatic || track.Source == "automatic"
			}
			if manual {
				args = append(args, "--write-subs")
			}
			if automatic {
				args = append(args, "--write-auto-subs")
			}
			args = append(args, "--sub-langs", strings.Join(languages, ","), "--sub-format", "vtt/best")
		}
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	if strings.TrimSpace(req.FFmpegLocation) != "" {
		args = append(args, "--ffmpeg-location", strings.TrimSpace(req.FFmpegLocation))
	}
	if cookieFile == "" && strings.TrimSpace(req.YoutubePlayer) != "" && isYouTubeURL(req.URL) {
		args = append(args, "--extractor-args", "youtube:player_client="+strings.TrimSpace(req.YoutubePlayer))
	}
	if req.Mode == "audio" {
		if selector := audioFormatSelector(req); selector != "" {
			args = append(args, "-f", selector)
		}
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

func audioFormatSelector(req downloadRequest) string {
	if strings.TrimSpace(req.FormatID) != "" {
		return strings.TrimSpace(req.FormatID)
	}
	switch strings.ToLower(strings.TrimSpace(req.Quality)) {
	case "", "best":
		return "ba/b[acodec!=none][height<=360]/b[acodec!=none]/b"
	case "1080p":
		return "ba/b[acodec!=none][height<=1080]/b[acodec!=none]/b"
	case "720p":
		return "ba/b[acodec!=none][height<=720]/b[acodec!=none]/b"
	case "480p":
		return "ba/b[acodec!=none][height<=480]/b[acodec!=none]/b"
	case "360p":
		return "ba/b[acodec!=none][height<=360]/b[acodec!=none]/b"
	case "worst":
		return "wa/w[acodec!=none]/worst[acodec!=none]/w"
	default:
		return strings.TrimSpace(req.Quality)
	}
}

func isYouTubeURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.Trim(u.Hostname(), "."))
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be"
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

func parseExtraArgs(raw string) []string {
	return strings.Fields(strings.TrimSpace(raw))
}

func probeMedia(ctx context.Context, runner commandRunner, ytdlpPath, rawURL, cookieFile string, extraArgs []string, proxyURL string) (map[string]any, error) {
	var stdout strings.Builder
	var stderr strings.Builder
	err := runner.Run(ctx, ytdlpPath, buildProbeArgs(rawURL, cookieFile, extraArgs, proxyURL), func(line string) {
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

func searchYouTube(ctx context.Context, runner commandRunner, ytdlpPath, query, cookieFile string, limit int, extraArgs []string, proxyURL string) ([]mediaSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if utf8.RuneCountInString(query) > 300 {
		return nil, errors.New("query must be 300 characters or fewer")
	}
	limit = searchLimit(limit)
	var stdout strings.Builder
	var stderr strings.Builder
	err := runner.Run(ctx, ytdlpPath, buildSearchArgs(query, limit, cookieFile, extraArgs, proxyURL), func(line string) {
		stdout.WriteString(line)
		stdout.WriteByte('\n')
	}, func(line string) {
		stderr.WriteString(line)
		stderr.WriteByte('\n')
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	var envelope struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		return nil, fmt.Errorf("parse yt-dlp search results: %w", err)
	}
	results := make([]mediaSearchResult, 0, len(envelope.Entries))
	for _, entry := range envelope.Entries {
		id := mapString(entry, "id")
		title := mapString(entry, "title")
		if id == "" || title == "" {
			continue
		}
		channel := mapString(entry, "channel")
		if channel == "" {
			channel = mapString(entry, "uploader")
		}
		results = append(results, mediaSearchResult{
			ID:              id,
			Title:           title,
			URL:             "https://www.youtube.com/watch?v=" + url.QueryEscape(id),
			Channel:         channel,
			DurationSeconds: mapFloat(entry, "duration"),
			Thumbnail:       bestThumbnail(entry),
			AgeLimit:        int(mapFloat(entry, "age_limit")),
			LiveStatus:      mapString(entry, "live_status"),
			UploadDate:      mapString(entry, "upload_date"),
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func mapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mapFloat(values map[string]any, key string) float64 {
	switch value := values[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case json.Number:
		number, _ := value.Float64()
		return number
	}
	return 0
}

func bestThumbnail(entry map[string]any) string {
	thumbnails, _ := entry["thumbnails"].([]any)
	bestURL := mapString(entry, "thumbnail")
	bestArea := float64(0)
	for _, raw := range thumbnails {
		thumbnail, _ := raw.(map[string]any)
		candidate := mapString(thumbnail, "url")
		if candidate == "" {
			continue
		}
		area := mapFloat(thumbnail, "width") * mapFloat(thumbnail, "height")
		if area >= bestArea {
			bestURL = candidate
			bestArea = area
		}
	}
	return bestURL
}

func findOutputFile(jobDir string, printed []string) (string, error) {
	for i := len(printed) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(printed[i])
		candidate = strings.TrimPrefix(candidate, "__APTEVA_FILE__")
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
