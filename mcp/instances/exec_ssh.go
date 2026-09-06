package main

// SSH-based execution for remote instances. Same shape as the local
// path (run + upload + output) but goes over an authenticated SSH
// channel using the per-instance keypair stored in the DB.
//
// Trust model: each instance has its own keypair, generated at
// provisioning time. Public key seeded into the VPS via cloud-init's
// authorized_keys; private key stored in the DB (plaintext in v0.1,
// encrypted in v0.2). To revoke access: destroy the instance — the
// VPS goes with it. There's no "rotate this key" path in v0.1.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	maxCommandOutputBytes = 1 << 20
	maxFileTransferBytes  = 16 << 20
	maxEncodedFileBytes   = ((maxFileTransferBytes + 2) / 3) * 4
)

// generateSSHKeypair creates a fresh Ed25519 keypair. Private key is
// PEM-encoded OpenSSH format; public key is OpenSSH authorized_keys
// format ("ssh-ed25519 AAAA..."). Both safe to pass into cloud-init
// userdata.
func generateSSHKeypair() (privPEM, pubAuth string, err error) {
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519: %w", err)
	}
	privBlock, err := ssh.MarshalPrivateKey(privKey, "apteva-instances")
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}
	privPEM = string(pem.EncodeToMemory(privBlock))
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", fmt.Errorf("ssh public key: %w", err)
	}
	pubAuth = string(ssh.MarshalAuthorizedKey(sshPub))
	pubAuth = strings.TrimSpace(pubAuth) // strip trailing \n
	return privPEM, pubAuth, nil
}

// dialSSH opens an SSH session to an instance. Used by both the
// readiness probe and the run/upload paths. Caller must Close().
func dialSSH(inst *Instance, timeout time.Duration) (*ssh.Client, error) {
	if inst.SSHPrivateKey == "" {
		return nil, errors.New("instance has no SSH private key")
	}
	signer, err := ssh.ParsePrivateKey([]byte(inst.SSHPrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	user := inst.SSHUser
	if user == "" {
		user = "root"
	}
	host := inst.SSHHost
	if host == "" {
		host = inst.PublicIPv4
	}
	if host == "" {
		host = inst.PublicIPv6
	}
	if host == "" {
		return nil, errors.New("instance has no public IP")
	}
	port := inst.SSHPort
	if port <= 0 {
		port = 22
	}
	var observedHostKey string
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// Trust on first use: the first successful connection pins the
		// server key in the instance row. Every reconnect verifies it.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			encoded := base64.StdEncoding.EncodeToString(key.Marshal())
			if inst.SSHHostKey != "" && inst.SSHHostKey != encoded {
				return fmt.Errorf("SSH host key changed for instance %d (received %s)", inst.ID, ssh.FingerprintSHA256(key))
			}
			observedHostKey = encoded
			return nil
		},
		Timeout: timeout,
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(deadline)
	cc, channels, requests, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, channels, requests)
	if inst.SSHHostKey == "" && observedHostKey != "" {
		pinned := observedHostKey
		if globalCtx != nil && globalCtx.AppDB() != nil {
			pinned, err = dbPinSSHHostKey(globalCtx.AppDB(), inst.ID, observedHostKey)
			if err != nil {
				_ = client.Close()
				return nil, fmt.Errorf("pin SSH host key: %w", err)
			}
		}
		if pinned != observedHostKey {
			_ = client.Close()
			return nil, fmt.Errorf("SSH host key changed while connecting to instance %d", inst.ID)
		}
	}
	return client, nil
}

// ─── SSH client pool ──────────────────────────────────────────────
//
// Only ingress forwarding uses this pool. Administrative commands,
// metrics and file transfers own independent connections, so an ingress
// failure cannot interrupt a lifecycle operation. Backend channel refusals
// do not evict healthy clients; only transport failures reconnect.
//
// No GC for v1: typical setups have a handful of hosts, and idle
// clients cost only one TCP socket each. Connections die naturally
// when the remote sshd disconnects.

type sshPool struct {
	mu         sync.Mutex
	clients    map[int64]*ssh.Client
	identities map[int64][32]byte
}

var globalSSHPool = &sshPool{clients: map[int64]*ssh.Client{}}

// get returns a cached *ssh.Client for the instance, dialing fresh
// if none is cached. fresh=true when this call dialed a new one
// (caller can use that to decide whether to retry on error).
func (p *sshPool) get(inst *Instance) (client *ssh.Client, fresh bool, err error) {
	return p.getWithTimeout(inst, 10*time.Second)
}

func (p *sshPool) getWithTimeout(inst *Instance, timeout time.Duration) (client *ssh.Client, fresh bool, err error) {
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%s|%d|%s|%s", inst.Provider, inst.ProviderConnectionID, inst.ProviderID, inst.SSHHost, inst.PublicIPv4, inst.PublicIPv6, inst.SSHPort, inst.SSHUser, inst.SSHPrivateKey)))
	p.mu.Lock()
	if c, ok := p.clients[inst.ID]; ok {
		known, recorded := p.identities[inst.ID]
		if !recorded || known == identity {
			p.mu.Unlock()
			return c, false, nil
		}
		delete(p.clients, inst.ID)
		_ = c.Close()
	}
	p.mu.Unlock()
	c, err := dialSSH(inst, timeout)
	if err != nil {
		return nil, true, err
	}
	p.mu.Lock()
	if existing, ok := p.clients[inst.ID]; ok {
		known, recorded := p.identities[inst.ID]
		if !recorded || known == identity {
			p.mu.Unlock()
			_ = c.Close()
			return existing, false, nil
		}
		_ = existing.Close()
	}
	if p.identities == nil {
		p.identities = map[int64][32]byte{}
	}
	p.clients[inst.ID] = c
	p.identities[inst.ID] = identity
	p.mu.Unlock()
	return c, true, nil
}

// drop evicts the cached client for instID, but only if it's still
// the same one the caller saw (don't trample a newer entry that
// someone else just dialed in).
func (p *sshPool) drop(instID int64, c *ssh.Client) {
	p.mu.Lock()
	if p.clients[instID] == c {
		delete(p.clients, instID)
	}
	p.mu.Unlock()
	_ = c.Close()
}

// evict drops the cached client for instID regardless of identity.
// Called from the destroy path: after the VPS is terminated upstream,
// any cached connection points at a dead host and the socket should
// be released, not silently leaked.
func (p *sshPool) evict(instID int64) {
	p.mu.Lock()
	c, ok := p.clients[instID]
	if ok {
		delete(p.clients, instID)
	}
	p.mu.Unlock()
	if ok {
		_ = c.Close()
	}
}

func (p *sshPool) closeAll() {
	p.mu.Lock()
	clients := make([]*ssh.Client, 0, len(p.clients))
	for id, client := range p.clients {
		clients = append(clients, client)
		delete(p.clients, id)
	}
	p.mu.Unlock()
	for _, client := range clients {
		_ = client.Close()
	}
}

// ─── combined-output writer ───────────────────────────────────────
//
// crypto/ssh delivers stdout and stderr via two separate goroutines.
// The pre-v0.3.2 code pointed session.Stdout AND session.Stderr at
// the same *bytes.Buffer, which races: bytes.Buffer is documented as
// NOT safe for concurrent use, and the symptom in production was
// entire stream's worth of output silently dropped (4 calls in 5
// observed). The vitals script's final JSON printf often lost its
// race with awk noise on stderr, so instance_metrics returned "no
// JSON in vitals script output" intermittently.
//
// Fix: a sync.Mutex-protected writer that both streams share. Output
// ordering is preserved per-write; the relative ordering of
// interleaved stdout/stderr writes is non-deterministic (same as
// CombinedOutput from os/exec) but no bytes are lost.

type lockedWriter struct {
	mu        sync.Mutex
	b         bytes.Buffer
	max       int
	truncated bool
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if w.max > 0 {
		remaining := w.max - w.b.Len()
		if remaining <= 0 {
			w.truncated = true
			return n, nil
		}
		if len(p) > remaining {
			p = p[:remaining]
			w.truncated = true
		}
	}
	_, _ = w.b.Write(p)
	return n, nil
}

func (w *lockedWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.b.Bytes()...)
}

func (w *lockedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// Administrative operations own their transport. Ingress forwarding and
// metrics must never cancel a lifecycle command by evicting a shared client.
// Keep this injectable for localhost SSH regression tests.
var dialAdministrativeSSH = dialSSH

// runSSH executes exactly once on a dedicated connection. A timeout can
// safely close this transport without affecting another command or tunnel.
func runSSH(inst *Instance, cmd string, timeout time.Duration) (output string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	started := time.Now()
	if inst.workContext != nil && inst.workContext.Err() != nil {
		return "", -1, inst.workContext.Err()
	}
	client, err := dialAdministrativeSSH(inst, timeout)
	if err != nil {
		return "", -1, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()
	if inst.workContext != nil {
		boundClient := client
		stop := context.AfterFunc(inst.workContext, func() { _ = boundClient.Close() })
		defer stop()
	}
	return runSSHOnce(client, cmd, timeout-time.Since(started))
}

type sshSessionOpenError struct{ error }

func (e *sshSessionOpenError) Unwrap() error { return e.error }

// Closing the transport is necessary if the peer stops answering channel
// requests. The deadline starts before NewSession, not after it.
func boundedSSHSession(client *ssh.Client, timeout time.Duration) (*ssh.Session, func(), error) {
	if timeout <= 0 {
		return nil, func() {}, context.DeadlineExceeded
	}
	timer := time.AfterFunc(timeout, func() { _ = client.Close() })
	session, err := client.NewSession()
	if err != nil {
		timer.Stop()
		return nil, func() {}, &sshSessionOpenError{err}
	}
	return session, func() { _ = session.Close(); timer.Stop() }, nil
}

// runSSHOnce is the single-attempt body of runSSH — open a session
// on the supplied client, run cmd, return combined output + exit
// code. Doesn't touch the pool.
func runSSHOnce(client *ssh.Client, cmd string, timeout time.Duration) (string, int, error) {
	started := time.Now()
	session, cleanup, err := boundedSSHSession(client, timeout)
	if err != nil {
		return "", -1, err
	}
	defer cleanup()

	writer := &lockedWriter{max: maxCommandOutputBytes}
	session.Stdout = writer
	session.Stderr = writer

	ctx, cancel := context.WithTimeout(context.Background(), timeout-time.Since(started))
	defer cancel()
	marker, err := newSSHExitMarker()
	if err != nil {
		return "", -1, err
	}
	done := make(chan error, 1)
	go func() { done <- session.Run(wrapSSHCommand(cmd, marker)) }()
	select {
	case <-ctx.Done():
		// Closing the transport bounds session/channel waits. The remote
		// command outcome may be unknown, so it must not be replayed.
		_ = client.Close()
		out := writer.String()
		return out, -1, fmt.Errorf("command timed out after %s", timeout)
	case runErr := <-done:
		return resolveSSHRunResult(writer.String(), marker, runErr)
	}
}

func newSSHExitMarker() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate SSH command marker: %w", err)
	}
	return "__APTEVA_EXIT_" + hex.EncodeToString(nonce[:]) + "__=", nil
}

func wrapSSHCommand(cmd, marker string) string {
	return "sh -c " + quoteShellArg(cmd) +
		"; rc=$?; printf '\\n" + marker + "%s\\n' \"$rc\"; exit \"$rc\""
}

func quoteShellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func resolveSSHRunResult(output, marker string, runErr error) (string, int, error) {
	clean, markerExit, markerOK := stripSSHExitMarker(output, marker)
	if runErr == nil {
		if markerOK && markerExit != 0 {
			return clean, markerExit, fmt.Errorf("remote command exited with status %d", markerExit)
		}
		return clean, 0, nil
	}
	if exitErr, ok := runErr.(*ssh.ExitError); ok {
		return clean, exitErr.ExitStatus(), runErr
	}
	if _, missing := runErr.(*ssh.ExitMissingError); missing && markerOK {
		if markerExit == 0 {
			return clean, 0, nil
		}
		return clean, markerExit, fmt.Errorf("remote command exited with status %d", markerExit)
	}
	return clean, -1, runErr
}

func stripSSHExitMarker(output, marker string) (string, int, bool) {
	prefix := "\n" + marker
	start := strings.LastIndex(output, prefix)
	if start < 0 {
		return output, -1, false
	}
	codeStart := start + len(prefix)
	codeEnd := codeStart
	for codeEnd < len(output) && output[codeEnd] >= '0' && output[codeEnd] <= '9' {
		codeEnd++
	}
	if codeEnd == codeStart || (codeEnd < len(output) && output[codeEnd] != '\n') {
		return output, -1, false
	}
	exit, err := strconv.Atoi(output[codeStart:codeEnd])
	if err != nil {
		return output, -1, false
	}
	removeEnd := codeEnd
	if removeEnd < len(output) && output[removeEnd] == '\n' {
		removeEnd++
	}
	return output[:start] + output[removeEnd:], exit, true
}

// isSSHConnError flags errors that mean the cached client is no
// longer usable. *ssh.ExitError means the command ran and exited
// non-zero — that's a USER-level error, the connection is fine.
// Everything else (channel/session/EOF/closed-network) means the
// underlying client is dead and we should evict + redial.
func isSSHConnError(err error) bool {
	if err == nil {
		return false
	}
	// The peer successfully answered a channel request. A refused backend
	// or forwarding policy is not a failed SSH transport.
	var channelErr *ssh.OpenChannelError
	if errors.As(err, &channelErr) {
		return false
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	// An uncertain completion must not poison the next verification. Evict
	// the transport, but never replay a command that may have executed.
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	for _, hint := range []string{
		"use of closed network connection",
		"connection lost",
		"connection reset",
		"broken pipe",
		"EOF",
		"channel",
		"ssh session",
		"command timed out after",
	} {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}

// uploadSSH writes file content to the remote via base64-decode +
// `cat > path`. Not the most elegant transport (real SCP is heavier
// to wire up; the SCP protocol is a separate package or shelling out
// to scp), but works on every Linux that has bash + base64 (every
// Ubuntu / Debian / Alpine the VPS providers serve).
//
// File transfers also own their transport; their deadlines cannot interrupt
// lifecycle commands or public tunnels.
func uploadSSH(inst *Instance, path, contentB64 string) (bytesWritten int, err error) {
	if len(contentB64) > maxEncodedFileBytes {
		return 0, fmt.Errorf("file exceeds %d byte upload limit", maxFileTransferBytes)
	}
	body, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return 0, fmt.Errorf("invalid base64: %w", err)
	}
	if len(body) > maxFileTransferBytes {
		return 0, fmt.Errorf("file exceeds %d byte upload limit", maxFileTransferBytes)
	}
	client, err := dialAdministrativeSSH(inst, 10*time.Second)
	if err != nil {
		return 0, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()
	return uploadSSHOnce(client, path, contentB64, len(body))
}

func uploadSSHOnce(client *ssh.Client, path, contentB64 string, decodedLen int) (int, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return 0, errors.New("valid file path required")
	}
	session, cleanup, err := boundedSSHSession(client, time.Minute)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	cmd := `set -eu; path=` + quoteShellArg(path) + `; case "$path" in -*) path=./$path;; esac
dir=$(dirname "$path"); mkdir -p "$dir"
tmp=$(mktemp "$dir/.apteva-upload.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM
base64 -d > "$tmp"
chmod 0644 "$tmp"
mv -f "$tmp" "$path"`
	stdin, err := session.StdinPipe()
	if err != nil {
		return 0, err
	}
	if err := session.Start(cmd); err != nil {
		return 0, err
	}
	if _, err := io.WriteString(stdin, contentB64); err != nil {
		_ = session.Close()
		return 0, err
	}
	if err := stdin.Close(); err != nil {
		return 0, err
	}
	if err := session.Wait(); err != nil {
		return 0, fmt.Errorf("remote write failed: %w", err)
	}
	return decodedLen, nil
}

func downloadSSH(inst *Instance, path string) (contentB64 string, bytesRead int, err error) {
	client, err := dialAdministrativeSSH(inst, 10*time.Second)
	if err != nil {
		return "", 0, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()
	body, runErr := downloadSSHOnce(client, path)
	if runErr != nil {
		return "", 0, runErr
	}
	return base64.StdEncoding.EncodeToString(body), len(body), nil
}

func downloadSSHOnce(client *ssh.Client, path string) ([]byte, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, errors.New("valid file path required")
	}
	session, cleanup, err := boundedSSHSession(client, time.Minute)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	stdout := &lockedWriter{max: maxFileTransferBytes + 1}
	stderr := &lockedWriter{max: maxCommandOutputBytes}
	session.Stdout = stdout
	session.Stderr = stderr
	if err := session.Run(`test -f ` + quoteShellArg(path) + ` && head -c ` + strconv.Itoa(maxFileTransferBytes+1) + ` < ` + quoteShellArg(path)); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("remote read failed: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("remote read failed: %w", err)
	}
	if stdout.Truncated() || len(stdout.Bytes()) > maxFileTransferBytes {
		return nil, fmt.Errorf("remote file exceeds %d byte download limit", maxFileTransferBytes)
	}
	return stdout.Bytes(), nil
}

// probeSSHReady polls TCP-connect + SSH handshake + a tiny remote
// command until success or timeout. The command check matters because
// some VPS images accept key auth while PAM still blocks non-
// interactive sessions with "password expired"; those hosts are not
// actually ready for instance_run_command or metrics.
func probeSSHReady(inst *Instance, timeout time.Duration) error {
	work := inst.workContext
	if work == nil {
		work = context.Background()
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if work.Err() != nil {
			return work.Err()
		}
		client, err := dialSSH(inst, min(5*time.Second, time.Until(deadline)))
		if err == nil {
			out, exit, runErr := runSSHOnce(client, "true", 5*time.Second)
			_ = client.Close()
			if runErr == nil && exit == 0 {
				return nil
			}
			if runErr != nil {
				lastErr = fmt.Errorf("ssh command probe failed: %w: %s", runErr, strings.TrimSpace(out))
			} else {
				lastErr = fmt.Errorf("ssh command probe exited %d: %s", exit, strings.TrimSpace(out))
			}
		} else {
			lastErr = err
		}
		select {
		case <-work.Done():
			return work.Err()
		case <-time.After(min(3*time.Second, time.Until(deadline))):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("ssh probe timed out")
	}
	return lastErr
}

var probeSSHReadyFn = probeSSHReady
