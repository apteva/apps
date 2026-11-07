package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/nacl/box"
)

type githubSigningProvider struct{}

func (githubSigningProvider) Name() string { return buildBackendGitHubActions }
func (githubSigningProvider) SetupMatchesBuildConfig(cfg cloudBuildConfig, setup *MobileSigningSetup) bool {
	if setup == nil {
		return false
	}
	var previous struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Prefix string `json:"prefix"`
	}
	return json.Unmarshal([]byte(setup.ProviderConfigJSON), &previous) == nil && previous.Owner == cfg.Owner && previous.Repo == cfg.Repo && previous.Prefix != ""
}

// Each profile/key revision has its own secret names, so switching apps or
// repairing signing cannot overwrite the credentials of a queued workflow.
func (githubSigningProvider) ProvisionSigningSecrets(ctx context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, material mobileSigningSecrets) (*mobileSigningProviderResult, error) {
	if bound == nil {
		return nil, errors.New("GitHub signing requires a bound integration")
	}
	credentials, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(credentials.Fields["token"])
	if token == "" {
		return nil, errors.New("GitHub connection has no token")
	}
	base := "https://api.github.com/repos/" + url.PathEscape(cfg.Owner) + "/" + url.PathEscape(cfg.Repo) + "/actions/secrets/"
	values := signingSecretEnvironment(material)
	body, _ := json.Marshal(values)
	digest := sha256.Sum256(append([]byte(fmt.Sprintf("%s/%d/%d/", d.ProjectID, d.ID, d.EnvironmentID)), body...))
	prefix := fmt.Sprintf("APTEVA_%X_", digest[:12])
	if err := provisionGitHubSigningSecrets(ctx, http.DefaultClient, base, token, prefix, values); err != nil {
		return nil, err
	}
	config, _ := json.Marshal(map[string]string{"owner": cfg.Owner, "repo": cfg.Repo, "prefix": prefix})
	return &mobileSigningProviderResult{SecretRef: prefix, ConfigJSON: string(config)}, nil
}

func provisionGitHubSigningSecrets(ctx context.Context, client *http.Client, base, token, prefix string, values map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	call := func(method, path string, body []byte) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("GitHub signing-secret request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return nil, errors.New("read GitHub signing-secret response")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("GitHub signing secrets HTTP %d; the selected connection needs repository Actions secrets write permission", response.StatusCode)
		}
		return data, nil
	}
	raw, err := call(http.MethodGet, "public-key", nil)
	if err != nil {
		return err
	}
	var key struct {
		ID      string `json:"key_id"`
		Encoded string `json:"key"`
	}
	if json.Unmarshal(raw, &key) != nil || key.ID == "" {
		return errors.New("GitHub returned an invalid signing-secrets public key")
	}
	decoded, err := base64.StdEncoding.DecodeString(key.Encoded)
	if err != nil || len(decoded) != 32 {
		return errors.New("GitHub signing-secrets public key must contain 32 bytes")
	}
	var public [32]byte
	copy(public[:], decoded)
	for name, value := range values {
		if value == "" {
			continue
		}
		encrypted, err := box.SealAnonymous(nil, []byte(value), &public, rand.Reader)
		if err != nil {
			return errors.New("encrypt GitHub signing secret")
		}
		body, _ := json.Marshal(map[string]string{"key_id": key.ID, "encrypted_value": base64.StdEncoding.EncodeToString(encrypted)})
		if _, err := call(http.MethodPut, prefix+name, body); err != nil {
			return err
		}
	}
	return nil
}
