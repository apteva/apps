package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type tunnelKey struct {
	instanceID int64
	targetPort int
}

type managedTunnel struct {
	key       tunnelKey
	listener  net.Listener
	localPort int
	instance  *Instance

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

type tunnelRegistry struct {
	mu      sync.Mutex
	tunnels map[tunnelKey]*managedTunnel
	dial    func(*Instance, string) (net.Conn, error)
}

var globalTunnelRegistry = newTunnelRegistry(dialInstanceLoopback)

func newTunnelRegistry(dial func(*Instance, string) (net.Conn, error)) *tunnelRegistry {
	return &tunnelRegistry{
		tunnels: map[tunnelKey]*managedTunnel{},
		dial:    dial,
	}
}

func dialInstanceLoopback(inst *Instance, target string) (net.Conn, error) {
	client, fresh, err := globalSSHPool.get(inst)
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial("tcp", target)
	if err == nil {
		return conn, nil
	}
	if !fresh {
		globalSSHPool.drop(inst.ID, client)
		client, _, redialErr := globalSSHPool.get(inst)
		if redialErr != nil {
			return nil, fmt.Errorf("ssh tunnel redial: %w", redialErr)
		}
		return client.Dial("tcp", target)
	}
	return nil, err
}

func (r *tunnelRegistry) open(inst *Instance, targetPort int) (*managedTunnel, error) {
	if inst == nil || inst.ID <= 0 || inst.IsLocal() {
		return nil, errors.New("remote instance required")
	}
	if targetPort <= 0 || targetPort > 65535 {
		return nil, fmt.Errorf("target_port must be between 1 and 65535")
	}
	key := tunnelKey{instanceID: inst.ID, targetPort: targetPort}

	r.mu.Lock()
	if existing := r.tunnels[key]; existing != nil {
		r.mu.Unlock()
		return existing, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	t := &managedTunnel{
		key:       key,
		listener:  ln,
		localPort: ln.Addr().(*net.TCPAddr).Port,
		instance:  inst,
		conns:     map[net.Conn]struct{}{},
	}
	r.tunnels[key] = t
	r.mu.Unlock()

	go r.serve(t)
	return t, nil
}

func (r *tunnelRegistry) serve(t *managedTunnel) {
	target := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", t.key.targetPort))
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		go r.forward(t, local, target)
	}
}

func (r *tunnelRegistry) forward(t *managedTunnel, local net.Conn, target string) {
	remote, err := r.dial(t.instance, target)
	if err != nil {
		_ = local.Close()
		return
	}
	t.track(local, true)
	t.track(remote, true)
	defer func() {
		t.track(local, false)
		t.track(remote, false)
		_ = local.Close()
		_ = remote.Close()
	}()

	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyOne(remote, local)
	go copyOne(local, remote)
	<-done
}

func (t *managedTunnel) track(conn net.Conn, add bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if add {
		t.conns[conn] = struct{}{}
	} else {
		delete(t.conns, conn)
	}
}

func (t *managedTunnel) close() {
	_ = t.listener.Close()
	t.mu.Lock()
	defer t.mu.Unlock()
	for conn := range t.conns {
		_ = conn.Close()
	}
	t.conns = map[net.Conn]struct{}{}
}

func (r *tunnelRegistry) close(instanceID int64, targetPort int) bool {
	key := tunnelKey{instanceID: instanceID, targetPort: targetPort}
	r.mu.Lock()
	t := r.tunnels[key]
	delete(r.tunnels, key)
	r.mu.Unlock()
	if t != nil {
		t.close()
		return true
	}
	return false
}

func (r *tunnelRegistry) closeInstance(instanceID int64) {
	r.mu.Lock()
	var closing []*managedTunnel
	for key, t := range r.tunnels {
		if key.instanceID == instanceID {
			closing = append(closing, t)
			delete(r.tunnels, key)
		}
	}
	r.mu.Unlock()
	for _, t := range closing {
		t.close()
	}
}

func (r *tunnelRegistry) closeAll() {
	r.mu.Lock()
	closing := make([]*managedTunnel, 0, len(r.tunnels))
	for key, t := range r.tunnels {
		closing = append(closing, t)
		delete(r.tunnels, key)
	}
	r.mu.Unlock()
	for _, t := range closing {
		t.close()
	}
}

func (a *App) toolOpenTunnel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	targetPort := intArg(args, "target_port", 0)
	inst, err := dbGetInstance(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if inst.IsLocal() {
		return nil, errors.New("SSH tunnel requires a remote instance")
	}
	if inst.Status != "ready" {
		return nil, fmt.Errorf("instance not ready (status=%s)", inst.Status)
	}
	t, err := globalTunnelRegistry.open(inst, targetPort)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":          id,
		"target_host": "127.0.0.1",
		"target_port": targetPort,
		"local_host":  "127.0.0.1",
		"local_port":  t.localPort,
		"opened_at":   time.Now().UTC(),
	}, nil
}

func (a *App) toolCloseTunnel(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	targetPort := intArg(args, "target_port", 0)
	if id <= 0 || targetPort <= 0 || targetPort > 65535 {
		return nil, errors.New("id and valid target_port are required")
	}
	closed := globalTunnelRegistry.close(id, targetPort)
	return map[string]any{"id": id, "target_port": targetPort, "closed": closed}, nil
}
