// WhatsApp Web app — linked-device WhatsApp provider.
//
// This app intentionally keeps WhatsApp Web session/runtime concerns out of
// Messaging. It owns QR pairing, the websocket, and WhatsMeow persistence, then
// exposes provider-shaped tools that Messaging can call later.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	goproto "google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1

name: whatsapp-web
display_name: WhatsApp Web
version: 0.1.0
description: |
  WhatsApp linked-device provider for WhatsApp and WhatsApp Business app
  accounts. Uses WhatsApp Web multi-device pairing, not the official
  WhatsApp Business Platform API.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/whatsapp-web
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/whatsapp-web/icon.svg
tags: [whatsapp, messaging, whatsapp-web]

scopes: [project, global]
min_apteva_version: "0.10.0"

requires:
  permissions:
    - db.write.app
    - net.egress
  integrations: []

provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: whatsapp_web_connect,        description: "Start WhatsApp Web linked-device pairing. Returns status; scan the QR from the panel or /qr.png." }
    - { name: whatsapp_web_status,         description: "Read connection, QR, and sender status." }
    - { name: whatsapp_web_disconnect,     description: "Disconnect the current WhatsApp Web socket without deleting pairing state." }
    - { name: whatsapp_web_logout,         description: "Log out/unpair the linked WhatsApp device and remove local session state." }
    - { name: whatsapp_web_messages,       description: "List recent locally-observed WhatsApp Web messages. Args: limit?." }
    - { name: list_whatsapp_senders,       description: "Provider-compatible sender list for Messaging. Returns the linked WhatsApp account when paired." }
    - { name: send_whatsapp,               description: "Provider-compatible WhatsApp send. Args: To, Body, From?." }
  ui_panels:
    - slot: project.page
      label: WhatsApp Web
      icon: message-circle
      entry: /ui/WhatsAppWebPanel.mjs
  publishes:
    - name: account.connected
      description: A WhatsApp Web linked device connected.
      payload:
        phone: string
        jid: string
    - name: account.disconnected
      description: The WhatsApp Web socket disconnected.
      payload:
        phone: string
        jid: string
    - name: message.received
      description: A WhatsApp message was received.
      payload:
        from: string
        to: string
        body_text: string
        message_id: string
    - name: message.sent
      description: A WhatsApp message was sent.
      payload:
        from: string
        to: string
        body_text: string
        message_id: string

runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/whatsapp-web
  port: 8080
  health_check: /health

db:
  driver: sqlite
  path: /data/whatsapp-web.db
  migrations: migrations/

upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct {
	mu        sync.Mutex
	ctx       *sdk.AppCtx
	container *sqlstore.Container
	client    *whatsmeow.Client
	qrCode    string
	qrEvent   string
	status    string
	lastErr   string
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("whatsapp-web requires a db block")
	}
	dataDir := ctx.DataDir()
	if dataDir == "" {
		dataDir = filepath.Dir(os.Getenv("DB_PATH"))
	}
	if dataDir == "" || dataDir == "." {
		dataDir = "/data"
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	container, err := sqlstore.New(context.Background(), "sqlite", filepath.Join(dataDir, "whatsmeow-session.db"), waLog.Noop)
	if err != nil {
		return fmt.Errorf("open whatsmeow store: %w", err)
	}
	a.mu.Lock()
	a.ctx = ctx
	a.container = container
	a.status = "disconnected"
	a.mu.Unlock()
	globalCtx = ctx
	ctx.Logger().Info("whatsapp-web mounted")
	go a.autoConnectExisting()
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.mu.Lock()
	client := a.client
	container := a.container
	a.mu.Unlock()
	if client != nil {
		client.Disconnect()
	}
	if container != nil {
		_ = container.Close()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/connect", Handler: a.handleConnect},
		{Pattern: "/disconnect", Handler: a.handleDisconnect},
		{Pattern: "/logout", Handler: a.handleLogout},
		{Pattern: "/qr.png", Handler: a.handleQRPNG},
		{Pattern: "/messages", Handler: a.handleMessages},
		{Pattern: "/senders", Handler: a.handleSenders},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "whatsapp_web_connect",
			Description: "Start WhatsApp Web linked-device pairing. If no session exists, scan the QR from the UI panel or /qr.png. Args: force? (bool, rebuild client).",
			InputSchema: schemaObject(map[string]any{"force": map[string]any{"type": "boolean"}}, nil),
			Handler:     a.toolConnect,
		},
		{
			Name:        "whatsapp_web_status",
			Description: "Read current WhatsApp Web connection state, QR event, linked sender, and last error.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolStatus,
		},
		{
			Name:        "whatsapp_web_disconnect",
			Description: "Disconnect the WhatsApp Web websocket without deleting the pairing/session.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolDisconnect,
		},
		{
			Name:        "whatsapp_web_logout",
			Description: "Log out/unpair WhatsApp Web and remove the local linked-device session.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolLogout,
		},
		{
			Name:        "whatsapp_web_messages",
			Description: "List recent locally-observed WhatsApp messages. Args: limit? (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{"limit": map[string]any{"type": "integer"}}, nil),
			Handler:     a.toolMessages,
		},
		{
			Name:        "list_whatsapp_senders",
			Description: "Provider-compatible sender list for Messaging. Returns the linked WhatsApp account when paired.",
			InputSchema: schemaObject(map[string]any{"PageSize": map[string]any{"type": "integer"}}, nil),
			Handler:     a.toolListWhatsAppSenders,
		},
		{
			Name:        "send_whatsapp",
			Description: "Provider-compatible WhatsApp text send. Args: To (required), Body (required), From? (ignored unless it does not match the linked account).",
			InputSchema: schemaObject(map[string]any{
				"To":   map[string]any{"type": "string"},
				"Body": map[string]any{"type": "string"},
				"From": map[string]any{"type": "string"},
			}, []string{"To", "Body"}),
			Handler: a.toolSendWhatsApp,
		},
	}
}

func (a *App) autoConnectExisting() {
	time.Sleep(250 * time.Millisecond)
	a.mu.Lock()
	container := a.container
	a.mu.Unlock()
	if container == nil {
		return
	}
	device, err := container.GetFirstDevice(context.Background())
	if err != nil || device == nil || device.ID == nil {
		return
	}
	_ = a.startConnect(context.Background(), false)
}

func (a *App) startConnect(ctx context.Context, force bool) error {
	a.mu.Lock()
	if a.container == nil {
		a.mu.Unlock()
		return errors.New("whatsmeow store not ready")
	}
	if a.client != nil && a.client.IsConnected() && !force {
		a.mu.Unlock()
		return nil
	}
	if force && a.client != nil {
		a.client.Disconnect()
		a.client = nil
	}
	device, err := a.container.GetFirstDevice(ctx)
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("get device: %w", err)
	}
	if device == nil {
		device = a.container.NewDevice()
	}
	client := whatsmeow.NewClient(device, waLog.Noop)
	client.EnableAutoReconnect = true
	client.AddEventHandler(a.handleWAEvent)
	a.client = client
	a.status = "connecting"
	a.lastErr = ""
	a.qrCode = ""
	a.qrEvent = ""
	needsQR := client.Store.ID == nil
	var qrChan <-chan whatsmeow.QRChannelItem
	if needsQR {
		qrChan, err = client.GetQRChannel(ctx)
		if err != nil {
			a.status = "error"
			a.lastErr = err.Error()
			a.mu.Unlock()
			return fmt.Errorf("get qr channel: %w", err)
		}
	}
	a.mu.Unlock()

	if qrChan != nil {
		go a.consumeQR(qrChan)
	}
	go func() {
		if err := client.ConnectContext(ctx); err != nil {
			a.setRuntimeStatus("error", err.Error())
		}
	}()
	return nil
}

func (a *App) consumeQR(qrChan <-chan whatsmeow.QRChannelItem) {
	for item := range qrChan {
		a.mu.Lock()
		a.qrEvent = item.Event
		if item.Event == "code" {
			a.qrCode = item.Code
			a.status = "pairing"
			a.lastErr = ""
		} else if item.Event == "success" {
			a.qrCode = ""
			a.status = "connected"
			a.lastErr = ""
		} else if item.Event == "timeout" || strings.Contains(strings.ToLower(item.Event), "error") {
			a.status = "error"
			a.lastErr = item.Event
		}
		a.mu.Unlock()
	}
}

func (a *App) handleWAEvent(evt any) {
	switch v := evt.(type) {
	case *events.Connected:
		a.setRuntimeStatus("connected", "")
		a.upsertAccount("connected")
	case *events.Disconnected:
		a.setRuntimeStatus("disconnected", "")
		a.markAccountsDisconnected("")
	case *events.LoggedOut:
		a.setRuntimeStatus("logged_out", v.PermanentDisconnectDescription())
		a.markAccountsDisconnected(v.PermanentDisconnectDescription())
	case *events.Message:
		a.persistWAEvent(v)
	case *events.Receipt:
		a.persistReceipt(v)
	}
}

func (a *App) setRuntimeStatus(status, lastErr string) {
	a.mu.Lock()
	a.status = status
	a.lastErr = lastErr
	if status == "connected" {
		a.qrCode = ""
	}
	a.mu.Unlock()
}

func (a *App) projectID(ctx *sdk.AppCtx, args map[string]any) string {
	if args != nil {
		if v := strings.TrimSpace(strArg(args, "_project_id")); v != "" {
			return v
		}
	}
	if ctx != nil && ctx.CurrentProject() != "" {
		return ctx.CurrentProject()
	}
	if a.ctx != nil && a.ctx.CurrentProject() != "" {
		return a.ctx.CurrentProject()
	}
	return "global"
}

func (a *App) accountSnapshot() accountInfo {
	a.mu.Lock()
	client := a.client
	status := a.status
	lastErr := a.lastErr
	qrEvent := a.qrEvent
	hasQR := a.qrCode != ""
	a.mu.Unlock()
	out := accountInfo{Status: status, LastError: lastErr, QREvent: qrEvent, HasQR: hasQR}
	if client != nil {
		out.Connected = client.IsConnected()
		out.LoggedIn = client.IsLoggedIn()
		if client.Store != nil && client.Store.ID != nil {
			out.JID = client.Store.ID.String()
			out.Phone = phoneFromJID(*client.Store.ID)
			out.PushName = client.Store.PushName
			out.BusinessName = client.Store.BusinessName
			out.Platform = client.Store.Platform
		}
	}
	return out
}

func (a *App) upsertAccount(status string) {
	if a.ctx == nil || a.ctx.AppDB() == nil {
		return
	}
	acc := a.accountSnapshot()
	if acc.JID == "" {
		return
	}
	pid := a.projectID(a.ctx, nil)
	_, _ = a.ctx.AppDB().Exec(
		`INSERT INTO accounts
			(project_id, jid, phone, push_name, business_name, platform, status, last_error, connected_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, jid) DO UPDATE SET
		   phone = excluded.phone,
		   push_name = excluded.push_name,
		   business_name = excluded.business_name,
		   platform = excluded.platform,
		   status = excluded.status,
		   last_error = '',
		   connected_at = COALESCE(accounts.connected_at, CURRENT_TIMESTAMP),
		   updated_at = CURRENT_TIMESTAMP`,
		pid, acc.JID, acc.Phone, acc.PushName, acc.BusinessName, acc.Platform, status,
	)
	a.ctx.EmitWithProject("account.connected", pid, map[string]any{"phone": acc.Phone, "jid": acc.JID})
}

func (a *App) markAccountsDisconnected(reason string) {
	if a.ctx == nil || a.ctx.AppDB() == nil {
		return
	}
	acc := a.accountSnapshot()
	pid := a.projectID(a.ctx, nil)
	_, _ = a.ctx.AppDB().Exec(
		`UPDATE accounts
		    SET status = 'disconnected', last_error = ?, disconnected_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		  WHERE project_id = ?`,
		reason, pid,
	)
	a.ctx.EmitWithProject("account.disconnected", pid, map[string]any{"phone": acc.Phone, "jid": acc.JID})
}

func (a *App) persistWAEvent(evt *events.Message) {
	if a.ctx == nil || a.ctx.AppDB() == nil || evt == nil || evt.Message == nil {
		return
	}
	pid := a.projectID(a.ctx, nil)
	direction := "in"
	if evt.Info.IsFromMe {
		direction = "out"
	}
	body := messageText(evt.Message)
	from := phoneFromJID(evt.Info.Sender)
	if from == "" && evt.Info.IsFromMe {
		from = phoneFromJID(accountJID(a.client))
	}
	to := phoneFromJID(evt.Info.Chat)
	if evt.Info.IsFromMe {
		to = phoneFromJID(evt.Info.Chat)
	}
	raw, _ := json.Marshal(evt.RawMessage)
	_, _ = a.ctx.AppDB().Exec(
		`INSERT OR IGNORE INTO messages
			(project_id, direction, from_addr, to_addr, chat_jid, sender_jid, message_id, body_text, status, raw_json, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, direction, from, to, evt.Info.Chat.String(), evt.Info.Sender.String(), string(evt.Info.ID), body, "received", string(raw), evt.Info.Timestamp.UTC().Format(time.RFC3339),
	)
	if direction == "in" {
		a.ctx.EmitWithProject("message.received", pid, map[string]any{
			"from": from, "to": to, "body_text": body, "message_id": string(evt.Info.ID),
		})
	}
}

func (a *App) persistReceipt(evt *events.Receipt) {
	if a.ctx == nil || a.ctx.AppDB() == nil || evt == nil {
		return
	}
	status := receiptStatus(evt.Type)
	if status == "" {
		return
	}
	pid := a.projectID(a.ctx, nil)
	for _, id := range evt.MessageIDs {
		_, _ = a.ctx.AppDB().Exec(
			`UPDATE messages SET status = ? WHERE project_id = ? AND message_id = ?`,
			status, pid, string(id),
		)
	}
}

func (a *App) sendText(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	acc := a.accountSnapshot()
	if !acc.Connected || !acc.LoggedIn {
		return nil, errors.New("WhatsApp Web is not connected")
	}
	toRaw := firstNonEmpty(strArg(args, "To"), strArg(args, "to"))
	body := firstNonEmpty(strArg(args, "Body"), strArg(args, "body"), strArg(args, "body_text"))
	fromRaw := firstNonEmpty(strArg(args, "From"), strArg(args, "from"))
	if toRaw == "" {
		return nil, errors.New("To required")
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("Body required")
	}
	if fromRaw != "" {
		from := normalizePhone(fromRaw)
		if from != "" && acc.Phone != "" && from != acc.Phone {
			return nil, fmt.Errorf("From %s does not match linked WhatsApp account %s", from, acc.Phone)
		}
	}
	jid, err := jidFromPhone(toRaw)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()
	if client == nil {
		return nil, errors.New("WhatsApp Web client not ready")
	}
	resp, err := client.SendMessage(context.Background(), jid, &waProto.Message{
		Conversation: goproto.String(body),
	})
	if err != nil {
		return nil, err
	}
	pid := a.projectID(ctx, args)
	toPhone := normalizePhone(toRaw)
	_, _ = ctx.AppDB().Exec(
		`INSERT OR IGNORE INTO messages
			(project_id, direction, from_addr, to_addr, chat_jid, sender_jid, message_id, body_text, status, raw_json, occurred_at)
		 VALUES (?, 'out', ?, ?, ?, ?, ?, ?, 'sent', '{}', ?)`,
		pid, acc.Phone, toPhone, jid.String(), acc.JID, string(resp.ID), body, resp.Timestamp.UTC().Format(time.RFC3339),
	)
	ctx.EmitWithProject("message.sent", pid, map[string]any{
		"from": acc.Phone, "to": toPhone, "body_text": body, "message_id": string(resp.ID),
	})
	return map[string]any{
		"sid":         string(resp.ID),
		"id":          string(resp.ID),
		"from":        acc.Phone,
		"to":          toPhone,
		"status":      "sent",
		"timestamp":   resp.Timestamp.UTC().Format(time.RFC3339),
		"provider":    "whatsapp-web",
		"session_jid": acc.JID,
	}, nil
}

func (a *App) listSenders() map[string]any {
	acc := a.accountSnapshot()
	senders := []map[string]any{}
	if acc.JID != "" {
		status := "offline"
		if acc.Connected && acc.LoggedIn {
			status = "online"
		}
		label := firstNonEmpty(acc.BusinessName, acc.PushName, acc.Phone)
		senders = append(senders, map[string]any{
			"sid":           acc.JID,
			"sender_id":     "whatsapp:" + acc.Phone,
			"phone_number":  acc.Phone,
			"friendly_name": label,
			"status":        status,
			"provider":      "whatsapp-web",
		})
	}
	return map[string]any{"senders": senders}
}

func (a *App) toolConnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := a.startConnect(context.Background(), boolArg(args, "force", false)); err != nil {
		return nil, err
	}
	return a.toolStatus(ctx, args)
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]any{"account": a.accountSnapshot()}, nil
}

func (a *App) toolDisconnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	client := a.client
	a.status = "disconnected"
	a.qrCode = ""
	a.mu.Unlock()
	if client != nil {
		client.Disconnect()
	}
	return a.toolStatus(ctx, args)
}

func (a *App) toolLogout(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mu.Lock()
	client := a.client
	container := a.container
	a.client = nil
	a.status = "logged_out"
	a.qrCode = ""
	a.mu.Unlock()
	if client != nil {
		_ = client.Logout(context.Background())
		client.Disconnect()
		if container != nil && client.Store != nil {
			_ = container.DeleteDevice(context.Background(), client.Store)
		}
	}
	if ctx != nil && ctx.AppDB() != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE accounts SET status = 'logged_out', updated_at = CURRENT_TIMESTAMP`)
	}
	return a.toolStatus(ctx, args)
}

func (a *App) toolMessages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]any{"messages": a.recentMessages(ctx, args)}, nil
}

func (a *App) toolListWhatsAppSenders(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.listSenders(), nil
}

func (a *App) toolSendWhatsApp(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.sendText(ctx, args)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"account": a.accountSnapshot()})
}

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.startConnect(r.Context(), false); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"account": a.accountSnapshot()})
}

func (a *App) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _ = a.toolDisconnect(globalCtx, nil)
	writeJSON(w, map[string]any{"account": a.accountSnapshot()})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _ = a.toolLogout(globalCtx, nil)
	writeJSON(w, map[string]any{"account": a.accountSnapshot()})
}

func (a *App) handleQRPNG(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	code := a.qrCode
	a.mu.Unlock()
	if code == "" {
		http.Error(w, "no QR code available", http.StatusNotFound)
		return
	}
	png, err := qrcode.Encode(code, qrcode.Medium, 320)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"messages": a.recentMessages(globalCtx, map[string]any{"limit": r.URL.Query().Get("limit")})})
}

func (a *App) handleSenders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.listSenders())
}

func (a *App) recentMessages(ctx *sdk.AppCtx, args map[string]any) []messageRow {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	pid := a.projectID(ctx, args)
	rows, err := ctx.AppDB().Query(
		`SELECT id, direction, from_addr, to_addr, message_id, body_text, status, occurred_at
		   FROM messages
		  WHERE project_id = ?
		  ORDER BY occurred_at DESC, id DESC
		  LIMIT ?`,
		pid, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []messageRow{}
	for rows.Next() {
		var m messageRow
		_ = rows.Scan(&m.ID, &m.Direction, &m.From, &m.To, &m.MessageID, &m.BodyText, &m.Status, &m.OccurredAt)
		out = append(out, m)
	}
	return out
}

type accountInfo struct {
	Status       string `json:"status"`
	Connected    bool   `json:"connected"`
	LoggedIn     bool   `json:"logged_in"`
	JID          string `json:"jid,omitempty"`
	Phone        string `json:"phone,omitempty"`
	PushName     string `json:"push_name,omitempty"`
	BusinessName string `json:"business_name,omitempty"`
	Platform     string `json:"platform,omitempty"`
	QREvent      string `json:"qr_event,omitempty"`
	HasQR        bool   `json:"has_qr"`
	LastError    string `json:"last_error,omitempty"`
}

type messageRow struct {
	ID         int64  `json:"id"`
	Direction  string `json:"direction"`
	From       string `json:"from"`
	To         string `json:"to"`
	MessageID  string `json:"message_id"`
	BodyText   string `json:"body_text"`
	Status     string `json:"status"`
	OccurredAt string `json:"occurred_at"`
}

func messageText(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	if s := msg.GetConversation(); s != "" {
		return s
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetCaption()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetCaption()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return doc.GetCaption()
	}
	return ""
}

func receiptStatus(t types.ReceiptType) string {
	switch t {
	case types.ReceiptTypeDelivered:
		return "delivered"
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		return "read"
	case types.ReceiptTypePlayed:
		return "played"
	default:
		return ""
	}
}

func accountJID(client *whatsmeow.Client) types.JID {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return types.EmptyJID
	}
	return *client.Store.ID
}

func jidFromPhone(raw string) (types.JID, error) {
	phone := normalizePhone(raw)
	if phone == "" {
		return types.EmptyJID, fmt.Errorf("invalid WhatsApp phone %q", raw)
	}
	return types.NewJID(strings.TrimPrefix(phone, "+"), types.DefaultUserServer), nil
}

func phoneFromJID(jid types.JID) string {
	if jid.IsEmpty() {
		return ""
	}
	if jid.Server == types.DefaultUserServer && jid.User != "" {
		for _, r := range jid.User {
			if r < '0' || r > '9' {
				return ""
			}
		}
		return "+" + jid.User
	}
	return jid.String()
}

func normalizePhone(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "whatsapp:")
	s = strings.TrimPrefix(s, "tel:")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "(", "")
	s = strings.ReplaceAll(s, ")", "")
	if strings.HasSuffix(s, "@"+types.DefaultUserServer) {
		jid, err := types.ParseJID(s)
		if err == nil {
			return phoneFromJID(jid)
		}
	}
	if !strings.HasPrefix(s, "+") {
		s = "+" + s
	}
	if len(s) < 8 {
		return ""
	}
	for _, r := range strings.TrimPrefix(s, "+") {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		if v == "" {
			return def
		}
		return v == "true" || v == "1" || v == "yes"
	default:
		return def
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
