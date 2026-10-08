package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestSenderSetupDoesNotRequestDMARCReports(t *testing.T) {
	for _, address := range []string{"example.com", "contact@example.com"} {
		t.Run(address, func(t *testing.T) {
			plat := &stubPlatform{
				bindingsOverride: map[string]any{"email_provider": float64(1), "domains": float64(42)},
				replyByTool: map[string]*sdk.ExecuteResult{
					"verify_domain": {Success: true, Status: 200, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)},
				},
				callAppReply: json.RawMessage(`{"action":"created"}`),
				callAppReplyByTool: map[string]json.RawMessage{
					"domain_records_list": json.RawMessage(`{"records":[]}`),
				},
			}
			ctx := newTestCtx(t, plat)
			out, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": address})
			if err != nil {
				t.Fatal(err)
			}
			assertPolicy := func(record string) {
				t.Helper()
				if !strings.Contains(record, "v=DMARC1") || !strings.Contains(record, "p=none") {
					t.Fatalf("DMARC policy missing: %q", record)
				}
				if strings.Contains(record, "rua=") || strings.Contains(record, "ruf=") || strings.Contains(record, "mailto:") {
					t.Fatalf("sender setup requests report emails: %q", record)
				}
			}
			returned, published := false, false
			for _, record := range out.(*sendersCreateResp).DnsRecords {
				if record["name"] == "_dmarc.example.com" && record["type"] == "TXT" {
					returned = true
					assertPolicy(record["value"])
				}
			}
			for _, call := range plat.callAppCalls {
				if call.Tool == "domain_records_set" && call.Input["name"] == "_dmarc" {
					published = true
					assertPolicy(call.Input["value"].(string))
				}
			}
			if !returned || !published {
				t.Fatalf("DMARC must still be returned and published: returned=%v published=%v", returned, published)
			}
			identity, err := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "example.com")
			if err != nil || identity == nil {
				t.Fatalf("domain identity missing: %v", err)
			}
			var metadata map[string]any
			if err := json.Unmarshal([]byte(identity.Metadata), &metadata); err != nil {
				t.Fatal(err)
			}
			assertPolicy(metadata["dmarc_record"].(string))
		})
	}
}
