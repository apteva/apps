package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

type DockerBackend interface {
	Probe(ctx context.Context) error
	CreateNetwork(ctx context.Context, name string) error
	CreateVolume(ctx context.Context, name string) error
	Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error)
	Start(ctx context.Context, containerName string) error
	Stop(ctx context.Context, containerName string) error
	Restart(ctx context.Context, containerName string) error
	Remove(ctx context.Context, containerName string, force bool) error
	RemoveNetwork(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	Logs(ctx context.Context, containerName string, tail int) (string, error)
	Inspect(ctx context.Context, containerName string) (*ContainerState, error)
}

type LocalDocker struct{}

type ContainerState struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Running bool   `json:"running"`
	Health  string `json:"health"`
}

func (d LocalDocker) Probe(ctx context.Context) error {
	_, err := docker(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}

func (d LocalDocker) CreateNetwork(ctx context.Context, name string) error {
	if _, err := docker(ctx, "network", "inspect", name); err == nil {
		return nil
	}
	_, err := docker(ctx, "network", "create", name)
	return err
}

func (d LocalDocker) CreateVolume(ctx context.Context, name string) error {
	if _, err := docker(ctx, "volume", "inspect", name); err == nil {
		return nil
	}
	_, err := docker(ctx, "volume", "create", name)
	return err
}

func (d LocalDocker) Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error) {
	args := []string{"run", "-d", "--name", containerName, "--restart", spec.RestartPolicy, "--network", networkName}
	for _, p := range spec.Ports {
		hostPort := p.HostPort
		if hostPort == 0 {
			allocated, err := freePort()
			if err != nil {
				return "", err
			}
			hostPort = allocated
		}
		args = append(args, "-p", fmt.Sprintf("%s:%d:%d/%s", p.BindAddr, hostPort, p.ContainerPort, p.Protocol))
	}
	for k, v := range spec.Env {
		if !validEnvKey(k) {
			return "", fmt.Errorf("invalid env key %q", k)
		}
		args = append(args, "-e", k+"="+v)
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", v.DockerVolumeName, v.MountPath))
	}
	if spec.Resources.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(spec.Resources.MemoryMB)+"m")
	}
	if spec.Resources.CPU > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.Resources.CPU, 'f', -1, 64))
	}
	args = append(args, spec.Image)
	out, err := docker(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (d LocalDocker) Start(ctx context.Context, containerName string) error {
	_, err := docker(ctx, "start", containerName)
	return err
}

func (d LocalDocker) Stop(ctx context.Context, containerName string) error {
	_, err := docker(ctx, "stop", "-t", "10", containerName)
	return err
}

func (d LocalDocker) Restart(ctx context.Context, containerName string) error {
	_, err := docker(ctx, "restart", "-t", "10", containerName)
	return err
}

func (d LocalDocker) Remove(ctx context.Context, containerName string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, containerName)
	_, err := docker(ctx, args...)
	return err
}

func (d LocalDocker) RemoveNetwork(ctx context.Context, name string) error {
	_, err := docker(ctx, "network", "rm", name)
	return err
}

func (d LocalDocker) RemoveVolume(ctx context.Context, name string) error {
	_, err := docker(ctx, "volume", "rm", name)
	return err
}

func (d LocalDocker) Logs(ctx context.Context, containerName string, tail int) (string, error) {
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	return docker(ctx, "logs", "--tail", strconv.Itoa(tail), containerName)
}

func (d LocalDocker) Inspect(ctx context.Context, containerName string) (*ContainerState, error) {
	raw, err := docker(ctx, "inspect", containerName)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		ID    string `json:"Id"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("container not found")
	}
	st := &ContainerState{ID: arr[0].ID, Status: arr[0].State.Status, Running: arr[0].State.Running}
	if arr[0].State.Health != nil {
		st.Health = arr[0].State.Health.Status
	}
	return st, nil
}

func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("docker %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func probeHTTP(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
