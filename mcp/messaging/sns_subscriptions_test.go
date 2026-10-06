package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

const canonicalInboundURL = "https://mail.example/api/apps/messaging/webhooks/ses-inbound?install_id=47"

func TestSNSCleanupConvergesToOneGlobalInboundAndPreservesForeignCallbacks(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "legacy-token")
	platform := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{
		"list_subscriptions_by_topic": subscriptionReply([]snsSubscription{
			{canonicalInboundURL, "arn:canonical"},
			{canonicalInboundURL, "arn:duplicate"},
			{canonicalInboundURL + "&project_id=alpha", "arn:project-alpha"},
			{canonicalInboundURL + "&project_id=beta", "arn:project-beta"},
			{"https://old.example/api/apps/messaging/webhooks/ses-inbound?api_key=legacy-token&project_id=gamma", "arn:legacy"},
			{"https://foreign.example/api/apps/messaging/webhooks/ses-inbound?install_id=47", "arn:foreign-origin"},
			{"https://mail.example/api/apps/messaging/webhooks/ses-inbound?install_id=48", "arn:foreign-install"},
			{"https://mail.example/api/apps/messaging/webhooks/ses-inbound?api_key=other-token", "arn:foreign-token"},
			{"https://mail.example/api/apps/messaging/webhooks/ses-bounces?install_id=47&project_id=alpha", "arn:bounces"},
			{"https://mail.example/api/apps/messaging/webhooks/twilio-inbound?install_id=47", "arn:other-path"},
		}),
	}}
	ctx := newTestCtx(t, platform)
	removed, err := cleanupStaleMessagingSNSSubscriptions(ctx, 3, "arn:topic", []string{canonicalInboundURL}, "alpha", true)
	if err != nil || len(removed) != 4 {
		t.Fatalf("removed=%v %v", removed, err)
	}
	got := []string{}
	for _, call := range platform.executeCalls {
		if call.Tool == "unsubscribe" {
			got = append(got, call.Input["SubscriptionArn"].(string))
		}
	}
	if !reflect.DeepEqual(got, []string{"arn:duplicate", "arn:project-alpha", "arn:project-beta", "arn:legacy"}) {
		t.Fatalf("unsubscribe=%v", got)
	}
}

func TestSNSCleanupRetainsLegacyCallbacksUntilGlobalConfirmed(t *testing.T) {
	for _, arn := range []string{"PendingConfirmation", "Deleted"} {
		t.Run(arn, func(t *testing.T) {
			platform := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{
				"list_subscriptions_by_topic": subscriptionReply([]snsSubscription{{canonicalInboundURL, arn}, {canonicalInboundURL + "&project_id=alpha", "arn:legacy"}}),
			}}
			ctx := newTestCtx(t, platform)
			if _, err := cleanupStaleMessagingSNSSubscriptions(ctx, 3, "arn:topic", []string{canonicalInboundURL}, "", false); err == nil {
				t.Fatal("expected unconfirmed canonical error")
			}
			for _, call := range platform.executeCalls {
				if call.Tool == "unsubscribe" {
					t.Fatal("working callback removed before confirmation")
				}
			}
			if arn == "PendingConfirmation" {
				got, exists, err := bootstrapSubscribeWebhook(ctx, 3, "arn:topic", canonicalInboundURL)
				if err != nil || !exists || got != arn {
					t.Fatalf("pending subscription duplicated: %s %v %v", got, exists, err)
				}
			}
		})
	}
}

func TestSNSMalformedInventoryNeverTriggersSubscribe(t *testing.T) {
	for _, data := range []string{`{}`, `{"ListSubscriptionsByTopicResult":null}`, `{"ListSubscriptionsByTopicResult":{}}`, `not json or XML`, `<WrongResponse/>`, `{"ListSubscriptionsByTopicResult":{"Subscriptions":{"member":{"Endpoint":"https://mail.example"}}}}`} {
		t.Run(data, func(t *testing.T) {
			platform := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{"list_subscriptions_by_topic": {Success: true, Data: json.RawMessage(data)}}}
			ctx := newTestCtx(t, platform)
			if _, _, err := bootstrapSubscribeWebhook(ctx, 3, "arn:topic", canonicalInboundURL); err == nil {
				t.Fatal("malformed inventory accepted")
			}
			for _, call := range platform.executeCalls {
				if call.Tool == "subscribe" {
					t.Fatal("subscribed with malformed inventory")
				}
			}
		})
	}
}

func TestSNSInventoryParsesXMLAndConvertedJSON(t *testing.T) {
	for _, data := range []string{
		`<ListSubscriptionsByTopicResponse><ListSubscriptionsByTopicResult><Subscriptions><member><Endpoint>https://mail.example</Endpoint><SubscriptionArn>arn:subscription</SubscriptionArn></member></Subscriptions></ListSubscriptionsByTopicResult></ListSubscriptionsByTopicResponse>`,
		`{"_name":"ListSubscriptionsByTopicResponse","ListSubscriptionsByTopicResult":{"Subscriptions":{"member":{"Endpoint":"https://mail.example","SubscriptionArn":"arn:subscription"}}}}`,
	} {
		subscriptions, err := parseSNSSubscriptions([]byte(data))
		if err != nil || !reflect.DeepEqual(subscriptions, []snsSubscription{{"https://mail.example", "arn:subscription"}}) {
			t.Fatalf("subscriptions=%v %v", subscriptions, err)
		}
	}
}

func TestSNSListFailureDoesNotCreateOrRemoveSubscription(t *testing.T) {
	platform := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{
		"list_subscriptions_by_topic": {Success: false, Status: 503, Data: json.RawMessage(`{"error":"unavailable"}`)},
	}}
	ctx := newTestCtx(t, platform)
	if _, _, err := bootstrapSubscribeWebhook(ctx, 3, "arn:topic", canonicalInboundURL); err == nil {
		t.Fatal("expected inventory error")
	}
	if _, err := cleanupStaleMessagingSNSSubscriptions(ctx, 3, "arn:topic", []string{canonicalInboundURL}, "", false); err == nil {
		t.Fatal("expected inventory error")
	}
	for _, call := range platform.executeCalls {
		if call.Tool == "subscribe" || call.Tool == "unsubscribe" {
			t.Fatal("mutated subscriptions with unknown inventory")
		}
	}
}

func TestSNSReconcilerVisitsDistinctBootstrappedTopicsOnce(t *testing.T) {
	platform := &stubPlatform{
		bindingsOverride: map[string]any{"inbound_notifications": float64(3)},
		whoAmIOverride:   &sdk.InstallIdentity{InstallID: 47, PublicURL: "https://mail.example"},
		replyByTool:      map[string]*sdk.ExecuteResult{"list_subscriptions_by_topic": subscriptionReply([]snsSubscription{{canonicalInboundURL, "arn:canonical"}})},
	}
	ctx := newTestCtx(t, platform)
	t.Setenv("APTEVA_PROJECT_ID", "")
	for _, entry := range []struct {
		project, domain, topic string
		bootstrapped           bool
	}{
		{"project-a", "alpha.example", "arn:topic-1", true},
		{"project-b", "beta.example", "arn:topic-1", true},
		{"project-c", "gamma.example", "arn:topic-2", true},
		{"project-d", "delta.example", "arn:topic-ignored", false},
	} {
		id, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{ProjectID: entry.project, Kind: "email_domain", Address: entry.domain, Provider: "aws-ses"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ctx.AppDB().Exec(`UPDATE identities SET inbound_bootstrapped=?,inbound_config=? WHERE id=?`, entry.bootstrapped, string(mustJSON(map[string]string{"topic_arn": entry.topic})), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&App{}).reconcileSESInboundSubscriptions(ctx); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, call := range platform.executeCalls {
		if call.Tool == "list_subscriptions_by_topic" {
			counts[call.Input["TopicArn"].(string)]++
		}
		if call.Tool == "subscribe" || call.Tool == "unsubscribe" {
			t.Fatal("already canonical subscriptions changed")
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"arn:topic-1": 2, "arn:topic-2": 2, testSNSTopicARN: 2}) {
		t.Fatalf("topic visits=%v", counts)
	}
	for _, worker := range (&App{}).Workers() {
		if worker.Name == "ses-inbound-subscriptions" && strings.Contains(worker.Schedule, "5m") {
			return
		}
	}
	t.Fatal("reconciliation worker not registered")
}
