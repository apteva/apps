package main

// actor.go — who made a Fleet call, recorded on every audit event.
//
// Fleet's audit timeline used to record the literal "user" for every
// operator action, so it could not say who revealed a key or reset a
// password. The platform already tells each tool call who is calling
// (sdk.Caller: agent + thread, bound sibling app, or delegated subject) and
// signs the end user onto each panel HTTP request (sdk.TrustedPrincipal).
// Fleet stamps that identity into the call's arguments under actorArg,
// overwriting anything the model supplied, and handlers read it back with
// actorOf when they record an event.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// actorArg is the server-owned argument key carrying the caller identity.
// Any value a model sends under this key is overwritten before dispatch.
const actorArg = "_fleet_actor"

// actorUnknown is recorded when the platform supplied no caller at all
// (an older platform or a direct in-process call).
const actorUnknown = "unknown"

// callerActor renders an sdk.Caller as a stable, greppable actor string:
//
//	app:<name>#<install>                 a bound sibling app
//	subject:<type>:<id>[ <email>]        a delegated end user
//	agent:<id>[/thread:<thread>]         an agent (and its thread)
//	platform                             a platform call with no principal
func callerActor(c *sdk.Caller) string {
	if c == nil {
		return actorUnknown
	}
	switch {
	case strings.TrimSpace(c.AppName) != "":
		out := "app:" + strings.TrimSpace(c.AppName)
		if c.AppInstallID > 0 {
			out += "#" + strconv.FormatInt(c.AppInstallID, 10)
		}
		return out
	case strings.TrimSpace(c.SubjectType) != "" && strings.TrimSpace(c.SubjectID) != "":
		out := "subject:" + strings.TrimSpace(c.SubjectType) + ":" + strings.TrimSpace(c.SubjectID)
		if email := strings.TrimSpace(c.SubjectEmail); email != "" {
			out += " " + email
		}
		return out
	case c.AgentID > 0:
		out := "agent:" + strconv.FormatInt(c.AgentID, 10)
		if thread := strings.TrimSpace(c.ThreadID); thread != "" {
			out += "/thread:" + thread
		}
		return out
	default:
		return "platform"
	}
}

// principalActor renders the platform-signed end user on a panel request.
// Unsigned or missing principals record "panel" — the request still passed
// Fleet's app-token check, so it came through the platform proxy.
func principalActor(r *http.Request) string {
	p, err := sdk.PrincipalFromRequest(r)
	if err != nil || p == nil {
		return "panel"
	}
	switch {
	case p.UserID > 0:
		out := "user:" + strconv.FormatInt(p.UserID, 10)
		if email := strings.TrimSpace(p.Email); email != "" {
			out += " " + email
		}
		return out
	case p.SubjectType != "" && p.SubjectID != "":
		out := "subject:" + p.SubjectType + ":" + p.SubjectID
		if email := strings.TrimSpace(p.SubjectEmail); email != "" {
			out += " " + email
		}
		return out
	default:
		return "panel"
	}
}

// withActor returns a copy of args carrying actor under actorArg.
func withActor(args map[string]any, actor string) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	if strings.TrimSpace(actor) == "" {
		actor = actorUnknown
	}
	out[actorArg] = actor
	return out
}

// actorOf reads the stamped caller identity back out of tool arguments.
func actorOf(args map[string]any) string {
	if args != nil {
		if s, ok := args[actorArg].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return actorUnknown
}

// stampCallerActors wraps every tool so the platform-supplied caller is
// stamped into its arguments before the handler runs.
func stampCallerActors(tools []sdk.Tool) []sdk.Tool {
	for i := range tools {
		t := &tools[i]
		switch {
		case t.HandlerCtx != nil:
			inner := t.HandlerCtx
			t.HandlerCtx = func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				return inner(ctx, app, withActor(args, callerActor(sdk.CallerFrom(ctx))))
			}
		case t.Handler != nil:
			inner := t.Handler
			t.Handler = nil
			t.HandlerCtx = func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				return inner(app, withActor(args, callerActor(sdk.CallerFrom(ctx))))
			}
		}
	}
	return tools
}
