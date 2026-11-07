package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func mobileProviderBuildLog(ctx context.Context, build *Build) (string, error) {
	cfg, err := parseCloudBuildConfig(build.BuildBackend, build.BuildBackendJSON)
	if err != nil {
		return "", err
	}
	bound, err := cloudIntegrationForConfig(build.BuildBackend, cfg)
	if err != nil {
		return "", err
	}
	if build.BuildBackend == buildBackendBitrise {
		raw, err := executeIntegration(bound, "get_build_log", map[string]any{"app_slug": cfg.AppID, "build_slug": build.ExternalJobID})
		if err != nil {
			return "", err
		}
		object, err := providerObject(raw)
		if err != nil {
			return "", err
		}
		var location string
		_ = json.Unmarshal(object["expiring_raw_log_url"], &location)
		if location != "" {
			text, err := fetchUnsignedProviderLog(ctx, location)
			if err == nil {
				return "\n--- Bitrise ---\n" + tailLogText(text, 2000), nil
			}
		}
		var chunks []struct {
			Chunk string `json:"chunk"`
		}
		if err := json.Unmarshal(object["log_chunks"], &chunks); err != nil {
			return "", err
		}
		var text strings.Builder
		for _, chunk := range chunks {
			text.WriteString(chunk.Chunk)
		}
		body := text.String()
		if len(body) > maxCodemagicLogBytes {
			body = body[len(body)-maxCodemagicLogBytes:]
		}
		return "\n--- Bitrise ---\n" + tailLogText(body, 2000), nil
	}
	job, err := parseAppcircleJob(build.ExternalJobID)
	if err != nil {
		return "", err
	}
	if job.BuildID == "" {
		return "\nAppcircle build is queued; step logs are not available yet.\n", nil
	}
	token, err := appcircleToken(ctx, bound)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appcircleDownloadURL(job, "/logs"), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many log redirects")
		}
		if len(via) > 0 && next.URL.Host != via[0].URL.Host {
			next.Header.Del("Authorization")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("Appcircle log request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("Appcircle logs unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCodemagicLogBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxCodemagicLogBytes {
		return "", errors.New("Appcircle log archive exceeds 2 MiB")
	}
	if bytes.HasPrefix(body, []byte("PK")) {
		z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return "", err
		}
		var text strings.Builder
		for _, f := range z.File {
			if !strings.HasSuffix(f.Name, ".log") && !strings.HasSuffix(f.Name, ".txt") {
				continue
			}
			reader, err := f.Open()
			if err != nil {
				return "", err
			}
			tail, err := readCodemagicLogTail(reader)
			_ = reader.Close()
			if err != nil {
				return "", err
			}
			text.WriteString("\n--- Appcircle: " + f.Name + " ---\n")
			text.Write(tail)
			if text.Len() > maxCodemagicLogBytes {
				return tailLogText(text.String(), 2000), nil
			}
		}
		return tailLogText(text.String(), 2000), nil
	}
	return "\n--- Appcircle ---\n" + tailLogText(string(body), 2000), nil
}

func fetchUnsignedProviderLog(ctx context.Context, location string) (string, error) {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", errors.New("invalid provider log URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("invalid provider log redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("provider log request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("provider log unavailable")
	}
	body, err := readCodemagicLogTail(resp.Body)
	return string(body), err
}
