package main

import (
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

// RepoCardData is the compact, non-sensitive repository shape used by
// chat attachments. In particular, it does not expose env_json or command
// configuration from the full repository row.
type RepoCardData struct {
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Framework     string          `json:"framework,omitempty"`
	Archived      bool            `json:"archived"`
	IsTemplate    bool            `json:"is_template"`
	TemplateScope string          `json:"template_scope,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	FileCount     int             `json:"file_count"`
	TotalSize     int64           `json:"total_size"`
	DevRun        *RepoCardDevRun `json:"dev_run,omitempty"`
}

type RepoCardDevRun struct {
	Status    string `json:"status"`
	Port      int    `json:"port,omitempty"`
	Framework string `json:"framework,omitempty"`
	Runner    string `json:"runner,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

type IssueCardData struct {
	RepoSlug      string `json:"repo_slug"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body,omitempty"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	State         string `json:"state"`
	StateReason   string `json:"state_reason,omitempty"`
	Priority      string `json:"priority"`
	Assignee      string `json:"assignee,omitempty"`
	CommentsCount int    `json:"comments_count"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

func issueCardData(issue *Issue) *IssueCardData {
	if issue == nil {
		return nil
	}
	body := []rune(issue.Body)
	if len(body) > 1000 {
		body = append(body[:999], '\u2026')
	}
	return &IssueCardData{
		RepoSlug:      issue.RepoSlug,
		Number:        issue.Number,
		Title:         issue.Title,
		Body:          string(body),
		Type:          issue.Type,
		Status:        issue.Status,
		State:         issue.State,
		StateReason:   issue.StateReason,
		Priority:      issue.Priority,
		Assignee:      issue.Assignee,
		CommentsCount: issue.CommentsCount,
		UpdatedAt:     issue.UpdatedAt,
	}
}

func emitDevEvent(ctx *sdk.AppCtx, repo *Repo, devRun *DevRun) {
	if ctx == nil || repo == nil {
		return
	}
	data := map[string]any{"slug": repo.Slug, "repo_id": repo.ID}
	if devRun != nil {
		data["status"] = devRun.Status
		data["framework"] = devRun.Framework
		data["port"] = devRun.Port
	}
	ctx.Emit("dev.changed", data)
}

func (a *App) repoCardData(projectID string, repo *Repo) (*RepoCardData, error) {
	files, err := listSourceFiles(a.storeFor(repo), repo.Slug, "", true, false)
	if err != nil {
		return nil, err
	}
	var totalSize int64
	for _, file := range files {
		if !file.IsDir {
			totalSize += file.Size
		}
	}
	devRun, err := dbGetDevRun(globalCtx.AppDB(), projectID, repo.ID)
	if err != nil {
		return nil, err
	}
	var cardDevRun *RepoCardDevRun
	if devRun != nil {
		cardDevRun = &RepoCardDevRun{
			Status:    devRun.Status,
			Port:      devRun.Port,
			Framework: devRun.Framework,
			Runner:    devRun.Runner,
			StartedAt: devRun.StartedAt,
		}
	}
	return &RepoCardData{
		Slug:          repo.Slug,
		Name:          repo.Name,
		Description:   repo.Description,
		Framework:     repo.Framework,
		Archived:      repo.IsArchived(),
		IsTemplate:    repo.IsTemplate,
		TemplateScope: repo.TemplateScope,
		UpdatedAt:     repo.UpdatedAt,
		FileCount:     len(files),
		TotalSize:     totalSize,
		DevRun:        cardDevRun,
	}, nil
}

func (a *App) httpRepoSummary(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	projectID, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := requireRepoSlug(globalCtx, projectID, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	data, err := a.repoCardData(projectID, repo)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"repository": data})
}
