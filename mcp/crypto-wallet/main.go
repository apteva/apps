package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

const appName = "crypto-wallet"

type App struct {
	ctx    *sdk.AppCtx
	db     *sql.DB
	keys   *keystore
	prices priceSource
	now    func() time.Time

	clientsMu sync.Mutex
	clients   map[string]chainClient // by chain slug

	locksMu sync.Mutex
	locks   map[int64]*sync.Mutex // one per wallet: proposals for a wallet run one at a time

	onEmit func(topic string, data map[string]any) // tests: capture published events
}

func newApp() *App {
	return &App{
		prices:  newSpotPrices(),
		now:     time.Now,
		clients: map[string]chainClient{},
		locks:   map[int64]*sync.Mutex{},
		keys:    &keystore{},
	}
}

func main() { sdk.Run(newApp()) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("crypto-wallet requires a db block")
	}
	a.ctx, a.db = ctx, ctx.AppDB()
	raw := os.Getenv("CRYPTO_WALLET_MASTER_KEY")
	if raw == "" {
		raw = ctx.Config().Get("master_key")
	}
	master, err := parseMasterKey(raw)
	if err != nil {
		// Boot anyway so the panel can explain the problem; every key
		// operation fails with errNoMasterKey until it's fixed.
		ctx.Logger().Error("master key invalid", "err", err)
	}
	a.keys = &keystore{master: master}
	ctx.Logger().Info("crypto-wallet mounted", "master_key_configured", a.keys.configured())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return nil
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "chain-watcher", Schedule: "@every 20s", Run: a.runWatcher}}
}

func (a *App) walletLock(id int64) *sync.Mutex {
	a.locksMu.Lock()
	defer a.locksMu.Unlock()
	l, ok := a.locks[id]
	if !ok {
		l = &sync.Mutex{}
		a.locks[id] = l
	}
	return l
}

func (a *App) client(chain *chainInfo) chainClient {
	a.clientsMu.Lock()
	defer a.clientsMu.Unlock()
	if c, ok := a.clients[chain.Slug]; ok {
		return c
	}
	url := chain.DefaultRPC
	if a.ctx != nil {
		if v := strings.TrimSpace(a.ctx.Config().Get(chain.RPCConfig)); v != "" {
			url = v
		}
	}
	c := newRPCClient(url)
	a.clients[chain.Slug] = c
	return c
}

func (a *App) emit(topic string, data map[string]any) {
	if a.onEmit != nil {
		a.onEmit(topic, data)
	}
	if a.ctx != nil {
		a.ctx.Emit(topic, data)
	}
}

// withTx runs fn in a transaction, committing on nil.
func (a *App) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func bg() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 45*time.Second)
}
