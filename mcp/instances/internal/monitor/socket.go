package monitor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// A private Unix socket makes live exports cheap; durable spool writes happen
// every 10s. There is no exposed TCP port and no remote platform token.
func Serve(ctx context.Context, dir string, e *Engine) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "collector.sock")
	// Only remove an abandoned socket. A second service must never replace the
	// running collector's endpoint.
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("collector already running")
	}
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer l.Close()
	defer os.Remove(path)
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		// Exports are small and serialized. Deadlines and input limits stop a stuck
		// local client from accumulating goroutines or memory.
		c.SetDeadline(time.Now().Add(5 * time.Second))
		var request struct {
			Since int64 `json:"since"`
		}
		if json.NewDecoder(io.LimitReader(c, 128)).Decode(&request) == nil {
			batch := e.Export(request.Since)
			data, err := EncodeBatch(batch)
			if err == nil {
				fmt.Fprintln(c, base64.StdEncoding.EncodeToString(data))
			}
		}
		c.Close()
	}
}
func ExportSocket(dir string, since int64) ([]byte, error) {
	c, err := net.DialTimeout("unix", filepath.Join(dir, "collector.sock"), time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(c).Encode(map[string]int64{"since": since}); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(c, 768<<10))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Keep the wire packet below runSSH's 1MiB output ceiling. Preserve every
// incident summary; drop oldest detail explicitly if a catch-up batch is large.
func EncodeBatch(b Batch) ([]byte, error) {
	data, err := Encode(b)
	if err != nil {
		return nil, err
	}
	for i := range b.Incidents {
		if len(data) <= 500<<10 {
			break
		}
		b.Incidents[i].Recordings = nil
		b.Incidents[i].DetailExpired = true
		data, err = Encode(b)
		if err != nil {
			return nil, err
		}
	}
	if len(data) > 500<<10 {
		return nil, fmt.Errorf("collector batch too large")
	}
	return data, nil
}
