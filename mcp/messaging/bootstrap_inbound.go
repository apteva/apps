package main

// bootstrap_inbound.go — one-shot SES inbound provisioning.
//
// Idempotent end-to-end setup that gets a fresh AWS account from
// "credentials bound" to "messaging receives mail at yourdomain.com"
// without ever opening the AWS console:
//
//   1. SNS create_topic                  (idempotent on Name)
//   2. SNS set_topic_attributes Policy   (allow ses.amazonaws.com:Publish)
//   3. S3  create_bucket                 (BucketAlreadyOwnedByYou ⇒ ok)
//   4. S3  put_bucket_policy             (allow ses.amazonaws.com s3:PutObject)
//   5. SES verify_domain                 (mints + returns DKIM tokens)
//   6. domains app: publish DKIM CNAMEs + MX (skipped if not bound)
//   7. SES create_receipt_rule_set       (AlreadyExists ⇒ ok)
//   8. SES create_receipt_rule           (S3Action with TopicArn — single
//                                         action drops the .eml in S3
//                                         AND publishes the SNS notif
//                                         in one atomic SES action)
//   9. SES set_active_receipt_rule_set
//  10. SNS subscribe                     (https endpoint = our /webhooks/
//                                         ses-inbound, scoped with the
//                                         install's APTEVA_APP_TOKEN as
//                                         api_key. Skipped if an
//                                         identical endpoint is already
//                                         subscribed.)
//
// On every step we collect a structured result; failures don't roll
// back partial state because every step is idempotent — re-running the
// bootstrap converges. The handler returns 200 with `steps[]` even on
// per-step failures so the caller can surface the exact error.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type bootstrapInboundReq struct {
	Domain      string `json:"domain"`
	Region      string `json:"region"`
	BucketName  string `json:"bucket_name"`
	TopicName   string `json:"topic_name"`
	RuleSetName string `json:"rule_set_name"`
	RuleName    string `json:"rule_name"`
}

type bootstrapStep struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type bootstrapInboundResp struct {
	Domain          string          `json:"domain"`
	Region          string          `json:"region"`
	BucketName      string          `json:"bucket_name"`
	TopicARN        string          `json:"topic_arn"`
	AccountID       string          `json:"account_id"`
	RuleSetName     string          `json:"rule_set_name"`
	RuleName        string          `json:"rule_name"`
	WebhookURL      string          `json:"webhook_url"`
	SubscriptionARN string          `json:"subscription_arn,omitempty"`
	Steps           []bootstrapStep `json:"steps"`
}

func (a *App) handleBootstrapInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body bootstrapInboundReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	out, err := a.bootstrapInboundImpl(globalCtx, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) toolBootstrapInbound(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body := bootstrapInboundReq{
		Domain:      argStr(args, "domain"),
		Region:      argStr(args, "region"),
		BucketName:  argStr(args, "bucket_name"),
		TopicName:   argStr(args, "topic_name"),
		RuleSetName: argStr(args, "rule_set_name"),
		RuleName:    argStr(args, "rule_name"),
	}
	return a.bootstrapInboundImpl(ctx, body)
}

func argStr(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (a *App) bootstrapInboundImpl(ctx *sdk.AppCtx, req bootstrapInboundReq) (*bootstrapInboundResp, error) {
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain == "" {
		return nil, errors.New("domain required")
	}
	region := req.Region
	if region == "" {
		region = "eu-west-1"
	}

	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil {
		return nil, fmt.Errorf("whoami: %w", err)
	}
	publicURL := strings.TrimSuffix(strings.TrimSpace(id.PublicURL), "/")
	if publicURL == "" {
		return nil, errors.New("platform PublicURL is unset — set Settings → Server → Public URL so SNS can reach the messaging webhook")
	}

	bucketName := req.BucketName
	if bucketName == "" {
		bucketName = fmt.Sprintf("apteva-ses-inbound-%d", id.InstallID)
	}
	topicName := req.TopicName
	if topicName == "" {
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

	resp := &bootstrapInboundResp{
		Domain:      domain,
		Region:      region,
		BucketName:  bucketName,
		RuleSetName: ruleSetName,
		RuleName:    ruleName,
	}

	sesBound := ctx.IntegrationFor("email_provider")
	s3Bound := ctx.IntegrationFor("inbound_storage")
	snsBound := ctx.IntegrationFor("inbound_notifications")
	if sesBound == nil {
		return nil, errors.New("email_provider (aws-ses) not bound")
	}
	if s3Bound == nil {
		return nil, errors.New("inbound_storage (aws-s3) not bound")
	}
	if snsBound == nil {
		return nil, errors.New("inbound_notifications (aws-sns) not bound")
	}

	// 1. Create SNS topic.
	topicArn, err := bootstrapCreateSNSTopic(ctx, snsBound.ConnectionID, topicName)
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_sns_topic", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.TopicARN = topicArn
	resp.AccountID = parseAccountFromARN(topicArn)
	resp.Steps = append(resp.Steps, bootstrapStep{
		Step: "create_sns_topic", OK: true,
		Detail: fmt.Sprintf("topic_arn=%s account=%s", topicArn, resp.AccountID),
	})

	// 2. SNS topic policy.
	if err := bootstrapSetSNSPolicy(ctx, snsBound.ConnectionID, topicArn, snsTopicPolicy(topicArn, resp.AccountID)); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_sns_topic_policy", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_sns_topic_policy", OK: true})

	// 3. Create S3 bucket.
	if err := bootstrapCreateS3Bucket(ctx, s3Bound.ConnectionID, bucketName, region); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_s3_bucket", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_s3_bucket", OK: true})

	// 4. S3 bucket policy.
	if err := bootstrapSetS3BucketPolicy(ctx, s3Bound.ConnectionID, bucketName, s3BucketPolicy(bucketName, resp.AccountID)); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "put_s3_bucket_policy", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "put_s3_bucket_policy", OK: true})

	// 5. SES verify_domain.
	dkimTokens, err := bootstrapVerifyDomain(ctx, sesBound.ConnectionID, domain)
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "ses_verify_domain", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{
		Step: "ses_verify_domain", OK: true,
		Detail: fmt.Sprintf("%d dkim tokens", len(dkimTokens)),
	})

	// 6. Publish DNS via domains app.
	if isAppDepBound(ctx, "domains") {
		resp.Steps = append(resp.Steps, bootstrapPublishDNS(ctx, domain, region, dkimTokens)...)
	} else {
		resp.Steps = append(resp.Steps, bootstrapStep{
			Step: "publish_dns", OK: true,
			Skipped: "domains app not bound — publish DKIM CNAMEs + MX manually",
		})
	}

	// 7. SES rule set.
	if err := bootstrapCreateRuleSet(ctx, sesBound.ConnectionID, ruleSetName); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule_set", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule_set", OK: true})

	// 8. SES receipt rule (S3 + SNS via the single S3Action).
	if err := bootstrapCreateReceiptRule(ctx, sesBound.ConnectionID, ruleSetName, ruleName, domain, bucketName, topicArn); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "create_receipt_rule", OK: true})

	// 9. Activate.
	if err := bootstrapActivateRuleSet(ctx, sesBound.ConnectionID, ruleSetName); err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_active_receipt_rule_set", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "set_active_receipt_rule_set", OK: true})

	// 10. Subscribe webhook.
	webhookURL := publicURL + "/api/apps/messaging/webhooks/ses-inbound?api_key=" + os.Getenv("APTEVA_APP_TOKEN")
	resp.WebhookURL = webhookURL
	subArn, alreadySubscribed, err := bootstrapSubscribeWebhook(ctx, snsBound.ConnectionID, topicArn, webhookURL)
	if err != nil {
		resp.Steps = append(resp.Steps, bootstrapStep{Step: "sns_subscribe_webhook", OK: false, Error: err.Error()})
		return resp, nil
	}
	resp.SubscriptionARN = subArn
	step := bootstrapStep{Step: "sns_subscribe_webhook", OK: true, Detail: subArn}
	if alreadySubscribed {
		step.Skipped = "subscription already exists"
	}
	resp.Steps = append(resp.Steps, step)

	return resp, nil
}

// ─── Per-step helpers ─────────────────────────────────────────────────

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

// parseFirstSNSARN looks up <Field>arn:aws:sns:…</Field> in either parsed
// XML-as-JSON or raw text. xmlToJson on the integrations side flattens
// some shapes; we accept either.
func parseFirstSNSARN(body, field string) string {
	// Probe the structured shape first.
	var probe map[string]any
	_ = json.Unmarshal([]byte(body), &probe)
	if v := walkForString(probe, field); v != "" {
		return v
	}
	// Fall back to raw substring extraction.
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
// value at any leaf with the named key. Lets us pull TopicArn /
// SubscriptionArn out of either the verbose
// {CreateTopicResponse:{CreateTopicResult:{TopicArn:…}}} shape or the
// flatter {TopicArn:…} that xmlToJson sometimes produces.
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
	// arn:aws:sns:eu-west-1:123456789012:topic-name
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

func bootstrapVerifyDomain(ctx *sdk.AppCtx, connID int64, domain string) ([]string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "verify_domain", map[string]any{
		"EmailIdentity": domain,
	})
	if err != nil {
		return nil, fmt.Errorf("verify_domain: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("verify_domain non-2xx: %s", truncateResData(res))
	}
	var probe struct {
		DkimAttributes struct {
			Tokens []string `json:"Tokens"`
		} `json:"DkimAttributes"`
	}
	_ = json.Unmarshal(res.Data, &probe)
	return probe.DkimAttributes.Tokens, nil
}

func bootstrapPublishDNS(ctx *sdk.AppCtx, domain, region string, dkimTokens []string) []bootstrapStep {
	out := []bootstrapStep{}
	for i, tok := range dkimTokens {
		out = append(out, bootstrapPublishDNSRecord(
			ctx,
			fmt.Sprintf("dns_dkim_%d", i+1),
			domain,
			tok+"._domainkey",
			"CNAME",
			tok+".dkim.amazonses.com",
		))
	}
	out = append(out, bootstrapPublishDNSRecord(
		ctx, "dns_mx", domain, "@", "MX",
		"10 inbound-smtp."+region+".amazonaws.com",
	))
	return out
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
		"RuleSetName": ruleSetName,
		// One S3Action that ALSO publishes the SES inbound notification
		// to the SNS topic (TopicArn is a sub-field of S3Action). This
		// keeps a single atomic SES action — the .eml lands in S3 and
		// the SNS post arrives with bucketName + objectKey inline so
		// the inbound webhook can fetch via aws-s3.get_object.
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
	// Skip if an identical endpoint is already subscribed.
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
