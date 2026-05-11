package main

import (
	"database/sql"
	"errors"
	"time"

	"github.com/apteva/apps/mcp/vpn/backend"
)

// ─── server row ─────────────────────────────────────────────────────

type serverRow struct {
	ProjectID    string `json:"project_id"`
	HostID       int64  `json:"host_id"`
	Backend      string `json:"backend"`
	PublicKey    string `json:"public_key"`
	PrivateKey   string `json:"-"` // never JSON-emit; secret
	Endpoint     string `json:"endpoint"`
	ListenPort   int    `json:"listen_port"`
	NetworkCIDR  string `json:"network_cidr"`
	InstalledAt  int64  `json:"installed_at"`
	LastPollAt   int64  `json:"last_poll_at"`
	LastPollOK   bool   `json:"last_poll_ok"`
	ActivePeers  int    `json:"active_peers"`
	RevokedPeers int    `json:"revoked_peers"`
}

var errNoServer = errors.New("vpn not installed yet — run vpn_install first")

func getServer(db *sql.DB, pid string) (*serverRow, error) {
	var s serverRow
	var pollAt, pollOK sql.NullInt64
	err := db.QueryRow(`
		SELECT project_id, host_id, backend, public_key, private_key,
		       endpoint, listen_port, network_cidr,
		       installed_at, last_poll_at, last_poll_ok
		  FROM server WHERE project_id = ?`, pid,
	).Scan(&s.ProjectID, &s.HostID, &s.Backend, &s.PublicKey, &s.PrivateKey,
		&s.Endpoint, &s.ListenPort, &s.NetworkCIDR,
		&s.InstalledAt, &pollAt, &pollOK)
	if err == sql.ErrNoRows {
		return nil, errNoServer
	}
	if err != nil {
		return nil, err
	}
	s.LastPollAt = pollAt.Int64
	s.LastPollOK = pollOK.Valid && pollOK.Int64 == 1
	return &s, nil
}

func insertServer(db *sql.DB, pid string, hostID int64, backendName string, srv backend.ServerIdentity) error {
	_, err := db.Exec(`
		INSERT INTO server (project_id, host_id, backend, public_key, private_key,
		                    endpoint, listen_port, network_cidr, installed_at)
		     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, hostID, backendName, srv.PublicKey, srv.PrivateKey,
		srv.Endpoint, srv.ListenPort, srv.NetworkCIDR, time.Now().Unix())
	return err
}

func deleteServer(db *sql.DB, pid string) error {
	if _, err := db.Exec(`DELETE FROM peer WHERE project_id = ?`, pid); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM server WHERE project_id = ?`, pid)
	return err
}

func updateServerPoll(db *sql.DB, pid string, ok bool) {
	v := int64(0)
	if ok {
		v = 1
	}
	_, _ = db.Exec(`UPDATE server SET last_poll_at = ?, last_poll_ok = ? WHERE project_id = ?`,
		time.Now().Unix(), v, pid)
}

// serverIdentity rebuilds the backend's ServerIdentity from the DB
// row — we round-trip through this on every AddPeer / RemovePeer
// so the backend stays stateless.
func (s *serverRow) identity() backend.ServerIdentity {
	return backend.ServerIdentity{
		PublicKey:   s.PublicKey,
		PrivateKey:  s.PrivateKey,
		Endpoint:    s.Endpoint,
		ListenPort:  s.ListenPort,
		NetworkCIDR: s.NetworkCIDR,
	}
}

// ─── peer rows ──────────────────────────────────────────────────────

type peerRow struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id"`
	Name            string `json:"name"`
	PublicKey       string `json:"public_key"`
	PrivateKey      string `json:"-"`
	PresharedKey    string `json:"-"`
	Address         string `json:"address"`
	AllowedIPs      string `json:"allowed_ips"`
	DNS             string `json:"dns"`
	CreatedAt       int64  `json:"created_at"`
	RevokedAt       int64  `json:"revoked_at"`
	LastHandshakeAt int64  `json:"last_handshake_at"`
	RxBytes         int64  `json:"rx_bytes"`
	TxBytes         int64  `json:"tx_bytes"`
}

const peerCols = `id, project_id, name, public_key, private_key, preshared_key,
	address, allowed_ips, dns, created_at,
	COALESCE(revoked_at, 0), COALESCE(last_handshake_at, 0),
	rx_bytes, tx_bytes`

func scanPeer(s scannable, p *peerRow) error {
	return s.Scan(&p.ID, &p.ProjectID, &p.Name, &p.PublicKey, &p.PrivateKey, &p.PresharedKey,
		&p.Address, &p.AllowedIPs, &p.DNS, &p.CreatedAt,
		&p.RevokedAt, &p.LastHandshakeAt, &p.RxBytes, &p.TxBytes)
}

type scannable interface {
	Scan(dest ...any) error
}

func listPeers(db *sql.DB, pid string, includeRevoked bool) ([]peerRow, error) {
	q := `SELECT ` + peerCols + ` FROM peer WHERE project_id = ?`
	if !includeRevoked {
		q += ` AND revoked_at IS NULL`
	}
	q += ` ORDER BY created_at ASC, id ASC`
	rows, err := db.Query(q, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []peerRow
	for rows.Next() {
		var p peerRow
		if err := scanPeer(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func getPeerByName(db *sql.DB, pid, name string) (*peerRow, error) {
	var p peerRow
	err := scanPeer(db.QueryRow(`SELECT `+peerCols+` FROM peer WHERE project_id = ? AND name = ?`, pid, name), &p)
	if err == sql.ErrNoRows {
		return nil, errors.New("peer not found: " + name)
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func insertPeer(db *sql.DB, pid string, p backend.Peer) (*peerRow, error) {
	res, err := db.Exec(`
		INSERT INTO peer (project_id, name, public_key, private_key, preshared_key,
		                  address, allowed_ips, dns, created_at)
		     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, p.Name, p.PublicKey, p.PrivateKey, p.PresharedKey,
		p.Address, p.AllowedIPs, p.DNS, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getPeerByID(db, pid, id)
}

func getPeerByID(db *sql.DB, pid string, id int64) (*peerRow, error) {
	var p peerRow
	err := scanPeer(db.QueryRow(`SELECT `+peerCols+` FROM peer WHERE project_id = ? AND id = ?`, pid, id), &p)
	if err == sql.ErrNoRows {
		return nil, errors.New("peer not found")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func revokePeer(db *sql.DB, pid, name string) error {
	res, err := db.Exec(`
		UPDATE peer SET revoked_at = ?
		 WHERE project_id = ? AND name = ? AND revoked_at IS NULL`,
		time.Now().Unix(), pid, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("peer not found or already revoked: " + name)
	}
	return nil
}

// takenAddresses returns every address ever claimed (active or
// revoked) so allocatePeerIP can avoid reusing revoked addresses.
func takenAddresses(db *sql.DB, pid string) ([]string, error) {
	rows, err := db.Query(`SELECT address FROM peer WHERE project_id = ?`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// applyStats joins backend.PeerStats rows into the peer table. We
// match by public_key; rows the backend doesn't know about are left
// untouched (a peer we revoked might still be in the daemon's view
// for a moment until the next config push lands).
func applyStats(db *sql.DB, pid string, stats []backend.PeerStats) {
	for _, s := range stats {
		_, _ = db.Exec(`
			UPDATE peer
			   SET last_handshake_at = NULLIF(?, 0),
			       rx_bytes = ?, tx_bytes = ?
			 WHERE project_id = ? AND public_key = ?`,
			s.LastHandshake, s.RxBytes, s.TxBytes, pid, s.PublicKey)
	}
}

// peerToBackend turns a stored row into the backend.Peer shape — for
// re-rendering daemon config and client configs.
func (p *peerRow) toBackend() backend.Peer {
	return backend.Peer{
		Name:         p.Name,
		PublicKey:    p.PublicKey,
		PrivateKey:   p.PrivateKey,
		PresharedKey: p.PresharedKey,
		Address:      p.Address,
		AllowedIPs:   p.AllowedIPs,
		DNS:          p.DNS,
	}
}

func activePeersForBackend(db *sql.DB, pid string) ([]backend.Peer, error) {
	rows, err := listPeers(db, pid, false)
	if err != nil {
		return nil, err
	}
	out := make([]backend.Peer, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toBackend())
	}
	return out, nil
}

func countPeers(db *sql.DB, pid string) (active, revoked int) {
	_ = db.QueryRow(`SELECT COUNT(*) FROM peer WHERE project_id = ? AND revoked_at IS NULL`, pid).Scan(&active)
	_ = db.QueryRow(`SELECT COUNT(*) FROM peer WHERE project_id = ? AND revoked_at IS NOT NULL`, pid).Scan(&revoked)
	return
}
