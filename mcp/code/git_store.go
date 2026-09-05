package main

import (
	"database/sql"
	"errors"
	"time"
)

type GitRemote struct {
	ID            int64  `json:"id"`
	RepoID        int64  `json:"repo_id"`
	Name          string `json:"name"`
	FetchURL      string `json:"fetch_url"`
	PushURL       string `json:"push_url,omitempty"`
	ConnectionID  int64  `json:"connection_id,omitempty"`
	ProviderSlug  string `json:"provider_slug,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	LastFetchAt   string `json:"last_fetch_at,omitempty"`
	LastPushAt    string `json:"last_push_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

func dbUpsertGitRemote(db *sql.DB, remote GitRemote) (*GitRemote, error) {
	if remote.Name == "" {
		remote.Name = "origin"
	}
	_, err := db.Exec(`
		INSERT INTO repo_git_remotes
			(repo_id, name, fetch_url, push_url, connection_id, provider_slug, default_branch)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, name) DO UPDATE SET
			fetch_url=excluded.fetch_url,
			push_url=excluded.push_url,
			connection_id=excluded.connection_id,
			provider_slug=excluded.provider_slug,
			default_branch=excluded.default_branch,
			updated_at=CURRENT_TIMESTAMP
	`, remote.RepoID, remote.Name, remote.FetchURL, remote.PushURL,
		remote.ConnectionID, remote.ProviderSlug, remote.DefaultBranch)
	if err != nil {
		return nil, err
	}
	return dbGetGitRemote(db, remote.RepoID, remote.Name)
}

func dbGetGitRemote(db *sql.DB, repoID int64, name string) (*GitRemote, error) {
	if name == "" {
		name = "origin"
	}
	row := db.QueryRow(`
		SELECT id, repo_id, name, fetch_url, push_url, connection_id,
		       provider_slug, default_branch,
		       IFNULL(last_fetch_at,''), IFNULL(last_push_at,''), last_error,
		       created_at, updated_at
		  FROM repo_git_remotes WHERE repo_id=? AND name=?
	`, repoID, name)
	var remote GitRemote
	if err := row.Scan(&remote.ID, &remote.RepoID, &remote.Name, &remote.FetchURL,
		&remote.PushURL, &remote.ConnectionID, &remote.ProviderSlug,
		&remote.DefaultBranch, &remote.LastFetchAt, &remote.LastPushAt,
		&remote.LastError, &remote.CreatedAt, &remote.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &remote, nil
}

func dbListGitRemotes(db *sql.DB, repoID int64) ([]GitRemote, error) {
	rows, err := db.Query(`
		SELECT id, repo_id, name, fetch_url, push_url, connection_id,
		       provider_slug, default_branch,
		       IFNULL(last_fetch_at,''), IFNULL(last_push_at,''), last_error,
		       created_at, updated_at
		  FROM repo_git_remotes WHERE repo_id=? ORDER BY name
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitRemote{}
	for rows.Next() {
		var remote GitRemote
		if err := rows.Scan(&remote.ID, &remote.RepoID, &remote.Name,
			&remote.FetchURL, &remote.PushURL, &remote.ConnectionID,
			&remote.ProviderSlug, &remote.DefaultBranch, &remote.LastFetchAt,
			&remote.LastPushAt, &remote.LastError, &remote.CreatedAt,
			&remote.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, remote)
	}
	return out, rows.Err()
}

func dbMarkGitRemoteResult(db *sql.DB, repoID int64, name, operation string, opErr error) {
	if name == "" {
		name = "origin"
	}
	errText := ""
	if opErr != nil {
		errText = opErr.Error()
	}
	column := ""
	switch operation {
	case "fetch", "pull", "clone", "connect":
		column = "last_fetch_at"
	case "push":
		column = "last_push_at"
	}
	if column == "" || opErr != nil {
		_, _ = db.Exec(`UPDATE repo_git_remotes SET last_error=?, updated_at=CURRENT_TIMESTAMP WHERE repo_id=? AND name=?`, errText, repoID, name)
		return
	}
	_, _ = db.Exec(`UPDATE repo_git_remotes SET `+column+`=?, last_error='', updated_at=CURRENT_TIMESTAMP WHERE repo_id=? AND name=?`, time.Now().UTC(), repoID, name)
}

func dbRecordGitOperation(db *sql.DB, repoID int64, operation, actor, fromSHA, toSHA string, opErr error) {
	status := "ok"
	errText := ""
	if opErr != nil {
		status = "error"
		errText = opErr.Error()
	}
	_, _ = db.Exec(`
		INSERT INTO repo_git_operations(repo_id, operation, actor, from_sha, to_sha, status, error)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, repoID, operation, actor, fromSHA, toSHA, status, errText)
}
