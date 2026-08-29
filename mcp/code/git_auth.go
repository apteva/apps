package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type gitAuth struct {
	ProviderSlug string
	ConnectionID int64
	Username     string
	Password     string
}

func validateGitRemoteURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("remote_url required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid remote_url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, errors.New("only https Git remotes are supported")
	}
	if u.Hostname() == "" {
		return nil, errors.New("remote_url host required")
	}
	if u.User != nil {
		return nil, errors.New("remote_url must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("remote_url must not contain a query or fragment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if u.Path == "" {
		return nil, errors.New("remote_url repository path required")
	}
	return u, nil
}

func boundGitIntegrations(ctx *sdk.AppCtx) []*sdk.BoundIntegration {
	if ctx == nil {
		return nil
	}
	out := []*sdk.BoundIntegration{}
	seen := map[int64]bool{}
	for _, role := range []string{"git", "github"} {
		for _, bound := range ctx.IntegrationsFor(role) {
			if bound == nil || bound.ConnectionID <= 0 || seen[bound.ConnectionID] {
				continue
			}
			seen[bound.ConnectionID] = true
			out = append(out, bound)
		}
	}
	return out
}

func boundGitIntegrationForSlug(ctx *sdk.AppCtx, slug string) *sdk.BoundIntegration {
	if strings.EqualFold(slug, "github") && ctx != nil {
		if legacy := ctx.IntegrationFor("github"); legacy != nil &&
			(legacy.AppSlug == "" || strings.EqualFold(legacy.AppSlug, slug)) {
			return legacy
		}
	}
	for _, bound := range boundGitIntegrations(ctx) {
		if bound != nil && strings.EqualFold(bound.AppSlug, slug) {
			return bound
		}
	}
	return nil
}

func gitAuthForRemote(ctx *sdk.AppCtx, remoteURL string, requestedConnectionID int64) (*gitAuth, error) {
	u, err := validateGitRemoteURL(remoteURL)
	if err != nil {
		return nil, err
	}
	bindings := boundGitIntegrations(ctx)
	var candidates []*sdk.BoundIntegration
	for _, bound := range bindings {
		if requestedConnectionID > 0 && bound.ConnectionID != requestedConnectionID {
			continue
		}
		if requestedConnectionID == 0 && !providerCouldServeHost(bound.AppSlug, u.Hostname()) {
			continue
		}
		candidates = append(candidates, bound)
	}
	if requestedConnectionID > 0 && len(candidates) == 0 {
		return nil, errors.New("connection is not bound to this Code install")
	}
	if len(candidates) == 0 {
		// Anonymous access is valid for public HTTPS remotes.
		return &gitAuth{}, nil
	}

	var errs []string
	for _, bound := range candidates {
		creds, credErr := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
		if credErr != nil {
			errs = append(errs, credErr.Error())
			continue
		}
		auth, authErr := adaptGitCredentials(u, creds)
		if authErr != nil {
			errs = append(errs, authErr.Error())
			continue
		}
		return auth, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("no compatible Git connection")
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

func providerCouldServeHost(slug, host string) bool {
	switch strings.ToLower(slug) {
	case "github":
		return strings.EqualFold(host, "github.com")
	case "bitbucket":
		return strings.EqualFold(host, "bitbucket.org")
	case "gitlab":
		// A GitLab connection may point at a self-hosted instance. The
		// credential adapter validates the exact configured origin.
		return true
	default:
		return false
	}
}

func adaptGitCredentials(remote *url.URL, creds *sdk.ConnectionCredentials) (*gitAuth, error) {
	if creds == nil {
		return nil, errors.New("empty connection credentials")
	}
	fields := creds.Fields
	auth := &gitAuth{ProviderSlug: creds.Slug, ConnectionID: creds.ConnectionID}
	switch strings.ToLower(creds.Slug) {
	case "github":
		if !strings.EqualFold(remote.Hostname(), "github.com") {
			return nil, errors.New("GitHub credentials may only be used with github.com")
		}
		auth.Username = "x-access-token"
		auth.Password = firstNonEmpty(fields["token"], fields["access_token"])
	case "gitlab":
		instance := firstNonEmpty(fields["instanceUrl"], fields["instance_url"], "https://gitlab.com")
		instanceURL, err := url.Parse(instance)
		if err != nil || instanceURL.Hostname() == "" {
			return nil, errors.New("GitLab connection has an invalid instance URL")
		}
		if !strings.EqualFold(remote.Hostname(), instanceURL.Hostname()) || remote.Port() != instanceURL.Port() {
			return nil, fmt.Errorf("GitLab credentials may only be used with %s", instanceURL.Host)
		}
		auth.Username = firstNonEmpty(fields["username"], "oauth2")
		auth.Password = firstNonEmpty(fields["accessToken"], fields["access_token"], fields["token"])
	case "bitbucket":
		if !strings.EqualFold(remote.Hostname(), "bitbucket.org") {
			return nil, errors.New("Bitbucket credentials may only be used with bitbucket.org")
		}
		auth.Username = fields["username"]
		auth.Password = firstNonEmpty(fields["appPassword"], fields["app_password"], fields["token"])
	default:
		return nil, fmt.Errorf("connection %q does not have a Git credential adapter", creds.Slug)
	}
	if auth.Username == "" || auth.Password == "" {
		return nil, fmt.Errorf("%s connection is missing Git credentials", creds.Slug)
	}
	return auth, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type gitAskPass struct {
	path string
	auth *gitAuth
}

func newGitAskPass(baseDir string, auth *gitAuth) (*gitAskPass, error) {
	if auth == nil || auth.Password == "" {
		return &gitAskPass{auth: &gitAuth{}}, nil
	}
	dir, err := os.MkdirTemp(baseDir, "git-auth-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	path := filepath.Join(dir, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n  *sername*) printf '%s\\n' \"$APTEVA_GIT_AUTH_USER\" ;;\n  *assword*) printf '%s\\n' \"$APTEVA_GIT_AUTH_PASSWORD\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &gitAskPass{path: path, auth: auth}, nil
}

func (a *gitAskPass) env() []string {
	if a == nil || a.path == "" {
		return []string{"GIT_TERMINAL_PROMPT=0"}
	}
	return []string{
		"GIT_ASKPASS=" + a.path,
		"GIT_TERMINAL_PROMPT=0",
		"APTEVA_GIT_AUTH_USER=" + a.auth.Username,
		"APTEVA_GIT_AUTH_PASSWORD=" + a.auth.Password,
	}
}

func (a *gitAskPass) close() {
	if a != nil && a.path != "" {
		_ = os.RemoveAll(filepath.Dir(a.path))
	}
}
