package api

// NavigationDiagnostics contains bounded, credential-free CDP evidence. CDP
// exposes connection reuse on responses, but not raw HTTP/2 RST/GOAWAY frames.
type NavigationDiagnostics struct {
	RequestedURL   string              `json:"requested_url"`
	ReadOnly       bool                `json:"read_only"`
	Recovered      bool                `json:"recovered"`
	RetryExhausted bool                `json:"retry_exhausted"`
	Attempts       []NavigationAttempt `json:"attempts"`
}
type NavigationAttempt struct {
	TabID            string              `json:"tab_id,omitempty"`
	URL              string              `json:"url"`
	FinalURL         string              `json:"final_url,omitempty"`
	FailedURL        string              `json:"failed_request_url,omitempty"`
	Error            string              `json:"browser_error,omitempty"`
	DurationMS       int64               `json:"duration_ms"`
	Requests         []NavigationRequest `json:"redirect_chain"`
	UnsafeMethod     bool                `json:"non_read_only_method_observed"`
	Canceled         bool                `json:"canceled"`
	BlockedReason    string              `json:"blocked_reason,omitempty"`
	TransportDetails string              `json:"http2_frame_details"`
}
type NavigationRequest struct {
	URL              string  `json:"url"`
	Method           string  `json:"method"`
	Status           int64   `json:"status,omitempty"`
	Protocol         string  `json:"protocol,omitempty"`
	ConnectionID     float64 `json:"connection_id,omitempty"`
	ConnectionReused *bool   `json:"connection_reused,omitempty"`
	RemoteIP         string  `json:"remote_ip,omitempty"`
}

type NavigationReporter interface{ LastNavigation() *NavigationDiagnostics }
