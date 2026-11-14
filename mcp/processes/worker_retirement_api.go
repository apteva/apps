package main

import (
	"context"
	"errors"
)

// Processes only releases its non-delegating workers. Callback affinity and
// durable state are checked before queuing retirement; delegated owners stay
// attached because the existing thread API cannot atomically guard children.
func (a *App) retireIdleWorker(ctx context.Context, project string, agent int64, thread string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	api := a.ctx.WithProject(project).ThreadAPI()
	if api == nil {
		return errors.New("platform thread API unavailable")
	}
	return api.KillThread(agent, thread)
}
