package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Only the routine inspection path uses these connections and sessions. A
// persistent non-interactive shell avoids a new PAM session for every poll.
// Commands are serialized, framed with random markers, bounded and never
// replayed automatically. Cancellation can close only this inspection worker.
type nativeInspectionPool struct {
	mu      sync.Mutex
	closed  bool
	workers map[int64]*nativeInspectionWorker
	dial    func(*Instance, time.Duration) (*ssh.Client, error)
}
type nativeInspectionWorker struct {
	identity string
	client   *ssh.Client
	session  *ssh.Session
	input    io.WriteCloser
	output   *bufio.Reader
	idle     *time.Timer
	used     time.Time
}

func nativeInspectionIdentity(i *Instance) string {
	return nativeHash(nativeJSON([]any{i.Provider, i.ProviderConnectionID, i.ProviderID, i.SSHHost, i.PublicIPv4, i.PublicIPv6, i.SSHPort, i.SSHUser, i.SSHPrivateKey, i.SSHHostKey}))
}
func newNativeInspectionPool(c context.Context) *nativeInspectionPool {
	p := &nativeInspectionPool{workers: map[int64]*nativeInspectionWorker{}, dial: dialSSH}
	context.AfterFunc(c, p.close)
	return p
}
func (w *nativeInspectionWorker) close() {
	if w.idle != nil {
		w.idle.Stop()
	}
	_ = w.client.Close()
	_ = w.session.Close()
}
func (p *nativeInspectionPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for id, w := range p.workers {
		w.close()
		delete(p.workers, id)
	}
}
func (p *nativeInspectionPool) run(c context.Context, i *Instance, cmd string) (string, error) {
	// nativeBackend already serializes inspections. Keep independent callers safe
	// too; there is never unrelated work sharing a cancellable inspection worker.
	c, cancel := context.WithTimeout(c, 12*time.Second)
	defer cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("inspection transport closed")
	}
	if e := c.Err(); e != nil {
		return "", e
	}
	w := p.workers[i.ID]
	identity := nativeInspectionIdentity(i)
	if w != nil && w.identity != identity {
		w.close()
		delete(p.workers, i.ID)
		w = nil
	}
	if w == nil {
		if len(p.workers) >= 32 {
			var oldest int64
			var oldestTime time.Time
			for id, candidate := range p.workers {
				if oldestTime.IsZero() || candidate.used.Before(oldestTime) {
					oldest, oldestTime = id, candidate.used
				}
			}
			p.workers[oldest].close()
			delete(p.workers, oldest)
		}
		timeout := 10 * time.Second
		if end, ok := c.Deadline(); ok {
			timeout = min(timeout, time.Until(end))
		}
		client, e := p.dial(i, timeout)
		if e != nil {
			return "", errors.New("inspection SSH connection failed")
		}
		stop := context.AfterFunc(c, func() { _ = client.Close() })
		timer := time.AfterFunc(timeout, func() { _ = client.Close() })
		session, e := client.NewSession()
		if e != nil {
			timer.Stop()
			stop()
			client.Close()
			return "", e
		}
		input, e := session.StdinPipe()
		if e != nil {
			timer.Stop()
			stop()
			session.Close()
			client.Close()
			return "", e
		}
		output, e := session.StdoutPipe()
		if e != nil {
			timer.Stop()
			stop()
			session.Close()
			client.Close()
			return "", e
		}
		session.Stderr = io.Discard
		e = session.Start("sh")
		timer.Stop()
		stop()
		if e != nil {
			session.Close()
			client.Close()
			return "", e
		}
		w = &nativeInspectionWorker{identity: identity, client: client, session: session, input: input, output: bufio.NewReaderSize(output, 64<<10)}
		p.workers[i.ID] = w

	}
	return p.runWorker(c, i.ID, w, cmd)
}
func (p *nativeInspectionPool) runWorker(c context.Context, id int64, w *nativeInspectionWorker, cmd string) (out string, err error) {
	if w.idle != nil {
		w.idle.Stop()
	}
	stop := context.AfterFunc(c, func() { _ = w.client.Close() })
	defer stop()
	defer func() {
		if err != nil {
			w.close()
			delete(p.workers, id)
			return
		}
		w.used = time.Now()
		w.idle = time.AfterFunc(2*time.Minute, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.workers[id] == w && time.Since(w.used) >= 2*time.Minute {
				w.close()
				delete(p.workers, id)
			}
		})
	}()
	marker, e := newSSHExitMarker()
	if e != nil {
		return "", e
	}
	if _, e = io.WriteString(w.input, "sh -c "+quoteShellArg(cmd)+"; rc=$?; printf '\\n"+marker+"%s\\n' \"$rc\"\n"); e != nil {
		return "", errors.New("inspection worker disconnected")
	}
	var result strings.Builder
	for {
		line, e := w.output.ReadSlice('\n')
		if result.Len()+len(line) > 64<<10 {
			return "", errors.New("inspection output exceeds limit")
		}
		result.Write(line)
		if strings.HasPrefix(string(line), marker) {
			clean, code, ok := stripSSHExitMarker(result.String(), marker)
			if !ok || code != 0 {
				return "", errors.New("remote inspection failed")
			}
			return strings.TrimSpace(clean), nil
		}
		if e != nil {
			return "", errors.New("inspection worker disconnected")
		}
	}
}

func nativeInspectionCommand(d nativeRecord, peers []nativeRecord, recoverRestore bool, measure ...bool) string {
	cmd := `set -eu
 root=` + quoteShellArg(d.ReleaseDir) + `
 if test ! -e "$root/.owner"; then printf '{"state":"unknown"}\n'; exit 0; fi
 ` + nativeOwned(d, false) + `snapshot_sha=
 `
	if recoverRestore && d.Restore != nil {
		cmd += nativeRestoreRecoverBody(d, peers) + `if marker_matches "$data"; then snapshot_sha=` + quoteShellArg(d.Restore.SHA256) + `; fi
 `
	}
	// The cheap intent existence test is always kept, even after the receipt has
	// been verified. A different generation/controller must not hide a pending swap.
	cmd += `unit=` + quoteShellArg(d.Unit) + `
 unitfile=/etc/systemd/system/"$unit"
 restore_pending=false
 if test -e "$(dirname "$data")/.restore-intent"; then restore_pending=true; fi
 ` + nativeUnitProperties() + `
 inspect_execution() {

 if test -e "$root/.stopped"; then ` + nativeInactiveProperties() + nativeReleasedListeners(d) + `printf stopped; return; fi
 if test "$state" = active; then
  test "$restore_pending" = false
  test "$fragment" = "$unitfile"
  test "$pid" -gt 0
  test "$(readlink "/proc/$pid/exe")" = ` + quoteShellArg(d.ReleaseDir+"/"+d.Artifact.Executable) + `
  test "$(stat -Lc '%d:%i:%s:%y:%z' "/proc/$pid/exe")" = "$(cat "$root/.exe-stat")"
  ` + nativeListener(d, true) + `printf running; return
 fi
 if test -e "$root/.start-intent"; then printf unknown; return; fi
 if test -e "$root/.prepared"; then printf prepared; else printf preparing; fi
 }
 observed=$(inspect_execution)
 `
	cmd += nativeInspectionMetadataScript(len(measure) > 0 && measure[0])
	cmd += nativeResourceInspectionScript()
	cmd += `printf '{"process_resources":%s,"state":"%s","snapshot_sha256":"%s","restore_pending":%s,"state_bytes":%s,"state_entries":%s,"state_available_bytes":%s}\n' "$resource_json" "$observed" "$snapshot_sha" "$restore_pending" "$metadata_bytes" "$metadata_entries" "$metadata_available"
 `
	return cmd
}
func (s nativeSSH) InspectState(c context.Context, i *Instance, d nativeRecord, peers []nativeRecord, recoverRestore bool) (nativeObservation, error) {
	measure, _ := c.Value(nativeMetadataKey{}).(bool)
	cmd := nativeInspectionCommand(d, peers, recoverRestore, measure)
	var out string
	var e error
	if s.inspections != nil {
		out, e = s.inspections.run(c, i, cmd)
	} else {
		out, e = nativeRun(c, i, cmd)
	}
	if e != nil {
		return nativeObservation{}, e
	}
	var obs nativeObservation
	if len(out) > 4096 || json.Unmarshal([]byte(out), &obs) != nil {
		return obs, errors.New("invalid inspection response")
	}
	switch obs.State {
	case "running", "stopped", "prepared", "preparing", "unknown":
	default:
		return obs, fmt.Errorf("invalid inspection state")
	}
	if obs.SnapshotSHA256 != "" && !nativeDigest(obs.SnapshotSHA256) {
		return obs, errors.New("invalid inspection digest")
	}
	for _, n := range []*int64{obs.StateBytes, obs.StateEntries, obs.StateAvailableBytes} {
		if n != nil && *n < 0 {
			return obs, errors.New("invalid state metadata")
		}
	}
	obs.Prepared = obs.State == "prepared"
	obs.Stopped = obs.State == "stopped"
	return obs, nil
}

type nativeMetadataKey struct{}
type nativeMetadataCache struct {
	Identity       string
	At             time.Time
	Bytes, Entries *int64
}

func (b *nativeBackend) metadataContext(c context.Context, i *Instance, d nativeRecord, force bool) (context.Context, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cache, ok := b.stateMetadata[d.ID]
	identity := nativeInspectionIdentity(i) + d.Execution.HostKey + d.DataDir
	measure := force || !ok || cache.Identity != identity || time.Since(cache.At) > time.Minute || d.Execution.StoppedConfirmed || !d.Started
	return context.WithValue(c, nativeMetadataKey{}, measure), measure
}
func (b *nativeBackend) mergeStateMetadata(i *Instance, d nativeRecord, obs *nativeObservation, measured bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stateMetadata == nil {
		b.stateMetadata = map[string]nativeMetadataCache{}
	}
	if measured {
		if len(b.stateMetadata) >= 1024 {
			b.stateMetadata = map[string]nativeMetadataCache{}
		}
		b.stateMetadata[d.ID] = nativeMetadataCache{nativeInspectionIdentity(i) + d.Execution.HostKey + d.DataDir, time.Now(), obs.StateBytes, obs.StateEntries}
	} else if cached, ok := b.stateMetadata[d.ID]; ok {
		obs.StateBytes, obs.StateEntries = cached.Bytes, cached.Entries
	}
}
func nativeInspectionMetadataScript(measure bool) string {
	script := `metadata_bytes=null;metadata_entries=null;metadata_available=null
 if capacity=$(stat -f -c '%a %S' "$data" 2>/dev/null); then
  set -- $capacity
  if test "$#" = 2; then metadata_available=$(( $1 * $2 )); fi
 fi
 `
	if measure {
		// Live estimates are refreshed at most once/minute. Transfers and stopped
		// targets request fresh measurements. Unsafe/unreadable data blocks migration
		// through null metadata without breaking ordinary service health inspection.
		walk := "set -eu\n" + nativeDataSafe(nativeSnapshotLimits{ArchiveBytes: nativeStateHardLimit, StateBytes: nativeStateHardLimit, MaxEntries: nativeStateMaxEntries}) + `printf '%s %s' "$state_bytes" "$state_entries"`
		// A slow tree must not turn a healthy app into an inspection failure.
		script += `if command -v timeout >/dev/null && metadata=$(data="$data" timeout 2 sh -c ` + quoteShellArg(walk) + ` 2>/dev/null); then
  set -- $metadata;metadata_bytes=$1;metadata_entries=$2
 fi
 `
	}
	return script
}
