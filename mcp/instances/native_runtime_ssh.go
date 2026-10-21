package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type nativeSSH struct{ inspections *nativeInspectionPool }

func nativeRun(c context.Context, i *Instance, cmd string) (string, error) {
	clone := *i
	clone.workContext = c
	t := 20 * time.Second
	if end, ok := c.Deadline(); ok {
		t = time.Until(end)
	}
	out, code, e := runSSH(&clone, cmd, t)
	if e != nil || code != 0 {
		return "", errors.New("remote operation failed; outcome requires inspection")
	}
	return out, nil
}
func (nativeSSH) Probe(c context.Context, i *Instance, gateway string) (nativeProbe, error) {
	out, e := nativeRun(c, i, `set -eu
 test "$(uname -s)" = Linux; test "$(id -u)" = 0; test -d /run/systemd/system
 for tool in systemctl ss flock tar sha256sum curl stat; do command -v "$tool" >/dev/null; done
 status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --connect-timeout 3 --max-time 5 --proto '=https,http' `+quoteShellArg(strings.TrimRight(gateway, "/")+"/health")+`)
 test "$status" = 200
 uname -m; cat /etc/machine-id
 if test -f /etc/ssh/ssh_host_ed25519_key.pub; then cat /etc/ssh/ssh_host_ed25519_key.pub; else cat /etc/ssh/ssh_host_rsa_key.pub; fi
 awk '/MemAvailable:/ {printf "%.0f\n", $2/1024}' /proc/meminfo
 df -Pm /var/lib | awk 'NR==2 {print $4}'
 if getconf GNU_LIBC_VERSION >/dev/null 2>&1; then printf 'ABI=gnu\n'; elif ls /lib/ld-musl-*.so.1 >/dev/null 2>&1; then printf 'ABI=musl\n'; else printf 'ABI=unknown\n'; fi
 if command -v ldconfig >/dev/null; then ldconfig -p 2>/dev/null | awk '/=>/ {print "LIB=" $1}' | sort -u; fi
`)
	if e != nil {
		return nativeProbe{}, e
	}
	return parseNativeTargetProbe(out)
}
func parseNativeTargetProbe(out string) (nativeProbe, error) {
	v := strings.Split(strings.TrimSpace(out), "\n")
	if len(v) < 6 || len(v) > 2054 || len(v[1]) != 32 || v[2] == "" {
		return nativeProbe{}, errors.New("invalid host identity probe")
	}
	platform := ""
	switch v[0] {
	case "x86_64":
		platform = "linux-amd64"
	case "aarch64", "arm64":
		platform = "linux-arm64"
	default:
		return nativeProbe{}, errors.New("unsupported architecture")
	}
	m, e := strconv.ParseInt(v[3], 10, 64)
	if e != nil {
		return nativeProbe{}, e
	}
	disk, e := strconv.ParseInt(v[4], 10, 64)
	if e != nil {
		return nativeProbe{}, e
	}
	abi := strings.TrimPrefix(v[5], "ABI=")
	if abi != "gnu" && abi != "musl" {
		return nativeProbe{}, errors.New("unsupported host ABI")
	}
	var libraries []string
	for _, line := range v[6:] {
		if !strings.HasPrefix(line, "LIB=") {
			return nativeProbe{}, errors.New("invalid library inventory")
		}
		lib := strings.TrimPrefix(line, "LIB=")
		if !nativeLibraryName(lib) {
			return nativeProbe{}, errors.New("invalid library inventory")
		}
		libraries = append(libraries, lib)
	}
	return nativeProbe{ABI: abi, SystemLibraries: libraries, Platform: platform, HostKey: "linux-systemd:" + nativeHash(v[1]+"\n"+v[2]), MemoryMB: m, DiskMB: disk}, nil
}

// Remote locking and immutable ownership markers protect lost-response recovery.
func nativeOwned(d nativeRecord, create bool) string {
	s := `set -eu; umask 077
 IFS= read -r machine < /etc/machine-id
 if test -f /etc/ssh/ssh_host_ed25519_key.pub; then IFS= read -r hostpub < /etc/ssh/ssh_host_ed25519_key.pub; else IFS= read -r hostpub < /etc/ssh/ssh_host_rsa_key.pub; fi
 identity=$(printf '%s\n%s' "$machine" "$hostpub" | sha256sum); identity=${identity%% *}
 test "linux-systemd:$identity" = ` + quoteShellArg(d.Execution.HostKey) + `

 root=` + quoteShellArg(d.ReleaseDir) + `
 data=` + quoteShellArg(d.DataDir) + `
 unit=` + quoteShellArg(d.Unit) + `
 unitfile=/etc/systemd/system/"$unit"
 owner=` + quoteShellArg(d.Fingerprint) + `
 for p in /var/lib/apteva-native "$(dirname "$(dirname "$data")")" "$(dirname "$data")" "$(dirname "$root")" "$root" "$data"; do test ! -L "$p"; done
 `
	if create {
		s += `mkdir -p "$root" "$(dirname "$data")"
 test ! -L "$(dirname "$data")/.state-lock"
 exec 8>"$(dirname "$data")/.state-lock"; flock -x 8
 test ! -e "$(dirname "$data")/.restore-intent"
 mkdir -p "$data"
 exec 9>"$root/.lock"; flock -x 9
 if test -e "$root/.owner"; then test "$(cat "$root/.owner")" = "$owner"; else
  test -z "$(find "$root" -mindepth 1 -maxdepth 1 ! -name .lock -print -quit)"
  printf '%s' "$owner" > "$root/.owner"
 fi
 `
	} else {
		s += `test ! -L "$(dirname "$data")/.state-lock"
 exec 8>"$(dirname "$data")/.state-lock"; flock -x 8
 test -f "$root/.owner"; test "$(cat "$root/.owner")" = "$owner"
 exec 9>"$root/.lock"; flock -x 9
 `
	}
	return s + `if test -e "$unitfile"; then test ! -L "$unitfile"; test "$(head -n 1 "$unitfile")" = "# apteva-native-owner:$owner"; fi
 `
}
func nativeListener(d nativeRecord, owned bool) string {
	var s string
	for _, p := range d.Execution.Ports {
		flags := "-lntp"
		if p.Protocol == "udp" {
			flags = "-lnup"
		}
		s += `listeners=$(ss -H ` + flags + ` 'sport = :` + strconv.Itoa(p.Start) + `')
 `
		if owned {
			s += `printf '%s' "$listeners" | grep -F "pid=$pid," >/dev/null
 `
		} else {
			s += `test -z "$listeners"
 `
		}
	}
	return s
}

// No start intent means this deployment never acquired any listener. Foreign
// listeners must not prevent cleanup of a failed preparation or be stopped.
func nativeReleasedListeners(d nativeRecord) string {
	return `if test -e "$root/.start-intent"; then
 :
 ` + nativeListener(d, false) + `fi
 `
}

// Read systemd's related properties from one consistent response.
func nativeUnitProperties() string {
	return `properties=$(systemctl show "$unit" -p ActiveState -p MainPID -p ControlGroup -p FragmentPath)
 state=; pid=; cg=; fragment=
 while IFS='=' read -r property value; do
  case "$property" in ActiveState) state=$value;; MainPID) pid=$value;; ControlGroup) cg=$value;; FragmentPath) fragment=$value;; esac
 done <<APTEVA_PROPERTIES
$properties
APTEVA_PROPERTIES
 `
}
func nativeInactiveProperties() string {
	return `case "$state" in inactive|failed|"") ;; *) exit 41;; esac
 test "$pid" = 0 -o -z "$pid"
 if test -n "$cg"; then
  for tasks in "/sys/fs/cgroup$cg/cgroup.procs" "/sys/fs/cgroup/systemd$cg/tasks"; do
   if test -f "$tasks"; then test -z "$(cat "$tasks")"; fi
  done
 fi
 `
}
func nativeUnitInactive() string { return nativeUnitProperties() + nativeInactiveProperties() }
func (nativeSSH) Prepare(c context.Context, i *Instance, d nativeRecord, dir string) error {
	out, e := nativeRun(c, i, nativeOwned(d, true)+`test ! -e "$root/.stopped"
 if test -e "$root/.prepared"; then cd "$root"; sha256sum -c .sha256 >/dev/null; stat -Lc '%d:%i:%s:%y:%z' `+quoteShellArg(d.Artifact.Executable)+` > .exe-stat; printf 'prepared'; exit 0; fi
 `+nativeUnitInactive()+nativeListener(d, false)+`printf 'upload'`)
	if e != nil {
		return e
	}
	if strings.TrimSpace(out) == "prepared" {
		return nil
	}
	client, e := dialAdministrativeSSH(i, 10*time.Second)
	if e != nil {
		return e
	}
	defer client.Close()
	stop := context.AfterFunc(c, func() { _ = client.Close() })
	defer stop()
	t := 5 * time.Minute
	if end, ok := c.Deadline(); ok {
		t = time.Until(end)
	}
	session, cleanup, e := boundedSSHSession(client, t)
	if e != nil {
		return e
	}
	defer cleanup()
	cmd := nativeOwned(d, false) + `test ! -e "$root/.stopped"; test ! -e "$root/.prepared"
 ` + nativeUnitInactive() + `tar -xf - -C "$root"
 cd "$root"; sha256sum -c .sha256 >/dev/null
 chmod 0755 ` + quoteShellArg(d.Artifact.Executable) + `
 stat -Lc '%d:%i:%s:%y:%z' ` + quoteShellArg(d.Artifact.Executable) + ` > .exe-stat
 touch .prepared
 `
	writer := &lockedWriter{max: 4096}
	session.Stdout = writer
	session.Stderr = writer
	stdin, e := session.StdinPipe()
	if e != nil {
		return e
	}
	if e = session.Start(cmd); e != nil {
		return e
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return e
	}
	defer root.Close()
	tw := tar.NewWriter(stdin)
	for _, f := range d.Artifact.Files {
		if e = tw.WriteHeader(&tar.Header{Name: f.Path, Mode: 0644, Size: f.Size, Typeflag: tar.TypeReg}); e != nil {
			break
		}
		file, err := root.Open(f.Path)
		if err != nil {
			e = err
			break
		}
		_, e = io.CopyN(tw, file, f.Size)
		file.Close()
		if e != nil {
			break
		}
	}
	if e == nil {
		var sums strings.Builder
		for _, f := range d.Artifact.Files {
			fmt.Fprintf(&sums, "%s  %s\n", f.SHA256, f.Path)
		}
		e = tw.WriteHeader(&tar.Header{Name: ".sha256", Mode: 0600, Size: int64(sums.Len()), Typeflag: tar.TypeReg})
		if e == nil {
			_, e = io.WriteString(tw, sums.String())
		}
	}
	closeErr := tw.Close()
	stdin.Close()
	waitErr := session.Wait()
	if e != nil || closeErr != nil || waitErr != nil {
		return errors.New("artifact transfer outcome unknown; inspect before retry")
	}
	return nil
}
func nativeEnvironmentText(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, k := range keys {
		v := strings.ReplaceAll(strings.ReplaceAll(env[k], `\`, `\\`), `"`, `\"`)
		fmt.Fprintf(&out, "%s=\"%s\"\n", k, v)
	}
	return out.String()
}
func nativeUnitText(d nativeRecord) string {
	return fmt.Sprintf(`# apteva-native-owner:%s
[Unit]
Description=Apteva native deployment %s
After=network-online.target
[Service]
Type=simple
ExecStart=%s/%s
WorkingDirectory=%s
EnvironmentFile=%s/.environment
KillMode=control-group
TimeoutStopSec=10
Restart=no
UMask=0077
`, d.Fingerprint, nativeHash(d.ID)[:12], d.ReleaseDir, d.Artifact.Executable, d.ReleaseDir, d.ReleaseDir)
}

// Process credentials travel over encrypted SSH stdin, never in a command line.
func nativeRunInput(c context.Context, i *Instance, cmd string, input io.Reader) error {
	client, e := dialAdministrativeSSH(i, 10*time.Second)
	if e != nil {
		return e
	}
	defer client.Close()
	stop := context.AfterFunc(c, func() { _ = client.Close() })
	defer stop()
	t := 20 * time.Second
	if end, ok := c.Deadline(); ok {
		t = time.Until(end)
	}
	session, cleanup, e := boundedSSHSession(client, t)
	if e != nil {
		return e
	}
	defer cleanup()
	writer := &lockedWriter{max: 4096}
	session.Stdout = writer
	session.Stderr = writer
	session.Stdin = input
	if e = session.Run(cmd); e != nil {
		return errors.New("remote start outcome unknown; inspect before retry")
	}
	return nil
}
func (nativeSSH) Start(c context.Context, i *Instance, d nativeRecord, env map[string]string) error {
	var input bytes.Buffer
	tw := tar.NewWriter(&input)
	for _, f := range []struct{ name, body string }{{".environment", nativeEnvironmentText(env)}, {".unit", nativeUnitText(d)}} {
		if e := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		if _, e := io.WriteString(tw, f.body); e != nil {
			return e
		}
	}
	if e := tw.Close(); e != nil {
		return e
	}
	cmd := nativeOwned(d, false) + `test ! -e "$(dirname "$data")/.restore-intent"
 test -e "$root/.prepared"; test ! -e "$root/.stopped"
 cd "$root"; sha256sum -c .sha256 >/dev/null
 if test -e "$root/.start-intent"; then exit 0; fi
 ` + nativeUnitInactive() + nativeListener(d, false) + `
 test "$(systemctl show "$unit" -p LoadState --value)" = not-found -o -f "$unitfile"
 tar -xf - -C "$root"
 chmod 0600 "$root/.environment" "$root/.unit"
 mv "$root/.unit" "$unitfile"
 systemctl daemon-reload
 touch "$root/.start-intent"
 systemctl start "$unit"
 `
	return nativeRunInput(c, i, cmd, &input)
}
func (s nativeSSH) Inspect(c context.Context, i *Instance, d nativeRecord) (nativeObservation, error) {
	return s.InspectState(c, i, d, nil, false)
}

func (nativeSSH) Stop(c context.Context, i *Instance, d nativeRecord) error {
	_, e := nativeRun(c, i, nativeOwned(d, true)+`
 if test -f "$unitfile"; then
  systemctl daemon-reload
  test "$(systemctl show "$unit" -p FragmentPath --value)" = "$unitfile"
  systemctl stop "$unit"
 else test "$(systemctl show "$unit" -p LoadState --value)" = not-found; fi
 `+nativeUnitInactive()+nativeReleasedListeners(d)+`touch "$root/.stopped"`)
	return e
}
func (nativeSSH) Delete(c context.Context, i *Instance, d nativeRecord) error {
	_, e := nativeRun(c, i, nativeOwned(d, false)+`test -f "$root/.stopped"
 `+nativeUnitInactive()+nativeReleasedListeners(d)+`
 if test -f "$unitfile"; then rm -- "$unitfile"; systemctl daemon-reload; fi
 find "$root" -mindepth 1 -maxdepth 1 ! -name .owner ! -name .lock ! -name .stopped ! -name .deleted -exec rm -rf -- {} +
 touch "$root/.deleted"
 `)
	return e
}
func (nativeSSH) Logs(c context.Context, i *Instance, d nativeRecord, cursor string, limit int) ([]string, string, error) {
	if len(cursor) > 512 || strings.ContainsAny(cursor, "\x00\r\n") {
		return nil, "", errors.New("invalid log cursor")
	}
	cmd := nativeOwned(d, false) + `test -f "$unitfile"
 journalctl --unit "$unit" --no-pager --output cat --lines ` + strconv.Itoa(min(100, max(1, limit))) + ` --show-cursor`
	if cursor != "" {
		cmd += " --after-cursor " + quoteShellArg(cursor)
	}
	out, e := nativeRun(c, i, cmd)
	if e != nil {
		return nil, "", e
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	next := ""
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "-- cursor: ") {
		next = strings.TrimPrefix(lines[len(lines)-1], "-- cursor: ")
		lines = lines[:len(lines)-1]
	}
	return lines, next, nil
}
