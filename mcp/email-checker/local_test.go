package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeResolver struct {
	mxs       []*net.MX
	mxErr     error
	addresses []net.IPAddr
	ipErr     error
	ipCalls   int
}

func (r *fakeResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	return r.mxs, r.mxErr
}

func (r *fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	r.ipCalls++
	return r.addresses, r.ipErr
}

func verifierForTest(resolver mailResolver, probe smtpHostProbe) localVerifier {
	if probe == nil {
		probe = func(_ context.Context, mx, _ string, _ time.Duration) SMTPAttempt {
			testingPanic("unexpected SMTP probe for " + mx)
			return SMTPAttempt{}
		}
	}
	return localVerifier{
		resolver:  resolver,
		probeHost: probe,
		randomLocal: func() string {
			return "apteva-check-fixed"
		},
	}
}

func testingPanic(message string) { panic(message) }

func TestResolveMailRoutesSortsByPreferenceAndDeduplicates(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{
		{Host: "mx20.example.com.", Pref: 20},
		{Host: "mx10b.example.com.", Pref: 10},
		{Host: "mx10a.example.com.", Pref: 10},
		{Host: "mx10a.example.com.", Pref: 30},
	}}
	v := verifierForTest(resolver, nil)
	routes, status := v.resolveMailRoutes(context.Background(), "example.com")
	if status != "mx" {
		t.Fatalf("status=%q, want mx", status)
	}
	want := []string{"mx10a.example.com", "mx10b.example.com", "mx20.example.com"}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes=%v, want %v", routes, want)
	}
}

func TestNoMXUsesRFCImplicitAddressRoute(t *testing.T) {
	resolver := &fakeResolver{addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}}
	v := verifierForTest(resolver, nil)
	result := v.check(context.Background(), "Person@Example.com", false, time.Second)
	if result.DomainStatus != "implicit_mx" || !result.ImplicitMX {
		t.Fatalf("unexpected routing result: %#v", result)
	}
	if !reflect.DeepEqual(result.MX, []string{"example.com"}) || !result.Valid {
		t.Fatalf("implicit route was not retained: %#v", result)
	}
}

func TestNullMXIsDefinitivelyUndeliverable(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{{Host: ".", Pref: 0}}}
	v := verifierForTest(resolver, nil)
	result := v.check(context.Background(), "person@example.com", false, time.Second)
	if result.DomainStatus != "null_mx" || result.Verdict != "undeliverable" || result.Valid {
		t.Fatalf("unexpected Null MX result: %#v", result)
	}
	if resolver.ipCalls != 0 {
		t.Fatalf("Null MX must not fall back to A/AAAA; IP lookups=%d", resolver.ipCalls)
	}
}

func TestNoMXAndNoAddressIsUndeliverable(t *testing.T) {
	v := verifierForTest(&fakeResolver{}, nil)
	result := v.check(context.Background(), "person@example.com", false, time.Second)
	if result.DomainStatus != "no_mail_server" || result.Verdict != "undeliverable" || result.Recommendation != "do_not_send" {
		t.Fatalf("unexpected no-mail-server result: %#v", result)
	}
}

func TestTemporaryDNSErrorRemainsRetryableUnknown(t *testing.T) {
	v := verifierForTest(&fakeResolver{mxErr: errors.New("resolver unavailable")}, nil)
	result := v.check(context.Background(), "person@example.com", false, time.Second)
	if result.DomainStatus != "dns_error" || result.Verdict != "unknown" || result.Recommendation != "retry" {
		t.Fatalf("temporary DNS failure was overclassified: %#v", result)
	}
}

func TestClassificationHandlesAliasesSubdomainsAndTypos(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{{Host: "mx.example.net.", Pref: 10}}}
	v := verifierForTest(resolver, nil)

	role := v.check(context.Background(), "Sales+campaign@sub.mailinator.com", false, time.Second)
	if !role.Role || !role.Disposable {
		t.Fatalf("subdomain/alias classification failed: %#v", role)
	}

	typo := v.check(context.Background(), "Person@GAMIL.com", false, time.Second)
	if typo.SuggestedEmail != "Person@gmail.com" || typo.Verdict != "risky" {
		t.Fatalf("typo suggestion failed: %#v", typo)
	}
}

func TestSMTPAcceptedRecipientAndRejectedControlIsDeliverable(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{{Host: "mx.example.com.", Pref: 10}}}
	probe := func(_ context.Context, mx, email string, _ time.Duration) SMTPAttempt {
		if email == "person@example.com" {
			return SMTPAttempt{MX: mx, Status: "ok", Code: 250}
		}
		return SMTPAttempt{MX: mx, Status: "reject", Code: 550, Response: "mailbox unavailable"}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "deliverable" || result.Confidence != "high" || result.SMTP.CatchAll == nil || *result.SMTP.CatchAll {
		t.Fatalf("unexpected accepted result: %#v", result)
	}
	if len(result.SMTP.Attempts) != 2 || result.SMTP.Attempts[1].Kind != "catch_all" {
		t.Fatalf("catch-all control was not recorded: %#v", result.SMTP.Attempts)
	}
}

func TestSMTPAcceptingGeneratedRecipientIsCatchAll(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{{Host: "mx.example.com.", Pref: 10}}}
	probe := func(_ context.Context, mx, _ string, _ time.Duration) SMTPAttempt {
		return SMTPAttempt{MX: mx, Status: "ok", Code: 250}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "risky" || result.SMTP.RcptStatus != "catch_all" || result.SMTP.CatchAll == nil || !*result.SMTP.CatchAll {
		t.Fatalf("catch-all was not detected: %#v", result)
	}
}

func TestSMTPTriesNextMXAfterTemporaryFailure(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{
		{Host: "primary.example.com.", Pref: 10},
		{Host: "backup.example.com.", Pref: 20},
	}}
	probe := func(_ context.Context, mx, email string, _ time.Duration) SMTPAttempt {
		if mx == "primary.example.com" {
			return SMTPAttempt{Status: "tempfail", Code: 451}
		}
		if email == "person@example.com" {
			return SMTPAttempt{Status: "ok", Code: 250}
		}
		return SMTPAttempt{Status: "reject", Code: 550}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "deliverable" || result.SMTP.MX != "backup.example.com" {
		t.Fatalf("backup MX did not recover the check: %#v", result)
	}
	if len(result.SMTP.Attempts) != 3 {
		t.Fatalf("attempts=%d, want primary + backup + control", len(result.SMTP.Attempts))
	}
}

func TestSMTPRequiresConsistentRejectionAcrossAllMX(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{
		{Host: "primary.example.com.", Pref: 10},
		{Host: "backup.example.com.", Pref: 20},
	}}
	probe := func(_ context.Context, _ string, _ string, _ time.Duration) SMTPAttempt {
		return SMTPAttempt{Status: "reject", Code: 550}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "undeliverable" || result.Valid || result.SMTP.Informative == nil || !*result.SMTP.Informative {
		t.Fatalf("consistent rejection was not definitive: %#v", result)
	}
}

func TestSMTPPolicyBlockIsNeverMailboxRejection(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{{Host: "inbound-smtp.eu-west-1.amazonaws.com.", Pref: 10}}}
	probe := func(_ context.Context, mx, _ string, _ time.Duration) SMTPAttempt {
		return SMTPAttempt{
			MX: mx, Status: "blocked", Code: 550, EnhancedCode: "5.7.1",
			Response: "5.7.1 IP address blacklisted by recipient",
		}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "contact@marcoschwartz.com", true, time.Second)
	if result.Verdict != "unknown" || !result.Valid || result.Recommendation != "review" {
		t.Fatalf("policy block was overclassified: %#v", result)
	}
	if result.SMTP.RcptStatus != "blocked" || result.SMTP.Informative == nil || *result.SMTP.Informative {
		t.Fatalf("policy block should be explicit and non-informative: %#v", result.SMTP)
	}
}

func TestMixedMailboxRejectAndPolicyBlockStaysUnknown(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{
		{Host: "primary.example.com.", Pref: 10},
		{Host: "backup.example.com.", Pref: 20},
	}}
	probe := func(_ context.Context, mx, _ string, _ time.Duration) SMTPAttempt {
		if mx == "primary.example.com" {
			return SMTPAttempt{Status: "reject", Code: 550, EnhancedCode: "5.1.1"}
		}
		return SMTPAttempt{Status: "blocked", Code: 550, EnhancedCode: "5.7.1"}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "unknown" || result.SMTP.RcptStatus != "blocked" || !result.Valid {
		t.Fatalf("mixed policy result was overclassified: %#v", result)
	}
}

func TestMixedSMTPRejectAndTemporaryFailureStaysUnknown(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{
		{Host: "primary.example.com.", Pref: 10},
		{Host: "backup.example.com.", Pref: 20},
	}}
	probe := func(_ context.Context, mx, _ string, _ time.Duration) SMTPAttempt {
		if mx == "primary.example.com" {
			return SMTPAttempt{Status: "reject", Code: 550}
		}
		return SMTPAttempt{Status: "tempfail", Code: 451}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "unknown" || result.Recommendation != "retry" || !result.SMTP.Retryable {
		t.Fatalf("mixed result was overclassified: %#v", result)
	}
}

func TestSMTPConnectionFailureIsUnknownAndRetryable(t *testing.T) {
	resolver := &fakeResolver{mxs: []*net.MX{{Host: "mx.example.com.", Pref: 10}}}
	probe := func(_ context.Context, _ string, _ string, _ time.Duration) SMTPAttempt {
		return SMTPAttempt{Status: "connect_failed", Response: "connection refused"}
	}
	result := verifierForTest(resolver, probe).check(context.Background(), "person@example.com", true, time.Second)
	if result.Verdict != "unknown" || result.Recommendation != "retry" || !result.SMTP.Retryable {
		t.Fatalf("connection failure was overclassified: %#v", result)
	}
}

func TestParseSMTPError(t *testing.T) {
	code, message := parseSMTPErr(errors.New("550 5.1.1 mailbox unavailable"))
	if code != 550 || message != "5.1.1 mailbox unavailable" {
		t.Fatalf("parsed (%d, %q)", code, message)
	}
	if code, _ := parseSMTPErr(errors.New("connection reset")); code != 0 {
		t.Fatalf("transport error parsed as SMTP code %d", code)
	}
}

func TestSMTPWireProbeClassifiesRecipientReplies(t *testing.T) {
	tests := []struct {
		name       string
		rcptReply  string
		wantStatus string
		wantCode   int
	}{
		{name: "accepted", rcptReply: "250 2.1.5 recipient ok", wantStatus: "ok", wantCode: 250},
		{name: "rejected", rcptReply: "550 5.1.1 mailbox unavailable", wantStatus: "reject", wantCode: 550},
		{name: "AWS policy block", rcptReply: "550 5.7.1 IP address blacklisted by recipient", wantStatus: "blocked", wantCode: 550},
		{name: "generic policy block", rcptReply: "550 rejected due to sender reputation policy", wantStatus: "blocked", wantCode: 550},
		{name: "legacy missing mailbox", rcptReply: "550 no such user", wantStatus: "reject", wantCode: 550},
		{name: "ambiguous permanent failure", rcptReply: "550 mailbox unavailable", wantStatus: "blocked", wantCode: 550},
		{name: "temporary", rcptReply: "451 4.7.1 try again later", wantStatus: "tempfail", wantCode: 451},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			serverDone := make(chan error, 1)
			dial := func(context.Context, string, time.Duration) (net.Conn, error) {
				client, server := net.Pipe()
				go serveTestSMTP(server, tc.rcptReply, serverDone)
				return client, nil
			}
			result := probeSMTPHostWithDial(context.Background(), "mx.example.com", "person@example.com", time.Second, dial)
			if result.Status != tc.wantStatus || result.Code != tc.wantCode {
				t.Fatalf("probe=%#v, want status=%s code=%d", result, tc.wantStatus, tc.wantCode)
			}
			if tc.name == "AWS policy block" && result.EnhancedCode != "5.7.1" {
				t.Fatalf("enhanced status was not retained: %#v", result)
			}
			select {
			case err := <-serverDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("test SMTP server did not exit")
			}
		})
	}
}

func TestParseEnhancedSMTPCode(t *testing.T) {
	tests := map[string]string{
		"5.1.1 mailbox unavailable":    "5.1.1",
		"5.7.1 IP address blacklisted": "5.7.1",
		"mailbox unavailable":          "",
		"5.x.1 malformed":              "",
	}
	for input, want := range tests {
		if got := parseEnhancedSMTPCode(input); got != want {
			t.Errorf("parseEnhancedSMTPCode(%q)=%q, want %q", input, got, want)
		}
	}
}

func serveTestSMTP(conn net.Conn, rcptReply string, done chan<- error) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(line string) error {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := write("220 mx.example.com ESMTP ready"); err != nil {
		done <- err
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// net/smtp closes the connection after a non-2xx RCPT response.
			done <- nil
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO "):
			if err := write("250-mx.example.com"); err != nil {
				done <- err
				return
			}
			if err := write("250 HELP"); err != nil {
				done <- err
				return
			}
		case strings.HasPrefix(command, "HELO "), strings.HasPrefix(command, "MAIL FROM:"):
			if err := write("250 2.1.0 ok"); err != nil {
				done <- err
				return
			}
		case strings.HasPrefix(command, "RCPT TO:"):
			if err := write(rcptReply); err != nil {
				done <- err
				return
			}
		case command == "QUIT":
			if err := write("221 2.0.0 bye"); err != nil {
				done <- err
				return
			}
			done <- nil
			return
		default:
			done <- errors.New("unexpected SMTP command: " + command)
			return
		}
	}
}
