package main

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Key from the web3.js documentation; its address is widely published.
const docKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"

func TestAddressDerivation(t *testing.T) {
	one := make([]byte, 32)
	one[31] = 1
	if got := checksumAddress(addressFromPrivate(secp256k1.PrivKeyFromBytes(one))); got != "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf" {
		t.Fatalf("key 1 address = %s", got)
	}
	if got := checksumAddress(addressFromPrivate(secp256k1.PrivKeyFromBytes(mustHex(t, docKey)))); got != "0x2c7536E3605D9C16a7a3D7b1898e529396a65c23" {
		t.Fatalf("doc key address = %s", got)
	}
}

func TestEIP55(t *testing.T) {
	// Vectors from EIP-55.
	for _, want := range []string{
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
		"0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
		"0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
	} {
		got, err := normalizeAddress(strings.ToLower(want))
		if err != nil || got != want {
			t.Fatalf("normalize(%s) = %s, %v", want, got, err)
		}
		if _, err := parseAddress(want); err != nil {
			t.Fatalf("valid checksum rejected: %v", err)
		}
	}
	// One flipped case letter must be caught.
	if _, err := parseAddress("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD"); err == nil {
		t.Fatal("bad checksum accepted")
	}
	for _, bad := range []string{"", "5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "0x1234", "0xzzzzb6053f3e94c9b9a09f33669435e7ef1beaed", "0x0000000000000000000000000000000000000000"} {
		if _, err := parseAddress(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestRLP(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{[]byte("dog"), "83646f67"},
		{rlpList{[]byte("cat"), []byte("dog")}, "c88363617483646f67"},
		{[]byte{}, "80"},
		{rlpList{}, "c0"},
		{uint64(0), "80"},
		{uint64(15), "0f"},
		{uint64(1024), "820400"},
		{[]byte("Lorem ipsum dolor sit amet, consectetur adipisicing elit"),
			"b8384c6f72656d20697073756d20646f6c6f722073697420616d65742c20636f6e7365637465747572206164697069736963696e6720656c6974"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(rlpEncode(c.in)); got != c.want {
			t.Fatalf("rlp(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestSelectors(t *testing.T) {
	if hex.EncodeToString(selectorTransfer) != "a9059cbb" || hex.EncodeToString(selectorBalanceOf) != "70a08231" {
		t.Fatal("wrong ERC-20 selectors")
	}
	if topicTransfer != "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
		t.Fatal("wrong Transfer topic")
	}
}

// Expected bytes produced by go-ethereum v1.14.12 types.SignTx with the
// London signer for the same transactions and key.
func TestSignMatchesGoEthereum(t *testing.T) {
	priv := secp256k1.PrivKeyFromBytes(mustHex(t, docKey))
	recipient, _ := parseAddress("0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb")
	usdc, _ := parseAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e")

	erc20 := &dynamicFeeTx{
		ChainID: big.NewInt(84532), Nonce: 7, MaxPriorityFeePerGas: big.NewInt(1_000_000), MaxFeePerGas: big.NewInt(2_500_000),
		Gas: 65000, To: usdc, Value: big.NewInt(0), Data: erc20TransferData(recipient, big.NewInt(5_000_000)),
	}
	raw, hash, err := erc20.sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if want := "02f8b083014a3407830f4240832625a082fde894036cbd53842c5426634e7929541ec2318f3dcf7e80b844a9059cbb000000000000000000000000d1220a0cf47c7b9be7a2e6ba89f429762e7b9adb00000000000000000000000000000000000000000000000000000000004c4b40c080a0ee8f155122aba02203da88c7dc61c42df19dde6c7f6cb317de094e7156835bdca01130803e59a5a5e30d7fc67e9b5ebc72e4cb2a69e4c908cd645975ae5a5c88eb"; hex.EncodeToString(raw) != want {
		t.Fatalf("erc20 raw mismatch\n got %x\nwant %s", raw, want)
	}
	if hash != "0x815b0772cbc434673c5c46d0dd7746a0ddbb6c201b9b433a1f39b1406dce1559" {
		t.Fatalf("erc20 hash = %s", hash)
	}

	native := &dynamicFeeTx{
		ChainID: big.NewInt(8453), Nonce: 0, MaxPriorityFeePerGas: big.NewInt(0), MaxFeePerGas: big.NewInt(1),
		Gas: 21000, To: recipient, Value: big.NewInt(1_000_000_000_000_000),
	}
	raw, hash, err = native.sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if want := "02f86b82210580800182520894d1220a0cf47c7b9be7a2e6ba89f429762e7b9adb87038d7ea4c6800080c080a0966e55fb5c0322bf26768354f53adadef7f78cbe67771a3a7ccee444d11bb2f1a007fd6902d8eb41faff4e1580a01b9c4ba2ddb480c7440dd4e7cfddf669c1f9b8"; hex.EncodeToString(raw) != want {
		t.Fatalf("native raw mismatch\n got %x\nwant %s", raw, want)
	}
	if hash != "0xcb1e68049087d06be5820bbd93514c2995cf830a38e2782575e8eee2c03d06a8" {
		t.Fatalf("native hash = %s", hash)
	}
}

func TestUnits(t *testing.T) {
	ok := []struct {
		in   string
		dec  int
		want string
	}{
		{"12.5", 6, "12500000"}, {"0.000001", 6, "1"}, {"1", 18, "1000000000000000000"}, {".5", 6, "500000"}, {"007", 6, "7000000"},
	}
	for _, c := range ok {
		n, err := parseUnits(c.in, c.dec)
		if err != nil || n.String() != c.want {
			t.Fatalf("parseUnits(%q) = %v, %v", c.in, n, err)
		}
	}
	for _, bad := range []string{"", "0", "0.0", "-1", "+1", "1.0000001", "1e6", "1,5", "abc", "."} {
		if _, err := parseUnits(bad, 6); err == nil {
			t.Fatalf("parseUnits(%q) accepted", bad)
		}
	}
	if got := formatUnits(big.NewInt(12_500_000), 6); got != "12.5" {
		t.Fatal(got)
	}
	if got := formatUnits(big.NewInt(1), 18); got != "0.000000000000000001" {
		t.Fatal(got)
	}
	if got := formatUnits(big.NewInt(3_000_000), 6); got != "3" {
		t.Fatal(got)
	}
}

func TestUSDMicrosRoundsUp(t *testing.T) {
	// 1 wei at $3000/ETH is far below a micro-dollar; it must count as 1.
	v, err := usdMicros(big.NewInt(1), 18, big.NewRat(3000, 1))
	if err != nil || v != 1 {
		t.Fatalf("got %d, %v", v, err)
	}
	// 0.01 ETH at $3,123.45 = $31.2345.
	v, _ = usdMicros(big.NewInt(10_000_000_000_000_000), 18, big.NewRat(312345, 100))
	if v != 31_234_500 {
		t.Fatalf("got %d", v)
	}
	v, _ = usdMicros(big.NewInt(25_000_000), 6, big.NewRat(1, 1))
	if v != 25_000_000 {
		t.Fatalf("got %d", v)
	}
}
