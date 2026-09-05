package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	tenantTransferTTL      = 2 * time.Hour
	tenantTransferTimeoutS = 7200
)

type tenantTransfer struct {
	ID             string
	Direction      string
	SourceDir      string
	TargetDir      string
	TenantID       string
	CleanupDir     string
	BackupManifest *fleetTenantBackupManifest
	ExpiresAt      time.Time
	InUse          bool
}

type tenantTransferProgress func(phase, detail string)

func (a *App) initTransferState() error {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("init transfer secret: %w", err)
	}
	a.transferMu.Lock()
	defer a.transferMu.Unlock()
	a.transferSecret = secret
	a.transfers = map[string]*tenantTransfer{}
	return nil
}

func (a *App) transferTenantData(ctx *sdk.AppCtx, sourceHost, targetHost fleetHost, sourceDir, targetDir, slug string, snapshot bool) error {
	return a.transferTenantDataWithProgress(ctx, sourceHost, targetHost, sourceDir, targetDir, slug, snapshot, nil)
}

func (a *App) transferTenantDataWithProgress(ctx *sdk.AppCtx, sourceHost, targetHost fleetHost, sourceDir, targetDir, slug string, snapshot bool, progress tenantTransferProgress) error {
	var err error
	if sourceHost.IsLocal() {
		if _, err := os.Stat(sourceDir); err != nil {
			return fmt.Errorf("local data dir %q not found: %w", sourceDir, err)
		}
		if targetHost.IsLocal() {
			err = streamLocalTenantToLocal(sourceDir, targetDir)
		} else {
			err = a.streamLocalTenantToRemote(ctx, sourceDir, targetHost.InstanceID, targetDir, slug)
		}
	} else if targetHost.IsLocal() {
		err = a.streamRemoteTenantToLocal(ctx, sourceHost.InstanceID, sourceDir, targetDir, slug, snapshot)
	} else {
		err = a.streamRemoteTenantToRemote(ctx, sourceHost, targetHost, sourceDir, targetDir, slug, progress)
	}
	if err != nil {
		return err
	}
	if err := removeTransferredRuntimeArtifacts(ctx, targetHost, targetDir); err != nil {
		return &publishedTransferError{err}
	}
	return nil
}

func removeTransferredRuntimeArtifacts(ctx *sdk.AppCtx, targetHost fleetHost, targetDir string) error {
	pidPath := filepath.Join(targetDir, "fleet.pid")
	if targetHost.IsLocal() {
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove transferred fleet pid: %w", err)
		}
		return nil
	}
	out, code, err := instanceRunCommand(ctx, targetHost.InstanceID, fmt.Sprintf("rm -f %s", sh(pidPath)), 10)
	if err != nil || code != 0 {
		return fmt.Errorf("remove transferred fleet pid: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}

func streamLocalTenantToLocal(sourceDir, targetDir string) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := streamTenantArchiveLocal(pw, sourceDir)
		_ = pw.CloseWithError(err)
		errCh <- err
	}()
	extractErr := extractTenantArchiveStream(pr, targetDir)
	_ = pr.Close()
	streamErr := <-errCh
	if extractErr != nil {
		return extractErr
	}
	return streamErr
}

func (a *App) streamLocalTenantToRemote(ctx *sdk.AppCtx, sourceDir string, instanceID int64, targetDir, slug string) error {
	transferURL, err := a.createTransferURL(ctx, sourceDir, tenantTransferTTL)
	if err != nil {
		return err
	}
	cmd := "set -eu\n" + remoteExtractCommand(transferURL, targetDir)
	return runHostedTransferJob(ctx, instanceID, cmd, slug)
}

func (a *App) createTransferURL(ctx *sdk.AppCtx, sourceDir string, ttl time.Duration) (string, error) {
	return a.createSignedTransferURL(ctx, &tenantTransfer{Direction: "download", SourceDir: sourceDir}, ttl)
}

func (a *App) createUploadURL(ctx *sdk.AppCtx, targetDir string, ttl time.Duration) (string, error) {
	return a.createSignedTransferURL(ctx, &tenantTransfer{Direction: "upload", TargetDir: targetDir}, ttl)
}

func (a *App) createBackupDownloadURL(ctx *sdk.AppCtx, sourceDir, cleanupDir string, manifest fleetTenantBackupManifest, ttl time.Duration) (string, error) {
	return a.createSignedTransferURL(ctx, &tenantTransfer{
		Direction: "backup-download", SourceDir: sourceDir, CleanupDir: cleanupDir, BackupManifest: &manifest,
	}, ttl)
}

func (a *App) createBackupRestoreURL(ctx *sdk.AppCtx, tenantID string, ttl time.Duration) (string, error) {
	return a.createSignedTransferURL(ctx, &tenantTransfer{Direction: "backup-restore", TenantID: tenantID}, ttl)
}

func (a *App) createSignedTransferURL(ctx *sdk.AppCtx, transfer *tenantTransfer, ttl time.Duration) (string, error) {
	publicBase := strings.TrimRight(a.publicTransferBaseURL(ctx), "/")
	if publicBase == "" {
		return "", errors.New("platform public_url is required for hosted streaming transfer")
	}
	u, err := url.Parse(publicBase)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("parse platform public_url %q", publicBase)
	}
	if u.Scheme != "https" && !allowInsecurePublicURL() {
		return "", errors.New("hosted streaming transfer requires an HTTPS platform public_url")
	}
	id, err := randomHex(16)
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(ttl).Unix()
	sig := a.signTransfer(id, exp)
	a.transferMu.Lock()
	if a.transfers == nil {
		a.transfers = map[string]*tenantTransfer{}
	}
	a.cleanupExpiredTransfersLocked(time.Now())
	transfer.ID = id
	transfer.ExpiresAt = time.Unix(exp, 0)
	a.transfers[id] = transfer
	a.transferMu.Unlock()

	u.Path = strings.TrimRight(u.Path, "/") + "/api/apps/fleet/transfers/" + url.PathEscape(id)
	q := u.Query()
	if installID := myInstallID(); installID > 0 {
		q.Set("install_id", strconv.FormatInt(installID, 10))
	}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", sig)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (a *App) publicTransferBaseURL(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil && strings.TrimSpace(info.PublicURL) != "" {
			return strings.TrimSpace(info.PublicURL)
		}
	}
	if env := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")); env != "" {
		return env
	}
	if a != nil && a.publicHost != "" && a.publicHost != "localhost" {
		return "http://" + a.publicHost + ":5280"
	}
	return ""
}

func (a *App) httpTransfer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/transfers/")
	if id == "" {
		http.Error(w, "transfer id required", http.StatusBadRequest)
		return
	}
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil || exp <= 0 {
		http.Error(w, "invalid exp", http.StatusForbidden)
		return
	}
	if time.Now().Unix() > exp {
		http.Error(w, "transfer expired", http.StatusForbidden)
		return
	}
	if !a.verifyTransfer(id, exp, r.URL.Query().Get("sig")) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	a.transferMu.Lock()
	a.cleanupExpiredTransfersLocked(time.Now())
	tr := a.transfers[id]
	if tr != nil && !tr.InUse {
		tr.InUse = true
	} else {
		tr = nil
	}
	a.transferMu.Unlock()
	if tr == nil || time.Now().After(tr.ExpiresAt) {
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	success := false
	defer func() {
		a.transferMu.Lock()
		if success {
			delete(a.transfers, id)
		} else if current := a.transfers[id]; current != nil {
			current.InUse = false
		}
		a.transferMu.Unlock()
		if success && tr.CleanupDir != "" {
			_ = os.RemoveAll(tr.CleanupDir)
		}
	}()
	if tr.Direction == "backup-restore" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxExtractedTenantBytes)
		report, err := a.restoreFleetTenantArchive(platformContext(nil), tr.TenantID, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		success = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(report)
		return
	}
	if tr.Direction == "upload" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := extractTenantArchiveStream(r.Body, tr.TargetDir); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		success = true
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if (tr.Direction != "download" && tr.Direction != "backup-download") || r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	var streamErr error
	if tr.Direction == "backup-download" && tr.BackupManifest != nil {
		streamErr = writeFleetTenantArchive(w, tr.SourceDir, *tr.BackupManifest)
	} else {
		streamErr = streamTenantArchiveLocal(w, tr.SourceDir)
	}
	if streamErr != nil {
		// If streaming already started this may only reach logs, but for
		// early failures it gives curl a non-2xx response.
		http.Error(w, streamErr.Error(), http.StatusInternalServerError)
		return
	}
	success = true
}

func (a *App) streamRemoteTenantToLocal(ctx *sdk.AppCtx, instanceID int64, sourceDir, targetDir, slug string, snapshot bool) error {
	uploadURL, err := a.createUploadURL(ctx, targetDir, tenantTransferTTL)
	if err != nil {
		return err
	}
	source := "$SRC"
	setup := ""
	cleanup := ""
	if snapshot {
		setup = `
command -v python3 >/dev/null 2>&1 || { echo "python3 required for non-disruptive remote clone snapshots" >&2; exit 127; }
SNAP=$(mktemp -d /tmp/fleet-clone-snap-XXXXXX)
cleanup() { rm -rf "$SNAP"; }
trap cleanup EXIT
python3 - "$SRC" "$SNAP" <<'PY'
import os, shutil, sqlite3, stat, sys
src, dst = sys.argv[1], sys.argv[2]
for root, dirs, files in os.walk(src, followlinks=False):
    rel = os.path.relpath(root, src)
    outdir = dst if rel == "." else os.path.join(dst, rel)
    os.makedirs(outdir, exist_ok=True)
    for name in files:
        if name.endswith(".db-wal") or name.endswith(".db-shm"):
            continue
        s, d = os.path.join(root, name), os.path.join(outdir, name)
        st = os.lstat(s)
        if stat.S_ISLNK(st.st_mode):
            os.symlink(os.readlink(s), d)
        elif stat.S_ISREG(st.st_mode) and name.endswith(".db"):
            srcdb = sqlite3.connect("file:" + s + "?mode=ro", uri=True, timeout=5)
            dstdb = sqlite3.connect(d)
            srcdb.backup(dstdb)
            dstdb.close(); srcdb.close()
            os.chmod(d, st.st_mode & 0o777)
        elif stat.S_ISREG(st.st_mode):
            shutil.copy2(s, d)
PY`
		source = "$SNAP"
		cleanup = ""
	}
	script := fmt.Sprintf(`
set -eu
SRC=%s
URL=%s
test -d "$SRC"
%s
if command -v curl >/dev/null 2>&1; then
  tar czf - -C %s . | curl -fsS --retry 2 --connect-timeout 15 -X POST --data-binary @- "$URL"
elif command -v wget >/dev/null 2>&1; then
  tar czf - -C %s . | wget -qO- --method=POST --body-file=- "$URL"
else
  echo "curl or wget required for fleet streaming transfer" >&2
  exit 127
fi
%s
`, sh(sourceDir), sh(uploadURL), setup, source, source, cleanup)
	return runHostedTransferJob(ctx, instanceID, script, slug)
}

func runHostedTransferJob(ctx *sdk.AppCtx, instanceID int64, script, slug string) error {
	return runHostedTransferJobWithProgress(ctx, instanceID, script, slug, nil)
}

func runHostedTransferJobWithProgress(ctx *sdk.AppCtx, instanceID int64, script, slug string, progress tenantTransferProgress) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	launch := fmt.Sprintf(`
set -eu
JOB=$(mktemp -d /tmp/fleet-transfer-job-%s-XXXXXX)
printf '%%s' %s | base64 -d > "$JOB/payload.sh"
cat > "$JOB/run.sh" <<'WRAPPER'
#!/bin/sh
set +e
sh "$(dirname "$0")/payload.sh" >"$(dirname "$0")/output.log" 2>&1
CODE=$?
printf '%%s\n' "$CODE" >"$(dirname "$0")/done"
exit "$CODE"
WRAPPER
chmod 700 "$JOB/run.sh" "$JOB/payload.sh"
setsid "$JOB/run.sh" >/dev/null 2>&1 &
printf '%%s\n' "$!" > "$JOB/pid"
printf '%%s\n' "$JOB"
`, shellSafeSlug(slug), sh(encoded))
	out, code, err := instanceRunCommand(ctx, instanceID, launch, 15)
	if err != nil || code != 0 {
		return fmt.Errorf("launch remote transfer job: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	job := strings.TrimSpace(out)
	if !strings.HasPrefix(job, "/tmp/fleet-transfer-job-") || strings.ContainsAny(job, "\r\n") {
		return fmt.Errorf("invalid remote transfer job path %q", job)
	}
	deadline := time.Now().Add(time.Duration(tenantTransferTimeoutS) * time.Second)
	defer func() {
		cleanup := fmt.Sprintf(`PID=$(cat %s/pid 2>/dev/null || true); case "$PID" in ''|*[!0-9]*) ;; *) [ "$PID" -gt 1 ] && kill -TERM -"$PID" 2>/dev/null || true;; esac; rm -rf %s`, sh(job), sh(job))
		_, _, _ = instanceRunCommand(ctx, instanceID, cleanup, 10)
	}()
	for {
		if time.Now().After(deadline) {
			stop := fmt.Sprintf(`PID=$(cat %s/pid 2>/dev/null || true); case "$PID" in ''|*[!0-9]*) exit 0;; esac; [ "$PID" -gt 1 ] && kill -TERM -"$PID" 2>/dev/null || true`, sh(job))
			_, _, _ = instanceRunCommand(ctx, instanceID, stop, 10)
			return errors.New("remote transfer timed out")
		}
		poll := fmt.Sprintf(`if [ -f %s/done ]; then printf 'done:'; cat %s/done; tail -c 8192 %s/output.log 2>/dev/null || true; else PID=$(cat %s/pid 2>/dev/null || true); if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then printf 'running:\n'; tail -c 8192 %s/output.log 2>/dev/null || true; else printf 'failed:'; tail -c 8192 %s/output.log 2>/dev/null || true; fi; fi`, sh(job), sh(job), sh(job), sh(job), sh(job), sh(job))
		status, pollCode, pollErr := instanceRunCommand(ctx, instanceID, poll, 15)
		if pollErr != nil || pollCode != 0 {
			return fmt.Errorf("poll remote transfer: %w (exit %d): %s", pollErr, pollCode, strings.TrimSpace(status))
		}
		if strings.HasPrefix(status, "done:") {
			lines := strings.SplitN(strings.TrimPrefix(status, "done:"), "\n", 2)
			if strings.TrimSpace(lines[0]) != "0" {
				log := ""
				if len(lines) > 1 {
					log = strings.TrimSpace(lines[1])
				}
				return fmt.Errorf("remote transfer failed with exit %s: %s", strings.TrimSpace(lines[0]), log)
			}
			return nil
		}
		if strings.HasPrefix(status, "running:") && progress != nil {
			progress("transfer", strings.TrimSpace(strings.TrimPrefix(status, "running:")))
		}
		if strings.HasPrefix(status, "failed:") {
			return fmt.Errorf("remote transfer job exited without status: %s", strings.TrimSpace(strings.TrimPrefix(status, "failed:")))
		}
		time.Sleep(2 * time.Second)
	}
}

const (
	maxExtractedTenantBytes = int64(1 << 40) // 1 TiB
	maxExtractedTenantFiles = 2_000_000
)

func extractTenantArchiveStream(r io.Reader, targetDir string) error {
	if _, err := os.Lstat(targetDir); err == nil {
		return fmt.Errorf("target data dir already exists: %s", targetDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(targetDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".fleet-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		files++
		if files > maxExtractedTenantFiles || hdr.Size < 0 || total+hdr.Size > maxExtractedTenantBytes {
			return errors.New("tenant archive exceeds extraction limits")
		}
		total += hdr.Size
		if err := extractArchiveEntry(root, hdr.Name, hdr, tr); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return err
	}
	return publishTenantDirectory(stage, targetDir)
}

func (a *App) signTransfer(id string, exp int64) string {
	a.transferMu.Lock()
	secret := append([]byte(nil), a.transferSecret...)
	a.transferMu.Unlock()
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, id)
	_, _ = io.WriteString(mac, "\n")
	_, _ = io.WriteString(mac, strconv.FormatInt(exp, 10))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) verifyTransfer(id string, exp int64, sig string) bool {
	if sig == "" {
		return false
	}
	want := a.signTransfer(id, exp)
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	wb, _ := hex.DecodeString(want)
	return hmac.Equal(got, wb)
}

func (a *App) cleanupExpiredTransfersLocked(now time.Time) {
	for id, tr := range a.transfers {
		if tr == nil || now.After(tr.ExpiresAt) {
			if tr != nil && tr.CleanupDir != "" {
				_ = os.RemoveAll(tr.CleanupDir)
			}
			delete(a.transfers, id)
		}
	}
}

func streamTenantArchiveLocal(w io.Writer, srcDir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := writeTenantArchiveTar(tw, srcDir)
	if closeErr := tw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	return err
}

func writeTenantArchiveTar(tw *tar.Writer, srcDir string) error {
	scratch, err := os.MkdirTemp(transferScratchRoot(), "sqlite-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	seq := 0
	return filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isSQLiteSidecar(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return tw.WriteHeader(hdr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		filePath := path
		if filepath.Ext(path) == ".db" {
			seq++
			filePath = filepath.Join(scratch, fmt.Sprintf("%d.db", seq))
			if err := cloneSQLiteDB(path, filePath); err != nil {
				return fmt.Errorf("clone sqlite %s: %w", path, err)
			}
			dbInfo, err := os.Stat(filePath)
			if err != nil {
				return err
			}
			hdr.Size = dbInfo.Size()
			hdr.Mode = int64(info.Mode().Perm())
			hdr.ModTime = info.ModTime()
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func transferScratchRoot() string {
	root := filepath.Join(localDataRoot(), ".transfers")
	_ = os.MkdirAll(root, 0o700)
	return root
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func shellSafeSlug(slug string) string {
	if slug == "" {
		return "tenant"
	}
	var b strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "tenant"
	}
	return b.String()
}

type publishedTransferError struct{ error }
