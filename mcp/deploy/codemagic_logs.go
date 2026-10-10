package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCodemagicLogBytes = 2 << 20

// The v3 actions API has step metadata only. The build-details API includes
// authenticated log URLs, including script output nested under subactions.
func fetchCodemagicLogs(ctx context.Context, client *http.Client, token, jobID string, tail int) (string, error) {
	if token == "" {
		return "", errors.New("Codemagic connection has no token")
	}
	if jobID == "" || strings.Trim(jobID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return "", errors.New("invalid Codemagic job id")
	}
	get := func(location string) ([]byte, error) {
		u, err := url.Parse(location)
		if err != nil {
			return nil, errors.New("invalid Codemagic log URL")
		}
		if !u.IsAbs() {
			u, err = url.Parse("https://api.codemagic.io" + location)
		}
		prefix := "/builds/" + jobID
		if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "api.codemagic.io" && u.Host != "codemagic.io") || (u.Path != prefix && !strings.HasPrefix(u.Path, prefix+"/step/")) {
			return nil, errors.New("Codemagic returned an unexpected log URL")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-auth-token", token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("Codemagic log request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Codemagic log request returned HTTP %d", resp.StatusCode)
		}
		if u.Path != prefix {
			return readCodemagicLogTail(resp.Body)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCodemagicLogBytes+1))
		if err != nil {
			return nil, err
		}
		if len(body) > maxCodemagicLogBytes {
			return nil, errors.New("Codemagic log response exceeds 2 MiB")
		}
		return body, nil
	}
	raw, err := get("/builds/" + jobID)
	if err != nil {
		return "", err
	}
	type action struct {
		Name       string `json:"name"`
		LogURL     string `json:"logUrl"`
		Subactions []struct {
			LogURL string `json:"logUrl"`
		} `json:"subactions"`
	}
	var details struct {
		Build struct {
			Actions []action `json:"buildActions"`
		} `json:"build"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		return "", errors.New("Codemagic returned invalid build details")
	}
	var out strings.Builder
	seen := map[string]bool{}
	requests := 0
	var firstErr error
	for _, step := range details.Build.Actions {
		locations := []string{step.LogURL}
		for _, sub := range step.Subactions {
			locations = append(locations, sub.LogURL)
		}
		for _, location := range locations {
			if location == "" || seen[location] {
				continue
			}
			seen[location] = true
			requests++
			if requests > 32 {
				firstErr = errors.New("Codemagic log request limit reached")
				break
			}
			body, err := get(location)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			fmt.Fprintf(&out, "\n--- Codemagic: %s ---\n%s\n", step.Name, tailLogText(string(body), tail))
		}
	}
	// Never include the provider token in returned output, even if a script echoes it.
	log := strings.ReplaceAll(out.String(), token, "[REDACTED]")
	if firstErr != nil {
		log += "\nProvider logs incomplete: " + firstErr.Error() + "\n"
	}
	if log == "" {
		return "", errors.New("Codemagic step logs are not available yet")
	}
	return tailLogText(log, tail), nil
}

// Preserve the end of long compiler logs without buffering the whole response.
func readCodemagicLogTail(reader io.Reader) ([]byte, error) {
	var suffix []byte
	buffer := make([]byte, 32<<10)
	truncated := false
	for {
		n, err := reader.Read(buffer)
		suffix = append(suffix, buffer[:n]...)
		if len(suffix) > maxCodemagicLogBytes {
			suffix = suffix[len(suffix)-maxCodemagicLogBytes:]
			truncated = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("Codemagic log response could not be read")
		}
	}
	if truncated {
		if newline := strings.IndexByte(string(suffix), '\n'); newline >= 0 {
			suffix = suffix[newline+1:]
		}
		suffix = append([]byte("[Earlier provider log output omitted]\n"), suffix...)
	}
	return suffix, nil
}

func tailLogText(body string, tail int) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return strings.Join(lines, "\n") + "\n"
}

func (a *App) buildLog(ctx context.Context, build *Build, tail int) (string, error) {
	local, localErr := tailFile(build.LogPath, tail)
	backend := normalizeBuildBackend(build.BuildBackend)
	if (backend != buildBackendCodemagic && backend != buildBackendBitrise && backend != buildBackendAppcircle) || build.ExternalJobID == "" {
		return local, localErr
	}
	// Share a brief disk cache across MCP and dashboard requests to avoid hitting
	// Codemagic once for every UI poll. Log paths already belong to scoped builds.
	cache := build.LogPath + "." + backend
	if info, err := os.Stat(cache); build.LogPath != "" && err == nil && time.Since(info.ModTime()) < 15*time.Second {
		if body, err := os.ReadFile(cache); err == nil {
			return tailLogText(local+string(body), tail), nil
		}
	}
	var provider string
	var err error
	if backend == buildBackendCodemagic {
		provider, err = a.codemagicBuildLog(ctx, build)
	} else {
		provider, err = mobileProviderBuildLog(ctx, build)
	}
	if err != nil {
		return local + "\nProvider logs unavailable: " + err.Error() + "\n", nil
	}
	if build.LogPath == "" {
		return tailLogText(local+provider, tail), nil
	}
	if tmp, err := os.CreateTemp(filepath.Dir(cache), ".codemagic-log-*"); err == nil {
		if _, err := tmp.WriteString(provider); err == nil {
			_ = tmp.Close()
			_ = os.Rename(tmp.Name(), cache)
		} else {
			_ = tmp.Close()
		}
		_ = os.Remove(tmp.Name())
	}
	return tailLogText(local+provider, tail), nil
}

func (a *App) codemagicBuildLog(ctx context.Context, build *Build) (string, error) {
	cfg, err := parseCloudBuildConfig(build.BuildBackend, build.BuildBackendJSON)
	if err != nil {
		return "", err
	}
	bound, err := cloudIntegrationForConfig(buildBackendCodemagic, cfg)
	if err != nil {
		return "", err
	}
	creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil || creds == nil {
		return "", errors.New("Codemagic credentials unavailable")
	}
	token := strings.TrimSpace(creds.Fields["token"])
	if token == "" {
		return "", errors.New("Codemagic connection has no token")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchCodemagicLogs(ctx, client, token, build.ExternalJobID, 2000)
}
