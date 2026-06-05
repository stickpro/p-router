package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/stickpro/p-router/internal/config"
	"github.com/stickpro/p-router/internal/repository"
	"github.com/stickpro/p-router/internal/router"
	"github.com/stickpro/p-router/internal/server"
	"github.com/stickpro/p-router/internal/service/checker"
	"github.com/stickpro/p-router/internal/vless"
	"github.com/stickpro/p-router/pkg/logger"
)

func Run(ctx context.Context, conf *config.Config, l logger.Logger) {
	l.Info("starting app")

	repo, err := repository.NewSQLiteRepository("proxies.db")
	if err != nil {
		log.Fatalf("Failed to create repository: %v", err)
	}
	defer repo.Close()

	r := router.NewProxyRouter(repo)

	pool := vless.NewPool()
	if pool.XrayFound() {
		l.Info("xray found: VLESS grpc/xhttp/reality will be routed via xray subprocess")
	} else {
		l.Info("xray not found: only VLESS tcp/ws transports are supported natively")
	}
	defer pool.Close()

	srv := server.NewServer(":"+conf.HTTP.Port, r, pool)

	l.Infof("Proxy router started on :%s", conf.HTTP.Port)
	l.Infow("Available proxies:")

	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.Error("error occurred while running http server", err)
		}
	}()

	chkr := checker.New(conf, l, repo)

	go chkr.StartPeriodicCheck(ctx, conf.Checker.Interval)

	<-ctx.Done()

	l.Info("Shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Stop(shutdownCtx); err != nil {
		l.Error("Server forced to shutdown", err)
	}

	l.Info("Server stopped")
}
