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

// runSSH executes a command on the remote instance. CombinedOutput-
// style — stdout+stderr returned together to match runLocal's shape.
func runSSH(inst *Instance, cmd string, timeout time.Duration) (output string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, err := dialSSH(inst, 10*time.Second)
	if err != nil {
		return "", -1, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", -1, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var buf bytes.Buffer
	session.Stdout = &buf
	session.Stderr = &buf

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()
	select {
	case <-ctx.Done():
		// Best-effort kill — close the session, the connection will
		// drop and the remote shell will SIGHUP.
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		out := buf.String()
		if len(out) > 1<<20 {
			out = out[:1<<20]
		}
		return out, -1, fmt.Errorf("command timed out after %s", timeout)
	case runErr := <-done:
		out := buf.String()
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

// uploadSSH writes file content to the remote via base64-decode +
// `cat > path`. Not the most elegant transport (real SCP is heavier
// to wire up; the SCP protocol is a separate package or shelling out
// to scp), but works on every Linux that has bash + base64 (every
// Ubuntu / Debian / Alpine the VPS providers serve).
func uploadSSH(inst *Instance, path, contentB64 string) (bytesWritten int, err error) {
	body, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return 0, fmt.Errorf("invalid base64: %w", err)
	}
	client, err := dialSSH(inst, 10*time.Second)
	if err != nil {
		return 0, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

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
	return len(body), nil
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
