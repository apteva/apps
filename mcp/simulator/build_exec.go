package main

// Build-process execution shared by the Android and iOS backends. Source
// builds intentionally execute project-controlled build logic, so keep the
// child on a short leash: do not pass platform credentials through the
// environment, bound the runtime, and terminate the whole process group when
// the request is cancelled or times out.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const buildTimeout = 20 * time.Minute

func buildContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, buildTimeout)
}

func runBuildProcess(ctx context.Context, bin string, args []string, dir string, logW *os.File, allowedSensitiveEnv []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout = logW
	cmd.Stderr = logW
	cmd.Env = sanitizedBuildEnv(allowedSensitiveEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// CommandContext only kills the immediate child. Gradle, shell build
		// overrides, and Xcode all create descendants, so signal the process
		// group and always wait to reap the leader.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return fmt.Errorf("build cancelled: %w", context.Cause(ctx))
	}
}

// sanitizedBuildEnv preserves normal compiler/SDK discovery while removing
// platform credentials and common injected secrets. Filesystem and network
// isolation remain the responsibility of the sidecar/container runtime; this
// prevents the most direct credential leak without breaking ordinary Gradle
// or Xcode builds and private dependency configuration stored outside env.
func sanitizedBuildEnv(allowedSensitiveEnv []string) []string {
	allowed := make(map[string]struct{}, len(allowedSensitiveEnv))
	for _, key := range allowedSensitiveEnv {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key != "" && !strings.HasPrefix(key, "APTEVA_") {
			allowed[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if sensitiveBuildEnvKey(key) {
			if _, explicitlyAllowed := allowed[strings.ToUpper(key)]; !explicitlyAllowed {
				continue
			}
		}
		out = append(out, entry)
	}
	return out
}

func buildEnvAllowlist(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
}

func sensitiveBuildEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if strings.HasPrefix(upper, "APTEVA_") {
		return true
	}
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	for _, exact := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"GOOGLE_APPLICATION_CREDENTIALS", "GITHUB_PAT", "GITLAB_TOKEN",
		"SSH_AUTH_SOCK", "DOCKER_HOST", "KUBECONFIG", "NETRC",
	} {
		if upper == exact {
			return true
		}
	}
	return false
}
