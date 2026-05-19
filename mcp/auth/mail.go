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
	"net/url"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	verifyEmailTTL   = 24 * time.Hour
	resetPasswordTTL = 1 * time.Hour
	magicLinkTTL     = 15 * time.Minute
	inviteTTL        = 7 * 24 * time.Hour
)

func issueVerifyEmailToken(ctx *sdk.AppCtx, projectID string, org *Organization, userID int64, email string) error {
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	if err := dbInsertVerificationToken(ctx.AppDB(), projectID, org.ID, userID, "verify_email",
		hashToken(raw), "", time.Now().Add(verifyEmailTTL)); err != nil {
		return err
	}
	link := buildLink(ctx, org, "/email/verify", url.Values{"token": {raw}})
	dbAudit(ctx.AppDB(), projectID, org.ID, &userID, "", "verify_email_sent",
		"", "", map[string]any{"link": link, "email": email})
	// TODO v0.4.x: ctx.PlatformAPI().CallApp("messaging", "send", {…})
	return nil
}

func issueResetToken(ctx *sdk.AppCtx, projectID string, org *Organization, userID int64, email string) error {
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	if err := dbInsertVerificationToken(ctx.AppDB(), projectID, org.ID, userID, "reset_password",
		hashToken(raw), "", time.Now().Add(resetPasswordTTL)); err != nil {
		return err
	}
	link := buildLink(ctx, org, "/password/reset", url.Values{"token": {raw}})
	dbAudit(ctx.AppDB(), projectID, org.ID, &userID, "", "password_reset_sent",
		"", "", map[string]any{"link": link, "email": email})
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
