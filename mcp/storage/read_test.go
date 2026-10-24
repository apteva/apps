package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("scenarios/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readStored(t *testing.T, app *sdk.AppCtx, f *File, args map[string]any) sdk.MCPResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["id"] = f.ID
	out, err := (&App{}).toolReadCtx(context.Background(), app, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(sdk.MCPResult)
}

func TestFilesReadTextPaginationAndLosslessByteCompatibility(t *testing.T) {
	ctx := newTestCtx(t)
	text := "Aé🙂中Z"
	f := mustUploadTyped(t, ctx, "note.txt", "text/plain", text)
	before := *f
	out := readStored(t, ctx, f, map[string]any{"offset": 1, "max_chars": 2})
	if out.Content[1].Text != "é🙂" || out.StructuredContent["next_offset"] != 3 || out.StructuredContent["total_chars"] != 5 || out.StructuredContent["truncated"] != true {
		t.Fatalf("Unicode pagination: %+v", out)
	}
	last := readStored(t, ctx, f, map[string]any{"offset": 3})
	if last.Content[1].Text != "中Z" || last.StructuredContent["truncated"] != false {
		t.Fatalf("last page: %+v", last)
	}
	after, err := dbGetByID(ctx.AppDB(), "test-proj", f.ID)
	if err != nil || !reflect.DeepEqual(before, *after) {
		t.Fatal("reading changed stored metadata", err)
	}
	raw, err := (&App{}).toolGetContentCtx(context.Background(), ctx, map[string]any{"id": f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if raw.(map[string]any)["content_base64"] != base64.StdEncoding.EncodeToString([]byte(text)) {
		t.Fatal("raw byte contract changed")
	}
	empty := mustUploadTyped(t, ctx, "empty.txt", "text/plain", " ")
	if err := backend().Put(context.Background(), fileObjectKey(empty), "text/plain", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec("UPDATE files SET size_bytes=0 WHERE id=?", empty.ID); err != nil {
		t.Fatal(err)
	}
	if got := readStored(t, ctx, empty, nil); got.Content[1].Text != "" {
		t.Fatal("empty text read failed")
	}
}

func TestFilesReadActualImageContentAndRemoteBackend(t *testing.T) {
	ctx := newTestCtx(t)
	data := readFixture(t, "picture.png")
	f := mustUploadTyped(t, ctx, "picture.png", "image/png", string(data))
	for _, remote := range []bool{false, true} {
		if remote {
			stub := newFakeS3()
			stub.objects[fileObjectKey(f)] = data
			globalBackend = stub
		}
		out := readStored(t, ctx, f, nil)
		if len(out.Content) != 2 || out.Content[1].Type != "image" || out.Content[1].MIMEType != "image/jpeg" {
			t.Fatalf("not visual content: %+v", out.StructuredContent)
		}
		pixels, err := base64.StdEncoding.DecodeString(out.Content[1].Data)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(bytes.NewReader(pixels))
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, _ := img.At(50, 50).RGBA()
		if r < 40000 || g > 12000 || b > 12000 {
			t.Fatal("source pixels lost")
		}
		if strings.Contains(out.Content[0].Text, "base64") {
			t.Fatal("bytes leaked into text")
		}
	}
}

func TestFilesReadImagesResizeAndNormalizeGIF(t *testing.T) {
	img := image.NewPaletted(image.Rect(0, 0, 2000, 1000), color.Palette{color.Transparent, color.RGBA{255, 0, 0, 255}})
	img.SetColorIndex(0, 0, 1)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	block, err := readableImage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(block.Data)
	out, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds().Dx() != 1600 || out.Bounds().Dy() != 800 {
		t.Fatal("image was not bounded", out.Bounds())
	}
	r, g, b, _ := out.At(500, 500).RGBA()
	if r < 60000 || g < 60000 || b < 60000 {
		t.Fatal("transparency was not flattened to white")
	}
	// A tiny PNG with a huge declared canvas must fail before pixel allocation.
	huge := append([]byte(nil), readFixture(t, "picture.png")...)
	binary.BigEndian.PutUint32(huge[16:20], 100000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	if _, err := readableImage(huge); err == nil || !strings.Contains(err.Error(), "pixels") {
		t.Fatal("oversized canvas accepted", err)
	}
}

func TestFilesReadPDFPageSelectionAndMalformedInput(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUploadTyped(t, ctx, "report.pdf", "application/pdf", string(readFixture(t, "report.pdf")))
	out := readStored(t, ctx, f, map[string]any{"pages": "2"})
	if out.StructuredContent["total_pages"] != 2 || len(out.Content) != 3 || out.Content[1].Text != "Page 2 of 2" || out.Content[2].Type != "image" {
		t.Fatalf("wrong page: %+v", out.StructuredContent)
	}
	data, _ := base64.StdEncoding.DecodeString(out.Content[2].Data)
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width > readImageEdge || config.Height > readImageEdge {
		t.Fatal("unbounded PDF render", config, err)
	}
	if _, err := (&App{}).toolReadCtx(context.Background(), ctx, map[string]any{"id": f.ID, "pages": "3"}); err == nil {
		t.Fatal("out-of-range page accepted")
	}
	if _, _, err := readPDF(context.Background(), []byte("bad PDF"), []int{1}); err == nil {
		t.Fatal("malformed PDF accepted")
	}
	// A failed render must leave the renderer usable for the next request.
	readStored(t, ctx, f, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := readPDF(canceled, readFixture(t, "report.pdf"), []int{1}); err == nil {
		t.Fatal("canceled render accepted")
	}
}

func TestFilesReadDOCXAndMalformedDocuments(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUploadTyped(t, ctx, "note.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", string(readFixture(t, "note.docx")))
	if out := readStored(t, ctx, f, nil); out.Content[1].Text != "The project code is ORCHID-726.\n" {
		t.Fatalf("DOCX body lost: %+v", out)
	}
	for _, data := range [][]byte{[]byte("bad zip"), readFixture(t, "picture.png")} {
		if _, err := readDOCX(data); err == nil {
			t.Fatal("invalid DOCX accepted")
		}
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("word/document.xml")
	_, _ = w.Write([]byte(strings.Repeat("X", (8<<20)+1)))
	_ = z.Close()
	if _, err := readDOCX(buf.Bytes()); err == nil {
		t.Fatal("oversized XML accepted")
	}
}

func TestFilesReadPermissionsAndInputErrors(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "secret.txt", "/private/", "secret")
	if _, err := (&App{}).toolReadCtx(withCaller(), ctx, map[string]any{"id": f.ID}); err == nil {
		t.Fatal("folder read permission bypassed")
	}
	allowed := withCaller(sdk.Grant{Effect: "allow", Permission: "files.read", Resource: "folder/private/**"})
	if _, err := (&App{}).toolReadCtx(allowed, ctx, map[string]any{"id": f.ID}); err != nil {
		t.Fatal(err)
	}
	t.Run("global project isolation", func(t *testing.T) {
		t.Setenv("APTEVA_PROJECT_ID", "")
		if _, err := (&App{}).toolReadCtx(context.Background(), ctx, map[string]any{"id": f.ID, "_project_id": "other"}); err == nil {
			t.Fatal("cross-project read allowed")
		}
	})
	for _, args := range []map[string]any{{"id": int64(99999)}, {"id": f.ID, "offset": -1}, {"id": f.ID, "offset": 100}, {"id": f.ID, "max_chars": 60001}, {"id": f.ID, "max_chars": 1.5}, {"id": f.ID, "pages": "1"}} {
		if _, err := (&App{}).toolReadCtx(context.Background(), ctx, args); err == nil {
			t.Fatalf("invalid input accepted: %v", args)
		}
	}
	for _, tc := range []struct{ name, mime, body string }{{"binary.bin", "application/octet-stream", "\x00\x01"}, {"invalid.txt", "text/plain", "\xff"}, {"invalid.png", "image/png", "broken"}} {
		file := mustUploadTyped(t, ctx, tc.name, tc.mime, tc.body)
		if _, err := (&App{}).toolReadCtx(context.Background(), ctx, map[string]any{"id": file.ID}); err == nil {
			t.Fatal("unreadable file accepted", tc.name)
		}
	}
	if _, err := ctx.AppDB().Exec("UPDATE files SET size_bytes=? WHERE id=?", readMaxBytes+1, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolReadCtx(context.Background(), ctx, map[string]any{"id": f.ID}); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestFilesReadPageSyntax(t *testing.T) {
	for _, input := range []any{"", "0", "-1", "3-1", "1-5", "1,2,3,4,5", "1--2", "1,", 4, "1-9223372036854775807"} {
		if _, err := parseReadPages(input); err == nil {
			t.Fatalf("invalid pages accepted: %v", input)
		}
	}
	pages, err := parseReadPages("2,1-3")
	if err != nil || !reflect.DeepEqual(pages, []int{2, 1, 3}) {
		t.Fatal(pages, err)
	}
}

func TestFilesReadPDFCancelActiveWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, _, err := readPDF(ctx, readFixture(t, "report.pdf"), []int{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("active render did not cancel", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("canceled PDF worker did not exit promptly")
	}
	if len(readPDFSlots) != 0 {
		t.Fatal("canceled PDF worker retained its slot")
	}
	blocks, total, err := readPDF(context.Background(), readFixture(t, "report.pdf"), []int{2})
	if err != nil || total != 2 || len(blocks) != 2 {
		t.Fatal("renderer did not recover after cancellation", total, err)
	}
}
