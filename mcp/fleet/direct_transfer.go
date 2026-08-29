package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	sqliteRsyncVersion      = "3.53.4"
	sqliteRsyncVersionCode  = "3530400"
	sqliteRsyncSourceSHA3   = "b834d474b9b393d85a9e3ee4cc11f1329e007e9376a424ee740796f5c4bda3a8"
	sqliteRsyncAmalgamSHA3  = "628a44cfe82c66aed1ccbbe85a562d2e33ebe64b3288981ed76285612227934e"
	remoteTransferToolsRoot = remoteFleetRoot + "/tools"
	remoteSQLiteRsync       = remoteTransferToolsRoot + "/sqlite3_rsync"
)

// streamRemoteTenantToRemote deliberately never relays tenant bytes through
// the Fleet controller. Ordinary files go directly from source to target via
// rsync. Every live SQLite database is then copied with sqlite3_rsync so the
// replica is a transactionally consistent snapshot without stopping source.
// The deterministic target-side stage survives interruption and is reused by
// rsync/sqlite3_rsync on the next tenant_clone attempt.
func (a *App) streamRemoteTenantToRemote(ctx *sdk.AppCtx, sourceHost, targetHost fleetHost, sourceDir, targetDir, slug string, progress tenantTransferProgress) error {
	if sourceHost.InstanceID <= 0 || targetHost.InstanceID <= 0 {
		return errors.New("direct hosted transfer requires two remote instances")
	}
	if err := a.ensureHostedDirectTransferTools(ctx, sourceHost.InstanceID); err != nil {
		return fmt.Errorf("source transfer tools: %w", err)
	}
	if sourceHost.InstanceID != targetHost.InstanceID {
		if err := a.ensureHostedDirectTransferTools(ctx, targetHost.InstanceID); err != nil {
			return fmt.Errorf("target transfer tools: %w", err)
		}
	}
	stageDir := filepath.Join(remoteFleetRoot, ".clone-transfer-"+shellSafeSlug(slug))
	if progress != nil {
		progress("preflight", "direct transfer tools ready; destination staging is resumable")
	}
	if sourceHost.InstanceID == targetHost.InstanceID {
		script := sameHostDirectTransferScript(sourceDir, targetDir, stageDir)
		if err := runHostedTransferJobWithProgress(ctx, sourceHost.InstanceID, script, slug, progress); err != nil {
			return err
		}
		return a.finalizeRemoteTransfer(ctx, targetHost.InstanceID, targetDir, stageDir)
	}

	target, err := hostedSSHTarget(targetHost)
	if err != nil {
		return err
	}
	transferID, err := randomHex(12)
	if err != nil {
		return err
	}
	sourceKeyDir := filepath.Join(remoteFleetRoot, ".transfers", "direct-"+transferID)
	pub, code, err := instanceRunCommand(ctx, sourceHost.InstanceID, fmt.Sprintf(`set -eu
KEYDIR=%s
mkdir -p "$KEYDIR"
chmod 700 "$KEYDIR"
ssh-keygen -q -t ed25519 -N '' -C %s -f "$KEYDIR/id_ed25519"
cat "$KEYDIR/id_ed25519.pub"`, sh(sourceKeyDir), sh("fleet-direct-"+transferID)), 30)
	if err != nil || code != 0 {
		return fmt.Errorf("create ephemeral source transfer key: %w (exit %d): %s", err, code, strings.TrimSpace(pub))
	}
	pub = strings.TrimSpace(pub)
	if !validTransferPublicKey(pub, transferID) {
		return errors.New("source returned an invalid ephemeral transfer public key")
	}
	defer func() {
		_, _, _ = instanceRunCommand(ctx, sourceHost.InstanceID, fmt.Sprintf(`rm -rf -- %s`, sh(sourceKeyDir)), 15)
	}()

	hostKey, code, err := instanceRunCommand(ctx, targetHost.InstanceID, `set -eu
for key in /etc/ssh/ssh_host_ed25519_key.pub /etc/ssh/ssh_host_ecdsa_key.pub /etc/ssh/ssh_host_rsa_key.pub; do
  if [ -r "$key" ]; then cat "$key"; exit 0; fi
done
echo "no readable SSH host public key" >&2
exit 1`, 15)
	if err != nil || code != 0 {
		return fmt.Errorf("read trusted target SSH host key: %w (exit %d): %s", err, code, strings.TrimSpace(hostKey))
	}
	knownHost, err := knownHostsLine(target.host, target.port, hostKey)
	if err != nil {
		return err
	}

	wrapperPath := filepath.Join(remoteFleetRoot, ".transfers", "accept-"+transferID+".py")
	targetSetup := targetTransferSetupScript(stageDir, wrapperPath, pub, transferID)
	out, code, err := instanceRunCommand(ctx, targetHost.InstanceID, targetSetup, 30)
	if err != nil || code != 0 {
		return fmt.Errorf("authorize target transfer: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	defer func() {
		cleanup := targetTransferCleanupScript(wrapperPath, transferID)
		_, _, _ = instanceRunCommand(ctx, targetHost.InstanceID, cleanup, 15)
	}()

	script := crossHostDirectTransferScript(sourceDir, stageDir, sourceKeyDir, target, knownHost)
	if err := runHostedTransferJobWithProgress(ctx, sourceHost.InstanceID, script, slug, progress); err != nil {
		return fmt.Errorf("direct source-to-target transfer: %w", err)
	}
	return a.finalizeRemoteTransfer(ctx, targetHost.InstanceID, targetDir, stageDir)
}

type hostedSSHDestination struct {
	user string
	host string
	port int
}

func hostedSSHTarget(h fleetHost) (hostedSSHDestination, error) {
	if h.Info == nil {
		return hostedSSHDestination{}, errors.New("target instance metadata required")
	}
	user := strings.TrimSpace(h.Info.SSHUser)
	if user == "" {
		user = "root"
	}
	host := strings.TrimSpace(h.Info.SSHHost)
	if host == "" {
		host = strings.TrimSpace(h.Info.PublicIPv4)
	}
	port := h.Info.SSHPort
	if port == 0 {
		port = 22
	}
	if !safeSSHAtom(user) || !safeSSHHost(host) || port < 1 || port > 65535 {
		return hostedSSHDestination{}, errors.New("target instance returned unsafe SSH connection metadata")
	}
	return hostedSSHDestination{user: user, host: host, port: port}, nil
}

func safeSSHAtom(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func safeSSHHost(v string) bool {
	if net.ParseIP(v) != nil {
		return !strings.Contains(v, ":") // Instances currently exposes IPv4 SSH endpoints.
	}
	return safeSSHAtom(v) && !strings.HasPrefix(v, "-")
}

func validTransferPublicKey(raw, transferID string) bool {
	if strings.ContainsAny(raw, "\r\n") {
		return false
	}
	parts := strings.Fields(raw)
	return len(parts) == 3 && parts[0] == "ssh-ed25519" && parts[2] == "fleet-direct-"+transferID
}

func knownHostsLine(host string, port int, rawKey string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(rawKey))
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "ssh-") && !strings.HasPrefix(parts[0], "ecdsa-") {
		return "", errors.New("target returned an invalid SSH host public key")
	}
	name := host
	if port != 22 {
		name = "[" + host + "]:" + strconv.Itoa(port)
	}
	return name + " " + parts[0] + " " + parts[1], nil
}

func (a *App) ensureHostedDirectTransferTools(ctx *sdk.AppCtx, instanceID int64) error {
	out, code, err := instanceRunCommand(ctx, instanceID, hostedDirectTransferToolsScript(), 900)
	if err != nil || code != 0 {
		return fmt.Errorf("prepare direct transfer tools: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "FLEET_DIRECT_TRANSFER_READY ") {
		return errors.New("direct transfer tool preparation returned no readiness marker")
	}
	return nil
}

func hostedDirectTransferToolsScript() string {
	return fmt.Sprintf(`set -eu
TOOLS=%s
VERSION=%s
mkdir -p "$TOOLS"
need_packages=0
for cmd in rsync ssh ssh-keygen cc unzip openssl curl python3; do
  command -v "$cmd" >/dev/null 2>&1 || need_packages=1
done
if [ "$need_packages" -ne 0 ]; then
  if [ "$(id -u)" -eq 0 ]; then SUDO=""; elif command -v sudo >/dev/null 2>&1; then SUDO="sudo"; else echo "root or sudo required to install transfer tools" >&2; exit 126; fi
  if command -v apt-get >/dev/null 2>&1; then
    $SUDO apt-get update -qq
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq rsync openssh-client build-essential unzip openssl curl python3
  elif command -v apk >/dev/null 2>&1; then
    $SUDO apk add --no-cache rsync openssh-client build-base unzip openssl curl python3
  elif command -v dnf >/dev/null 2>&1; then
    $SUDO dnf install -y rsync openssh-clients gcc make unzip openssl curl python3
  elif command -v yum >/dev/null 2>&1; then
    $SUDO yum install -y rsync openssh-clients gcc make unzip openssl curl python3
  else
    echo "unsupported package manager for direct transfer tools" >&2
    exit 127
  fi
fi
if [ ! -x "$TOOLS/sqlite3_rsync" ] || [ ! -f "$TOOLS/sqlite3_rsync.version" ] || [ "$(cat "$TOOLS/sqlite3_rsync.version" 2>/dev/null || true)" != "$VERSION" ]; then
  BUILD=$(mktemp -d "$TOOLS/.sqlite-build-XXXXXX")
  cleanup_build() { rm -rf -- "$BUILD"; }
  trap cleanup_build EXIT
  curl -fsSL --retry 3 --connect-timeout 15 %s -o "$BUILD/src.zip"
  curl -fsSL --retry 3 --connect-timeout 15 %s -o "$BUILD/amalg.zip"
  src_hash=$(openssl dgst -sha3-256 "$BUILD/src.zip" | sed 's/^.*= //')
  amalg_hash=$(openssl dgst -sha3-256 "$BUILD/amalg.zip" | sed 's/^.*= //')
  [ "$src_hash" = %s ] || { echo "SQLite source checksum mismatch" >&2; exit 1; }
  [ "$amalg_hash" = %s ] || { echo "SQLite amalgamation checksum mismatch" >&2; exit 1; }
  unzip -q "$BUILD/src.zip" -d "$BUILD/src"
  unzip -q "$BUILD/amalg.zip" -d "$BUILD/amalg"
  cc -O2 -I "$BUILD/amalg/sqlite-amalgamation-%s" \
    -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_THREADSAFE=0 \
    -DSQLITE_OMIT_LOAD_EXTENSION -DSQLITE_OMIT_DEPRECATED \
    "$BUILD/src/sqlite-src-%s/tool/sqlite3_rsync.c" \
    "$BUILD/amalg/sqlite-amalgamation-%s/sqlite3.c" \
    -o "$BUILD/sqlite3_rsync" -ldl -lpthread -lm
  chmod 755 "$BUILD/sqlite3_rsync"
  mv "$BUILD/sqlite3_rsync" "$TOOLS/sqlite3_rsync"
  printf '%%s\n' "$VERSION" > "$TOOLS/sqlite3_rsync.version"
fi
printf 'FLEET_DIRECT_TRANSFER_READY sqlite=%%s rsync=%%s\n' "$VERSION" "$(rsync --version | sed -n '1p')"`,
		sh(remoteTransferToolsRoot), sh(sqliteRsyncVersion),
		"https://sqlite.org/2026/sqlite-src-"+sqliteRsyncVersionCode+".zip",
		"https://sqlite.org/2026/sqlite-amalgamation-"+sqliteRsyncVersionCode+".zip",
		sh(sqliteRsyncSourceSHA3), sh(sqliteRsyncAmalgamSHA3),
		sqliteRsyncVersionCode, sqliteRsyncVersionCode, sqliteRsyncVersionCode)
}

func targetTransferSetupScript(stageDir, wrapperPath, publicKey, transferID string) string {
	wrapper := targetTransferWrapper(stageDir)
	wrapperB64 := base64.StdEncoding.EncodeToString([]byte(wrapper))
	entry := fmt.Sprintf(`command="%s",no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding %s`, wrapperPath, publicKey)
	return fmt.Sprintf(`set -eu
STAGE=%s
WRAPPER=%s
TOKEN=%s
mkdir -p "$STAGE" "$(dirname "$WRAPPER")" "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
printf '%%s' %s | base64 -d > "$WRAPPER"
chmod 700 "$WRAPPER"
AUTH="$HOME/.ssh/authorized_keys"
touch "$AUTH"
chmod 600 "$AUTH"
grep -v -- "$TOKEN$" "$AUTH" > "$AUTH.fleet-new" || true
printf '%%s\n' %s >> "$AUTH.fleet-new"
mv "$AUTH.fleet-new" "$AUTH"`, sh(stageDir), sh(wrapperPath), sh("fleet-direct-"+transferID), sh(wrapperB64), sh(entry))
}

func targetTransferWrapper(stageDir string) string {
	return fmt.Sprintf(`#!/usr/bin/env python3
import os, shlex, sys
stage = %q
raw = os.environ.get("SSH_ORIGINAL_COMMAND", "")
try:
    argv = shlex.split(raw)
except ValueError:
    raise SystemExit("invalid transfer command")
if argv and argv[0].startswith("PATH="):
    argv = argv[1:]
if not argv:
    raise SystemExit("missing transfer command")
replica_index = argv.index("--replica") if "--replica" in argv else -1
dest_arg = argv[replica_index + 2] if replica_index >= 0 and len(argv) > replica_index + 2 else argv[-1]
dest = os.path.abspath(dest_arg)
if dest != stage and not dest.startswith(stage + os.sep):
    raise SystemExit("transfer destination outside clone stage")
if argv[0] == "rsync" and len(argv) >= 4 and argv[1] == "--server":
    os.execvp("rsync", argv)
if argv[0] == %q and "--replica" in argv:
    os.execv(argv[0], argv)
raise SystemExit("transfer command not allowed")
`, stageDir, remoteSQLiteRsync)
}

func targetTransferCleanupScript(wrapperPath, transferID string) string {
	return fmt.Sprintf(`set -eu
AUTH="$HOME/.ssh/authorized_keys"
if [ -f "$AUTH" ]; then
  grep -v -- %s "$AUTH" > "$AUTH.fleet-new" || true
  mv "$AUTH.fleet-new" "$AUTH"
  chmod 600 "$AUTH"
fi
rm -f -- %s`, sh("fleet-direct-"+transferID+"$"), sh(wrapperPath))
}

func crossHostDirectTransferScript(sourceDir, stageDir, sourceKeyDir string, target hostedSSHDestination, knownHost string) string {
	destination := target.user + "@" + target.host
	return fmt.Sprintf(`set -eu
SRC=%s
STAGE=%s
KEYDIR=%s
DEST=%s
PORT=%d
SQLITE_RSYNC=%s
test -d "$SRC"
printf '%%s\n' %s > "$KEYDIR/known_hosts"
cat > "$KEYDIR/ssh" <<EOF
#!/bin/sh
if [ "\${1:-}" = "-e" ] && [ "\${2:-}" = "none" ]; then shift 2; fi
exec ssh -i "$KEYDIR/id_ed25519" -p "$PORT" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$KEYDIR/known_hosts" -o ConnectTimeout=15 "\$@"
EOF
chmod 700 "$KEYDIR/ssh"
echo 'phase=ordinary-files method=rsync resume=true'
rsync -rlptD --safe-links --delete-delay --partial --partial-dir=.fleet-rsync-partial --info=progress2 \
  -e "$KEYDIR/ssh" \
  --exclude='*.db' --exclude='*.sqlite' --exclude='*.sqlite3' \
  --exclude='*-wal' --exclude='*-shm' --exclude='*-journal' --exclude='fleet.pid' \
  "$SRC/" "$DEST:$STAGE/"
db_count=0
find "$SRC" -type f \( -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite3' \) -print | while IFS= read -r db; do
  rel=\${db#"$SRC"/}
  echo "phase=sqlite database=$rel method=sqlite3_rsync resume=true"
  "$SQLITE_RSYNC" -vv "$db" "$DEST:$STAGE/$rel" --ssh "$KEYDIR/ssh" --exe %s
  db_count=$((db_count + 1))
done
echo 'phase=complete ordinary=rsync sqlite=sqlite3_rsync'`, sh(sourceDir), sh(stageDir), sh(sourceKeyDir), sh(destination), target.port, sh(remoteSQLiteRsync), sh(knownHost), sh(remoteSQLiteRsync))
}

func sameHostDirectTransferScript(sourceDir, targetDir, stageDir string) string {
	return fmt.Sprintf(`set -eu
SRC=%s
DST=%s
STAGE=%s
SQLITE_RSYNC=%s
test -d "$SRC"
test ! -e "$DST"
mkdir -p "$STAGE"
echo 'phase=ordinary-files method=rsync resume=true'
rsync -rlptD --safe-links --delete-delay --partial --partial-dir=.fleet-rsync-partial --info=progress2 \
  --exclude='*.db' --exclude='*.sqlite' --exclude='*.sqlite3' \
  --exclude='*-wal' --exclude='*-shm' --exclude='*-journal' --exclude='fleet.pid' \
  "$SRC/" "$STAGE/"
find "$SRC" -type f \( -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite3' \) -print | while IFS= read -r db; do
  rel=\${db#"$SRC"/}
  echo "phase=sqlite database=$rel method=sqlite3_rsync resume=true"
  "$SQLITE_RSYNC" -v "$db" "$STAGE/$rel"
done
echo 'phase=complete ordinary=rsync sqlite=sqlite3_rsync'`, sh(sourceDir), sh(targetDir), sh(stageDir), sh(remoteSQLiteRsync))
}

func (a *App) finalizeRemoteTransfer(ctx *sdk.AppCtx, instanceID int64, targetDir, stageDir string) error {
	cmd := fmt.Sprintf(`set -eu
DST=%s
STAGE=%s
test ! -e "$DST"
test -d "$STAGE"
find "$STAGE" -type f \( -name '*-wal' -o -name '*-shm' -o -name '*-journal' -o -name 'fleet.pid' \) -delete
rm -rf -- "$STAGE/.fleet-rsync-partial"
mv "$STAGE" "$DST"`, sh(targetDir), sh(stageDir))
	out, code, err := instanceRunCommand(ctx, instanceID, cmd, 60)
	if err != nil || code != 0 {
		return fmt.Errorf("finalize direct transfer: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}
