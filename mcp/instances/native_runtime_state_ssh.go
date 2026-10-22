package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func nativeDataInactive(peers []nativeRecord) string {
	var out strings.Builder
	for _, peer := range peers {
		out.WriteString("unit=" + quoteShellArg(peer.Unit) + "\nunitfile=/etc/systemd/system/\"$unit\"\n")
		out.WriteString("if test -e \"$unitfile\"; then test ! -L \"$unitfile\"; test \"$(head -n 1 \"$unitfile\")\" = " + quoteShellArg("# apteva-native-owner:"+peer.Fingerprint) + "; fi\n")
		out.WriteString(nativeUnitInactive())
	}
	return out.String()
}
func nativeDataSafe(policies ...nativeSnapshotLimits) string {
	limits := defaultNativeSnapshotLimits()
	if len(policies) > 0 {
		limits = policies[0]
	}
	// Do not print filenames or materialize directory contents in memory. The END
	// sentinel also detects find failures, even on shells without pipefail.
	return `test -d "$data" && test ! -L "$data" || exit 74
 marker="$data/.apteva-native-restore"
 if test -e "$marker" || test -L "$marker"; then
 test ! -L "$marker" && test -f "$marker" && test "$(stat -c %h "$marker")" -eq 1 && test "$(wc -c < "$marker")" -le 256 || exit 74
 fi
 state_summary=$( { find "$data" -mindepth 1 ! -path "$marker" -printf '%y %s %n\n' && printf 'END\n'; } | awk '
 $1=="END" {complete=1; next}
 {entries++; if ($1!="f" && $1!="d") bad=1; if ($1=="f") {bytes+=$2; if ($3>1) bad=1}; if (bytes>` + strconv.FormatInt(limits.StateBytes, 10) + `) {large=1; exit}; if (entries>` + strconv.FormatInt(nativeEntryLimit(limits), 10) + `) {bad=1; exit}; if (bad) exit}
 END {if (large) exit 73; if (!complete || bad) exit 74; printf "%.0f %.0f\n", bytes,entries}') || { code=$?; exit "$code"; }
 state_bytes=${state_summary%% *}
 state_entries=${state_summary#* }
 `
}
func (nativeSSH) StateSpace(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord) (nativeStateSpace, error) {
	pendingCheck := `test ! -e "$(dirname "$data")/.restore-intent"
 reclaimable=0
 `
	if d.Restore != nil {
		pendingCheck = nativeRestoreVariables(d) + `reclaimable=0
 if test -e "$intent"; then
  test -f "$intent"; test "$(wc -c < "$intent")" -le 256; test "$(cat "$intent")" = "$expected"; test ! -e "$backup"
  if test -d "$stage"; then reclaimable=$(du -sk -- "$stage" | awk '{printf "%.0f",$1*1024}'); fi
 fi
 `
	}
	cmd := nativeOwned(d, false) + pendingCheck + nativeDataInactive(peers) + nativeDataSafe(nativeLimitsFromContext(c)) + `parent=$(dirname "$data")
 available=$(df -Pk "$parent" | awk 'NR==2 {printf "%.0f", $4*1024}')
 test -n "$available"
 allocation=$(stat -f -c %S "$parent")
 test "$allocation" -ge 512; test "$allocation" -le 1048576
 printf '{"bytes":%s,"entries":%s,"available_bytes":%s,"allocation_unit":%s}' "$state_bytes" "$state_entries" "$((available+reclaimable))" "$allocation"
 `
	out := &lockedWriter{max: 4096}
	if e := nativeStateSSH(c, i, cmd, nil, out); e != nil {
		return nativeStateSpace{}, e
	}
	var space nativeStateSpace
	if json.Unmarshal([]byte(out.String()), &space) != nil || space.Bytes < 0 || space.Entries < 0 || space.Available < 0 || space.AllocationUnit < 512 || space.AllocationUnit > 1048576 {
		return space, errors.New("invalid state space response")
	}
	return space, nil
}

func nativeOutput(c context.Context, i *Instance, cmd string, out io.Writer) error {
	return nativeStateSSH(c, i, cmd, nil, out)
}

// State transfer has its own session: cancellation never closes a lifecycle or
// inspection transport. Only stable codes are returned; raw remote output is private.
func nativeStateSSH(c context.Context, i *Instance, cmd string, input io.Reader, out io.Writer) error {
	client, e := dialAdministrativeSSH(i, 10*time.Second)
	if e != nil {
		return e
	}
	defer client.Close()
	stop := context.AfterFunc(c, func() { _ = client.Close() })
	defer stop()
	timeout := 2 * time.Hour
	if end, ok := c.Deadline(); ok {
		timeout = time.Until(end)
	}
	session, cleanup, e := boundedSSHSession(client, timeout)
	if e != nil {
		return e
	}
	defer cleanup()
	stderr := &lockedWriter{max: 4096}
	session.Stdout = out
	session.Stderr = stderr
	session.Stdin = input
	if e = session.Run("export LC_ALL=C\n" + cmd); e != nil {
		var exit *ssh.ExitError
		if errors.As(e, &exit) {
			switch exit.ExitStatus() {
			case 72:
				return errNativeDiskSpace
			case 73:
				return errNativeStateTooLarge
			case 74:
				return errNativeArchiveInvalid
			}
		}
		// GNU tar reports ENOSPC/EDQUOT on stderr without a dedicated status.
		if strings.Contains(stderr.String(), "No space left on device") || strings.Contains(stderr.String(), "Disk quota exceeded") {
			return errNativeDiskSpace
		}
		return fmt.Errorf("remote state outcome unknown; inspect before retry: %w", e)
	}
	return nil
}

func (nativeSSH) Snapshot(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord, out io.Writer) error {
	cmd := nativeOwned(d, false) + `test ! -e "$(dirname "$data")/.restore-intent"
 ` + nativeDataInactive(peers) + nativeDataSafe(nativeLimitsFromContext(c)) + `tar --sort=name --exclude=./.apteva-native-restore -cf - -C "$data" .`
	return nativeOutput(c, i, cmd, out)
}
func nativeRestoreVariables(d nativeRecord) string {
	expected := d.Fingerprint + ":" + d.Restore.Key + ":" + d.Restore.SHA256
	return `parent=$(dirname "$data")
 stage="$parent/.restore-stage"
 backup="$parent/.restore-previous"
 intent="$parent/.restore-intent"
 expected=` + quoteShellArg(expected) + `
 for p in "$stage" "$backup" "$intent"; do test ! -L "$p"; done
 marker_matches() {
  test -d "$1" && test ! -L "$1/` + nativeStateMarker + `" && test -f "$1/` + nativeStateMarker + `" && test "$(wc -c < "$1/` + nativeStateMarker + `")" -le 256 && test "$(cat "$1/` + nativeStateMarker + `")" = "$expected"
 }
 `
}

// A receipt is placed INSIDE the staged data before any rename. The shared
// intent blocks every start until recovery has completed both renames/cleanup.
func nativeRestoreRecoverBody(d nativeRecord, peers []nativeRecord) string {
	return nativeRestoreVariables(d) + `
 if test -e "$intent"; then
  test -f "$intent"; test "$(wc -c < "$intent")" -le 256; test "$(cat "$intent")" = "$expected"
  ` + nativeDataInactive(peers) + `
  if ! marker_matches "$data" && marker_matches "$stage"; then
   if test -e "$data"; then test ! -e "$backup"; mv -- "$data" "$backup"; sync; else test -d "$backup"; fi
   mv -- "$stage" "$data"; sync
  fi
  if marker_matches "$data"; then
   rm -rf -- "$backup" "$stage"
   rm -- "$intent"; sync
  fi
 fi
 `
}
func nativeRestoreRecover(d nativeRecord, peers []nativeRecord) string {
	return nativeRestoreRecoverBody(d, peers) + `if marker_matches "$data"; then printf '%s' ` + quoteShellArg(d.Restore.SHA256) + `; exit 0; fi
 `
}
func (nativeSSH) RestoreStatus(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord) (string, error) {
	if d.Restore == nil {
		return "", nil
	}
	out, e := nativeRun(c, i, nativeOwned(d, false)+nativeRestoreRecover(d, peers))
	if e != nil {
		return "", e
	}
	sha := strings.TrimSpace(out)
	if sha != "" && !nativeDigest(sha) {
		return "", errors.New("invalid restore receipt")
	}
	return sha, nil
}
func (nativeSSH) Restore(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord, input io.Reader) error {
	if d.Restore == nil || !nativeDigest(d.Restore.SHA256) || !nativeDigest(d.Restore.Key) {
		return errors.New("restore intent missing")
	}
	entries := d.Restore.Entries
	if entries == 0 {
		entries = nativeStateMaxEntries
	} // compatibility with durable pre-limit receipts
	cmd := nativeOwned(d, false) + nativeRestoreRecover(d, peers) + nativeDataInactive(peers) + nativeDataSafe(nativeLimitsFromContext(c)) + `
 if test -e "$intent"; then test "$(cat "$intent")" = "$expected"; test ! -e "$backup"; fi
 test ! -e "$backup"
 rm -rf -- "$stage"
 ` + nativeStateRestoreDiskCheck(d.Restore.Bytes, entries) + `
 test ! -L "$parent/.restore-intent.new"
 printf '%s' "$expected" > "$parent/.restore-intent.new"
 sync; mv -- "$parent/.restore-intent.new" "$intent"; sync
 mkdir -m 0700 "$stage"
 tar -xf - -C "$stage"
 ` + // Only a successful extraction receives a commit marker.
		`printf '%s' "$expected" > "$stage/` + nativeStateMarker + `"
 sync
 mv -- "$data" "$backup"; sync
 mv -- "$stage" "$data"; sync
 rm -rf -- "$backup"
 rm -- "$intent"; sync
 `
	limits := nativeLimitsFromContext(c)
	if d.Restore.Bytes < 0 || d.Restore.Bytes > limits.StateBytes+nativeStateTarOverhead {
		return errNativeStateTooLarge
	}
	return nativeStateSSH(c, i, cmd, input, io.Discard)
}

func nativeStateRestoreDiskCheck(bytes, entries int64) string {
	return `allocation=$(stat -f -c %S "$parent")
 test "$allocation" -ge 512; test "$allocation" -le 1048576
 available=$(df -Pk "$parent" | awk 'NR==2 {printf "%.0f",$4*1024}')
 test -n "$available"
 needed=$((` + strconv.FormatInt(bytes, 10) + ` + ` + strconv.FormatInt(entries, 10) + ` * allocation + ` + strconv.FormatInt(nativeStateReserve, 10) + `))
 test "$available" -ge "$needed" || exit 72
 `
}
