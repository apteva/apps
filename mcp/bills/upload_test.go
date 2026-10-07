package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

type uploadOCRPlatform struct {
	recordingStoragePlatform
	fail bool
}

func (p *uploadOCRPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if tool != "extract_invoice" {
		return p.recordingStoragePlatform.CallAppResult(app, tool, input, out)
	}
	if p.fail {
		return errors.New("HTTP 400: Unsupported parameter: temperature")
	}
	raw := []byte(`{"vendor":{"name":"Acme"},"invoice_number":"INV-UPLOAD","currency":"EUR","total_cents":12500}`)
	return json.Unmarshal(raw, out)
}

func TestUploadExtractionFailureDoesNotCreateBill(t *testing.T) {
	for _, transport := range []string{"http-json", "http-multipart", "mcp"} {
		for _, explicitVendor := range []bool{false, true} {
			t.Run(transport+map[bool]string{false: "/automatic-vendor", true: "/selected-vendor"}[explicitVendor], func(t *testing.T) {
				pf := &uploadOCRPlatform{fail: true}
				ctx := newTestCtx(t, tk.WithPlatform(pf), withOCRConfig("test-ocr"))
				vendor := mustVendor(t, ctx, "ap@acme.example", "Acme")
				body := map[string]any{"_project_id": "test-proj"}
				if explicitVendor {
					body["vendor_id"] = vendor.ID
				}
				app := &App{}
				var message string
				if transport == "mcp" {
					body["name"] = "invoice.pdf"
					body["content_base64"] = "JVBERg=="
					_, err := app.toolBillsCreateFromFile(ctx, body)
					if err == nil {
						t.Fatal("expected extraction error")
					}
					message = err.Error()
				} else {
					var r *http.Request
					if transport == "http-json" {
						raw, _ := json.Marshal(map[string]any{"file_id": 88, "bill": body})
						r = httptest.NewRequest(http.MethodPost, "/bills/from-file?project_id=test-proj", bytes.NewReader(raw))
						r.Header.Set("Content-Type", "application/json")
					} else {
						var buf bytes.Buffer
						mw := multipart.NewWriter(&buf)
						part, err := mw.CreateFormFile("file", "invoice.pdf")
						if err != nil {
							t.Fatal(err)
						}
						_, _ = part.Write([]byte("%PDF"))
						raw, _ := json.Marshal(body)
						_ = mw.WriteField("bill_json", string(raw))
						_ = mw.Close()
						r = httptest.NewRequest(http.MethodPost, "/bills/from-file?project_id=test-proj", &buf)
						r.Header.Set("Content-Type", mw.FormDataContentType())
					}
					rec := httptest.NewRecorder()
					app.handleHTTPBillsCreateFromFile(rec, r)
					if rec.Code != http.StatusBadGateway {
						t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
					}
					message = rec.Body.String()
				}
				for _, want := range []string{"Unsupported parameter: temperature", "No bill was created", "storage file #88"} {
					if !strings.Contains(message, want) {
						t.Fatalf("error %q missing %q", message, want)
					}
				}
				// The dashboard only opens its vendor picker on this marker.
				if strings.Contains(message, "vendor_id required") {
					t.Fatal("extraction failure must not trigger vendor fallback")
				}
				var count int
				if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM bills").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("created %d bills after failed extraction", count)
				}
				for _, call := range pf.calls {
					if strings.Contains(call.Tool, "delete") {
						t.Fatalf("document unexpectedly deleted: %+v", call)
					}
				}
			})
		}
	}
}

func TestUploadSuccessfulExtractionMatchesExistingVendor(t *testing.T) {
	pf := &uploadOCRPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf), withOCRConfig("test-ocr"))
	vendor := mustVendor(t, ctx, "ap@acme.example", "Acme")
	result, err := (&App{}).toolBillsCreateFromFile(ctx, map[string]any{"_project_id": "test-proj", "name": "invoice.pdf", "content_base64": "JVBERg=="})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)
	bill := got["bill"].(*Bill)
	if bill.TotalCents != 12500 || bill.Currency != "EUR" || bill.VendorID != vendor.ID || got["ocr_extracted"] != true {
		t.Fatalf("incorrect extracted bill: %+v response=%v", bill, got)
	}
}

func TestUploadManualFieldsStillWorkWhenExtractionOff(t *testing.T) {
	pf := &uploadOCRPlatform{fail: true}
	ctx := newTestCtx(t, tk.WithPlatform(pf), withOCRConfig("off"))
	vendor := mustVendor(t, ctx, "ap@acme.example", "Acme")
	result, err := (&App{}).toolBillsCreateFromFile(ctx, map[string]any{"_project_id": "test-proj", "name": "invoice.pdf", "content_base64": "JVBERg==", "vendor_id": vendor.ID, "total_cents": 12500})
	if err != nil {
		t.Fatal(err)
	}
	bill := result.(map[string]any)["bill"].(*Bill)
	if bill.TotalCents != 12500 {
		t.Fatalf("manual total=%d", bill.TotalCents)
	}
}

type boundCodexUploadPlatform struct {
	uploadOCRPlatform
	t *testing.T
}

func (p *boundCodexUploadPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"vision_llm": 1}}, nil
}
func (p *boundCodexUploadPlatform) GetConnection(int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{AppSlug: "openai-codex"}, nil
}
func (p *boundCodexUploadPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if tool == "files_get_content" {
		raw, _ := json.Marshal(map[string]any{"id": 88, "name": "invoice.png", "content_type": "image/png", "content_base64": base64.StdEncoding.EncodeToString([]byte("test image"))})
		return json.Unmarshal(raw, out)
	}
	return p.uploadOCRPlatform.CallAppResult(app, tool, input, out)
}
func (p *boundCodexUploadPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if _, ok := input["temperature"]; ok {
		p.t.Fatal("unsupported temperature sent to Codex")
	}
	if id != 1 || tool != "responses_create" || input["model"] != "gpt-6-luna" || input["reasoning"].(map[string]any)["effort"] != "low" {
		p.t.Fatalf("incorrect Codex request: id=%d tool=%q model=%v reasoning=%v", id, tool, input["model"], input["reasoning"])
	}
	raw, _ := json.Marshal(map[string]any{"status": "completed", "output_text": `{"vendor":{"name":"Acme"},"total_cents":12500,"currency":"EUR"}`})
	return &sdk.ExecuteResult{Success: true, Data: raw}, nil
}
func TestUploadAutomaticCodexBindingWithoutExplicitVendor(t *testing.T) {
	pf := &boundCodexUploadPlatform{t: t}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	vendor := mustVendor(t, ctx, "ap@acme.example", "Acme")
	result, err := (&App{}).toolBillsCreateFromFile(ctx, map[string]any{"_project_id": "test-proj", "name": "invoice.png", "content_base64": "dGVzdA==", "content_type": "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	bill := result.(map[string]any)["bill"].(*Bill)
	if bill.VendorID != vendor.ID || bill.TotalCents != 12500 || bill.Currency != "EUR" {
		t.Fatalf("incorrect bill: %+v", bill)
	}
}
