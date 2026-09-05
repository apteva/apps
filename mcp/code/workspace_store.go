package main

import (
	"database/sql"
	"encoding/json"
	"errors"
)

type RepoWorkspace struct {
	ProjectID        string   `json:"project_id"`
	RepoID           int64    `json:"repo_id"`
	WorkspaceID      string   `json:"workspace_id"`
	Profile          string   `json:"profile"`
	Image            string   `json:"image"`
	SourceDigest     string   `json:"source_digest"`
	SourcePaths      []string `json:"source_paths,omitempty"`
	WorkspacePaths   []string `json:"workspace_paths,omitempty"`
	SupportPaths     []string `json:"support_paths,omitempty"`
	DependencyDigest string   `json:"dependency_digest,omitempty"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func scanRepoWorkspace(scanner interface{ Scan(...any) error }) (*RepoWorkspace, error) {
	var row RepoWorkspace
	var paths, workspacePatterns, supportPatterns string
	err := scanner.Scan(&row.ProjectID, &row.RepoID, &row.WorkspaceID, &row.Profile, &row.Image,
		&row.SourceDigest, &paths, &workspacePatterns, &supportPatterns,
		&row.DependencyDigest, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(paths), &row.SourcePaths)
	_ = json.Unmarshal([]byte(workspacePatterns), &row.WorkspacePaths)
	_ = json.Unmarshal([]byte(supportPatterns), &row.SupportPaths)
	return &row, nil
}

func dbGetRepoWorkspace(db *sql.DB, projectID string, repoID int64) (*RepoWorkspace, error) {
	return scanRepoWorkspace(db.QueryRow(`SELECT project_id, repo_id, workspace_id,
		profile, image, source_digest, source_paths_json, workspace_patterns_json,
		support_patterns_json, dependency_digest,
		created_at, updated_at FROM repo_workspaces WHERE project_id=? AND repo_id=?`, projectID, repoID))
}

func dbPutRepoWorkspace(db *sql.DB, row *RepoWorkspace) error {
	paths, _ := json.Marshal(row.SourcePaths)
	workspacePatterns, _ := json.Marshal(row.WorkspacePaths)
	supportPatterns, _ := json.Marshal(row.SupportPaths)
	_, err := db.Exec(`INSERT INTO repo_workspaces (
		project_id, repo_id, workspace_id, profile, image, source_digest,
		source_paths_json, workspace_patterns_json, support_patterns_json,
		dependency_digest, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	ON CONFLICT(project_id, repo_id) DO UPDATE SET
		workspace_id=excluded.workspace_id, profile=excluded.profile,
		image=excluded.image,
		source_digest=excluded.source_digest, source_paths_json=excluded.source_paths_json,
		workspace_patterns_json=excluded.workspace_patterns_json,
		support_patterns_json=excluded.support_patterns_json,
		dependency_digest=excluded.dependency_digest, updated_at=CURRENT_TIMESTAMP`,
		row.ProjectID, row.RepoID, row.WorkspaceID, row.Profile, row.Image, row.SourceDigest,
		string(paths), string(workspacePatterns), string(supportPatterns), row.DependencyDigest)
	return err
}

func dbDeleteRepoWorkspace(db *sql.DB, projectID string, repoID int64) error {
	_, err := db.Exec(`DELETE FROM repo_workspaces WHERE project_id=? AND repo_id=?`, projectID, repoID)
	return err
}
