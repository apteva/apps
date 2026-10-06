package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type invoiceStoragePlatform struct {
	tk.BasePlatformClient
	call func(string, string, map[string]any) (any, error)
}

func (p *invoiceStoragePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	value, err := p.call(app, tool, input)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// Use a global Billing install, matching production, to catch lost project
// context on either cross-app call.
func storageInvoice(t *testing.T, platform sdk.PlatformClient) (*sdk.AppCtx, int64) {
	t.Helper()
	ctx := newGlobalTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	out, err := app.toolCustomersUpsertByEmail(ctx, map[string]any{"_project_id": "invoice-project", "email": "pdf@example.test", "defaults": map[string]any{"name": "PDF Customer"}})
	if err != nil {
		t.Fatal(err)
	}
	customer := out.(map[string]any)["customer"].(*Customer)
	out, err = app.toolInvoicesCreate(ctx, map[string]any{"_project_id": "invoice-project", "customer_id": customer.ID, "line_items": []any{line("Service", 1, 1000, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	inv := out.(map[string]any)["invoice"].(*Invoice)
	if _, err = app.toolInvoicesFinalize(ctx, map[string]any{"_project_id": "invoice-project", "invoice_id": inv.ID}); err != nil {
		t.Fatal(err)
	}
	return ctx, inv.ID
}

func assertPDFFallback(t *testing.T, result map[string]any) {
	t.Helper()
	if result["saved"] != false || result["shareable"] != false || result["url"] != nil {
		t.Fatalf("fallback falsely claims a saved/shared PDF: %#v", result)
	}
	data, err := base64.StdEncoding.DecodeString(result["pdf_base64"].(string))
	if err != nil || !strings.HasPrefix(string(data), "%PDF-") {
		t.Fatalf("fallback has no valid PDF: %v", err)
	}
	if result["size_bytes"] != len(data) || !strings.HasSuffix(result["filename"].(string), ".pdf") {
		t.Fatal("fallback metadata does not match PDF")
	}
}

func TestInvoicesRenderPDF_StorageSignedURL(t *testing.T) {
	expiry := time.Now().Add(time.Hour).Unix()
	// Storage owns the URL, including exact install routing and signature.
	signedURL := "https://files.example.test/api/apps/storage/files/42/content/invoice.pdf?project_id=invoice-project&install_id=16&sig=storage-signature&exp=123"
	calls := []string{}
	platform := &invoiceStoragePlatform{call: func(app, tool string, in map[string]any) (any, error) {
		if app != "storage" || in["_project_id"] != "invoice-project" {
			t.Fatalf("cross-app scope lost: %s %#v", app, in)
		}
		calls = append(calls, tool)
		switch tool {
		case "files_upload":
			if in["visibility"] != "private" || in["folder"] != "/.billing/invoices/" || in["content_type"] != "application/pdf" || in["source"] != "billing" {
				t.Fatalf("unsafe upload: %#v", in)
			}
			data, err := base64.StdEncoding.DecodeString(in["content_base64"].(string))
			if err != nil || !strings.HasPrefix(string(data), "%PDF-") {
				t.Fatal("uploaded bytes are not a PDF")
			}
			return map[string]any{"id": 42, "url": "https://files.example.test/authenticated"}, nil
		case "files_get_url":
			if in["id"] != int64(42) {
				t.Fatalf("signed wrong file: %#v", in)
			}
			return map[string]any{"url": signedURL, "expires_at": expiry}, nil
		default:
			t.Fatalf("unexpected tool %s", tool)
			return nil, nil
		}
	}}
	ctx, id := storageInvoice(t, platform)
	out, err := (&App{}).toolInvoicesRenderPDF(ctx, map[string]any{"_project_id": "invoice-project", "invoice_id": id, "save_to_storage": true})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["url"] != signedURL || result["expires_at"] != expiry || result["saved"] != true || result["shareable"] != true || result["file_id"] != int64(42) {
		t.Fatalf("wrong signed result: %#v", result)
	}
	if strings.Join(calls, ",") != "files_upload,files_get_url" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestInvoicesRenderPDF_StorageUploadFallback(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		value        any
		err          error
	}{
		{name: "unbound", reason: "Storage is not linked to Billing", err: errors.New("http 403: app not bound: storage")},
		{name: "legacy unbound", reason: "Storage is not linked to Billing", err: errors.New("target app is not bound: storage")},
		{name: "unreachable", reason: "target unreachable", err: errors.New("target unreachable")},
		{name: "missing id", reason: "no file id", value: map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			platform := &invoiceStoragePlatform{call: func(app, tool string, in map[string]any) (any, error) {
				calls++
				if tool != "files_upload" {
					t.Fatalf("retried after upload failure: %s", tool)
				}
				return tc.value, tc.err
			}}
			ctx, id := storageInvoice(t, platform)
			out, err := (&App{}).toolInvoicesRenderPDF(ctx, map[string]any{"_project_id": "invoice-project", "invoice_id": id, "save_to_storage": true, "folder": "/customer-invoices/"})
			if err != nil {
				t.Fatal(err)
			}
			result := out.(map[string]any)
			assertPDFFallback(t, result)
			if calls != 1 || !strings.Contains(result["storage_error"].(string), tc.reason) {
				t.Fatalf("wrong fallback: %#v calls=%d", result, calls)
			}
			if tc.name == "unreachable" && strings.Contains(result["storage_error"].(string), "not linked") {
				t.Fatal("misdiagnosed a reachability failure")
			}
		})
	}
}

func TestInvoicesRenderPDF_StorageSigningFailurePreservesSavedFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		err   error
	}{
		{name: "signing failed", err: errors.New("signer unavailable")},
		{name: "empty url", value: map[string]any{"expires_at": time.Now().Add(time.Hour).Unix()}},
		{name: "missing expiry", value: map[string]any{"url": "https://files.example.test/signed"}},
		{name: "expired url", value: map[string]any{"url": "https://files.example.test/signed", "expires_at": time.Now().Add(-time.Hour).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			platform := &invoiceStoragePlatform{call: func(app, tool string, in map[string]any) (any, error) {
				calls++
				if tool == "files_upload" {
					return map[string]any{"id": 42, "url": "https://files.example.test/authenticated?install_id=16"}, nil
				}
				return tc.value, tc.err
			}}
			ctx, id := storageInvoice(t, platform)
			out, err := (&App{}).toolInvoicesRenderPDF(ctx, map[string]any{"_project_id": "invoice-project", "invoice_id": id, "save_to_storage": true})
			if err != nil {
				t.Fatal(err)
			}
			result := out.(map[string]any)
			if result["saved"] != true || result["shareable"] != false || result["file_id"] != int64(42) || result["url"] != "https://files.example.test/authenticated?install_id=16" || result["expires_at"] != nil {
				t.Fatalf("saved file lost or falsely shared: %#v", result)
			}
			if !strings.Contains(result["storage_error"].(string), "retry storage.files_get_url") || calls != 2 {
				t.Fatalf("wrong recovery: %#v calls=%d", result, calls)
			}
		})
	}
}

func TestInvoicesRenderPDF_NoPlatformReturnsBytes(t *testing.T) {
	ctx, id := storageInvoice(t, nil)
	out, err := (&App{}).toolInvoicesRenderPDF(ctx, map[string]any{"_project_id": "invoice-project", "invoice_id": id, "save_to_storage": true})
	if err != nil {
		t.Fatal(err)
	}
	assertPDFFallback(t, out.(map[string]any))
}
