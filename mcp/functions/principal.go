package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

// InvocationPolicy is independent of outbound access and source deployments.
// Missing policy preserves legacy entry points and denies principal admission.
type InvocationPolicy struct {
	RequireAuthenticated bool              `json:"require_authenticated"`
	AuthenticatedCallers []PrincipalCaller `json:"authenticated_callers"`
}
type PrincipalCaller struct {
	InstallationID int64    `json:"installation_id"`
	Issuers        []string `json:"issuers"`
}

// Principal is a bounded assertion from a configured, platform-verified app.
// Claims are opaque to Functions. Session/bearer credentials are not accepted.
type Principal struct {
	Subject     string         `json:"subject"`
	Issuer      string         `json:"issuer"`
	ProjectID   string         `json:"project_id"`
	FunctionIDs []int64        `json:"function_ids"`
	Claims      map[string]any `json:"claims,omitempty"`
}

// InvocationIdentity is safe audit metadata, with no claims or credentials.
type InvocationIdentity struct {
	Kind                 string `json:"kind"`
	Subject              string `json:"subject,omitempty"`
	Issuer               string `json:"issuer,omitempty"`
	ProjectID            string `json:"project_id"`
	CallerInstallationID int64  `json:"caller_installation_id,omitempty"`
	CallerApp            string `json:"caller_app,omitempty"`
	RequestID            string `json:"request_id"`
	ParentInvocationID   int64  `json:"parent_invocation_id,omitempty"`
}

type invocationSecurity struct {
	Principal *Principal
	Identity  InvocationIdentity
}
type securityKey struct{}

func securityFrom(ctx context.Context) *invocationSecurity {
	s, _ := ctx.Value(securityKey{}).(*invocationSecurity)
	return s
}

var errInvocationDenied = errors.New("invocation identity or scope denied")

func strictDecode(raw any, out any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if len(b) > 32<<10 {
		return errors.New("invocation policy or principal exceeds 32 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func parseInvocationPolicy(raw any) (*InvocationPolicy, error) {
	if raw == nil {
		return nil, nil
	}
	var p InvocationPolicy
	if err := strictDecode(raw, &p); err != nil {
		return nil, err
	}
	if len(p.AuthenticatedCallers) > 100 {
		return nil, errors.New("too many authenticated callers")
	}
	seen := map[int64]bool{}
	for _, c := range p.AuthenticatedCallers {
		if c.InstallationID <= 0 || seen[c.InstallationID] || len(c.Issuers) == 0 || len(c.Issuers) > 100 {
			return nil, errors.New("authenticated callers require unique installation IDs and explicit issuers")
		}
		seen[c.InstallationID] = true
		for _, issuer := range c.Issuers {
			if !validIdentityText(issuer) {
				return nil, errors.New("invalid issuer")
			}
		}
	}
	return &p, nil
}
func validIdentityText(s string) bool {
	return s != "" && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func (p *InvocationPolicy) allows(installation int64, issuer string) bool {
	if p == nil || installation <= 0 {
		return false
	}
	for _, c := range p.AuthenticatedCallers {
		if c.InstallationID == installation && slices.Contains(c.Issuers, issuer) {
			return true
		}
	}
	return false
}

// Reject caller credentials only in identity claims. Business event fields
// are opaque, even when named password, token, or credentials. No role/claim
// semantics are evaluated, and Functions never calls an identity provider.
func containsIdentityCredentials(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, x := range v {
			key = strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(key))
			switch key {
			case "token", "bearertoken", "apikey", "authorization", "cookie", "setcookie", "session", "sessionid", "sessiontoken", "accesstoken", "refreshtoken", "idtoken", "password", "clientsecret", "credentials":
				return true
			}
			if containsIdentityCredentials(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if containsIdentityCredentials(x) {
				return true
			}
		}
	}
	return false
}

// Only app_only MCP calls can supply a principal. The callback gateway binds
// the caller installation and replaces _project_id after permission checks.
// Ordinary proxy/model calls cannot create this context. On older platforms
// missing a bound caller this entry point fails closed.
func (a *App) toolInvokeAuthenticated(parent context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	caller := sdk.CallerFrom(parent)
	if caller == nil || caller.AppInstallID <= 0 || caller.AppName == "" || securityFrom(parent) != nil {
		return nil, errInvocationDenied
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil || pid == "" {
		return nil, errInvocationDenied
	}
	if scoped, ok := args["_project_id"].(string); ok && scoped != pid {
		return nil, errInvocationDenied
	}
	if caller.ProjectID != "" && caller.ProjectID != pid {
		return nil, errInvocationDenied
	}
	var principal Principal
	if args["principal"] == nil || strictDecode(args["principal"], &principal) != nil {
		return nil, errInvocationDenied
	}
	if !validIdentityText(principal.Subject) || !validIdentityText(principal.Issuer) || principal.ProjectID != pid || len(principal.FunctionIDs) == 0 || len(principal.FunctionIDs) > 100 || containsIdentityCredentials(principal.Claims) {
		return nil, errInvocationDenied
	}
	for _, id := range principal.FunctionIDs {
		if id <= 0 {
			return nil, errInvocationDenied
		}
	}
	// The authenticating caller supplies the original bounded request deadline.
	deadline, err := time.Parse(time.RFC3339Nano, strArg(args, "deadline"))
	if err != nil || !deadline.After(time.Now()) {
		return nil, errInvocationDenied
	}
	parent, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	requestID := strArg(args, "request_id")
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if !correlationPattern.MatchString(requestID) {
		return nil, errInvocationDenied
	}
	parent = context.WithValue(parent, correlationKey{}, requestID)
	parent = context.WithValue(parent, securityKey{}, &invocationSecurity{Principal: &principal, Identity: InvocationIdentity{Kind: "authenticated", Subject: principal.Subject, Issuer: principal.Issuer, ProjectID: pid, CallerInstallationID: caller.AppInstallID, CallerApp: caller.AppName, RequestID: requestID}})
	return a.toolInvokeContext(parent, ctx, args)
}

func invocationAdmission(parent context.Context, fn *Function, trigger string) (context.Context, *invocationSecurity, error) {
	s := &invocationSecurity{}
	if inherited := securityFrom(parent); inherited != nil {
		*s = *inherited
	}
	if s.Principal != nil {
		p := s.Principal
		if p.ProjectID != fn.ProjectID || !slices.Contains(p.FunctionIDs, fn.ID) || !fn.InvocationPolicy.allows(s.Identity.CallerInstallationID, p.Issuer) {
			return parent, nil, errInvocationDenied
		}
	} else {
		if fn.InvocationPolicy != nil && fn.InvocationPolicy.RequireAuthenticated {
			return parent, nil, errInvocationDenied
		}
		if s.Identity.Kind == "" {
			s.Identity.Kind = "anonymous"
			if caller := sdk.CallerFrom(parent); caller != nil {
				if caller.ProjectID != "" && caller.ProjectID != fn.ProjectID {
					return parent, nil, errInvocationDenied
				}
				if caller.AppInstallID > 0 {
					s.Identity.Kind = "service"
					s.Identity.Subject = strconv.FormatInt(caller.AppInstallID, 10)
					s.Identity.Issuer = "apteva:installation"
					s.Identity.CallerInstallationID = caller.AppInstallID
					s.Identity.CallerApp = caller.AppName
				} else if caller.AgentID > 0 {
					s.Identity.Kind = "agent"
					s.Identity.Subject = strconv.FormatInt(caller.AgentID, 10)
					s.Identity.Issuer = "apteva:agent"
				}
			}
			if trigger == "function_url" {
				s.Identity.Kind = "function_url"
			}
		}
	}
	s.Identity.ProjectID = fn.ProjectID
	if s.Identity.RequestID == "" {
		s.Identity.RequestID = correlationID(parent)
	}
	if s.Identity.RequestID == "" {
		s.Identity.RequestID = uuid.NewString()
	}
	if trace := traceFrom(parent); trace != nil {
		s.Identity.ParentInvocationID = trace.InvocationID
	}
	parent = context.WithValue(parent, correlationKey{}, s.Identity.RequestID)
	parent = context.WithValue(parent, securityKey{}, s)
	return parent, s, nil
}

// Round-trip the payload to remove aliases, custom marshalers, and RawMessage
// bypasses. The reserved authorizer is always replaced. Legacy scalar/array
// payloads remain unchanged; authenticated events must be objects.
func trustedEvent(event any, s *invocationSecurity) (any, error) {
	b, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(b) > maxFrame {
		return nil, errors.New("event exceeds 8 MiB")
	}
	var copied any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = d.Decode(&copied); err != nil {
		return nil, err
	}
	obj, ok := copied.(map[string]any)
	if !ok {
		if s.Principal != nil {
			return nil, errors.New("authenticated event must be a JSON object")
		}
		return copied, nil
	}
	rc, _ := obj["requestContext"].(map[string]any)
	if rc != nil {
		delete(rc, "authorizer")
	}
	if s.Principal != nil {
		// Business payloads are opaque. Only the principal is credential-checked;
		// authenticated event bodies are omitted from invocation history.
		if rc == nil {
			rc = map[string]any{}
			obj["requestContext"] = rc
		}
		// Independent serialized copy: worker edits cannot touch dispatcher identity.
		rc["authorizer"] = map[string]any{"principal": s.Principal, "claims": s.Principal.Claims}
	}
	return copied, nil
}

func invocationIdentityFromRequest(r *http.Request) *http.Request {
	// SDK HTTP routes are installation-token protected; these headers are minted
	// by the platform app bridge, stripped on ordinary/public proxies.
	id, _ := strconv.ParseInt(r.Header.Get(sdk.HeaderBoundCallerInstallID), 10, 64)
	if id <= 0 || r.Header.Get(sdk.HeaderBoundCallerAppName) == "" {
		return r
	}
	return r.WithContext(sdk.WithCaller(r.Context(), &sdk.Caller{AppInstallID: id, AppName: r.Header.Get(sdk.HeaderBoundCallerAppName), ProjectID: r.Header.Get("X-Apteva-Project-ID")}))
}

func writeInvocationDenied(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errInvocationDenied) {
		return false
	}
	httpErr(w, http.StatusForbidden, errInvocationDenied.Error())
	return true
}

func invocationPolicySchema() map[string]any {
	return map[string]any{"type": "object", "description": "Invocation admission policy. require_authenticated defaults false. authenticated_callers is a list of {installation_id, issuers:[exact issuer strings]}. No configured caller can supply a principal by default."}
}
func authenticatedInvocationTool(a *App) sdk.Tool {
	return sdk.Tool{Name: "functions_invoke_authenticated", Exposure: sdk.ToolExposureAppOnly, Description: "Invoke with a bounded principal from a configured platform-verified installation. No session credentials. event must be an object. deadline is the original request deadline in RFC3339 format.", InputSchema: schemaObject(map[string]any{
		"id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
		"principal": map[string]any{"type": "object", "description": "subject, issuer, project_id, function_ids (exact IDs, including nested targets), optional opaque claims"},
		"event":     map[string]any{"type": "object"}, "deadline": map[string]any{"type": "string"}, "request_id": map[string]any{"type": "string"},
	}, []string{"principal", "event", "deadline"}), HandlerCtx: a.toolInvokeAuthenticated}
}

func decodeStoredInvocationPolicy(b string, out **InvocationPolicy) error {
	if err := json.Unmarshal([]byte(b), out); err != nil {
		return fmt.Errorf("invalid invocation policy: %w", err)
	}
	return nil
}
