package main

import (
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func TestMediaAskImagesUseActualSourceAndReportEvidence(t *testing.T) {
	stub := boundOpenAI()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: canonOK("The subject is visible.")}
	ctx := newTestCtxWithPlatform(t, stub)
	p := sampleImageProbe()
	p.Width, p.Height = 1920, 1080
	upsertMedia(ctx.AppDB(), testProj, "94319", p, "sha", "", "native.png")
	upsertDerivation(ctx.AppDB(), testProj, "94319", "thumbnail", 94320, 320, 180, 0)
	for _, detail := range []string{"source", "thumbnail"} {
		out, e := (&App{}).toolAsk(ctx, map[string]any{"file_id": "94319", "question": "Is the pose visible?", "image_detail": detail})
		if e != nil {
			t.Fatal(e)
		}
		r := out.(map[string]any)
		es := r["evidence"].([]askEvidence)
		if len(es) != 1 {
			t.Fatal(es)
		}
		v := es[0]
		if detail == "source" {
			if v.StorageFileID != "94319" || v.Width != 1920 || v.Height != 1080 || r["coverage"].(askCoverage).Method != "existing_source_image" {
				t.Fatalf("source evidence=%+v", v)
			}
		} else {
			if v.StorageFileID != "94320" || v.Width != 320 || v.Height != 180 {
				t.Fatalf("thumbnail=%+v", v)
			}
		}
		ls := strings.Join(r["limitations"].([]string), " ")
		if !strings.Contains(ls, "Provider-side") {
			t.Fatalf("provider resolution caveat missing: %q", ls)
		}
		if detail == "thumbnail" && !strings.Contains(ls, "Native-resolution") {
			t.Fatalf("thumbnail limitation missing: %q", ls)
		}
	}
}
