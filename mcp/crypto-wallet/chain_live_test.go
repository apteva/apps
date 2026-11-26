package main

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Live checks against the public Base endpoints. Off by default; run with
// RUN_CHAIN_TESTS=1. Nothing here moves funds: the broadcast comes from a
// fresh empty key and must be refused for lack of funds, which proves the
// node decoded the transaction and recovered our signer.

func liveOnly(t *testing.T) {
	if os.Getenv("RUN_CHAIN_TESTS") != "1" {
		t.Skip("set RUN_CHAIN_TESTS=1 to run against public Base RPC endpoints")
	}
}

func ethCallString(t *testing.T, c *rpcClient, ctx context.Context, to, selector string) []byte {
	t.Helper()
	var out string
	if err := c.call(ctx, "eth_call", []any{map[string]any{"to": to, "data": selector}, "latest"}, &out); err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimPrefix(out, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestChainTokensMatchOnChain(t *testing.T) {
	liveOnly(t)
	for _, slug := range []string{"base", "base-sepolia"} {
		chain := chains[slug]
		c := newRPCClient(chain.DefaultRPC)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		id, err := c.ChainID(ctx)
		if err != nil || id.Int64() != chain.ChainID {
			cancel()
			t.Fatalf("%s chain id %v %v", slug, id, err)
		}
		usdc := chain.Tokens["USDC"]
		symbol := ethCallString(t, c, ctx, usdc.Contract, "0x95d89b41")   // symbol()
		decimals := ethCallString(t, c, ctx, usdc.Contract, "0x313ce567") // decimals()
		cancel()
		if len(symbol) < 96 || !strings.HasPrefix(string(symbol[64:]), "USDC") {
			t.Fatalf("%s: %s is not USDC (symbol bytes %x)", slug, usdc.Contract, symbol)
		}
		if new(big.Int).SetBytes(decimals).Int64() != int64(usdc.Decimals) {
			t.Fatalf("%s: USDC decimals %x", slug, decimals)
		}
		t.Logf("%s: chain %d, USDC at %s, 6 decimals", slug, chain.ChainID, usdc.Contract)
	}
}

func TestBroadcastFromEmptyKeyIsRefusedForFunds(t *testing.T) {
	liveOnly(t)
	chain := chains["base-sepolia"]
	c := newRPCClient(chain.DefaultRPC)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := addressFromPrivate(priv)
	tip, maxFee, err := c.SuggestFees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	to, _ := parseAddress("0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb")
	tx := &dynamicFeeTx{ChainID: big.NewInt(chain.ChainID), Nonce: 0, MaxPriorityFeePerGas: tip, MaxFeePerGas: maxFee, Gas: 21000, To: to, Value: big.NewInt(1)}
	raw, hash, err := tx.sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SendRawTransaction(ctx, raw)
	var rpcErr *rpcCallError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected a node refusal, got %v", err)
	}
	msg := strings.ToLower(rpcErr.Message)
	if !strings.Contains(msg, "insufficient funds") {
		t.Fatalf("node refused for another reason (bad encoding or signature?): %s", rpcErr.Message)
	}
	t.Logf("signed %s from %s; node: %s", hash, checksumAddress(from), rpcErr.Message)
}
