package main

import (
	"net/url"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestValidateGitRemoteURL(t *testing.T) {
	for _, raw := range []string{
		"file:///tmp/repo",
		"git://github.com/acme/repo.git",
		"https://token@github.com/acme/repo.git",
		"https://github.com/acme/repo.git?token=secret",
	} {
		if _, err := validateGitRemoteURL(raw); err == nil {
			t.Errorf("validateGitRemoteURL(%q) unexpectedly succeeded", raw)
		}
	}
	got, err := validateGitRemoteURL("https://github.com/acme/repo.git/")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "https://github.com/acme/repo.git" {
		t.Fatalf("normalized URL = %q", got.String())
	}
}

func TestAdaptGitCredentialsScopesProviderHost(t *testing.T) {
	remote, _ := url.Parse("https://github.com/acme/repo.git")
	auth, err := adaptGitCredentials(remote, &sdk.ConnectionCredentials{
		ConnectionID: 7,
		Slug:         "github",
		Fields:       map[string]string{"token": "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Username != "x-access-token" || auth.Password != "secret" || auth.ConnectionID != 7 {
		t.Fatalf("unexpected auth: %+v", auth)
	}
	evil, _ := url.Parse("https://example.com/acme/repo.git")
	if _, err := adaptGitCredentials(evil, &sdk.ConnectionCredentials{Slug: "github", Fields: map[string]string{"token": "secret"}}); err == nil {
		t.Fatal("GitHub credential accepted for another host")
	}
}

func TestGitAskPassDoesNotPersistSecret(t *testing.T) {
	auth := &gitAuth{Username: "user", Password: "very-secret"}
	helper, err := newGitAskPass(t.TempDir(), auth)
	if err != nil {
		t.Fatal(err)
	}
	defer helper.close()
	body, err := osReadFile(helper.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), auth.Password) {
		t.Fatal("askpass helper persisted the credential")
	}
}

var osReadFile = func(path string) ([]byte, error) {
	return os.ReadFile(path)
}
