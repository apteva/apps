package main

import (
	sdk "github.com/apteva/app-sdk"
	"strings"
	"sync"
	"time"
)

// Unit tests can exercise individual dispatches synchronously; the executable
// always enables this bounded, durable queue. Wakeups carry no message payload.
type deliveryWorker struct {
	wake chan struct{}
	stop chan struct{}
	done sync.WaitGroup
}

func (a *App) startDeliveryWorker(ctx *sdk.AppCtx) {
	a.deliveryWorker = &deliveryWorker{wake: make(chan struct{}, 4), stop: make(chan struct{})}
	for i := 0; i < 4; i++ {
		a.deliveryWorker.done.Add(1)
		go func() {
			defer a.deliveryWorker.done.Done()
			for {
				select {
				case <-a.deliveryWorker.stop:
					return
				case <-a.deliveryWorker.wake:
					if _, err := a.redeliverPending(ctx); err != nil {
						ctx.Logger().Warn("delivery recovery failed", "err", err)
					}
				}
			}
		}()
	}
}
func (a *App) wakeDeliveries() {
	if a.deliveryWorker == nil {
		return
	}
	for i := 0; i < 4; i++ {
		select {
		case a.deliveryWorker.wake <- struct{}{}:
		default:
		}
	}
}
func (a *App) dispatchOrQueue(ctx *sdk.AppCtx, target string, conv *Conversation, msg *Message) {
	if a.deliveryWorker != nil && !strings.HasPrefix(target, "web:") {
		a.wakeDeliveries()
		return
	}
	a.attemptDelivery(ctx, target, conv, msg)
}
func (a *App) maintainLease(msgID int64, target, token string) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = a.store.db.Exec(`UPDATE deliveries SET lease_until=? WHERE message_id=? AND target=? AND lease_token=? AND status='processing'`, time.Now().Unix()+120, msgID, target, token)
			}
		}
	}()
	return func() { close(stop); <-done }
}

func (a *App) maintainTelegramLease(connectionID, updateID int64, token string) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = a.store.db.Exec(`UPDATE telegram_updates SET lease_until=? WHERE connection_id=? AND update_id=? AND lease_token=? AND completed=0`, time.Now().Unix()+120, connectionID, updateID, token)
			}
		}
	}()
	return func() { close(stop); <-done }
}
