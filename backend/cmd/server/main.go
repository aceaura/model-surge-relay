// Command server 装配 model-surge-relay 的全部组件并提供 HTTP 服务。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/config"
	"github.com/aceaura/model-surge-relay/backend/dispatch"
	"github.com/aceaura/model-surge-relay/backend/httpapi"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/policy/runtime"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/store"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.PGDSN)
	if err != nil {
		return err
	}
	defer st.Close()

	var backend cache.Backend
	if cfg.CacheEnabled() {
		redis := cache.NewRedisBackend(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		defer redis.Close()
		backend = redis
	} else {
		log.Info("cache disabled, reading straight from postgres")
	}
	c := cache.New(backend, cfg.CacheTTL)

	upstream := upstreamclient.New(cfg.UpstreamBaseURL, cfg.UpstreamDeliveryKey)
	engine := policy.NewEngine(
		policy.NewRegistry(runtime.NewLua(), runtime.NewJavaScript(), runtime.NewTypeScript()),
		cfg.PolicyTimeout,
	)

	collections := collection.NewRepo(st.Pool(), c, upstream)
	policies := policy.NewRepo(st.Pool(), c, engine)
	userModels := usermodel.NewRepo(st.Pool(), c)
	runStates := runstate.NewRepo(st.Pool())

	svc := &dispatch.Service{
		UserModels:  userModels,
		Collections: collections,
		Policies:    policies,
		Engine:      engine,
		RunStates:   runStates,
		Resolver:    upstream,
		Thresholds: runstate.Thresholds{
			FailureThreshold: cfg.CooldownThreshold,
			CooldownDuration: cfg.CooldownDuration,
		},
		Logger: httpapi.SlogLogger{Log: log},
	}

	api := &httpapi.Server{
		Dispatch:    svc,
		Health:      health{store: st, cache: c, upstream: upstream},
		Collections: collections,
		Policies:    policies,
		Engine:      engine,
		UserModels:  userModels,
		RunStates:   runStates,
		DispatchKey: cfg.DispatchKey,
		AdminKey:    cfg.AdminKey,
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "cache", cfg.CacheEnabled())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

type health struct {
	store    *store.Store
	cache    *cache.Cache
	upstream *upstreamclient.Client
}

func (h health) PingDatabase(ctx context.Context) error { return h.store.Ping(ctx) }
func (h health) CacheReady(ctx context.Context) bool    { return h.cache.Ready(ctx) }
func (h health) UpstreamReady(ctx context.Context) bool { return h.upstream.Ready(ctx) }
