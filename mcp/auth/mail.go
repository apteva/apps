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

func issueVerifyEmailTokenForClient(ctx *sdk.AppCtx, pid string, org *Organization, uid int64, email string, opts recoveryLinkOptions) error {
	return issueRecoveryToken(ctx, pid, org, uid, email, "verify_email", opts)
}
func issueResetToken(ctx *sdk.AppCtx, pid string, org *Organization, uid int64, email string) error {
	return issueResetTokenForClient(ctx, pid, org, uid, email, recoveryLinkOptions{})
}
func issueResetTokenForClient(ctx *sdk.AppCtx, pid string, org *Organization, uid int64, email string, opts recoveryLinkOptions) error {
	return issueRecoveryToken(ctx, pid, org, uid, email, "reset_password", opts)
}
func issueRecoveryToken(ctx *sdk.AppCtx, pid string, org *Organization, uid int64, email, kind string, opts recoveryLinkOptions) error {
	if ctx == nil || ctx.PlatformAPI() == nil || strings.TrimSpace(ctx.Config().Get("from_email")) == "" {
		return errors.New("email delivery is not configured")
	}
	if orgBaseURL(ctx, nil, org) == "" {
		return errors.New("public Auth URL is not configured")
	}
	if opts.ClientID == "" {
		clients, err := dbListClients(ctx.AppDB(), pid, org.ID, false)
		if err != nil {
			return err
		}
		for _, c := range clients {
			if c.Type == "spa" || c.Type == "native" {
				opts.ClientID = c.ClientID
				break
			}
		}
		if opts.ClientID == "" {
			return errors.New("an active public client is required for the built-in recovery page")
		}
	}
	raw, err := randSlug(32)
	if err != nil {
		return err
	}
	action, path, subject, intro, ttl := "verify", "/email/verify", "Verify your email", "Verify your email to finish creating your account.", verifyEmailTTL
	if kind == "reset_password" {
		action, path, subject, intro, ttl = "reset", "/password/reset", "Reset your password", "Use this secure link to choose a new password.", resetPasswordTTL
	}
	meta, _ := json.Marshal(opts)
	if err = dbInsertVerificationToken(ctx.AppDB(), pid, org.ID, uid, kind, hashToken(raw), string(meta), time.Now().Add(ttl)); err != nil {
		return err
	}
	link := recoveryLink(ctx, org, opts.ContinueURL, action, raw, path)
	if opts.ContinueURL == "" {
		u, _ := url.Parse(link)
		f, _ := url.ParseQuery(u.Fragment)
		f.Set("client_id", opts.ClientID)
		f.Set("project_id", pid)
		u.Fragment = f.Encode()
		link = u.String()
	}
	err = sendRecoveryEmail(ctx, pid, email, recoveryBrand(org, opts.BrandName), subject, intro, subject, link, fmt.Sprintf("auth:%s:%d:%s", action, uid, hashToken(raw)[:16]))
	status := "sent"
	if err != nil {
		status = "failed"
		_, _ = ctx.AppDB().Exec(`DELETE FROM verification_tokens WHERE project_id=? AND token_hash=?`, pid, hashToken(raw))
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, opts.ClientID, kind+"_delivery", "", "", map[string]any{"status": status})
	return err
}

func recoveryLink(ctx *sdk.AppCtx, org *Organization, continueURL, action, token, fallbackPath string) string {
	continueURL = strings.TrimSpace(continueURL)
	if continueURL == "" {
		return buildLink(ctx, org, fallbackPath, nil) + "#" + url.Values{action: {token}}.Encode()
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
		return errors.New("email delivery is not configured")
	}
	from := strings.TrimSpace(ctx.Config().Get("from_email"))
	if from == "" {
		return errors.New("email delivery is not configured")
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
		return ""
	}
	if q != nil {
		return base + path + "?" + q.Encode()
	}
	return base + path
}
