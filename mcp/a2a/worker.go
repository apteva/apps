package main

import (
	"context"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) syncRemoteTasks(ctx context.Context, app *sdk.AppCtx) error {
	projectID := app.CurrentProject()
	if projectID == "" {
		return nil
	}
	tasks, err := listOpenOutboundTasks(app.AppDB(), projectID, 100)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if task.RemoteTaskID == "" {
			continue
		}
		remote, remoteErr := getRemoteAgentByPeerCard(app.AppDB(), task.PeerID, task.RemoteCardID)
		if remoteErr != nil || remote == nil {
			continue
		}
		peer, peerErr := findPeer(app, task.PeerID)
		if peerErr != nil {
			continue
		}
		remote, remoteErr = a.ensureRemoteCard(ctx, app, remote, peer)
		if remoteErr != nil {
			app.Logger().Warn("remote task card refresh failed", "task", task.ID, "peer", peer.Name, "err", remoteErr)
			continue
		}
		var response a2aTaskWire
		if callErr := a.callRemoteRPC(ctx, app, peer, remote.EndpointURL, "tasks/get", taskIDParams{ID: task.RemoteTaskID}, &response); callErr != nil {
			app.Logger().Warn("remote task sync failed", "task", task.ID, "peer", peer.Name, "err", callErr)
			continue
		}
		status := localStateFromA2A(response.Status.State)
		message := "Remote task is now " + strings.ReplaceAll(status, "_", " ") + "."
		if response.Status.Message != nil {
			if text := extractA2AText(*response.Status.Message); text != "" {
				message = text
			}
		}
		seen, _ := messageBodyExists(app.AppDB(), task.ID, 0, message)
		if status == task.Status && seen {
			continue
		}
		if status != task.Status {
			if err := setTaskSyncState(app.AppDB(), projectID, task.ID, status); err != nil {
				continue
			}
			task.Status = status
		}
		remoteIdentity := &callIdentity{AgentName: remote.Name, ProjectID: projectID}
		if err := deliverToParticipant(app, task, task.FromAgentID, formatReplyEvent(task, remoteIdentity, message)); err != nil {
			app.Logger().Warn("remote task reply delivery failed", "task", task.ID, "agent", task.FromAgentID, "err", err)
		}
		_ = recordMessage(app.AppDB(), task.ID, 0, task.FromAgentID, message, status)
		emitTask(app, "task.updated", task)
	}
	return nil
}
