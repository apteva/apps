package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Every registered machine's reachability is checked about every 45 seconds.
// A check never authenticates or opens a new SSH session: it reuses a pooled
// connection when one exists (a keepalive request), and otherwise opens a TCP
// connection and reads the SSH greeting. For a reverse tunnel the greeting
// travels the whole path, server listener -> tunnel -> machine's sshd.
const (
	presenceInterval          = 45 * time.Second
	presenceTimeout           = 5 * time.Second
	presenceConcurrency       = 8
	instanceConnectedTopic    = "instance.connected"
	instanceDisconnectedTopic = "instance.disconnected"
)

var errBadConnectedFilter = errors.New("connected must be true or false")

var (
	presenceProbe     = probeInstancePresence
	presenceKeepalive = pooledKeepalive
)

func sshAddress(inst *Instance) string {
	host := inst.SSHHost
	if host == "" {
		host = inst.PublicIPv4
	}
	if host == "" {
		host = inst.PublicIPv6
	}
	if host == "" {
		return ""
	}
	port := inst.SSHPort
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func pooledKeepalive(id int64) (alive, pooled bool) {
	globalSSHPool.mu.Lock()
	c := globalSSHPool.clients[id]
	globalSSHPool.mu.Unlock()
	if c == nil {
		return false, false
	}
	done := make(chan error, 1)
	go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			globalSSHPool.drop(id, c)
			return false, true
		}
		return true, true
	case <-time.After(presenceTimeout):
		globalSSHPool.drop(id, c)
		return false, true
	}
}

func probeInstancePresence(inst *Instance) bool {
	if alive, pooled := presenceKeepalive(inst.ID); pooled && alive {
		return true
	}
	addr := sshAddress(inst)
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, presenceTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(presenceTimeout))
	line, err := bufio.NewReaderSize(conn, 256).ReadString('\n')
	return err == nil && strings.HasPrefix(line, "SSH-")
}

// Only machines Instances is meant to reach are checked; rows being created,
// destroyed or rolled back are skipped.
func presenceCandidates(db *sql.DB) ([]*Instance, error) {
	rows, err := dbListInstances(db, "", "")
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, inst := range rows {
		if inst.IsLocal() || inst.ID == 0 {
			continue
		}
		switch inst.Status {
		case "ready", "provisioning", "upgrading", "error":
			if sshAddress(inst) != "" {
				out = append(out, inst)
			}
		}
	}
	return out, nil
}

// Records the result and emits instance.connected / instance.disconnected on
// a change. The status guard keeps a concurrent destroy from being undone.
func recordPresence(ctx *sdk.AppCtx, inst *Instance, connected bool, now time.Time) error {
	seen := inst.LastSeenAt
	if connected {
		seen = now.UTC().Format(time.RFC3339)
	}
	result, err := ctx.AppDB().Exec(`UPDATE instances SET connected=?, last_seen_at=? WHERE id=? AND status NOT IN ('destroying','rolling_back','destroyed')`, connected, seen, inst.ID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 1 && connected != inst.Connected {
		inst.Connected, inst.LastSeenAt = connected, seen
		topic := instanceDisconnectedTopic
		if connected {
			topic = instanceConnectedTopic
		}
		payload := instanceEventPayload(inst)
		payload["connected"] = connected
		payload["last_seen_at"] = seen
		payload["connect_mode"] = inst.ConnectMode
		emitInstanceEventMap(ctx, topic, payload)
	}
	return nil
}

func checkPresenceOnce(ctx *sdk.AppCtx, work context.Context) {
	instances, err := presenceCandidates(ctx.AppDB())
	if err != nil {
		return
	}
	sem := make(chan struct{}, presenceConcurrency)
	var wg sync.WaitGroup
	for _, inst := range instances {
		if work.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(inst *Instance) {
			defer func() { <-sem; wg.Done() }()
			connected := presenceProbe(inst)
			if work.Err() == nil {
				_ = recordPresence(ctx, inst, connected, time.Now())
			}
		}(inst)
	}
	wg.Wait()
}

// Runs as its own background worker so slow probes never delay provisioning
// and setup reconcilers.
func startPresence(ctx *sdk.AppCtx) {
	startInstanceWorker(ctx, -2, func(work context.Context) {
		for {
			checkPresenceOnce(ctx, work)
			if err := sleepContext(work, presenceInterval); err != nil {
				return
			}
		}
	})
}

// The built-in local host is this server, so it is always reachable.
func withLocalPresence(inst *Instance) *Instance {
	if inst != nil && (inst.IsLocal() || inst.ID == 0) {
		inst.Connected = true
		inst.LastSeenAt = nowUTC()
		inst.ConnectMode = "local"
	}
	return inst
}

// Optional connected filter shared by the MCP tool and the HTTP list route.
func filterConnected(rows []*Instance, raw any) ([]*Instance, error) {
	if raw == nil {
		return rows, nil
	}
	var want bool
	switch v := raw.(type) {
	case bool:
		want = v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "":
			return rows, nil
		case "true", "1", "yes":
			want = true
		case "false", "0", "no":
		default:
			return nil, errBadConnectedFilter
		}
	default:
		return nil, errBadConnectedFilter
	}
	out := rows[:0]
	for _, r := range rows {
		if r.Connected == want {
			out = append(out, r)
		}
	}
	return out, nil
}
