package instanceworker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWorker(t *testing.T) *Worker {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	w, e := New(Config{OperationID: strings.Repeat("a", 48), Token: strings.Repeat("b", 48), Kind: "restore", Identity: "source-identity", TargetPath: filepath.Join(dir, "target")}, dir)
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func makeArchive(t *testing.T, w *Worker, entries ...*tar.Header) {
	t.Helper()
	f, e := os.Create(w.archive())
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tr := tar.NewWriter(gz)
	raw, _ := json.Marshal(Manifest{Format: Format, Identity: w.Config.Identity, Paths: []string{"/source"}})
	tr.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg})
	tr.Write(raw)
	for _, h := range entries {
		if e = tr.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if h.Size > 0 {
			tr.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	tr.Close()
	gz.Close()
	f.Close()
	w.Config.SHA256, e = HashFile(w.archive())
	if e != nil {
		t.Fatal(e)
	}
}
func TestRestoreRejectsTraversalAndLinks(t *testing.T) {
	for _, h := range []*tar.Header{{Name: "data/0/../../escape", Typeflag: tar.TypeReg}, {Name: "data/0/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, {Name: "/tmp/escape", Typeflag: tar.TypeReg}, {Name: "data/0", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}} {
		t.Run(h.Name, func(t *testing.T) {
			w := testWorker(t)
			makeArchive(t, w, h)
			if e := w.Restore(context.Background()); e == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, e := os.Stat(w.Config.TargetPath); !os.IsNotExist(e) {
				t.Fatal("failed restore activated")
			}
		})
	}
}
func TestExistingTargetIsNotOverwritten(t *testing.T) {
	w := testWorker(t)
	os.Mkdir(w.Config.TargetPath, 0700)
	sentinel := filepath.Join(w.Config.TargetPath, "important")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	makeArchive(t, w)
	if e := w.Restore(context.Background()); e == nil {
		t.Fatal("existing target accepted")
	}
	b, _ := os.ReadFile(sentinel)
	if string(b) != "keep" {
		t.Fatal("existing contents changed")
	}
}
func TestRestoreIntegrityFailure(t *testing.T) {
	w := testWorker(t)
	makeArchive(t, w)
	w.Config.SHA256 = "wrong"
	if e := w.Restore(context.Background()); e == nil {
		t.Fatal("integrity failure accepted")
	}
	if _, e := os.Stat(w.Config.TargetPath); !os.IsNotExist(e) {
		t.Fatal("failed integrity created target")
	}
}
func TestSymlinkPathsAreRejected(t *testing.T) {
	w := testWorker(t)
	real := filepath.Join(w.Dir, "real")
	os.Mkdir(real, 0700)
	link := filepath.Join(w.Dir, "link")
	os.Symlink(real, link)
	if _, e := Probe([]string{link}); e == nil {
		t.Fatal("symlink source accepted")
	}
	w.Config.TargetPath = filepath.Join(link, "target")
	makeArchive(t, w)
	if e := w.Restore(context.Background()); e == nil {
		t.Fatal("symlink restore parent accepted")
	}
}
func TestCaptureRejectsSymlinksAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			w := testWorker(t)
			source := filepath.Join(w.Dir, "source")
			os.Mkdir(source, 0700)
			p := filepath.Join(source, "bad")
			if kind == "symlink" {
				os.Symlink("/etc/passwd", p)
			} else {
				makeFIFO(t, p)
			}
			w.Config.Kind = "backup"
			w.Config.Paths = []string{source}
			if e := w.Capture(context.Background()); e == nil {
				t.Fatal("special source accepted")
			}
		})
	}
}
func TestAtomicRestoreAndReceiptRetry(t *testing.T) {
	w := testWorker(t)
	makeArchive(t, w, &tar.Header{Name: "data/0", Typeflag: tar.TypeDir, Mode: 0750}, &tar.Header{Name: "data/0/file", Typeflag: tar.TypeReg, Mode: 0640, Size: 3})
	if e := w.Restore(context.Background()); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(w.Config.TargetPath, "0/file")); e != nil || string(b) != "xxx" {
		t.Fatal("contents mismatch", e)
	}
	w.state = State{Stage: "restoring"}
	if e := w.Restore(context.Background()); e != nil {
		t.Fatal("receipt recovery failed", e)
	}
	if w.Snapshot().Stage != "restored" {
		t.Fatal("receipt did not recover completion")
	}
}

func TestWorkerHTTPAuthenticationAndShutdown(t *testing.T) {
	w := testWorker(t)
	source := filepath.Join(w.Dir, "source")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "file"), []byte("private archive"), 0600)
	w.Config.Kind = "backup"
	w.Config.Paths = []string{source}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Serve(ctx) }()
	var endpoint struct {
		Port int `json:"port"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, e := os.ReadFile(filepath.Join(w.Dir, "endpoint.json"))
		if e == nil && json.Unmarshal(b, &endpoint) == nil && endpoint.Port > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if endpoint.Port == 0 {
		t.Fatal("worker failed to listen")
	}
	client := &http.Client{Timeout: time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", endpoint.Port)
	response, e := client.Get(base + "/archive")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("unauthenticated archive access allowed")
	}
	req, _ := http.NewRequest("GET", base+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+w.Config.Token)
	response, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("authenticated status unavailable")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker ignored cancellation")
	}
}
