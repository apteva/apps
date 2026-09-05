package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func TestReceiptUpdateRefusesMalformedState(t *testing.T) {
	for _, body := range []string{
		`{"Rule":{"Name":"inbound","Actions":null,"Recipients":{"member":"old.example.com"}}}`,
		`{"Rule":{"Name":"inbound","Actions":{"member":{"S3Action":{"BucketName":"keep"}}},"Recipients":{"unexpected":"old.example.com"}}}`,
		`{"Rule":{"Name":"inbound","Actions":{"member":{"S3Action":{"BucketName":"keep"}}},"Recipients":[17]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			plat := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{"describe_receipt_rule": {Success: true, Data: json.RawMessage(body)}}}
			ctx := newTestCtx(t, plat)
			if err := mergeReceiptRuleRecipient(ctx, 1, "existing", "inbound", "new.example.com", "unused", "unused"); err == nil {
				t.Fatal("malformed response accepted")
			}
			if len(plat.executeCalls) != 1 {
				t.Fatalf("provider mutated: %+v", plat.executeCalls)
			}
		})
	}
}
func TestBucketPolicyMergePreservesOtherServices(t *testing.T) {
	merged, err := mergeBucketPolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Sid":"customer","Effect":"Allow","Principal":{"AWS":"other"}},{"Sid":"messaging","Resource":"old"}]}`), []byte(`{"Statement":[{"Sid":"messaging","Resource":"new"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	json.Unmarshal([]byte(merged), &policy)
	rows := policy["Statement"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["Sid"] != "customer" || rows[1].(map[string]any)["Resource"] != "new" {
		t.Fatalf("policy=%s", merged)
	}
	if _, err := mergeBucketPolicy([]byte(`{"Statement":17}`), []byte(`{"Statement":[]}`)); err == nil {
		t.Fatal("malformed policy accepted")
	}
}
func TestDNSPlansPreserveExistingMailPolicies(t *testing.T) {
	records := []setupDNSRecord{{ID: "spf", Name: "example.com", Type: "TXT", Value: "v=spf1 include:existing.example -all"}, {ID: "txt", Name: "example.com", Type: "TXT", Value: "site-verification=keep"}}
	next, id, same, err := planDNSRecord(records, "example.com", "@", "TXT", "v=spf1 include:amazonses.com ~all")
	if err != nil || same || id != "spf" || next != "v=spf1 include:existing.example include:amazonses.com -all" {
		t.Fatalf("SPF plan %s %s %v %v", next, id, same, err)
	}
	records = append(records, setupDNSRecord{ID: "duplicate", Name: "example.com", Type: "TXT", Value: "v=spf1 include:amazonses.com ~all"})
	if _, _, _, err := planDNSRecord(records, "example.com", "@", "TXT", "v=spf1 include:amazonses.com ~all"); err == nil {
		t.Fatal("multiple SPF policies accepted")
	}
	_, _, same, err = planDNSRecord([]setupDNSRecord{{Name: "_dmarc.example.com", Type: "TXT", Value: "v=DMARC1; p=reject"}}, "example.com", "_dmarc", "TXT", "v=DMARC1; p=none")
	if err != nil || !same {
		t.Fatal("existing DMARC was not preserved")
	}
	if _, _, _, err := planDNSRecord([]setupDNSRecord{{Name: "example.com", Type: "MX", Value: "10 existing.example"}}, "example.com", "@", "MX", "10 inbound-smtp.eu-west-1.amazonaws.com"); err == nil {
		t.Fatal("existing inbound MX replaced")
	}
}
func TestSESConfigurationIsInstallationScoped(t *testing.T) {
	plat := &stubPlatform{whoAmIOverride: &sdk.InstallIdentity{InstallID: 1}}
	ctx := newTestCtx(t, plat)
	first := scopedSESConfigName(ctx, 1)
	plat.whoAmIOverride.InstallID = 2
	second := scopedSESConfigName(ctx, 1)
	if first == second {
		t.Fatal("two installations share config")
	}
	if err := saveSESConfigName(ctx, 1, second); err != nil {
		t.Fatal(err)
	}
	if sesConfigName(ctx, 1) != second {
		t.Fatal("scoped config not persisted")
	}
}
func TestRegionsRejectMismatchedConnections(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{connectionCreds: map[int64]map[string]string{1: {"region": "eu-west-1"}, 3: {"region": "us-east-1"}}})
	if _, err := validateProviderRegions(ctx, "", 1, 3); err == nil {
		t.Fatal("region mismatch accepted")
	}
}
func TestPublicIdentityMetadataRedactsLegacyToken(t *testing.T) {
	result := identityRowToMap(&identityRow{InboundConfig: `{"url":"https://gateway.example/webhooks?api_key=legacy-secret&project_id=p"}`, Metadata: `{"nested":{"callback":"https://gateway.example/path?api_key=legacy-secret"}}`})
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "legacy-secret") || strings.Contains(string(raw), "api_key") {
		t.Fatalf("legacy secret leaked %s", raw)
	}
}
func TestSESFailedSecondPagePreservesExistingSenders(t *testing.T) {
	plat := &stubPlatform{executeOverride: func(tool string, prior int) *sdk.ExecuteResult {
		if tool != "list_identities" {
			return nil
		}
		if prior == 0 {
			return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"EmailIdentities":[],"NextToken":"next"}`)}
		}
		return &sdk.ExecuteResult{Success: false, Data: json.RawMessage(`{"error":"unavailable"}`)}
	}}
	ctx := newTestCtx(t, plat)
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: fromAcme, Kind: "email_mailbox", Provider: "aws-ses", Verified: true, SendingEnabled: true})
	if err := (&App{}).refreshSESIdentities(ctx, "test-proj", 1); err == nil {
		t.Fatal("incomplete inventory accepted")
	}
	row, err := dbFindSender(ctx.AppDB(), "test-proj", "email", fromAcme)
	if err != nil || row == nil || !row.Verified {
		t.Fatalf("partial inventory removed sender: %+v %v", row, err)
	}
}
func TestWhatsAppInventoryReadsSecondPage(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.executeOverride = func(tool string, prior int) *sdk.ExecuteResult {
		if tool != "list_whatsapp_senders" {
			return nil
		}
		if prior == 0 {
			return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"senders":[],"meta":{"next_page_url":"https://messaging.twilio.com/v2/Channels/Senders?PageToken=next"}}`)}
		}
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"senders":[{"sid":"XE-test","sender_id":"whatsapp:+15551234567","status":"ONLINE"}],"meta":{"next_page_url":null}}`)}
	}
	ctx := newTestCtx(t, plat)
	preseedSender(t, ctx, senderUpsert{Channel: "whatsapp", Address: "+15551234567", Kind: "phone", Provider: "twilio", ProviderIdentityID: "XE-test", Verified: true, SendingEnabled: true})
	if err := (&App{}).refreshTwilioNumbers(ctx, "test-proj", 2); err != nil {
		t.Fatal(err)
	}
	row, err := dbFindSender(ctx.AppDB(), "test-proj", "whatsapp", "+15551234567")
	if err != nil || row == nil || !row.Verified {
		t.Fatalf("second page sender: %+v %v", row, err)
	}
	found := false
	for _, call := range plat.executeCalls {
		if call.Tool == "list_whatsapp_senders" && call.Input["PageToken"] == "next" {
			found = true
		}
	}
	if !found {
		t.Fatal("continuation token not sent")
	}
}

func TestRegionMustBeKnownBeforeSetup(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{connectionCreds: map[int64]map[string]string{1: {}}})
	if _, err := validateProviderRegions(ctx, "eu-west-1", 1); err == nil {
		t.Fatal("unknown provider region accepted")
	}
}

func TestMissingSESParentDisablesInheritedMailbox(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	parent := preseedIdentity(t, ctx, identityUpsert{Kind: "email_domain", Address: "example.com", Provider: "aws-ses", Verified: true, VerificationStatus: "SUCCESS", DkimStatus: "SUCCESS", InboundBootstrapped: true})
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: "support@example.com", Kind: "email_mailbox", Provider: "aws-ses", Verified: true, SendingEnabled: true, ParentIdentityID: parent})
	if err := (&App{}).refreshSESIdentities(ctx, "test-proj", 1); err != nil {
		t.Fatal(err)
	}
	mailbox, err := dbFindSender(ctx.AppDB(), "test-proj", "email", "support@example.com")
	if err != nil || mailbox == nil || mailbox.Verified || mailbox.SendingEnabled || mailbox.LastSyncedAt == nil {
		t.Fatalf("stale inherited sender=%+v %v", mailbox, err)
	}
	anchor, _ := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "example.com")
	if anchor == nil || anchor.Verified || anchor.DeletedAt != nil || !anchor.InboundBootstrapped {
		t.Fatalf("anchor state=%+v", anchor)
	}
}
func TestCreateSenderAppliesRequestedDefault(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool["list_phone_numbers"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN-one","phone_number":"+15551234567","capabilities":{"sms":true}}]}`)}
	ctx := newTestCtx(t, plat)
	_, err := (&App{}).sendersCreateImpl(ctx, sendersCreateReq{ProjectID: "test-proj", Channel: "sms", Address: "+15551234567", SetDefault: true, Inbound: "false"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := dbDefaultSender(ctx.AppDB(), "test-proj", "sms")
	if err != nil || row == nil || row.Address != "+15551234567" {
		t.Fatalf("default=%+v %v", row, err)
	}
}

func TestMalformedTwilioPaginationCannotRemoveSenders(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool["list_phone_numbers"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"incoming_phone_numbers":[],"next_page_uri":123}`)}
	ctx := newTestCtx(t, plat)
	preseedSender(t, ctx, senderUpsert{Channel: "sms", Address: "+15551234567", Kind: "phone", Provider: "twilio", Verified: true})
	if err := (&App{}).refreshTwilioNumbers(ctx, "test-proj", 2); err == nil {
		t.Fatal("malformed pagination accepted")
	}
	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "sms", "+15551234567")
	if row == nil || row.DeletedAt != nil {
		t.Fatal("incomplete inventory removed sender")
	}
}
func TestRemovingInheritedMailboxNeverDeletesStandaloneProviderIdentity(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	parent := preseedIdentity(t, ctx, identityUpsert{Kind: "email_domain", Address: "example.com", Provider: "aws-ses", Verified: false})
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: "support@example.com", Kind: "email_mailbox", Provider: "aws-ses", ParentIdentityID: parent})
	if _, err := (&App{}).toolSendersDelete(ctx, map[string]any{"channel": "email", "address": "support@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range plat.executeCalls {
		if call.Tool == "delete_identity" {
			t.Fatal("inherited mailbox triggered provider deletion")
		}
	}
}

func TestActiveRuleSetRejectsUncertainMetadata(t *testing.T) {
	for _, raw := range []string{`{"Metadata":{}}`, `{"Metadata":{"Name":17}}`, `{"Metadata":{"Name":""}}`} {
		plat := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{"describe_active_receipt_rule_set": {Success: true, Data: json.RawMessage(raw)}}}
		ctx := newTestCtx(t, plat)
		if _, err := activeReceiptRuleSet(ctx, 1); err == nil {
			t.Fatalf("ambiguous active ruleset accepted: %s", raw)
		}
	}
}
