package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

type actorBrowserSecret struct {
	Value     string `json:"value"`
	ContextID string `json:"context_id"`
	Host      string `json:"host"`
	ExpiresAt string `json:"expires_at"`
}

func resolveActorBrowserSecret(raw, project, ref, contextID, target string, now time.Time) (string, error) {
	var projects map[string]map[string]actorBrowserSecret
	if json.Unmarshal([]byte(raw), &projects) != nil {
		return "", errors.New("browser secret configuration is unavailable")
	}
	secret, ok := projects[project][ref]
	if !ok || secret.Value == "" {
		return "", errors.New("browser secret reference is unavailable for this project")
	}
	expiry, err := time.Parse(time.RFC3339, secret.ExpiresAt)
	if err != nil || !now.Before(expiry) {
		return "", errors.New("browser secret reference has expired or has no valid expiry")
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != u.Hostname() || secret.Host == "" || !strings.EqualFold(u.Hostname(), secret.Host) || secret.ContextID == "" || secret.ContextID != contextID {
		return "", errors.New("browser secret is not bound to this context and HTTPS host")
	}
	return secret.Value, nil
}

func (e *actorExecution) fillSecret(step actorStep) error {
	if e.session == nil || e.session.AppContextID != e.definition.Browser.ContextID {
		return errors.New("secret fill requires the exact saved Computer context")
	}
	value, err := resolveActorBrowserSecret(configString(e.ctx, "browser_secrets"), projectID(e.ctx), step.SecretRef, e.definition.Browser.ContextID, e.currentURL, time.Now())
	if err != nil {
		return err
	}
	// Keep the definition, input, trace and error snapshots reference-only.
	e.secretInputUsed = true
	step.Action = "fill"
	step.Text = value
	step.SecretRef = ""
	if err = e.interact(step); err != nil {
		return errors.New("secret-backed browser fill failed; underlying text-bearing diagnostics suppressed")
	}
	return nil
}

// Verification links are bearer credentials too. Never persist their URL while
// navigation is in flight; retain only the resulting origin; tokens may also occur in URL paths.
func (e *actorExecution) gotoSecret(step actorStep) error {
	if e.session == nil || e.session.AppContextID != e.definition.Browser.ContextID {
		return errors.New("secret navigation requires the exact saved Computer context")
	}
	value, err := resolveActorBrowserSecret(configString(e.ctx, "browser_secrets"), projectID(e.ctx), step.SecretRef, e.definition.Browser.ContextID, e.currentURL, time.Now())
	if err != nil {
		return err
	}
	current, _ := url.Parse(e.currentURL)
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || target.User != nil || target.Host != current.Host || !hostAllowed(value, e.definition.AllowedHosts) {
		return errors.New("secret navigation target must remain on the bound HTTPS host")
	}
	e.secretInputUsed = true
	e.secretNavigation = true
	err = e.gotoURL(value)
	e.currentURL = withoutSecretURLParameters(e.currentURL)
	if e.session != nil {
		e.session.CurrentURL = withoutSecretURLParameters(e.session.CurrentURL)
	}
	e.secretNavigation = false
	if err != nil {
		return errors.New("secret-backed browser navigation failed; underlying URL-bearing diagnostics suppressed")
	}
	return e.persistProgress("secret navigation completed")
}

func withoutSecretURLParameters(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}
