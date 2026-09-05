// Package service implements the Computer interface using a custom browser-service HTTP API.
package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/apteva/apps/mcp/computer/internal/browser/providerhttp"
	"net/http"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/presentation"
)

type Computer struct {
	requestCtx context.Context
	url        string
	display    computer.DisplaySize
	client     *http.Client
}

// New creates a Computer backed by a browser-service HTTP API.
func New(url string, display computer.DisplaySize) (*Computer, error) {
	if url == "" {
		return nil, fmt.Errorf("service: url is required")
	}
	return &Computer{
		url:     url,
		display: display,
		client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *Computer) Execute(action computer.Action) ([]byte, error) {
	switch action.Type {
	case "screenshot":
		return c.Screenshot()
	case "wait":
		dur := action.Duration
		if dur <= 0 {
			dur = 1000
		}
		if err := cdputil.Sleep(c.callerContext(), time.Duration(dur)*time.Millisecond); err != nil {
			return nil, err
		}
		return c.finishAction(action)
	}

	if action.Type == "type" && action.Presentation.Enabled() && action.Presentation.TypingDelayMS > 0 {
		for _, char := range []rune(action.Text) {
			part := action
			part.Text = string(char)
			if err := c.dispatch(part); err != nil {
				return nil, err
			}
			time.Sleep(time.Duration(action.Presentation.TypingDelayMS) * time.Millisecond)
		}
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.finishAction(action)
	}
	if err := c.dispatch(action); err != nil {
		return nil, err
	}
	presentation.AfterAction(action.Presentation, 200*time.Millisecond)
	return c.finishAction(action)
}

func (c *Computer) dispatch(action computer.Action) error {
	data, _ := json.Marshal(action)
	req, err := http.NewRequestWithContext(c.callerContext(), "POST", c.url+"/action", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("action %s failed: %w", action.Type, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := providerhttp.ReadAll(resp.Body, 64<<20)
		return fmt.Errorf("action %s: HTTP %d: %s", action.Type, resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Computer) Screenshot() ([]byte, error) {
	req, err := http.NewRequestWithContext(c.callerContext(), "GET", c.url+"/screenshot", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("screenshot failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := providerhttp.ReadAll(resp.Body, 64<<20)
		return nil, fmt.Errorf("screenshot: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	respBody, err := providerhttp.ReadAll(resp.Body, 64<<20)
	if err != nil {
		return nil, err
	}
	if decoded, err := base64.StdEncoding.DecodeString(string(respBody)); err == nil {
		return decoded, nil
	}
	return respBody, nil
}

func (c *Computer) DisplaySize() computer.DisplaySize { return c.display }

func (c *Computer) Close() error { return nil }

func (c *Computer) callerContext() context.Context {
	if c.requestCtx != nil {
		return c.requestCtx
	}
	return context.Background()
}
func (c *Computer) BindRequest(ctx context.Context) func() {
	previous := c.requestCtx
	c.requestCtx = ctx
	return func() { c.requestCtx = previous }
}
func (c *Computer) ExecuteAction(action computer.Action) error {
	action.NoScreenshot = true
	_, err := c.Execute(action)
	return err
}
func (c *Computer) finishAction(action computer.Action) ([]byte, error) {
	if action.NoScreenshot {
		return nil, nil
	}
	return c.Screenshot()
}
