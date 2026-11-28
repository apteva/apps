package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// System packages for container-mode workspaces.
//
// Commands run as the workspace's own unprivileged user, so they cannot run
// apk themselves. Each workspace instead gets a `pkg` command that asks
// Workspaces (root) to install packages from the machine's configured Alpine
// repositories with `apk add --no-scripts`. The request goes over a loopback
// HTTP listener and carries a per-workspace token. Installed files lose any
// setuid/setgid bit and file capabilities, so a package cannot hand the
// workspace user root. Packages are machine-wide (every workspace on this
// machine sees them); each workspace records the ones it asked for and they
// are installed again after the machine is reset (a new container image).

const (
	apkPath          = "/sbin/apk"
	pkgIndexMaxAge   = time.Hour
	pkgRequestLimit  = 64 * 1024
	pkgMaxNames      = 50
	pkgCommandTimout = 15 * time.Minute
)

var pkgNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]{0,99}$`)

type pkgService struct {
	once    sync.Once
	url     string
	err     error
	mu      sync.Mutex // one apk at a time
	indexAt time.Time
}

// packagesUnavailable says why `pkg` cannot install system packages for this
// workload, or "" when it can.
func (d *localDriver) packagesUnavailable(wl *localWorkload) string {
	kind, _ := d.workloadMode(wl)
	if kind != sandboxContainer {
		if runtime.GOOS == "darwin" {
			return "system packages are not available in workspaces that run under the macOS sandbox: install tools on this Mac (for example with Homebrew), or create a Docker workspace (driver containers), where the image's package manager runs as root"
		}
		return "system packages are only available when Workspaces runs in container mode; install tools on this host, or create a Docker workspace (driver containers), where the image's package manager runs as root"
	}
	if !d.packagesOn() {
		return "system packages are turned off for this install (setting local_packages)"
	}
	if _, err := os.Stat(apkPath); err != nil {
		return "this machine has no apk; Workspaces installs system packages with Alpine's apk"
	}
	return ""
}

func (d *localDriver) packagesOn() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.packagesOff
}

// pkgEnv is what a command needs to reach the package service.
func (d *localDriver) pkgEnv(wl *localWorkload) map[string]string {
	if reason := d.packagesUnavailable(wl); reason != "" {
		return map[string]string{"APTEVA_PKG_UNAVAILABLE": reason}
	}
	url, err := d.pkgURL()
	if err != nil {
		return map[string]string{"APTEVA_PKG_UNAVAILABLE": "the package service did not start: " + err.Error()}
	}
	return map[string]string{"APTEVA_PKG_URL": url, "APTEVA_PKG_TOKEN": wl.PkgToken, "APTEVA_PKG_WORKLOAD": wl.ID}
}

func (d *localDriver) pkgURL() (string, error) {
	d.pkg.once.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			d.pkg.err = err
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(d.servePkg), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		d.pkg.url = "http://" + ln.Addr().String()
	})
	return d.pkg.url, d.pkg.err
}

// writePkgScript installs <workload>/bin/pkg, owned by root and not writable
// by the workspace user.
func (d *localDriver) writePkgScript(wl *localWorkload) error {
	dir := filepath.Join(d.workloadDir(wl.ID), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(dir, "pkg")
	if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, []byte(pkgScript)) {
		return nil
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(pkgScript), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

const pkgScript = `#!/bin/sh
# pkg - install system packages in this Apteva workspace.
usage() {
	cat <<'USAGE'
usage: pkg add <package>...     install packages (Alpine names, e.g. nodejs npm python3 git build-base)
       pkg del <package>...     remove packages this workspace added
       pkg list                 packages this workspace added
       pkg search <term>        find packages
Packages are installed for the whole machine and stay after restarts.
USAGE
}
cmd="$1"
[ $# -gt 0 ] && shift
case "$cmd" in
	add|install) cmd=add ;;
	del|remove|uninstall) cmd=del ;;
	list|ls) cmd=list ;;
	search) ;;
	""|-h|--help|help) usage; exit 0 ;;
	*) echo "pkg: unknown command: $cmd" >&2; usage >&2; exit 2 ;;
esac
if [ -n "$APTEVA_PKG_UNAVAILABLE" ]; then
	echo "pkg: $APTEVA_PKG_UNAVAILABLE" >&2
	exit 1
fi
if [ -z "$APTEVA_PKG_URL" ] || [ -z "$APTEVA_PKG_TOKEN" ]; then
	echo "pkg: run this inside an Apteva workspace command" >&2
	exit 1
fi
body=$(printf '%s\n' "$@")
wget -q -O - -T 900 \
	--header "X-Apteva-Workload: $APTEVA_PKG_WORKLOAD" \
	--header "X-Apteva-Pkg-Token: $APTEVA_PKG_TOKEN" \
	--post-data "$body" "$APTEVA_PKG_URL/$cmd" | awk '
	/^pkg-exit: [0-9]+$/ { code = $2; next }
	{ print; fflush() }
	END {
		if (code == "") { print "pkg: no answer from Apteva Workspaces" > "/dev/stderr"; exit 1 }
		exit code
	}'
`

// pkgWriter streams apk's output to the caller as it is produced.
type pkgWriter struct {
	w    http.ResponseWriter
	f    http.Flusher
	mu   sync.Mutex
	last byte
}

func (p *pkgWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	n, err := p.w.Write(b)
	if n > 0 {
		p.last = b[n-1]
	}
	if p.f != nil {
		p.f.Flush()
	}
	return n, err
}

func (p *pkgWriter) printf(format string, args ...any) { fmt.Fprintf(p, format, args...) }

func (p *pkgWriter) exit(code int) {
	p.mu.Lock()
	last := p.last
	p.mu.Unlock()
	if last != 0 && last != '\n' {
		p.Write([]byte("\n"))
	}
	p.printf("pkg-exit: %d\n", code)
}

func (d *localDriver) servePkg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	wl, err := d.loadWorkload(map[string]any{"workload_id": r.Header.Get("X-Apteva-Workload")})
	token := r.Header.Get("X-Apteva-Pkg-Token")
	if err != nil || wl.PkgToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(wl.PkgToken)) != 1 {
		http.Error(w, "pkg: not authorised", http.StatusForbidden)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, pkgRequestLimit))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	args := strings.Fields(string(raw))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)
	out := &pkgWriter{w: w, f: flusher}
	if reason := d.packagesUnavailable(wl); reason != "" {
		out.printf("pkg: %s\n", reason)
		out.exit(1)
		return
	}
	for _, name := range args {
		if !pkgNameRe.MatchString(name) {
			out.printf("pkg: %q is not a package name\n", name)
			out.exit(2)
			return
		}
	}
	if len(args) > pkgMaxNames {
		out.printf("pkg: at most %d packages at a time\n", pkgMaxNames)
		out.exit(2)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pkgCommandTimout)
	defer cancel()
	op := strings.TrimPrefix(r.URL.Path, "/")
	var code int
	switch op {
	case "add":
		code = d.pkgAdd(ctx, wl.ID, args, out)
	case "del":
		code = d.pkgDel(ctx, wl.ID, args, out)
	case "list":
		code = d.pkgList(ctx, wl.ID, out)
	case "search":
		code = d.pkgSearch(ctx, args, out)
	default:
		out.printf("pkg: unknown command %q\n", op)
		code = 2
	}
	out.exit(code)
}

func apkCommand(ctx context.Context, out io.Writer, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, apkPath, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout, cmd.Stderr = out, out
	return cmd
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	return 1
}

// pkgIndex refreshes the repository indexes when they are older than an
// hour. Caller holds d.pkg.mu.
func (d *localDriver) pkgIndex(ctx context.Context, out io.Writer) {
	if time.Since(d.pkg.indexAt) < pkgIndexMaxAge {
		return
	}
	if err := apkCommand(ctx, out, "update", "--quiet", "--no-progress").Run(); err == nil {
		d.pkg.indexAt = time.Now()
	}
}

// installedPackages maps package name to name-version for every installed
// package.
func installedPackages(ctx context.Context) (map[string]string, error) {
	var buf bytes.Buffer
	cmd := apkCommand(ctx, &buf, "info", "-v")
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("apk info: %w", err)
	}
	out := map[string]string{}
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		full := strings.TrimSpace(scanner.Text())
		if full == "" || strings.HasPrefix(full, "WARNING") {
			continue
		}
		out[packageName(full)] = full
	}
	return out, nil
}

// packageName strips "-<version>-r<n>" from an apk name-version.
func packageName(full string) string {
	parts := strings.Split(full, "-")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-2], "-")
	}
	return full
}

func (d *localDriver) pkgAdd(ctx context.Context, workloadID string, names []string, out *pkgWriter) int {
	if len(names) == 0 {
		out.printf("usage: pkg add <package>...\n")
		return 2
	}
	d.pkg.mu.Lock()
	defer d.pkg.mu.Unlock()
	code := d.apkAddLocked(ctx, names, out)
	if code != 0 {
		return code
	}
	d.recordPackages(workloadID, names, nil)
	return 0
}

// apkAddLocked installs names and hardens the files of every package that
// changed. Caller holds d.pkg.mu.
func (d *localDriver) apkAddLocked(ctx context.Context, names []string, out *pkgWriter) int {
	before, err := installedPackages(ctx)
	if err != nil {
		out.printf("pkg: %v\n", err)
		return 1
	}
	d.pkgIndex(ctx, out)
	args := append([]string{"add", "--no-scripts", "--no-progress"}, names...)
	if code := exitCodeOf(apkCommand(ctx, out, args...).Run()); code != 0 {
		out.printf("pkg: apk add failed (exit %d); `pkg search <term>` finds package names\n", code)
		return code
	}
	after, err := installedPackages(ctx)
	if err != nil {
		out.printf("pkg: %v\n", err)
		return 1
	}
	var changed []string
	for name, full := range after {
		if before[name] != full {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	for _, path := range packageFiles(ctx, changed) {
		if note := hardenFile(path); note != "" {
			out.printf("pkg: %s\n", note)
		}
	}
	return 0
}

// packageFiles lists the absolute paths of the files of the named packages.
func packageFiles(ctx context.Context, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	var buf bytes.Buffer
	cmd := apkCommand(ctx, &buf, append([]string{"info", "-L"}, names...)...)
	cmd.Stderr = io.Discard
	_ = cmd.Run()
	var out []string
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasSuffix(line, " contains:") || strings.HasPrefix(line, "WARNING") {
			continue
		}
		out = append(out, "/"+strings.TrimPrefix(line, "/"))
	}
	return out
}

// hardenFile removes setuid/setgid bits and file capabilities, so nothing a
// package installs runs with more rights than the user who starts it.
func hardenFile(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	var notes []string
	if mode := info.Mode(); mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		if os.Chmod(path, mode.Perm()) == nil {
			notes = append(notes, "removed setuid/setgid")
		}
	}
	if unix.Removexattr(path, "security.capability") == nil {
		notes = append(notes, "removed file capabilities")
	}
	if len(notes) == 0 {
		return ""
	}
	return strings.Join(notes, " and ") + " from " + path
}

func (d *localDriver) pkgDel(ctx context.Context, workloadID string, names []string, out *pkgWriter) int {
	if len(names) == 0 {
		out.printf("usage: pkg del <package>...\n")
		return 2
	}
	d.pkg.mu.Lock()
	defer d.pkg.mu.Unlock()
	wl, err := d.loadWorkload(map[string]any{"workload_id": workloadID})
	if err != nil {
		out.printf("pkg: %v\n", err)
		return 1
	}
	mine := map[string]bool{}
	for _, name := range wl.Packages {
		mine[name] = true
	}
	for _, name := range names {
		if !mine[name] {
			out.printf("pkg: %s was not added by this workspace (pkg list shows the ones it added)\n", name)
			return 1
		}
	}
	shared := d.packagesOfOthers(workloadID)
	var remove []string
	for _, name := range names {
		if shared[name] {
			out.printf("pkg: %s stays installed: another workspace on this machine uses it\n", name)
			continue
		}
		remove = append(remove, name)
	}
	if len(remove) > 0 {
		args := append([]string{"del", "--no-scripts", "--no-progress"}, remove...)
		if code := exitCodeOf(apkCommand(ctx, out, args...).Run()); code != 0 {
			return code
		}
	}
	d.recordPackages(workloadID, nil, names)
	return 0
}

func (d *localDriver) pkgList(ctx context.Context, workloadID string, out *pkgWriter) int {
	wl, err := d.loadWorkload(map[string]any{"workload_id": workloadID})
	if err != nil {
		out.printf("pkg: %v\n", err)
		return 1
	}
	if len(wl.Packages) == 0 {
		out.printf("No packages added by this workspace. Install some with: pkg add <package>\n")
		return 0
	}
	installed, _ := installedPackages(ctx)
	for _, name := range wl.Packages {
		if full := installed[name]; full != "" {
			out.printf("%s\n", full)
		} else {
			out.printf("%s (not installed; it is installed again before the next command)\n", name)
		}
	}
	return 0
}

func (d *localDriver) pkgSearch(ctx context.Context, terms []string, out *pkgWriter) int {
	if len(terms) == 0 {
		out.printf("usage: pkg search <term>\n")
		return 2
	}
	d.pkg.mu.Lock()
	d.pkgIndex(ctx, out)
	d.pkg.mu.Unlock()
	return exitCodeOf(apkCommand(ctx, out, append([]string{"search", "-v"}, terms...)...).Run())
}

// recordPackages updates the packages a workload asked for.
func (d *localDriver) recordPackages(workloadID string, add, remove []string) {
	wl, err := d.loadWorkload(map[string]any{"workload_id": workloadID})
	if err != nil {
		return
	}
	set := map[string]bool{}
	for _, name := range wl.Packages {
		set[name] = true
	}
	for _, name := range add {
		set[name] = true
	}
	for _, name := range remove {
		delete(set, name)
	}
	wl.Packages = wl.Packages[:0]
	for name := range set {
		wl.Packages = append(wl.Packages, name)
	}
	sort.Strings(wl.Packages)
	wl.UpdatedAt = nowUTC()
	_ = d.saveWorkload(wl)
}

func (d *localDriver) packagesOfOthers(workloadID string) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(d.root)
	for _, entry := range entries {
		if entry.Name() == workloadID || !localWorkloadIDRe.MatchString(entry.Name()) {
			continue
		}
		if wl, err := d.loadWorkload(map[string]any{"workload_id": entry.Name()}); err == nil {
			for _, name := range wl.Packages {
				out[name] = true
			}
		}
	}
	return out
}

// replayPackages installs again the packages a workload added that are no
// longer on the machine (a new container image resets the system), once per
// workload per Workspaces process.
func (d *localDriver) replayPackages(wl *localWorkload, log io.Writer) {
	if len(wl.Packages) == 0 || d.packagesUnavailable(wl) != "" {
		return
	}
	d.mu.Lock()
	done := d.replayed[wl.ID]
	d.replayed[wl.ID] = true
	d.mu.Unlock()
	if done {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pkgCommandTimout)
	defer cancel()
	d.pkg.mu.Lock()
	defer d.pkg.mu.Unlock()
	installed, err := installedPackages(ctx)
	if err != nil {
		return
	}
	var missing []string
	for _, name := range wl.Packages {
		if installed[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	out := &pkgWriter{w: discardResponse{log}}
	out.printf("pkg: installing this workspace's packages again (the machine was reset): %s\n", strings.Join(missing, " "))
	if code := d.apkAddLocked(ctx, missing, out); code != 0 {
		out.printf("pkg: reinstall failed (exit %d); run pkg add again\n", code)
	}
}

// discardResponse lets pkgWriter write into a command's log.
type discardResponse struct{ io.Writer }

func (discardResponse) Header() http.Header { return http.Header{} }
func (discardResponse) WriteHeader(int)     {}
