package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestPendingAllowanceMatchesConfiguredFileLimit(t *testing.T) {
	for _, explicit := range []string{"", "auto", "1024"} {
		t.Run("pending="+explicit, func(t *testing.T) {
			cfg := map[string]string{"max_upload_size_mb": "4096"}
			if explicit != "" {
				cfg["max_pending_upload_mb"] = explicit
			}
			ctx := auditCtx(t, tk.WithConfig(cfg))
			err := reserveUpload(ctx, "large-test", "test-proj", 3<<30, 0)
			if explicit == "1024" {
				if err == nil || !strings.Contains(err.Error(), "max_pending_upload_mb") {
					t.Fatalf("explicit limit not explained: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("3 GiB file refused below 4 GiB file limit: %v", err)
				}
				if err = reserveUpload(ctx, "second-test", "test-proj", 2<<30, 0); err == nil {
					t.Fatal("combined quota bypassed")
				}
			}
		})
	}
}

func TestUploadLimitsDoNotReserveSpace(t *testing.T) {
	ctx := auditCtx(t, tk.WithConfig(map[string]string{"max_upload_size_mb": "4096"}))
	w := httptest.NewRecorder()
	(&App{}).handleUploadsCollection(w, httptest.NewRequest("GET", "/uploads?project_id=test-proj", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"max_pending_bytes":4294967296`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM upload_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
