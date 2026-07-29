package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tunnelclient "github.com/apteva/apps/mcp/tunnel/client"
)

func main() {
	var (
		server    = flag.String("server", os.Getenv("APTEVA_TUNNEL_SERVER"), "Tunnel app URL, for example https://instance.example/api/apps/tunnel")
		target    = flag.String("target", "http://127.0.0.1:5280", "Local HTTP target")
		tokenFile = flag.String("token-file", "", "File containing the connector token")
	)
	flag.Parse()

	token, err := loadToken(*tokenFile)
	if err != nil {
		fatal(err)
	}
	if strings.TrimSpace(*server) == "" {
		fatal(errors.New("--server or APTEVA_TUNNEL_SERVER is required"))
	}
	client, err := tunnelclient.New(*server, token, *target)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backoff := time.Second
	for ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "connecting to %s → %s\n", *server, *target)
		err = client.Run(ctx)
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(os.Stderr, "tunnel disconnected: %v; retrying in %s\n", err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func loadToken(path string) (string, error) {
	if path == "" {
		token := strings.TrimSpace(os.Getenv("APTEVA_TUNNEL_TOKEN"))
		if token == "" {
			return "", errors.New("--token-file or APTEVA_TUNNEL_TOKEN is required")
		}
		return token, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	if len(data) > 4096 {
		return "", errors.New("token file is unexpectedly large")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("token file is empty")
	}
	return token, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "apteva-tunnel:", err)
	os.Exit(1)
}
