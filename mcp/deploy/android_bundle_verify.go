package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"go.mozilla.org/pkcs7"
)

const maxJARSignatureMetadataBytes = 16 << 20

type jarSignerFiles struct {
	signature *zip.File
	block     *zip.File
}

// verifyAndroidBundleSignaturePureGo verifies the PKCS#7 signature, the
// signed manifest, every payload digest, and the managed signer certificate.
// It intentionally does not validate a public CA chain: Android upload keys
// are self-signed and trust is established by the identity fingerprint.
func verifyAndroidBundleSignaturePureGo(bundlePath, expectedFingerprint string) (string, error) {
	reader, err := zip.OpenReader(bundlePath)
	if err != nil {
		return "", fmt.Errorf("open Android App Bundle: %w", err)
	}
	defer reader.Close()

	entries := make(map[string]*zip.File, len(reader.File))
	signers := map[string]*jarSignerFiles{}
	var manifestFile *zip.File
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if _, exists := entries[name]; exists {
			return "", fmt.Errorf("Android App Bundle contains duplicate entry %q", name)
		}
		entries[name] = file
		upper := strings.ToUpper(name)
		if upper == "META-INF/MANIFEST.MF" {
			manifestFile = file
			continue
		}
		if !strings.HasPrefix(upper, "META-INF/") || strings.Count(upper, "/") != 1 {
			continue
		}
		base, ext := jarSignatureName(upper)
		if base == "" {
			continue
		}
		pair := signers[base]
		if pair == nil {
			pair = &jarSignerFiles{}
			signers[base] = pair
		}
		if ext == ".SF" {
			pair.signature = file
		} else {
			pair.block = file
		}
	}
	if manifestFile == nil {
		return "", errors.New("Android App Bundle has no META-INF/MANIFEST.MF")
	}
	manifest, err := readZIPFileLimited(manifestFile, maxJARSignatureMetadataBytes)
	if err != nil {
		return "", fmt.Errorf("read Android App Bundle manifest: %w", err)
	}
	expectedFingerprint = normalizeCertificateFingerprint(expectedFingerprint)
	var observed []string
	for _, pair := range signers {
		if pair.signature == nil || pair.block == nil {
			continue
		}
		signature, err := readZIPFileLimited(pair.signature, maxJARSignatureMetadataBytes)
		if err != nil {
			return "", fmt.Errorf("read Android signature file: %w", err)
		}
		if err := verifyJARSignatureManifest(signature, manifest); err != nil {
			return "", err
		}
		block, err := readZIPFileLimited(pair.block, maxJARSignatureMetadataBytes)
		if err != nil {
			return "", fmt.Errorf("read Android signature block: %w", err)
		}
		signedData, err := pkcs7.Parse(block)
		if err != nil {
			return "", fmt.Errorf("parse Android PKCS#7 signature: %w", err)
		}
		signedData.Content = signature
		if err := signedData.Verify(); err != nil {
			return "", fmt.Errorf("verify Android PKCS#7 signature: %w", err)
		}
		certificate := signedData.GetOnlySigner()
		if certificate == nil {
			return "", errors.New("Android signature block must contain exactly one signer")
		}
		if time.Now().Before(certificate.NotBefore) || time.Now().After(certificate.NotAfter) {
			return "", errors.New("Android App Bundle signer certificate is not currently valid")
		}
		_, fingerprint := certificateFingerprints(certificate)
		fingerprint = normalizeCertificateFingerprint(fingerprint)
		observed = append(observed, fingerprint)
		if expectedFingerprint != "" && fingerprint != expectedFingerprint {
			continue
		}
		if err := verifyJARPayloadDigests(entries, manifest); err != nil {
			return "", err
		}
		return fingerprint, nil
	}
	if len(observed) == 0 {
		return "", errors.New("Android App Bundle has no complete JAR signature")
	}
	return "", fmt.Errorf(
		"Android App Bundle is signed by %s, expected managed upload certificate %s",
		strings.Join(observed, ", "), expectedFingerprint,
	)
}

func jarSignatureName(upper string) (string, string) {
	for _, ext := range []string{".SF", ".RSA", ".DSA", ".EC"} {
		if strings.HasSuffix(upper, ext) {
			return strings.TrimSuffix(upper, ext), ext
		}
	}
	return "", ""
}

func readZIPFileLimited(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("signature metadata exceeds size limit")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("signature metadata exceeds size limit")
	}
	return body, nil
}

func verifyJARSignatureManifest(signature, manifest []byte) error {
	sections, err := parseJARManifestSections(signature)
	if err != nil {
		return fmt.Errorf("parse Android signature manifest: %w", err)
	}
	if len(sections) == 0 {
		return errors.New("Android signature file is empty")
	}
	want, err := base64.StdEncoding.DecodeString(sections[0]["sha-256-digest-manifest"])
	if err != nil || len(want) != sha256.Size {
		return errors.New("Android signature file has no valid SHA-256 manifest digest")
	}
	actual := sha256.Sum256(manifest)
	if subtle.ConstantTimeCompare(want, actual[:]) != 1 {
		return errors.New("Android App Bundle signed manifest digest does not match")
	}
	return nil
}

func verifyJARPayloadDigests(entries map[string]*zip.File, manifest []byte) error {
	sections, err := parseJARManifestSections(manifest)
	if err != nil {
		return fmt.Errorf("parse Android App Bundle manifest: %w", err)
	}
	signed := map[string][]byte{}
	for _, section := range sections[1:] {
		name := section["name"]
		digestValue := section["sha-256-digest"]
		if name == "" || digestValue == "" {
			continue
		}
		if _, exists := signed[name]; exists {
			return fmt.Errorf("Android App Bundle manifest repeats entry %q", name)
		}
		digest, err := base64.StdEncoding.DecodeString(digestValue)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("Android App Bundle manifest has invalid digest for %q", name)
		}
		signed[name] = digest
	}
	for name, file := range entries {
		if file.FileInfo().IsDir() || isJARSignatureMetadata(name) {
			continue
		}
		want, exists := signed[name]
		if !exists {
			return fmt.Errorf("Android App Bundle payload entry %q is unsigned", name)
		}
		reader, err := file.Open()
		if err != nil {
			return fmt.Errorf("read Android App Bundle entry %q: %w", name, err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			return fmt.Errorf("hash Android App Bundle entry %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Android App Bundle entry %q: %w", name, closeErr)
		}
		if subtle.ConstantTimeCompare(want, hash.Sum(nil)) != 1 {
			return fmt.Errorf("Android App Bundle payload digest does not match for %q", name)
		}
		delete(signed, name)
	}
	for name := range signed {
		if !isJARSignatureMetadata(name) {
			return fmt.Errorf("Android App Bundle manifest references missing entry %q", name)
		}
	}
	return nil
}

func isJARSignatureMetadata(name string) bool {
	upper := strings.ToUpper(filepath.ToSlash(name))
	if upper == "META-INF/MANIFEST.MF" {
		return true
	}
	if !strings.HasPrefix(upper, "META-INF/") || strings.Count(upper, "/") != 1 {
		return false
	}
	base, _ := jarSignatureName(upper)
	return base != ""
}

func parseJARManifestSections(body []byte) ([]map[string]string, error) {
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	rawSections := bytes.Split(normalized, []byte("\n\n"))
	sections := make([]map[string]string, 0, len(rawSections))
	for _, rawSection := range rawSections {
		if len(bytes.TrimSpace(rawSection)) == 0 {
			continue
		}
		section := map[string]string{}
		currentKey := ""
		for _, line := range bytes.Split(rawSection, []byte("\n")) {
			if len(line) > 0 && line[0] == ' ' {
				if currentKey == "" {
					return nil, errors.New("manifest continuation has no attribute")
				}
				section[currentKey] += string(line[1:])
				continue
			}
			separator := bytes.IndexByte(line, ':')
			if separator <= 0 {
				return nil, fmt.Errorf("invalid manifest attribute %q", line)
			}
			key := strings.ToLower(string(line[:separator]))
			if _, exists := section[key]; exists {
				return nil, fmt.Errorf("duplicate manifest attribute %q", key)
			}
			value := line[separator+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			section[key] = string(value)
			currentKey = key
		}
		sections = append(sections, section)
	}
	return sections, nil
}
