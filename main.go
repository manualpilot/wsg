package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/sethvargo/go-envconfig"
	"golang.org/x/exp/slog"
	"manualpilot/wsg/internal"
)

type Env struct {
	Port          int                   `env:"PORT,default=8080"`
	InstanceID    string                `env:"INSTANCE_ID,required"`
	RedisURL      string                `env:"REDIS_URL,required"`
	PrivateKey    envconfig.Base64Bytes `env:"PRIVATE_KEY,required"`
	DownstreamURL string                `env:"DOWNSTREAM_URL,required"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := Env{}
	if err := envconfig.Process(ctx, &env); err != nil {
		slog.Error("failed to parse env", err)
		os.Exit(1)
	}

	router, err := internal.Main(ctx, env.InstanceID, env.RedisURL, env.PrivateKey, env.DownstreamURL)
	if err != nil {
		slog.Error("failed to build router", err)
		os.Exit(1)
	}

	// TODO: wait for signal

	if err := http.ListenAndServe(fmt.Sprintf(":%v", env.Port), router); err != nil && err != http.ErrServerClosed {
		slog.Error("failed to start server", err)
		os.Exit(1)
	}
}
