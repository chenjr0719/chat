package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/ginutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	Port              string `env:"PORT"                        envDefault:"8081"`
	DevMode           bool   `env:"DEV_MODE"                    envDefault:"false"`
	DevFallbackSiteID string `env:"PORTAL_DEV_FALLBACK_SITE_ID" envDefault:"site-local"`

	// OIDC settings — required when DEV_MODE is false.
	OIDCIssuerURL string   `env:"OIDC_ISSUER_URL"`
	OIDCAudiences []string `env:"OIDC_AUDIENCES" envSeparator:","`
	TLSSkipVerify bool     `env:"TLS_SKIP_VERIFY" envDefault:"false"`

	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB"       envDefault:"portal"`
	MongoUsername string `env:"MONGO_USERNAME" envDefault:""`
	MongoPassword string `env:"MONGO_PASSWORD" envDefault:""`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	ctx := context.Background()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}

	store := newMongoDirectoryStore(mongoClient.Database(cfg.MongoDB))
	idxCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := store.EnsureIndexes(idxCtx); err != nil {
		return fmt.Errorf("ensure directory indexes: %w", err)
	}

	var handler *PortalHandler
	if cfg.DevMode {
		slog.Info("dev mode enabled — OIDC validation disabled")
		handler = NewPortalHandler(nil, store, true, cfg.DevFallbackSiteID)
	} else {
		if cfg.OIDCIssuerURL == "" || len(cfg.OIDCAudiences) == 0 {
			return fmt.Errorf("OIDC_ISSUER_URL and OIDC_AUDIENCES are required when DEV_MODE is false")
		}
		oidcValidator, err := pkgoidc.NewValidator(ctx, pkgoidc.Config{
			IssuerURL:     cfg.OIDCIssuerURL,
			Audiences:     cfg.OIDCAudiences,
			TLSSkipVerify: cfg.TLSSkipVerify,
		})
		if err != nil {
			return fmt.Errorf("create oidc validator: %w", err)
		}
		slog.Info("oidc validator initialized", "issuer", cfg.OIDCIssuerURL)
		handler = NewPortalHandler(oidcValidator, store, false, cfg.DevFallbackSiteID)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(ginutil.RequestID())
	r.Use(ginutil.AccessLog())
	r.Use(ginutil.CORS())
	registerRoutes(r, handler)

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	srvErr := make(chan error, 1)
	go func() {
		slog.Info("portal service starting", "addr", addr)
		srvErr <- srv.ListenAndServe()
	}()

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		shutdown.Wait(ctx, 25*time.Second, func(ctx context.Context) error {
			slog.Info("shutting down portal service")
			err := srv.Shutdown(ctx)
			mongoutil.Disconnect(ctx, mongoClient)
			return err
		})
	}()

	err = <-srvErr
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen portal server: %w", err)
	}
	<-shutdownDone

	return nil
}
