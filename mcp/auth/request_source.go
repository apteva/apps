package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// clientAddr resolves the address of the party that originated a request.
// Behind the platform proxy RemoteAddr is always the loopback, so the
// platform-signed client IP is authoritative. trusted is false when no signed
// IP was present (an older server, a direct local call, or a synthetic
// request); forged or malformed metadata is ignored and also reported as
// untrusted. Raw X-Forwarded-For style headers are never consulted.
func clientAddr(r *http.Request) (ip string, trusted bool) {
	if r == nil {
		return "", false
	}
	if signed, err := sdk.ClientIPFromRequest(r); err == nil && signed != "" {
		return signed, true
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host, false
	}
	return r.RemoteAddr, false
}

func clientIP(r *http.Request) string {
	ip, _ := clientAddr(r)
	return ip
}

// auditSource describes who caused an audited event and through which path.
// ActorType is one of: platform_user (a dashboard/admin user identified by the
// platform-signed principal), agent, app, self (the end user acting on their
// own account), anonymous, or system.
type auditSource struct {
	IP              string
	UserAgent       string
	ActorType       string
	ActorID         string
	SourceApp       string
	SourceInstallID int64
	CredentialID    string
	CorrelationID   string
}

func (s auditSource) withCredential(id string) auditSource {
	s.CredentialID = id
	return s
}

func requestCorrelationID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, h := range []string{"X-Request-Id", "X-Correlation-Id"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" && len(v) <= 128 {
			return v
		}
	}
	id, _ := randSlug(12)
	return id
}

// auditFromRequest builds the audit source for an HTTP request. The actor is
// taken only from the platform-signed principal; unsigned identity headers on
// public routes are attacker-controlled and are never trusted.
func auditFromRequest(r *http.Request) auditSource {
	if r == nil {
		return auditSource{ActorType: "system"}
	}
	s := auditSource{IP: clientIP(r), UserAgent: r.UserAgent(), CorrelationID: requestCorrelationID(r)}
	if p, err := sdk.PrincipalFromRequest(r); err == nil && p != nil {
		switch {
		case p.UserID > 0:
			s.ActorType, s.ActorID = "platform_user", strconv.FormatInt(p.UserID, 10)
		case p.SubjectID != "":
			s.ActorType, s.ActorID = "subject:"+p.SubjectType, p.SubjectID
		}
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-Apteva-App-Install-ID")), 10, 64); err == nil && id > 0 && s.ActorType != "" {
		s.SourceInstallID = id
	}
	return s
}

// auditSelf attributes an action to the end user authenticated by their own
// bearer session (self-service routes).
func auditSelf(r *http.Request, uid int64) auditSource {
	s := auditFromRequest(r)
	if s.ActorType == "" {
		s.ActorType, s.ActorID = "self", strconv.FormatInt(uid, 10)
	}
	return s
}

// auditFromCaller builds the audit source for an MCP tool call. Caller fields
// come from server-owned headers parsed by the SDK, never from tool arguments.
func auditFromCaller(c *sdk.Caller) auditSource {
	s := auditSource{UserAgent: "mcp", ActorType: "agent"}
	if c == nil {
		return s
	}
	s.CorrelationID = c.ToolCallID
	if c.AppName != "" {
		s.ActorType = "app"
		s.ActorID = c.AppName
		s.SourceApp = c.AppName
		s.SourceInstallID = c.AppInstallID
	}
	if c.AgentID > 0 {
		s.ActorType = "agent"
		s.ActorID = strconv.FormatInt(c.AgentID, 10)
	}
	if c.SubjectID != "" && c.AgentID <= 0 && c.AppName == "" {
		s.ActorType, s.ActorID = "subject:"+c.SubjectType, c.SubjectID
	}
	if s.CorrelationID == "" {
		s.CorrelationID = c.ThreadID
	}
	return s
}

// callerArgKey carries the SDK caller into Handler-style tools. The value is a
// Go pointer, which JSON-decoded model arguments can never produce.
const callerArgKey = "\x00auth_caller"

// auditSourceArgKey lets an HTTP admin handler that delegates to a tool
// function keep the platform user it authenticated as the recorded actor.
const auditSourceArgKey = "\x00auth_audit_source"

func auditFromArgs(args map[string]any) auditSource {
	if s, ok := args[auditSourceArgKey].(auditSource); ok {
		return s
	}
	c, _ := args[callerArgKey].(*sdk.Caller)
	return auditFromCaller(c)
}

// withCallerArgs adapts every Handler-style tool so it can attribute audit
// rows to the calling agent or app.
func withCallerArgs(tools []sdk.Tool) []sdk.Tool {
	for i := range tools {
		if tools[i].HandlerCtx != nil || tools[i].Handler == nil {
			continue
		}
		h := tools[i].Handler
		tools[i].Handler = nil
		tools[i].HandlerCtx = func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			if args == nil {
				args = map[string]any{}
			}
			delete(args, callerArgKey)
			delete(args, auditSourceArgKey)
			if c := sdk.CallerFrom(ctx); c != nil {
				args[callerArgKey] = c
			}
			return h(app, args)
		}
	}
	return tools
}
