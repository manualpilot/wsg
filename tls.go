package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strconv"
	"sync"
	"time"

	"github.com/bsm/redislock"
	"github.com/caddyserver/certmagic"
	"github.com/libdns/porkbun"
	"github.com/redis/go-redis/v9"
	"github.com/sethvargo/go-envconfig"
)

type storage struct {
	rdb    *redis.Client
	locker *redislock.Client
	locks  sync.Map
}

func (s *storage) Lock(ctx context.Context, name string) error {
	opts := &redislock.Options{
		RetryStrategy: redislock.LinearBackoff(1 * time.Minute),
	}

	lock, err := s.locker.Obtain(ctx, fmt.Sprintf("tls:lock:%v", name), 1*time.Minute, opts)
	if err != nil {
		return err
	}

	s.locks.Store(name, lock)
	return nil
}

func (s *storage) Unlock(ctx context.Context, name string) error {
	lock, ok := s.locks.LoadAndDelete(name)
	if !ok {
		return fmt.Errorf("no lock for %v", name)
	}

	return lock.(*redislock.Lock).Release(ctx)
}

func (s *storage) Store(ctx context.Context, key string, value []byte) error {
	hashmap := map[string]any{
		"modified": time.Now().Unix(),
		"data":     base64.RawURLEncoding.EncodeToString(value),
		"size":     len(value),
	}

	return s.rdb.HSet(ctx, fmt.Sprintf("tls:%v", key), hashmap).Err()
}

func (s *storage) Load(ctx context.Context, key string) ([]byte, error) {
	res, err := s.rdb.HGet(ctx, fmt.Sprintf("tls:%v", key), "data").Result()
	if err != nil && err != redis.Nil {
		return nil, err
	} else if err == redis.Nil {
		return nil, fs.ErrNotExist
	}

	return base64.RawURLEncoding.DecodeString(res)
}

func (s *storage) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, fmt.Sprintf("tls:%v", key)).Err()
}

func (s *storage) Exists(ctx context.Context, key string) bool {
	res, err := s.rdb.Exists(ctx, fmt.Sprintf("tls:%v", key)).Result()
	return err != nil && res > 0
}

func (s *storage) List(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	pattern := fmt.Sprintf("tls:%v", prefix)
	if recursive {
		pattern = fmt.Sprintf("%v*", pattern)
	}

	return s.rdb.Keys(ctx, pattern).Result()
}

func (s *storage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	info := certmagic.KeyInfo{}

	res, err := s.rdb.HMGet(ctx, fmt.Sprintf("tls:%v", key), "modified", "size").Result()
	if err != nil && err != redis.Nil {
		return info, err
	} else if err == redis.Nil {
		return info, fs.ErrNotExist
	}

	modified, err := strconv.Atoi(res[0].(string))
	if err != nil {
		return info, err
	}

	size, err := strconv.Atoi(res[1].(string))
	if err != nil {
		return info, err
	}

	info.Key = key
	info.Modified = time.Unix(int64(modified), 0)
	info.Size = int64(size)
	info.IsTerminal = true

	return info, nil
}

type EnvTLS struct {
	PorkbunAPIKey    string `env:"PORKBUN_API_KEY,required"`
	PorkbunAPISecret string `env:"PORKBUN_API_SECRET,required"`
}

func TLSConfig(ctx context.Context, domain string, rdb *redis.Client) (*tls.Config, error) {
	env := EnvTLS{}
	if err := envconfig.Process(ctx, &env); err != nil {
		return nil, err
	}

	certmagic.DefaultACME.DNS01Solver = &certmagic.DNS01Solver{
		DNSProvider: &porkbun.Provider{
			APIKey:       env.PorkbunAPIKey,
			APISecretKey: env.PorkbunAPISecret,
		},
	}

	certmagic.Default.Storage = &storage{
		rdb:    rdb,
		locker: redislock.New(rdb),
		locks:  sync.Map{},
	}

	return certmagic.TLS([]string{domain})
}
