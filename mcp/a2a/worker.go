package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A bounded network budget prevents one project or offline peer from monopolizing
// the SDK's sequential project dispatcher. DB updates and deliveries stay serial.
func (a *App) syncRemoteTasks(ctx context.Context, app *sdk.AppCtx) error {
	if app.CurrentProject() == "" {
		return nil
	}
	if err := flushDeliveries(ctx, app); err != nil {
		return err
	}
	tasks, err := listOpenOutboundTasks(app.AppDB(), app.CurrentProject(), 100)
	if err != nil || len(tasks) == 0 {
		return err
	}
	peers, err := peerConfigs(app)
	if err != nil {
		return err
	}
	byID := make(map[string]peerConfig, len(peers))
	for _, peer := range peers {
		byID[peer.ID] = peer
	}
	type job struct {
		task   *Task
		remote *remoteAgent
		peer   peerConfig
	}
	type result struct {
		job
		response a2aTaskWire
		err      error
	}
	jobs := make(chan job, len(tasks))
	results := make(chan result, len(tasks))
	for _, task := range tasks {
		peer, ok := byID[task.PeerID]
		remote, e := getRemoteAgentByPeerCard(app.AppDB(), task.PeerID, task.RemoteCardID)
		if !ok || e != nil || remote == nil {
			if err := markPoll(app, task.ID, true); err != nil {
				return err
			}
			continue
		}
		jobs <- job{task: task, remote: remote, peer: peer}
	}
	close(jobs)
	pollCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if pollCtx.Err() != nil {
					return
				}
				attempt, stop := context.WithTimeout(pollCtx, 2*time.Second)
				remote, e := a.ensureRemoteCard(attempt, app, j.remote, &j.peer)
				var response a2aTaskWire
				if e == nil {
					e = a.callRemoteRPC(attempt, app, &j.peer, remote.EndpointURL, "tasks/get", taskIDParams{ID: j.task.RemoteTaskID}, &response)
				}
				stop()
				results <- result{job: j, response: response, err: e}
			}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if err := markPoll(app, r.task.ID, r.err != nil); err != nil {
			return err
		}
		if r.err != nil {
			app.Logger().Warn("remote task sync deferred", "task", r.task.ID, "err", r.err)
			continue
		}
		if r.response.ID != r.task.RemoteTaskID {
			app.Logger().Warn("remote task id mismatch", "task", r.task.ID)
			continue
		}
		if err := applyRemoteResult(app, r.task, r.remote.Name, r.response); err != nil {
			return err
		}
	}
	return nil
}

func markPoll(app *sdk.AppCtx, id int64, failed bool) error {
	next, failures := "", 0
	if failed {
		if err := app.AppDB().QueryRow(`SELECT poll_failures FROM a2a_tasks WHERE id=?`, id).Scan(&failures); err != nil {
			return err
		}
		failures++
		delay := 30 * time.Second * time.Duration(1<<min(failures-1, 3))
		next = time.Now().UTC().Add(delay).Format(time.RFC3339)
	}
	_, err := app.AppDB().Exec(`UPDATE a2a_tasks SET last_synced_at=?,next_poll_at=?,poll_failures=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), next, failures, id)
	return err
}

func remoteResultText(response a2aTaskWire) string {
	var parts []string
	if response.Status.Message != nil {
		if text := extractA2AText(*response.Status.Message); text != "" {
			parts = append(parts, text)
		}
	}
	for _, raw := range response.Artifacts {
		var artifact struct {
			Name  string            `json:"name"`
			Parts []json.RawMessage `json:"parts"`
		}
		if json.Unmarshal(raw, &artifact) != nil {
			continue
		}
		if artifact.Name != "" {
			parts = append(parts, artifact.Name)
		}
		for _, part := range artifact.Parts {
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(part, &p) == nil && strings.TrimSpace(p.Text) != "" {
				parts = append(parts, p.Text)
			} else {
				parts = append(parts, string(part))
			}
		}
	}
	if len(parts) == 0 {
		return "Remote task is now " + strings.ReplaceAll(localStateFromA2A(response.Status.State), "_", " ") + "."
	}
	return strings.Join(parts, "\n")
}

func applyRemoteResult(app *sdk.AppCtx, task *Task, name string, response a2aTaskWire) error {
	current, err := getTask(app.AppDB(), task.ProjectID, task.ID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("task %d not found", task.ID)
	}
	// In-flight polling must not reopen a task concurrently canceled locally.
	if !openStatuses[current.Status] {
		return nil
	}
	*task = *current
	status := localStateFromA2A(response.Status.State)
	message := remoteResultText(response)
	var lastBody, lastStatus string
	_ = app.AppDB().QueryRow(`SELECT body,status_after FROM a2a_messages WHERE task_id=? AND from_agent_id=0 ORDER BY id DESC LIMIT 1`, task.ID).Scan(&lastBody, &lastStatus)
	artifacts, _ := json.Marshal(response.Artifacts)
	unchangedArtifacts := response.Artifacts == nil || string(artifacts) == string(task.Artifacts)
	if task.Status == status && lastStatus == status && lastBody == message && unchangedArtifacts {
		return nil
	}
	task.Status = status
	identity := &callIdentity{AgentName: name, ProjectID: task.ProjectID}
	id, err := saveReply(app.AppDB(), task, 0, task.FromAgentID, message, formatReplyEvent(task, identity, message), response.Artifacts)
	if err != nil {
		return fmt.Errorf("persist remote reply: %w", err)
	}
	if err := deliverPending(app, id); err != nil {
		app.Logger().Warn("remote reply queued for retry", "task", task.ID, "err", err)
	}
	emitTask(app, "task.updated", task)
	return nil
}
