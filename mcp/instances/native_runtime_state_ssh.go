package main

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
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
func nativeDataSafe() string {
	return `test -d "$data"; test ! -L "$data"
 test -z "$(find "$data" -mindepth 1 ! -type f ! -type d -print -quit)"
 test -z "$(find "$data" -type f -links +1 -print -quit)"
 find "$data" -type f -printf '%s\n' | awk '{n++; total+=$1; if (total>536870912 || n>100000) exit 1}'
 test "$(find "$data" -mindepth 1 -printf 'x\n' | wc -l)" -le 100000
 `
}

func nativeOutput(c context.Context, i *Instance, cmd string, out io.Writer) error {
	client, e := dialAdministrativeSSH(i, 10*time.Second)
	if e != nil {
		return e
	}
	defer client.Close()
	stop := context.AfterFunc(c, func() { _ = client.Close() })
	defer stop()
	timeout := 20 * time.Minute
	if end, ok := c.Deadline(); ok {
		timeout = time.Until(end)
	}
	session, cleanup, e := boundedSSHSession(client, timeout)
	if e != nil {
		return e
	}
	defer cleanup()
	session.Stdout = out
	session.Stderr = &lockedWriter{max: 4096}
	if e = session.Run(cmd); e != nil {
		return errors.New("remote state transfer failed; inspect before retry")
	}
	return nil
}
func (nativeSSH) Snapshot(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord, out io.Writer) error {
	cmd := nativeOwned(d, false) + `test ! -e "$(dirname "$data")/.restore-intent"
 ` + nativeDataInactive(peers) + nativeDataSafe() + `tar --sort=name --exclude=./.apteva-native-restore -cf - -C "$data" .`
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
	cmd := nativeOwned(d, false) + nativeRestoreRecover(d, peers) + nativeDataInactive(peers) + nativeDataSafe() + `
 if test -e "$intent"; then test "$(cat "$intent")" = "$expected"; test ! -e "$backup"; fi
 test ! -e "$backup"
 available=$(df -Pk "$parent" | awk 'NR==2 {print $4}')
 test "$available" -ge ` + strconv.FormatInt((d.Restore.Bytes+(16<<20)+1023)/1024, 10) + `
 rm -rf -- "$stage"
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
	return nativeRunInput(c, i, cmd, input)
}
