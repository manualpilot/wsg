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
	"github.com/redis/go-redis/v9"
	"golang.org/x/exp/slog"
)

func Main(ctx context.Context, instanceID, redisURL string, bPrivateKey []byte, downstream string) (chi.Router, error) {
	logger := slog.With(slog.String("instance", instanceID))
	privateKey := ed25519.PrivateKey(bPrivateKey)

	b, err := Get(fmt.Sprintf("%v/.well-known/public.txt", downstream))
	if err != nil {
		return nil, err
	}

	downstreamKey := make([]byte, base64.RawURLEncoding.DecodedLen(len(b)))
	if _, err := base64.RawURLEncoding.Decode(downstreamKey, b); err != nil {
		return nil, err
	}

	signer := NewRequestSigner(privateKey)
	verifier := NewRequestVerifier(downstreamKey)

	state := &State{
		Lock:        sync.RWMutex{},
		Connections: make(map[string]*Connection),
	}

	rOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	rdb := redis.NewClient(rOpts)
	if err := rdb.Info(ctx).Err(); err != nil {
		return nil, err
	}

	go SubscribeEvents(ctx, logger, state, rdb, instanceID)

	router := chi.NewRouter()
	router.Use(mid(instanceID))
	router.Get("/.well-known/public.txt", PublicKeyRoute(privateKey))
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

func mid(instanceID string) func(http.Handler) http.Handler {
	return func (handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Instance-ID", instanceID)
			handler.ServeHTTP(w, r)
		})
	}
}
