package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/backup/internal/instanceworker"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func privateDirectory(p string) error {
	if e := os.Mkdir(p, 0700); e != nil && !os.IsExist(e) {
		return e
	}
	info, e := os.Lstat(p)
	if e != nil {
		return e
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe worker directory")
	}
	f, e := instanceworker.OpenDirectory(p)
	if e != nil {
		return e
	}
	defer f.Close()
	var st unix.Stat_t
	if e = unix.Fstat(int(f.Fd()), &st); e != nil {
		return e
	}
	if st.Uid != uint32(os.Geteuid()) {
		return errors.New("worker directory is owned by another user")
	}
	return nil
}
func run() error {
	if len(os.Args) != 3 {
		return errors.New("usage: backup-helper probe|start|serve argument")
	}
	switch os.Args[1] {
	case "probe":
		var paths []string
		if e := json.Unmarshal([]byte(os.Args[2]), &paths); e != nil {
			return e
		}
		result, e := instanceworker.Probe(paths)
		if e != nil {
			result = map[string]any{"supported": false, "reason": e.Error()}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "start":
		raw, e := base64.StdEncoding.DecodeString(os.Args[2])
		if e != nil {
			return e
		}
		var cfg instanceworker.Config
		if e = json.Unmarshal(raw, &cfg); e != nil {
			return e
		}
		// Resolve the platform's /tmp symlink once, then validate owned directories.
		tmp, e := filepath.EvalSymlinks("/tmp")
		if e != nil {
			return e
		}
		base := filepath.Join(tmp, fmt.Sprintf("apteva-backup-%d", os.Geteuid()))
		if e = privateDirectory(base); e != nil {
			return e
		}
		if len(cfg.OperationID) != 48 || strings.ContainsAny(cfg.OperationID, "/.") {
			return errors.New("invalid operation id")
		}
		dir := filepath.Join(base, cfg.OperationID)
		if e = privateDirectory(dir); e != nil {
			return e
		}
		if _, e = instanceworker.New(cfg, dir); e != nil {
			return e
		}
		configPath := filepath.Join(dir, "config.json")
		if old, e := os.ReadFile(configPath); e == nil {
			var previous instanceworker.Config
			if json.Unmarshal(old, &previous) != nil {
				return errors.New("invalid persisted operation")
			}
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(cfg)
			if string(a) != string(b) {
				return errors.New("operation configuration changed")
			}
		} else if !os.IsNotExist(e) {
			return e
		} else if e = instanceworker.AtomicJSON(configPath, cfg); e != nil {
			return e
		}
		executable, e := os.Executable()
		if e != nil {
			return e
		}
		null, e := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		defer null.Close()
		cmd := exec.Command(executable, "serve", dir)
		cmd.Stdin = null
		cmd.Stdout = null
		cmd.Stderr = null
		cmd.SysProcAttr = &unixSysProcAttr
		if e = cmd.Start(); e != nil {
			return e
		}
		_ = cmd.Process.Release()
		client := &http.Client{Timeout: time.Second}
		for i := 0; i < 80; i++ {
			var endpoint struct {
				Port int `json:"port"`
			}
			b, e := os.ReadFile(filepath.Join(dir, "endpoint.json"))
			if e == nil && json.Unmarshal(b, &endpoint) == nil && endpoint.Port > 0 {
				req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/status", endpoint.Port), nil)
				req.Header.Set("Authorization", "Bearer "+cfg.Token)
				resp, e := client.Do(req)
				if e == nil {
					io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
					resp.Body.Close()
					if resp.StatusCode == 200 {
						return json.NewEncoder(os.Stdout).Encode(endpoint)
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("worker failed to start")
	case "serve":
		dir := os.Args[2]
		lock, e := os.OpenFile(filepath.Join(dir, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		defer lock.Close()
		if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
			return nil
		}
		raw, e := os.ReadFile(filepath.Join(dir, "config.json"))
		if e != nil {
			return e
		}
		var cfg instanceworker.Config
		if e = json.Unmarshal(raw, &cfg); e != nil {
			return e
		}
		worker, e := instanceworker.New(cfg, dir)
		if e != nil {
			return e
		}
		return worker.Serve(context.Background())
	default:
		return errors.New("unknown command")
	}
}
