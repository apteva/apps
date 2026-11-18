package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// sesDescribeReceiptRuleReal is the shape the aws-ses integration returns for
// describe_receipt_rule (captured from a live account, account ID and domains
// replaced). Each action lists all nine action types with the unused ones
// null, and unset fields inside the populated action are null too.
const sesDescribeReceiptRuleReal = `{"DescribeReceiptRuleResponse":{"DescribeReceiptRuleResult":{"Rule":{
	"Actions":[{"AddHeaderAction":null,"BounceAction":null,"ConnectAction":null,"InvokeMailManagerRulesetAction":null,"LambdaAction":null,
		"S3Action":{"BucketName":"apteva-ses-inbound-47","IamRoleArn":null,"KmsKeyArn":null,"ObjectKeyPrefix":null,"TopicArn":"arn:aws:sns:eu-west-1:123456789012:apteva-ses-inbound-47"},
		"SNSAction":null,"StopAction":null,"WorkmailAction":null}],
	"Enabled":true,"Name":"messaging-inbound-47",
	"Recipients":["existing-one.com","existing-two.com"],
	"ScanEnabled":true,"TlsPolicy":"Optional"}},
	"ResponseMetadata":{"RequestId":"d4a3fe2e-2cb0-4165-9f39-c807c901c8f9"}}}`

func TestMergeReceiptRuleRecipient_AcceptsRealSESActionShape(t *testing.T) {
	plat := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{
		"describe_receipt_rule": {Success: true, Data: json.RawMessage(sesDescribeReceiptRuleReal)},
		"update_receipt_rule":   {Success: true, Data: json.RawMessage(`{}`)},
	}}
	ctx := newTestCtx(t, plat)
	if err := mergeReceiptRuleRecipient(ctx, 1, "apteva-default", "messaging-inbound-47", "newdomain.com", "unused", "unused"); err != nil {
		t.Fatal(err)
	}
	if len(plat.executeCalls) != 2 || plat.executeCalls[1].Tool != "update_receipt_rule" {
		t.Fatalf("calls=%+v", plat.executeCalls)
	}
	got := plat.executeCalls[1].Input
	want := map[string]any{
		"RuleSetName":                               "apteva-default",
		"Rule.Name":                                 "messaging-inbound-47",
		"Rule.Recipients.member.1":                  "existing-one.com",
		"Rule.Recipients.member.2":                  "existing-two.com",
		"Rule.Recipients.member.3":                  "newdomain.com",
		"Rule.Actions.member.1.S3Action.BucketName": "apteva-ses-inbound-47",
		"Rule.Actions.member.1.S3Action.TopicArn":   "arn:aws:sns:eu-west-1:123456789012:apteva-ses-inbound-47",
		"Rule.Enabled":                              "true",
		"Rule.ScanEnabled":                          "true",
		"Rule.TlsPolicy":                            "Optional",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%v, want %v", k, got[k], v)
		}
	}
	// Null action types and null fields must not be sent back to SES.
	for k, v := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected update field %s=%v", k, v)
		}
	}
}

func TestValidAWSActions(t *testing.T) {
	parse := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"real SES list with null types", `[{"S3Action":{"BucketName":"b"},"SNSAction":null,"LambdaAction":null,"StopAction":null}]`, true},
		{"two actions, nulls each", `[{"S3Action":{"BucketName":"b"},"SNSAction":null},{"S3Action":null,"SNSAction":{"TopicArn":"t"}}]`, true},
		{"member wrapper single", `{"member":{"S3Action":{"BucketName":"b"}}}`, true},
		{"empty object type ignored", `[{"S3Action":{"BucketName":"b"},"BounceAction":{}}]`, true},
		{"no populated type", `[{"S3Action":null,"SNSAction":null}]`, false},
		{"two populated types in one action", `[{"S3Action":{"BucketName":"b"},"SNSAction":{"TopicArn":"t"}}]`, false},
		{"non-action key", `[{"S3Action":{"BucketName":"b"},"Extra":null}]`, false},
		{"non-object type value", `[{"S3Action":"b"}]`, false},
		{"empty list", `[]`, false},
	}
	for _, c := range cases {
		if got := validAWSActions(parse(c.in)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func inboundDomainPlatform(describe string) *stubPlatform {
	return &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"inbound_storage":       float64(2),
			"inbound_notifications": float64(3),
		},
		whoAmIOverride: &sdk.InstallIdentity{AppName: "messaging", ProjectID: "test-proj", InstallID: 47, PublicURL: "https://test.public.example"},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_topic":                {Success: true, Status: 200, Data: json.RawMessage(`{"CreateTopicResponse":{"CreateTopicResult":{"TopicArn":"arn:aws:sns:eu-west-1:123456789012:apteva-ses-inbound-47"}}}`)},
			"set_topic_attributes":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_s3_bucket":            {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"put_s3_bucket_policy":        {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"verify_domain":               {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"PENDING"}}`)},
			"create_receipt_rule_set":     {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"create_receipt_rule":         {Success: false, Status: 400, Data: json.RawMessage(`{"Error":{"Code":"AlreadyExists","Message":"Rule messaging-inbound-47 already exists"}}`)},
			"describe_receipt_rule":       {Success: true, Status: 200, Data: json.RawMessage(describe)},
			"update_receipt_rule":         {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"set_active_receipt_rule_set": {Success: true, Status: 200, Data: json.RawMessage(`{}`)},
			"subscribe":                   {Success: true, Status: 200, Data: json.RawMessage(`{"SubscribeResponse":{"SubscribeResult":{"SubscriptionArn":"arn:sub"}}}`)},
		},
	}
}

// The reported failure: the shared rule already exists, so the new domain is
// merged into it. With SES's real response that must now complete.
func TestSendersCreate_Domain_MergesIntoExistingRuleWithRealSESShape(t *testing.T) {
	plat := inboundDomainPlatform(sesDescribeReceiptRuleReal)
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "newdomain.com", "inbound": "true"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if !hasStep(resp.Steps, "create_receipt_rule", true) {
		t.Fatalf("create_receipt_rule did not succeed: %+v", resp.Steps)
	}
	if resp.InboundFailed || resp.Inbound == nil || !resp.Inbound.Bootstrapped || resp.Inbound.Error != "" {
		t.Fatalf("inbound should be bootstrapped: failed=%v inbound=%+v", resp.InboundFailed, resp.Inbound)
	}
	var update *executeCall
	for i := range plat.executeCalls {
		if plat.executeCalls[i].Tool == "update_receipt_rule" {
			update = &plat.executeCalls[i]
		}
	}
	if update == nil || update.Input["Rule.Recipients.member.3"] != "newdomain.com" {
		t.Fatalf("new domain not merged into shared rule: %+v", update)
	}
}

// A failed inbound step must be visible at the top level, not only as one
// ok:false entry inside steps.
func TestSendersCreate_Domain_InboundFailureIsReportedAtTopLevel(t *testing.T) {
	broken := strings.Replace(sesDescribeReceiptRuleReal, `"SNSAction":null`, `"SNSAction":{"TopicArn":"t"}`, 1)
	plat := inboundDomainPlatform(broken)
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "newdomain.com", "inbound": "true"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if !resp.InboundFailed || resp.Inbound == nil || resp.Inbound.Bootstrapped {
		t.Fatalf("expected inbound_failed: %+v / %+v", resp.InboundFailed, resp.Inbound)
	}
	if !strings.HasPrefix(resp.Inbound.Error, "create_receipt_rule: ") || !strings.Contains(resp.Inbound.Error, "missing actions") {
		t.Fatalf("inbound.error=%q", resp.Inbound.Error)
	}
	if !strings.HasPrefix(resp.NextStep, "Inbound mail setup failed (create_receipt_rule: ") {
		t.Fatalf("next_step=%q", resp.NextStep)
	}
	raw, _ := json.Marshal(resp)
	if !strings.Contains(string(raw), `"inbound_failed":true`) {
		t.Fatalf("inbound_failed missing from JSON: %s", raw)
	}
}

// Outbound-only setups are not inbound failures.
func TestSendersCreate_Domain_InboundSkippedIsNotFailure(t *testing.T) {
	plat := inboundDomainPlatform(sesDescribeReceiptRuleReal)
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "newdomain.com", "inbound": "false"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if resp.InboundFailed || strings.HasPrefix(resp.NextStep, "Inbound mail setup failed") {
		t.Fatalf("outbound-only setup flagged as inbound failure: %+v", resp)
	}
}

// Mailbox creation verifies the parent domain; its inbound failure must
// carry through to the mailbox result too.
func TestSendersCreate_Mailbox_CarriesParentInboundFailure(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{
			"email_provider":        float64(1),
			"domains":               float64(42),
			"inbound_storage":       float64(3),
			"inbound_notifications": float64(4),
		},
		replyByTool: map[string]*sdk.ExecuteResult{
			"create_topic":  {Success: true, Status: 200, Data: json.RawMessage(`{"TopicArn":"arn:aws:sns:eu-west-1:123456789012:apteva-test"}`)},
			"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["t1","t2","t3"],"Status":"SUCCESS"}}`)},
		},
		callAppReply: json.RawMessage(`{"action":"created"}`),
	}
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "ops@newdomain.com"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)
	if !resp.InboundFailed || !strings.HasPrefix(resp.NextStep, "Inbound mail setup failed (sns_subscribe_webhook: ") {
		t.Fatalf("mailbox result hides parent inbound failure: failed=%v next=%q", resp.InboundFailed, resp.NextStep)
	}
	if !strings.Contains(resp.NextStep, "ready to send") {
		t.Fatalf("mailbox next step lost: %q", resp.NextStep)
	}
}
