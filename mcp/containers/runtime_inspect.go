package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type publishedPortReader interface {
	PublishedPorts(context.Context, string, []PortSpec) ([]PortSpec, error)
}

func parsePublishedPorts(raw string, ports []PortSpec) ([]PortSpec, error) {
	var bindings map[string][]struct {
		HostIP   string
		HostPort string
	}
	if err := json.Unmarshal([]byte(raw), &bindings); err != nil {
		return nil, err
	}
	out := append([]PortSpec(nil), ports...)
	for i, p := range out {
		values := bindings[fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol)]
		if len(values) == 0 {
			return nil, fmt.Errorf("published port %d missing", p.ContainerPort)
		}
		chosen := values[0]
		for _, v := range values {
			if strings.Trim(v.HostIP, "[]") == strings.Trim(p.BindAddr, "[]") {
				chosen = v
				break
			}
		}
		n, err := strconv.Atoi(chosen.HostPort)
		if err != nil {
			return nil, err
		}
		out[i].HostPort = n
	}
	return out, nil
}
func (d LocalDocker) PublishedPorts(ctx context.Context, name string, ports []PortSpec) ([]PortSpec, error) {
	raw, err := docker(ctx, "inspect", "--format", "{{json .NetworkSettings.Ports}}", name)
	if err != nil {
		return nil, err
	}
	return parsePublishedPorts(raw, ports)
}
func (d *RemoteDocker) PublishedPorts(ctx context.Context, name string, ports []PortSpec) ([]PortSpec, error) {
	raw, err := d.remoteDocker(ctx, 30, "inspect", "--format", "{{json .NetworkSettings.Ports}}", name)
	if err != nil {
		return nil, err
	}
	return parsePublishedPorts(raw, ports)
}
func savePublishedPorts(db *sql.DB, id string, ports []PortSpec) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range ports {
		if _, err := tx.Exec(`UPDATE containers_ports SET host_port=? WHERE workload_id=? AND container_port=? AND protocol=?`, p.HostPort, id, p.ContainerPort, p.Protocol); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func publicWorkloadURL(host string, port PortSpec) string {
	ip := net.ParseIP(strings.Trim(port.BindAddr, "[]"))
	if port.Protocol != "tcp" || port.HostPort <= 0 {
		return ""
	}
	if host == "" {
		host = strings.Trim(port.BindAddr, "[]")
		if ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
		}
	} else if ip != nil && ip.IsLoopback() {
		return ""
	}
	return "http://" + net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port.HostPort))
}

type workloadHealthProber interface {
	ProbeService(context.Context, *Workload, RunSpec) error
}

func healthTarget(w *Workload, s RunSpec) string {
	port := s.HealthPort
	if port == 0 {
		for _, p := range w.Ports {
			if p.Protocol == "tcp" {
				port = p.ContainerPort
				break
			}
		}
	}
	if port == 0 {
		return ""
	}
	scheme := s.HealthScheme
	if scheme == "" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, port, s.HealthPath)
}
func (d LocalDocker) ProbeService(ctx context.Context, w *Workload, s RunSpec) error {
	url := healthTarget(w, s)
	if url == "" {
		return fmt.Errorf("HTTP health check requires a TCP health port")
	}
	_, err := helperContainer(ctx, nil, 1024, "--network", "container:"+w.ContainerName, "alpine:3.20", "wget", "-q", "-T", "3", "-O", "/dev/null", url)
	return err
}
func (d *RemoteDocker) ProbeService(ctx context.Context, w *Workload, s RunSpec) error {
	url := healthTarget(w, s)
	if url == "" {
		return fmt.Errorf("HTTP health check requires a TCP health port")
	}
	name := "containers-health-" + newExecutionID()
	cmd := "trap " + shellQuote("docker rm -f "+shellQuote(name)+" >/dev/null 2>&1 || true") + " EXIT; " + shellJoin("docker", "run", "--rm", "--name", name, "--network", "container:"+w.ContainerName, "--memory", "32m", "--cpus", "0.25", "--pids-limit", "16", "alpine:3.20", "wget", "-q", "-T", "3", "-O", "/dev/null", url)
	_, _, err := d.runRemote(ctx, cmd, 15)
	return err
}

var remoteCapabilities struct {
	mu    sync.Mutex
	ready map[string]time.Time
}
