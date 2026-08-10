package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/apteva/apps/mcp/tunnel/protocol"
	"github.com/gorilla/websocket"
)

type Client struct {
	ServerURL       string
	Token           string
	TargetURL       *url.URL
	HTTPClient      *http.Client
	MaxResponseBody int64
	writeMu         sync.Mutex
}

func New(serverURL, token, targetURL string) (*Client, error) {
	serverURL = strings.TrimSpace(serverURL)
	token = strings.TrimSpace(token)
	if serverURL == "" {
		return nil, errors.New("tunnel server URL is required")
	}
	if token == "" {
		return nil, errors.New("connector token is required")
	}
	target, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, errors.New("target must be an absolute http:// or https:// URL")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, errors.New("target must use http or https")
	}
	return &Client{
		ServerURL:       serverURL,
		Token:           token,
		TargetURL:       target,
		HTTPClient:      &http.Client{Timeout: 60 * time.Second},
		MaxResponseBody: 10 << 20,
	}, nil
}

func (c *Client) Run(ctx context.Context) error {
	connectURL, err := websocketURL(c.ServerURL)
	if err != nil {
		return err
	}
	headers := http.Header{"Authorization": []string{"Bearer " + c.Token}}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, connectURL, headers)
	if err != nil {
		if response != nil {
			return fmt.Errorf("connect tunnel: HTTP %d", response.StatusCode)
		}
		return fmt.Errorf("connect tunnel: %w", err)
	}
	defer conn.Close()
	stopClose := make(chan struct{})
	defer close(stopClose)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "connector stopping"),
				time.Now().Add(time.Second),
			)
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	conn.SetReadLimit(c.MaxResponseBody*2 + (1 << 20))
	conn.SetPingHandler(func(value string) error {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(value), time.Now().Add(5*time.Second))
	})

	for {
		var message protocol.Message
		if err := conn.ReadJSON(&message); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read tunnel request: %w", err)
		}
		if message.Type != protocol.TypeRequest || message.ID == "" {
			continue
		}
		go c.handleRequest(ctx, conn, message)
	}
}

func (c *Client) handleRequest(ctx context.Context, conn *websocket.Conn, message protocol.Message) {
	response := protocol.Message{
		Type: protocol.TypeResponse,
		ID:   message.ID,
	}
	status, headers, body, err := c.forward(ctx, message)
	if err != nil {
		response.Error = safeClientError(err)
	} else {
		response.StatusCode = status
		response.Headers = headers
		response.Body = body
	}
	c.writeMu.Lock()
	_ = conn.WriteJSON(response)
	c.writeMu.Unlock()
}

func (c *Client) forward(ctx context.Context, message protocol.Message) (int, map[string][]string, []byte, error) {
	requestURI := message.Path
	if requestURI == "" {
		requestURI = "/"
	}
	if message.RawQuery != "" {
		requestURI += "?" + message.RawQuery
	}
	incoming, err := url.ParseRequestURI(requestURI)
	if err != nil || incoming.IsAbs() {
		return 0, nil, nil, errors.New("invalid tunneled request path")
	}
	target := *c.TargetURL
	target.Path = strings.TrimRight(c.TargetURL.Path, "/") + incoming.Path
	target.RawPath = ""
	target.RawQuery = incoming.RawQuery
	request, err := http.NewRequestWithContext(ctx, message.Method, target.String(), bytes.NewReader(message.Body))
	if err != nil {
		return 0, nil, nil, errors.New("could not create target request")
	}
	for key, values := range message.Headers {
		if isHopHeader(key) {
			continue
		}
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("X-Apteva-Tunnel", "1")
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("local target unavailable: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, c.MaxResponseBody+1))
	if err != nil {
		return 0, nil, nil, errors.New("could not read target response")
	}
	if int64(len(body)) > c.MaxResponseBody {
		return 0, nil, nil, fmt.Errorf("target response exceeds %d bytes", c.MaxResponseBody)
	}
	return response.StatusCode, copyHeaders(response.Header), body, nil
}

func websocketURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid tunnel server URL")
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", errors.New("tunnel server URL must use https, http, wss, or ws")
	}
	if !strings.HasSuffix(parsed.Path, "/v1/connect") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/connect"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func copyHeaders(input http.Header) map[string][]string {
	output := make(map[string][]string, len(input))
	for key, values := range input {
		if isHopHeader(key) {
			continue
		}
		output[key] = append([]string(nil), values...)
	}
	return output
}

func isHopHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func safeClientError(err error) string {
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
