package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryWaitsForLargeSnapshotPreparation(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises the former 30-second response-header deadline")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(31 * time.Second):
			io.WriteString(w, "snapshot")
		}
	}))
	defer server.Close()
	c := &recoveryHTTPClient{baseURL: server.URL, client: platformTransferClient}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	body, err := c.OpenPlatformSnapshotWithPassphrase(ctx, "portable recovery passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil || string(raw) != "snapshot" {
		t.Fatalf("body=%q err=%v", raw, err)
	}
}

func TestRecoveryPreparationStillHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c := &recoveryHTTPClient{baseURL: server.URL, client: platformTransferClient}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.OpenPlatformSnapshotWithPassphrase(ctx, "portable recovery passphrase")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancellation err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestPlatformArchiveLimitsBeforeUpload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(platformArchiveLimit + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = validateSnapshotArchiveContext(context.Background(), path, true); err == nil || !strings.Contains(err.Error(), "compressed restore limit") {
		t.Fatalf("err=%v", err)
	}
	for _, count := range []int{platformEntryLimit - 1, platformEntryLimit} {
		f, err = os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		manifest := []byte(`{"format_version":2}`)
		tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(manifest))})
		tw.Write(manifest)
		for i := 0; i < count; i++ {
			if err = tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("apps/1/data/%d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		if err = tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err = gz.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()
		_, err = validateSnapshotArchiveContext(context.Background(), path, true)
		if (err == nil) != (count < platformEntryLimit) {
			t.Fatalf("%d payload files: %v", count, err)
		}
	}
}

func TestValidationCancelsDuringDecompression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: 2})
	tw.Write([]byte("{}"))
	tw.WriteHeader(&tar.Header{Name: "payload", Size: 16 << 20})
	io.CopyN(tw, zeroBackupReader{}, 16<<20)
	tw.Close()
	gz.Close()
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = validateSnapshotArchiveContext(ctx, path, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type zeroBackupReader struct{}

func (zeroBackupReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestArchiveDiskWriterFailsBeforeWritingPastBudget(t *testing.T) {
	var out bytes.Buffer
	w := &archiveDiskWriter{ctx: context.Background(), out: &out, path: t.TempDir(), remaining: 8}
	if _, err := w.Write(make([]byte, 9)); err == nil || out.Len() != 0 {
		t.Fatal("archive exceeded size ceiling")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.ctx = ctx
	if _, err := w.Write([]byte("x")); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatal("cancelled writer wrote data")
	}
	w.ctx = context.Background()
	w.path = filepath.Join(t.TempDir(), "missing")
	if _, err := w.Write([]byte("x")); err == nil || out.Len() != 0 {
		t.Fatal("unknown disk capacity allowed writes")
	}
}

func TestDiskAndEncryptionMemoryHeadroom(t *testing.T) {
	if checkDiskSpace(diskReserve+10, 11) == nil || checkDiskSpace(diskReserve-1, 0) == nil {
		t.Fatal("disk reserve bypassed")
	}
	if err := checkDiskSpace(diskReserve+10, 10); err != nil {
		t.Fatal(err)
	}
	for _, available := range []int64{-1, 128 << 20, 256 << 20, (384 << 20) - 1} {
		if checkEncryptionMemory(available) == nil {
			t.Fatalf("accepted %d bytes", available)
		}
	}
	if err := checkEncryptionMemory(384 << 20); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptionMemoryUsesContainerAndAncestorLimits(t *testing.T) {
	for _, tc := range []struct {
		name, cgroup string
		files        map[string]string
		want         int64
	}{
		{"nested-v2", "0::/parent/child\n", map[string]string{
			"/sys/fs/cgroup/parent/child/memory.max": "max", "/sys/fs/cgroup/parent/child/memory.current": "0",
			"/sys/fs/cgroup/parent/memory.max": "536870912", "/sys/fs/cgroup/parent/memory.current": "268435456",
		}, 256 << 20},
		{"over-limit", "0::/\n", map[string]string{"/sys/fs/cgroup/memory.max": "100", "/sys/fs/cgroup/memory.current": "101"}, 0},
		{"reclaimable-cache", "0::/\n", map[string]string{"/sys/fs/cgroup/memory.max": "536870912", "/sys/fs/cgroup/memory.current": "536870912", "/sys/fs/cgroup/memory.stat": "inactive_file 402653184\n"}, 384 << 20},
		{"v1", "3:memory:/service\n", map[string]string{"/sys/fs/cgroup/memory/service/memory.limit_in_bytes": "536870912", "/sys/fs/cgroup/memory/service/memory.usage_in_bytes": "134217728"}, 384 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				if path == "/proc/meminfo" {
					return []byte("MemAvailable: 8388608 kB\n"), nil
				}
				if path == "/proc/self/cgroup" {
					return []byte(tc.cgroup), nil
				}
				if v, ok := tc.files[path]; ok {
					return []byte(v), nil
				}
				return nil, os.ErrNotExist
			}
			if got := encryptionMemoryAvailable(read); got != tc.want {
				t.Fatalf("available=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestCloudMultipartPartsStayBoundedForLargeArchives(t *testing.T) {
	for _, size := range []int64{1 << 20, 64 << 30, 256 << 30, 1 << 40} {
		part, threads := cloudUploadParts(size)
		upper := size + size/1024 + (1 << 20)
		if (upper+int64(part)-1)/int64(part) > 9999 {
			t.Fatalf("%d bytes exceeds S3 part count", size)
		}
		if part*uint64(threads) > 128<<20 {
			t.Fatalf("upload buffers exceeded memory budget for %d bytes", size)
		}
	}
}

func TestInteractiveRunIsDurableAndDeduplicated(t *testing.T) {
	ctx := newTestCtx(t)
	dest, err := dbCreateDestination(ctx.AppDB(), &Destination{Name: "local", Kind: kindLocal, Enabled: true, Config: []byte(fmt.Sprintf(`{"path":%q}`, t.TempDir()))})
	if err != nil {
		t.Fatal(err)
	}
	first, err := enqueueInteractiveRun(ctx, dest, nil, defaultScope())
	if err != nil {
		t.Fatal(err)
	}
	second, err := enqueueInteractiveRun(ctx, dest, nil, defaultScope())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Status != "queued" {
		t.Fatalf("duplicate requests: %+v %+v", first, second)
	}
	var count int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backup_queue`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable queue count=%d err=%v", count, err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE runs SET status='running' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	third, err := enqueueInteractiveRun(ctx, dest, nil, defaultScope())
	if err != nil || third.ID != first.ID {
		t.Fatalf("repeated click duplicated active backup: %+v %v", third, err)
	}
}
