package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const trustedProjectHeader = "X-Apteva-Project-ID"

// requestProjectID resolves the project identity already authorized by the
// gateway. Query and body values are selectors only and may never override it.
func requestProjectID(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("request required")
	}
	trusted := strings.TrimSpace(r.Header.Get(trustedProjectHeader))
	query := strings.TrimSpace(r.URL.Query().Get("project_id"))
	current := ""
	if globalCtx != nil {
		current = strings.TrimSpace(globalCtx.CurrentProject())
	}
	authorized := trusted
	if authorized == "" {
		authorized = current
	}
	if authorized != "" {
		if query != "" && query != authorized {
			return "", fmt.Errorf("project_id %q does not match authorized project", query)
		}
		return authorized, nil
	}
	// Global installs receive a gateway-authorized project in both the query
	// and trusted header. Retaining the query fallback keeps direct SDK tests
	// and project-scoped legacy installs usable without weakening mismatch checks.
	if query != "" {
		return query, nil
	}
	return "", errors.New("project context required")
}

func requireRequestProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID, err := requestProjectID(r)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "does not match") {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return "", false
	}
	return projectID, true
}

func assignRequestProject(bodyProjectID, requestProjectID string) (string, error) {
	bodyProjectID = strings.TrimSpace(bodyProjectID)
	if bodyProjectID != "" && bodyProjectID != requestProjectID {
		return "", fmt.Errorf("project_id %q does not match authorized project", bodyProjectID)
	}
	return requestProjectID, nil
}
