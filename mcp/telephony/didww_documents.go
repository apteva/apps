package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// DIDWW's published wire format is RSA-OAEP-SHA256(key+IV) for each of
// two public keys followed by AES-256-CBC/PKCS7 ciphertext. SHA1 is used
// only for the provider-mandated public-key fingerprints, not encryption.
// https://doc.didww.com/api3/2022-05-10/regulation-resources/encrypted-files/encryption-details.html
func encryptDIDWWDocument(data []byte, publicKeys []string) ([]byte, string, error) {
	if len(publicKeys) != 2 {
		return nil, "", errors.New("DIDWW must supply exactly two encryption keys")
	}
	credentials := make([]byte, 48)
	if _, err := rand.Read(credentials); err != nil {
		return nil, "", err
	}
	var encrypted []byte
	fingerprints := make([]string, 0, 2)
	for _, key := range publicKeys {
		block, _ := pem.Decode([]byte(key))
		if block == nil {
			return nil, "", errors.New("invalid DIDWW public key")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, "", errors.New("invalid DIDWW public key")
		}
		rsaKey, ok := parsed.(*rsa.PublicKey)
		if !ok || rsaKey.N.BitLen() < 2048 {
			return nil, "", errors.New("DIDWW encryption requires RSA keys of at least 2048 bits")
		}
		wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, credentials, nil)
		if err != nil {
			return nil, "", err
		}
		encrypted = append(encrypted, wrapped...)
		fingerprint := sha1.Sum(block.Bytes)
		fingerprints = append(fingerprints, hex.EncodeToString(fingerprint[:]))
	}
	block, err := aes.NewCipher(credentials[:32])
	if err != nil {
		return nil, "", err
	}
	padding := aes.BlockSize - len(data)%aes.BlockSize
	padded := make([]byte, len(data)+padding)
	copy(padded, data)
	copy(padded[len(data):], bytes.Repeat([]byte{byte(padding)}, padding))
	cipher.NewCBCEncrypter(block, credentials[32:]).CryptBlocks(padded, padded)
	return append(encrypted, padded...), strings.Join(fingerprints, ":::"), nil
}

func (a *App) didwwProofCreate(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	provider, err := a.didwwResourceProvider(ctx)
	if err != nil {
		return nil, err
	}
	identityID, addressID := strings.TrimSpace(strArg(args, "identity_id", "")), strings.TrimSpace(strArg(args, "address_id", ""))
	if (identityID == "") == (addressID == "") {
		return nil, errors.New("supply exactly one identity_id or address_id for a DIDWW proof")
	}
	proofType := strings.TrimSpace(strArg(args, "proof_type_id", ""))
	if proofType == "" {
		return nil, errors.New("proof_type_id from the provider requirements is required")
	}
	fileIDs := stringListArg(args, "file_ids")
	file := strings.TrimSpace(strArg(args, "file", ""))
	if file != "" && len(fileIDs) > 0 {
		return nil, errors.New("supply file or existing encrypted file_ids, not both")
	}
	if file != "" {
		// Bound memory before decoding. The panel uses a 5 MiB limit within its
		// existing 8 MiB JSON request limit. No plaintext reaches the integration.
		if len(file) > 7<<20 {
			return nil, errors.New("DIDWW document exceeds the Telephony 5 MB upload limit")
		}
		if strings.HasPrefix(file, "data:") {
			marker := strings.Index(file, ";base64,")
			if marker < 0 {
				return nil, errors.New("file must be base64 or a base64 data URL")
			}
			file = file[marker+8:]
		}
		data, err := base64.StdEncoding.DecodeString(file)
		if err != nil || len(data) == 0 {
			return nil, errors.New("file must contain a base64 encoded document")
		}
		if len(data) > 5<<20 {
			return nil, errors.New("DIDWW document exceeds the Telephony 5 MB upload limit")
		}
		name := filepath.Base(strArg(args, "file_name", "document.pdf"))
		ext := strings.ToLower(filepath.Ext(name))
		valid := (ext == ".pdf" && bytes.HasPrefix(data, []byte("%PDF-"))) || ((ext == ".jpg" || ext == ".jpeg") && bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff})) || (ext == ".png" && bytes.HasPrefix(data, []byte{137, 80, 78, 71, 13, 10, 26, 10}))
		if !valid {
			return nil, errors.New("DIDWW documents must be PDF, JPEG, or PNG with a matching filename")
		}
		raw, err := executeCarrierTool(ctx, provider.ConnID, "get_public_keys", map[string]any{})
		if err != nil {
			return nil, err
		}
		var keys struct {
			Data []struct {
				Attributes struct {
					Key string `json:"key"`
				} `json:"attributes"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &keys) != nil {
			return nil, errors.New("invalid DIDWW public keys response")
		}
		publicKeys := make([]string, 0, len(keys.Data))
		for _, key := range keys.Data {
			publicKeys = append(publicKeys, key.Attributes.Key)
		}
		encrypted, fingerprint, err := encryptDIDWWDocument(data, publicKeys)
		if err != nil {
			return nil, err
		}
		raw, err = executeCarrierTool(ctx, provider.ConnID, "create_encrypted_file", map[string]any{
			"encrypted_files[encryption_fingerprint]": fingerprint,
			"encrypted_files[items][][description]":   strArg(args, "friendly_name", "Regulatory proof"),
			"file":                                    base64.StdEncoding.EncodeToString(encrypted), "file_name": name,
		})
		if err != nil {
			return nil, err
		}
		var uploaded struct {
			IDs []string `json:"ids"`
		}
		if json.Unmarshal(raw, &uploaded) != nil || len(uploaded.IDs) != 1 || uploaded.IDs[0] == "" {
			return nil, errors.New("DIDWW upload returned no unique file ID; check provider files before retrying")
		}
		fileIDs = uploaded.IDs
	}
	if len(fileIDs) == 0 {
		return nil, errors.New("file or encrypted file_ids are required")
	}
	tool, input := "create_identity_proof", map[string]any{"identity_id": identityID, "proof_type_id": proofType, "file_ids": fileIDs}
	if addressID != "" {
		tool = "create_address_proof"
		delete(input, "identity_id")
		input["address_id"] = addressID
	}
	raw, err := executeCarrierTool(ctx, provider.ConnID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("DIDWW proof attachment failed (encrypted file IDs %s may be reused): %w", strings.Join(fileIDs, ","), err)
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("invalid DIDWW proof response")
	}
	return map[string]any{"provider": "didww", "proof": result["data"], "file_ids": fileIDs}, nil
}
