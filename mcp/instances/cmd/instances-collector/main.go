// instances-collector is a port-free host service. Only the SSH account can read
// its spool. Exporting never needs a network listener or a platform credential.
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

func main() {
	dir := flag.String("data-dir", "", "Private collector data directory")
	export := flag.Bool("export", false, "Export bounded gzip/base64 batch")
	since := flag.Int64("since", 0, "Last ingested Unix milliseconds")
	version := flag.Bool("version", false, "Print collector version")
	flag.Parse()
	if *version {
		fmt.Println(monitor.CollectorVersion)
		return
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "--data-dir required")
		os.Exit(1)
	}
	path := filepath.Join(*dir, "spool.json.gz")
	if *export {
		if data, err := monitor.ExportSocket(*dir, *since); err == nil {
			fmt.Print(string(data))
			return
		}
	}
	engine, err := monitor.Load(path)
	if *export {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		data, err := monitor.EncodeBatch(engine.Export(*since))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(data))
		return
	}
	if err != nil {
		engine = monitor.NewEngine()
	}
	engine.Recover(time.Now().UnixMilli())
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		if err := monitor.Serve(ctx, *dir, engine); err != nil {
			fmt.Fprintln(os.Stderr, err)
			cancel()
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); monitor.Run(ctx, engine) }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			<-done
			if err := engine.Save(path); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			return
		case <-ticker.C:
			if err := engine.Save(path); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}
}
