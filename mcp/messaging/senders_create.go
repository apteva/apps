package main

// senders_create.go — unified sender registration.
//
// One MCP tool (senders_create) + one HTTP route (/senders/create) cover
// every "add an identity to messaging" path:
//
//   • address looks like an email (foo@x.com) → SES verify_email. SES
//     mails the inbox; nothing else happens.
//
//   • address looks like a domain (x.com) and inbound="auto" (default):
//       - SES verify_domain → DKIM tokens
//       - publish DKIM CNAMEs (+ SPF if enabled) via the domains app
//       - if aws-s3 AND aws-sns are bound, also run the full inbound
//         bootstrap (S3 bucket + bucket policy, SNS topic + topic
//         policy, receipt rule set + rule + activation, SNS subscribe
//         the messaging webhook, MX record). Otherwise skip with a
//         per-step note.
//
//   • address is a domain and inbound="true": same as above but hard-
//     require S3+SNS bound, fail loudly if not.
//
//   • address is a domain and inbound="false": outbound only — no MX,
//     no AWS S3/SNS calls.
//
// Every per-step result lands in resp.Steps so the caller can render
// exactly what ran / what was skipped / what failed. All AWS calls are
// idempotent — re-running senders_create converges on the same state.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type sendersCreateReq struct {
	Address     string `json:"address"`       // required: email | domain | E.164 phone
	Channel     string `json:"channel"`       // optional: 'email' | 'sms' | 'whatsapp'. Auto-detected if blank.
	Inbound     string `json:"inbound"`       // "auto" | "true" | "false"; default "auto"
	PublishDNS  *bool  `json:"publish_dns"`   // domain only; default true
	SPF         *bool  `json:"spf"`           // domain only; default true
	Region      string `json:"region"`        // default eu-west-1 (inbound only)
	BucketName  string `json:"bucket_name"`   // auto-named if blank
	TopicName   string `json:"topic_name"`    // auto-named if blank
	RuleSetName string `json:"rule_set_name"` // default "apteva-default"
	RuleName    string `json:"rule_name"`     // default "messaging-inbound"
	DisplayName string `json:"display_name"`  // optional friendly name persisted on the local row
	SetDefault  bool   `json:"set_default"`   // make this the default sender for (project, channel)
	ProjectID   string `json:"-"`             // resolved from args / env; not user-supplied
}

type bootstrapStep struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type sendersCreateInbound struct {
	Bootstrapped    bool   `json:"bootstrapped"`
	SkippedReason   string `json:"skipped_reason,omitempty"`
	BucketName      string `json:"bucket_name,omitempty"`
	TopicARN        string `json:"topic_arn,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	WebhookURL      string `json:"webhook_url,omitempty"`
	SubscriptionARN string `json:"subscription_arn,omitempty"`
	Region          string `json:"region,omitempty"`
	RuleSetName     string `json:"rule_set_name,omitempty"`
	RuleName        string `json:"rule_name,omitempty"`
}

type sendersCreateResp struct {
	Address    string                `json:"address"`
	Kind       string                `json:"kind"` // "email" | "domain"
	Pending    bool                  `json:"pending"`
	NextStep   string                `json:"next_step,omitempty"`
	DkimTokens []string              `json:"dkim_tokens,omitempty"`
	DkimStatus string                `json:"dkim_status,omitempty"`
	DnsRecords []map[string]string   `json:"dns_records,omitempty"`
	Inbound    *sendersCreateInbound `json:"inbound,omitempty"`
	Steps      []bootstrapStep       `json:"steps"`
}

// HTTP entry point — POST /senders/create.
func (a *App) handleSendersCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body sendersCreateReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	out, err := a.sendersCreateImpl(globalCtx, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

// MCP entry point — args mirror sendersCreateReq.
func (a *App) toolSendersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body := sendersCreateReq{
		Address:     strArg(args, "address"),
		Inbound:     strArg(args, "inbound"),
		Region:      strArg(args, "region"),
		BucketName:  strArg(args, "bucket_name"),
		TopicName:   strArg(args, "topic_name"),
		RuleSetName: strArg(args, "rule_set_name"),
		RuleName:    strArg(args, "rule_name"),
	}
	if v, ok := args["publish_dns"].(bool); ok {
		body.PublishDNS = &v
	}
	if v, ok := args["spf"].(bool); ok {
		body.SPF = &v
	}
	return a.sendersCreateImpl(ctx, body)
}

func (a *App) sendersCreateImpl(ctx *sdk.AppCtx, req sendersCreateReq) (*sendersCreateResp, error) {
	pid := req.ProjectID
	if pid == "" {
		pid = strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	}
	if pid == "" {
		return nil, errors.New("project_id required")
	}

	// Phone shapes (E.164: leading "+" followed by digits) and the
	// explicit channel=sms|whatsapp arg both route to the Twilio
	// branch. Email and domain shapes route to SES. Empty channel
	// auto-detects.
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	addr := strings.TrimSpace(req.Address)
	if channel == "" {
		channel = inferChannelFromAddress(addr)
	}

	switch channel {
	case "sms", "whatsapp":
		return a.sendersCreatePhone(ctx, pid, channel, addr, req)
	case "email", "":
		// Email branch handles both "foo@x.com" and "x.com" — fall
		// through to the classifier.
	default:
		return nil, fmt.Errorf("unsupported channel %q (use email|sms|whatsapp)", channel)
	}

	kind, raw, err := classifyEmailIdentity(addr)
	if err != nil {
		return nil, err
	}
	sesBound := ctx.IntegrationFor("email_provider")
	if sesBound == nil {
		return nil, errors.New("email_provider (aws-ses) not bound")
	}
	normalisedKind := normaliseSenderKind(kind)
	resp := &sendersCreateResp{
		Address: canonicalSenderAddress(kind, raw),
		Kind:    normalisedKind,
		Pending: true,
	}
	if normalisedKind == "email" {
		return a.sendersCreateEmail(ctx, pid, sesBound.ConnectionID, raw, resp)
	}
	return a.sendersCreateDomain(ctx, pid, sesBound.ConnectionID, raw, req, resp)
}

// inferChannelFromAddress: leading "+" + digits → sms; contains "@"
// or is a bare domain → email. Returns "" when ambiguous; the caller
// surfaces the channel-required error.
func inferChannelFromAddress(addr string) string {
	s := strings.TrimSpace(addr)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "+") {
		rest := s[1:]
		if rest != "" && allDigits(rest) {
			return "sms"
		}
	}
	if strings.Contains(s, "@") {
		return "email"
	}
	// Bare domain (has a dot, no @, no spaces). classifyEmailIdentity
	// will refine; route to email so we go through its validator.
	if strings.Contains(s, ".") && !strings.ContainsAny(s, " /\t\r\n") {
		return "email"
	}
	return ""
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func (a *App) sendersCreateEmail(ctx *sdk.AppCtx, pid string, connID int64, addr string, resp *sendersCreateResp) (*sendersCreateResp, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "verify_email", map[string]any{
		"EmailIdentity": addr,
	})
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_email", OK: false, Error: err.Error()})
		return resp, nil
	}
	if res == nil || !res.Success {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_email", OK: false, Error: truncateResData(res)})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_email", OK: true})
	resp.NextStep = verifyNextStepHint("email")
	a.persistSenderRow(ctx, pid, &senderUpsert{
		ProjectID:          pid,
		Channel:            "email",
		Address:            addr,
		Kind:               "email",
		Provider:           "aws-ses",
		ProviderIdentityID: addr,
		Verified:           false,
		VerificationStatus: "pending",
		SendingEnabled:     true,
		MarkSyncedNow:      true,
	}, resp)
	return resp, nil
}

func (a *App) sendersCreateDomain(ctx *sdk.AppCtx, pid string, sesConnID int64, domain string, req sendersCreateReq, resp *sendersCreateResp) (*sendersCreateResp, error) {
	region := req.Region
	if region == "" {
		region = "eu-west-1"
	}
	publishDNS := true
	if req.PublishDNS != nil {
		publishDNS = *req.PublishDNS
	}
	publishSPF := true
	if req.SPF != nil {
		publishSPF = *req.SPF
	}

	s3Bound := ctx.IntegrationFor("inbound_storage")
	snsBound := ctx.IntegrationFor("inbound_notifications")
	doInbound, skipReason, err := resolveInboundMode(req.Inbound, s3Bound, snsBound)
	if err != nil {
		return nil, err
	}
	resp.Inbound = &sendersCreateInbound{Bootstrapped: false, SkippedReason: skipReason, Region: region}

	id, _ := ctx.PlatformAPI().WhoAmI()
	bucketName := req.BucketName
	if bucketName == "" && id != nil {
		bucketName = fmt.Sprintf("apteva-ses-inbound-%d", id.InstallID)
	}
	topicName := req.TopicName
	if topicName == "" && id != nil {
		topicName = fmt.Sprintf("apteva-ses-inbound-%d", id.InstallID)
	}
	ruleSetName := req.RuleSetName
	if ruleSetName == "" {
		ruleSetName = "apteva-default"
	}
	ruleName := req.RuleName
	if ruleName == "" {
		ruleName = "messaging-inbound"
	}
	if doInbound {
		resp.Inbound.BucketName = bucketName
		resp.Inbound.RuleSetName = ruleSetName
		resp.Inbound.RuleName = ruleName
	}

	// SNS topic + policy first (so we know the account id before
	// writing the S3 bucket policy).
	var topicArn, accountID string
	if doInbound {
		topicArn, err = bootstrapCreateSNSTopic(ctx, snsBound.ConnectionID, topicName)
		if err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_sns_topic", OK: false, Error: err.Error()})
			return resp, nil
		}
		accountID = parseAccountFromARN(topicArn)
		resp.Inbound.TopicARN = topicArn
		resp.Inbound.AccountID = accountID
		resp.Steps = append(resp.Steps, bootstrapStep{
			Step: "create_sns_topic", OK: true,
			Detail: fmt.Sprintf("topic_arn=%s account=%s", topicArn, accountID),
		})

		if err := bootstrapSetSNSPolicy(ctx, snsBound.ConnectionID, topicArn, snsTopicPolicy(topicArn, accountID)); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_sns_topic_policy", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_sns_topic_policy", OK: true})

		if err := bootstrapCreateS3Bucket(ctx, s3Bound.ConnectionID, bucketName, region); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_s3_bucket", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_s3_bucket", OK: true})

		if err := bootstrapSetS3BucketPolicy(ctx, s3Bound.ConnectionID, bucketName, s3BucketPolicy(bucketName, accountID)); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "put_s3_bucket_policy", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "put_s3_bucket_policy", OK: true})
	}

	// SES verify_domain — outbound + inbound both need DKIM.
	dkimTokens, dkimStatus, err := bootstrapVerifyDomain(ctx, sesConnID, domain)
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_domain", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.DkimTokens = dkimTokens
	resp.DkimStatus = dkimStatus
	resp.DnsRecords = dkimCNAMERecords(domain, dkimTokens)
	resp.Steps = append(resp.Steps, bootstrapStep{
		Step: "ses_verify_domain", OK: true,
		Detail: fmt.Sprintf("%d dkim tokens", len(dkimTokens)),
	})

	if publishDNS {
		if isAppDepBound(ctx, "domains") {
			for i, tok := range dkimTokens {
				resp.Steps = append(resp.Steps, bootstrapPublishDNSRecord(
					ctx,
					fmt.Sprintf("dns_dkim_%d", i+1),
					domain,
					tok+"._domainkey",
					"CNAME",
					tok+".dkim.amazonses.com",
				))
			}
			if doInbound {
				resp.Steps = append(resp.Steps, bootstrapPublishDNSRecord(
					ctx, "dns_mx", domain, "@", "MX",
					"10 inbound-smtp."+region+".amazonaws.com",
				))
			}
			if publishSPF {
				resp.Steps = append(resp.Steps, bootstrapPublishDNSRecord(
					ctx, "dns_spf", domain, "@", "TXT",
					"v=spf1 include:amazonses.com ~all",
				))
			}
		} else {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step: "publish_dns", OK: true,
				Skipped: fmt.Sprintf("domains app not bound — publish %d DKIM CNAME(s) + MX/SPF manually", len(dkimTokens)),
			})
		}
	}

	if doInbound {
		if err := bootstrapCreateRuleSet(ctx, sesConnID, ruleSetName); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule_set", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule_set", OK: true})

		if err := bootstrapCreateReceiptRule(ctx, sesConnID, ruleSetName, ruleName, domain, bucketName, topicArn); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule", OK: true})

		if err := bootstrapActivateRuleSet(ctx, sesConnID, ruleSetName); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_active_receipt_rule_set", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_active_receipt_rule_set", OK: true})

		publicURL := ""
		if id != nil {
			publicURL = strings.TrimSuffix(strings.TrimSpace(id.PublicURL), "/")
		}
		if publicURL == "" {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step:  "sns_subscribe_webhook",
				OK:    false,
				Error: "platform PublicURL is unset — set Settings → Server → Public URL so SNS can reach /webhooks/ses-inbound",
			})
			return resp, nil
		}
		webhookURL := publicURL + "/api/apps/messaging/webhooks/ses-inbound?api_key=" + os.Getenv("APTEVA_APP_TOKEN")
		resp.Inbound.WebhookURL = webhookURL
		subArn, already, err := bootstrapSubscribeWebhook(ctx, snsBound.ConnectionID, topicArn, webhookURL)
		if err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "sns_subscribe_webhook", OK: false, Error: err.Error()})
			return resp, nil
		}
		resp.Inbound.SubscriptionARN = subArn
		step := bootstrapStep{Step: "sns_subscribe_webhook", OK: true, Detail: subArn}
		if already {
			step.Skipped = "subscription already exists"
		}
		resp.Steps = append(resp.Steps, step)
		resp.Inbound.Bootstrapped = true
	}

	resp.NextStep = sendersCreateNextStep(doInbound, isAppDepBound(ctx, "domains"))

	// Persist the local row. Provider state already mirrored; the
	// inbound block (if bootstrapped) gets serialised into
	// inbound_config so subsequent senders_get returns it without a
	// provider round-trip.
	a.persistSenderRow(ctx, pid, &senderUpsert{
		ProjectID:           pid,
		Channel:             "email",
		Address:             domain,
		Kind:                "domain",
		DisplayName:         req.DisplayName,
		Provider:            "aws-ses",
		ProviderIdentityID:  domain,
		Verified:            strings.EqualFold(resp.DkimStatus, "SUCCESS"),
		VerificationStatus:  domainVerificationStatus(resp.DkimStatus),
		SendingEnabled:      true,
		DkimStatus:          resp.DkimStatus,
		InboundBootstrapped: resp.Inbound != nil && resp.Inbound.Bootstrapped,
		InboundConfig:       inboundConfigJSON(resp.Inbound),
		MarkSyncedNow:       true,
	}, resp)
	return resp, nil
}

// domainVerificationStatus maps SES's DkimAttributes.Status to our
// internal verification_status enum.
func domainVerificationStatus(dkimStatus string) string {
	switch strings.ToUpper(strings.TrimSpace(dkimStatus)) {
	case "SUCCESS":
		return "verified"
	case "FAILED":
		return "failed"
	case "":
		return "pending"
	default:
		return "pending"
	}
}

// inboundConfigJSON serialises the panel-friendly Inbound block into
// the JSON shape we store in senders.inbound_config. Returns "" when
// not bootstrapped.
func inboundConfigJSON(inb *sendersCreateInbound) string {
	if inb == nil || !inb.Bootstrapped {
		return ""
	}
	cfg := map[string]any{
		"bucket":           inb.BucketName,
		"topic_arn":        inb.TopicARN,
		"account_id":       inb.AccountID,
		"webhook_url":      inb.WebhookURL,
		"subscription_arn": inb.SubscriptionARN,
		"region":           inb.Region,
		"rule_set_name":    inb.RuleSetName,
		"rule_name":        inb.RuleName,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(b)
}

// persistSenderRow upserts the local row + optionally flips the
// default flag when req.SetDefault is true. Failures get appended as
// a non-fatal "persist_local" step — the provider work already
// succeeded so we don't roll that back; the next senders_refresh
// will reconcile.
func (a *App) persistSenderRow(ctx *sdk.AppCtx, pid string, u *senderUpsert, resp *sendersCreateResp) {
	db := ctx.AppDB()
	if db == nil {
		return
	}
	id, err := dbUpsertSender(db, u)
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "persist_local", OK: false, Error: err.Error()})
		return
	}
	step := bootstrapStep{Step: "persist_local", OK: true, Detail: fmt.Sprintf("sender id=%d", id)}
	// The req.SetDefault path is only triggered by the caller passing
	// set_default=true; pass it down via resp.NextStep semantics — we
	// don't have direct access to the req here, so the orchestrators
	// already account for it before calling persistSenderRow if
	// needed. Future: thread req through.
	resp.Steps = append(resp.Steps, step)
}

// sendersCreatePhone — Twilio branch. Adopts an already-purchased
// phone number into the local senders table, optionally wiring its
// SMS webhook URL at /webhooks/twilio-inbound.
//
// Today the Twilio integration doesn't expose a "purchase a fresh
// number" flow from senders_create — that path is left to the
// twilio.buy_phone_number tool. senders_create is the adoption /
// configuration entry point.
func (a *App) sendersCreatePhone(ctx *sdk.AppCtx, pid, channel, addr string, req sendersCreateReq) (*sendersCreateResp, error) {
	if channel == "whatsapp" {
		// Twilio WhatsApp senders live behind a separate API (
		// list_whatsapp_senders / register_whatsapp_sender ) with
		// approval workflow. Out of scope for v0.9 — send_message
		// channel=whatsapp already works for outbound against an
		// already-approved WhatsApp number.
		return nil, errors.New("channel=whatsapp adoption not yet supported in senders_create — register the WhatsApp sender via twilio.register_whatsapp_sender, then call senders_create again")
	}
	if addr == "" || !strings.HasPrefix(addr, "+") || !allDigits(addr[1:]) {
		return nil, fmt.Errorf("phone address must be E.164 (e.g. +15551234567), got %q", addr)
	}
	phoneBound := ctx.IntegrationFor("phone_provider")
	if phoneBound == nil {
		return nil, errors.New("phone_provider (twilio) not bound")
	}

	resp := &sendersCreateResp{
		Address: addr,
		Kind:    "phone",
		Pending: false,
	}

	// 1. Look up the phone in the Twilio account.
	listRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(phoneBound.ConnectionID, "list_phone_numbers", map[string]any{
		"PhoneNumber": addr,
		"PageSize":    50,
	})
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_list_phone_numbers", OK: false, Error: err.Error()})
		return resp, nil
	}
	if listRes == nil || !listRes.Success {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_list_phone_numbers", OK: false, Error: truncateResData(listRes)})
		return resp, nil
	}
	var listed struct {
		IncomingPhoneNumbers []struct {
			SID          string `json:"sid"`
			PhoneNumber  string `json:"phone_number"`
			FriendlyName string `json:"friendly_name"`
			SmsURL       string `json:"sms_url"`
			SmsMethod    string `json:"sms_method"`
		} `json:"incoming_phone_numbers"`
	}
	if err := json.Unmarshal(listRes.Data, &listed); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_list_phone_numbers", OK: false, Error: "parse: " + err.Error()})
		return resp, nil
	}
	var match *struct {
		SID          string `json:"sid"`
		PhoneNumber  string `json:"phone_number"`
		FriendlyName string `json:"friendly_name"`
		SmsURL       string `json:"sms_url"`
		SmsMethod    string `json:"sms_method"`
	}
	for i := range listed.IncomingPhoneNumbers {
		if listed.IncomingPhoneNumbers[i].PhoneNumber == addr {
			match = &listed.IncomingPhoneNumbers[i]
			break
		}
	}
	if match == nil {
		resp.Steps = append(resp.Steps, bootstrapStep{
			Step: "twilio_list_phone_numbers", OK: false,
			Error: fmt.Sprintf("phone %s not found in the bound Twilio account — buy it via twilio.buy_phone_number first", addr),
		})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{
		Step: "twilio_list_phone_numbers", OK: true,
		Detail: fmt.Sprintf("sid=%s", match.SID),
	})

	// 2. Decide inbound mode. For Twilio "auto" is true whenever the
	//    phone exists and PublicURL is set — no extra integrations
	//    required.
	id, _ := ctx.PlatformAPI().WhoAmI()
	publicURL := ""
	if id != nil {
		publicURL = strings.TrimSuffix(strings.TrimSpace(id.PublicURL), "/")
	}
	mode := strings.ToLower(strings.TrimSpace(req.Inbound))
	doInbound := false
	skipReason := ""
	switch mode {
	case "", "auto":
		if publicURL == "" {
			skipReason = "auto: platform PublicURL is unset"
		} else {
			doInbound = true
		}
	case "true", "yes", "1":
		if publicURL == "" {
			return nil, errors.New("inbound=true but platform PublicURL is unset — set Settings → Server → Public URL")
		}
		doInbound = true
	case "false", "no", "0":
		skipReason = "inbound=false"
	default:
		return nil, fmt.Errorf("invalid inbound value %q (use auto|true|false)", req.Inbound)
	}
	resp.Inbound = &sendersCreateInbound{Bootstrapped: false, SkippedReason: skipReason}

	// 3. Set the SMS webhook URL on the phone number (if requested).
	if doInbound {
		webhookURL := publicURL + "/api/apps/messaging/webhooks/twilio-inbound?api_key=" + os.Getenv("APTEVA_APP_TOKEN")
		if match.SmsURL == webhookURL && strings.EqualFold(match.SmsMethod, "POST") {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step: "twilio_update_phone_number", OK: true,
				Skipped: "webhook already pointed at messaging",
			})
		} else {
			updRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(phoneBound.ConnectionID, "update_phone_number", map[string]any{
				"PhoneNumberSid": match.SID,
				"SmsUrl":         webhookURL,
				// SmsMethod isn't in the integration's schema today;
				// Twilio defaults to POST when SmsUrl is set without
				// SmsMethod, so this is fine.
			})
			if err != nil {
				resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_update_phone_number", OK: false, Error: err.Error()})
				return resp, nil
			}
			if updRes == nil || !updRes.Success {
				resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_update_phone_number", OK: false, Error: truncateResData(updRes)})
				return resp, nil
			}
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_update_phone_number", OK: true, Detail: "sms_url=" + webhookURL})
		}
		resp.Inbound.WebhookURL = webhookURL
		resp.Inbound.Bootstrapped = true
	}

	// 4. Persist local row.
	inboundCfg := ""
	if resp.Inbound != nil && resp.Inbound.Bootstrapped {
		cfg := map[string]any{
			"sms_url":          resp.Inbound.WebhookURL,
			"previous_sms_url": match.SmsURL,
		}
		if b, err := json.Marshal(cfg); err == nil {
			inboundCfg = string(b)
		}
	}
	a.persistSenderRow(ctx, pid, &senderUpsert{
		ProjectID:           pid,
		Channel:             channel,
		Address:             addr,
		Kind:                "phone",
		DisplayName:         req.DisplayName,
		Provider:            "twilio",
		ProviderIdentityID:  match.SID,
		Verified:            true, // Twilio phones are usable from the moment of purchase.
		VerificationStatus:  "verified",
		SendingEnabled:      true,
		InboundBootstrapped: resp.Inbound != nil && resp.Inbound.Bootstrapped,
		InboundConfig:       inboundCfg,
		MarkSyncedNow:       true,
	}, resp)

	if doInbound {
		resp.NextStep = "Phone " + addr + " is ready to send + receive SMS via messaging."
	} else {
		resp.NextStep = "Phone " + addr + " adopted. Inbound webhook not wired — set inbound=true to point Twilio at /webhooks/twilio-inbound."
	}
	return resp, nil
}

// resolveInboundMode returns (doInbound, skipReason, err).
//
//	mode=""|auto → opt-in to inbound when both S3 + SNS are bound
//	mode=true     → hard-require S3 + SNS, fail clearly otherwise
//	mode=false    → never run inbound
func resolveInboundMode(mode string, s3Bound, snsBound *sdk.BoundIntegration) (bool, string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		missing := []string{}
		if s3Bound == nil {
			missing = append(missing, "inbound_storage (aws-s3)")
		}
		if snsBound == nil {
			missing = append(missing, "inbound_notifications (aws-sns)")
		}
		if len(missing) > 0 {
			return false, "auto: " + strings.Join(missing, " + ") + " not bound", nil
		}
		return true, "", nil
	case "true", "yes", "1":
		if s3Bound == nil {
			return false, "", errors.New("inbound=true but inbound_storage (aws-s3) not bound")
		}
		if snsBound == nil {
			return false, "", errors.New("inbound=true but inbound_notifications (aws-sns) not bound")
		}
		return true, "", nil
	case "false", "no", "0":
		return false, "inbound=false", nil
	default:
		return false, "", fmt.Errorf("invalid inbound value %q (use auto|true|false)", mode)
	}
}

func sendersCreateNextStep(doInbound, domainsBound bool) string {
	if doInbound {
		if domainsBound {
			return "Wait 5–30min for DNS propagation, then call senders_get to confirm dkim_status=Success. Inbound mail to the domain is wired."
		}
		return "Publish the DKIM CNAMEs + MX record in your registrar. Once propagated, senders_get reports dkim_status=Success and inbound mail starts flowing."
	}
	if domainsBound {
		return "Wait 5–30min for DNS propagation, then call senders_get to confirm dkim_status=Success."
	}
	return "Publish the DKIM records above in your registrar, then call senders_get once propagated."
}

// ─── Per-step helpers — shared with the unified flow above ────────────

func bootstrapCreateSNSTopic(ctx *sdk.AppCtx, connID int64, name string) (string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "create_topic", map[string]any{
		"Name": name,
	})
	if err != nil {
		return "", fmt.Errorf("create_topic: %w", err)
	}
	if res == nil || !res.Success {
		return "", fmt.Errorf("create_topic non-2xx: %s", truncateResData(res))
	}
	return parseFirstSNSARN(string(res.Data), "TopicArn"), nil
}

// parseFirstSNSARN walks either parsed-XML-as-JSON or raw text looking
// for the named ARN field. xmlToJson on the integrations side flattens
// some shapes; we accept either.
func parseFirstSNSARN(body, field string) string {
	var probe map[string]any
	_ = json.Unmarshal([]byte(body), &probe)
	if v := walkForString(probe, field); v != "" {
		return v
	}
	if idx := strings.Index(body, "arn:aws:sns:"); idx >= 0 {
		end := idx
		for end < len(body) {
			c := body[end]
			ok := (c >= 'a' && c <= 'z') ||
				(c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') ||
				c == ':' || c == '-' || c == '_' || c == '.' || c == '/'
			if !ok {
				break
			}
			end++
		}
		return body[idx:end]
	}
	return ""
}

// walkForString depth-first searches a JSON-decoded tree for the first
// value at any leaf with the named key.
func walkForString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if got, ok := x[key].(string); ok && got != "" {
			return got
		}
		for _, child := range x {
			if got := walkForString(child, key); got != "" {
				return got
			}
		}
	case []any:
		for _, child := range x {
			if got := walkForString(child, key); got != "" {
				return got
			}
		}
	}
	return ""
}

func parseAccountFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

func snsTopicPolicy(topicArn, accountID string) string {
	cond := ""
	if accountID != "" {
		cond = fmt.Sprintf(`,"Condition":{"StringEquals":{"AWS:SourceAccount":"%s"}}`, accountID)
	}
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"AllowSESPublish","Effect":"Allow","Principal":{"Service":"ses.amazonaws.com"},"Action":"sns:Publish","Resource":"%s"%s}]}`, topicArn, cond)
}

func bootstrapSetSNSPolicy(ctx *sdk.AppCtx, connID int64, topicArn, policy string) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "set_topic_attributes", map[string]any{
		"TopicArn":       topicArn,
		"AttributeName":  "Policy",
		"AttributeValue": policy,
	})
	if err != nil {
		return fmt.Errorf("set_topic_attributes: %w", err)
	}
	if res == nil || !res.Success {
		return fmt.Errorf("set_topic_attributes non-2xx: %s", truncateResData(res))
	}
	return nil
}

func bootstrapCreateS3Bucket(ctx *sdk.AppCtx, connID int64, bucket, region string) error {
	body := ""
	if region != "us-east-1" {
		body = fmt.Sprintf(`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>%s</LocationConstraint></CreateBucketConfiguration>`, region)
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "create_bucket", map[string]any{
		"bucket": bucket,
		"body":   body,
	})
	if err != nil {
		return fmt.Errorf("create_bucket: %w", err)
	}
	if res != nil && res.Success {
		return nil
	}
	if res != nil {
		raw := string(res.Data)
		if strings.Contains(raw, "BucketAlreadyOwnedByYou") {
			return nil
		}
		return fmt.Errorf("create_bucket non-2xx: %s", truncate(raw, 400))
	}
	return errors.New("create_bucket: nil result")
}

func s3BucketPolicy(bucket, accountID string) string {
	cond := ""
	if accountID != "" {
		cond = fmt.Sprintf(`,"Condition":{"StringEquals":{"AWS:SourceAccount":"%s"}}`, accountID)
	}
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"AllowSESPuts","Effect":"Allow","Principal":{"Service":"ses.amazonaws.com"},"Action":"s3:PutObject","Resource":"arn:aws:s3:::%s/*"%s}]}`, bucket, cond)
}

func bootstrapSetS3BucketPolicy(ctx *sdk.AppCtx, connID int64, bucket, policy string) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "put_bucket_policy", map[string]any{
		"bucket": bucket,
		"policy": policy,
	})
	if err != nil {
		return fmt.Errorf("put_bucket_policy: %w", err)
	}
	if res == nil || !res.Success {
		return fmt.Errorf("put_bucket_policy non-2xx: %s", truncateResData(res))
	}
	return nil
}

func bootstrapVerifyDomain(ctx *sdk.AppCtx, connID int64, domain string) ([]string, string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "verify_domain", map[string]any{
		"EmailIdentity": domain,
	})
	if err != nil {
		return nil, "", fmt.Errorf("verify_domain: %w", err)
	}
	if res == nil || !res.Success {
		return nil, "", fmt.Errorf("verify_domain non-2xx: %s", truncateResData(res))
	}
	var probe struct {
		DkimAttributes struct {
			Tokens []string `json:"Tokens"`
			Status string   `json:"Status"`
		} `json:"DkimAttributes"`
	}
	_ = json.Unmarshal(res.Data, &probe)
	return probe.DkimAttributes.Tokens, probe.DkimAttributes.Status, nil
}

func bootstrapPublishDNSRecord(ctx *sdk.AppCtx, step, domain, name, recType, value string) bootstrapStep {
	args := map[string]any{
		"domain": domain,
		"name":   name,
		"type":   recType,
		"value":  value,
		"ttl":    1800,
	}
	var probe struct {
		Action string `json:"action"`
		Error  string `json:"error"`
	}
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", args, &probe); err != nil {
		return bootstrapStep{Step: step, Error: err.Error()}
	}
	if probe.Error != "" {
		return bootstrapStep{Step: step, Error: probe.Error}
	}
	return bootstrapStep{Step: step, OK: true, Detail: probe.Action}
}

func bootstrapCreateRuleSet(ctx *sdk.AppCtx, connID int64, name string) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "create_receipt_rule_set", map[string]any{
		"RuleSetName": name,
	})
	if err != nil {
		return fmt.Errorf("create_receipt_rule_set: %w", err)
	}
	if res != nil && res.Success {
		return nil
	}
	if res != nil {
		raw := string(res.Data)
		if strings.Contains(raw, "AlreadyExists") {
			return nil
		}
		return fmt.Errorf("create_receipt_rule_set non-2xx: %s", truncate(raw, 400))
	}
	return errors.New("create_receipt_rule_set: nil result")
}

func bootstrapCreateReceiptRule(ctx *sdk.AppCtx, connID int64, ruleSetName, ruleName, domain, bucket, topicArn string) error {
	args := map[string]any{
		"RuleSetName":                               ruleSetName,
		"Rule.Name":                                 ruleName,
		"Rule.Enabled":                              "true",
		"Rule.ScanEnabled":                          "true",
		"Rule.Recipients.member.1":                  domain,
		"Rule.Actions.member.1.S3Action.BucketName": bucket,
		"Rule.Actions.member.1.S3Action.TopicArn":   topicArn,
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "create_receipt_rule", args)
	if err != nil {
		return fmt.Errorf("create_receipt_rule: %w", err)
	}
	if res != nil && res.Success {
		return nil
	}
	if res != nil {
		raw := string(res.Data)
		if strings.Contains(raw, "AlreadyExists") {
			return nil
		}
		return fmt.Errorf("create_receipt_rule non-2xx: %s", truncate(raw, 400))
	}
	return errors.New("create_receipt_rule: nil result")
}

func bootstrapActivateRuleSet(ctx *sdk.AppCtx, connID int64, name string) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "set_active_receipt_rule_set", map[string]any{
		"RuleSetName": name,
	})
	if err != nil {
		return fmt.Errorf("set_active_receipt_rule_set: %w", err)
	}
	if res == nil || !res.Success {
		return fmt.Errorf("set_active_receipt_rule_set non-2xx: %s", truncateResData(res))
	}
	return nil
}

func bootstrapSubscribeWebhook(ctx *sdk.AppCtx, connID int64, topicArn, endpoint string) (string, bool, error) {
	listRes, listErr := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_subscriptions_by_topic", map[string]any{
		"TopicArn": topicArn,
	})
	if listErr == nil && listRes != nil && listRes.Success && strings.Contains(string(listRes.Data), endpoint) {
		return "", true, nil
	}
	subRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "subscribe", map[string]any{
		"TopicArn":              topicArn,
		"Protocol":              "https",
		"Endpoint":              endpoint,
		"ReturnSubscriptionArn": "true",
	})
	if err != nil {
		return "", false, fmt.Errorf("subscribe: %w", err)
	}
	if subRes == nil || !subRes.Success {
		return "", false, fmt.Errorf("subscribe non-2xx: %s", truncateResData(subRes))
	}
	return parseFirstSNSARN(string(subRes.Data), "SubscriptionArn"), false, nil
}

func truncateResData(res *sdk.ExecuteResult) string {
	if res == nil {
		return "(nil)"
	}
	return truncate(string(res.Data), 400)
}
