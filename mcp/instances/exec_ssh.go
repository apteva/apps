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
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
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
	host := inst.PublicIPv4
	if host == "" {
		host = inst.PublicIPv6
	}
	if host == "" {
		return nil, errors.New("instance has no public IP")
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// First-time-trust on host keys. Pinning on first connection
		// is a v0.2 polish — for v0.1 we accept whatever the VPS
		// presents at provisioning time. The keypair itself is the
		// security boundary; MITM at first connect would need a
		// network attacker between Apteva and the VPS provider's
		// edge, which is a different threat model than what apps
		// usually defend against.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}
	addr := net.JoinHostPort(host, "22")
	return ssh.Dial("tcp", addr, cfg)
}

// ─── SSH client pool ──────────────────────────────────────────────
//
// Fresh dial per call cost ~0.5–2s (TCP + crypto handshake + key auth +
// across-Atlantic RTT). The vitals poller hits this every 5s; agent-
// driven run_command bursts hit it even harder. Caching one *ssh.Client
// per host_id and multiplexing many channels through it makes warm
// calls sub-100ms locally.
//
// crypto/ssh handles concurrent sessions on a single client correctly
// — each NewSession is its own channel. The remote sshd's MaxSessions
// (default 10) is the only ceiling and our usage stays well under it.
//
// Cache invalidation: any session-level error drops the client so the
// next call redials. Reasons a cached client can go stale:
//   - sshd idle timeout (default off, but some images set it)
//   - VPS rebooted / network blip
//   - we hit MaxSessions
// All surface as a session.NewSession or session.Run error; we treat
// any of them by closing + evicting the cached client and returning
// the error. The next call gets a fresh dial. No auto-retry — one
// failure is invisible to the panel (5s cache + "Loading vitals…"),
// and silent retries would mask real connectivity issues.
//
// No GC for v1: typical setups have a handful of hosts, and idle
// clients cost only one TCP socket each. Connections die naturally
// when the remote sshd disconnects.

type sshPool struct {
	mu      sync.Mutex
	clients map[int64]*ssh.Client
}

var globalSSHPool = &sshPool{clients: map[int64]*ssh.Client{}}

// get returns a cached *ssh.Client for the instance, dialing fresh
// if none is cached. fresh=true when this call dialed a new one
// (caller can use that to decide whether to retry on error).
func (p *sshPool) get(inst *Instance) (client *ssh.Client, fresh bool, err error) {
	p.mu.Lock()
	if c, ok := p.clients[inst.ID]; ok {
		p.mu.Unlock()
		return c, false, nil
	}
	p.mu.Unlock()

	c, err := dialSSH(inst, 10*time.Second)
	if err != nil {
		return nil, true, err
	}

	p.mu.Lock()
	// Re-check after the dial — another goroutine may have raced us
	// and already cached one. Use theirs; close ours.
	if existing, ok := p.clients[inst.ID]; ok {
		p.mu.Unlock()
		_ = c.Close()
		return existing, false, nil
	}
	p.clients[inst.ID] = c
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
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// runSSH executes a command on the remote instance. CombinedOutput-
// style — stdout+stderr returned together to match runLocal's shape.
// Uses the global pool for connection reuse; on any session-level
// error the cached client is evicted so the next call redials.
func runSSH(inst *Instance, cmd string, timeout time.Duration) (output string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, fresh, err := globalSSHPool.get(inst)
	if err != nil {
		return "", -1, fmt.Errorf("ssh dial: %w", err)
	}
	out, exit, runErr := runSSHOnce(client, cmd, timeout)
	if runErr != nil && !fresh && isSSHConnError(runErr) {
		// Cached client went stale (idle timeout, VPS reboot, etc.).
		// Evict + redial once. Pure session errors (non-zero exit
		// code from the user's command) don't trigger this branch
		// — those are returned as-is.
		globalSSHPool.drop(inst.ID, client)
		client, _, err = globalSSHPool.get(inst)
		if err != nil {
			return "", -1, fmt.Errorf("ssh redial after stale connection: %w", err)
		}
		out, exit, runErr = runSSHOnce(client, cmd, timeout)
	}
	return out, exit, runErr
}

// runSSHOnce is the single-attempt body of runSSH — open a session
// on the supplied client, run cmd, return combined output + exit
// code. Doesn't touch the pool.
func runSSHOnce(client *ssh.Client, cmd string, timeout time.Duration) (string, int, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", -1, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	writer := &lockedWriter{}
	session.Stdout = writer
	session.Stderr = writer

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()
	select {
	case <-ctx.Done():
		// Best-effort kill — close the session, the connection will
		// drop the remote shell via SIGHUP. We don't evict the
		// cached client here: a timeout is the user's command being
		// slow, not the transport being broken.
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		out := writer.String()
		if len(out) > 1<<20 {
			out = out[:1<<20]
		}
		return out, -1, fmt.Errorf("command timed out after %s", timeout)
	case runErr := <-done:
		out := writer.String()
		if len(out) > 1<<20 {
			out = out[:1<<20]
		}
		exit := 0
		if runErr != nil {
			if exitErr, ok := runErr.(*ssh.ExitError); ok {
				exit = exitErr.ExitStatus()
			} else {
				exit = -1
			}
		}
		return out, exit, runErr
	}
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
	if _, ok := err.(*ssh.ExitError); ok {
		return false
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
// Uses the shared SSH pool — repeated uploads to the same host
// (e.g. media render workers staging multiple sources) reuse the
// connection.
func uploadSSH(inst *Instance, path, contentB64 string) (bytesWritten int, err error) {
	body, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return 0, fmt.Errorf("invalid base64: %w", err)
	}
	client, fresh, err := globalSSHPool.get(inst)
	if err != nil {
		return 0, fmt.Errorf("ssh dial: %w", err)
	}
	n, runErr := uploadSSHOnce(client, path, contentB64, body)
	if runErr != nil && !fresh && isSSHConnError(runErr) {
		globalSSHPool.drop(inst.ID, client)
		client, _, err = globalSSHPool.get(inst)
		if err != nil {
			return 0, fmt.Errorf("ssh redial after stale connection: %w", err)
		}
		n, runErr = uploadSSHOnce(client, path, contentB64, body)
	}
	return n, runErr
}

func uploadSSHOnce(client *ssh.Client, path, contentB64 string, decodedBody []byte) (int, error) {
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()

	cmd := fmt.Sprintf(`mkdir -p $(dirname %q) && base64 -d > %q`, path, path)
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
	return len(decodedBody), nil
}

// probeSSHReady polls TCP-connect + SSH handshake until success or
// timeout. Used after VPS provisioning so callers can wait until the
// machine actually accepts our key. Returns nil on success.
func probeSSHReady(inst *Instance, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := dialSSH(inst, 5*time.Second)
		if err == nil {
			_ = client.Close()
			return nil
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	if lastErr == nil {
		lastErr = errors.New("ssh probe timed out")
	}
	return lastErr
}
