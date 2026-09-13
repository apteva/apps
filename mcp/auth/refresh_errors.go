package main

import (
	"database/sql"
	"errors"
	"net/http"
)

// A failed commit can have an unknown outcome. Never tell a rotating client
// that the old credential is safe to replay in that case.
var errRefreshUncertain = errors.New("refresh outcome uncertain")

// Missing identity/session rows are rejection; missing infrastructure rows
// (for example a signing key) are service failures and must remain retryable.
func sessionLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("invalid_grant")
	}
	return err
}

func writeRefreshError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRefreshUncertain) {
		httpStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "refresh_uncertain"})
		return
	}
	definitive := false
	switch err.Error() {
	case "invalid_grant", "refresh_token_reuse: session revoked", "client_not_allowed", "invalid_client", "unknown client_id", "client disabled", "organization_inactive", "user_inactive", "account_locked", "email_unverified", "mfa_required: MFA authentication is not implemented":
		definitive = true
	}
	if definitive {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	// These failures occur before rotation, or in a transaction rolled back by
	// refreshSession. This explicit code permits retry; generic proxy 5xx does not.
	w.Header().Set("Retry-After", "1")
	httpStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "refresh_unavailable"})
}
