package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/orchestrator/bootstrap"
	"github.com/gos0001/gomod-cryptopay/internal/orchestrator/cron"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
	pkgpostgres "github.com/gos0001/gomod-cryptopay/pkg/postgres"
)

type App struct {
	logger     *zap.SugaredLogger
	router     *gin.Engine
	httpServer *http.Server
	bootstrap  *bootstrap.Bootstrap
	cron       *cron.Cron
	pg         *pkgpostgres.Pool
	configFile *config.File
}

func NewApp(
	logger *zap.SugaredLogger,
	router *gin.Engine,
	cfg Config,
	boot *bootstrap.Bootstrap,
	cj *cron.Cron,
	pg *pkgpostgres.Pool,
	configFile *config.File,
) *App {
	// Gin trusts every proxy unless told otherwise, and then reads the client
	// address out of X-Forwarded-For — a header the client writes. Anything that
	// keys on the client address, the public rate limit included, would be
	// bypassed by a single forged value. Empty configuration means trust nobody.
	//
	// The error is only ever returned for an unparseable entry, which LoadConfig
	// has already refused — so by here there is nothing left for it to report.
	_ = router.SetTrustedProxies(cfg.TrustedProxies)

	return &App{
		logger: logger,
		router: router,
		httpServer: &http.Server{
			Addr:    cfg.Addr,
			Handler: router,
		},
		bootstrap:  boot,
		cron:       cj,
		pg:         pg,
		configFile: configFile,
	}
}

// BuildInfo is what the linker stamped into the binary, carried through so the
// running process can say which build it is.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func (a *App) Run(build BuildInfo) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First line of the log, before anything can fail: an operator reading a
	// crash needs to know which build produced it.
	a.logger.Infow("cryptopay starting",
		"version", build.Version, "commit", build.Commit, "built", build.Date)

	// Reported here rather than at load time because a warning needs every
	// package to have claimed its section first, and that only finishes once
	// wire has built the graph — which is to say, by the time Run is called.
	for _, w := range a.configFile.Warnings() {
		a.logger.Warnw("configuration", "problem", w, "file", a.configFile.Path())
	}

	// Bootstrap before the listener opens: a failed startup task must stop the
	// process, not let it serve traffic against a half-prepared system.
	if err := a.bootstrap.Run(ctx); err != nil {
		a.logger.Fatalw("bootstrap failed", "error", err)
	}

	a.cron.Start(ctx)

	a.logger.Infow("starting server", "addr", a.httpServer.Addr)

	go func() {
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Fatalw("server error", "error", err)
		}
	}()

	waitForShutdown(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		// Stop accepting requests before tearing down what they depend on.
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			a.logger.Errorw("shutdown error", "error", err)
		}

		// Cron next: in-flight jobs hold connections from the pools closed below.
		a.cron.Stop()
		a.pg.Close()

		a.logger.Info("server stopped")
	})
}
