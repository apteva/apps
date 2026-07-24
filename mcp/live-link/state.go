package main

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type RuntimeState struct {
	ActiveProvider string
	DesiredLive    bool
}

func dbRuntimeState(db *sql.DB) (*RuntimeState, error) {
	row := db.QueryRow(`SELECT active_provider, desired_live FROM runtime_state WHERE id = 1`)
	var state RuntimeState
	var desired int
	if err := row.Scan(&state.ActiveProvider, &desired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	state.DesiredLive = desired != 0
	return &state, nil
}

func dbInitRuntimeState(db *sql.DB, provider string, desired bool) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO runtime_state (id, active_provider, desired_live)
		 VALUES (1, ?, ?)`, provider, boolInt(desired))
	return err
}

func dbSetActiveProvider(db *sql.DB, provider string) error {
	_, err := db.Exec(
		`INSERT INTO runtime_state (id, active_provider, desired_live)
		 VALUES (1, ?, 0)
		 ON CONFLICT(id) DO UPDATE SET
		   active_provider = excluded.active_provider,
		   desired_live = 0,
		   updated_at = CURRENT_TIMESTAMP`, provider)
	return err
}

func dbSetDesiredLive(db *sql.DB, desired bool) error {
	_, err := db.Exec(
		`INSERT INTO runtime_state (id, active_provider, desired_live)
		 VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   desired_live = excluded.desired_live,
		   updated_at = CURRENT_TIMESTAMP`, providerNameQuick, boolInt(desired))
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func validProviderName(name string) bool {
	switch name {
	case providerNameQuick, providerNameNamed, providerNameNgrok, providerNameZrok:
		return true
	default:
		return false
	}
}

// normalizeZrokName applies zrok v2's documented public-name grammar.
// Names are DNS labels in a namespace, so mixed case, dots, underscores,
// and leading/trailing hyphens are rejected rather than silently rewritten.
func normalizeZrokName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if len(name) < 3 || len(name) > 63 {
		return "", errors.New("zrok name must be between 3 and 63 characters")
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return "", errors.New("zrok name cannot start or end with a hyphen")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", errors.New("zrok name must use lowercase ASCII letters, digits, and hyphens")
	}
	return name, nil
}

// normalizeDNSName accepts an ASCII DNS hostname and returns its canonical
// lower-case form. Unicode names must be entered as punycode so the value sent
// to Cloudflare, ngrok, and SQLite is identical.
func normalizeDNSName(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" || len(host) > 253 {
		return "", errors.New("hostname must be between 1 and 253 characters")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", errors.New("hostname must be a fully-qualified DNS name")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return "", errors.New("hostname contains an empty or oversized DNS label")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("DNS labels cannot start or end with a hyphen")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return "", errors.New("hostname must use ASCII letters, digits, hyphens, and dots (use punycode for IDNs)")
		}
	}
	return host, nil
}

func validateHostnameInZone(hostname, zoneName string) error {
	host, err := normalizeDNSName(hostname)
	if err != nil {
		return err
	}
	zone, err := normalizeDNSName(zoneName)
	if err != nil {
		return fmt.Errorf("invalid Cloudflare zone name: %w", err)
	}
	if host != zone && !strings.HasSuffix(host, "."+zone) {
		return fmt.Errorf("hostname %q does not belong to Cloudflare zone %q", host, zone)
	}
	return nil
}

func validateTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("target_url must be an absolute HTTP or HTTPS URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("target_url must use http or https")
	}
	if u.User != nil {
		return errors.New("target_url must not contain embedded credentials")
	}
	return nil
}

// stableTunnelName prevents the truncation collisions in the legacy naming
// scheme. The digest includes install identity when available, keeping two
// installs in the same Cloudflare account from adopting each other's tunnel.
func stableTunnelName(ctx *sdk.AppCtx, hostname string) string {
	identity := hostname
	if ctx != nil {
		// DataDir is install-specific and remains available if the identity API
		// is briefly unavailable, preventing cross-install name collisions.
		if dataDir := strings.TrimSpace(ctx.DataDir()); dataDir != "" {
			identity = dataDir + ":" + hostname
		}
	}
	if ctx != nil && ctx.PlatformAPI() != nil {
		if who, err := ctx.PlatformAPI().WhoAmI(); err == nil && who != nil {
			identity = strconv.FormatInt(who.InstallID, 10) + ":" + who.ProjectID + ":" + hostname
		}
	}
	sum := sha256.Sum256([]byte(identity))
	label := sanitizeForTunnelName(strings.Split(hostname, ".")[0])
	if len(label) > 8 {
		label = label[:8]
	}
	if label == "" {
		label = "tunnel"
	}
	return fmt.Sprintf("apt-ll-%s-%x", label, sum[:6])
}
