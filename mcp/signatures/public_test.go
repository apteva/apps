package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/apteva/app-sdk/testkit"
)

// Browsers submit the signing page's plain <form> as
// application/x-www-form-urlencoded. The handler once demanded
// multipart and rejected every real submission as "too large".
func TestCompleteSigningAcceptsBrowserFormPost(t *testing.T) {
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-a"))
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })

	db := ctx.AppDB()
	env, recipients, fields := createPreparedEnvelope(t, db)
	if _, _, err := activateEnvelope(db, "project-a", env.ID, "manual", ""); err != nil {
		t.Fatal(err)
	}
	_, _, token, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"consent":    {"on"},
		"legal_name": {"Ada Lovelace"},
		fmt.Sprintf("field_%d", fields[0].ID): {"Ada Lovelace"},
	}
	req := httptest.NewRequest("POST", "/sign/"+token+"/complete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	(&App{}).handleSigning(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Thank you") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	session, err := sessionByToken(db, token)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatal("token should be consumed after signing")
	}
}

func TestCompleteSigningAcceptsDrawnSignature(t *testing.T) {
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-a"))
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })

	db := ctx.AppDB()
	env, recipients, fields := createPreparedEnvelope(t, db)
	if _, _, err := activateEnvelope(db, "project-a", env.ID, "manual", ""); err != nil {
		t.Fatal(err)
	}
	_, _, token, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"consent":    {"on"},
		"legal_name": {"Ada Lovelace"},
		fmt.Sprintf("field_%d", fields[0].ID): {drawnSignatureFixture(t)},
	}
	req := httptest.NewRequest("POST", "/sign/"+token+"/complete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	(&App{}).handleSigning(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	values, err := loadCompletionValues(db, "project-a", env.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range values {
		if v.Field.ID == fields[0].ID {
			found = true
			if _, ok := drawnSignatureImage(v.ValueText); !ok {
				t.Fatalf("stored value is not a drawn signature: %.60q", v.ValueText)
			}
		}
	}
	if !found {
		t.Fatal("drawn signature value not stored")
	}
}

func TestSigningPageEmbedsOverlayPayloadAndAssets(t *testing.T) {
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-a"))
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })

	db := ctx.AppDB()
	env, recipients, _ := createPreparedEnvelope(t, db)
	if _, _, err := activateEnvelope(db, "project-a", env.ID, "manual", ""); err != nil {
		t.Fatal(err)
	}
	_, _, token, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sign/"+token, nil)
	rec := httptest.NewRecorder()
	(&App{}).handleSigning(rec, req)
	if rec.Code != 200 {
		t.Fatalf("page status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="signing-data"`, `"doc_url"`, `./assets/sign.js`, `data-field-id`} {
		if !strings.Contains(body, want) {
			t.Fatalf("signing page missing %q", want)
		}
	}

	for _, asset := range []string{"sign.js", "pdf.min.mjs", "pdf.worker.min.mjs"} {
		areq := httptest.NewRequest("GET", "/sign/assets/"+asset, nil)
		arec := httptest.NewRecorder()
		(&App{}).handleSigning(arec, areq)
		if arec.Code != 200 || arec.Body.Len() == 0 {
			t.Fatalf("asset %s: status %d, %d bytes", asset, arec.Code, arec.Body.Len())
		}
		if ct := arec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
			t.Fatalf("asset %s content type %q", asset, ct)
		}
	}
}
