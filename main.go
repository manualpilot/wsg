package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
	"github.com/sethvargo/go-envconfig"
	"golang.org/x/exp/slog"
	"manualpilot/wsg/impl"
	"manualpilot/wsg/internal"
)

type Env struct {
	Port             int                   `env:"PORT,default=8080"`
	InstanceID       string                `env:"INSTANCE_ID,required"`
	ServiceDomain    string                `env:"SERVICE_DOMAIN,required"`
	DownstreamURL    string                `env:"DOWNSTREAM_URL,required"`
	RedisURL         string                `env:"REDIS_URL,required"`
	PorkbunAPIKey    string                `env:"PORKBUN_API_KEY,default=pk1_33c6fabb89beae993a7c1297fda3693db37224f66d3826b1e55c79807301665d"`
	PorkbunAPISecret string                `env:"PORKBUN_API_SECRET,default=sk1_ef4aa665b02eb9647891d154dd1191892f97fbd8a0eddcdc9399ea0f45dd4f13"`
	PrivateKey       envconfig.Base64Bytes `env:"PRIVATE_KEY,required"`
}

func doMain(logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := Env{}
	if err := envconfig.Process(ctx, &env); err != nil {
		return err
	}

	logger = logger.With(slog.String("instance", env.InstanceID))

	rOpts, err := redis.ParseURL(env.RedisURL)
	if err != nil {
		return err
	}

	rdb := redis.NewClient(rOpts)
	if err := rdb.Info(ctx).Err(); err != nil {
		return err
	}

	router, err := internal.Main(logger, ctx, env.InstanceID, rdb, env.PrivateKey, env.DownstreamURL)
	if err != nil {
		return err
	}

	tlsConfig, err := impl.TLSConfig(env.ServiceDomain, env.PorkbunAPIKey, env.PorkbunAPISecret, rdb)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:      fmt.Sprintf(":%v", env.Port),
		Handler:   router,
		TLSConfig: tlsConfig,
	}

	//goland:noinspection GoUnhandledErrorResult
	defer server.Close()

	ec := make(chan error)
	go func() {
		logger.Debug("starting...", slog.String("address", server.Addr))

		var err error
		if server.TLSConfig != nil {
			err = server.ListenAndServeTLS("", "")
			// TODO: http->https redirector server
		} else {
			err = server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			ec <- err
		}
	}()

	sc := make(chan os.Signal)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sc:
		logger.Warn("shutdown signal", slog.String("signal", sig.String()))
	case err := <-ec:
		logger.Error("failed to start http server", err)
	}

	return nil
}

func main() {
	handler := slog.HandlerOptions{AddSource: true, Level: slog.LevelDebug}
	logger := slog.New(handler.NewTextHandler(os.Stdout))

	if err := doMain(logger); err != nil {
		logger.Error("failed to start", err)
		os.Exit(1)
	}
}
