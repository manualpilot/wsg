package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
	"github.com/sethvargo/go-envconfig"
	"golang.org/x/exp/slog"

	"manualpilot/wsg/impl"
	"manualpilot/wsg/internal"
	"time"
)

type Env struct {
	Port             int                   `env:"PORT,default=8080"`
	RedirectPort     int                   `env:"REDIRECT_PORT,default=9090"`
	InstanceID       string                `env:"INSTANCE_ID,required"`
	ServiceDomain    string                `env:"SERVICE_DOMAIN,required"`
	DownstreamURL    string                `env:"DOWNSTREAM_URL,required"`
	RedisURL         string                `env:"REDIS_URL,required"`
	PorkbunAPIKey    string                `env:"PORKBUN_API_KEY,required"`
	PorkbunAPISecret string                `env:"PORKBUN_API_SECRET,required"`
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
		// TODO: we actually only want to discard TLS handshake errors as they are caused by automated scanners
		ErrorLog: log.New(io.Discard, "", 0),
	}

	redirect := &http.Server{
		Addr: fmt.Sprintf(":%v", env.RedirectPort),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := fmt.Sprintf("https://%v%v", env.ServiceDomain, r.RequestURI)
			http.Redirect(w, r, u, http.StatusTemporaryRedirect)
		}),
	}

	defer func() {
		ctx, rCancel := context.WithTimeout(context.Background(), 1 * time.Minute)
		defer rCancel()
		_ = redirect.Shutdown(ctx)

		ctx, sCancel := context.WithTimeout(context.Background(), 1 * time.Minute)
		defer sCancel()
		_ = server.Shutdown(ctx)
	}()

	logger.Debug("starting...", slog.String("address", server.Addr))

	ec := make(chan error)

	go func() {
		if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			ec <- err
		}
	}()

	go func() {
		if err := redirect.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
