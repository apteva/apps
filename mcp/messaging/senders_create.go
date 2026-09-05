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
//       - publish DKIM CNAMEs (+ SPF/DMARC/custom MAIL FROM if enabled)
//         via the domains app
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
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	DMARC       *bool  `json:"dmarc"`         // domain only; default true
	MailFrom    *bool  `json:"mail_from"`     // domain only; default true
	Region      string `json:"region"`        // default eu-west-1 (inbound only)
	MailFromSub string `json:"mail_from_sub"` // default "mail"
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
	Address    string              `json:"address"`
	Kind       string              `json:"kind"` // "email" | "domain"
	Pending    bool                `json:"pending"`
	NextStep   string              `json:"next_step,omitempty"`
	DkimTokens []string            `json:"dkim_tokens,omitempty"`
	DkimStatus string              `json:"dkim_status,omitempty"`
	DnsRecords []map[string]string `json:"dns_records,omitempty"`
	// DnsPublishPartial is true when one or more dns_* publish steps
	// failed. Top-level signal so callers/UI can show a warning without
	// scanning the per-step list — the buried ✗ used to be masked by a
	// green dkim_status (which reflects SES, not whether DNS landed).
	DnsPublishPartial bool                  `json:"dns_publish_partial,omitempty"`
	Inbound           *sendersCreateInbound `json:"inbound,omitempty"`
	Steps             []bootstrapStep       `json:"steps"`
}

// HTTP entry point — POST /senders/create.
func (a *App) handleSendersCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body sendersCreateReq
	if err := decodeJSONRequest(w, r, &body, maxControlRequestBytes); err != nil {
		httpJSONDecodeError(w, err)
		return
	}
	// Global-scope installs have no APTEVA_PROJECT_ID env; resolve
	// project_id from the query string so the panel and curl callers
	// both work. sendersCreateImpl's env-fallback handles the
	// project-scope case where this returns "".
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		body.ProjectID = pid
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
		Channel:     strArg(args, "channel"),
		Inbound:     strArg(args, "inbound"),
		Region:      strArg(args, "region"),
		MailFromSub: strArg(args, "mail_from_sub"),
		BucketName:  strArg(args, "bucket_name"),
		TopicName:   strArg(args, "topic_name"),
		RuleSetName: strArg(args, "rule_set_name"),
		RuleName:    strArg(args, "rule_name"),
		DisplayName: strArg(args, "display_name"),
	}
	if v, ok := args["publish_dns"].(bool); ok {
		body.PublishDNS = &v
	}
	if v, ok := args["spf"].(bool); ok {
		body.SPF = &v
	}
	if v, ok := args["dmarc"].(bool); ok {
		body.DMARC = &v
	}
	if v, ok := args["mail_from"].(bool); ok {
		body.MailFrom = &v
	}
	if v, ok := args["set_default"].(bool); ok {
		body.SetDefault = v
	}
	// Global-scope installs have no APTEVA_PROJECT_ID env, so
	// sibling-app callers (CRM, agents) inject _project_id per the
	// platform convention. Every other senders_* tool routes through
	// resolveProjectFromArgs; the create path was the lone omission,
	// which made it the only senders tool that 500'd on global
	// installs ("project_id required"). The error returned here is
	// silently absorbed when neither env nor arg yields a project —
	// sendersCreateImpl will surface its own clearer error.
	if pid, err := resolveProjectFromArgs(args); err == nil {
		body.ProjectID = pid
	}
	return a.sendersCreateImpl(ctx, body)
}

func (a *App) sendersCreateImpl(ctx *sdk.AppCtx, req sendersCreateReq) (result *sendersCreateResp, resultErr error) {
	defer func() {
		if resultErr != nil || result == nil || !req.SetDefault {
			return
		}
		pid := req.ProjectID
		if pid == "" {
			pid = os.Getenv("APTEVA_PROJECT_ID")
		}
		channel := req.Channel
		if channel == "" {
			channel = inferChannelFromAddress(req.Address)
		}
		if channel == "" {
			channel = "email"
		}
		if err := dbSetDefaultSender(ctx.AppDB(), pid, channel, strings.ToLower(stripScheme(req.Address))); err != nil {
			resultErr = fmt.Errorf("set default sender: %w", err)
		}
	}()
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
		// When the Domains app is bound, drive verification through the
		// parent domain's DKIM instead of the per-mailbox click-link
		// flow. SES inherits — every mailbox at a DKIM-verified domain
		// can send, signed with the domain's keys. Without the Domains
		// app, fall back to the legacy verify_email path (still useful
		// for mailboxes at domains the operator doesn't control).
		if isAppDepBound(ctx, "domains") {
			return a.sendersCreateEmailViaParentDomain(ctx, pid, sesBound.ConnectionID, raw, req, resp)
		}
		return a.sendersCreateEmail(ctx, pid, sesBound.ConnectionID, raw, req, resp)
	}
	return a.sendersCreateDomain(ctx, pid, sesBound.ConnectionID, raw, req, resp)
}

// sendersCreateEmailViaParentDomain handles the "alice@acme.com" case
// when the Domains app is bound. Resolves the parent (acme.com),
// ensures it's verified through the domain flow (skipping the work
// when a verified row already exists), then registers the mailbox row
// as inherited-verified. No per-mailbox click-link.
func (a *App) sendersCreateEmailViaParentDomain(ctx *sdk.AppCtx, pid string, sesConnID int64, addr string, req sendersCreateReq, resp *sendersCreateResp) (*sendersCreateResp, error) {
	parts := strings.SplitN(addr, "@", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("malformed email %q", addr)
	}
	parent := strings.ToLower(parts[1])

	// v0.12: parent lives in identities (kind=email_domain), not in
	// senders. Verified state on the identity row is the inheritance
	// signal.
	parentIdentity, _ := dbFindIdentity(ctx.AppDB(), pid, "email_domain", parent)
	parentReady := parentIdentity != nil && parentIdentity.Verified && parentIdentity.DeletedAt == nil

	if !parentReady {
		// Run the full domain verification on the parent. Reuses
		// sendersCreateDomain's idempotent path — DKIM tokens, DNS
		// publish via Domains app, optional inbound bootstrap.
		// sendersCreateDomain writes the parent into identities, so
		// after this returns we can re-fetch to grab the FK target.
		domainResp := &sendersCreateResp{Address: parent, Kind: "domain", Pending: true}
		if _, err := a.sendersCreateDomain(ctx, pid, sesConnID, parent, req, domainResp); err != nil {
			return nil, err
		}
		resp.DkimTokens = domainResp.DkimTokens
		resp.DkimStatus = domainResp.DkimStatus
		resp.DnsRecords = domainResp.DnsRecords
		resp.DnsPublishPartial = domainResp.DnsPublishPartial
		resp.Inbound = domainResp.Inbound
		resp.Steps = append(resp.Steps, domainResp.Steps...)
		parentIdentity, _ = dbFindIdentity(ctx.AppDB(), pid, "email_domain", parent)
	} else {
		resp.Steps = append(resp.Steps, bootstrapStep{
			Step:   "parent_domain_already_verified",
			OK:     true,
			Detail: fmt.Sprintf("%s is already DKIM-verified — %s inherits", parent, addr),
		})
		resp.DkimStatus = "SUCCESS"
	}

	// The mailbox inherits the parent domain's ACTUAL verification state,
	// not a hard-coded SUCCESS. Persisting verified=true while the parent
	// is still pending (or its DKIM DNS failed to publish) was the "lie
	// about success" bug — the panel showed green for a mailbox that
	// couldn't actually send yet.
	var parentID int64
	parentVerified := false
	if parentIdentity != nil {
		parentID = parentIdentity.ID
		parentVerified = parentIdentity.Verified && parentIdentity.DeletedAt == nil
	}
	mboxStatus := "pending"
	mboxDkim := resp.DkimStatus
	if parentVerified {
		mboxStatus = "verified"
		if mboxDkim == "" {
			mboxDkim = "SUCCESS"
		}
	}
	a.persistSenderRow(ctx, pid, &senderUpsert{
		ProjectID:          pid,
		Channel:            "email",
		Address:            addr,
		Kind:               "email_mailbox",
		DisplayName:        req.DisplayName,
		Provider:           "aws-ses",
		ProviderIdentityID: addr,
		Verified:           parentVerified,
		VerificationStatus: mboxStatus,
		SendingEnabled:     true,
		DkimStatus:         mboxDkim,
		ParentIdentityID:   parentID,
		MarkSyncedNow:      true,
	}, resp)
	if parentVerified {
		resp.Pending = false
		resp.NextStep = fmt.Sprintf("inherits DKIM from %s — ready to send", parent)
	} else {
		resp.Pending = true
		resp.NextStep = fmt.Sprintf("%s registered; waiting on %s DKIM verification — check senders_get once DNS propagates.", addr, parent)
		if resp.DnsPublishPartial {
			resp.NextStep = "Some DNS records failed to publish — review the dns_* steps and re-run senders_create. " + resp.NextStep
		}
	}
	return resp, nil
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

func (a *App) sendersCreateEmail(ctx *sdk.AppCtx, pid string, connID int64, addr string, req sendersCreateReq, resp *sendersCreateResp) (*sendersCreateResp, error) {
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
		Kind:               "email_mailbox",
		DisplayName:        req.DisplayName,
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
	publishDMARC := true
	if req.DMARC != nil {
		publishDMARC = *req.DMARC
	}
	configureMailFrom := true
	if req.MailFrom != nil {
		configureMailFrom = *req.MailFrom
	}
	mailFromSub := strings.ToLower(strings.TrimSpace(req.MailFromSub))
	if mailFromSub == "" {
		mailFromSub = "mail"
	}
	if strings.ContainsAny(mailFromSub, " @/\t\r\n") || strings.Trim(mailFromSub, ".") == "" {
		return nil, fmt.Errorf("invalid mail_from_sub %q", req.MailFromSub)
	}
	mailFromSub = strings.Trim(mailFromSub, ".")
	mailFromDomain := mailFromSub + "." + domain

	s3Bound := ctx.IntegrationFor("inbound_storage")
	snsBound := ctx.IntegrationFor("inbound_notifications")
	doInbound, skipReason, err := resolveInboundMode(req.Inbound, s3Bound, snsBound)
	if err != nil {
		return nil, err
	}
	connections := []int64{sesConnID}
	if doInbound && s3Bound != nil {
		connections = append(connections, s3Bound.ConnectionID)
	}
	if doInbound && snsBound != nil {
		connections = append(connections, snsBound.ConnectionID)
	}
	region, err = validateProviderRegions(ctx, req.Region, connections...)
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
		active := ""
		if doInbound {
			var err error
			active, err = activeReceiptRuleSet(ctx, sesConnID)
			if err != nil {
				return nil, err
			}
		}
		ruleSetName = active
		if ruleSetName == "" {
			ruleSetName = "apteva-default"
		}
	}
	ruleName := req.RuleName
	if ruleName == "" {
		// Per-install rule name. Before v0.12.5 this was the constant
		// "messaging-inbound" — first install to bootstrap "claimed"
		// the rule, every other install hit AlreadyExists and silently
		// no-op'd while still persisting inbound_bootstrapped=1
		// locally. Suffixing with install_id lets every install own
		// its own rule inside the shared rule set; the merge logic in
		// bootstrapCreateReceiptRule handles same-install re-runs.
		ruleName = "messaging-inbound"
		if iid, err := ctx.PlatformAPI().WhoAmI(); err == nil && iid != nil && iid.InstallID > 0 {
			ruleName = fmt.Sprintf("messaging-inbound-%d", iid.InstallID)
		}
	}
	doEvents := snsBound != nil
	if doInbound || doEvents {
		resp.Inbound.BucketName = bucketName
		resp.Inbound.RuleSetName = ruleSetName
		resp.Inbound.RuleName = ruleName
	}

	// SNS topic + policy first (so we know the account id before
	// writing the S3 bucket policy). The same topic receives both SES
	// inbound S3 notifications and outbound engagement events.
	var topicArn, accountID string
	if doInbound || doEvents {
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
		// Persist the authorized topic before later setup steps. If S3,
		// SES identity, DNS, or subscription work fails, signed callbacks
		// from any unrelated SNS topic must still be rejected.
		persistSNSTopicAuthorization(ctx, pid, domain, resp)
	}

	if doInbound {
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

	// SES verify_domain — outbound + inbound both need DKIM. Idempotent:
	// if the identity already exists in SES, bootstrapVerifyDomain
	// adopts it and returns the existing DKIM tokens.
	dkimTokens, dkimStatus, adopted, reprobed, err := bootstrapVerifyDomain(ctx, sesConnID, domain)
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_domain", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.DkimTokens = dkimTokens
	resp.DkimStatus = dkimStatus
	resp.DnsRecords = dkimCNAMERecords(domain, dkimTokens)
	if doInbound {
		resp.DnsRecords = append(resp.DnsRecords, map[string]string{"name": domain, "type": "MX", "value": "10 inbound-smtp." + region + ".amazonaws.com"})
	}
	if publishSPF {
		resp.DnsRecords = append(resp.DnsRecords, map[string]string{
			"name":  domain,
			"type":  "TXT",
			"value": "v=spf1 include:amazonses.com ~all",
		})
	}
	if publishDMARC {
		resp.DnsRecords = append(resp.DnsRecords, map[string]string{
			"name":  "_dmarc." + domain,
			"type":  "TXT",
			"value": defaultDMARCRecord(domain),
		})
	}
	if configureMailFrom {
		resp.DnsRecords = append(resp.DnsRecords,
			map[string]string{
				"name":  mailFromDomain,
				"type":  "MX",
				"value": "10 feedback-smtp." + region + ".amazonses.com",
			},
			map[string]string{
				"name":  mailFromDomain,
				"type":  "TXT",
				"value": "v=spf1 include:amazonses.com ~all",
			},
		)
	}
	detail := fmt.Sprintf("%d dkim tokens", len(dkimTokens))
	if adopted {
		detail += " (adopted existing identity)"
	}
	if reprobed {
		detail += " — re-probed stuck DKIM (delete+recreate)"
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_domain", OK: true, Detail: detail})
	metadata := domainSetupMetadata(domain, region, mailFromSub, mailFromDomain, publishDMARC, configureMailFrom)
	persistDomainIdentityWithMetadata(ctx, pid, domain, resp, false, metadata, "persist_domain_identity")

	if configureMailFrom {
		resp.Steps = append(resp.Steps, bootstrapSetMailFrom(ctx, sesConnID, domain, mailFromDomain))
	} else if adopted && !reprobed {
		// Explicit opt-out preserves the old cleanup behavior for
		// deployments that do not want custom MAIL FROM on Messaging domains.
		resp.Steps = append(resp.Steps, bootstrapClearMailFrom(ctx, sesConnID, domain))
	}

	if doEvents {
		if err := bootstrapConfigureSESEvents(ctx, sesConnID, topicArn); err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_event_publishing", OK: false, Error: err.Error()})
		} else {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step:   "ses_event_publishing",
				OK:     true,
				Detail: sesEventConfigurationSetName + " -> " + topicArn,
			})
		}
	} else {
		resp.Steps = append(resp.Steps, bootstrapStep{
			Step: "ses_event_publishing", OK: true,
			Skipped: "inbound_notifications (aws-sns) not bound — SES open/click/delivery events will not be published",
		})
	}

	if publishDNS {
		if resolveDNSPublisher(ctx, domain) != "" {
			for i, tok := range dkimTokens {
				st := bootstrapPublishDNSRecord(
					ctx, pid,
					fmt.Sprintf("dns_dkim_%d", i+1),
					domain,
					tok+"._domainkey",
					"CNAME",
					tok+".dkim.amazonses.com",
				)
				if !st.OK {
					resp.DnsPublishPartial = true
				}
				resp.Steps = append(resp.Steps, st)
			}

			if publishSPF {
				st := bootstrapPublishDNSRecord(
					ctx, pid, "dns_spf", domain, "@", "TXT",
					"v=spf1 include:amazonses.com ~all",
				)
				if !st.OK {
					resp.DnsPublishPartial = true
				}
				resp.Steps = append(resp.Steps, st)
			}
			if publishDMARC {
				st := bootstrapPublishDNSRecord(
					ctx, pid, "dns_dmarc", domain, "_dmarc", "TXT",
					defaultDMARCRecord(domain),
				)
				if !st.OK {
					resp.DnsPublishPartial = true
				}
				resp.Steps = append(resp.Steps, st)
			}
			if configureMailFrom {
				st := bootstrapPublishDNSRecord(
					ctx, pid, "dns_mail_from_mx", domain, mailFromSub, "MX",
					"10 feedback-smtp."+region+".amazonses.com",
				)
				if !st.OK {
					resp.DnsPublishPartial = true
				}
				resp.Steps = append(resp.Steps, st)
				st = bootstrapPublishDNSRecord(
					ctx, pid, "dns_mail_from_spf", domain, mailFromSub, "TXT",
					"v=spf1 include:amazonses.com ~all",
				)
				if !st.OK {
					resp.DnsPublishPartial = true
				}
				resp.Steps = append(resp.Steps, st)
			}
		} else {
			recordCount := len(dkimTokens)
			if publishSPF {
				recordCount++
			}
			if publishDMARC {
				recordCount++
			}
			if configureMailFrom {
				recordCount += 2
			}
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step: "publish_dns", OK: true,
				Skipped: fmt.Sprintf("domains app not bound — publish %d DNS record(s) manually", recordCount),
			})
		}
	}

	publicURL := ""
	includeWebhookProjectID := false
	if id != nil {
		publicURL, err = messagingWebhookPublicURL(ctx, id)
		if err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "resolve_webhook_public_url", OK: false, Error: err.Error()})
			publicURL = ""
		}
		includeWebhookProjectID = strings.TrimSpace(id.ProjectID) != ""
	}

	if doEvents {
		if publicURL == "" {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step:  "sns_subscribe_events_webhook",
				OK:    false,
				Error: "platform PublicURL is unset — set Settings → Server → Public URL so SNS can reach /webhooks/ses-bounces",
			})
		} else {
			eventsWebhookURL := messagingWebhookURL(publicURL, "/webhooks/ses-bounces", pid, includeWebhookProjectID)
			subArn, already, err := bootstrapSubscribeWebhook(ctx, snsBound.ConnectionID, topicArn, eventsWebhookURL)
			if err != nil {
				resp.Steps = append(resp.Steps, bootstrapStep{Step: "sns_subscribe_events_webhook", OK: false, Error: err.Error()})
			} else {
				step := bootstrapStep{Step: "sns_subscribe_events_webhook", OK: true, Detail: subArn}
				if already {
					step.Skipped = "subscription already exists"
				}
				resp.Steps = append(resp.Steps, step)
			}
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

		if publicURL == "" {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step:  "sns_subscribe_webhook",
				OK:    false,
				Error: "platform PublicURL is unset — set Settings → Server → Public URL so SNS can reach /webhooks/ses-inbound",
			})
			return resp, nil
		}
		webhookURL := messagingWebhookURL(publicURL, "/webhooks/ses-inbound", pid, includeWebhookProjectID)
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
		if publishDNS && resolveDNSPublisher(ctx, domain) != "" {
			st := bootstrapPublishDNSRecord(ctx, pid, "dns_mx", domain, "@", "MX", "10 inbound-smtp."+region+".amazonaws.com")
			resp.Steps = append(resp.Steps, st)
			if !st.OK {
				resp.DnsPublishPartial = true
				return resp, nil
			}
		}
		resp.Inbound.Bootstrapped = true
	}

	if snsBound != nil && publicURL != "" && topicArn != "" {
		expected := []string{}
		if doEvents {
			expected = append(expected, messagingWebhookURL(publicURL, "/webhooks/ses-bounces", pid, includeWebhookProjectID))
		}
		if doInbound {
			expected = append(expected, messagingWebhookURL(publicURL, "/webhooks/ses-inbound", pid, includeWebhookProjectID))
		}
		if len(expected) > 0 {
			removed, err := cleanupStaleMessagingSNSSubscriptions(ctx, snsBound.ConnectionID, topicArn, expected, pid, includeWebhookProjectID)
			step := bootstrapStep{Step: "sns_cleanup_stale_webhooks", OK: err == nil}
			if err != nil {
				step.Error = err.Error()
			} else if len(removed) == 0 {
				step.Skipped = "no stale Messaging subscriptions"
			} else {
				step.Detail = fmt.Sprintf("removed %d stale subscription(s)", len(removed))
			}
			resp.Steps = append(resp.Steps, step)
		}
	}

	resp.NextStep = sendersCreateNextStep(doInbound, isAppDepBound(ctx, "domains"))
	if resp.DnsPublishPartial {
		resp.NextStep = "Some DNS records failed to publish — review the dns_* steps and re-run senders_create. " + resp.NextStep
	}

	if resp.Inbound != nil && resp.Inbound.Bootstrapped {
		persistDomainIdentityWithMetadata(ctx, pid, domain, resp, true, metadata, "persist_domain_identity_inbound")
	}
	return resp, nil
}

// persistDomainIdentity writes the DKIM anchor as soon as SES returns
// domain verification state. Inbound/SNS bootstrap happens later and
// may fail independently; the mailbox inheritance path must still see
// a verified parent domain when DKIM is already SUCCESS.
func persistDomainIdentity(ctx *sdk.AppCtx, pid, domain string, resp *sendersCreateResp, includeInbound bool, stepName string) {
	persistDomainIdentityWithMetadata(ctx, pid, domain, resp, includeInbound, "", stepName)
}

func persistSNSTopicAuthorization(ctx *sdk.AppCtx, pid, domain string, resp *sendersCreateResp) {
	if resp == nil {
		return
	}
	step := bootstrapStep{Step: "persist_sns_topic", OK: true}
	if resp.Inbound == nil || resp.Inbound.TopicARN == "" {
		step.OK = false
		step.Error = "SNS topic ARN is empty"
		resp.Steps = append(resp.Steps, step)
		return
	}
	existing, err := dbFindIdentity(ctx.AppDB(), pid, "email_domain", domain)
	if err != nil {
		step.OK = false
		step.Error = err.Error()
		resp.Steps = append(resp.Steps, step)
		return
	}
	config := map[string]any{}
	if existing != nil && existing.InboundConfig != "" {
		_ = json.Unmarshal([]byte(existing.InboundConfig), &config)
	}
	config["topic_arn"] = resp.Inbound.TopicARN
	config["account_id"] = resp.Inbound.AccountID
	config["region"] = resp.Inbound.Region
	encoded, err := json.Marshal(config)
	if err != nil {
		step.OK = false
		step.Error = err.Error()
		resp.Steps = append(resp.Steps, step)
		return
	}
	if existing != nil {
		_, err = ctx.AppDB().Exec(`UPDATE identities SET inbound_config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(encoded), existing.ID)
	} else {
		_, err = dbUpsertIdentity(ctx.AppDB(), &identityUpsert{
			ProjectID: pid, Kind: "email_domain", Address: domain,
			Provider: "aws-ses", ProviderIdentityID: domain, InboundConfig: string(encoded),
		})
	}
	if err != nil {
		step.OK = false
		step.Error = err.Error()
	} else {
		step.Detail = resp.Inbound.TopicARN
	}
	resp.Steps = append(resp.Steps, step)
}

func persistDomainIdentityWithMetadata(ctx *sdk.AppCtx, pid, domain string, resp *sendersCreateResp, includeInbound bool, metadata string, stepName string) {
	inboundBootstrapped := false
	inboundConfig := ""
	if includeInbound && resp.Inbound != nil {
		inboundBootstrapped = resp.Inbound.Bootstrapped
		inboundConfig = inboundConfigJSON(resp.Inbound)
	}
	identityID, persistErr := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{
		ProjectID:           pid,
		Kind:                "email_domain",
		Address:             domain,
		Provider:            "aws-ses",
		ProviderIdentityID:  domain,
		Verified:            strings.EqualFold(resp.DkimStatus, "SUCCESS"),
		VerificationStatus:  domainVerificationStatus(resp.DkimStatus),
		DkimStatus:          resp.DkimStatus,
		InboundBootstrapped: inboundBootstrapped,
		InboundConfig:       inboundConfig,
		Metadata:            metadata,
		MarkSyncedNow:       true,
	})
	if persistErr != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: stepName, OK: false, Error: persistErr.Error()})
		return
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: stepName, OK: true, Detail: fmt.Sprintf("identity id=%d", identityID)})
}

func domainSetupMetadata(domain, region, mailFromSub, mailFromDomain string, publishDMARC, configureMailFrom bool) string {
	meta := map[string]any{}
	if publishDMARC {
		meta["dmarc_desired"] = true
		meta["dmarc_record"] = defaultDMARCRecord(domain)
	}
	if configureMailFrom {
		meta["mail_from_domain"] = mailFromDomain
		meta["mail_from_desired"] = true
		meta["mail_from_dns_mx"] = "10 feedback-smtp." + region + ".amazonses.com"
		meta["mail_from_dns_spf"] = "v=spf1 include:amazonses.com ~all"
		meta["mail_from_mx_subdomain"] = mailFromSub
		meta["mail_from_ses_mx_region"] = region
	}
	if len(meta) == 0 {
		return ""
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
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
// the JSON shape stored on the domain identity. The SNS topic is saved
// as soon as it is created, even before inbound receipt setup completes.
func inboundConfigJSON(inb *sendersCreateInbound) string {
	if inb == nil || (inb.TopicARN == "" && !inb.Bootstrapped) {
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
	if addr == "" || !strings.HasPrefix(addr, "+") || !allDigits(addr[1:]) {
		return nil, fmt.Errorf("phone address must be E.164 (e.g. +15551234567), got %q", addr)
	}
	phoneBound := ctx.IntegrationFor("phone_provider")
	if phoneBound == nil {
		return nil, errors.New("phone_provider (twilio) not bound")
	}
	if channel == "whatsapp" {
		return a.sendersCreateWhatsApp(ctx, pid, addr, req, phoneBound.ConnectionID)
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
		publicURL, _ = messagingWebhookPublicURL(ctx, id)
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
		webhookURL := twilioWebhookURL(ctx, "/webhooks/twilio-inbound", pid)
		if match.SmsURL == webhookURL && strings.EqualFold(match.SmsMethod, "POST") {
			resp.Steps = append(resp.Steps, bootstrapStep{
				Step: "twilio_update_phone_number", OK: true,
				Skipped: "webhook already pointed at messaging",
			})
		} else {
			updRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(phoneBound.ConnectionID, "update_phone_number", map[string]any{
				"PhoneNumberSid": match.SID,
				"SmsUrl":         webhookURL,
				"SmsMethod":      "POST",
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
			"sms_url":             resp.Inbound.WebhookURL,
			"sms_method":          "POST",
			"previous_sms_url":    match.SmsURL,
			"previous_sms_method": match.SmsMethod,
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

func (a *App) sendersCreateWhatsApp(ctx *sdk.AppCtx, pid, addr string, req sendersCreateReq, connID int64) (*sendersCreateResp, error) {
	resp := &sendersCreateResp{
		Address: addr,
		Kind:    "phone",
		Pending: true,
	}
	listRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_whatsapp_senders", map[string]any{
		"PageSize": 100,
	})
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_list_whatsapp_senders", OK: false, Error: err.Error()})
		return resp, nil
	}
	if listRes == nil || !listRes.Success {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_list_whatsapp_senders", OK: false, Error: truncateResData(listRes)})
		return resp, nil
	}
	sender, ok := findTwilioWhatsAppSender(listRes.Data, addr)
	if !ok {
		resp.Steps = append(resp.Steps, bootstrapStep{
			Step:  "twilio_list_whatsapp_senders",
			OK:    false,
			Error: fmt.Sprintf("WhatsApp sender whatsapp:%s not found in the bound Twilio account — register it via twilio.register_whatsapp_sender first", addr),
		})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{
		Step:   "twilio_list_whatsapp_senders",
		OK:     true,
		Detail: fmt.Sprintf("sid=%s status=%s", sender.SID, sender.Status),
	})
	verified := twilioWhatsAppSenderVerified(sender.Status)
	resp.Pending = !verified

	inboundCfg := ""
	id, _ := ctx.PlatformAPI().WhoAmI()
	publicURL := ""
	if id != nil {
		publicURL, _ = messagingWebhookPublicURL(ctx, id)
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

	if doInbound {
		webhookURL := twilioWebhookURL(ctx, "/webhooks/twilio-inbound", pid)
		statusWebhookURL := twilioWebhookURL(ctx, "/webhooks/twilio-status", pid)
		webhook := map[string]any{
			"callback_url":    webhookURL,
			"callback_method": "POST",
		}
		if statusWebhookURL != "" {
			webhook["status_callback_url"] = statusWebhookURL
			webhook["status_callback_method"] = "POST"
		}
		updRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "update_whatsapp_sender", map[string]any{
			"SenderSid": sender.SID,
			"webhook":   webhook,
		})
		if err != nil {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_update_whatsapp_sender", OK: false, Error: err.Error()})
			return resp, nil
		}
		if updRes == nil || !updRes.Success {
			resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_update_whatsapp_sender", OK: false, Error: truncateResData(updRes)})
			return resp, nil
		}
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "twilio_update_whatsapp_sender", OK: true, Detail: "callback_url=" + webhookURL})
		cfg := map[string]any{
			"callback_url":    webhookURL,
			"callback_method": "POST",
		}
		if statusWebhookURL != "" {
			cfg["status_callback_url"] = statusWebhookURL
			cfg["status_callback_method"] = "POST"
		}
		if b, err := json.Marshal(cfg); err == nil {
			inboundCfg = string(b)
		}
		resp.Inbound = &sendersCreateInbound{
			Bootstrapped: true,
			WebhookURL:   webhookURL,
		}
	}

	a.persistSenderRow(ctx, pid, &senderUpsert{
		ProjectID:           pid,
		Channel:             "whatsapp",
		Address:             addr,
		Kind:                "phone",
		DisplayName:         req.DisplayName,
		Provider:            "twilio",
		ProviderIdentityID:  sender.SID,
		Verified:            verified,
		VerificationStatus:  twilioWhatsAppVerificationStatus(sender.Status),
		SendingEnabled:      verified,
		InboundBootstrapped: resp.Inbound != nil && resp.Inbound.Bootstrapped,
		InboundConfig:       inboundCfg,
		MarkSyncedNow:       true,
	}, resp)
	if verified {
		if doInbound {
			resp.NextStep = "WhatsApp sender " + addr + " is approved and ready to send + receive WhatsApp via messaging."
		} else {
			resp.NextStep = "WhatsApp sender " + addr + " is approved for outbound. Inbound webhook not wired — set inbound=true once PublicURL is configured."
		}
	} else {
		resp.NextStep = "WhatsApp sender " + addr + " is tracked but not approved yet — refresh after Twilio/Meta approval."
	}
	return resp, nil
}

type twilioWhatsAppSender struct {
	SID      string
	SenderID string
	Status   string
}

func findTwilioWhatsAppSender(raw []byte, addr string) (twilioWhatsAppSender, bool) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return twilioWhatsAppSender{}, false
	}
	wantPlain := strings.TrimSpace(stripScheme(addr))
	wantWA := "whatsapp:" + wantPlain
	var walk func(any) (twilioWhatsAppSender, bool)
	walk = func(v any) (twilioWhatsAppSender, bool) {
		switch x := v.(type) {
		case map[string]any:
			senderID := firstStringField(x, "sender_id", "SenderId", "phone_number", "phoneNumber", "address", "from")
			if strings.EqualFold(strings.TrimSpace(senderID), wantPlain) || strings.EqualFold(strings.TrimSpace(senderID), wantWA) {
				return twilioWhatsAppSender{
					SID:      firstStringField(x, "sid", "Sid", "SID", "sender_sid", "SenderSid"),
					SenderID: senderID,
					Status:   firstStringField(x, "status", "Status", "state", "State"),
				}, true
			}
			for _, child := range x {
				if got, ok := walk(child); ok {
					return got, true
				}
			}
		case []any:
			for _, child := range x {
				if got, ok := walk(child); ok {
					return got, true
				}
			}
		}
		return twilioWhatsAppSender{}, false
	}
	return walk(root)
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func twilioWhatsAppSenderVerified(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "approved", "online", "active", "verified":
		return true
	}
	return false
}

func twilioWhatsAppVerificationStatus(status string) string {
	if twilioWhatsAppSenderVerified(status) {
		return "verified"
	}
	if strings.TrimSpace(status) == "" {
		return "pending"
	}
	return strings.ToLower(strings.TrimSpace(status))
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
	arn := parseFirstSNSARN(string(res.Data), "TopicArn")
	if arn == "" {
		return "", errors.New("create_topic response missing TopicArn")
	}
	return arn, nil
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
	existing, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_bucket_policy", map[string]any{"bucket": bucket})
	if err != nil {
		return err
	}
	var previous []byte
	if existing == nil {
		return errors.New("empty bucket policy response")
	}
	if existing.Success {
		previous = existing.Data
		var quoted string
		if json.Unmarshal(previous, &quoted) == nil {
			previous = []byte(quoted)
		}
	} else if existing.Status != 404 {
		return fmt.Errorf("read bucket policy: %s", truncateResData(existing))
	}
	merged, err := mergeBucketPolicy(previous, []byte(policy))
	if err != nil {
		return err
	}
	policy = merged

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

var sesEventTypes = []string{
	"SEND",
	"REJECT",
	"BOUNCE",
	"COMPLAINT",
	"DELIVERY",
	"OPEN",
	"CLICK",
	"RENDERING_FAILURE",
	"DELIVERY_DELAY",
	"SUBSCRIPTION",
}

func bootstrapConfigureSESEvents(ctx *sdk.AppCtx, connID int64, topicArn string) error {
	name := scopedSESConfigName(ctx, connID)
	if topicArn == "" {
		return errors.New("sns topic arn missing")
	}
	if err := bootstrapCreateSESConfigSet(ctx, connID, name); err != nil {
		return err
	}
	dest := map[string]any{
		"Enabled":            true,
		"MatchingEventTypes": sesEventTypes,
		"SnsDestination": map[string]any{
			"TopicArn": topicArn,
		},
	}
	args := map[string]any{
		"ConfigurationSetName": name,
		"EventDestinationName": "apteva-messaging-sns",
		"EventDestination":     dest,
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "add_event_destination", args)
	if err != nil {
		return fmt.Errorf("add_event_destination: %w", err)
	}
	if res != nil && res.Success {
		return saveSESConfigName(ctx, connID, name)
	}
	body := ""
	if res != nil {
		body = string(res.Data)
	}
	if !looksLikeAlreadyExists(res, body) {
		return fmt.Errorf("add_event_destination non-2xx: %s", truncate(body, 400))
	}
	res, err = ctx.PlatformAPI().ExecuteIntegrationTool(connID, "update_event_destination", args)
	if err != nil {
		return fmt.Errorf("update_event_destination: %w", err)
	}
	if res == nil || !res.Success {
		return fmt.Errorf("update_event_destination non-2xx: %s", truncateResData(res))
	}
	return saveSESConfigName(ctx, connID, name)
}

func bootstrapCreateSESConfigSet(ctx *sdk.AppCtx, connID int64, name string) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "create_config_set", map[string]any{
		"ConfigurationSetName": name,
	})
	if err != nil {
		return fmt.Errorf("create_config_set: %w", err)
	}
	if res != nil && res.Success {
		return nil
	}
	body := ""
	if res != nil {
		body = string(res.Data)
	}
	if looksLikeAlreadyExists(res, body) {
		return nil
	}
	return fmt.Errorf("create_config_set non-2xx: %s", truncate(body, 400))
}

// bootstrapVerifyDomain creates (or adopts) the SES email identity
// for `domain` and returns its DKIM tokens + verification status.
//
// SES v2 create_email_identity errors with "already exists" if the
// identity is already in the account — but that's exactly the state
// the bootstrap wants. When that happens we fall through to
// get_email_identity to read the existing DKIM tokens. The adopted
// return flag lets the caller annotate the step.
func bootstrapVerifyDomain(ctx *sdk.AppCtx, connID int64, domain string) (tokens []string, status string, adopted bool, reprobed bool, err error) {
	res, vErr := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "verify_domain", map[string]any{
		"EmailIdentity": domain,
	})
	if vErr != nil {
		return nil, "", false, false, fmt.Errorf("verify_domain: %w", vErr)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		if looksLikeAlreadyExists(res, body) {
			t, s, gErr := getDomainDKIM(ctx, connID, domain)
			if gErr != nil {
				return nil, "", false, false, fmt.Errorf("verify_domain reported exists but get_identity_verification failed: %w", gErr)
			}
			// Adoption must never delete an existing identity to restart verification.

			return t, s, true, false, nil
		}
		return nil, "", false, false, fmt.Errorf("verify_domain non-2xx: %s", truncate(body, 400))
	}
	var probe struct {
		DkimAttributes struct {
			Tokens []string `json:"Tokens"`
			Status string   `json:"Status"`
		} `json:"DkimAttributes"`
	}
	_ = json.Unmarshal(res.Data, &probe)
	return probe.DkimAttributes.Tokens, probe.DkimAttributes.Status, false, false, nil
}

// isBrokenDkimStatus reports whether SES's DkimAttributes.Status is a
// state that won't self-heal and warrants a forced re-probe.
func isBrokenDkimStatus(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "FAILED", "TEMPORARY_FAILURE":
		return true
	}
	return false
}

// reprobeDomainIdentity deletes the SES identity and re-creates it,
// returning the fresh DKIM tokens + status. SES restarts its DKIM probe
// schedule on the new identity. Tokens are deterministic per (account,
// domain) under Easy DKIM, so the previously-published CNAMEs stay valid.
func reprobeDomainIdentity(ctx *sdk.AppCtx, connID int64, domain string) ([]string, string, error) {
	return nil, "", errors.New("destructive DKIM reprobe is disabled; repair DNS and recheck the existing identity")
}

// bootstrapClearMailFrom resets any leftover custom MAIL FROM domain on
// an adopted identity back to the SES default (amazonses.com). Messaging
// doesn't support custom MAIL FROM, so a carried-over MailFromDomain
// (often stuck in FAILED) is pure noise. Non-fatal: a missing/older
// integration catalog without set_mail_from degrades to a skipped step
// so it can never break senders_create.
func bootstrapClearMailFrom(ctx *sdk.AppCtx, connID int64, domain string) bootstrapStep {
	// Omit MailFromDomain entirely — the documented way to reset the
	// custom MAIL FROM to the SES default is to send no MailFromDomain
	// (an empty string can be rejected as an invalid domain).
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "set_mail_from", map[string]any{
		"EmailIdentity": domain,
	})
	if err != nil {
		return bootstrapStep{Step: "clear_mail_from", OK: true, Skipped: "set_mail_from unavailable: " + err.Error()}
	}
	if res == nil || !res.Success {
		return bootstrapStep{Step: "clear_mail_from", OK: true, Skipped: "set_mail_from not applied: " + truncateResData(res)}
	}
	return bootstrapStep{Step: "clear_mail_from", OK: true, Detail: "reset MAIL FROM to amazonses.com default"}
}

func bootstrapSetMailFrom(ctx *sdk.AppCtx, connID int64, domain, mailFromDomain string) bootstrapStep {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "set_mail_from", map[string]any{
		"EmailIdentity":       domain,
		"MailFromDomain":      mailFromDomain,
		"BehaviorOnMxFailure": "USE_DEFAULT_VALUE",
	})
	if err != nil {
		return bootstrapStep{Step: "ses_mail_from", OK: false, Error: err.Error()}
	}
	if res == nil || !res.Success {
		return bootstrapStep{Step: "ses_mail_from", OK: false, Error: truncateResData(res)}
	}
	return bootstrapStep{Step: "ses_mail_from", OK: true, Detail: mailFromDomain}
}

// looksLikeAlreadyExists classifies a SES failure as "identity is
// already registered" — idempotency signal, not a real error.
func looksLikeAlreadyExists(res *sdk.ExecuteResult, body string) bool {
	if res != nil && res.Status == 409 {
		return true
	}
	return strings.Contains(strings.ToLower(body), "already exist")
}

// getDomainDKIM reads the existing DKIM tokens + status for an
// identity already at SES. Used to adopt a domain bootstrap that
// hit "already exists".
func getDomainDKIM(ctx *sdk.AppCtx, connID int64, domain string) ([]string, string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_identity_verification", map[string]any{
		"EmailIdentity": domain,
	})
	if err != nil {
		return nil, "", fmt.Errorf("get_identity_verification: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, "", fmt.Errorf("get_identity_verification non-2xx: %s", truncate(body, 400))
	}
	var inner struct {
		DkimAttributes struct {
			Tokens []string `json:"Tokens"`
			Status string   `json:"Status"`
		} `json:"DkimAttributes"`
	}
	_ = json.Unmarshal(res.Data, &inner)
	return inner.DkimAttributes.Tokens, inner.DkimAttributes.Status, nil
}

func bootstrapPublishDNSRecord(ctx *sdk.AppCtx, pid, step, domain, name, recType, value string) bootstrapStep {
	publisher := resolveDNSPublisher(ctx, domain)
	recordID := ""
	// DKIM labels are isolated, but SPF/DMARC/MX can belong to other services.
	if recType == "TXT" || recType == "MX" {
		if !isAppDepBound(ctx, "domains") {
			return bootstrapStep{Step: step, Error: "DNS inventory unavailable; review the proposed record and configure it manually or bind Domains before changing existing mail DNS"}
		}
		var inventory struct {
			Records *[]setupDNSRecord `json:"records"`
		}
		err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_list", map[string]any{"_project_id": pid, "domain": domain, "type": recType, "name": name}, &inventory)
		if err != nil {
			return bootstrapStep{Step: step, Error: err.Error()}
		}
		if inventory.Records == nil {
			return bootstrapStep{Step: step, Error: "invalid DNS inventory; no change applied"}
		}
		next, id, unchanged, err := planDNSRecord(*inventory.Records, domain, name, recType, value)
		if err != nil {
			return bootstrapStep{Step: step, Error: err.Error()}
		}
		if unchanged {
			return bootstrapStep{Step: step, OK: true, Skipped: "existing DNS already satisfies this setting"}
		}
		value, recordID = next, id
		// Use the inventory's provider and exact record identity for updates.
		publisher = "domains"
	}

	if publisher == "platform" {
		result, err := ctx.PlatformAPI().UpsertDNSRecord(sdk.DNSRecordRequest{
			ProjectID: pid,
			Domain:    domain,
			Name:      name,
			Type:      recType,
			Value:     value,
			TTL:       1800,
		})
		if err != nil {
			return bootstrapStep{Step: step, Error: err.Error()}
		}
		if result == nil || !result.OK {
			errText := "platform DNS update failed"
			if result != nil && result.Error != "" {
				errText = result.Error
			}
			return bootstrapStep{Step: step, Error: errText}
		}
		return bootstrapStep{Step: step, OK: true, Detail: result.Action}
	}
	// Global-scope domains installs reject calls without _project_id —
	// the inject is the same convention every cross-app helper in this
	// codebase already follows (see domains_link.go, certs/domain_link.go,
	// deploy/sources.go). v0.12.2 and earlier omitted it here, so
	// publish_dns succeeded only on project-scoped domains installs
	// (matched dev's; broke prod's, where domains is global).
	args := map[string]any{
		"_project_id": pid,
		"domain":      domain,
		"name":        name,
		"type":        recType,
		"value":       value,
		"ttl":         1800,
	}
	if recordID != "" {
		args["record_id"] = recordID
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

func resolveDNSPublisher(ctx *sdk.AppCtx, domain string) string {
	if platformDNSGrantCovers(ctx, domain) {
		return "platform"
	}
	if isAppDepBound(ctx, "domains") {
		return "domains"
	}
	return ""
}

func platformDNSGrantCovers(ctx *sdk.AppCtx, domain string) bool {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return false
	}
	domain = strings.ToLower(strings.Trim(domain, "."))
	grants, err := ctx.PlatformAPI().ListDomainGrants()
	if err != nil {
		return false
	}
	for _, grant := range grants {
		granted := strings.ToLower(strings.Trim(strings.TrimPrefix(grant.Domain, "*."), "."))
		if granted == domain || (grant.Wildcard && strings.HasSuffix(domain, "."+granted)) {
			return true
		}
	}
	return false
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

// bootstrapCreateReceiptRule creates the receipt rule for `domain` —
// or merges the new recipient into an existing rule of the same name
// when AlreadyExists fires. Pre-v0.12.5 the AlreadyExists branch
// returned nil silently, leaving whatever recipient + S3 target the
// FIRST caller set in place; second-and-later domains (or installs)
// looked successfully bootstrapped locally but SES had no idea about
// them, so their inbound mail bounced.
//
// SES doesn't expose UpdateReceiptRule on the query API we have
// available; the merge path is describe-active → delete → recreate
// with the union of (existing_recipients, new_domain). Brief 50ms
// window where the rule doesn't exist; for inbound mail that just
// means SES retries the SMTP delivery.
func bootstrapCreateReceiptRule(ctx *sdk.AppCtx, connID int64, ruleSetName, ruleName, domain, bucket, topicArn string) error {
	args := buildReceiptRuleArgs(ruleSetName, ruleName, []string{domain}, bucket, topicArn)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "create_receipt_rule", args)
	if err != nil {
		return fmt.Errorf("create_receipt_rule: %w", err)
	}
	if res != nil && res.Success {
		return nil
	}
	if res == nil {
		return errors.New("create_receipt_rule: nil result")
	}
	raw := string(res.Data)
	if !strings.Contains(raw, "AlreadyExists") {
		return fmt.Errorf("create_receipt_rule non-2xx: %s", truncate(raw, 400))
	}
	return mergeReceiptRuleRecipient(ctx, connID, ruleSetName, ruleName, domain, bucket, topicArn)
}

func buildReceiptRuleArgs(ruleSetName, ruleName string, recipients []string, bucket, topicArn string) map[string]any {
	args := map[string]any{
		"RuleSetName":      ruleSetName,
		"Rule.Name":        ruleName,
		"Rule.Enabled":     "true",
		"Rule.ScanEnabled": "true",
		"Rule.Actions.member.1.S3Action.BucketName": bucket,
		"Rule.Actions.member.1.S3Action.TopicArn":   topicArn,
	}
	// SES query API takes Recipients as numbered members starting at 1.
	for i, r := range recipients {
		args[fmt.Sprintf("Rule.Recipients.member.%d", i+1)] = r
	}
	return args
}

// mergeReceiptRuleRecipient preserves the complete existing rule and updates in place.
func mergeReceiptRuleRecipient(ctx *sdk.AppCtx, connID int64, ruleSetName, ruleName, domain, bucket, topicArn string) error {
	rule, err := readReceiptRule(ctx, connID, ruleSetName, ruleName)
	if err != nil {
		return err
	}
	recipients, err := receiptRecipients(rule["Recipients"])
	if err != nil {
		return err
	}
	// Empty recipients means the existing rule matches all recipients; preserve that meaning.
	if len(recipients) > 0 {
		found := false
		for _, r := range recipients {
			if strings.EqualFold(r, domain) {
				found = true
			}
		}
		if !found {
			recipients = append(recipients, domain)
		}
		rule["Recipients"] = recipients
	}
	args := map[string]any{"RuleSetName": ruleSetName}
	flattenAWSQuery(args, "Rule", rule)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "update_receipt_rule", args)
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		return fmt.Errorf("update receipt rule: %s", truncateResData(res))
	}
	return nil
}

// describeReceiptRuleRecipients reads the exact named rule and rejects malformed lists.
func describeReceiptRuleRecipients(ctx *sdk.AppCtx, connID int64, ruleSetName, ruleName string) ([]string, error) {
	rule, err := readReceiptRule(ctx, connID, ruleSetName, ruleName)
	if err != nil {
		return nil, err
	}
	return receiptRecipients(rule["Recipients"])
}

// extractRecipientsForRule walks the Rules payload (which can be
// shaped {"member":[...]}, [...], or a single object) and returns the
// Recipients list for the rule whose Name matches. Tolerant of the
// variability so a slightly different SES → JSON shape doesn't make
// us silently rebuild the rule with empty recipients.
func extractRecipientsForRule(raw json.RawMessage, ruleName string) []string {
	if len(raw) == 0 {
		return nil
	}
	// Try { "member": [...] }.
	var wrapped struct {
		Member []map[string]any `json:"member"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Member) > 0 {
		return recipientsFromRuleList(wrapped.Member, ruleName)
	}
	// Try [...]
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return recipientsFromRuleList(arr, ruleName)
	}
	// Try single object.
	var single map[string]any
	if err := json.Unmarshal(raw, &single); err == nil && len(single) > 0 {
		if member, ok := single["member"].(map[string]any); ok {
			single = member
		}
		return recipientsFromRuleList([]map[string]any{single}, ruleName)
	}
	return nil
}

func recipientsFromRuleList(rules []map[string]any, ruleName string) []string {
	for _, rule := range rules {
		name, _ := rule["Name"].(string)
		if !strings.EqualFold(name, ruleName) {
			continue
		}
		raw, _ := json.Marshal(rule["Recipients"])
		return extractRecipientsField(raw)
	}
	return nil
}

func extractRecipientsField(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// { "member": ["a", "b"] }
	var wrapped struct {
		Member []string `json:"member"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Member) > 0 {
		return wrapped.Member
	}
	// { "member": "single" }
	var wrappedSingle struct {
		Member string `json:"member"`
	}
	if err := json.Unmarshal(raw, &wrappedSingle); err == nil && wrappedSingle.Member != "" {
		return []string{wrappedSingle.Member}
	}
	// Bare ["a", "b"]
	var bare []string
	if err := json.Unmarshal(raw, &bare); err == nil {
		return bare
	}
	return nil
}

func bootstrapActivateRuleSet(ctx *sdk.AppCtx, connID int64, name string) error {
	active, err := activeReceiptRuleSet(ctx, connID)
	if err != nil {
		return err
	}
	if active == name {
		return nil
	}
	if active != "" {
		return fmt.Errorf("active receipt ruleset is %q; use it or explicitly migrate it before activating %q", active, name)
	}
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

func messagingWebhookURL(publicURL, webhookPath, projectID string, includeProjectID bool) string {
	q := webhookRoutingQuery(globalCtx)
	if includeProjectID && projectID != "" {
		q.Set("project_id", projectID)
	}
	return strings.TrimSuffix(publicURL, "/") + "/api/apps/messaging" + webhookPath + "?" + q.Encode()
}

func messagingWebhookPublicURL(ctx *sdk.AppCtx, id *sdk.InstallIdentity) (string, error) {
	if ctx != nil {
		if raw := strings.TrimSpace(ctx.Config().Get("webhook_public_url")); raw != "" {
			return normaliseWebhookPublicURL(raw)
		}
	}
	if id == nil || strings.TrimSpace(id.PublicURL) == "" {
		return "", errors.New("platform Public URL is unset and webhook_public_url is not configured")
	}
	return normaliseWebhookPublicURL(id.PublicURL)
}

func normaliseWebhookPublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return "", errors.New("webhook_public_url is invalid")
	}
	if u.Scheme != "https" {
		return "", errors.New("webhook_public_url must start with https://")
	}
	if u.Host == "" || u.User != nil {
		return "", errors.New("webhook_public_url must include a host and no credentials")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("webhook_public_url must be an origin only, without a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("webhook_public_url must not include query string or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

func bootstrapSubscribeWebhook(ctx *sdk.AppCtx, connID int64, topicArn, endpoint string) (string, bool, error) {
	subscriptions, listErr := listSNSSubscriptions(ctx, connID, topicArn)
	if listErr == nil {
		for _, sub := range subscriptions {
			if sub.Endpoint == endpoint {
				return sub.SubscriptionARN, true, nil
			}
		}
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

type snsSubscription struct {
	Endpoint        string
	SubscriptionARN string
}

func cleanupStaleMessagingSNSSubscriptions(ctx *sdk.AppCtx, connID int64, topicARN string, expected []string, projectID string, includeProjectID bool) ([]string, error) {
	subscriptions, err := listSNSSubscriptions(ctx, connID, topicARN)
	if err != nil {
		return nil, err
	}
	expectedSet := map[string]bool{}
	for _, endpoint := range expected {
		expectedSet[endpoint] = true
	}
	removed := []string{}
	for _, sub := range subscriptions {
		if sub.Endpoint == "" || sub.SubscriptionARN == "" || expectedSet[sub.Endpoint] ||
			!isStaleMessagingWebhookEndpoint(sub.Endpoint, os.Getenv("APTEVA_APP_TOKEN"), projectID, includeProjectID) {
			continue
		}
		result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "unsubscribe", map[string]any{"SubscriptionArn": sub.SubscriptionARN})
		if err != nil || result == nil || !result.Success {
			if err != nil {
				return removed, err
			}
			return removed, fmt.Errorf("unsubscribe non-2xx: %s", truncateResData(result))
		}
		removed = append(removed, sub.Endpoint)
	}
	return removed, nil
}

func listSNSSubscriptions(ctx *sdk.AppCtx, connID int64, topicARN string) ([]snsSubscription, error) {
	var all []snsSubscription
	nextToken := ""
	seenTokens := map[string]bool{}
	for page := 0; page < 100; page++ {
		input := map[string]any{"TopicArn": topicARN}
		if nextToken != "" {
			input["NextToken"] = nextToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_subscriptions_by_topic", input)
		if err != nil {
			return nil, fmt.Errorf("list subscriptions: %w", err)
		}
		if res == nil || !res.Success {
			return nil, fmt.Errorf("list subscriptions non-2xx: %s", truncateResData(res))
		}
		all = append(all, parseSNSSubscriptions(res.Data)...)
		nextToken = parseSNSNextToken(res.Data)
		if nextToken == "" {
			return all, nil
		}
		if seenTokens[nextToken] {
			return nil, errors.New("list subscriptions returned a repeated NextToken")
		}
		seenTokens[nextToken] = true
	}
	return nil, errors.New("list subscriptions exceeded 100 pages")
}

func parseSNSNextToken(data []byte) string {
	var root any
	if json.Unmarshal(data, &root) == nil {
		return walkForString(root, "NextToken")
	}
	var envelope struct {
		NextToken string `xml:"ListSubscriptionsByTopicResult>NextToken"`
	}
	if xml.Unmarshal(data, &envelope) == nil {
		return strings.TrimSpace(envelope.NextToken)
	}
	return ""
}

func isStaleMessagingWebhookEndpoint(endpoint, token, projectID string, includeProjectID bool) bool {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Path != "/api/apps/messaging/webhooks/ses-bounces" && u.Path != "/api/apps/messaging/webhooks/ses-inbound") {
		return false
	}
	query := u.Query()
	if token == "" || query.Get("api_key") != token {
		return false
	}
	return !includeProjectID || projectID == "" || query.Get("project_id") == projectID
}

func parseSNSSubscriptions(data []byte) []snsSubscription {
	var root any
	out := []snsSubscription{}
	if json.Unmarshal(data, &root) == nil {
		walkSNSSubscriptions(root, &out)
	} else {
		var envelope struct {
			Members []struct {
				Endpoint        string `xml:"Endpoint"`
				SubscriptionARN string `xml:"SubscriptionArn"`
			} `xml:"ListSubscriptionsByTopicResult>Subscriptions>member"`
		}
		if xml.Unmarshal(data, &envelope) != nil {
			return nil
		}
		for _, member := range envelope.Members {
			out = append(out, snsSubscription{Endpoint: member.Endpoint, SubscriptionARN: member.SubscriptionARN})
		}
	}
	seen := map[string]bool{}
	unique := out[:0]
	for _, sub := range out {
		key := sub.Endpoint + "\x00" + sub.SubscriptionARN
		if key == "\x00" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, sub)
	}
	return unique
}

func walkSNSSubscriptions(value any, out *[]snsSubscription) {
	switch current := value.(type) {
	case map[string]any:
		endpoint, _ := current["Endpoint"].(string)
		arn, _ := current["SubscriptionArn"].(string)
		if endpoint != "" || arn != "" {
			*out = append(*out, snsSubscription{Endpoint: endpoint, SubscriptionARN: arn})
		}
		for _, child := range current {
			walkSNSSubscriptions(child, out)
		}
	case []any:
		for _, child := range current {
			walkSNSSubscriptions(child, out)
		}
	}
}

func truncateResData(res *sdk.ExecuteResult) string {
	if res == nil {
		return "(nil)"
	}
	return truncate(string(res.Data), 400)
}
