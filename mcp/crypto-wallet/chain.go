package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Chains and tokens ───────────────────────────────────────────────

type tokenInfo struct {
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
	Contract string `json:"contract,omitempty"` // empty = the chain's native coin
}

type chainInfo struct {
	Slug          string               `json:"slug"`
	Name          string               `json:"name"`
	ChainID       int64                `json:"chain_id"`
	Testnet       bool                 `json:"testnet"`
	Explorer      string               `json:"explorer"`
	RPCConfig     string               `json:"-"` // install config key holding the RPC URL
	DefaultRPC    string               `json:"-"`
	Tokens        map[string]tokenInfo `json:"tokens"`
	Confirmations int64                `json:"-"`
}

// Circle's native USDC on each chain. Addresses from Circle's published
// list; TestChainTokensMatchOnChain (RUN_CHAIN_TESTS=1) checks them live.
var chains = map[string]*chainInfo{
	"base": {
		Slug: "base", Name: "Base", ChainID: 8453, Explorer: "https://basescan.org",
		RPCConfig: "base_rpc_url", DefaultRPC: "https://mainnet.base.org", Confirmations: 3,
		Tokens: map[string]tokenInfo{
			"ETH":  {Symbol: "ETH", Decimals: 18},
			"USDC": {Symbol: "USDC", Decimals: 6, Contract: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"},
		},
	},
	"base-sepolia": {
		Slug: "base-sepolia", Name: "Base Sepolia (test network)", ChainID: 84532, Testnet: true, Explorer: "https://sepolia.basescan.org",
		RPCConfig: "base_sepolia_rpc_url", DefaultRPC: "https://sepolia.base.org", Confirmations: 1,
		Tokens: map[string]tokenInfo{
			"ETH":  {Symbol: "ETH", Decimals: 18},
			"USDC": {Symbol: "USDC", Decimals: 6, Contract: "0x036CbD53842c5426634e7929541eC2318f3dCF7e"},
		},
	},
}

func chainBySlug(slug string) (*chainInfo, error) {
	c, ok := chains[strings.ToLower(strings.TrimSpace(slug))]
	if !ok {
		return nil, fmt.Errorf("unknown chain %q (base | base-sepolia)", slug)
	}
	return c, nil
}

func (c *chainInfo) token(symbol string) (tokenInfo, error) {
	t, ok := c.Tokens[strings.ToUpper(strings.TrimSpace(symbol))]
	if !ok {
		return tokenInfo{}, fmt.Errorf("token %q is not supported on %s (ETH | USDC)", symbol, c.Slug)
	}
	return t, nil
}

// ─── JSON-RPC ────────────────────────────────────────────────────────

// chainClient is the slice of Ethereum JSON-RPC the app uses. An interface
// so tests can run the whole proposal flow against a fake chain.
type chainClient interface {
	ChainID(ctx context.Context) (*big.Int, error)
	BlockNumber(ctx context.Context) (uint64, error)
	Balance(ctx context.Context, addr [20]byte) (*big.Int, error)
	TokenBalance(ctx context.Context, contract, owner [20]byte) (*big.Int, error)
	PendingNonce(ctx context.Context, addr [20]byte) (uint64, error)
	SuggestFees(ctx context.Context) (tip, maxFee *big.Int, err error)
	EstimateGas(ctx context.Context, from, to [20]byte, value *big.Int, data []byte) (uint64, error)
	SendRawTransaction(ctx context.Context, raw []byte) (string, error)
	Receipt(ctx context.Context, hash string) (*txReceipt, error)
	TransferLogs(ctx context.Context, contract [20]byte, to [20]byte, from, toBlock uint64) ([]transferLog, error)
}

type txReceipt struct {
	Status      uint64
	BlockNumber uint64
	GasUsed     *big.Int
	EffGasPrice *big.Int
}

type transferLog struct {
	TxHash      string
	LogIndex    int64
	BlockNumber uint64
	From        [20]byte
	Amount      *big.Int
}

type rpcClient struct {
	url  string
	http *http.Client
	id   atomic.Int64
}

func newRPCClient(url string) *rpcClient {
	return &rpcClient{url: url, http: &http.Client{Timeout: 20 * time.Second}}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcCallError is a definite answer from the node (for example "insufficient
// funds"), as opposed to a transport failure where the request may or may
// not have reached it. The difference decides whether a failed broadcast
// can be treated as "nothing was sent".
type rpcCallError struct {
	Method  string
	Code    int
	Message string
}

func (e *rpcCallError) Error() string {
	return fmt.Sprintf("%s: %s (code %d)", e.Method, e.Message, e.Code)
}

func (c *rpcClient) call(ctx context.Context, method string, params []any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id.Add(1), "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: rpc http %d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: invalid rpc response", method)
	}
	if env.Error != nil {
		return &rpcCallError{Method: method, Code: env.Error.Code, Message: env.Error.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

func (c *rpcClient) quantity(ctx context.Context, method string, params []any) (*big.Int, error) {
	var s string
	if err := c.call(ctx, method, params, &s); err != nil {
		return nil, err
	}
	return parseHexBig(s)
}

func hexAddr(a [20]byte) string { return "0x" + hex.EncodeToString(a[:]) }

func (c *rpcClient) ChainID(ctx context.Context) (*big.Int, error) {
	return c.quantity(ctx, "eth_chainId", []any{})
}

func (c *rpcClient) BlockNumber(ctx context.Context) (uint64, error) {
	n, err := c.quantity(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}

func (c *rpcClient) Balance(ctx context.Context, addr [20]byte) (*big.Int, error) {
	return c.quantity(ctx, "eth_getBalance", []any{hexAddr(addr), "latest"})
}

func (c *rpcClient) TokenBalance(ctx context.Context, contract, owner [20]byte) (*big.Int, error) {
	return c.quantity(ctx, "eth_call", []any{map[string]any{
		"to": hexAddr(contract), "data": "0x" + hex.EncodeToString(erc20BalanceOfData(owner)),
	}, "latest"})
}

func (c *rpcClient) PendingNonce(ctx context.Context, addr [20]byte) (uint64, error) {
	n, err := c.quantity(ctx, "eth_getTransactionCount", []any{hexAddr(addr), "pending"})
	if err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}

// SuggestFees: tip from eth_maxPriorityFeePerGas, max fee = 2 x base fee +
// tip, which survives several full blocks of base-fee growth.
func (c *rpcClient) SuggestFees(ctx context.Context) (*big.Int, *big.Int, error) {
	tip, err := c.quantity(ctx, "eth_maxPriorityFeePerGas", []any{})
	if err != nil {
		return nil, nil, err
	}
	var block struct {
		BaseFee string `json:"baseFeePerGas"`
	}
	if err := c.call(ctx, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
		return nil, nil, err
	}
	base, err := parseHexBig(block.BaseFee)
	if err != nil {
		return nil, nil, err
	}
	maxFee := new(big.Int).Add(new(big.Int).Mul(base, big.NewInt(2)), tip)
	return tip, maxFee, nil
}

func (c *rpcClient) EstimateGas(ctx context.Context, from, to [20]byte, value *big.Int, data []byte) (uint64, error) {
	call := map[string]any{"from": hexAddr(from), "to": hexAddr(to), "value": hexBig(value)}
	if len(data) > 0 {
		call["data"] = "0x" + hex.EncodeToString(data)
	}
	n, err := c.quantity(ctx, "eth_estimateGas", []any{call})
	if err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}

func (c *rpcClient) SendRawTransaction(ctx context.Context, raw []byte) (string, error) {
	var hash string
	err := c.call(ctx, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(raw)}, &hash)
	return hash, err
}

func (c *rpcClient) Receipt(ctx context.Context, hash string) (*txReceipt, error) {
	var r *struct {
		Status      string `json:"status"`
		BlockNumber string `json:"blockNumber"`
		GasUsed     string `json:"gasUsed"`
		EffGasPrice string `json:"effectiveGasPrice"`
	}
	if err := c.call(ctx, "eth_getTransactionReceipt", []any{hash}, &r); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	status, _ := parseHexBig(r.Status)
	block, _ := parseHexBig(r.BlockNumber)
	gas, _ := parseHexBig(r.GasUsed)
	price, _ := parseHexBig(r.EffGasPrice)
	return &txReceipt{Status: status.Uint64(), BlockNumber: block.Uint64(), GasUsed: gas, EffGasPrice: price}, nil
}

func (c *rpcClient) TransferLogs(ctx context.Context, contract, to [20]byte, from, toBlock uint64) ([]transferLog, error) {
	var logs []struct {
		TxHash   string   `json:"transactionHash"`
		LogIndex string   `json:"logIndex"`
		Block    string   `json:"blockNumber"`
		Topics   []string `json:"topics"`
		Data     string   `json:"data"`
		Removed  bool     `json:"removed"`
	}
	err := c.call(ctx, "eth_getLogs", []any{map[string]any{
		"address":   hexAddr(contract),
		"fromBlock": "0x" + strconv.FormatUint(from, 16),
		"toBlock":   "0x" + strconv.FormatUint(toBlock, 16),
		"topics":    []any{topicTransfer, nil, addressTopic(to)},
	}}, &logs)
	if err != nil {
		return nil, err
	}
	out := make([]transferLog, 0, len(logs))
	for _, l := range logs {
		if l.Removed || len(l.Topics) < 3 {
			continue
		}
		fromTopic, err := hex.DecodeString(strings.TrimPrefix(l.Topics[1], "0x"))
		if err != nil || len(fromTopic) != 32 {
			continue
		}
		var fromAddr [20]byte
		copy(fromAddr[:], fromTopic[12:])
		amount, err := parseHexBig(l.Data)
		if err != nil {
			continue
		}
		idx, _ := parseHexBig(l.LogIndex)
		blk, _ := parseHexBig(l.Block)
		out = append(out, transferLog{TxHash: l.TxHash, LogIndex: idx.Int64(), BlockNumber: blk.Uint64(), From: fromAddr, Amount: amount})
	}
	return out, nil
}

// ─── ETH/USD price ───────────────────────────────────────────────────

// priceSource values tokens in USD micro-dollars. USDC is fixed at $1;
// ETH comes from Coinbase's public spot price, cached for a minute. When
// the price can't be fetched, ETH transfers are refused rather than valued
// at zero: a cap that silently stops counting is worse than no transfer.
type priceSource interface {
	USDMicros(ctx context.Context, symbol string, amount *big.Int, decimals int) (int64, error)
}

type spotPrices struct {
	mu      sync.Mutex
	ethUSD  *big.Rat
	fetched time.Time
	url     string
	http    *http.Client
}

func newSpotPrices() *spotPrices {
	return &spotPrices{url: "https://api.coinbase.com/v2/prices/ETH-USD/spot", http: &http.Client{Timeout: 10 * time.Second}}
}

func (p *spotPrices) ethPrice(ctx context.Context) (*big.Rat, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ethUSD != nil && time.Since(p.fetched) < time.Minute {
		return p.ethUSD, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ETH price unavailable: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Amount string `json:"amount"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body) != nil {
		return nil, errors.New("ETH price unavailable")
	}
	price, ok := new(big.Rat).SetString(body.Data.Amount)
	if !ok || price.Sign() <= 0 {
		return nil, errors.New("ETH price unavailable")
	}
	p.ethUSD, p.fetched = price, time.Now()
	return price, nil
}

func (p *spotPrices) USDMicros(ctx context.Context, symbol string, amount *big.Int, decimals int) (int64, error) {
	var price *big.Rat
	switch strings.ToUpper(symbol) {
	case "USDC":
		price = big.NewRat(1, 1)
	case "ETH":
		var err error
		if price, err = p.ethPrice(ctx); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("no USD price for %s", symbol)
	}
	return usdMicros(amount, decimals, price)
}

// usdMicros = amount / 10^decimals * price * 10^6, rounded up so caps err
// on the side of counting a little more, never less.
func usdMicros(amount *big.Int, decimals int, price *big.Rat) (int64, error) {
	v := new(big.Rat).SetInt(amount)
	v.Mul(v, price)
	v.Mul(v, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil)))
	v.Quo(v, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
	q, r := new(big.Int).QuoRem(v.Num(), v.Denom(), new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, errors.New("amount is too large")
	}
	return q.Int64(), nil
}
