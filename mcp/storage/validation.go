package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

func validateFolder(raw string) (string, error) {
	if len(raw) > 4096 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", errors.New("invalid folder")
	}
	for _, segment := range strings.Split(strings.TrimSpace(raw), "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("folder cannot contain . or .. segments")
		}
	}
	return normaliseFolder(raw), nil
}

func validateFilename(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("name required")
	}
	if len(raw) > 200 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n") {
		return "", errors.New("name must be valid text of at most 200 bytes")
	}
	return normaliseFilename(raw), nil
}

func validateVisibility(raw string) error {
	if raw != "" && visibilityOrDefault(raw) == "" {
		return errors.New("visibility must be one of: private, signed, public")
	}
	return nil
}

func authorizeFolder(c context.Context, permission, raw string) (string, error) {
	folder, err := validateFolder(raw)
	if err != nil {
		return "", err
	}
	if caller := sdk.CallerFrom(c); caller != nil && !caller.Allows(permission, fileResource(folder)) {
		return "", sdk.Forbidden(permission, fileResource(folder))
	}
	return folder, nil
}

func decodeJSON(r *http.Request, out any, limit int64, optional bool) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("JSON request too large")
	}
	if len(strings.TrimSpace(string(data))) == 0 && optional {
		return nil
	}
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("JSON object required")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

type actorContextKey struct{}

func requestActor(r *http.Request) string {
	if user := r.Header.Get("X-User-ID"); user != "" {
		return "human:" + user
	}
	if install := r.Header.Get("X-Apteva-Bound-Caller-Install-ID"); install != "" {
		return "app:" + install
	}
	return "app"
}
func actorFrom(c context.Context) string {
	if v, ok := c.Value(actorContextKey{}).(string); ok {
		return v
	}
	if caller := sdk.CallerFrom(c); caller != nil {
		if caller.AgentID > 0 {
			return fmt.Sprintf("agent:%d", caller.AgentID)
		}
		if caller.AppInstallID > 0 {
			return fmt.Sprintf("app:%d", caller.AppInstallID)
		}
	}
	return "agent"
}

// Process-wide admission bounds full-file/base64 processing and backend streams.
var transferSlots = make(chan struct{}, 4)

func acquireTransfer(c context.Context) (func(), error) {
	select {
	case transferSlots <- struct{}{}:
		return func() { <-transferSlots }, nil
	case <-c.Done():
		return nil, c.Err()
	}
}
