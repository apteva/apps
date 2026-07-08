package main

// Static-server mode. When the deploy binary is invoked with
// `--static-server` as the first arg (instead of running as an apteva
// sidecar), it becomes a tiny standalone HTTP file server for one
// release's dist/ directory. This is how `framework: static` releases
// are spawned in v0.11.0+ — fleet's deploy.app re-execs itself with
// this flag instead of running a FileServer in-process.
//
// Why: the in-process model couldn't survive an apteva-server restart
// (the server lived in deploy's own process; when deploy respawned,
// the server died). A subprocess survives in its own pgrp the same
// way bun/node/go releases do, and v0.7.0's Adopt(pid, port) catches
// it on boot. One ship surface — we use os.Args[0] (deploy's own
// binary) so there's no extra binary to build, distribute, or pin.
//
// Args parsed:
//   --port N            (required) TCP port to bind
//   --root /path/to/dir (required) directory to serve
//   --log-path /…       (optional) tee server log here; otherwise
//                       writes to stderr (which the parent supervisor
//                       captures via cmd.Stderr already)
//
// SIGTERM triggers a graceful Shutdown then process exit. SIGKILL
// terminates immediately (kernel-enforced; nothing to do here).
//
// The 404 / SPA fallback policy is whatever staticHandler does
// (already exists in runtime.go): 404.html wins if present, else
// index.html, else real 404. Same as the previous in-process version.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// runStaticServer is the entry point when main() sees the
// --static-server flag. Returns once the server has exited (caller
// returns from main, ending the process).
func runStaticServer(args []string) {
	fs := flag.NewFlagSet("static-server", flag.ExitOnError)
	port := fs.Int("port", 0, "TCP port to bind (required)")
	root := fs.String("root", "", "directory to serve (required)")
	logPath := fs.String("log-path", "", "optional log file; otherwise stderr")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "static-server: parse flags: %v\n", err)
		os.Exit(2)
	}
	if *port == 0 || *root == "" {
		fmt.Fprintln(os.Stderr, "static-server: --port and --root are required")
		os.Exit(2)
	}

	logger := log.New(os.Stderr, "static-server ", log.LstdFlags|log.Lmsgprefix)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "static-server: open log %s: %v\n", *logPath, err)
			os.Exit(2)
		}
		defer f.Close()
		// Mirror writes to both stderr (so the parent's cmd.Stderr
		// capture stays useful) and the log file (so the release's
		// runtime.log persists past process exit).
		logger = log.New(io.MultiWriter(os.Stderr, f), "static-server ", log.LstdFlags|log.Lmsgprefix)
	}
	logger.Printf("listening on :%d root=%s", *port, *root)

	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:           staticHandler(*root),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// SIGTERM → graceful shutdown. systemd / parent-supervisor send
	// this first; SIGKILL would just terminate us without any of this
	// running, which is also fine.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		logger.Print("received signal, shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Printf("listen error: %v", err)
		os.Exit(1)
	}
	logger.Print("clean exit")
}

