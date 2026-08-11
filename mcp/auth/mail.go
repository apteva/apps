package main

// mail.go — composes with the messaging app via PlatformAPI to send
// transactional email (verify, reset, magic-link, invite). When the
// messaging app isn't installed, links are written to the audit log
// only — a development escape hatch.
//
// v0.4.0: every link is org-prefixed. The `kind=verify_email`,
// `reset_password`, … tokens belong to a specific org; the URL the
// user clicks must round-trip through that org's auth surface so
// /me, /refresh, /password/reset all resolve to the same key pool.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type recoveryLinkOptions struct {
	ClientID    string `json:"client_id,omitempty"`
	ContinueURL string `json:"continue_url,omitempty"`
	BrandName   string `json:"brand_name,omitempty"`
}

const (
	verifyEmailTTL   = 24 * time.Hour
	resetPasswordTTL = 1 * time.Hour
	magicLinkTTL     = 15 * time.Minute
	inviteTTL        = 7 * 24 * time.Hour
)

func issueVerifyEmailToken(ctx *sdk.AppCtx, projectID string, org *Organization, userID int64, email string) error {
	return issueVerifyEmailTokenForClient(ctx, projectID, org, userID, email, recoveryLinkOptions{})
}

func issueVerifyEmailTokenForClient(ctx *sdk.AppCtx, projectID string, org *Organization, userID int64, email string, opts recoveryLinkOptions) error {
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	meta, _ := json.Marshal(opts)
	if err := dbInsertVerificationToken(ctx.AppDB(), projectID, org.ID, userID, "verify_email",
		hashToken(raw), string(meta), time.Now().Add(verifyEmailTTL)); err != nil {
		return err
	}
	link := recoveryLink(ctx, org, opts.ContinueURL, "verify", raw, "/email/verify")
	dbAudit(ctx.AppDB(), projectID, org.ID, &userID, "", "verify_email_sent",
		"", "", map[string]any{"link": link, "email": email})
	brand := recoveryBrand(org, opts.BrandName)
	return sendRecoveryEmail(ctx, projectID, email, brand, "Verify your email",
		"Verify your email to finish creating your "+brand+" account.", "Verify email", link,
		fmt.Sprintf("auth:verify:%d:%s", userID, hashToken(raw)[:16]))
}

func issueResetToken(ctx *sdk.AppCtx, projectID string, org *Organization, userID int64, email string) error {
	return issueResetTokenForClient(ctx, projectID, org, userID, email, recoveryLinkOptions{})
}

func issueResetTokenForClient(ctx *sdk.AppCtx, projectID string, org *Organization, userID int64, email string, opts recoveryLinkOptions) error {
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	meta, _ := json.Marshal(opts)
	if err := dbInsertVerificationToken(ctx.AppDB(), projectID, org.ID, userID, "reset_password",
		hashToken(raw), string(meta), time.Now().Add(resetPasswordTTL)); err != nil {
		return err
	}
	link := recoveryLink(ctx, org, opts.ContinueURL, "reset", raw, "/password/reset")
	dbAudit(ctx.AppDB(), projectID, org.ID, &userID, "", "password_reset_sent",
		"", "", map[string]any{"link": link, "email": email})
	brand := recoveryBrand(org, opts.BrandName)
	return sendRecoveryEmail(ctx, projectID, email, brand, "Reset your password",
		"Use this secure link to choose a new password for your "+brand+" account.", "Reset password", link,
		fmt.Sprintf("auth:reset:%d:%s", userID, hashToken(raw)[:16]))
}

func recoveryLink(ctx *sdk.AppCtx, org *Organization, continueURL, action, token, fallbackPath string) string {
	continueURL = strings.TrimSpace(continueURL)
	if continueURL == "" {
		return buildLink(ctx, org, fallbackPath, url.Values{"token": {token}})
	}
	u, err := url.Parse(continueURL)
	if err != nil {
		return continueURL
	}
	fragment, _ := url.ParseQuery(u.Fragment)
	fragment.Set(action, token)
	u.Fragment = fragment.Encode()
	return u.String()
}

func recoveryBrand(org *Organization, configured string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	if org != nil && strings.TrimSpace(org.Name) != "" {
		return strings.TrimSpace(org.Name)
	}
	return "your account"
}

func sendRecoveryEmail(ctx *sdk.AppCtx, projectID, recipient, brand, subject, intro, action, link, idempotencyKey string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	from := strings.TrimSpace(ctx.Config().Get("from_email"))
	if from == "" {
		return nil // Development mode: the audited link remains available.
	}
	body := intro + "\n\n" + link + "\n\nIf you did not request this, you can ignore this email."
	bodyHTML := "<p>" + html.EscapeString(intro) + "</p><p><a href=\"" + html.EscapeString(link) + "\">" + html.EscapeString(action) + "</a></p><p>If you did not request this, you can ignore this email.</p>"
	var out map[string]any
	if err := ctx.WithProject(projectID).PlatformAPI().CallAppResult("messaging", "send_message", map[string]any{
		"channel": "email", "from": from, "from_name": brand, "to": recipient,
		"subject": subject, "body": body, "body_html": bodyHTML, "idempotency_key": idempotencyKey,
	}, &out); err != nil {
		return fmt.Errorf("messaging.send_message: %w", err)
	}
	if status, _ := out["status"].(string); status == "failed" {
		return errors.New("messaging failed to send the recovery email")
	}
	return nil
}

// buildLink composes the org-prefixed URL the user clicks. Lives at
// {platform_base}/orgs/{slug}{path}?{q} so the landing page can
// resolve org → keys → user without ambiguity.
func buildLink(ctx *sdk.AppCtx, org *Organization, path string, q url.Values) string {
	base := orgBaseURL(ctx, nil, org)
	if base == "" {
		base = "http://localhost:8080"
	}
	if q != nil {
		return base + path + "?" + q.Encode()
	}
	return base + path
}
