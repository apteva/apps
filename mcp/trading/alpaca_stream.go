package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/coder/websocket"
)

const (
	alpacaStreamReconnectMin = time.Second
	alpacaStreamReconnectMax = 30 * time.Second
)

type streamHealth struct {
	Status       string `json:"status"`
	Feed         string `json:"feed,omitempty"`
	ConnectionID int64  `json:"connection_id,omitempty"`
	ConnectedAt  string `json:"connected_at,omitempty"`
	LastEventAt  string `json:"last_event_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
}

var alpacaStreams = struct {
	sync.RWMutex
	byProject map[string]map[string]streamHealth
}{byProject: map[string]map[string]streamHealth{}}

func setAlpacaStreamHealth(app *sdk.AppCtx, kind string, state streamHealth) {
	projectID := app.CurrentProject()
	alpacaStreams.Lock()
	if alpacaStreams.byProject[projectID] == nil {
		alpacaStreams.byProject[projectID] = map[string]streamHealth{}
	}
	previous := alpacaStreams.byProject[projectID][kind]
	alpacaStreams.byProject[projectID][kind] = state
	alpacaStreams.Unlock()
	if previous.Status != state.Status || previous.Feed != state.Feed || previous.LastError != state.LastError {
		app.Emit("provider.health.changed", map[string]any{
			"schema_version": 1, "provider": "alpaca", "stream": kind,
			"status": state.Status, "feed": state.Feed, "last_error": state.LastError,
		})
	}
}

func alpacaStreamHealthSnapshot(projectID string) map[string]streamHealth {
	alpacaStreams.RLock()
	defer alpacaStreams.RUnlock()
	out := map[string]streamHealth{}
	for key, value := range alpacaStreams.byProject[projectID] {
		out[key] = value
	}
	return out
}

// startAlpacaStreams is a one-shot SDK worker. It starts project-scoped
// supervisors and returns so global installs can be dispatched for every
// project. The goroutines inherit the SDK shutdown context and stop cleanly.
func startAlpacaStreams(ctx context.Context, app *sdk.AppCtx) error {
	go superviseAlpacaMarketStream(ctx, app)
	go superviseAlpacaTradeStream(ctx, app)
	go superviseAlpacaCorporateActionsStream(ctx, app)
	return nil
}

func superviseAlpacaCorporateActionsStream(ctx context.Context, app *sdk.AppCtx) {
	backoff := alpacaStreamReconnectMin
	for ctx.Err() == nil {
		connID := boundConnectionID(app, "reference_actions", alpacaMarketDataSlug)
		if connID == 0 {
			connID = boundConnectionID(app, "market_data_equity", alpacaMarketDataSlug)
		}
		if connID == 0 {
			if discovered, ok := findActiveConnection(app.PlatformAPI(), alpacaMarketDataSlug); ok {
				connID = discovered
			}
		}
		if connID == 0 {
			setAlpacaStreamHealth(app, "corporate_actions", streamHealth{Status: "unbound"})
			if !waitContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		err := runAlpacaCorporateActionsStream(ctx, app, connID)
		if ctx.Err() != nil {
			return
		}
		setAlpacaStreamHealth(app, "corporate_actions", streamHealth{Status: "reconnecting", ConnectionID: connID, LastError: cleanStreamError(err)})
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = minDuration(backoff*2, alpacaStreamReconnectMax)
	}
}

func runAlpacaCorporateActionsStream(ctx context.Context, app *sdk.AppCtx, connID int64) error {
	key, secret, err := connectionCredentials(app, connID)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://stream.data.alpaca.markets/v1beta1/events/corporate-actions?region=all", nil)
	if err != nil {
		return err
	}
	request.Header.Set("APCA-API-KEY-ID", key)
	request.Header.Set("APCA-API-SECRET-KEY", secret)
	request.Header.Set("Accept", "text/event-stream")
	var checkpoint string
	_ = app.AppDB().QueryRow(`SELECT cursor FROM reference_data_checkpoints WHERE provider=? AND stream='corporate_actions_sse'`, referenceProviderAlpaca).Scan(&checkpoint)
	if checkpoint != "" {
		request.Header.Set("Last-Event-ID", checkpoint)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("alpaca corporate-actions stream: %s", response.Status)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	health := streamHealth{Status: "connected", Feed: "global", ConnectionID: connID, ConnectedAt: now, LastEventAt: now}
	setAlpacaStreamHealth(app, "corporate_actions", health)
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	eventID := ""
	data := strings.Builder{}
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := []byte(data.String())
		if err := applyAlpacaCorporateActionEvent(app, eventID, payload); err != nil {
			return err
		}
		health.LastEventAt = time.Now().UTC().Format(time.RFC3339Nano)
		setAlpacaStreamHealth(app, "corporate_actions", health)
		if eventID != "" {
			_ = dbSetReferenceCheckpoint(app.AppDB(), referenceProviderAlpaca, "corporate_actions_sse", eventID, health.LastEventAt, "", 1)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				app.Logger().Warn("alpaca corporate action event rejected", "err", err)
			}
			eventID = ""
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "id:") {
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("alpaca corporate-actions stream ended")
}

func applyAlpacaCorporateActionEvent(app *sdk.AppCtx, eventID string, payload []byte) error {
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	rawCA, ok := envelope["ca"].(map[string]any)
	if !ok {
		if nested, ok := envelope["corporate_action"].(map[string]any); ok {
			rawCA = nested
		} else {
			return nil
		}
	}
	typ := strings.ToLower(strings.TrimSuffix(refString(envelope, "event_type", "type"), "_corporateaction_event"))
	if !supportedCorporateActionTypes[typ] {
		return fmt.Errorf("unsupported corporate action event type %q", typ)
	}
	rawCA["type"] = typ
	if refString(rawCA, "id") == "" {
		rawCA["id"] = refString(envelope, "corporate_action_id", "id")
	}
	wrapper, _ := json.Marshal(map[string]any{"corporate_actions": map[string]any{"events": []any{rawCA}}})
	actions, _, err := parseAlpacaCorporateActions(wrapper)
	if err != nil || len(actions) == 0 {
		return err
	}
	action := actions[0]
	operation := strings.ToLower(refString(envelope, "action", "event_action", "operation"))
	if operation == "delete" {
		action.Status = "deleted"
	}
	canonical, _ := json.Marshal(envelope)
	action.RawJSON = string(canonical)
	action.PayloadSHA256 = fmt.Sprintf("%x", sha256.Sum256(canonical))
	securityID, err := resolveSecurityID(app.AppDB(), referenceProviderAlpaca, "", action.ISIN, action.CUSIP, "", action.Symbol)
	if err != nil {
		return err
	}
	action.SecurityID = securityID
	if err := ensurePlaceholderSecurity(app.AppDB(), action); err != nil {
		return err
	}
	inserted, corrected, err := dbUpsertCorporateAction(app.AppDB(), action)
	if err != nil {
		return err
	}
	if inserted {
		topic := "corporate_action.received"
		if corrected {
			topic = "corporate_action.corrected"
		}
		app.Emit(topic, map[string]any{"schema_version": 1, "event_id": eventID, "provider": action.Provider, "provider_event_id": action.ProviderEventID, "revision": action.Revision, "action_type": action.ActionType, "symbol": action.Symbol, "operation": operation})
		_ = applyDueCorporateActions(context.Background(), app)
	}
	return nil
}

func superviseAlpacaMarketStream(ctx context.Context, app *sdk.AppCtx) {
	backoff := alpacaStreamReconnectMin
	for ctx.Err() == nil {
		connID := boundConnectionID(app, "market_data_equity", alpacaMarketDataSlug)
		if connID == 0 {
			setAlpacaStreamHealth(app, "market_data", streamHealth{Status: "unbound"})
			if !waitContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		err := runAlpacaMarketStream(ctx, app, connID)
		if ctx.Err() != nil {
			return
		}
		setAlpacaStreamHealth(app, "market_data", streamHealth{Status: "reconnecting", ConnectionID: connID, LastError: cleanStreamError(err)})
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = minDuration(backoff*2, alpacaStreamReconnectMax)
	}
}

func superviseAlpacaTradeStream(ctx context.Context, app *sdk.AppCtx) {
	backoff := alpacaStreamReconnectMin
	for ctx.Err() == nil {
		connID := boundConnectionID(app, "broker", "alpaca-trading")
		if connID == 0 {
			setAlpacaStreamHealth(app, "trade_updates", streamHealth{Status: "unbound"})
			if !waitContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		err := runAlpacaTradeStream(ctx, app, connID)
		if ctx.Err() != nil {
			return
		}
		setAlpacaStreamHealth(app, "trade_updates", streamHealth{Status: "reconnecting", ConnectionID: connID, LastError: cleanStreamError(err)})
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = minDuration(backoff*2, alpacaStreamReconnectMax)
	}
}

func boundConnectionID(app *sdk.AppCtx, role, slug string) int64 {
	if app == nil {
		return 0
	}
	for _, bound := range app.IntegrationsFor(role) {
		if bound != nil && bound.ConnectionID > 0 && strings.EqualFold(bound.AppSlug, slug) {
			return bound.ConnectionID
		}
	}
	return 0
}

func connectionCredentials(app *sdk.AppCtx, connID int64) (string, string, error) {
	if app == nil || app.PlatformAPI() == nil {
		return "", "", errors.New("platform unavailable")
	}
	creds, err := app.PlatformAPI().GetConnectionCredentials(connID)
	if err != nil {
		return "", "", err
	}
	key := strings.TrimSpace(creds.Fields["api_key"])
	secret := strings.TrimSpace(creds.Fields["api_secret"])
	if key == "" || secret == "" {
		return "", "", errors.New("alpaca API key or secret missing")
	}
	return key, secret, nil
}

func alpacaConnectionEnvironment(app *sdk.AppCtx, connID int64) (string, bool) {
	if app == nil || app.PlatformAPI() == nil {
		return "", false
	}
	public, err := sdk.GetConnectionPublicConfig(app.PlatformAPI(), connID)
	if err == nil && public != nil {
		if environment, ok := alpacaHostEnvironment(public.Fields["host"]); ok {
			return environment, true
		}
	}
	// Older servers/catalog snapshots do not expose host as public metadata.
	// Trading already needs credential access for the WebSockets, so inspect
	// host server-side as a compatibility fallback without returning secrets.
	credentials, err := app.PlatformAPI().GetConnectionCredentials(connID)
	if err != nil {
		return "", false
	}
	return alpacaHostEnvironment(credentials.Fields["host"])
}

func alpacaHostEnvironment(host string) (string, bool) {
	switch strings.TrimSpace(host) {
	case "paper-api.alpaca.markets":
		return "broker_paper", true
	case "api.alpaca.markets":
		return "broker_live", true
	default:
		return "", false
	}
}

func runAlpacaMarketStream(ctx context.Context, app *sdk.AppCtx, connID int64) error {
	key, secret, err := connectionCredentials(app, connID)
	if err != nil {
		return err
	}
	configured := strings.ToLower(strings.TrimSpace(app.Config().Get("alpaca_feed")))
	feeds := []string{configured}
	if configured == "" || configured == "auto" {
		feeds = []string{"sip", "iex"}
	}
	var lastErr error
	for _, feed := range feeds {
		if feed != "sip" && feed != "iex" && feed != "delayed_sip" {
			continue
		}
		lastErr = consumeAlpacaMarketFeed(ctx, app, connID, key, secret, feed)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if configured != "" && configured != "auto" {
			return lastErr
		}
		if !alpacaEntitlementError(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

func consumeAlpacaMarketFeed(ctx context.Context, app *sdk.AppCtx, connID int64, key, secret, feed string) error {
	url := "wss://stream.data.alpaca.markets/v2/" + feed
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutdown")
	conn.SetReadLimit(8 << 20)
	if err := writeWSJSON(ctx, conn, map[string]any{"action": "auth", "key": key, "secret": secret}); err != nil {
		return err
	}
	if err := awaitAlpacaSuccess(ctx, conn, "authenticated"); err != nil {
		return err
	}

	symbols, err := alpacaSubscriptionSymbols(app.AppDB())
	if err != nil {
		return err
	}
	if len(symbols) == 0 {
		symbols = alpacaEquitySymbolsKnown()
	}
	if err := writeWSJSON(ctx, conn, map[string]any{
		"action": "subscribe", "quotes": symbols, "trades": symbols, "bars": symbols,
		"updatedBars": symbols, "statuses": symbols,
	}); err != nil {
		return err
	}
	streamCtx, stopSubscriptions := context.WithCancel(ctx)
	defer stopSubscriptions()
	knownSymbols := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		knownSymbols[symbol] = true
	}
	go maintainAlpacaSubscriptions(streamCtx, app, conn, knownSymbols)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	health := streamHealth{Status: "connected", Feed: feed, ConnectionID: connID, ConnectedAt: now, LastEventAt: now}
	setAlpacaStreamHealth(app, "market_data", health)
	coalescer := newMarkEventCoalescer(ctx, app, feed)
	defer coalescer.Close()

	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		health.LastEventAt = time.Now().UTC().Format(time.RFC3339Nano)
		setAlpacaStreamHealth(app, "market_data", health)
		if err := applyAlpacaMarketPayload(app, feed, payload, coalescer); err != nil {
			app.Logger().Warn("alpaca market stream payload rejected", "err", err)
		}
	}
}

// maintainAlpacaSubscriptions expands the stream as operators edit
// watchlists or strategies create positions/orders. A periodic refresh is a
// low-volume control-plane operation; quote data itself remains push-only.
func maintainAlpacaSubscriptions(ctx context.Context, app *sdk.AppCtx, conn *websocket.Conn, known map[string]bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			symbols, err := alpacaSubscriptionSymbols(app.AppDB())
			if err != nil {
				continue
			}
			added := []string{}
			for _, symbol := range symbols {
				if !known[symbol] {
					known[symbol] = true
					added = append(added, symbol)
				}
			}
			if len(added) == 0 {
				continue
			}
			if err := writeWSJSON(ctx, conn, map[string]any{
				"action": "subscribe", "quotes": added, "trades": added, "bars": added,
				"updatedBars": added, "statuses": added,
			}); err != nil {
				_ = conn.CloseNow()
				return
			}
		}
	}
}

type alpacaMarketMessage struct {
	Type       string  `json:"T"`
	Symbol     string  `json:"S"`
	Time       string  `json:"t"`
	Price      float64 `json:"p"`
	Size       float64 `json:"s"`
	BidPrice   float64 `json:"bp"`
	AskPrice   float64 `json:"ap"`
	BidSize    float64 `json:"bs"`
	AskSize    float64 `json:"as"`
	Open       float64 `json:"o"`
	High       float64 `json:"h"`
	Low        float64 `json:"l"`
	Close      float64 `json:"c"`
	Volume     float64 `json:"v"`
	VWAP       float64 `json:"vw"`
	TradeCount int64   `json:"n"`
	StatusCode string  `json:"sc"`
	Status     string  `json:"sm"`
	ReasonCode string  `json:"rc"`
	Reason     string  `json:"rm"`
	Message    string  `json:"msg"`
	Code       int     `json:"code"`
}

func applyAlpacaMarketPayload(app *sdk.AppCtx, feed string, payload []byte, coalescer *markEventCoalescer) error {
	var messages []alpacaMarketMessage
	if err := json.Unmarshal(payload, &messages); err != nil {
		return err
	}
	for _, message := range messages {
		symbol := canonicalSymbol(message.Symbol)
		switch message.Type {
		case "q", "t", "b", "u", "d":
			if symbol == "" {
				continue
			}
			mark, err := streamedAlpacaMark(app.AppDB(), feed, message)
			if err != nil {
				return err
			}
			if err := dbUpsertMark(app.AppDB(), mark); err != nil {
				return err
			}
			coalescer.Add(mark)
			if message.Type == "b" || message.Type == "u" || message.Type == "d" {
				bar := &MarketBar{Symbol: symbol, Timeframe: "1m", BarAt: message.Time, Open: message.Open, High: message.High,
					Low: message.Low, Close: message.Close, Volume: message.Volume, TradeCount: message.TradeCount,
					VWAP: message.VWAP, Source: alpacaMarketDataSlug, Feed: feed,
					ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano), Complete: message.Type != "d"}
				if err := dbUpsertMarketBar(app.AppDB(), bar); err != nil {
					return err
				}
				topic := "market.bar.closed"
				if message.Type == "u" {
					topic = "market.bar.corrected"
				}
				if message.Type == "d" {
					topic = "market.bar.updated"
				}
				app.Emit(topic, map[string]any{"schema_version": 1, "source": alpacaMarketDataSlug, "feed": feed, "bar": bar})
			}
		case "s":
			app.Emit("market.status.changed", map[string]any{"schema_version": 1, "source": alpacaMarketDataSlug,
				"feed": feed, "symbol": symbol, "status_code": message.StatusCode, "status": message.Status,
				"reason_code": message.ReasonCode, "reason": message.Reason, "occurred_at": message.Time})
		case "error":
			return fmt.Errorf("alpaca stream %d: %s", message.Code, message.Message)
		}
	}
	return nil
}

func streamedAlpacaMark(db *sql.DB, feed string, message alpacaMarketMessage) (*Mark, error) {
	symbol := canonicalSymbol(message.Symbol)
	mark, _ := dbGetMark(db, symbol)
	if mark == nil {
		mark = &Mark{Symbol: symbol, AssetClass: inferAssetClass(symbol)}
	}
	mark.Source = alpacaMarketDataSlug
	mark.Feed = feed
	mark.TimestampKind = "exchange"
	mark.MarkedAt = message.Time
	if mark.MarkedAt == "" {
		mark.MarkedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if mark.Instrument == nil {
		mark.Instrument = defaultInstrument(symbol, mark.AssetClass, alpacaMarketDataSlug, time.Now().UTC())
		mark.Instrument.Exchange = "ALPACA_US"
	}
	switch message.Type {
	case "q":
		if message.BidPrice > 0 {
			mark.BidPrice = ptr(message.BidPrice)
		}
		if message.AskPrice > 0 {
			mark.AskPrice = ptr(message.AskPrice)
		}
		if message.BidSize > 0 {
			mark.BidSize = ptr(message.BidSize)
		}
		if message.AskSize > 0 {
			mark.AskSize = ptr(message.AskSize)
		}
		mark.QuoteAt = message.Time
		if mark.Price <= 0 {
			switch {
			case message.BidPrice > 0 && message.AskPrice > 0:
				mark.Price = (message.BidPrice + message.AskPrice) / 2
			case message.BidPrice > 0:
				mark.Price = message.BidPrice
			case message.AskPrice > 0:
				mark.Price = message.AskPrice
			}
		}
	case "t":
		if message.Price > 0 {
			mark.Price = message.Price
			mark.LastTradePrice = ptr(message.Price)
		}
		if message.Size > 0 {
			mark.LastTradeSize = ptr(message.Size)
		}
	case "b", "u", "d":
		if message.Close > 0 {
			mark.Price = message.Close
		}
		if message.Volume > 0 {
			mark.Volume24h = ptr(message.Volume)
		}
	}
	if mark.Price <= 0 {
		return nil, fmt.Errorf("alpaca stream message for %s has no executable price", symbol)
	}
	return mark, nil
}

type markEventCoalescer struct {
	add    chan *Mark
	cancel context.CancelFunc
}

func newMarkEventCoalescer(parent context.Context, app *sdk.AppCtx, feed string) *markEventCoalescer {
	ctx, cancel := context.WithCancel(parent)
	c := &markEventCoalescer{add: make(chan *Mark, 512), cancel: cancel}
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		pending := map[string]*Mark{}
		flush := func() {
			if len(pending) == 0 {
				return
			}
			marks := make([]*Mark, 0, len(pending))
			for _, mark := range pending {
				marks = append(marks, mark)
			}
			sort.Slice(marks, func(i, j int) bool { return marks[i].Symbol < marks[j].Symbol })
			app.Emit("market.quotes.updated", map[string]any{"schema_version": 1, "source": alpacaMarketDataSlug, "feed": feed, "marks": marks})
			pending = map[string]*Mark{}
		}
		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case mark := <-c.add:
				if mark != nil {
					pending[mark.Symbol] = mark
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
	return c
}

func (c *markEventCoalescer) Add(mark *Mark) {
	select {
	case c.add <- mark:
	default:
	}
}
func (c *markEventCoalescer) Close() { c.cancel() }

func alpacaSubscriptionSymbols(db *sql.DB) ([]string, error) {
	all, err := dbListRefreshSymbols(db)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, symbol := range all {
		class := inferAssetClass(symbol)
		if class == "equity" || class == "etf" {
			out = append(out, strings.ToUpper(symbol))
		}
	}
	sort.Strings(out)
	return out, nil
}

func runAlpacaTradeStream(ctx context.Context, app *sdk.AppCtx, connID int64) error {
	key, secret, err := connectionCredentials(app, connID)
	if err != nil {
		return err
	}
	host := "paper-api.alpaca.markets"
	if environment, ok := alpacaConnectionEnvironment(app, connID); ok && environment == "broker_live" {
		host = "api.alpaca.markets"
	}
	conn, _, err := websocket.Dial(ctx, "wss://"+host+"/stream", &websocket.DialOptions{HTTPHeader: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutdown")
	if err := writeWSJSON(ctx, conn, map[string]any{"action": "authenticate", "data": map[string]any{"key_id": key, "secret_key": secret}}); err != nil {
		return err
	}
	if err := writeWSJSON(ctx, conn, map[string]any{"action": "listen", "data": map[string]any{"streams": []string{"trade_updates"}}}); err != nil {
		return err
	}
	env := "broker_paper"
	if host == "api.alpaca.markets" {
		env = "broker_live"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	health := streamHealth{Status: "connected", Feed: env, ConnectionID: connID, ConnectedAt: now, LastEventAt: now}
	setAlpacaStreamHealth(app, "trade_updates", health)
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		health.LastEventAt = time.Now().UTC().Format(time.RFC3339Nano)
		setAlpacaStreamHealth(app, "trade_updates", health)
		if err := applyAlpacaTradePayload(app, env, payload); err != nil {
			app.Logger().Warn("alpaca trade update rejected", "err", err)
		}
	}
}

func applyAlpacaTradePayload(app *sdk.AppCtx, environment string, payload []byte) error {
	var envelope struct {
		Stream string `json:"stream"`
		Data   struct {
			Event string          `json:"event"`
			Order json.RawMessage `json:"order"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if envelope.Stream != "trade_updates" || len(envelope.Data.Order) == 0 {
		return nil
	}
	var identity struct {
		ClientOrderID string `json:"client_order_id"`
	}
	if err := json.Unmarshal(envelope.Data.Order, &identity); err != nil {
		return err
	}
	if identity.ClientOrderID == "" {
		return nil
	}
	order, err := dbGetOrderAnyProject(app.AppDB(), identity.ClientOrderID)
	if err != nil {
		return nil
	}
	portfolio, err := dbGetPortfolioAnyProject(app.AppDB(), order.PortfolioID)
	if err != nil || portfolio.BrokerSlug != "alpaca-trading" {
		return err
	}
	adapter := alpacaAdapter{}
	brokerOrder, err := adapter.ParseOrder(envelope.Data.Order)
	if err != nil {
		return err
	}
	changed, err := applyBrokerProgress(app.AppDB(), portfolio.ProjectID, portfolio, order, brokerOrder)
	if err != nil {
		return err
	}
	app.Emit("order.state.changed", map[string]any{"schema_version": 1, "portfolio_id": portfolio.ID,
		"order_id": order.ID, "broker_order_id": brokerOrder.BrokerOrderID, "broker_event": envelope.Data.Event,
		"status": brokerOrder.Status, "filled_qty": brokerOrder.ExecutedQty, "execution_environment": environment,
		"changed": changed})
	return nil
}

func writeWSJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func awaitAlpacaSuccess(ctx context.Context, conn *websocket.Conn, want string) error {
	_, payload, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	var messages []alpacaMarketMessage
	if err := json.Unmarshal(payload, &messages); err != nil {
		return err
	}
	for _, message := range messages {
		if message.Type == "success" && message.Message == want {
			return nil
		}
		if message.Type == "error" {
			return fmt.Errorf("alpaca stream %d: %s", message.Code, message.Message)
		}
	}
	return fmt.Errorf("alpaca stream did not acknowledge %s", want)
}

func alpacaEntitlementError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "409") || strings.Contains(text, "subscription") || strings.Contains(text, "entitlement")
}

func cleanStreamError(err error) string {
	if err == nil {
		return "stream ended"
	}
	text := err.Error()
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
