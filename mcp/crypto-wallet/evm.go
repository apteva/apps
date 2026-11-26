package main

// Minimal EVM primitives: secp256k1 keys, addresses, RLP, EIP-1559
// transaction signing and ERC-20 calldata. Kept small and dependency-light
// on purpose (decred secp256k1 + x/crypto keccak) so every byte that ends up
// in a signed transaction is readable in this file. evm_test.go pins the
// encoding against known vectors.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

func keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// addressFromPrivate derives the 20-byte address of a private key.
func addressFromPrivate(priv *secp256k1.PrivateKey) [20]byte {
	pub := priv.PubKey().SerializeUncompressed() // 0x04 || X || Y
	var out [20]byte
	copy(out[:], keccak256(pub[1:])[12:])
	return out
}

// checksumAddress renders an address in EIP-55 mixed case.
func checksumAddress(addr [20]byte) string {
	lower := hex.EncodeToString(addr[:])
	hash := hex.EncodeToString(keccak256([]byte(lower)))
	var b strings.Builder
	b.WriteString("0x")
	for i, c := range lower {
		if c >= 'a' && c <= 'f' && hash[i] >= '8' {
			b.WriteRune(c - 32)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// parseAddress accepts a 0x-prefixed 40-hex-digit address. Mixed-case input
// must carry a valid EIP-55 checksum, so a mistyped character is caught
// instead of sending funds to a near-miss address.
func parseAddress(s string) ([20]byte, error) {
	var out [20]byte
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return out, errors.New("address must start with 0x")
	}
	body := s[2:]
	if len(body) != 40 {
		return out, errors.New("address must be 40 hex characters after 0x")
	}
	raw, err := hex.DecodeString(body)
	if err != nil {
		return out, errors.New("address is not valid hex")
	}
	copy(out[:], raw)
	if body != strings.ToLower(body) && body != strings.ToUpper(body) {
		if checksumAddress(out)[2:] != body {
			return out, errors.New("address checksum does not match; check for a typo")
		}
	}
	if out == ([20]byte{}) {
		return out, errors.New("the zero address cannot receive funds")
	}
	return out, nil
}

// normalizeAddress returns the EIP-55 form of a valid address.
func normalizeAddress(s string) (string, error) {
	a, err := parseAddress(s)
	if err != nil {
		return "", err
	}
	return checksumAddress(a), nil
}

// ─── RLP ─────────────────────────────────────────────────────────────

type rlpList []any

func rlpEncode(v any) []byte {
	switch x := v.(type) {
	case []byte:
		if len(x) == 1 && x[0] < 0x80 {
			return []byte{x[0]}
		}
		return append(rlpLength(len(x), 0x80), x...)
	case *big.Int:
		if x == nil || x.Sign() == 0 {
			return rlpEncode([]byte{})
		}
		return rlpEncode(x.Bytes())
	case uint64:
		return rlpEncode(new(big.Int).SetUint64(x))
	case rlpList:
		var body []byte
		for _, item := range x {
			body = append(body, rlpEncode(item)...)
		}
		return append(rlpLength(len(body), 0xc0), body...)
	default:
		panic(fmt.Sprintf("rlp: unsupported type %T", v))
	}
}

func rlpLength(n int, offset byte) []byte {
	if n < 56 {
		return []byte{offset + byte(n)}
	}
	lenBytes := new(big.Int).SetInt64(int64(n)).Bytes()
	return append([]byte{offset + 55 + byte(len(lenBytes))}, lenBytes...)
}

// ─── EIP-1559 transactions ───────────────────────────────────────────

type dynamicFeeTx struct {
	ChainID              *big.Int
	Nonce                uint64
	MaxPriorityFeePerGas *big.Int
	MaxFeePerGas         *big.Int
	Gas                  uint64
	To                   [20]byte
	Value                *big.Int
	Data                 []byte
}

func (tx *dynamicFeeTx) fields() rlpList {
	value := tx.Value
	if value == nil {
		value = new(big.Int)
	}
	data := tx.Data
	if data == nil {
		data = []byte{}
	}
	return rlpList{
		tx.ChainID, tx.Nonce, tx.MaxPriorityFeePerGas, tx.MaxFeePerGas,
		tx.Gas, tx.To[:], value, data, rlpList{},
	}
}

// signingHash is keccak256(0x02 || rlp([chainId, nonce, ..., accessList])).
func (tx *dynamicFeeTx) signingHash() []byte {
	return keccak256([]byte{0x02}, rlpEncode(tx.fields()))
}

// sign returns the raw signed transaction (0x02-typed envelope) and its hash.
func (tx *dynamicFeeTx) sign(priv *secp256k1.PrivateKey) (raw []byte, hash string, err error) {
	if tx.ChainID == nil || tx.ChainID.Sign() <= 0 {
		return nil, "", errors.New("chain id required")
	}
	// SignCompact returns [27 + recovery id] || R || S with a canonical
	// (low-S) signature, as Ethereum requires.
	sig := ecdsa.SignCompact(priv, tx.signingHash(), false)
	yParity := uint64(sig[0] - 27)
	if yParity > 1 {
		return nil, "", errors.New("unexpected recovery id")
	}
	// Self-check before anything leaves the process: the signature must
	// recover to this key's address.
	signer, err := tx.recoverSigner(byte(yParity), sig[1:33], sig[33:65])
	if err != nil || signer != addressFromPrivate(priv) {
		return nil, "", errors.New("signature self-check failed")
	}
	r := new(big.Int).SetBytes(sig[1:33])
	s := new(big.Int).SetBytes(sig[33:65])
	fields := append(tx.fields(), yParity, r, s)
	raw = append([]byte{0x02}, rlpEncode(fields)...)
	return raw, "0x" + hex.EncodeToString(keccak256(raw)), nil
}

// recoverSigner recovers the sender of a signed EIP-1559 transaction built
// by sign. Used in tests and as a pre-broadcast self-check.
func (tx *dynamicFeeTx) recoverSigner(yParity byte, r, s []byte) ([20]byte, error) {
	var out [20]byte
	compact := make([]byte, 65)
	compact[0] = 27 + yParity
	copy(compact[33-len(r):33], r)
	copy(compact[65-len(s):65], s)
	pub, _, err := ecdsa.RecoverCompact(compact, tx.signingHash())
	if err != nil {
		return out, err
	}
	b := pub.SerializeUncompressed()
	copy(out[:], keccak256(b[1:])[12:])
	return out, nil
}

// ─── ERC-20 ──────────────────────────────────────────────────────────

var (
	selectorTransfer  = keccak256([]byte("transfer(address,uint256)"))[:4]
	selectorBalanceOf = keccak256([]byte("balanceOf(address)"))[:4]
	topicTransfer     = "0x" + hex.EncodeToString(keccak256([]byte("Transfer(address,address,uint256)")))
)

func word(b []byte) []byte {
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func erc20TransferData(to [20]byte, amount *big.Int) []byte {
	data := append([]byte{}, selectorTransfer...)
	data = append(data, word(to[:])...)
	return append(data, word(amount.Bytes())...)
}

func erc20BalanceOfData(owner [20]byte) []byte {
	return append(append([]byte{}, selectorBalanceOf...), word(owner[:])...)
}

func addressTopic(addr [20]byte) string {
	return "0x" + hex.EncodeToString(word(addr[:]))
}

// ─── Amounts ─────────────────────────────────────────────────────────

// parseUnits turns a decimal string ("12.5") into an integer amount in the
// token's smallest unit. More fractional digits than the token has is an
// error rather than a silent rounding.
func parseUnits(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("amount required")
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return nil, errors.New("amount must be a positive decimal number")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	for _, c := range whole + frac {
		if c < '0' || c > '9' {
			return nil, errors.New("amount must be a decimal number like 12.5")
		}
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("amount has more than %d decimal places", decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, errors.New("invalid amount")
	}
	if n.Sign() <= 0 {
		return nil, errors.New("amount must be greater than zero")
	}
	return n, nil
}

// formatUnits renders a smallest-unit integer as a decimal string with
// trailing zeros trimmed.
func formatUnits(n *big.Int, decimals int) string {
	if n == nil {
		return "0"
	}
	neg := n.Sign() < 0
	s := new(big.Int).Abs(n).String()
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	whole, frac := s[:len(s)-decimals], strings.TrimRight(s[len(s)-decimals:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func hexBig(n *big.Int) string {
	if n == nil || n.Sign() == 0 {
		return "0x0"
	}
	return "0x" + n.Text(16)
}

func parseHexBig(s string) (*big.Int, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	if s == "" {
		return new(big.Int), nil
	}
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex quantity %q", s)
	}
	return n, nil
}
