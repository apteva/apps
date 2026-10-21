package sdk

// The shared telemetry client serves apps and authenticated remote callers.
// PlatformClient and runtime control contracts remain unchanged.
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type TelemetryQuery struct {
	ProjectID    string    `json:"project_id,omitempty"`
	AgentID      int64     `json:"agent_id,omitempty"`
	RuntimeID    string    `json:"runtime_id,omitempty"`
	ThreadID     string    `json:"thread_id,omitempty"`
	ThreadPrefix string    `json:"thread_prefix,omitempty"`
	Events       []string  `json:"events,omitempty"`
	Since        time.Time `json:"since,omitempty"`
	Until        time.Time `json:"until,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	Cursor       string    `json:"cursor,omitempty"`
	Projection   string    `json:"projection,omitempty"` // metadata (default), content
}
type TelemetryPage struct {
	ServerID   string                 `json:"server_id"`
	Events     []TelemetryStreamEvent `json:"events"`
	NextCursor string                 `json:"next_cursor,omitempty"`
	Order      string                 `json:"order"` // newest ingestion first
}
type TelemetryWatch struct {
	Query    TelemetryQuery
	Cursor   string
	Backfill bool // bounded stored history on each connection and replay gap
	LiveOnly bool // start fresh on each connection; useful for token deltas
}
type TelemetryDelivery struct {
	Kind     string                `json:"kind"` // ready, history, event, checkpoint, gap, error
	ServerID string                `json:"server_id,omitempty"`
	Cursor   string                `json:"cursor,omitempty"`
	Event    *TelemetryStreamEvent `json:"event,omitempty"`
	History  *TelemetryPage        `json:"history,omitempty"`
	Reason   string                `json:"reason,omitempty"`
	Error    string                `json:"error,omitempty"`
}
type ObservedAgent struct {
	ServerID  string `json:"server_id"`
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	RuntimeID string `json:"runtime_id,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}
type ObservedRuntime struct {
	ServerID  string         `json:"server_id"`
	ID        string         `json:"id"`
	ProjectID string         `json:"project_id"`
	Status    string         `json:"status"`
	Agents    []RuntimeAgent `json:"agents"`
	Apps      []RuntimeApp   `json:"apps"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}
type ObservedThread struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Status   string `json:"status,omitempty"`
}
type ObservedAttachment struct {
	ToolsAvailable bool     `json:"tools_available"`
	InstallID      int64    `json:"install_id,omitempty"`
	Name           string   `json:"name"`
	Tools          []string `json:"tools"`
}
type AgentTopology struct {
	ServerID             string               `json:"server_id"`
	AgentID              int64                `json:"agent_id"`
	RuntimeID            string               `json:"runtime_id,omitempty"`
	Threads              []ObservedThread     `json:"threads"`
	Attachments          []ObservedAttachment `json:"attachments"`
	ThreadsAvailable     bool                 `json:"threads_available"`
	AttachmentsAvailable bool                 `json:"attachments_available"`
}
type TelemetryClient interface {
	SubscribeTelemetry(context.Context, TelemetrySubscription) (<-chan TelemetryStreamEvent, error)
	QueryTelemetry(context.Context, TelemetryQuery) (*TelemetryPage, error)
	WatchTelemetry(context.Context, TelemetryWatch) (<-chan TelemetryDelivery, error)
	ListObservedAgents(context.Context, string) ([]ObservedAgent, error)
	ListObservedRuntimes(context.Context, string) ([]ObservedRuntime, error)
	GetObservedRuntime(context.Context, string, string) (*ObservedRuntime, error)
	GetAgentTopology(context.Context, string, int64) (*AgentTopology, error)
}
type telemetryClient struct {
	platform *httpPlatformClient
	callback bool
}

// NewTelemetryClient uses a user/API credential on another Server. It grants
// no extra access. Credentials remain in Authorization headers; redirects are
// refused. HTTP is allowed only for loopback development.
func NewTelemetryClient(serverURL, token string) (TelemetryClient, error) {
	u, e := url.Parse(strings.TrimRight(serverURL, "/"))
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("telemetry server must be an origin URL")
	}
	host := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")) {
		return nil, errors.New("telemetry server requires HTTPS")
	}
	p := newHTTPPlatformClient(strings.TrimRight(serverURL, "/"), token).(*httpPlatformClient)
	return &telemetryClient{platform: p}, nil
}
func (c *AppCtx) TelemetryAPI() TelemetryClient {
	if c == nil {
		return nil
	}
	if p, ok := c.platform.(*projectScopedClient); ok {
		if _, ok := p.inner.(TelemetryClient); !ok {
			return nil
		}
	}
	out, _ := c.platform.(TelemetryClient)
	return out
}
func (c *telemetryClient) path(suffix string) string {
	p := "/api/telemetry"
	if c.callback {
		p = "/api/apps/callback/telemetry"
	}
	return c.platform.baseURL + p + suffix
}
func telemetryQueryValues(q TelemetryQuery) url.Values {
	v := url.Values{}
	for k, s := range map[string]string{"project_id": q.ProjectID, "runtime_id": q.RuntimeID, "thread_id": q.ThreadID, "thread_prefix": q.ThreadPrefix, "events": strings.Join(q.Events, ","), "cursor": q.Cursor, "projection": q.Projection} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.AgentID != 0 {
		v.Set("agent_id", strconv.FormatInt(q.AgentID, 10))
	}
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		v.Set("until", q.Until.UTC().Format(time.RFC3339Nano))
	}
	return v
}
func (c *telemetryClient) request(ctx context.Context, suffix string, v url.Values, stream bool) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", c.path(suffix)+"?"+v.Encode(), nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.platform.token)
	if c.platform.userSession != "" {
		req.Header.Set("X-Apteva-User-Session", c.platform.userSession)
	}
	client := c.platform.client
	if stream {
		client = c.platform.streamClient
		req.Header.Set("Accept", "text/event-stream")
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := copyClient.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, &telemetryHTTPError{resp.StatusCode}
	}
	return resp, nil
}

type telemetryHTTPError struct{ status int }

func (e *telemetryHTTPError) Error() string {
	return fmt.Sprintf("telemetry request returned HTTP %d", e.status)
}
func (c *telemetryClient) get(ctx context.Context, path string, v url.Values, out any) error {
	r, e := c.request(ctx, path, v, false)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(out)
}
func (c *telemetryClient) QueryTelemetry(ctx context.Context, q TelemetryQuery) (*TelemetryPage, error) {
	v := telemetryQueryValues(q)
	var out TelemetryPage
	v.Set("format", "page")
	path := ""
	if c.callback {
		path = "/history"
	}
	e := c.get(ctx, path, v, &out)
	return &out, e
}
func (c *telemetryClient) ListObservedRuntimes(ctx context.Context, p string) ([]ObservedRuntime, error) {
	v := telemetryQueryValues(TelemetryQuery{ProjectID: p})
	var out []ObservedRuntime
	e := c.get(ctx, "/runtimes", v, &out)
	return out, e
}
func (c *telemetryClient) GetObservedRuntime(ctx context.Context, p, id string) (*ObservedRuntime, error) {
	v := telemetryQueryValues(TelemetryQuery{ProjectID: p})
	var out ObservedRuntime
	e := c.get(ctx, "/runtimes/"+url.PathEscape(id), v, &out)
	return &out, e
}
func (c *telemetryClient) GetAgentTopology(ctx context.Context, p string, id int64) (*AgentTopology, error) {
	v := telemetryQueryValues(TelemetryQuery{ProjectID: p})
	var out AgentTopology
	e := c.get(ctx, "/agents/"+strconv.FormatInt(id, 10)+"/topology", v, &out)
	return &out, e
}
func (c *telemetryClient) WatchTelemetry(ctx context.Context, w TelemetryWatch) (<-chan TelemetryDelivery, error) {
	return c.watchTelemetry(ctx, w, false)
}

// Legacy subscriptions negotiate the original event shape over this same path.
func (c *telemetryClient) watchTelemetry(ctx context.Context, w TelemetryWatch, legacy bool) (<-chan TelemetryDelivery, error) {
	v := telemetryQueryValues(w.Query)
	v.Del("cursor")
	if !legacy {
		v.Set("format", "delivery")
	} else if !c.callback && w.Query.AgentID == 0 {
		v.Set("all", "1")
	}
	if w.LiveOnly {
		v.Set("replay", "false")
	}
	if w.Cursor != "" {
		v.Set("cursor", w.Cursor)
	}
	if w.Backfill {
		v.Set("backfill", "true")
	}
	path := "/stream"
	if c.callback {
		path = ""
	}
	first, e := c.request(ctx, path, v, true)
	if e != nil {
		return nil, e
	}
	out := make(chan TelemetryDelivery, 64)
	go func() {
		defer close(out)
		resp := first
		delay := time.Second
		seen := map[string]bool{}
		order := []string{}
		send := func(d TelemetryDelivery) bool {
			select {
			case out <- d:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 4096), 8<<20)
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var d TelemetryDelivery
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d) != nil {
					if !send(TelemetryDelivery{Kind: "error", Error: "invalid telemetry stream frame"}) {
						resp.Body.Close()
						return
					}
					resp.Body.Close()
					return
				}
				// Older Servers emit the event directly at the same endpoint.
				if d.Kind == "" {
					if !w.LiveOnly {
						send(TelemetryDelivery{Kind: "error", Error: "server does not support resumable telemetry; upgrade the Server"})
						resp.Body.Close()
						return
					}
					var event TelemetryStreamEvent
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil || event.Type == "" {
						continue
					}
					d = TelemetryDelivery{Kind: "event", ServerID: event.ServerID, Event: &event}
				}
				if d.History != nil {
					filtered := make([]TelemetryStreamEvent, 0, len(d.History.Events))
					for _, event := range d.History.Events {
						if !seen[event.ServerID+":"+event.ID] {
							filtered = append(filtered, event)
						}
					}
					d.History.Events = filtered
				}
				key := ""
				duplicate := false
				if d.Event != nil && d.Event.ID != "" && !w.LiveOnly {
					key = d.ServerID + ":" + d.Event.ID
					duplicate = seen[key]
				}
				if !duplicate && !send(d) {
					resp.Body.Close()
					return
				}
				if d.Cursor != "" && !w.LiveOnly {
					v.Set("cursor", d.Cursor)
				}
				if key != "" && !duplicate {
					seen[key] = true
					order = append(order, key)
					if len(order) > 10000 {
						delete(seen, order[0])
						order = order[1:]
					}
				}
				if d.History != nil {
					for _, event := range d.History.Events {
						k := event.ServerID + ":" + event.ID
						if !seen[k] {
							seen[k] = true
							order = append(order, k)
						}
					}
					for len(order) > 10000 {
						delete(seen, order[0])
						order = order[1:]
					}
				}
				if d.Kind == "error" {
					resp.Body.Close()
					return
				}
				delay = time.Second
			}
			resp.Body.Close()

			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				resp, e = c.request(ctx, path, v, true)
				if e == nil {
					break
				}
				var h *telemetryHTTPError
				if errors.As(e, &h) && h.status >= 400 && h.status < 500 && h.status != 429 {
					send(TelemetryDelivery{Kind: "error", Error: e.Error()})
					return
				}
				delay = min(delay*2, 30*time.Second)
			}
		}
	}()
	return out, nil
}
func (c *httpPlatformClient) telemetry() *telemetryClient {
	return &telemetryClient{platform: c, callback: true}
}
func (c *httpPlatformClient) QueryTelemetry(ctx context.Context, q TelemetryQuery) (*TelemetryPage, error) {
	return c.telemetry().QueryTelemetry(ctx, q)
}
func (c *httpPlatformClient) WatchTelemetry(ctx context.Context, q TelemetryWatch) (<-chan TelemetryDelivery, error) {
	return c.telemetry().WatchTelemetry(ctx, q)
}
func (c *httpPlatformClient) ListObservedRuntimes(ctx context.Context, p string) ([]ObservedRuntime, error) {
	return c.telemetry().ListObservedRuntimes(ctx, p)
}
func (c *httpPlatformClient) GetObservedRuntime(ctx context.Context, p, id string) (*ObservedRuntime, error) {
	return c.telemetry().GetObservedRuntime(ctx, p, id)
}
func (c *httpPlatformClient) GetAgentTopology(ctx context.Context, p string, id int64) (*AgentTopology, error) {
	return c.telemetry().GetAgentTopology(ctx, p, id)
}
func (p *projectScopedClient) scopedTelemetryQuery(q TelemetryQuery) (TelemetryQuery, error) {
	if p.projectID != "" && q.ProjectID != "" && q.ProjectID != p.projectID {
		return q, errors.New("telemetry project scope mismatch")
	}
	if p.projectID != "" {
		q.ProjectID = p.projectID
	}
	return q, nil
}
func (p *projectScopedClient) QueryTelemetry(ctx context.Context, q TelemetryQuery) (*TelemetryPage, error) {
	q, e := p.scopedTelemetryQuery(q)
	if e != nil {
		return nil, e
	}
	if c, ok := p.inner.(TelemetryClient); ok {
		return c.QueryTelemetry(ctx, q)
	}
	return nil, errors.New("telemetry API unavailable")
}
func (p *projectScopedClient) WatchTelemetry(ctx context.Context, q TelemetryWatch) (<-chan TelemetryDelivery, error) {
	var e error
	q.Query, e = p.scopedTelemetryQuery(q.Query)
	if e != nil {
		return nil, e
	}
	if c, ok := p.inner.(TelemetryClient); ok {
		return c.WatchTelemetry(ctx, q)
	}
	return nil, errors.New("telemetry API unavailable")
}
func (p *projectScopedClient) ListObservedRuntimes(ctx context.Context, project string) ([]ObservedRuntime, error) {
	q, e := p.scopedTelemetryQuery(TelemetryQuery{ProjectID: project})
	if e != nil {
		return nil, e
	}
	if c, ok := p.inner.(TelemetryClient); ok {
		return c.ListObservedRuntimes(ctx, q.ProjectID)
	}
	return nil, errors.New("telemetry API unavailable")
}
func (p *projectScopedClient) GetObservedRuntime(ctx context.Context, project, id string) (*ObservedRuntime, error) {
	q, e := p.scopedTelemetryQuery(TelemetryQuery{ProjectID: project})
	if e != nil {
		return nil, e
	}
	if c, ok := p.inner.(TelemetryClient); ok {
		return c.GetObservedRuntime(ctx, q.ProjectID, id)
	}
	return nil, errors.New("telemetry API unavailable")
}
func (p *projectScopedClient) GetAgentTopology(ctx context.Context, project string, id int64) (*AgentTopology, error) {
	q, e := p.scopedTelemetryQuery(TelemetryQuery{ProjectID: project})
	if e != nil {
		return nil, e
	}
	if c, ok := p.inner.(TelemetryClient); ok {
		return c.GetAgentTopology(ctx, q.ProjectID, id)
	}
	return nil, errors.New("telemetry API unavailable")
}

var _ TelemetryClient = (*httpPlatformClient)(nil)
var _ TelemetryClient = (*projectScopedClient)(nil)

func (c *telemetryClient) ListObservedAgents(ctx context.Context, project string) ([]ObservedAgent, error) {
	v := telemetryQueryValues(TelemetryQuery{ProjectID: project})
	var out []ObservedAgent
	e := c.get(ctx, "/agents", v, &out)
	return out, e
}
func (c *httpPlatformClient) ListObservedAgents(ctx context.Context, project string) ([]ObservedAgent, error) {
	return c.telemetry().ListObservedAgents(ctx, project)
}
func (p *projectScopedClient) ListObservedAgents(ctx context.Context, project string) ([]ObservedAgent, error) {
	q, e := p.scopedTelemetryQuery(TelemetryQuery{ProjectID: project})
	if e != nil {
		return nil, e
	}
	if c, ok := p.inner.(TelemetryClient); ok {
		return c.ListObservedAgents(ctx, q.ProjectID)
	}
	return nil, errors.New("telemetry API unavailable")
}

// TelemetryMetadata is the content-free projection. Metadata also understands
// legacy tool-name aliases so consumers do not need payload-shape fallbacks.
type TelemetryMetadata struct {
	ToolName          string  `json:"tool_name,omitempty"`
	Status            string  `json:"status,omitempty"`
	DurationMS        float64 `json:"duration_ms,omitempty"`
	TokensIn          float64 `json:"tokens_in,omitempty"`
	TokensOut         float64 `json:"tokens_out,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	Success           *bool   `json:"success,omitempty"`
	ParentThreadID    string  `json:"parent_thread_id,omitempty"`
	TargetThreadID    string  `json:"target_thread_id,omitempty"`
	TargetAgentID     int64   `json:"target_agent_id,omitempty"`
	CallID            string  `json:"call_id,omitempty"`
	ToolCallID        string  `json:"tool_call_id,omitempty"`
	ProxyConnectionID string  `json:"proxy_connection_id,omitempty"`
	TraceID           string  `json:"trace_id,omitempty"`
	ExecutionID       string  `json:"execution_id,omitempty"`
	ParentExecutionID string  `json:"parent_execution_id,omitempty"`
	RequestID         string  `json:"request_id,omitempty"`
	ToolSpanID        string  `json:"tool_span_id,omitempty"`
}

func (e TelemetryStreamEvent) Metadata() (TelemetryMetadata, error) {
	var out TelemetryMetadata
	if len(e.Data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(e.Data, &out); err != nil {
		return out, err
	}
	if out.ToolName == "" && strings.HasPrefix(e.Type, "tool.") {
		var legacy struct {
			Name string `json:"name"`
			Tool string `json:"tool"`
		}
		_ = json.Unmarshal(e.Data, &legacy)
		out.ToolName = legacy.Name
		if out.ToolName == "" {
			out.ToolName = legacy.Tool
		}
	}
	return out, nil
}
