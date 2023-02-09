package internal

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/segmentio/ksuid"
)

type (
	RequestSigner   = func(r *http.Request, id string) error
	RequestVerifier = func(r *http.Request) string
)

func NewRequestSigner(privateKey ed25519.PrivateKey) RequestSigner {
	return func(r *http.Request, id string) error {
		nonce, err := ksuid.NewRandom()
		if err != nil {
			return err
		}

		msg := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%v_%v", nonce.String(), id)))
		sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(msg)))

		r.Header.Set("WebSocket-Gateway-Auth", fmt.Sprintf("%v.%v", msg, sig))

		return nil
	}
}

func NewRequestVerifier(publicKey ed25519.PublicKey) RequestVerifier {
	return func(r *http.Request) string {
		parts := strings.Split(r.Header.Get("WebSocket-Gateway-Auth"), ".")
		if len(parts) != 2 {
			return ""
		}

		sig, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}

		if !ed25519.Verify(publicKey, []byte(parts[0]), sig) {
			return ""
		}

		msg, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return ""
		}

		parts = strings.Split(string(msg), "_")
		if len(parts) != 2 {
			return ""
		}

		nonce := ksuid.KSUID{}
		if err := nonce.UnmarshalText([]byte(parts[0])); err != nil {
			return ""
		}

		now := time.Now()
		notBefore := now.Add(-1 * time.Minute)
		notAfter := now.Add(1 * time.Minute)

		nt := nonce.Time()
		if nt.Before(notBefore) || nt.After(notAfter) {
			return ""
		}

		return parts[1]
	}
}

func PublicKeyRoute(privateKey ed25519.PrivateKey) http.HandlerFunc {
	pubKey := privateKey.Public().(ed25519.PublicKey)
	publicKey := make([]byte, base64.RawURLEncoding.EncodedLen(len(pubKey)))
	base64.RawURLEncoding.Encode(publicKey, pubKey)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(publicKey)
	}
}
