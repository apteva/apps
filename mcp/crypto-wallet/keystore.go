package main

// Envelope encryption for built-in wallet keys.
//
//   private key --AES-256-GCM(DEK, aad=address)--> key_cipher
//   DEK         --AES-256-GCM(master, aad=dek:address)--> dek_cipher
//
// The master key comes from install config (stored by the platform, not in
// this app's database) or the CRYPTO_WALLET_MASTER_KEY environment variable.
// A database backup on its own therefore cannot spend anything. The master
// key fingerprint is stored beside each key so a wrong or rotated master key
// is reported as such instead of as a decryption error.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

var errNoMasterKey = errors.New("master key not configured: generate one in the Crypto Wallet panel and paste it into the app's master_key setting")

type keystore struct {
	master []byte // 32 bytes; nil when not configured
}

// parseMasterKey accepts 32 bytes as base64 (std or URL, padded or not) or
// as 64 hex characters.
func parseMasterKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(raw); err == nil {
			if len(b) != 32 {
				return nil, errors.New("master key must decode to exactly 32 bytes")
			}
			return b, nil
		}
	}
	return nil, errors.New("master key must be 32 bytes as base64 or 64 hex characters")
}

func generateMasterKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func (k *keystore) configured() bool { return len(k.master) == 32 }

func (k *keystore) fingerprint() string {
	if !k.configured() {
		return ""
	}
	sum := sha256.Sum256(append([]byte("crypto-wallet:master-fp:"), k.master...))
	return hex.EncodeToString(sum[:8])
}

type sealedKey struct {
	KeyNonce, KeyCipher, DEKNonce, DEKCipher []byte
	MasterFP                                 string
}

func gcmSeal(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, aad), nil
}

func gcmOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// newKey generates a private key and seals it. The address it returns is the
// value bound into the ciphertexts as additional data, so a sealed key copied
// onto another wallet row fails to open.
func (k *keystore) newKey() (address string, sealed *sealedKey, err error) {
	if !k.configured() {
		return "", nil, errNoMasterKey
	}
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return "", nil, err
	}
	defer priv.Zero()
	address = checksumAddress(addressFromPrivate(priv))
	keyBytes := priv.Serialize()
	defer wipe(keyBytes)
	sealed, err = k.seal(address, keyBytes)
	return address, sealed, err
}

func (k *keystore) seal(address string, keyBytes []byte) (*sealedKey, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	defer wipe(dek)
	addrAAD := []byte(strings.ToLower(address))
	kn, kc, err := gcmSeal(dek, keyBytes, addrAAD)
	if err != nil {
		return nil, err
	}
	dn, dc, err := gcmSeal(k.master, dek, append([]byte("dek:"), addrAAD...))
	if err != nil {
		return nil, err
	}
	return &sealedKey{KeyNonce: kn, KeyCipher: kc, DEKNonce: dn, DEKCipher: dc, MasterFP: k.fingerprint()}, nil
}

// withKey opens a sealed key, hands it to fn, and zeroes every plaintext
// copy afterwards. The key never leaves fn.
func (k *keystore) withKey(address string, s *sealedKey, fn func(*secp256k1.PrivateKey) error) error {
	if !k.configured() {
		return errNoMasterKey
	}
	if s.MasterFP != k.fingerprint() {
		return errors.New("this wallet was sealed with a different master key; restore the original master_key setting")
	}
	addrAAD := []byte(strings.ToLower(address))
	dek, err := gcmOpen(k.master, s.DEKNonce, s.DEKCipher, append([]byte("dek:"), addrAAD...))
	if err != nil {
		return errors.New("wallet key could not be decrypted")
	}
	defer wipe(dek)
	keyBytes, err := gcmOpen(dek, s.KeyNonce, s.KeyCipher, addrAAD)
	if err != nil {
		return errors.New("wallet key could not be decrypted")
	}
	defer wipe(keyBytes)
	priv := secp256k1.PrivKeyFromBytes(keyBytes)
	defer priv.Zero()
	if checksumAddress(addressFromPrivate(priv)) != address {
		return errors.New("wallet key does not match its address")
	}
	return fn(priv)
}
