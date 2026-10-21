package sdk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	FileReferencesVersion                   = "apteva-file-references/v1"
	FileReferencePrefix                     = "blobref://"
	LegacyFileReferencePrefix               = "apteva-file://"
	FileReferenceReadPath                   = "/_apteva/internal/file-references/read"
	HeaderFileReadSignature                 = "X-Apteva-File-Read-Signature"
	FileReferenceSchemaKey                  = "x-apteva-file"
	FileReferencePassthroughSchemaKey       = "x-apteva-file-reference"
	MaxFileReferenceBytes             int64 = 25 << 20
)

// HeaderFileReferenceThread is supplied by trusted runtime HTTP transport,
// never taken from model-generated tool arguments.
const HeaderFileReferenceThread = "X-Apteva-File-Thread"

// FileReference identifies immutable bytes owned by an app. Ref is an opaque
// identifier, not a bearer credential. No bytes or download URL enter prompts.
type FileReference struct {
	Revoked      bool       `json:"revoked,omitempty"`
	Ref          string     `json:"ref"`
	File         bool       `json:"_file"`
	ProjectID    string     `json:"project_id"`
	AttachmentID string     `json:"attachment_id"`
	Version      string     `json:"version"`
	Filename     string     `json:"filename"`
	MIMEType     string     `json:"mimeType"`
	Size         int64      `json:"size"`
	SHA256       string     `json:"sha256"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

type RegisterFileReferenceRequest struct {
	ProjectID    string     `json:"project_id"`
	AttachmentID string     `json:"attachment_id"`
	Version      string     `json:"version"`
	Filename     string     `json:"filename"`
	MIMEType     string     `json:"mimeType"`
	Size         int64      `json:"size"`
	SHA256       string     `json:"sha256"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

type FileReferenceGrant struct {
	Ref      string `json:"ref"`
	AgentID  int64  `json:"agent_id"`
	ThreadID string `json:"thread_id"`
}

// Optional extension: older PlatformClient implementations remain compatible.
// Grants are limited to threads whose server-owned scope belongs to this app.
type FileReferencesClient interface {
	RegisterFileReference(context.Context, RegisterFileReferenceRequest) (*FileReference, error)
	GrantFileReference(context.Context, FileReferenceGrant) error
	RevokeFileReferenceGrant(context.Context, FileReferenceGrant) error
	RevokeFileReference(context.Context, string) error
	GetFileReference(context.Context, string) (*FileReference, error)
}

type FileReferenceError struct {
	Code       string `json:"code"`
	Message    string `json:"error"`
	StatusCode int    `json:"-"`
}

func (e *FileReferenceError) Error() string { return e.Code + ": " + e.Message }

// FileReadRequest is sent only by the authenticated platform resolver. The app
// must look up AttachmentID + Version + ProjectID in its own durable storage.
// Never interpret any field as an arbitrary path or URL.
type FileReadRequest struct {
	FileReference
	AgentID  int64     `json:"agent_id"`
	ThreadID string    `json:"thread_id"`
	Deadline time.Time `json:"deadline"`
}

// FileReferenceSource is implemented by apps that own attachments. Returning a
// FileReferenceError with file_deleted/file_expired/file_inaccessible lets the
// resolver report a stable error. The SDK closes the returned reader.
type FileReferenceSource interface {
	OpenFileReference(context.Context, FileReadRequest) (io.ReadCloser, error)
}

// FileReferenceReadRequest asks the platform for bytes through an app-owned,
// trusted thread scope. It is intended for authorized app presentation flows,
// such as rendering a generated image in a conversation history. It is not a
// model-facing tool or a public download URL.
type FileReferenceReadRequest struct {
	Ref   string             `json:"ref"`
	Scope FileReferenceScope `json:"scope"`
}

// FileReferenceReadResponse contains authoritative metadata and bytes for an
// app presentation flow. The platform checks the app installation, project,
// agent/thread scope and current grant before returning it.
type FileReferenceReadResponse struct {
	FileReference
	Data []byte `json:"data"`
}

// FileReferenceReader is an optional platform extension for apps that need to
// present a granted reference to an authorized user. It does not replace the
// source-reader route used by file-aware tool dispatch.
type FileReferenceReader interface {
	ReadFileReference(context.Context, FileReferenceReadRequest) (*FileReferenceReadResponse, error)
}

// FileArgumentSchema declares a supported tool argument. Tools still receive
// the existing _binary envelope. References can also be used in array items or
// nested properties marked with this helper.
func FileArgumentSchema(description string) map[string]any {
	return map[string]any{FileReferenceSchemaKey: true, "description": description,
		"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "object"}}}
}

// FileReferencePassthroughSchema declares an argument that accepts a shared
// handle while preserving the handle for the destination app. The server
// authenticates and authorizes the reference but does not read its bytes.
func FileReferencePassthroughSchema(description string) map[string]any {
	return map[string]any{FileReferencePassthroughSchemaKey: true, "description": description,
		"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "object"}}}
}

func FileReadSignature(token string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(FileReferencesVersion + "\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func fileReferenceReadHandler(source FileReferenceSource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int, code, message string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(FileReferenceError{Code: code, Message: message})
		}
		if r.Method != http.MethodPost {
			fail(405, "invalid_request", "POST required")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		if err != nil {
			fail(400, "invalid_request", "invalid request")
			return
		}
		token := os.Getenv("APTEVA_APP_TOKEN")
		if token == "" || !hmac.Equal([]byte(r.Header.Get(HeaderFileReadSignature)), []byte(FileReadSignature(token, raw))) {
			fail(403, "file_inaccessible", "platform signature required")
			return
		}
		var req FileReadRequest
		if json.Unmarshal(raw, &req) != nil || req.Deadline.Before(time.Now()) || req.Deadline.After(time.Now().Add(time.Minute)) || req.ProjectID == "" || req.AttachmentID == "" || req.Version == "" || req.Size < 0 || req.Size > MaxFileReferenceBytes {
			fail(400, "invalid_request", "invalid or expired read request")
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), req.Deadline)
		defer cancel()
		body, err := source.OpenFileReference(ctx, req)
		if err != nil {
			var problem *FileReferenceError
			if errors.As(err, &problem) {
				switch problem.Code {
				case "file_deleted":
					fail(410, problem.Code, "attachment deleted")
				case "file_expired":
					fail(410, problem.Code, "attachment expired")
				case "file_inaccessible":
					fail(403, problem.Code, "attachment inaccessible")
				default:
					fail(503, "file_unavailable", "attachment unavailable")
				}
			} else {
				fail(503, "file_unavailable", "attachment unavailable")
			}
			return
		}
		if body == nil {
			fail(503, "file_unavailable", "attachment unavailable")
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		// The resolver checks length and checksum before dispatch. An extra byte
		// makes oversized/mutated sources detectable without buffering in the SDK.
		if _, err = io.Copy(w, io.LimitReader(body, req.Size+1)); err != nil {
			panic(http.ErrAbortHandler)
		}
	})
}

func (c *httpPlatformClient) fileReferenceCall(ctx context.Context, action string, req any, out any) error {
	return c.fileReferenceCallLimit(ctx, action, req, out, 64<<10)
}

func (c *httpPlatformClient) fileReferenceCallLimit(ctx context.Context, action string, req any, out any, limit int64) error {
	return c.fileTransportCall(ctx, c.client, "/api/apps/callback/file-references/"+action, req, out, limit)
}

// Both storage adapters use the same structured error contract.
func (c *httpPlatformClient) fileTransportCall(ctx context.Context, client *http.Client, path string, req any, out any, limit int64) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c.addAuth(r)
	r.Header.Set("Content-Type", "application/json")
	setAppCallDeadline(r)
	resp, err := client.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return &FileReferenceError{Code: "file_response_too_large", Message: "file API response exceeded its limit", StatusCode: resp.StatusCode}
	}
	if resp.StatusCode/100 != 2 {
		problem := &FileReferenceError{StatusCode: resp.StatusCode}
		if json.Unmarshal(data, problem) != nil || problem.Code == "" {
			problem.Code = "file_references_unavailable"
			problem.Message = fmt.Sprintf("file reference API returned HTTP %d", resp.StatusCode)
		}
		return problem
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
func (c *httpPlatformClient) RegisterFileReference(ctx context.Context, req RegisterFileReferenceRequest) (*FileReference, error) {
	var out FileReference
	err := c.fileReferenceCall(ctx, "register", req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *httpPlatformClient) GrantFileReference(ctx context.Context, req FileReferenceGrant) error {
	return c.fileReferenceCall(ctx, "grant", req, nil)
}
func (c *httpPlatformClient) RevokeFileReferenceGrant(ctx context.Context, req FileReferenceGrant) error {
	return c.fileReferenceCall(ctx, "revoke-grant", req, nil)
}
func (c *httpPlatformClient) RevokeFileReference(ctx context.Context, ref string) error {
	return c.fileReferenceCall(ctx, "revoke", map[string]string{"ref": ref}, nil)
}
func (c *httpPlatformClient) GetFileReference(ctx context.Context, ref string) (*FileReference, error) {
	var out FileReference
	err := c.fileReferenceCall(ctx, "get", map[string]string{"ref": ref}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *httpPlatformClient) ReadFileReference(ctx context.Context, req FileReferenceReadRequest) (*FileReferenceReadResponse, error) {
	var out FileReferenceReadResponse
	err := c.fileReferenceCallLimit(ctx, "read", req, &out, 36<<20)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func (p *projectScopedClient) fileReferences() (FileReferencesClient, error) {
	client, ok := p.inner.(FileReferencesClient)
	if !ok {
		return nil, errors.New("platform client does not support file references")
	}
	return client, nil
}
func (p *projectScopedClient) RegisterFileReference(ctx context.Context, req RegisterFileReferenceRequest) (*FileReference, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	if req.ProjectID != p.projectID {
		return nil, errors.New("file reference project differs from scoped client")
	}
	c, err := p.fileReferences()
	if err != nil {
		return nil, err
	}
	return c.RegisterFileReference(ctx, req)
}
func (p *projectScopedClient) GetFileReference(ctx context.Context, ref string) (*FileReference, error) {
	c, err := p.fileReferences()
	if err != nil {
		return nil, err
	}
	out, err := c.GetFileReference(ctx, ref)
	if err == nil && out.ProjectID != p.projectID {
		return nil, errors.New("file reference project differs from scoped client")
	}
	return out, err
}
func (p *projectScopedClient) ReadFileReference(ctx context.Context, req FileReferenceReadRequest) (*FileReferenceReadResponse, error) {
	if req.Scope.ProjectID == "" {
		req.Scope.ProjectID = p.projectID
	}
	if req.Scope.ProjectID != p.projectID {
		return nil, errors.New("file reference read project differs from scoped client")
	}
	client, ok := p.inner.(FileReferenceReader)
	if !ok {
		return nil, errors.New("platform client does not support file reference reads")
	}
	return client.ReadFileReference(ctx, req)
}
func (p *projectScopedClient) GrantFileReference(ctx context.Context, req FileReferenceGrant) error {
	if _, err := p.GetFileReference(ctx, req.Ref); err != nil {
		return err
	}
	c, err := p.fileReferences()
	if err != nil {
		return err
	}
	return c.GrantFileReference(ctx, req)
}
func (p *projectScopedClient) RevokeFileReferenceGrant(ctx context.Context, req FileReferenceGrant) error {
	if _, err := p.GetFileReference(ctx, req.Ref); err != nil {
		return err
	}
	c, err := p.fileReferences()
	if err != nil {
		return err
	}
	return c.RevokeFileReferenceGrant(ctx, req)
}
func (p *projectScopedClient) RevokeFileReference(ctx context.Context, ref string) error {
	if _, err := p.GetFileReference(ctx, ref); err != nil {
		return err
	}
	c, err := p.fileReferences()
	if err != nil {
		return err
	}
	return c.RevokeFileReference(ctx, ref)
}

func IsFileReference(value string) bool {
	return strings.HasPrefix(value, FileReferencePrefix) || strings.HasPrefix(value, LegacyFileReferencePrefix)
}

// FileHandle is the common model-visible descriptor for uploads and tool
// outputs. Source registration metadata and access grants stay server-side.
type FileHandle struct {
	File     bool   `json:"_file"`
	Ref      string `json:"ref"`
	Filename string `json:"filename,omitempty"`
	MIMEType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

// CanonicalFileReference normalizes the old scheme without granting access.
// Reference identifiers remain opaque; validation belongs to the owning server.
func CanonicalFileReference(ref string) string {
	if strings.HasPrefix(ref, LegacyFileReferencePrefix) {
		return FileReferencePrefix + strings.TrimPrefix(ref, LegacyFileReferencePrefix)
	}
	return ref
}

// Read legacy metadata, while marshaling always uses the shared handle fields.
func (f *FileHandle) UnmarshalJSON(raw []byte) error {
	type handle FileHandle
	var wire struct {
		handle
		LegacyMIMEType string `json:"mime_type"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*f = FileHandle(wire.handle)
	f.Ref = CanonicalFileReference(f.Ref)
	if f.MIMEType == "" {
		f.MIMEType = wire.LegacyMIMEType
	}
	return nil
}

func (f FileReference) Handle() FileHandle {
	return FileHandle{File: true, Ref: CanonicalFileReference(f.Ref), Filename: f.Filename, MIMEType: f.MIMEType, Size: f.Size}
}

// ContentPart wraps the same handle for an incoming agent event. The wrapper
// is a transport content type, not a different kind of reference.
func (f FileHandle) ContentPart() map[string]any {
	f.File = true
	f.Ref = CanonicalFileReference(f.Ref)
	return map[string]any{"type": "file_ref", "file_ref": f}
}

// FileReferenceScope is supplied by trusted app backend code, never copied
// from a model tool argument. The server validates app-owned thread scope.
type FileReferenceScope struct {
	ProjectID string `json:"project_id"`
	AgentID   int64  `json:"agent_id"`
	ThreadID  string `json:"thread_id"`
}

// FileIntegrationClient extends the existing connection executor. Apps can
// transfer their granted attachments without downloading/base64 encoding them.
type FileIntegrationClient interface {
	ExecuteIntegrationToolWithFiles(context.Context, int64, string, map[string]any, FileReferenceScope) (*ExecuteResult, error)
}

func (c *httpPlatformClient) ExecuteIntegrationToolWithFiles(ctx context.Context, connID int64, tool string, input map[string]any, scope FileReferenceScope) (*ExecuteResult, error) {
	var out ExecuteResult
	err := c.fileTransportCall(ctx, c.slowClient, fmt.Sprintf("/api/apps/callback/integrations/%d/execute", connID), map[string]any{"tool": tool, "input": input, "file_scope": scope}, &out, 36<<20)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func (p *projectScopedClient) ExecuteIntegrationToolWithFiles(ctx context.Context, connID int64, tool string, input map[string]any, scope FileReferenceScope) (*ExecuteResult, error) {
	if scope.ProjectID == "" {
		scope.ProjectID = p.projectID
	}
	if scope.ProjectID != p.projectID {
		return nil, errors.New("file reference project differs from scoped client")
	}
	c, ok := p.inner.(FileIntegrationClient)
	if !ok {
		return nil, errors.New("platform client does not support file integration calls")
	}
	return c.ExecuteIntegrationToolWithFiles(ctx, connID, tool, input, scope)
}
