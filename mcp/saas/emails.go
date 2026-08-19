package main

import (
	"fmt"
	"html"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Transactional email delivery through the optional messaging app.
//
// messaging is declared optional in the manifest, so every send here is
// best-effort: failures (including "messaging is not installed") are
// recorded as notification.failed account events and never bubble into
// the calling handler or worker. Payment collection must not depend on
// email delivery.

const (
	// trialReminderLeadTime is how far before trial_ends_at the
	// trial-ending reminder goes out.
	trialReminderLeadTime = 48 * time.Hour
	// trialReminderRetryAfter throttles re-attempts for reminders whose
	// send failed (messaging missing or transient error) so the sweep
	// does not spam events every tick.
	trialReminderRetryAfter = 6 * time.Hour
)

type accountEmail struct {
	Kind           string
	Subject        string
	Body           string
	BodyHTML       string
	IdempotencyKey string
	Detail         map[string]any
}

// sendAccountEmail delivers one email to the account owner via the
// messaging app. Returns true only when messaging accepted the send.
func (a *App) sendAccountEmail(ctx *sdk.AppCtx, pid string, acct *Account, msg accountEmail) bool {
	if acct == nil || strings.TrimSpace(acct.OwnerEmail) == "" {
		return false
	}
	input := map[string]any{
		"_project_id": pid,
		"to":          "mailto:" + strings.TrimSpace(acct.OwnerEmail),
		"subject":     msg.Subject,
		"body":        msg.Body,
	}
	if msg.BodyHTML != "" {
		input["body_html"] = msg.BodyHTML
	}
	if msg.IdempotencyKey != "" {
		input["idempotency_key"] = msg.IdempotencyKey
	}
	detail := map[string]any{"kind": msg.Kind, "recipient_email": acct.OwnerEmail}
	for k, v := range msg.Detail {
		detail[k] = v
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("messaging", "send_message", input, &out); err != nil {
		detail["error"] = err.Error()
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "notification.failed", "saas", detail)
		return false
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "notification.sent", "saas", detail)
	return true
}

// planDisplayName resolves a human-readable plan name for email copy,
// falling back to the plan key.
func (a *App) planDisplayName(ctx *sdk.AppCtx, pid, planKey string) string {
	if plan, err := dbPlanGet(ctx.AppDB(), pid, planKey); err == nil && plan != nil && strings.TrimSpace(plan.Name) != "" {
		return plan.Name
	}
	return planKey
}

func formatAmountCents(cents int64, currency string) string {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" {
		cur = "USD"
	}
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, cents/100, cents%100, cur)
}

func formatEmailDate(value string) string {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC().Format("January 2, 2006")
	}
	return value
}

// sendPaymentLinkEmail tells the account owner a payment is due and
// where to pay. Only called when a hosted payment link URL exists —
// automatically collected cycles produce no link and no email.
func (a *App) sendPaymentLinkEmail(ctx *sdk.AppCtx, pid string, acct *Account, invoiceID int64, url, amount, periodStart, periodEnd string) {
	planName := a.planDisplayName(ctx, pid, acct.PlanKey)
	subject := fmt.Sprintf("Payment due for %s", planName)
	amountLine := ""
	if amount != "" {
		amountLine = "Amount due: " + amount + "\n"
	}
	periodLine := ""
	if periodStart != "" && periodEnd != "" {
		periodLine = fmt.Sprintf("Billing period: %s to %s\n", formatEmailDate(periodStart), formatEmailDate(periodEnd))
	}
	body := fmt.Sprintf("Your %s subscription has a payment due.\n\n%s%s\nPay securely:\n%s\n\nIf you have already paid, you can ignore this email.", planName, amountLine, periodLine, url)
	bodyHTML := "<p>Your <strong>" + html.EscapeString(planName) + "</strong> subscription has a payment due.</p>"
	if amount != "" {
		bodyHTML += "<p>Amount due: <strong>" + html.EscapeString(amount) + "</strong></p>"
	}
	if periodLine != "" {
		bodyHTML += "<p>" + html.EscapeString(strings.TrimSpace(periodLine)) + "</p>"
	}
	bodyHTML += "<p><a href=\"" + html.EscapeString(url) + "\">Pay securely</a></p><p>If you have already paid, you can ignore this email.</p>"
	a.sendAccountEmail(ctx, pid, acct, accountEmail{
		Kind:           "payment_link",
		Subject:        subject,
		Body:           body,
		BodyHTML:       bodyHTML,
		IdempotencyKey: fmt.Sprintf("saas:%s:payment_link:%d", acct.ID, invoiceID),
		Detail:         map[string]any{"invoice_id": invoiceID},
	})
}

// sendReceiptEmail confirms a paid invoice to the account owner.
func (a *App) sendReceiptEmail(ctx *sdk.AppCtx, pid string, acct *Account, invoice *billingInvoiceProjection) {
	if invoice == nil {
		return
	}
	planName := a.planDisplayName(ctx, pid, acct.PlanKey)
	amount := formatAmountCents(firstNonZero(invoice.AmountPaidCents, invoice.TotalCents), invoice.Currency)
	subject := fmt.Sprintf("Payment received — invoice #%d", invoice.InvoiceID)
	paidLine := ""
	if invoice.PaidAt != "" {
		paidLine = "Paid on: " + formatEmailDate(invoice.PaidAt) + "\n"
	}
	body := fmt.Sprintf("Thank you — we received your payment for %s.\n\nInvoice: #%d\nAmount paid: %s\n%s\nNo action is needed.", planName, invoice.InvoiceID, amount, paidLine)
	bodyHTML := "<p>Thank you — we received your payment for <strong>" + html.EscapeString(planName) + "</strong>.</p>" +
		fmt.Sprintf("<p>Invoice: #%d<br>Amount paid: <strong>%s</strong></p>", invoice.InvoiceID, html.EscapeString(amount))
	if invoice.PaidAt != "" {
		bodyHTML += "<p>Paid on " + html.EscapeString(formatEmailDate(invoice.PaidAt)) + ".</p>"
	}
	bodyHTML += "<p>No action is needed.</p>"
	a.sendAccountEmail(ctx, pid, acct, accountEmail{
		Kind:           "receipt",
		Subject:        subject,
		Body:           body,
		BodyHTML:       bodyHTML,
		IdempotencyKey: fmt.Sprintf("saas:%s:receipt:%d", acct.ID, invoice.InvoiceID),
		Detail:         map[string]any{"invoice_id": invoice.InvoiceID, "amount_paid_cents": invoice.AmountPaidCents},
	})
}

// sendPaymentFailedEmail notifies the account owner that collection
// failed, including the payment link when one exists so they can retry.
func (a *App) sendPaymentFailedEmail(ctx *sdk.AppCtx, pid string, acct *Account, invoiceID int64, eventName, url string) {
	planName := a.planDisplayName(ctx, pid, acct.PlanKey)
	subject := fmt.Sprintf("Payment failed for %s", planName)
	payLine := "Please update your payment method to keep your subscription active."
	payHTML := "<p>" + html.EscapeString(payLine) + "</p>"
	if url != "" {
		payLine = "Complete your payment to keep your subscription active:\n" + url
		payHTML = "<p><a href=\"" + html.EscapeString(url) + "\">Complete your payment</a> to keep your subscription active.</p>"
	}
	body := fmt.Sprintf("A payment for your %s subscription did not go through (invoice #%d).\n\n%s\n\nIf you believe this is an error, reply to this email.", planName, invoiceID, payLine)
	bodyHTML := fmt.Sprintf("<p>A payment for your <strong>%s</strong> subscription did not go through (invoice #%d).</p>", html.EscapeString(planName), invoiceID) +
		payHTML + "<p>If you believe this is an error, reply to this email.</p>"
	a.sendAccountEmail(ctx, pid, acct, accountEmail{
		Kind:           "payment_failed",
		Subject:        subject,
		Body:           body,
		BodyHTML:       bodyHTML,
		IdempotencyKey: fmt.Sprintf("saas:%s:payment_failed:%d:%s", acct.ID, invoiceID, eventName),
		Detail:         map[string]any{"invoice_id": invoiceID, "event": eventName},
	})
}

// runTrialReminders sweeps active accounts whose trial ends within
// trialReminderLeadTime and emails the owner once. The sent marker
// lives in account metadata; failed attempts are throttled and retried
// until the trial window closes.
func (a *App) runTrialReminders(ctx *sdk.AppCtx) error {
	pid := projectID(ctx, nil)
	if pid == "" {
		return nil
	}
	accounts, err := dbAccountList(ctx.AppDB(), pid, map[string]any{"status": StatusActive})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, acct := range accounts {
		meta := mapFromAny(acct.Metadata)
		if len(meta) == 0 || strArg(meta, "trial_reminder_sent_at") != "" {
			continue
		}
		trialEndsRaw := strArg(meta, "trial_ends_at")
		trialEnd, err := time.Parse(time.RFC3339, trialEndsRaw)
		if err != nil {
			continue
		}
		until := trialEnd.Sub(now)
		if until <= 0 || until > trialReminderLeadTime {
			continue
		}
		if attempted, err := time.Parse(time.RFC3339, strArg(meta, "trial_reminder_attempted_at")); err == nil && now.Sub(attempted) < trialReminderRetryAfter {
			continue
		}
		meta["trial_reminder_attempted_at"] = now.Format(time.RFC3339)
		if err := dbAccountSetMetadata(ctx.AppDB(), pid, acct.ID, meta); err != nil {
			return err
		}
		planName := a.planDisplayName(ctx, pid, acct.PlanKey)
		endDate := formatEmailDate(trialEndsRaw)
		subject := fmt.Sprintf("Your %s trial ends on %s", planName, endDate)
		body := fmt.Sprintf("Your free trial of %s ends on %s.\n\nTo keep access after the trial, complete the payment on the invoice you will receive when the trial ends.\n\nIf you have questions, reply to this email.", planName, endDate)
		bodyHTML := "<p>Your free trial of <strong>" + html.EscapeString(planName) + "</strong> ends on " + html.EscapeString(endDate) + ".</p>" +
			"<p>To keep access after the trial, complete the payment on the invoice you will receive when the trial ends.</p>" +
			"<p>If you have questions, reply to this email.</p>"
		if a.sendAccountEmail(ctx, pid, acct, accountEmail{
			Kind:           "trial_reminder",
			Subject:        subject,
			Body:           body,
			BodyHTML:       bodyHTML,
			IdempotencyKey: fmt.Sprintf("saas:%s:trial_reminder:%s", acct.ID, trialEndsRaw),
			Detail:         map[string]any{"trial_ends_at": trialEndsRaw},
		}) {
			meta["trial_reminder_sent_at"] = now.Format(time.RFC3339)
			if err := dbAccountSetMetadata(ctx.AppDB(), pid, acct.ID, meta); err != nil {
				return err
			}
		}
	}
	return nil
}
