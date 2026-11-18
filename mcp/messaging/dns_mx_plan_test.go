package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Spaceship (through Domains) returns MX records with the host in value and
// the priority in a separate prio field. Re-running senders_create on a
// domain whose MX is already correct must not report a conflict.
const spaceshipMXInventory = `{"records":[
	{"id":"mx-root","name":"@","type":"MX","value":"inbound-smtp.eu-west-1.amazonaws.com","prio":10,"ttl":1800},
	{"id":"mx-mail","name":"mail","type":"MX","value":"feedback-smtp.eu-west-1.amazonses.com","prio":10,"ttl":1800}
]}`

func decodeInventory(t *testing.T, raw string) []setupDNSRecord {
	t.Helper()
	var inv struct {
		Records []setupDNSRecord `json:"records"`
	}
	if err := json.Unmarshal([]byte(raw), &inv); err != nil {
		t.Fatal(err)
	}
	return inv.Records
}

func TestPlanDNSRecord_MXWithSeparatePriorityIsUnchanged(t *testing.T) {
	records := decodeInventory(t, spaceshipMXInventory)
	for _, c := range []struct{ name, value, id string }{
		{"@", "10 inbound-smtp.eu-west-1.amazonaws.com", "mx-root"},
		{"mail", "10 feedback-smtp.eu-west-1.amazonses.com", "mx-mail"},
	} {
		next, id, unchanged, err := planDNSRecord(records, "hemven.com", c.name, "MX", c.value)
		if err != nil || !unchanged || id != c.id || next != c.value {
			t.Fatalf("%s: next=%q id=%q unchanged=%v err=%v", c.name, next, id, unchanged, err)
		}
	}
}

func TestPlanDNSRecord_MXComparison(t *testing.T) {
	const proposed = "10 inbound-smtp.eu-west-1.amazonaws.com"
	cases := []struct {
		label string
		row   string
		same  bool
	}{
		{"priority inside value", `{"name":"@","type":"MX","value":"10 inbound-smtp.eu-west-1.amazonaws.com"}`, true},
		{"separate prio", `{"name":"@","type":"MX","value":"inbound-smtp.eu-west-1.amazonaws.com","prio":10}`, true},
		{"prio as string", `{"name":"@","type":"MX","value":"inbound-smtp.eu-west-1.amazonaws.com","prio":"10"}`, true},
		{"trailing dot and case", `{"name":"hemven.com.","type":"MX","value":"Inbound-SMTP.eu-west-1.amazonaws.com.","prio":10}`, true},
		{"trailing dot inside value", `{"name":"@","type":"MX","value":"10 inbound-smtp.eu-west-1.amazonaws.com."}`, true},
		{"different priority", `{"name":"@","type":"MX","value":"inbound-smtp.eu-west-1.amazonaws.com","prio":20}`, false},
		{"priority zero", `{"name":"@","type":"MX","value":"inbound-smtp.eu-west-1.amazonaws.com","prio":0}`, false},
		{"different host", `{"name":"@","type":"MX","value":"mx.other-provider.example","prio":10}`, false},
		{"bare host without prio", `{"name":"@","type":"MX","value":"inbound-smtp.eu-west-1.amazonaws.com"}`, false},
		{"malformed value", `{"name":"@","type":"MX","value":"ten inbound-smtp.eu-west-1.amazonaws.com"}`, false},
	}
	for _, c := range cases {
		records := decodeInventory(t, `{"records":[`+c.row+`]}`)
		_, _, unchanged, err := planDNSRecord(records, "hemven.com", "@", "MX", proposed)
		if c.same && (err != nil || !unchanged) {
			t.Errorf("%s: want unchanged, got unchanged=%v err=%v", c.label, unchanged, err)
		}
		if !c.same && (err == nil || !strings.Contains(err.Error(), "conflicts with proposed")) {
			t.Errorf("%s: want conflict, got unchanged=%v err=%v", c.label, unchanged, err)
		}
	}
}

// A priority-zero MX matches a proposed priority-zero value: the raw prio
// field keeps 0 distinct from a missing priority.
func TestPlanDNSRecord_MXPriorityZero(t *testing.T) {
	records := decodeInventory(t, `{"records":[{"name":"@","type":"MX","value":"mx.example.net","prio":0}]}`)
	if _, _, unchanged, err := planDNSRecord(records, "example.com", "@", "MX", "0 mx.example.net"); err != nil || !unchanged {
		t.Fatalf("unchanged=%v err=%v", unchanged, err)
	}
}

// End to end through the publish step: with Spaceship's inventory the MX
// steps succeed without writing DNS.
func TestBootstrapPublishDNSRecord_SpaceshipMXAlreadyPresent(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{"email_provider": float64(1), "domains": float64(42)},
		callAppReplyByTool: map[string]json.RawMessage{
			"domain_records_list": json.RawMessage(spaceshipMXInventory),
		},
	}
	ctx := newTestCtx(t, plat)
	for _, c := range []struct{ step, name, value string }{
		{"dns_mx", "@", "10 inbound-smtp.eu-west-1.amazonaws.com"},
		{"dns_mail_from_mx", "mail", "10 feedback-smtp.eu-west-1.amazonses.com"},
	} {
		st := bootstrapPublishDNSRecord(ctx, "test-proj", c.step, "hemven.com", c.name, "MX", c.value)
		if !st.OK || st.Skipped == "" || st.Error != "" {
			t.Fatalf("%s: %+v", c.step, st)
		}
	}
	for _, call := range plat.callAppCalls {
		if call.Tool != "domain_records_list" {
			t.Fatalf("existing MX must not be rewritten, got %s %+v", call.Tool, call.Input)
		}
	}
}
