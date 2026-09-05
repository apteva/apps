package main

import (
	"context"
	"database/sql"
	sdk "github.com/apteva/app-sdk"
	"sync"
)

type mediaLifecycle struct {
	done    chan struct{}
	mu      sync.Mutex
	closing bool
	once    sync.Once
	workers sync.WaitGroup
}

var mediaLifecycles sync.Map

func initMediaLifecycle(app *sdk.AppCtx) {
	mediaLifecycles.Store(app.AppDB(), &mediaLifecycle{done: make(chan struct{})})
}
func mediaDone(app *sdk.AppCtx) <-chan struct{} {
	if life, ok := mediaLifecycles.Load(app.AppDB()); ok {
		return life.(*mediaLifecycle).done
	}
	return app.Done()
}
func startMediaWorker(app *sdk.AppCtx, work func()) bool {
	if life, ok := mediaLifecycles.Load(app.AppDB()); ok {
		l := life.(*mediaLifecycle)
		l.mu.Lock()
		if l.closing {
			l.mu.Unlock()
			return false
		}
		l.workers.Add(1)
		l.mu.Unlock()
		go func() { defer l.workers.Done(); work() }()
		return true
	}
	go work()
	return true
}
func stopMediaLifecycle(db *sql.DB) {
	if life, ok := mediaLifecycles.Load(db); ok {
		l := life.(*mediaLifecycle)
		l.mu.Lock()
		l.closing = true
		l.once.Do(func() { close(l.done) })
		l.mu.Unlock()
		l.workers.Wait()
		mediaLifecycles.Delete(db)
	}
}
func mediaContext(parent context.Context, app *sdk.AppCtx) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-ctx.Done():
		case <-mediaDone(app):
			cancel()
		case <-app.Done():
			cancel()
		}
	}()
	return ctx, cancel
}
