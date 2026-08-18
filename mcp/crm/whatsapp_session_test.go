package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// waStubPlatform serves a canned message_list payload and counts the
// calls, so a test can assert both what CRM asked Messaging for and
// that it didn't ask at all when the input was rejected up front.
type waStubPlatform struct {
	tk.BasePlatformClient
	messages []map[string]any
	calls    []map[string]any
	tools    []string
}

func (p *waStubPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName:   "crm",
		InstallID: 99,
		ProjectID: "test-proj",
		Bindings:  map[string]any{"messaging": float64(42)},
	}, nil
}

func (p *waStubPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "messaging", Status: "running", ProjectID: "test-proj"}, nil
}

func (p *waStubPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.tools = append(p.tools, tool)
	p.calls = append(p.calls, input)
	if out == nil {
		return nil
	}
	payload := map[string]any{"ok": true}
	if tool == "message_list" {
		msgs := p.messages
		if msgs == nil {
			msgs = []map[string]any{}
		}
		payload = map[string]any{"messages": msgs, "count": len(msgs)}
	}
	b, _ := json.Marshal(payload)
	return json.Unmarshal(b, out)
}

func TestWhatsAppAddress_NormalisesAndRejects(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain E.164", "+15551234567", "+15551234567", false},
		{"spaced and dashed", "+1 555-123-4567", "+15551234567", false},
		{"parenthesised", "+1 (555) 123-4567", "+15551234567", false},
		{"whatsapp scheme", "whatsapp:+15551234567", "+15551234567", false},
		{"tel scheme with spacing", "tel:+1 555 123 4567", "+15551234567", false},
		{"surrounding whitespace", "  +15551234567  ", "+15551234567", false},
		{"non-phone", "banana", "", true},
		{"empty", "", "", true},
		{"blank", "   ", "", true},
		{"missing plus", "15551234567", "", true},
		{"too short", "+12", "", true},
		{"leading zero country code", "+05551234567", "", true},
		{"email", "alice@example.com", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := whatsAppAddress("to", tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("whatsAppAddress(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("whatsAppAddress(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("whatsAppAddress(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The error has to name the expected format — an agent that gets back
// a bare "invalid" can't tell a typo from an unreachable contact.
func TestWhatsAppAddress_ErrorNamesFieldAndFormat(t *testing.T) {
	_, err := whatsAppAddress("to", "banana")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"to", "banana", "E.164", "+15551234567"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

// The regression this whole change exists for: a correct number in a
// different notation used to compare unequal and report active:false.
func TestCheckWhatsAppSession_MatchesAcrossNotation(t *testing.T) {
	pf := &waStubPlatform{messages: []map[string]any{{
		"from":              "whatsapp:+1 555-123-4567",
		"to":                []any{"+1 (555) 000-1111"},
		"matched_recipient": "",
		"received_at":       "2026-08-18T10:00:00Z",
	}}}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}

	out, err := app.checkWhatsAppSession(ctx, "test-proj", "+15550001111", "+1 555-123-4567")
	if err != nil {
		t.Fatal(err)
	}
	if active, _ := out["active"].(bool); !active {
		t.Fatalf("active=false for a matching number in a different notation: %#v", out)
	}
	if out["last_inbound"] != "2026-08-18T10:00:00Z" {
		t.Fatalf("last_inbound=%v", out["last_inbound"])
	}
	// Both sides of the response are reported canonically.
	if out["to"] != "+15551234567" || out["from"] != "+15550001111" {
		t.Fatalf("addresses not canonical in response: %#v", out)
	}
	// Messaging filters `address` by exact equality, so the value we
	// send upstream has to be canonical too, not the caller's notation.
	if len(pf.calls) != 1 {
		t.Fatalf("message_list calls=%d, want 1", len(pf.calls))
	}
	if got := pf.calls[0]["address"]; got != "+15551234567" {
		t.Fatalf("message_list address=%v, want canonical +15551234567", got)
	}
}

func TestCheckWhatsAppSession_NoMatchStaysInactive(t *testing.T) {
	pf := &waStubPlatform{messages: []map[string]any{{
		"from":              "+15559998888",
		"to":                []any{"+15550001111"},
		"matched_recipient": "+15550001111",
		"received_at":       "2026-08-18T10:00:00Z",
	}}}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}

	out, err := app.checkWhatsAppSession(ctx, "test-proj", "+15550001111", "+15551234567")
	if err != nil {
		t.Fatal(err)
	}
	if active, _ := out["active"].(bool); active {
		t.Fatalf("active=true for a different sender: %#v", out)
	}
}

func TestCheckWhatsAppSession_RejectsNonPhoneWithoutCallingMessaging(t *testing.T) {
	pf := &waStubPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}

	if _, err := app.checkWhatsAppSession(ctx, "test-proj", "+15550001111", "banana"); err == nil {
		t.Fatal("expected error for non-phone recipient")
	}
	if len(pf.calls) != 0 {
		t.Fatalf("messaging called %d times for invalid input, want 0", len(pf.calls))
	}
}

func TestResolveWhatsAppSessionRecipient_ValidatesToArg(t *testing.T) {
	pf := &waStubPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}

	if _, err := app.resolveWhatsAppSessionRecipient(ctx, "test-proj", map[string]any{"to": "banana"}); err == nil {
		t.Fatal("expected error for non-phone to")
	}
	got, err := app.resolveWhatsAppSessionRecipient(ctx, "test-proj", map[string]any{"to": "+1 555-123-4567"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "+15551234567" {
		t.Fatalf("to=%q, want canonical +15551234567", got)
	}
}

// A contact whose stored phone is in a human notation must still
// resolve — the shape check applies after normalisation, not before.
func TestResolveWhatsAppSessionRecipient_FromContactChannel(t *testing.T) {
	pf := &waStubPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{
		"display_name": "Dana",
		"channels": []any{
			map[string]any{"kind": "phone", "value": "+1 555-123-4567", "is_primary": true},
		},
	})

	got, err := app.resolveWhatsAppSessionRecipient(ctx, "test-proj", map[string]any{"contact_id": c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got != "+15551234567" {
		t.Fatalf("to=%q, want +15551234567", got)
	}
}

func TestResolveWhatsAppSessionSender_RejectsBadExplicitFrom(t *testing.T) {
	pf := &waStubPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}

	// An explicit bad sender is an error, not a reason to silently
	// fall back to a discovered one.
	if _, err := app.resolveWhatsAppSessionSender(ctx, "test-proj", "banana"); err == nil {
		t.Fatal("expected error for non-phone from")
	}
	if len(pf.calls) != 0 {
		t.Fatalf("senders_list called %d times, want 0", len(pf.calls))
	}
}

func TestResolveWhatsAppSessionSender_NormalisesConfiguredDefault(t *testing.T) {
	pf := &waStubPlatform{}
	ctx := newTestCtx(t,
		tk.WithPlatform(pf),
		tk.WithConfig(map[string]string{"default_sender_phone": "+1 (555) 000-1111"}),
	)
	app := &App{}

	got, err := app.resolveWhatsAppSessionSender(ctx, "test-proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "+15550001111" {
		t.Fatalf("from=%q, want +15550001111", got)
	}
}
