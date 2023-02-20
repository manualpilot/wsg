package internal

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/manualpilot/auth"
	"github.com/redis/go-redis/v9"
	"golang.org/x/exp/slog"
)

func Main(
	logger *slog.Logger,
	ctx context.Context,
	instanceID string,
	rdb *redis.Client,
	bPrivateKey []byte,
	downstream string,
) (chi.Router, error) {
	privateKey := ed25519.PrivateKey(bPrivateKey)

	b, err := Get(fmt.Sprintf("%v/.well-known/public.txt", downstream))
	if err != nil {
		return nil, err
	}

	logger.Debug("downstream public key", slog.String("public-key", string(b)))

	downstreamKey := make([]byte, base64.RawURLEncoding.DecodedLen(len(b)))
	if _, err := base64.RawURLEncoding.Decode(downstreamKey, b); err != nil {
		return nil, err
	}

	signer := auth.NewRequestSigner(privateKey, "Websocket-Gateway-Auth")
	verifier := auth.NewRequestVerifier(downstreamKey, "Websocket-Gateway-Auth")

	state := &State{
		Lock:        sync.RWMutex{},
		Connections: make(map[string]chan Message),
	}

	go SubscribeEvents(ctx, logger, state, rdb, instanceID)

	router := chi.NewRouter()
	router.Use(mid(instanceID))
	router.Get("/health", health())
	router.Get("/.well-known/public.txt", publicKeyRoute(privateKey))
	router.Get("/", JoinRoute(state, logger, rdb, signer, instanceID, downstream))
	router.Post("/", WriteHandler(state, rdb, verifier))
	router.Delete("/", DropHandler(state, rdb, verifier))

	return router, nil
}

func Get(url string) ([]byte, error) {
	client := http.Client{Timeout: 30 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}

	//goland:noinspection GoUnhandledErrorResult
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func health() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func mid(instanceID string) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Server", "manualpilot")
			w.Header().Set("Instance-ID", instanceID)
			handler.ServeHTTP(w, r)
		})
	}
}

func publicKeyRoute(privateKey ed25519.PrivateKey) http.HandlerFunc {
	pubKey := privateKey.Public().(ed25519.PublicKey)
	publicKey := make([]byte, base64.RawURLEncoding.EncodedLen(len(pubKey)))
	base64.RawURLEncoding.Encode(publicKey, pubKey)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(publicKey)
	}
}
