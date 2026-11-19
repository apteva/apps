package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// policyWriteError is a refused access-policy write and the HTTP status the
// operator route answers with.
type policyWriteError struct {
	status  int
	message string
}

func (e *policyWriteError) Error() string { return e.message }

var errPolicyRevisionChanged = &policyWriteError{409, "policy revision changed"}

// writePhonePolicy replaces a project's access policy. PUT /access/policy and
// telephony_access_policy_set both use it. Omitted (nil) delegations keep the
// stored ones. The write applies only at the current revision (0 creates the
// first policy), so concurrent edits cannot restore removed access.
func (a *App) writePhonePolicy(project string, p phonePolicy) (phonePolicy, error) {
	if p.Delegations == nil {
		existing, e := a.phonePolicy(project)
		if e != nil {
			return p, &policyWriteError{503, "policy unavailable"}
		}
		p.Delegations = existing.Delegations
	}
	if len(p.Providers) > 0 {
		providers := append([]phoneAuthProvider(nil), p.Providers...)
		for i := range providers {
			if providers[i].URL == "" && providers[i].Format == "apteva-auth" {
				u, e := gatewayProviderURL(project, providers[i])
				if e != nil {
					return p, &policyWriteError{400, e.Error()}
				}
				providers[i].URL = u
			}
		}
		p.Providers = providers
	}
	if e := a.validatePhonePolicy(project, p); e != nil {
		return p, &policyWriteError{400, e.Error()}
	}
	raw, _ := json.Marshal(p)
	var res sql.Result
	var e error
	if p.Revision == 0 {
		res, e = a.db().db.Exec(`INSERT INTO telephony_access_policies(project_id,revision,policy_json) VALUES(?,1,?) ON CONFLICT(project_id) DO NOTHING`, project, string(raw))
	} else {
		res, e = a.db().db.Exec(`UPDATE telephony_access_policies SET revision=revision+1,policy_json=? WHERE project_id=? AND revision=?`, string(raw), project, p.Revision)
	}
	if e != nil {
		return p, &policyWriteError{500, "policy unavailable"}
	}
	n, e := res.RowsAffected()
	if e != nil {
		return p, &policyWriteError{500, "policy unavailable"}
	}
	if n != 1 {
		return p, errPolicyRevisionChanged
	}
	a.routingCommitted(project)
	a.revokeCallListeners(project)
	p.Revision++
	return p, nil
}

// gatewayProviderURL is the /me endpoint of an Apteva Auth install, reached
// through the platform gateway. The gateway origin is already the only
// internal address a provider may use, so this adds no new target. It lets a
// caller that only knows the install number, such as an Environments seed
// using {"$runtime_install_id": "auth"}, configure the provider.
func gatewayProviderURL(project string, p phoneAuthProvider) (string, error) {
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if gateway == "" {
		return "", errors.New("provider url required: no platform gateway is configured")
	}
	if !appNameValid(p.IssuerApp) || !installIDValid(p.IssuerInstallID) {
		return "", errors.New("provider url required unless issuer_app is an app name and issuer_install_id an install number")
	}
	return gateway + "/api/apps/" + p.IssuerApp + "/_install/" + p.IssuerInstallID + "/me?project_id=" + url.QueryEscape(project), nil
}

func appNameValid(name string) bool {
	if name == "" || len(name) > 64 || name[0] == '-' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func installIDValid(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

// requirePlatformPolicyCaller admits API keys, the panel and Environments
// seeds. Agents could otherwise add an identity provider they control and grant
// themselves call access. Bound apps use the scoped resource tools, and
// application users the softphone API; the shared tool wrappers refuse them
// too, and this check keeps that true if the wrappers change.
func requirePlatformPolicyCaller(c context.Context) error {
	caller := sdk.CallerFrom(c)
	if caller == nil {
		return nil
	}
	if caller.AgentID > 0 {
		return errors.New("Telephony access is managed by the platform: agents cannot read or change it")
	}
	if caller.AppInstallID > 0 || caller.AppName != "" {
		return errors.New("bound services must use scoped resource administration")
	}
	if caller.SubjectID != "" || caller.SubjectType != "" {
		return errors.New("application users must use the authorized softphone API")
	}
	return nil
}

// The tools return Go errors, not error content, so a refused write fails an
// Environments seed instead of passing as a result.
func (a *App) toolAccessPolicyGet(c context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	if err := requirePlatformPolicyCaller(c); err != nil {
		return nil, err
	}
	p, err := a.phonePolicy(currentProject(ctx))
	if err != nil {
		return nil, errors.New("policy unavailable")
	}
	return p, nil
}

func (a *App) toolAccessPolicySet(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := requirePlatformPolicyCaller(c); err != nil {
		return nil, err
	}
	p, err := policyFromToolArgs(args)
	if err != nil {
		return nil, err
	}
	return a.writePhonePolicy(currentProject(ctx), p)
}

// policyFromToolArgs reads a whole policy from tool arguments. Unknown fields
// are refused: the write replaces the policy, so a misspelled "users" would
// otherwise remove every user. issuer_install_id may be a number, which is
// what {"$runtime_install_id": ...} resolves to in Environments seeds.
func policyFromToolArgs(args map[string]any) (phonePolicy, error) {
	var p phonePolicy
	if _, ok := args["revision"]; !ok {
		return p, errors.New("revision required: 0 creates the first policy, otherwise pass the revision from telephony_access_policy_get")
	}
	body := make(map[string]any, len(args))
	for key, value := range args {
		if !strings.HasPrefix(key, "_") {
			body[key] = installIDsAsStrings(value)
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return p, fmt.Errorf("invalid policy: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid policy: %v", err)
	}
	return p, nil
}

// installIDsAsStrings rewrites whole-number issuer_install_id values as the
// decimal strings the policy stores.
func installIDsAsStrings(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "issuer_install_id" {
				if id, ok := wholeNumberString(item); ok {
					out[key] = id
					continue
				}
			}
			out[key] = installIDsAsStrings(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = installIDsAsStrings(item)
		}
		return out
	}
	return value
}

func wholeNumberString(value any) (string, bool) {
	switch n := value.(type) {
	case float64:
		if n > 0 && n <= 1<<53 && n == math.Trunc(n) {
			return strconv.FormatInt(int64(n), 10), true
		}
	case int:
		if n > 0 {
			return strconv.Itoa(n), true
		}
	case int64:
		if n > 0 {
			return strconv.FormatInt(n, 10), true
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return strconv.FormatInt(i, 10), true
		}
	}
	return "", false
}
