package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Container mode gives each workspace its own uid. Without a passwd entry
// for it, whoami, ssh, git and npm report "unknown user" or refuse to run.
// Workspaces adds one per workspace (ws<uid>) to the container's /etc/passwd
// and /etc/group, and removes it when the workspace is destroyed.

var (
	etcPasswd = "/etc/passwd"
	etcGroup  = "/etc/group"
	usersMu   sync.Mutex
)

func workspaceUserName(uid int) string { return "ws" + strconv.Itoa(uid) }

// ensureWorkspaceUser is idempotent: the entry is added again after a
// container reset.
func ensureWorkspaceUser(uid int, home string) error {
	if uid < localUIDBase {
		return nil
	}
	usersMu.Lock()
	defer usersMu.Unlock()
	name := workspaceUserName(uid)
	if err := appendEntry(etcPasswd, uid, 2, fmt.Sprintf("%s:x:%d:%d:Apteva workspace:%s:/bin/sh", name, uid, uid, home)); err != nil {
		return err
	}
	return appendEntry(etcGroup, uid, 2, fmt.Sprintf("%s:x:%d:", name, uid))
}

func removeWorkspaceUser(uid int) {
	if uid < localUIDBase {
		return
	}
	usersMu.Lock()
	defer usersMu.Unlock()
	removeEntry(etcPasswd, uid, 2)
	removeEntry(etcGroup, uid, 2)
}

func entryHasID(line string, field, id int) bool {
	parts := strings.Split(line, ":")
	return len(parts) > field && parts[field] == strconv.Itoa(id)
}

func appendEntry(path string, id, field int, line string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, existing := range strings.Split(string(raw), "\n") {
		if entryHasID(existing, field, id) {
			return nil
		}
	}
	prefix := ""
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(prefix + line + "\n")
	return err
}

func removeEntry(path string, id, field int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(raw), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !entryHasID(line, field, id) {
			kept = append(kept, line)
		}
	}
	if len(kept) == len(lines) {
		return
	}
	info, _ := os.Stat(path)
	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".apteva-tmp"
	if os.WriteFile(tmp, []byte(strings.Join(kept, "\n")), mode) == nil {
		_ = os.Rename(tmp, path)
	}
}
