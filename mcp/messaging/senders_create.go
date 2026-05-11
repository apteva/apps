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
	Address     string `json:"address"`       // required: email or domain
	Inbound     string `json:"inbound"`       // "auto" | "true" | "false"; default "auto"
	PublishDNS  *bool  `json:"publish_dns"`   // domain only; default true
	SPF         *bool  `json:"spf"`           // domain only; default true
	Region      string `json:"region"`        // default eu-west-1 (inbound only)
	BucketName  string `json:"bucket_name"`   // auto-named if blank
	TopicName   string `json:"topic_name"`    // auto-named if blank
	RuleSetName string `json:"rule_set_name"` // default "apteva-default"
	RuleName    string `json:"rule_name"`     // default "messaging-inbound"
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
	kind, raw, err := classifyEmailIdentity(req.Address)
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
		return a.sendersCreateEmail(ctx, sesBound.ConnectionID, raw, resp)
	}
	return a.sendersCreateDomain(ctx, sesBound.ConnectionID, raw, req, resp)
}

func (a *App) sendersCreateEmail(ctx *sdk.AppCtx, connID int64, addr string, resp *sendersCreateResp) (*sendersCreateResp, error) {
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
	return resp, nil
}

func (a *App) sendersCreateDomain(ctx *sdk.AppCtx, sesConnID int64, domain string, req sendersCreateReq, resp *sendersCreateResp) (*sendersCreateResp, error) {
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
