package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

const oauthDonePath = "/api/apps/social/accounts/oauth_done"

func newOAuthCallbackToken() (string, string, error) {
	token, err := callbackNonce()
	if err != nil {
		return "", "", err
	}
	return token, oauthCallbackTokenHash(token), nil
}

func oauthCallbackTokenHash(token string) string {
	if len(token) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(token); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func oauthCallbackTokenMatches(storedHash, token string) bool {
	hash := oauthCallbackTokenHash(token)
	return hash != "" && storedHash != "" && subtle.ConstantTimeCompare([]byte(storedHash), []byte(hash)) == 1
}

// An OAuth callback always lands on Social's token-validated handoff route.
// The former arbitrary same-origin return_to could disclose the callback token
// to a different handler and could not be opened anonymously by the gateway.
func socialOAuthReturnURL(projectID, returnTo string) (string, error) {
	if raw := strings.TrimSpace(returnTo); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.IsAbs() || u.Host != "" || u.Path != oauthDonePath || u.Fragment != "" || u.RawFragment != "" {
			return "", errors.New("return_to must be the Social OAuth callback path")
		}
		q := u.Query()
		for key := range q {
			if key != "project_id" {
				return "", errors.New("return_to may only include project_id")
			}
		}
		if q.Has("project_id") && q.Get("project_id") != projectID {
			return "", errors.New("return_to project_id does not match the active project")
		}
	}
	return oauthDonePath + "?project_id=" + url.QueryEscape(projectID), nil
}
