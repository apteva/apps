package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

const peerKeyFilename = "a2a-peer.key"

var inMemoryPeerKeys sync.Map // map[*sql.DB][]byte; test-only in-memory databases

type peerKeyring struct{ aead cipher.AEAD }

type peerRecord struct {
	peerConfig
	OwnerInstallID sql.NullInt64
}

func peerConfigs(app *sdk.AppCtx) ([]peerConfig, error) {
	records, err := loadPeerRecords(app)
	if err != nil {
		return nil, err
	}
	peers := make([]peerConfig, 0, len(records))
	for _, record := range records {
		peers = append(peers, record.peerConfig)
	}
	return peers, nil
}

// syncConfiguredPeers keeps peers_json as a backwards-compatible desired-state
// source for operator-managed peers. App-managed rows have an owner_install_id
// and are never overwritten or removed by configuration reconciliation.
func syncConfiguredPeers(app *sdk.AppCtx) error {
	desired, err := parseConfiguredPeers(app.Config().Get("peers_json"))
	if err != nil {
		return err
	}
	keys, err := loadPeerKeyring(app)
	if err != nil {
		return fmt.Errorf("peer registry key: %w", err)
	}

	desiredIDs := make(map[string]bool, len(desired))
	for _, peer := range desired {
		desiredIDs[peer.ID] = true
		var owner sql.NullInt64
		err := app.AppDB().QueryRow(`SELECT owner_install_id FROM a2a_peers WHERE id = ?`, peer.ID).Scan(&owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && owner.Valid {
			return fmt.Errorf("configured peer %q is managed by app install %d", peer.ID, owner.Int64)
		}
		if err := storePeer(app.AppDB(), keys, peer, nil); err != nil {
			return fmt.Errorf("store configured peer %q: %w", peer.ID, err)
		}
	}

	rows, err := app.AppDB().Query(`SELECT id FROM a2a_peers WHERE owner_install_id IS NULL`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !desiredIDs[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := app.AppDB().Exec(`DELETE FROM a2a_remote_agents WHERE peer_id = ?`, id); err != nil {
			return err
		}
		if _, err := app.AppDB().Exec(`DELETE FROM a2a_peers WHERE id = ? AND owner_install_id IS NULL`, id); err != nil {
			return err
		}
	}
	return nil
}

func parseConfiguredPeers(raw string) ([]peerConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var peers []peerConfig
	if err := json.Unmarshal([]byte(raw), &peers); err != nil {
		var wrapper struct {
			Peers []peerConfig `json:"peers"`
		}
		if err2 := json.Unmarshal([]byte(raw), &wrapper); err2 != nil {
			return nil, fmt.Errorf("invalid peers_json: %w", err)
		}
		peers = wrapper.Peers
	}
	seenID := map[string]bool{}
	seenToken := map[[sha256.Size]byte]bool{}
	for i := range peers {
		if err := normalizePeer(&peers[i]); err != nil {
			return nil, fmt.Errorf("peers_json entry %d: %w", i, err)
		}
		if seenID[peers[i].ID] {
			return nil, fmt.Errorf("duplicate peer id %q", peers[i].ID)
		}
		hash := sha256.Sum256([]byte(peers[i].Token))
		if seenToken[hash] {
			return nil, errors.New("peer tokens must be unique")
		}
		seenID[peers[i].ID] = true
		seenToken[hash] = true
	}
	return peers, nil
}

func normalizePeer(peer *peerConfig) error {
	peer.ID = strings.TrimSpace(peer.ID)
	peer.Name = strings.TrimSpace(peer.Name)
	peer.BaseURL = strings.TrimRight(strings.TrimSpace(peer.BaseURL), "/")
	peer.Token = strings.TrimSpace(peer.Token)
	peer.DiscoverAgents = normalizeRules(peer.DiscoverAgents)
	peer.InvokeAgents = normalizeRules(peer.InvokeAgents)
	if peer.ID == "" || peer.BaseURL == "" || peer.Token == "" {
		return errors.New("id, base_url, and token are required")
	}
	if peer.Name == "" {
		peer.Name = peer.ID
	}
	if err := validatePeerBaseURL(peer.BaseURL); err != nil {
		return err
	}
	return nil
}

func normalizeRules(rules []string) []string {
	out := make([]string, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			out = append(out, rule)
			seen[rule] = true
		}
	}
	return out
}

func loadPeerRecords(app *sdk.AppCtx) ([]peerRecord, error) {
	keys, err := loadPeerKeyring(app)
	if err != nil {
		return nil, fmt.Errorf("peer registry key: %w", err)
	}
	rows, err := app.AppDB().Query(`SELECT id, name, base_url, encrypted_token,
		discover_agents_json, invoke_agents_json, owner_install_id
		FROM a2a_peers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []peerRecord
	for rows.Next() {
		var record peerRecord
		var encrypted []byte
		var discoverJSON, invokeJSON string
		if err := rows.Scan(&record.ID, &record.Name, &record.BaseURL, &encrypted,
			&discoverJSON, &invokeJSON, &record.OwnerInstallID); err != nil {
			return nil, err
		}
		token, err := keys.open(record.ID, encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt peer %q token: %w", record.ID, err)
		}
		record.Token = string(token)
		if err := json.Unmarshal([]byte(discoverJSON), &record.DiscoverAgents); err != nil {
			return nil, fmt.Errorf("decode peer %q discovery grants: %w", record.ID, err)
		}
		if err := json.Unmarshal([]byte(invokeJSON), &record.InvokeAgents); err != nil {
			return nil, fmt.Errorf("decode peer %q invocation grants: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func storePeer(db *sql.DB, keys *peerKeyring, peer peerConfig, owner *int64) error {
	discoverJSON, _ := json.Marshal(peer.DiscoverAgents)
	invokeJSON, _ := json.Marshal(peer.InvokeAgents)
	hash := sha256.Sum256([]byte(peer.Token))

	var existingName, existingURL, existingDiscover, existingInvoke string
	var existingHash []byte
	var existingOwner sql.NullInt64
	err := db.QueryRow(`SELECT name, base_url, token_hash, discover_agents_json,
		invoke_agents_json, owner_install_id FROM a2a_peers WHERE id = ?`, peer.ID).
		Scan(&existingName, &existingURL, &existingHash, &existingDiscover, &existingInvoke, &existingOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	wantedOwner := sql.NullInt64{}
	if owner != nil {
		wantedOwner = sql.NullInt64{Int64: *owner, Valid: true}
	}
	if err == nil && existingName == peer.Name && existingURL == peer.BaseURL &&
		string(existingHash) == string(hash[:]) && existingDiscover == string(discoverJSON) &&
		existingInvoke == string(invokeJSON) && existingOwner == wantedOwner {
		return nil
	}

	encrypted, err := keys.seal(peer.ID, []byte(peer.Token))
	if err != nil {
		return err
	}
	now := nowUTC()
	result, err := db.Exec(`INSERT INTO a2a_peers
		(id, name, base_url, encrypted_token, token_hash, discover_agents_json,
		 invoke_agents_json, owner_install_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 name = excluded.name,
		 base_url = excluded.base_url,
		 encrypted_token = excluded.encrypted_token,
		 token_hash = excluded.token_hash,
		 discover_agents_json = excluded.discover_agents_json,
		 invoke_agents_json = excluded.invoke_agents_json,
		 owner_install_id = excluded.owner_install_id,
		 updated_at = excluded.updated_at
		WHERE a2a_peers.owner_install_id IS excluded.owner_install_id`,
		peer.ID, peer.Name, peer.BaseURL, encrypted, hash[:], string(discoverJSON),
		string(invokeJSON), owner, now, now)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("peer %q ownership changed during update", peer.ID)
	}
	return nil
}

func loadPeerKeyring(app *sdk.AppCtx) (*peerKeyring, error) {
	key, err := resolvePeerMasterKey(app)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &peerKeyring{aead: aead}, nil
}

func resolvePeerMasterKey(app *sdk.AppCtx) ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv("A2A_MASTER_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, errors.New("A2A_MASTER_KEY must be base64-encoded 32 bytes")
		}
		return key, nil
	}
	dir, err := appDatabaseDir(app)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		if existing, ok := inMemoryPeerKeys.Load(app.AppDB()); ok {
			return existing.([]byte), nil
		}
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		actual, _ := inMemoryPeerKeys.LoadOrStore(app.AppDB(), key)
		return actual.([]byte), nil
	}
	path := filepath.Join(dir, peerKeyFilename)
	if key, err := readPeerKey(path); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readPeerKey(path)
	}
	if err != nil {
		return nil, err
	}
	if written, err := f.Write(key); err != nil || written != len(key) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, io.ErrShortWrite
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func readPeerKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s has wrong size %d", peerKeyFilename, len(key))
	}
	return key, nil
}

func appDatabaseDir(app *sdk.AppCtx) (string, error) {
	rows, err := app.AppDB().Query(`PRAGMA database_list`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var seq int
			var name, path string
			if err := rows.Scan(&seq, &name, &path); err != nil {
				return "", err
			}
			if name == "main" && strings.TrimSpace(path) != "" {
				return filepath.Dir(path), nil
			}
		}
	}
	if dir := strings.TrimSpace(app.DataDir()); dir != "" {
		return dir, nil
	}
	return "", nil
}

func (k *peerKeyring) seal(peerID string, plain []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plain, []byte(peerID)), nil
}

func (k *peerKeyring) open(peerID string, encrypted []byte) ([]byte, error) {
	nonceSize := k.aead.NonceSize()
	if len(encrypted) < nonceSize+k.aead.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	return k.aead.Open(nil, encrypted[:nonceSize], encrypted[nonceSize:], []byte(peerID))
}

func boundAppOwner(ctx context.Context) (int64, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AppInstallID <= 0 || strings.TrimSpace(caller.AppName) == "" {
		return 0, errors.New("authenticated app caller required")
	}
	return caller.AppInstallID, nil
}

func (a *App) toolNodeInfo(ctx context.Context, app *sdk.AppCtx, _ map[string]any) (any, error) {
	if _, err := boundAppOwner(ctx); err != nil {
		return nil, err
	}
	node, err := ensureLocalNode(app)
	if err != nil {
		return nil, err
	}
	var peerCount int
	if err := app.AppDB().QueryRow(`SELECT COUNT(*) FROM a2a_peers`).Scan(&peerCount); err != nil {
		return nil, err
	}
	publicURL := publicA2ABaseURL(app)
	if strings.HasPrefix(publicURL, "/") {
		publicURL = ""
	}
	return map[string]any{
		"node_id": node.NodeID, "name": node.DisplayName,
		"public_url": publicURL, "peer_count": peerCount,
	}, nil
}

func (a *App) toolPeerUpsert(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := boundAppOwner(ctx)
	if err != nil {
		return nil, err
	}
	peer := peerConfig{
		ID: stringArg(args, "id"), Name: stringArg(args, "name"),
		BaseURL: stringArg(args, "base_url"), Token: stringArg(args, "token"),
		DiscoverAgents: stringListArg(args, "discover_agents"),
		InvokeAgents:   stringListArg(args, "invoke_agents"),
	}
	if err := normalizePeer(&peer); err != nil {
		return nil, err
	}
	var existingOwner sql.NullInt64
	err = app.AppDB().QueryRow(`SELECT owner_install_id FROM a2a_peers WHERE id = ?`, peer.ID).Scan(&existingOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if !existingOwner.Valid {
			return nil, fmt.Errorf("peer %q is operator-managed", peer.ID)
		}
		if existingOwner.Int64 != owner {
			return nil, fmt.Errorf("peer %q is managed by another app install", peer.ID)
		}
	}
	keys, err := loadPeerKeyring(app)
	if err != nil {
		return nil, err
	}
	if err := storePeer(app.AppDB(), keys, peer, &owner); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": peer.ID, "name": peer.Name, "base_url": peer.BaseURL,
		"discover_agents": peer.DiscoverAgents, "invoke_agents": peer.InvokeAgents,
		"owner_install_id": owner,
	}, nil
}

func (a *App) toolPeerRemove(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := boundAppOwner(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(stringArg(args, "id"))
	if id == "" {
		return nil, errors.New("id required")
	}
	var existingOwner sql.NullInt64
	err = app.AppDB().QueryRow(`SELECT owner_install_id FROM a2a_peers WHERE id = ?`, id).Scan(&existingOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"id": id, "removed": false}, nil
	}
	if err != nil {
		return nil, err
	}
	if !existingOwner.Valid {
		return nil, fmt.Errorf("peer %q is operator-managed", id)
	}
	if existingOwner.Int64 != owner {
		return nil, fmt.Errorf("peer %q is managed by another app install", id)
	}
	tx, err := app.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM a2a_remote_agents WHERE peer_id = ?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM a2a_peers WHERE id = ? AND owner_install_id = ?`, id, owner); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "removed": true}, nil
}

func stringListArg(args map[string]any, key string) []string {
	switch value := args[key].(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
