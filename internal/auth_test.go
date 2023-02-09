package internal

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"

	"github.com/segmentio/ksuid"
)

func TestAuth(t *testing.T) {
	uid, err := ksuid.NewRandom()
	if err != nil {
		t.Fatal(err)
	}

	id := uid.String()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewRequestSigner(privateKey)
	verifier := NewRequestVerifier(publicKey)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := signer(req, id); err != nil {
		t.Fatal(err)
	}

	if id != verifier(req) {
		t.Error("nope")
	}
}
