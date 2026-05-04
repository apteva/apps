package main

// mail.go — composes with the messaging app via PlatformAPI to send
// transactional email (verify, reset, magic-link, invite). When the
// messaging app isn't installed, links are written to the audit log
// only — a development escape hatch.
//
// v0.1 ships the function plumbing + audit-only fallback. Full
// messaging-app integration (CallApp("messaging", "send", …)) lands
// in the next pass once we settle on messaging.send's input schema.

import (
	"net/url"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	verifyEmailTTL  = 24 * time.Hour
	resetPasswordTTL = 1 * time.Hour
	magicLinkTTL    = 15 * time.Minute
	inviteTTL       = 7 * 24 * time.Hour
)

// issueVerifyEmailToken creates a one-time verify_email token, writes
// it to the DB, and "sends" it. v0.1 fallback: log a `verify_email_sent`
// audit row containing the link. The deployed SaaS reads this from
// the dashboard during dev, then wires up messaging in production.
func issueVerifyEmailToken(ctx *sdk.AppCtx, projectID string, userID int64, email string) error {
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	if err := dbInsertVerificationToken(ctx.AppDB(), projectID, userID, "verify_email",
		hashToken(raw), "", time.Now().Add(verifyEmailTTL)); err != nil {
		return err
	}
	link := buildLink(ctx, "/email/verify", url.Values{"token": {raw}})
	dbAudit(ctx.AppDB(), projectID, &userID, "", "verify_email_sent",
		"", "", map[string]any{"link": link, "email": email})
	// TODO v0.1.1: ctx.PlatformAPI().CallApp("messaging", "send", {…})
	return nil
}

// issueResetToken — same shape, kind=reset_password.
func issueResetToken(ctx *sdk.AppCtx, projectID string, userID int64, email string) error {
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	if err := dbInsertVerificationToken(ctx.AppDB(), projectID, userID, "reset_password",
		hashToken(raw), "", time.Now().Add(resetPasswordTTL)); err != nil {
		return err
	}
	link := buildLink(ctx, "/password/reset", url.Values{"token": {raw}})
	dbAudit(ctx.AppDB(), projectID, &userID, "", "password_reset_sent",
		"", "", map[string]any{"link": link, "email": email})
	return nil
}

func buildLink(ctx *sdk.AppCtx, path string, q url.Values) string {
	base := cfgStr(ctx, "app_url", "")
	if base == "" {
		base = "http://localhost:8080"
	}
	if q != nil {
		return base + path + "?" + q.Encode()
	}
	return base + path
}
