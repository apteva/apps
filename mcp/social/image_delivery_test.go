package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixturePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 180, G: uint8(x % 255), B: uint8(y % 255), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func storageImageFixture(p *recordingPlatform, body []byte, id int64) {
	p.callAppResponses["storage:files_get_content"], _ = json.Marshal(map[string]any{"content_base64": base64.StdEncoding.EncodeToString(body)})
	p.callAppResponses["storage:files_upload"], _ = json.Marshal(map[string]any{"id": id})
	p.callAppResponses[fmt.Sprintf("storage:files_get:%d", id)], _ = json.Marshal(map[string]any{"id": id})
}
func countStorageCalls(p *recordingPlatform, tool string) int {
	n := 0
	for _, c := range p.callAppCalls {
		if c.Tool == tool {
			n++
		}
	}
	return n
}

func TestImageDeliveryReportedPortrait(t *testing.T) {
	original := fixturePNG(t, 1080, 1924)
	checksum := imageSHA(original)
	out, d, err := normalizeDeliveryImage(original, tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Width != 1078 || d.Height != 1920 || d.SourceWidth != 1080 || d.SourceHeight != 1924 || d.SourceMIME != "image/png" {
		t.Fatalf("dimensions/format: %+v", d)
	}
	if imageSHA(original) != checksum {
		t.Fatal("original was mutated")
	}
	if _, _, err = validateDeliveryImage(out, "image/jpeg", tikTokPhotoPolicy); err != nil {
		t.Fatal(err)
	}
}
func TestImageDeliveryProportionalBounds(t *testing.T) {
	for _, c := range []struct{ w, h, ow, oh int }{{1924, 1080, 1920, 1078}, {1080, 1920, 1080, 1920}, {2000, 2000, 1080, 1080}, {32, 16, 32, 16}, {4000, 3000, 1440, 1080}} {
		w, h := deliveryDimensions(c.w, c.h, tikTokPhotoPolicy)
		if w != c.ow || h != c.oh {
			t.Fatalf("%dx%d -> %dx%d", c.w, c.h, w, h)
		}
	}
}
func TestImageDeliveryRejectsCorruptionAndLimits(t *testing.T) {
	valid := fixturePNG(t, 32, 16)
	for _, c := range []struct {
		name   string
		body   []byte
		policy imageDeliveryPolicy
	}{
		{"empty", nil, tikTokPhotoPolicy}, {"html", []byte("<html>zrok interstitial</html>"), tikTokPhotoPolicy},
		{"truncated", valid[:len(valid)/2], tikTokPhotoPolicy},
		{"pixels", valid, func() imageDeliveryPolicy { p := tikTokPhotoPolicy; p.MaxPixels = 10; return p }()},
		{"source bytes", valid, func() imageDeliveryPolicy { p := tikTokPhotoPolicy; p.MaxSourceBytes = 10; return p }()},
		{"output bytes", valid, func() imageDeliveryPolicy { p := tikTokPhotoPolicy; p.MaxBytes = 10; return p }()},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := normalizeDeliveryImage(c.body, c.policy); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}
func TestImageDeliveryWebPAndStoredMIME(t *testing.T) {
	// A one-pixel lossy WebP; actual bytes determine the decoder, not Storage MIME.
	body, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	out, d, err := normalizeDeliveryImage(body, tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if d.SourceMIME != "image/webp" {
		t.Fatalf("%+v", d)
	}
	if _, _, err = validateDeliveryImage(out, "image/jpeg", tikTokPhotoPolicy); err != nil {
		t.Fatal(err)
	}
	pf := newRecordingPlatform()
	storageImageFixture(pf, fixturePNG(t, 16, 16), 109)
	ctx := newSocialCtx(t, pf)
	_, d, err = (&App{}).prepareImageDelivery(ctx, mediaItem{ID: 9, Mime: "image/jpeg"}, "media-proj", tikTokPhotoPolicy)
	if err != nil || d.SourceMIME != "image/png" {
		t.Fatalf("trusted stored MIME: %+v %v", d, err)
	}
}
func exifJPEG(body []byte, orientation uint16) []byte {
	tiff := make([]byte, 26)
	copy(tiff, "II")
	binary.LittleEndian.PutUint16(tiff[2:], 42)
	binary.LittleEndian.PutUint32(tiff[4:], 8)
	binary.LittleEndian.PutUint16(tiff[8:], 1)
	binary.LittleEndian.PutUint16(tiff[10:], 0x112)
	binary.LittleEndian.PutUint16(tiff[12:], 3)
	binary.LittleEndian.PutUint32(tiff[14:], 1)
	binary.LittleEndian.PutUint16(tiff[18:], orientation)
	segment := append([]byte("Exif\x00\x00"), tiff...)
	header := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(header[2:], uint16(len(segment)+2))
	result := append([]byte{}, body[:2]...)
	result = append(result, header...)
	result = append(result, segment...)
	return append(result, body[2:]...)
}
func TestImageDeliveryEXIFAndAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(y*3 + x + 1), A: 255})
		}
	}
	expected := [][]uint8{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for orientation := 1; orientation <= 8; orientation++ {
		out := orientImage(src, orientation)
		i := 0
		for y := 0; y < out.Bounds().Dy(); y++ {
			for x := 0; x < out.Bounds().Dx(); x++ {
				c := color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA)
				if c.R != expected[orientation-1][i] {
					t.Fatalf("orientation %d at %d: %d", orientation, i, c.R)
				}
				i++
			}
		}
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 16, 8))
	var b bytes.Buffer
	_ = png.Encode(&b, transparent)
	out, _, err := normalizeDeliveryImage(b.Bytes(), tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	r, g, blue, _ := img.At(0, 0).RGBA()
	if r < 60000 || g < 60000 || blue < 60000 {
		t.Fatal("alpha not composited on white")
	}
	rotated := exifJPEG(out, 6)
	if exifOrientation(rotated) != 6 {
		t.Fatal("EXIF not read")
	}
	normalized, d, err := normalizeDeliveryImage(rotated, tikTokPhotoPolicy)
	if err != nil || d.Width != 8 || d.Height != 16 || d.Orientation != 6 || exifOrientation(normalized) != 1 {
		t.Fatalf("EXIF normalization: %+v %v", d, err)
	}
}
func TestImageDeliveryCacheScopeAndInvalidation(t *testing.T) {
	pf := newRecordingPlatform()
	body := fixturePNG(t, 16, 16)
	storageImageFixture(pf, body, 109)
	ctx := newSocialCtx(t, pf)
	app := &App{}
	first, d, err := app.prepareImageDelivery(ctx, mediaItem{ID: 9, Mime: "image/png"}, "media-proj", tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	cached, d2, err := app.prepareImageDelivery(ctx, mediaItem{ID: 10, Mime: "image/jpeg"}, "media-proj", tikTokPhotoPolicy)
	if err != nil || first.ID != cached.ID || !d2.CacheHit || d2.SourceID != 10 || countStorageCalls(pf, "files_upload") != 1 {
		t.Fatalf("cache: %+v %v", d2, err)
	}
	if d.SourceSHA != imageSHA(body) || d.DeliveryID != 109 {
		t.Fatal("lineage missing")
	}
	_, _, err = app.prepareImageDelivery(ctx, mediaItem{ID: 9}, "other-project", tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	changed := tikTokPhotoPolicy
	changed.JPEGQuality = 82
	_, _, err = app.prepareImageDelivery(ctx, mediaItem{ID: 9}, "media-proj", changed)
	if err != nil {
		t.Fatal(err)
	}
	if countStorageCalls(pf, "files_upload") != 3 {
		t.Fatal("project/policy change used stale cache")
	}
	delete(pf.callAppResponses, "storage:files_get:109")
	_, _, err = app.prepareImageDelivery(ctx, mediaItem{ID: 9}, "media-proj", tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if countStorageCalls(pf, "files_upload") != 4 {
		t.Fatal("deleted derivative reused")
	}
	// A revoked/deleted source must not be bypassed by a cached derivative.
	delete(pf.callAppResponses, "storage:files_get_content")
	if _, _, err = app.prepareImageDelivery(ctx, mediaItem{ID: 9}, "media-proj", tikTokPhotoPolicy); err == nil {
		t.Fatal("cache bypassed source access")
	}
}
func TestImageDeliveryHTTPValidation(t *testing.T) {
	source := fixturePNG(t, 16, 16)
	good, _, err := normalizeDeliveryImage(source, tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	huge, _, err := normalizeDeliveryImage(fixturePNG(t, 1100, 1100), func() imageDeliveryPolicy {
		p := tikTokPhotoPolicy
		p.MaxShortEdge = 2000
		p.MaxLongEdge = 2000
		return p
	}())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, mime string
		body       []byte
		status     int
		hash       string
	}{
		{"good", "image/jpeg", good, 200, imageSHA(good)}, {"interstitial", "text/html", []byte("<html>warning</html>"), 200, ""},
		{"spoofed MIME", "image/jpeg", source, 200, ""}, {"corrupt JPEG", "image/jpeg", good[:len(good)/2], 200, ""},
		{"too large dimensions", "image/jpeg", huge, 200, ""}, {"wrong checksum", "image/jpeg", good, 200, "wrong"},
		{"redirect", "image/jpeg", good, 302, ""}, {"range response", "image/jpeg", good, 206, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Range") != "" {
					t.Error("must fetch full image")
				}
				if !strings.Contains(r.UserAgent(), "Mozilla") {
					t.Error("browser preflight required")
				}
				w.Header().Set("Content-Type", c.mime)
				w.Header().Set("Location", "/destination")
				w.WriteHeader(c.status)
				_, _ = w.Write(c.body)
			}))
			defer server.Close()
			d, err := preflightImageDeliveryURL(server.URL+"/file.jpg?sig=secret", tikTokPhotoPolicy, c.hash)
			if (err == nil) != (c.name == "good") {
				t.Fatalf("result %+v %v", d, err)
			}
			if strings.Contains(d.DeliveryPath, "secret") {
				t.Fatal("signature persisted")
			}
		})
	}
}
