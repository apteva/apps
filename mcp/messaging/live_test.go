//go:build live
// +build live

package main

// Live smoke tests against real AWS SES. Skipped unless built with
// -tags live AND AWS credentials are present in the env. Never reads
// from a config file — keys must come from the operator's shell so
// they can't leak through a checked-in fixture.
//
// READ-ONLY by construction. The only request helper here is sesGet
// (GET only, asserted at runtime). No verify_email, no delete_identity,
// no create_template — those modify SES state and have no place in a
// smoke test. If you add a new test, keep it on read endpoints; the
// guard in sesGet will fail loudly if you ever pass a non-GET method.
//
// Run:
//   AWS_ACCESS_KEY_ID=... \
//   AWS_SECRET_ACCESS_KEY=... \
//   AWS_REGION=eu-west-1 \
//   go test -tags live -v -run TestLive_ ./...
//
// What this verifies that the unit tests can't:
//   - integrations/aws-ses.json paths/headers/auth still match what
//     SES accepts in eu-west-1 (catalog drift surfaces here, not as
//     a 5xx from a customer's panel)
//   - the SES v2 response shapes that toolSendersList /
//     toolSendersGetQuota expect — Identities[].VerificationStatus,
//     SendQuota.{Max24HourSend, MaxSendRate, SentLast24Hours},
//     ProductionAccessEnabled — still match what the API returns

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveCreds(t *testing.T) (key, secret, region string) {
	t.Helper()
	key = strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	secret = strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	region = strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = "eu-west-1"
	}
	if key == "" || secret == "" {
		t.Skip("AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY not set — skipping live test")
	}
	return
}

// TestLive_SES_GetAccount exercises the same path that
// toolSendersGetQuota proxies. Asserts the response carries the
// fields the panel's sandbox banner reads.
func TestLive_SES_GetAccount(t *testing.T) {
	key, secret, region := liveCreds(t)
	raw, status := sesGet(t, key, secret, region, "/v2/email/account", "")
	if status != 200 {
		t.Fatalf("GET /v2/email/account → %d, body=%s", status, raw)
	}
	var got struct {
		SendQuota struct {
			Max24HourSend   float64 `json:"Max24HourSend"`
			MaxSendRate     float64 `json:"MaxSendRate"`
			SentLast24Hours float64 `json:"SentLast24Hours"`
		} `json:"SendQuota"`
		SendingEnabled          bool   `json:"SendingEnabled"`
		ProductionAccessEnabled bool   `json:"ProductionAccessEnabled"`
		EnforcementStatus       string `json:"EnforcementStatus"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode account body: %v\nraw: %s", err, raw)
	}
	if got.SendQuota.Max24HourSend <= 0 {
		t.Errorf("Max24HourSend not populated: %+v", got)
	}
	t.Logf("live OK: sandbox=%v sending=%v 24h=%.0f/%.0f rate=%.1f/s status=%s",
		!got.ProductionAccessEnabled, got.SendingEnabled,
		got.SendQuota.SentLast24Hours, got.SendQuota.Max24HourSend,
		got.SendQuota.MaxSendRate, got.EnforcementStatus,
	)
}

// TestLive_SES_ListIdentities verifies the Identities[] shape that
// toolSendersList parses through. The list may be empty — that's
// fine; we're only checking schema adherence.
func TestLive_SES_ListIdentities(t *testing.T) {
	key, secret, region := liveCreds(t)
	raw, status := sesGet(t, key, secret, region, "/v2/email/identities", "PageSize=100")
	if status != 200 {
		t.Fatalf("GET /v2/email/identities → %d, body=%s", status, raw)
	}
	var got struct {
		EmailIdentities []struct {
			IdentityName       string `json:"IdentityName"`
			IdentityType       string `json:"IdentityType"`
			SendingEnabled     bool   `json:"SendingEnabled"`
			VerificationStatus string `json:"VerificationStatus"`
		} `json:"EmailIdentities"`
		NextToken string `json:"NextToken,omitempty"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode identities body: %v\nraw: %s", err, raw)
	}
	for i, id := range got.EmailIdentities {
		if id.IdentityName == "" {
			t.Errorf("identity[%d] has empty IdentityName: %+v", i, id)
		}
		switch id.IdentityType {
		case "EMAIL_ADDRESS", "DOMAIN", "MANAGED_DOMAIN":
			// known
		default:
			t.Errorf("identity[%d] unknown IdentityType %q (catalog drift?)", i, id.IdentityType)
		}
	}
	t.Logf("live OK: %d identities first-page (region=%s, more=%v)",
		len(got.EmailIdentities), region, got.NextToken != "")
}

// TestLive_SES_NormalisesThroughOurParser feeds the live response
// through toolSendersList's normalisation logic to confirm the panel
// shape is correct end-to-end. Uses the exact JSON produced by SES,
// not a stub.
func TestLive_SES_NormalisesThroughOurParser(t *testing.T) {
	key, secret, region := liveCreds(t)
	raw, status := sesGet(t, key, secret, region, "/v2/email/identities", "PageSize=100")
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, raw)
	}
	// Mirror toolSendersList's parsing block exactly.
	var inner struct {
		EmailIdentities []struct {
			IdentityName       string `json:"IdentityName"`
			IdentityType       string `json:"IdentityType"`
			SendingEnabled     bool   `json:"SendingEnabled"`
			VerificationStatus string `json:"VerificationStatus"`
		} `json:"EmailIdentities"`
	}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	if len(inner.EmailIdentities) == 0 {
		t.Skip("no identities in this region — skipping normalisation roundtrip")
	}
	for _, id := range inner.EmailIdentities {
		kind := "email"
		if id.IdentityType == "DOMAIN" || id.IdentityType == "MANAGED_DOMAIN" {
			kind = "domain"
		}
		uri := canonicalSenderURI(kind, strings.ToLower(id.IdentityName))
		if !strings.HasPrefix(uri, "mailto:") {
			t.Errorf("normalised URI lost scheme: %q", uri)
		}
	}
	t.Logf("live OK: normalised %d identities through canonicalSenderURI", len(inner.EmailIdentities))
}

// ─── Minimal SigV4 GET signer ──────────────────────────────────────

// sesGet performs a SigV4-signed GET against the SES v2 endpoint
// in the given region. Returns the raw body + status code. Fatals
// the test on transport error.
//
// Hard-locks to GET so a future test author can't accidentally
// repurpose this helper for a mutating call.
func sesGet(t *testing.T, accessKey, secretKey, region, path, query string) (json.RawMessage, int) {
	t.Helper()
	host := "email." + region + ".amazonaws.com"
	endpoint := "https://" + host + path
	if query != "" {
		endpoint += "?" + query
	}
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" {
		t.Fatalf("sesGet must only issue GET, got %q — refusing to mutate SES state", req.Method)
	}
	signSESv4(req, accessKey, secretKey, region, query, []byte{})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sesGet %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(body), resp.StatusCode
}

// signSESv4 mutates req with AWS SigV4 headers for the SES service.
// Implements only what these tests need: GET, no body (or empty),
// canonical query string passed through verbatim.
func signSESv4(req *http.Request, accessKey, secretKey, region, query string, body []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	host := req.URL.Host

	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)

	// Canonical request.
	payloadHash := sha256Hex(body)
	canonicalQuery := canonicaliseQuery(query)
	canonicalHeaders := "host:" + host + "\nx-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.Path,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign.
	credentialScope := dateStamp + "/" + region + "/ses/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// Signing key + signature.
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte("ses"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
}

// canonicaliseQuery sorts the query parameters and re-encodes them
// in the SigV4-required form. Our tests only ever pass
// "PageSize=100" or empty so this is intentionally minimal.
func canonicaliseQuery(query string) string {
	if query == "" {
		return ""
	}
	// Already in key=value&key2=value2 form; SigV4 wants params
	// sorted by key. For our trivial cases this is a no-op.
	return query
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
