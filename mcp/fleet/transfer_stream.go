package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	ID        string
	SourceDir string
	ExpiresAt time.Time
}

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
	if sourceHost.IsLocal() {
		if _, err := os.Stat(sourceDir); err != nil {
			return fmt.Errorf("local data dir %q not found: %w", sourceDir, err)
		}
		if !targetHost.IsLocal() {
			return a.streamLocalTenantToRemote(ctx, sourceDir, targetHost.InstanceID, targetDir, slug)
		}
	}

	var raw []byte
	var err error
	switch {
	case sourceHost.IsLocal():
		raw, err = makeTenantArchiveLocal(sourceDir)
	case snapshot:
		raw, err = makeTenantArchiveRemoteSnapshot(ctx, sourceHost.InstanceID, sourceDir, slug)
	default:
		raw, err = makeTenantArchiveRemoteCold(ctx, sourceHost.InstanceID, sourceDir, slug)
	}
	if err != nil {
		return err
	}
	if targetHost.IsLocal() {
		return extractTenantArchiveLocal(raw, targetDir)
	}
	return extractTenantArchiveRemote(ctx, targetHost.InstanceID, raw, targetDir, slug)
}

func (a *App) streamLocalTenantToRemote(ctx *sdk.AppCtx, sourceDir string, instanceID int64, targetDir, slug string) error {
	transferURL, err := a.createTransferURL(ctx, sourceDir, tenantTransferTTL)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(`
set -eu
DST=%s
URL=%s
PARENT=$(dirname "$DST")
test ! -e "$DST"
mkdir -p "$PARENT"
TMP=$(mktemp -d "$PARENT/.transfer-%s-XXXXXX")
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT
PAYLOAD="$TMP/payload"
mkdir -p "$PAYLOAD"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --retry 3 --connect-timeout 15 "$URL" | tar xzf - -C "$PAYLOAD"
elif command -v wget >/dev/null 2>&1; then
  wget -qO- "$URL" | tar xzf - -C "$PAYLOAD"
else
  echo "curl or wget required for fleet streaming transfer" >&2
  exit 127
fi
mv "$PAYLOAD" "$DST"
`, sh(targetDir), sh(transferURL), shellSafeSlug(slug))
	if out, code, err := instanceRunCommand(ctx, instanceID, cmd, tenantTransferTimeoutS); err != nil || code != 0 {
		return fmt.Errorf("remote streamed extract: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}

func (a *App) createTransferURL(ctx *sdk.AppCtx, sourceDir string, ttl time.Duration) (string, error) {
	publicBase := strings.TrimRight(a.publicTransferBaseURL(ctx), "/")
	if publicBase == "" {
		return "", errors.New("platform public_url is required for hosted streaming transfer")
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
	a.transfers[id] = &tenantTransfer{ID: id, SourceDir: sourceDir, ExpiresAt: time.Unix(exp, 0)}
	a.transferMu.Unlock()

	u, err := url.Parse(publicBase)
	if err != nil {
		return "", fmt.Errorf("parse public_url: %w", err)
	}
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
	a.transferMu.Unlock()
	if tr == nil || time.Now().After(tr.ExpiresAt) {
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	if err := streamTenantArchiveLocal(w, tr.SourceDir); err != nil {
		// If streaming already started this may only reach logs, but for
		// early failures it gives curl a non-2xx response.
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
