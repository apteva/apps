package protocol

// Message is the version-one Apteva Tunnel wire envelope. Bodies are carried
// as base64 by encoding/json so clients in other languages can implement the
// protocol without needing WebSocket binary-frame multiplexing.
type Message struct {
	Type       string              `json:"type"`
	ID         string              `json:"id,omitempty"`
	Method     string              `json:"method,omitempty"`
	Path       string              `json:"path,omitempty"`
	RawQuery   string              `json:"raw_query,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
	StatusCode int                 `json:"status_code,omitempty"`
	Error      string              `json:"error,omitempty"`
}

const (
	TypeRequest  = "request"
	TypeResponse = "response"
	TypeHello    = "hello"
)
