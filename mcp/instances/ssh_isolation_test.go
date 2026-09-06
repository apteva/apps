package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func waitSSHTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("SSH command did not start")
}

func TestAdministrativeCommandSurvivesIngressRefusalsAndEviction(t *testing.T) {
	for _, evict := range []bool{false, true} {
		name := "refused_backend"
		if evict {
			name = "broken_ingress_transport"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			admin := auditSSHClient(t, dir)
			ingress := auditSSHClient(t, dir)
			oldDial, oldPool := dialAdministrativeSSH, globalSSHPool
			dialAdministrativeSSH = func(*Instance, time.Duration) (*ssh.Client, error) { return admin, nil }
			globalSSHPool = &sshPool{clients: map[int64]*ssh.Client{991: ingress}}
			defer func() { globalSSHPool.closeAll(); globalSSHPool = oldPool; dialAdministrativeSSH = oldDial }()
			done := make(chan error, 1)
			go func() {
				out, code, err := runSSH(&Instance{ID: 991}, "touch started; sleep 0.3; printf done", 3*time.Second)
				if code != 0 || out != "done" {
					done <- errors.New("command interrupted: " + out)
					return
				}
				done <- err
			}()
			waitSSHTestFile(t, filepath.Join(dir, "started"))
			if evict {
				globalSSHPool.drop(991, ingress)
			} else {
				for i := 0; i < 15; i++ {
					_, err := dialInstanceLoopback(&Instance{ID: 991}, "127.0.0.1:7200")
					var refused *ssh.OpenChannelError
					if !errors.As(err, &refused) {
						t.Fatalf("expected channel refusal, got %T %v", err, err)
					}
					if globalSSHPool.clients[991] != ingress {
						t.Fatal("backend refusal evicted healthy transport")
					}
				}
				out, code, err := runSSHOnce(ingress, "printf still-connected", time.Second)
				if err != nil || code != 0 || out != "still-connected" {
					t.Fatalf("ingress transport lost: %q %d %v", out, code, err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommandTimeoutCannotCancelConcurrentCommand(t *testing.T) {
	dir := t.TempDir()
	longClient := auditSSHClient(t, dir)
	shortClient := auditSSHClient(t, dir)
	old := dialAdministrativeSSH
	defer func() { dialAdministrativeSSH = old }()
	var dials atomic.Int32
	dialAdministrativeSSH = func(*Instance, time.Duration) (*ssh.Client, error) {
		if dials.Add(1) == 1 {
			return longClient, nil
		}
		return shortClient, nil
	}
	done := make(chan error, 1)
	go func() {
		out, code, err := runSSH(&Instance{ID: 991}, "touch long-started; sleep 0.3; printf survived", 2*time.Second)
		if code != 0 || out != "survived" {
			done <- errors.New("long command interrupted")
			return
		}
		done <- err
	}()
	waitSSHTestFile(t, filepath.Join(dir, "long-started"))
	_, code, err := runSSH(&Instance{ID: 991}, "sleep 0.2", 30*time.Millisecond)
	if code != -1 || err == nil {
		t.Fatalf("expected short timeout: %d %v", code, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 2 {
		t.Fatal("commands did not get distinct transports")
	}
}

func TestChannelFailureClassificationDoesNotUseErrorText(t *testing.T) {
	for _, reason := range []ssh.RejectionReason{ssh.ConnectionFailed, ssh.Prohibited, ssh.ResourceShortage, ssh.UnknownChannelType} {
		err := &ssh.OpenChannelError{Reason: reason, Message: "EOF connection reset channel"}
		if isSSHConnError(err) {
			t.Fatalf("channel rejection misclassified: %s", err)
		}
	}
	if !isSSHConnError(errors.New("EOF")) {
		t.Fatal("real transport EOF no longer detected")
	}
}

func TestTunnelDeadlineDoesNotCloseOtherChannels(t *testing.T) {
	dir := t.TempDir()
	client := auditSSHClientWithForwardDelay(t, dir, 150*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		out, code, err := runSSHOnce(client, "touch started; sleep 0.3; printf survived", 2*time.Second)
		if code != 0 || out != "survived" {
			done <- errors.New("shared channel interrupted")
			return
		}
		done <- err
	}()
	waitSSHTestFile(t, filepath.Join(dir, "started"))
	_, err := dialTunnelWithTimeout(client, "127.0.0.1:7200", 30*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded channel timeout, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
