package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Signals are authorized by process identity, never by a reused TCP port.
func terminateOwnedGroup(pid int, identity string, grace time.Duration) error {
	if pid <= 1 || identity == "" {
		return errors.New("missing process identity")
	}
	current := processIdentity(pid)
	if current == "" {
		return nil
	}
	if current != identity {
		return errors.New("process identity changed")
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return err
	}
	if pgid != pid {
		return errors.New("release is not its process group leader")
	}
	if err = syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processGroupAlive(pgid) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The original leader may have exited while its children drain. A new leader
	// with the same ID must never be signalled as part of the old group.
	if current = processIdentity(pid); current != "" && current != identity {
		return errors.New("process group identity changed during stop")
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processGroupAlive(pgid) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("process group %d did not exit before shutdown deadline", pgid)
}

// Legacy releases have no stored generation. Admit them only when the live
// process birth time matches the recorded launch, then persist the identity.
func verifiedReleaseProcessIdentity(releaseID int64, pid int) (string, error) {
	identity := processIdentity(pid)
	if identity == "" {
		return "", errors.New("release process is not alive")
	}
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return "", errors.New("release database unavailable")
	}
	db := globalCtx.AppDB()
	var saved string
	_ = db.QueryRow(`SELECT process_identity FROM release_runtime WHERE release_id=?`, releaseID).Scan(&saved)
	if saved != "" {
		if saved != identity {
			return "", errors.New("recorded process identity changed")
		}
		return identity, nil
	}
	rel, err := dbGetRelease(db, releaseID)
	if err != nil || rel == nil || rel.PID != pid {
		return "", errors.New("release process record missing")
	}
	recorded, err := time.Parse(time.RFC3339, rel.StartedAt)
	born := processStartedAt(pid)
	delta := born.Sub(recorded)
	if err != nil || born.IsZero() || delta < -5*time.Second || delta > 5*time.Second {
		return "", errors.New("legacy process launch time does not match release")
	}
	group, err := syscall.Getpgid(pid)
	if err != nil || group != pid {
		return "", errors.New("legacy release is not its process group leader")
	}
	_, err = db.Exec(`INSERT INTO release_runtime(release_id,process_identity) VALUES(?,?) ON CONFLICT(release_id) DO UPDATE SET process_identity=excluded.process_identity`, releaseID, identity)
	return identity, err
}
