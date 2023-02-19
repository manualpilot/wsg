package impl

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/bsm/redislock"
	"github.com/caddyserver/certmagic"
	"github.com/redis/go-redis/v9"
)

func TestTLS(t *testing.T) {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	rlock := redislock.New(rdb)

	certmagic.Default.Storage = &storage{
		rdb:    rdb,
		locker: rlock,
		locks:  sync.Map{},
	}

	ctx := context.Background()
	domain := "example.com"
	key := []byte("key value")

	if err := certmagic.Default.Storage.Lock(ctx, domain); err != nil {
		t.Fatalf("failed to lock %v", err)
	}

	if err := certmagic.Default.Storage.Unlock(ctx, domain); err != nil {
		t.Fatalf("failed to unlock %v", err)
	}

	if err := certmagic.Default.Storage.Store(ctx, domain, key); err != nil {
		t.Fatalf("failed to store %v", err)
	}

	b, err := certmagic.Default.Storage.Load(ctx, domain)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(b, key) {
		t.Fatalf("keys not equal")
	}
}
