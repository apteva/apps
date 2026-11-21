package main

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// selectAuthMethod uses credential carriers only. Verification never falls
// through to another method after a credential or backend failure.
func selectAuthMethod(r *http.Request, policy authorizerPolicy) (authorizerPolicy, *http.Request, error) {
	if policy.Kind != "any_of" {
		return policy, r, nil
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return policy, r, authFailure(400, "invalid request query", nil)
	}
	authorization := r.Header.Values("Authorization")
	keyHeaders := r.Header.Values("X-API-Key")
	keyQuery := query["api_key"]
	if len(authorization) > 1 || len(keyHeaders) > 1 || len(keyQuery) > 1 || (len(keyHeaders) > 0 && len(keyQuery) > 0) {
		return policy, r, authFailure(400, "ambiguous authentication credentials", nil)
	}
	keyPresent := len(keyHeaders) > 0 || len(keyQuery) > 0
	bearerPresent := len(authorization) > 0
	fields := strings.Fields(r.Header.Get("Authorization"))
	validBearer := len(fields) == 2 && strings.EqualFold(fields[0], "Bearer")
	appToken := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	if validBearer && appToken != "" && subtle.ConstantTimeCompare([]byte(fields[1]), []byte(appToken)) == 1 {
		// The platform may inject this private app credential when proxying a
		// request without client Authorization. Public metadata cannot mark an
		// arbitrary bearer credential as internal.
		bearerPresent = false
	}
	if bearerPresent && keyPresent {
		return policy, r, authFailure(400, "ambiguous authentication credentials", nil)
	}
	if !bearerPresent && !keyPresent {
		return policy, r, authFailure(401, "missing authentication credential", nil)
	}
	if bearerPresent && !validBearer {
		return policy, r, authFailure(401, "invalid bearer token", nil)
	}
	if keyPresent && strings.TrimSpace(r.Header.Get("X-API-Key")) == "" && strings.TrimSpace(query.Get("api_key")) == "" {
		return policy, r, authFailure(401, "invalid api key", nil)
	}
	for _, raw := range policy.Methods {
		method, err := parseAuthPolicy(string(raw))
		if err != nil {
			return policy, r, authFailure(500, "invalid authentication configuration", nil)
		}
		if (method.Kind == "api_key") != keyPresent {
			continue
		}
		if bearerPresent {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+fields[1])
		}
		return method, r, nil
	}
	return policy, r, authFailure(500, "invalid authentication configuration", nil)
}
