package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var emailCheckSlots = make(chan struct{}, maxParallelEmailChecks)

const (
	emailVerificationRole       = "email_verification"
	emailVerificationCapability = "email.verify"
	emailVerificationTool       = "email_check"
	maxParallelEmailChecks      = 4
)

type emailCheckerResult struct {
	Email          string                 `json:"email"`
	Valid          bool                   `json:"valid"`
	Reasons        []string               `json:"reasons"`
	SyntaxOK       bool                   `json:"syntax_ok"`
	DomainStatus   string                 `json:"domain_status"`
	ImplicitMX     bool                   `json:"implicit_mx"`
	SuggestedEmail string                 `json:"suggested_email"`
	Disposable     bool                   `json:"disposable"`
	Role           bool                   `json:"role"`
	Free           bool                   `json:"free"`
	Verdict        string                 `json:"verdict"`
	Confidence     string                 `json:"confidence"`
	Recommendation string                 `json:"recommendation"`
	SMTP           emailCheckerSMTPResult `json:"smtp"`
}

type emailCheckerSMTPResult struct {
	Checked     bool   `json:"checked"`
	RcptStatus  string `json:"rcpt_status"`
	Informative *bool  `json:"informative"`
	CatchAll    *bool  `json:"catch_all"`
	Retryable   bool   `json:"retryable"`
	Note        string `json:"note"`
}

type EmailVerificationResult struct {
	ChannelID      int64          `json:"channel_id,omitempty"`
	Email          string         `json:"email"`
	Verdict        string         `json:"verdict"`
	Confidence     string         `json:"confidence"`
	Reason         string         `json:"reason,omitempty"`
	Reasons        []string       `json:"reasons,omitempty"`
	Recommendation string         `json:"recommendation,omitempty"`
	Source         string         `json:"source"`
	SuggestedValue string         `json:"suggested_value,omitempty"`
	CheckedAt      string         `json:"checked_at"`
	Details        map[string]any `json:"details,omitempty"`
}

type channelVerificationRecord struct {
	Verdict        string
	Confidence     string
	Reason         string
	Recommendation string
	Source         string
	SuggestedValue string
	CheckedAt      string
	DetailsJSON    string
}

type emailVerificationSettings struct {
	Mode            string
	Policy          string
	TimeoutSeconds  int
	BlockDisposable bool
}

type EmailVerificationPolicyError struct {
	Results []EmailVerificationResult
}

func (e *EmailVerificationPolicyError) Error() string {
	if len(e.Results) == 0 {
		return "email rejected by verification policy"
	}
	result := e.Results[0]
	reason := result.Reason
	if reason == "" {
		reason = result.Verdict
	}
	return fmt.Sprintf("email %q rejected by verification policy: %s", result.Email, reason)
}

func loadEmailVerificationSettings(ctx *sdk.AppCtx) emailVerificationSettings {
	settings := emailVerificationSettings{
		Mode:           "local",
		Policy:         "annotate",
		TimeoutSeconds: 5,
	}
	if ctx == nil {
		return settings
	}
	cfg := ctx.Config()
	if mode := strings.ToLower(strings.TrimSpace(cfg.Get("email_verification_mode"))); mode == "off" || mode == "local" || mode == "smtp" {
		settings.Mode = mode
	}
	if policy := strings.ToLower(strings.TrimSpace(cfg.Get("email_verification_policy"))); policy == "annotate" || policy == "reject_definitive" {
		settings.Policy = policy
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(cfg.Get("email_verification_timeout_seconds"))); err == nil && seconds >= 1 && seconds <= 60 {
		settings.TimeoutSeconds = seconds
	}
	settings.BlockDisposable = boolFromAny(cfg.Get("email_verification_block_disposable"))
	return settings
}

func emailCheckerBinding(ctx *sdk.AppCtx) *sdk.BoundIntegration {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	return ctx.IntegrationFor(emailVerificationRole)
}

type emailCheckerTarget struct {
	appName string
	tool    string
}

func resolveEmailCheckerTarget(ctx *sdk.AppCtx) *emailCheckerTarget {
	bound := emailCheckerBinding(ctx)
	if bound == nil {
		return nil
	}
	appName := strings.TrimSpace(bound.AppName)
	if appName == "" {
		appName = "email-checker"
	}
	tool := emailVerificationTool
	if bound.ToolFor != nil {
		if mapped := strings.TrimSpace(bound.ToolFor(emailVerificationCapability)); mapped != "" && mapped != emailVerificationCapability {
			tool = mapped
		}
	}
	return &emailCheckerTarget{appName: appName, tool: tool}
}

func callEmailCheckerTarget(ctx *sdk.AppCtx, pid, email string, smtp bool, timeoutSeconds int, target *emailCheckerTarget) (emailCheckerResult, error) {
	if target == nil {
		return emailCheckerResult{}, errors.New("Email Checker is not bound")
	}
	input := map[string]any{
		"email":           email,
		"smtp":            smtp,
		"provider":        "local",
		"timeout_seconds": timeoutSeconds,
	}
	var result emailCheckerResult
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult(target.appName, target.tool, input, &result); err != nil {
		return emailCheckerResult{}, err
	}
	return result, nil
}

func callEmailChecker(ctx *sdk.AppCtx, pid, email string, smtp bool, timeoutSeconds int) (emailCheckerResult, error) {
	return callEmailCheckerTarget(ctx, pid, email, smtp, timeoutSeconds, resolveEmailCheckerTarget(ctx))
}

func verificationResultFromChecker(email string, smtp bool, checked emailCheckerResult) EmailVerificationResult {
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	source := "email-checker:local"
	if smtp {
		source = "email-checker:local-smtp"
	}
	reasons := append([]string(nil), checked.Reasons...)
	verdict := checked.Verdict
	if verdict != "deliverable" && verdict != "undeliverable" && verdict != "risky" && verdict != "unknown" {
		verdict = "unknown"
	}
	confidence := checked.Confidence
	if confidence == "" {
		confidence = "low"
	}
	recommendation := checked.Recommendation
	if recommendation == "" && verdict == "unknown" {
		recommendation = "retry"
	}
	details := map[string]any{
		"reasons":       reasons,
		"domain_status": checked.DomainStatus,
		"implicit_mx":   checked.ImplicitMX,
		"disposable":    checked.Disposable,
		"role":          checked.Role,
		"free":          checked.Free,
		"smtp": map[string]any{
			"checked":     checked.SMTP.Checked,
			"rcpt_status": checked.SMTP.RcptStatus,
			"informative": checked.SMTP.Informative,
			"catch_all":   checked.SMTP.CatchAll,
			"retryable":   checked.SMTP.Retryable,
			"note":        checked.SMTP.Note,
		},
	}
	return EmailVerificationResult{
		Email:          email,
		Verdict:        verdict,
		Confidence:     confidence,
		Reason:         primaryEmailVerificationReason(checked),
		Reasons:        reasons,
		Recommendation: recommendation,
		Source:         source,
		SuggestedValue: checked.SuggestedEmail,
		CheckedAt:      checkedAt,
		Details:        details,
	}
}

func unavailableEmailVerification(email string, smtp bool) EmailVerificationResult {
	source := "email-checker:local"
	if smtp {
		source = "email-checker:local-smtp"
	}
	return EmailVerificationResult{
		Email:          email,
		Verdict:        "unknown",
		Confidence:     "low",
		Reason:         "verifier_unavailable",
		Reasons:        []string{"verifier_unavailable"},
		Recommendation: "retry",
		Source:         source,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		Details:        map[string]any{"retryable": true},
	}
}

func primaryEmailVerificationReason(result emailCheckerResult) string {
	priority := []string{"bad_syntax", "domain_does_not_accept_mail", "no_mail_server", "disposable_domain", "possible_typo", "dns_temporary_error", "role_account"}
	for _, want := range priority {
		for _, got := range result.Reasons {
			if got == want {
				return got
			}
		}
	}
	if result.SMTP.Checked {
		switch result.SMTP.RcptStatus {
		case "reject":
			return "smtp_rejected"
		case "catch_all":
			return "catch_all"
		case "tempfail", "timeout", "connect_failed", "unavailable":
			return "smtp_temporary_failure"
		}
	}
	if len(result.Reasons) > 0 {
		return result.Reasons[0]
	}
	return ""
}

func definitiveEmailFailure(result emailCheckerResult, blockDisposable bool) bool {
	if (!result.SyntaxOK && containsEmailSignal(result.Reasons, "bad_syntax")) || result.DomainStatus == "null_mx" || result.DomainStatus == "no_mail_server" {
		return true
	}
	if blockDisposable && result.Disposable {
		return true
	}
	return result.SMTP.Checked && result.SMTP.Informative != nil && *result.SMTP.Informative && result.SMTP.RcptStatus == "reject"
}

func containsEmailSignal(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func recordFromEmailVerification(result EmailVerificationResult) channelVerificationRecord {
	detailsJSON, _ := json.Marshal(result.Details)
	return channelVerificationRecord{
		Verdict:        result.Verdict,
		Confidence:     result.Confidence,
		Reason:         result.Reason,
		Recommendation: result.Recommendation,
		Source:         result.Source,
		SuggestedValue: result.SuggestedValue,
		CheckedAt:      result.CheckedAt,
		DetailsJSON:    string(detailsJSON),
	}
}

func automaticEmailVerificationCandidates(raw any, existing []Channel) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	channels, err := parseChannelInputs(raw)
	if err != nil {
		return nil, err
	}
	existingEmails := map[string]bool{}
	for _, channel := range existing {
		if channel.Kind == "email" {
			existingEmails[normaliseChannel("email", channel.Value)] = true
		}
	}
	emails := []string{}
	for _, channel := range channels {
		if channel.Kind != "email" || existingEmails[channel.Value] {
			continue
		}
		emails = append(emails, channel.Value)
	}
	return emails, nil
}

func prepareAutomaticEmailVerifications(ctx *sdk.AppCtx, pid string, raw any, existing []Channel, enforcePolicy bool) (map[string]channelVerificationRecord, []EmailVerificationResult, error) {
	settings := loadEmailVerificationSettings(ctx)
	if settings.Mode == "off" {
		return nil, []EmailVerificationResult{}, nil
	}
	target := resolveEmailCheckerTarget(ctx)
	if target == nil {
		return nil, []EmailVerificationResult{}, nil
	}
	emails, err := automaticEmailVerificationCandidates(raw, existing)
	if err != nil || len(emails) == 0 {
		return nil, []EmailVerificationResult{}, err
	}

	results := make([]EmailVerificationResult, len(emails))
	checkerResults := make([]emailCheckerResult, len(emails))
	smtp := settings.Mode == "smtp"
	sem := emailCheckSlots
	var wg sync.WaitGroup
	for i, email := range emails {
		sem <- struct{}{}
		wg.Add(1)
		go func(index int, address string) {
			defer wg.Done()
			checked, callErr := callEmailCheckerTarget(ctx, pid, address, smtp, settings.TimeoutSeconds, target)
			<-sem
			if callErr != nil {
				results[index] = unavailableEmailVerification(address, smtp)
				return
			}
			checkerResults[index] = checked
			results[index] = verificationResultFromChecker(address, smtp, checked)
		}(i, email)
	}
	wg.Wait()

	records := make(map[string]channelVerificationRecord, len(results))
	rejected := []EmailVerificationResult{}
	for i, result := range results {
		records[result.Email] = recordFromEmailVerification(result)
		if enforcePolicy && settings.Policy == "reject_definitive" && result.Reason != "verifier_unavailable" && definitiveEmailFailure(checkerResults[i], settings.BlockDisposable) {
			rejected = append(rejected, result)
		}
	}
	if len(rejected) > 0 {
		return records, results, &EmailVerificationPolicyError{Results: rejected}
	}
	return records, results, nil
}

func attachVerificationChannelIDs(contact *Contact, results []EmailVerificationResult) []EmailVerificationResult {
	if contact == nil || len(results) == 0 {
		return results
	}
	ids := map[string]int64{}
	for _, channel := range contact.Channels {
		if channel.Kind == "email" {
			ids[channel.Value] = channel.ID
		}
	}
	for i := range results {
		results[i].ChannelID = ids[results[i].Email]
	}
	return results
}

func createContactWithEmailVerification(ctx *sdk.AppCtx, pid string, args map[string]any, enforcePolicy bool) (*Contact, []EmailVerificationResult, error) {
	records, results, err := prepareAutomaticEmailVerifications(ctx, pid, args["channels"], nil, enforcePolicy)
	if err != nil {
		return nil, results, err
	}
	contact, err := dbCreateWithChannelVerifications(ctx.AppDB(), pid, args, records)
	if err != nil {
		return nil, results, err
	}
	if err := loadChannels(ctx.AppDB(), contact); err != nil {
		return nil, results, err
	}
	return contact, attachVerificationChannelIDs(contact, results), nil
}

func updateContactWithEmailVerification(ctx *sdk.AppCtx, pid string, id int64, patch map[string]any, source string) (*Contact, []EmailVerificationResult, error) {
	existing, err := dbGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, nil, err
	}
	if existing == nil {
		return nil, nil, sql.ErrNoRows
	}
	if err := loadChannels(ctx.AppDB(), existing); err != nil {
		return nil, nil, err
	}
	records, results, err := prepareAutomaticEmailVerifications(ctx, pid, patch["channels"], existing.Channels, true)
	if err != nil {
		return nil, results, err
	}
	contact, err := dbUpdateWithChannelVerifications(ctx.AppDB(), pid, id, patch, source, records)
	if err != nil {
		return nil, results, err
	}
	return contact, attachVerificationChannelIDs(contact, results), nil
}

func upsertContactWithEmailVerification(ctx *sdk.AppCtx, pid, kind, value string, defaults map[string]any, source string, enforcePolicy bool) (*Contact, bool, []EmailVerificationResult, error) {
	value = normaliseChannel(kind, value)
	existing, err := dbGetByPrimary(ctx.AppDB(), pid, kind, value)
	if err != nil {
		return nil, false, nil, err
	}
	if existing != nil {
		_ = loadChannels(ctx.AppDB(), existing)
		return existing, false, []EmailVerificationResult{}, nil
	}
	args := map[string]any{}
	for key, item := range defaults {
		args[key] = item
	}
	args["source"] = source
	args["channels"] = []any{map[string]any{"kind": kind, "value": value, "is_primary": true}}
	contact, results, err := createContactWithEmailVerification(ctx, pid, args, enforcePolicy)
	if err != nil {
		if found, lookupErr := dbGetByPrimary(ctx.AppDB(), pid, kind, value); lookupErr == nil && found != nil {
			_ = loadChannels(ctx.AppDB(), found)
			return found, false, []EmailVerificationResult{}, nil
		}
		return nil, false, results, err
	}
	return contact, true, results, nil
}

func (a *App) verifyContactEmail(ctx *sdk.AppCtx, pid string, contactID, channelID int64, smtp bool) (*Contact, EmailVerificationResult, error) {
	var kind, email string
	if err := ctx.AppDB().QueryRow(
		`SELECT kind, value FROM contact_channels WHERE project_id = ? AND contact_id = ? AND id = ?`,
		pid, contactID, channelID,
	).Scan(&kind, &email); err != nil {
		return nil, EmailVerificationResult{}, err
	}
	if kind != "email" {
		return nil, EmailVerificationResult{}, errors.New("channel is not an email")
	}
	settings := loadEmailVerificationSettings(ctx)
	checked, err := callEmailChecker(ctx, pid, email, smtp, settings.TimeoutSeconds)
	if err != nil {
		return nil, EmailVerificationResult{}, err
	}
	result := verificationResultFromChecker(email, smtp, checked)
	result.ChannelID = channelID
	record := recordFromEmailVerification(result)
	updated, err := ctx.AppDB().Exec(
		`UPDATE contact_channels SET verification_verdict = ?, verification_confidence = ?,
			verification_reason = ?, verification_recommendation = ?, verification_source = ?,
			verification_suggested_value = ?, verification_checked_at = ?, verification_details = ?
		 WHERE project_id = ? AND contact_id = ? AND id = ? AND kind = 'email' AND value = ?`,
		nullStr(record.Verdict), nullStr(record.Confidence), nullStr(record.Reason), nullStr(record.Recommendation),
		nullStr(record.Source), nullStr(record.SuggestedValue), nullStr(record.CheckedAt), nullStr(record.DetailsJSON),
		pid, contactID, channelID, email,
	)
	if err != nil {
		return nil, EmailVerificationResult{}, err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return nil, EmailVerificationResult{}, errors.New("email channel changed while verification was running; retry")
	}
	contact, err := dbGetByID(ctx.AppDB(), pid, contactID)
	if err != nil || contact == nil {
		return contact, EmailVerificationResult{}, err
	}
	if err := loadChannels(ctx.AppDB(), contact); err != nil {
		return nil, EmailVerificationResult{}, err
	}
	emitContact(ctx, pid, "contact.updated", contact)
	return contact, result, nil
}
