package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"core/components/ginc"
	"core/components/gormc"
	"core/components/jwtc"
	"core/components/otelc"
	"core/components/redisc"
	"core/di"
	"core/sctx"
	"iam/config"
	"iam/internal/password"
	"iam/internal/service"
	grpctransport "iam/internal/transport/rpc"
	"iam/migrations"

	"github.com/gin-gonic/gin"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run returns errors instead of exiting so deferred sc.Stop() always executes.
func run() error {
	sc := sctx.NewServiceContext(
		sctx.WithName(config.ServiceName),
		// first: activated before the components it instruments, stopped last to flush
		sctx.WithComponent(otelc.NewOtel(config.ComponentOtel)),
		sctx.WithComponent(gormc.NewGormDB(config.ComponentDB, "", gormc.WithMigrations(migrations.FS))),
		sctx.WithComponent(redisc.NewRedis(config.ComponentRedis, "")),
		sctx.WithComponent(jwtc.NewSigner(config.ComponentJWT)),
		// registered last so it is stopped first: drain HTTP, then close the rest
		sctx.WithComponent(ginc.NewGin(config.ComponentGin)),
	)

	// Load rolls back already-activated components on failure
	if err := sc.Load(); err != nil {
		return err
	}

	logger := sc.Logger("main")
	defer func() {
		if err := sc.Stop(); err != nil {
			logger.Errorf("stop service context: %v", err)
		}
	}()

	dbc := sctx.MustGetAs[gormc.GormComponent](sc, config.ComponentDB)
	if dbc.MigrateOnly() {
		logger.Info("migrations applied (--db-migrate=only), exiting")
		return nil
	}
	db := dbc.GetDB()

	jwt := sctx.MustGetAs[jwtc.SignerComponent](sc, config.ComponentJWT)

	c, err := buildContainer(deps{
		db:     db,
		redis:  sctx.MustGetAs[redisc.RedisComponent](sc, config.ComponentRedis).Client(),
		signer: jwt.Signer(),
		hasher: password.NewHasher(password.DefaultParams, 0),
		tokens: service.TokenConfig{
			RefreshTTL: service.DefaultRefreshTTL,
			DevLogin:   sc.EnvName() == sctx.DevEnv,
		},
		versionTTL: time.Hour,
		logger:     sc.Logger("app"),
	})
	if err != nil {
		return err
	}

	server := sctx.MustGetAs[ginc.GinComponent](sc, config.ComponentGin)
	router := server.GetRouter()

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// public keys for verifying access tokens (gateway: --jwt-jwks-url)
	jwks := jwt.JWKS()
	router.GET("/.well-known/jwks.json", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=300")
		c.JSON(http.StatusOK, jwks)
	})

	// gRPC (+ gRPC-Web, Connect/JSON) on the same port, HTTP/2 via h2c.
	// Internal only: the gateway exposes the public API.
	di.MustGet[*grpctransport.IamHandler](c).Mount(router)

	if err := server.Run(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	return nil
}
