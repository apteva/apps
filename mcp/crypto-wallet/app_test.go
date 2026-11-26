package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// ─── Fakes ───────────────────────────────────────────────────────────

type fakeChain struct {
	mu       sync.Mutex
	chainID  int64
	nonce    uint64
	sent     [][]byte
	sendErr  error
	receipts map[string]*txReceipt
	head     uint64
	logs     []transferLog
}

func (f *fakeChain) ChainID(context.Context) (*big.Int, error) { return big.NewInt(f.chainID), nil }
func (f *fakeChain) BlockNumber(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}
func (f *fakeChain) Balance(context.Context, [20]byte) (*big.Int, error) {
	return big.NewInt(1_000_000_000_000_000_000), nil
}
func (f *fakeChain) TokenBalance(context.Context, [20]byte, [20]byte) (*big.Int, error) {
	return big.NewInt(500_000_000), nil
}
func (f *fakeChain) PendingNonce(context.Context, [20]byte) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nonce, nil
}
func (f *fakeChain) SuggestFees(context.Context) (*big.Int, *big.Int, error) {
	return big.NewInt(1_000_000), big.NewInt(3_000_000), nil
}
func (f *fakeChain) EstimateGas(context.Context, [20]byte, [20]byte, *big.Int, []byte) (uint64, error) {
	return 50_000, nil
}
func (f *fakeChain) SendRawTransaction(_ context.Context, raw []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.sent = append(f.sent, raw)
	f.nonce++
	return "0x" + hex.EncodeToString(keccak256(raw)), nil
}
func (f *fakeChain) Receipt(_ context.Context, hash string) (*txReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.receipts[hash], nil
}
func (f *fakeChain) TransferLogs(_ context.Context, _ [20]byte, _ [20]byte, from, to uint64) ([]transferLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []transferLog
	for _, l := range f.logs {
		if l.BlockNumber >= from && l.BlockNumber <= to {
			out = append(out, l)
		}
	}
	return out, nil
}

type fixedPrices struct{ ethUSD int64 }

func (p fixedPrices) USDMicros(_ context.Context, symbol string, amount *big.Int, decimals int) (int64, error) {
	switch symbol {
	case "USDC":
		return usdMicros(amount, decimals, big.NewRat(1, 1))
	case "ETH":
		if p.ethUSD == 0 {
			return 0, errors.New("ETH price unavailable")
		}
		return usdMicros(amount, decimals, big.NewRat(p.ethUSD, 1))
	}
	return 0, errors.New("no price")
}

type card struct{ title, body string }

type harness struct {
	t     *testing.T
	app   *App
	chain *fakeChain
	cards []card
	clock time.Time
}

const (
	alice = "0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb"
	bob   = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, chain: &fakeChain{chainID: 84532, head: 100, receipts: map[string]*txReceipt{}}, clock: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	master, _ := parseMasterKey(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	a := newApp()
	a.db = db
	a.keys = &keystore{master: master}
	a.prices = fixedPrices{ethUSD: 3000}
	a.now = func() time.Time { return h.clock }
	a.clients["base-sepolia"] = h.chain
	a.syncApprovals = true
	a.postApproval = func(_, title, body string) (int64, error) {
		h.cards = append(h.cards, card{title, body})
		return int64(1000 + len(h.cards)), nil
	}
	h.app = a
	return h
}

func (h *harness) wallet() *Wallet {
	h.t.Helper()
	w, err := h.app.createWallet("proj", "Ops", "base-sepolia", 7)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *harness) allow(w *Wallet, addr string) {
	h.t.Helper()
	if _, err := h.app.addRecipient(w.ID, addr, "test", 7); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) propose(w *Wallet, token, to, amount string) *Proposal {
	h.t.Helper()
	p, err := h.app.proposeTransfer(context.Background(), transferRequest{WalletID: w.ID, Token: token, To: to, Amount: amount, AgentID: 42})
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func agentCtx(agentID int64, toolCall string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: agentID, ToolCallID: toolCall})
}

// ─── Flow tests ──────────────────────────────────────────────────────

func TestSmallTransferIsSignedAndSent(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	p := h.propose(w, "USDC", alice, "10")
	if p.State != "sent" || p.TxHash == "" || p.Nonce != 0 {
		t.Fatalf("state=%s hash=%s nonce=%d err=%s", p.State, p.TxHash, p.Nonce, p.Error)
	}
	if len(h.chain.sent) != 1 || !strings.EqualFold(p.TxHash, "0x"+hex.EncodeToString(keccak256(h.chain.sent[0]))) {
		t.Fatal("broadcast transaction does not match the stored hash")
	}
	// The broadcast must be a USDC transfer of 10_000_000 base units to alice.
	raw := hex.EncodeToString(h.chain.sent[0])
	if !strings.Contains(raw, "a9059cbb000000000000000000000000d1220a0cf47c7b9be7a2e6ba89f429762e7b9adb0000000000000000000000000000000000000000000000000000000000989680") {
		t.Fatal("calldata is not transfer(alice, 10 USDC)")
	}
	if !strings.Contains(raw, "036cbd53842c5426634e7929541ec2318f3dcf7e") {
		t.Fatal("not sent to the USDC contract")
	}
}

func TestRules(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)

	if p := h.propose(w, "USDC", bob, "1"); p.State != "refused" || p.RefusalRule != "recipient_not_allowed" {
		t.Fatalf("unknown recipient: %s %s", p.State, p.RefusalRule)
	}
	if p := h.propose(w, "ETH", alice, "0.001"); p.RefusalRule != "token_not_allowed" {
		t.Fatalf("ETH not in default token list: %s", p.RefusalRule)
	}
	if p := h.propose(w, "USDC", alice, "150"); p.RefusalRule != "per_transfer_cap" {
		t.Fatalf("over per-transfer cap: %s", p.RefusalRule)
	}
	if p := h.propose(w, "USDC", alice, "30"); p.State != "pending_approval" || len(h.cards) != 1 {
		t.Fatalf("above threshold: %s, cards=%d", p.State, len(h.cards))
	}
	if !strings.Contains(h.cards[0].title, "30 USDC") {
		t.Fatalf("card title %q", h.cards[0].title)
	}
	if _, err := h.app.setFrozen(w.ID, true, "test", "user:7"); err != nil {
		t.Fatal(err)
	}
	if p := h.propose(w, "USDC", alice, "1"); p.RefusalRule != "frozen" {
		t.Fatalf("frozen: %s", p.RefusalRule)
	}
	if len(h.chain.sent) != 0 {
		t.Fatal("nothing should have been broadcast")
	}
}

func TestDailyCapCountsEverythingSentInTheWindow(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	// Raise the approval threshold so transfers sign directly.
	if _, err := h.app.setPolicy(w.ID, Policy{PerTxUSDMicros: 100e6, DailyUSDMicros: 250e6, ApprovalUSDMicros: 100e6, Tokens: []string{"USDC"}, NewRecipientDelayHours: 24}, 7); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if p := h.propose(w, "USDC", alice, "100"); p.State != "sent" {
			t.Fatalf("transfer %d: %s %s", i, p.State, p.RefusalDetail)
		}
	}
	p := h.propose(w, "USDC", alice, "60")
	if p.RefusalRule != "daily_cap" || !strings.Contains(p.RefusalDetail, "$50.00") {
		t.Fatalf("cap: %s %s", p.RefusalRule, p.RefusalDetail)
	}
	if p := h.propose(w, "USDC", alice, "50"); p.State != "sent" {
		t.Fatalf("exactly the remaining room should pass: %s", p.RefusalDetail)
	}
	h.clock = h.clock.Add(24*time.Hour + time.Second)
	if p := h.propose(w, "USDC", alice, "100"); p.State != "sent" {
		t.Fatalf("window should have rolled over: %s", p.RefusalDetail)
	}
}

func TestConcurrentProposalsCannotOverrunTheCap(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	if _, err := h.app.setPolicy(w.ID, Policy{PerTxUSDMicros: 100e6, DailyUSDMicros: 100e6, ApprovalUSDMicros: 100e6, Tokens: []string{"USDC"}}, 7); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]*Proposal, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := h.app.proposeTransfer(context.Background(), transferRequest{WalletID: w.ID, Token: "USDC", To: alice, Amount: "30"})
			if err != nil {
				t.Error(err)
			}
			results[i] = p
		}(i)
	}
	wg.Wait()
	sent := 0
	for _, p := range results {
		if p != nil && p.State == "sent" {
			sent++
		}
	}
	if sent != 3 || len(h.chain.sent) != 3 {
		t.Fatalf("sent %d (broadcast %d), want 3 x $30 under a $100 cap", sent, len(h.chain.sent))
	}
}

func TestRetriedToolCallDoesNotPayTwice(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	args := map[string]any{"wallet_id": float64(w.ID), "token": "USDC", "to": alice, "amount": "5"}
	first, err := h.app.toolTransferPropose(agentCtx(42, "call-1"), nil, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.app.toolTransferPropose(agentCtx(42, "call-1"), nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != 1 {
		t.Fatalf("broadcast %d times", len(h.chain.sent))
	}
	if first.(map[string]any)["proposal"].(*Proposal).ID != second.(map[string]any)["proposal"].(*Proposal).ID {
		t.Fatal("retry returned a different proposal")
	}
}

func TestApprovalCallbackOnlyFromConversations(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	p := h.propose(w, "USDC", alice, "40")
	if p.State != "pending_approval" {
		t.Fatal(p.State)
	}
	p, _ = getProposal(h.app.db, p.ID)
	args := map[string]any{"message_id": float64(p.ApprovalMessageID), "action_id": "approve"}

	for name, c := range map[string]*sdk.Caller{
		"no caller":   nil,
		"agent":       {AgentID: 42},
		"other app":   {AppName: "crm", AppInstallID: 9},
		"spoof agent": {AgentID: 42, AppName: "conversations", AppInstallID: 3},
	} {
		ctx := context.Background()
		if c != nil {
			ctx = sdk.WithCaller(ctx, c)
		}
		if _, err := h.app.toolApprovalCallback(ctx, nil, args); err == nil {
			t.Fatalf("%s was allowed to approve", name)
		}
	}
	if len(h.chain.sent) != 0 {
		t.Fatal("sent before a real approval")
	}

	ctx := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "conversations", AppInstallID: 3})
	out, err := h.app.toolApprovalCallback(ctx, nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["proposal"].(*Proposal); got.State != "sent" {
		t.Fatalf("after approval: %s %s", got.State, got.Error)
	}
	// A second delivery of the same decision is harmless.
	out, _ = h.app.toolApprovalCallback(ctx, nil, args)
	if out.(map[string]any)["ok"] != false || len(h.chain.sent) != 1 {
		t.Fatal("duplicate approval delivery sent again")
	}
}

func TestApprovalRechecksRules(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	p := h.propose(w, "USDC", alice, "40")
	if _, err := h.app.setFrozen(w.ID, true, "incident", "user:7"); err != nil {
		t.Fatal(err)
	}
	got, err := h.app.decideProposal(context.Background(), p.ID, true, "user:7", "panel", "", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "refused" || got.RefusalRule != "frozen" || len(h.chain.sent) != 0 {
		t.Fatalf("approved while frozen: %s %s", got.State, got.RefusalRule)
	}
}

func TestRejectedTransferNeverSends(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	p := h.propose(w, "USDC", alice, "40")
	got, err := h.app.decideProposal(context.Background(), p.ID, false, "user:7", "panel", "no", 7)
	if err != nil || got.State != "rejected" || len(h.chain.sent) != 0 {
		t.Fatalf("%v %s", err, got.State)
	}
	if _, err := h.app.decideProposal(context.Background(), p.ID, true, "user:7", "panel", "", 7); err == nil {
		t.Fatal("a rejected proposal was approved later")
	}
}

func TestAgentProposedRecipientWaits(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	r, created, err := h.app.proposeRecipient(w.ID, strings.ToLower(bob), "Bob", "invoice 12", 42)
	if err != nil || !created || r.Status != "pending" || r.Address != bob {
		t.Fatalf("%v %v %+v", err, created, r)
	}
	if len(h.cards) != 1 {
		t.Fatal("no approval card for the recipient")
	}
	if p := h.propose(w, "USDC", bob, "1"); p.RefusalRule != "recipient_not_allowed" {
		t.Fatal("pending recipient received funds")
	}
	ctx := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "conversations", AppInstallID: 3})
	if _, err := h.app.toolApprovalCallback(ctx, nil, map[string]any{"message_id": float64(1001), "action_id": "approve"}); err != nil {
		t.Fatal(err)
	}
	if p := h.propose(w, "USDC", bob, "1"); p.RefusalRule != "recipient_cooling_off" {
		t.Fatalf("new recipient paid before the delay: %s", p.RefusalRule)
	}
	h.clock = h.clock.Add(24*time.Hour + time.Minute)
	if p := h.propose(w, "USDC", bob, "1"); p.State != "sent" {
		t.Fatalf("after the delay: %s %s", p.State, p.RefusalDetail)
	}
}

func TestNodeRejectionFreesTheCap(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	h.chain.sendErr = &rpcCallError{Method: "eth_sendRawTransaction", Code: -32000, Message: "insufficient funds for gas"}
	p := h.propose(w, "USDC", alice, "20")
	if p.State != "failed" || p.ExecutedAt != "" {
		t.Fatalf("state=%s executed_at=%q", p.State, p.ExecutedAt)
	}
	spent, _ := spentSince(h.app.db, w.ID, h.clock.Add(-time.Hour))
	if spent != 0 {
		t.Fatalf("a rejected broadcast still counts %d", spent)
	}
	// Transport failure: may have been sent, so it keeps counting.
	h.chain.sendErr = errors.New("connection reset")
	p = h.propose(w, "USDC", alice, "20")
	if p.State != "sent" || p.ExecutedAt == "" || !strings.Contains(p.Error, "unknown") {
		t.Fatalf("state=%s err=%s", p.State, p.Error)
	}
}

func TestWatcherConfirmsAndRecordsIncoming(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	h.allow(w, alice)
	p := h.propose(w, "USDC", alice, "10")
	ctx := context.Background()
	if err := h.app.runWatcher(ctx, nil); err != nil { // first pass also sets the scan start
		t.Fatal(err)
	}
	h.chain.receipts[p.TxHash] = &txReceipt{Status: 1, BlockNumber: 101, GasUsed: big.NewInt(50_000), EffGasPrice: big.NewInt(2)}
	h.chain.head = 101
	from, _ := parseAddress(bob)
	h.chain.logs = []transferLog{{TxHash: "0xabc", LogIndex: 3, BlockNumber: 101, From: from, Amount: big.NewInt(7_250_000)}}
	for i := 0; i < 2; i++ { // twice: incoming must be recorded once
		if err := h.app.runWatcher(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := getProposal(h.app.db, p.ID)
	if got.State != "confirmed" {
		t.Fatalf("state %s", got.State)
	}
	txs, _ := listTransactions(h.app.db, w.ID, 10)
	in := 0
	for _, tx := range txs {
		if tx.Direction == "in" {
			in++
		}
		if tx.Direction == "out" && (tx.Status != "confirmed" || tx.FeeWei != "100000") {
			t.Fatalf("out tx %+v", tx)
		}
	}
	if in != 1 {
		t.Fatalf("incoming recorded %d times", in)
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	if _, err := h.app.db.Exec(`UPDATE ledger SET kind = 'x' WHERE wallet_id = ?`, w.ID); err == nil {
		t.Fatal("ledger update allowed")
	}
	if _, err := h.app.db.Exec(`DELETE FROM ledger WHERE wallet_id = ?`, w.ID); err == nil {
		t.Fatal("ledger delete allowed")
	}
}

func TestKeystore(t *testing.T) {
	master, _ := parseMasterKey(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	k := &keystore{master: master}
	addr, sealed, err := k.newKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := k.withKey(addr, sealed, func(*secp256k1.PrivateKey) error { return nil }); err != nil {
		t.Fatal(err)
	}
	other, _ := parseMasterKey(strings.Repeat("ab", 32))
	if err := (&keystore{master: other}).withKey(addr, sealed, func(*secp256k1.PrivateKey) error { return nil }); err == nil || !strings.Contains(err.Error(), "different master key") {
		t.Fatalf("wrong master key: %v", err)
	}
	addr2, _, _ := k.newKey()
	if err := k.withKey(addr2, sealed, func(*secp256k1.PrivateKey) error { return nil }); err == nil {
		t.Fatal("a sealed key opened under another wallet's address")
	}
	if _, _, err := (&keystore{}).newKey(); !errors.Is(err, errNoMasterKey) {
		t.Fatal("created a key without a master key")
	}
	for _, bad := range []string{"short", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := parseMasterKey(bad); err == nil {
			t.Fatalf("accepted master key %q", bad)
		}
	}
}

func TestExportNeedsTypedAddress(t *testing.T) {
	h := newHarness(t)
	w := h.wallet()
	if _, err := h.app.exportKey(w.ID, "yes", 7); err == nil {
		t.Fatal("exported without the address")
	}
	key, err := h.app.exportKey(w.ID, w.Address, 7)
	if err != nil || len(key) != 66 {
		t.Fatalf("%v %q", err, key)
	}
	entries, _ := listLedger(h.app.db, w.ID, 10)
	if entries[0].Kind != "key.exported" {
		t.Fatal("export not logged")
	}
}

func TestPolicyLimits(t *testing.T) {
	chain := chains["base"]
	for _, p := range []Policy{
		{PerTxUSDMicros: 0, DailyUSDMicros: 1, Tokens: []string{"USDC"}},
		{PerTxUSDMicros: 200e6, DailyUSDMicros: 100e6, Tokens: []string{"USDC"}},
		{PerTxUSDMicros: 1e6, DailyUSDMicros: maxDailyUSDMicros + 1, Tokens: []string{"USDC"}},
		{PerTxUSDMicros: 1e6, DailyUSDMicros: 1e6, Tokens: []string{"DOGE"}},
		{PerTxUSDMicros: 1e6, DailyUSDMicros: 1e6, Tokens: []string{"USDC"}, NewRecipientDelayHours: -1},
	} {
		if err := p.validate(chain); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	if err := defaultPolicy().validate(chain); err != nil {
		t.Fatal(err)
	}
}

// ─── HTTP and manifest ───────────────────────────────────────────────

func TestPanelRoutesNeedAPerson(t *testing.T) {
	h := newHarness(t)
	t.Setenv("APTEVA_APP_TOKEN", "tok")
	mux := http.NewServeMux()
	for _, r := range h.app.HTTPRoutes() {
		mux.HandleFunc(r.Method+" "+r.Pattern, r.Handler)
	}
	req := httptest.NewRequest("POST", "/api/wallets", strings.NewReader(`{"name":"x","chain":"base-sepolia"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no operator: %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/api/wallets", strings.NewReader(`{"name":"x","chain":"base-sepolia"}`))
	req.Header.Set("X-Apteva-Operator-ID", "7")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator: %d %s", rec.Code, rec.Body)
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	req = httptest.NewRequest("GET", "/api/overview", nil)
	req.Header.Set("X-Apteva-Operator-ID", "7")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without an app token the operator header must not be trusted: %d", rec.Code)
	}
}

func TestManifestAndTools(t *testing.T) {
	a := newApp()
	m := a.Manifest()
	if m.Name != appName {
		t.Fatal(m.Name)
	}
	reserved := map[string]bool{"/health": true, "/manifest": true, "/mcp": true, "/events": true}
	for _, r := range a.HTTPRoutes() {
		if reserved[r.Pattern] || strings.HasPrefix(r.Pattern, "/ui/") || !strings.HasPrefix(r.Pattern, "/api/") {
			t.Fatalf("route %s collides with the SDK or the manifest prefix", r.Pattern)
		}
	}
	declared := map[string]bool{}
	for _, t := range m.Provides.MCPTools {
		declared[t.Name] = true
	}
	for _, tool := range a.MCPTools() {
		if tool.Exposure == sdk.ToolExposureAppOnly {
			if declared[tool.Name] {
				t.Fatalf("app-only tool %s is listed in the manifest", tool.Name)
			}
			continue
		}
		if !declared[tool.Name] {
			t.Fatalf("tool %s missing from apteva.yaml", tool.Name)
		}
		delete(declared, tool.Name)
		for _, forbidden := range []string{"policy", "freeze", "approve", "export", "create"} {
			if strings.Contains(tool.Name, forbidden) {
				t.Fatalf("agent tool %s looks like a people-only action", tool.Name)
			}
		}
	}
	if len(declared) > 0 {
		t.Fatalf("manifest declares tools the app does not serve: %v", declared)
	}
	_ = fmt.Sprint
}
