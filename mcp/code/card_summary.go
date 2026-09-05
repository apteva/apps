package main

import (
	"net/http"
	"sync"
	"time"

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
	ClaimOwner    string `json:"claim_owner,omitempty"`
	ClaimLabel    string `json:"claim_label,omitempty"`
	ClaimedAt     string `json:"claimed_at,omitempty"`
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
		ClaimOwner:    issue.ClaimOwner,
		ClaimLabel:    issue.ClaimLabel,
		ClaimedAt:     issue.ClaimedAt,
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
	count, totalSize, err := a.sourceSummary(repo)
	if err != nil {
		return nil, err
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
		FileCount:     count,
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

// Revision invalidation covers Code mutations; expiry also catches trusted local
// dev processes, which can write outside FileStore. Only 256 summaries are kept.
type sourceSummaryEntry struct {
	revision uint64
	at       time.Time
	count    int
	bytes    int64
}
type summaryCache struct {
	sync.Mutex
	entries map[string]sourceSummaryEntry
}

func (a *App) sourceSummary(repo *Repo) (int, int64, error) {
	key := repoStoreKey(repo)
	revision := uint64(0)
	if a.locks != nil {
		revision = a.locks.revision(key)
	}
	a.summaries.Lock()
	defer a.summaries.Unlock()
	if e, ok := a.summaries.entries[key]; ok && e.revision == revision && time.Since(e.at) < 15*time.Second {
		return e.count, e.bytes, nil
	}
	files, err := listSourceFiles(a.storeFor(repo), repo.Slug, "", true, false)
	if err != nil {
		return 0, 0, err
	}
	e := sourceSummaryEntry{revision: revision, at: time.Now()}
	for _, f := range files {
		if !f.IsDir {
			e.count++
			e.bytes += f.Size
		}
	}
	if a.summaries.entries == nil || len(a.summaries.entries) >= 256 {
		a.summaries.entries = map[string]sourceSummaryEntry{}
	}
	a.summaries.entries[key] = e
	return e.count, e.bytes, nil
}
