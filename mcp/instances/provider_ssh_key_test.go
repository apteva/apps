package main

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/ssh"
	"testing"
)

func TestProviderSSHKeyCompatibility(t *testing.T) {
	for _, provider := range []string{"clouding", "vultr", "upcloud"} {
		t.Run(provider, func(t *testing.T) {
			private, public, e := generateProviderSSHKeypair(provider)
			if e != nil {
				t.Fatal(e)
			}
			signer, e := ssh.ParsePrivateKey([]byte(private))
			if e != nil {
				t.Fatal(e)
			}
			pub, _, _, _, e := ssh.ParseAuthorizedKey([]byte(public))
			if e != nil {
				t.Fatal(e)
			}
			if string(pub.Marshal()) != string(signer.PublicKey().Marshal()) {
				t.Fatal("keys mismatch")
			}
			if provider == "clouding" {
				if pub.Type() != ssh.KeyAlgoRSA {
					t.Fatal(pub.Type())
				}
				p := pub.(ssh.CryptoPublicKey).CryptoPublicKey().(*rsa.PublicKey)
				if p.N.BitLen() != 3072 {
					t.Fatal("weak RSA key")
				}
				sig, e := signer.(ssh.AlgorithmSigner).SignWithAlgorithm(rand.Reader, []byte("proof"), ssh.KeyAlgoRSASHA256)
				if e != nil || pub.Verify([]byte("proof"), sig) != nil {
					t.Fatal("RSA SHA2 authentication unsupported", e)
				}
			} else if pub.Type() != ssh.KeyAlgoED25519 {
				t.Fatal("changed other provider key type")
			}
		})
	}
}

func TestCloudingKeyRejectionIsNotUnknownServerCreation(t *testing.T) {
	ctx, p := cloudingUpcloudTestCtx(t, "clouding", func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "create_ssh_key" {
			return nil, errors.New("key registration rejected before compute")
		}
		return cloudingFixtures(id, tool, args)
	})
	if _, e := apiProviderProvision(ctx, CreateInstanceInput{Name: "reject-key", Provider: "clouding", ProviderConnectionID: 7, Region: "barcelona", Size: "2x4", Image: "ubuntu"}); e == nil {
		t.Fatal("key rejection accepted")
	}
	rows, e := dbListInstances(ctx.AppDB(), "clouding", "")
	if e != nil || len(rows) != 1 {
		t.Fatal(e, len(rows))
	}
	if rows[0].CreatePending || rows[0].ProviderID != "" || rows[0].Status != "error" {
		t.Fatal("pre-request failure retained unknown compute intent")
	}
	for _, tool := range p.calls {
		if tool == "create_server" {
			t.Fatal("compute was requested after key rejection")
		}
	}
}
