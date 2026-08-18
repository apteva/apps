package main

// pairing.go — DM policy for unknown external senders.
//
// A stranger messaging a bound bot never reaches an agent directly:
// they get a one-time code, the operator approves it from the
// dashboard, and only then does a conversation exist. Codes expire
// after an hour; one live code per (channel, identity) so a stranger
// cannot mint codes in a loop.

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

const pairingTTL = time.Hour

type PairingCode struct {
	Code             string     `json:"code"`
	Channel          string     `json:"channel"`
	ExternalIdentity string     `json:"external_identity"`
	DisplayName      string     `json:"display_name,omitempty"`
	ExpiresAt        time.Time  `json:"expires_at"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
}

// pairingAlphabet omits look-alikes (0/O, 1/I/L) — the code is read
// off a phone screen and typed by a human.
const pairingAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newPairingCode() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	for i, b := range buf {
		buf[i] = pairingAlphabet[int(b)%len(pairingAlphabet)]
	}
	return string(buf)
}

// EnsurePairing returns the live code for an unknown sender, minting
// one when none exists or the previous expired. approved=true means
// the identity may converse.
func (s *store) EnsurePairing(channel, identity, displayName string) (code string, approved bool, err error) {
	var existing string
	var expiresRaw string
	var approvedAt sql.NullString
	err = s.db.QueryRow(`
		SELECT code, expires_at, approved_at FROM pairing_codes
		WHERE channel = ? AND external_identity = ?`, channel, identity).
		Scan(&existing, &expiresRaw, &approvedAt)
	switch {
	case err == sql.ErrNoRows:
		// fall through to mint
	case err != nil:
		return "", false, err
	default:
		if approvedAt.Valid {
			return existing, true, nil
		}
		if expires, parseErr := parseSQLiteTime(expiresRaw); parseErr == nil && time.Now().Before(expires) {
			return existing, false, nil
		}
		// Expired and unapproved: replace.
		if _, err := s.db.Exec(`DELETE FROM pairing_codes WHERE code = ?`, existing); err != nil {
			return "", false, err
		}
	}

	code = newPairingCode()
	_, err = s.db.Exec(`
		INSERT INTO pairing_codes (code, channel, external_identity, display_name, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		code, channel, identity, displayName,
		time.Now().Add(pairingTTL).UTC().Format(time.RFC3339))
	return code, false, err
}

func (s *store) ApprovePairing(code string) error {
	res, err := s.db.Exec(`
		UPDATE pairing_codes SET approved_at = CURRENT_TIMESTAMP
		WHERE code = ? AND approved_at IS NULL AND expires_at > ?`,
		code, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("pairing code %q is unknown, expired, or already approved", code)
	}
	return nil
}

func (s *store) PairingApproved(channel, identity string) (bool, error) {
	var one int
	err := s.db.QueryRow(`
		SELECT 1 FROM pairing_codes
		WHERE channel = ? AND external_identity = ? AND approved_at IS NOT NULL`,
		channel, identity).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *store) PendingPairings() ([]PairingCode, error) {
	rows, err := s.db.Query(`
		SELECT code, channel, external_identity, display_name, expires_at
		FROM pairing_codes
		WHERE approved_at IS NULL AND expires_at > ?
		ORDER BY created_at DESC`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PairingCode{}
	for rows.Next() {
		var p PairingCode
		var expiresRaw string
		if err := rows.Scan(&p.Code, &p.Channel, &p.ExternalIdentity, &p.DisplayName, &expiresRaw); err != nil {
			return nil, err
		}
		p.ExpiresAt, _ = parseSQLiteTime(expiresRaw)
		out = append(out, p)
	}
	return out, rows.Err()
}
