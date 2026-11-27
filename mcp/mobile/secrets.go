package main

import (
	"encoding/json"
	"strings"
	"time"
)

// mobileSecret is one entry of the encrypted mobile_secrets setting:
// project ID -> reference -> entry. Same shape and expiry rule as Actors'
// browser_secrets, bound to an Android package instead of a host.
type mobileSecret struct {
	Value      string `json:"value"`
	Package    string `json:"package"`
	ResourceID string `json:"resource_id"`
	DeviceID   string `json:"device_id"`
	ExpiresAt  string `json:"expires_at"`
}

// secretDeviceArg carries a reference's device binding from the sidecar to the
// engine, which knows the session's device. Never accepted from callers.
const secretDeviceArg = "_secret_device_id"

func loadMobileSecret(raw, project, ref string, now time.Time) (mobileSecret, error) {
	var projects map[string]map[string]mobileSecret
	if json.Unmarshal([]byte(raw), &projects) != nil {
		return mobileSecret{}, problem("secret_unavailable", "secret configuration is unavailable")
	}
	secret, ok := projects[project][ref]
	if !ok || secret.Value == "" {
		return mobileSecret{}, problem("secret_unavailable", "secret reference is unavailable for this project")
	}
	expiry, err := time.Parse(time.RFC3339, secret.ExpiresAt)
	if err != nil || !now.Before(expiry) {
		return mobileSecret{}, problem("secret_unavailable", "secret reference has expired or has no valid expiry")
	}
	if !appIDPattern.MatchString(secret.Package) {
		return mobileSecret{}, problem("secret_unavailable", "secret reference is not bound to an Android package")
	}
	return secret, nil
}

// resolveSecureText rewrites a secure_text call in place: exactly one of
// secret_ref or password; a reference is replaced by its value and its
// bindings become guards. Errors never contain the value. It returns a copy so
// the caller's map (which may be logged by the caller) never holds the value.
func resolveSecureText(args map[string]any, project string, get func(string) string, now time.Time) (map[string]any, error) {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	delete(out, secretDeviceArg)
	if str(args, "action") != "secure_text" {
		delete(out, "secret_ref")
		if _, ok := args["secret_ref"]; ok {
			return nil, problem("invalid_argument", "secret_ref is only accepted by secure_text")
		}
		return out, nil
	}
	_, hasPassword := args["password"]
	ref, hasRef := args["secret_ref"]
	if hasPassword == hasRef {
		return nil, problem("invalid_argument", "secure_text requires exactly one of secret_ref or password")
	}
	if hasPassword {
		if strings.EqualFold(strings.TrimSpace(get("allow_literal_secure_text")), "false") {
			return nil, problem("forbidden", "literal passwords are disabled; use secret_ref")
		}
		return out, nil
	}
	name, ok := ref.(string)
	if !ok || name == "" || len(name) > 200 {
		return nil, problem("invalid_argument", "secret_ref must be a non-empty string")
	}
	secret, err := loadMobileSecret(get("mobile_secrets"), project, name, now)
	if err != nil {
		return nil, err
	}
	if pkg := str(args, "expected_package"); pkg != "" && pkg != secret.Package {
		return nil, problem("forbidden", "secret is not bound to this package")
	}
	if rid := str(args, "expected_resource_id"); secret.ResourceID != "" && rid != "" && rid != secret.ResourceID {
		return nil, problem("forbidden", "secret is not bound to this field")
	}
	out["expected_package"] = secret.Package
	if secret.ResourceID != "" {
		out["expected_resource_id"] = secret.ResourceID
	}
	if secret.DeviceID != "" {
		out[secretDeviceArg] = secret.DeviceID
	}
	delete(out, "secret_ref")
	out["password"] = secret.Value
	return out, nil
}

// secretDeviceAllowed checks a reference's device binding against the
// session's device (its inventory ID or serial/UDID).
func secretDeviceAllowed(args map[string]any, d Device) bool {
	want := str(args, secretDeviceArg)
	return want == "" || want == d.ID || want == d.UDID
}
