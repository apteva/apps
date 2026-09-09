package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed helper-assets.json
var helperAssetsJSON []byte

const maxHelperBytes = 16 << 20

var helperCache sync.Map

// Tests supply a locally built native helper; production always checks the
// immutable checksum embedded in this Backup release before installing bytes.
var helperArtifact = downloadHelperArtifact

func downloadHelperArtifact(platform string) ([]byte, string, error) {
	if cached, ok := helperCache.Load(platform); ok {
		v := cached.(helperBlob)
		return v.Body, v.SHA256, nil
	}
	var assets map[string]struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	if e := json.Unmarshal(helperAssetsJSON, &assets); e != nil {
		return nil, "", e
	}
	asset, ok := assets[platform]
	if !ok {
		return nil, "", fmt.Errorf("unsupported instance platform %s", platform)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if e != nil {
		return nil, "", e
	}
	client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("invalid helper download redirect")
		}
		return nil
	}}
	resp, e := client.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("helper download HTTP %d", resp.StatusCode)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, maxHelperBytes+1))
	if e != nil {
		return nil, "", e
	}
	if len(body) == 0 || len(body) > maxHelperBytes {
		return nil, "", errors.New("invalid helper binary size")
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != asset.SHA256 {
		return nil, "", errors.New("helper checksum mismatch")
	}
	helperCache.Store(platform, helperBlob{body, asset.SHA256})
	return body, asset.SHA256, nil
}

type helperBlob struct {
	Body   []byte
	SHA256 string
}

func ensureInstanceHelper(ctx *sdk.AppCtx, id int64) (string, error) {
	output, e := instanceCommand(ctx, id, "uname -s; uname -m")
	if e != nil {
		return "", e
	}
	parts := strings.Fields(output)
	if len(parts) != 2 {
		return "", errors.New("unable to determine instance OS and architecture")
	}
	system := strings.ToLower(parts[0])
	arch := parts[1]
	switch arch {
	case "aarch64", "arm64":
		arch = "arm64"
	case "x86_64", "amd64":
		arch = "amd64"
	default:
		return "", fmt.Errorf("unsupported instance architecture %s", arch)
	}
	if system != "darwin" && system != "linux" {
		return "", fmt.Errorf("unsupported host %s: only macOS and Linux folder backups are supported", system)
	}
	body, digest, e := helperArtifact(system + "-" + arch)
	if e != nil {
		return "", e
	}
	// mktemp allocates an owned, private path. The immutable binary is reused
	// within this process; a fresh installation never trusts a remote cache.
	_, identity, e := getBackupInstance(ctx, id)
	if e != nil {
		return "", e
	}
	installedKey := identity + "/" + digest
	if value, ok := installedHelpers.Load(installedKey); ok {
		if _, e := instanceCommand(ctx, id, "test -x "+shellQuote(value.(string))); e == nil {
			return value.(string), nil
		}
		installedHelpers.Delete(installedKey)
	}
	temp, e := instanceCommand(ctx, id, "umask 077; mktemp -d /tmp/apteva-backup-helper.XXXXXXXX")
	if e != nil {
		return "", e
	}
	dir := strings.TrimSpace(temp)
	if !strings.HasPrefix(dir, "/tmp/apteva-backup-helper.") || strings.ContainsAny(strings.TrimPrefix(dir, "/tmp/apteva-backup-helper."), "/\n\r\t ") {
		return "", errors.New("invalid remote helper directory")
	}
	file := dir + "/backup-helper"
	var result struct {
		Bytes int `json:"bytes_written"`
	}
	if e = callInstances(ctx, "instance_upload_file", map[string]any{"id": id, "path": file, "content_b64": base64.StdEncoding.EncodeToString(body)}, &result); e != nil {
		return "", e
	}
	if result.Bytes != len(body) {
		return "", errors.New("incomplete helper upload")
	}
	// Verification is done again on the host before execution. These hash tools
	// ship with the supported Linux/macOS operating systems.
	checksum := "sha256sum"
	if system == "darwin" {
		checksum = "shasum -a 256"
	}
	command := "set -e; test \"$(" + checksum + " " + shellQuote(file) + " | cut -d ' ' -f 1)\" = " + shellQuote(digest) + "; chmod 700 " + shellQuote(file)
	if _, e = instanceCommand(ctx, id, command); e != nil {
		return "", e
	}
	installedHelpers.Store(installedKey, file)
	return file, nil
}

var installedHelpers sync.Map
