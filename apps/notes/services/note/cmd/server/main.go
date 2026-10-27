package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"core/authz/rebac"
	"core/components/authzc"
	"core/components/ginc"
	"core/components/gormc"
	"core/components/internalauthc"
	"core/components/otelc"
	"core/di"
	"core/outbox"
	"core/sctx"
	modelauthz "note/authz"
	"note/config"
	grpctransport "note/internal/transport/rpc"
	"note/migrations"

	"github.com/gin-gonic/gin"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run returns errors instead of exiting so deferred sc.Stop() always executes.
func run() error {
	// defined before the service context parses flags (and their env, ADMIN_API_KEY)
	adminKey := flag.String("admin-api-key", "",
		"key of the engine's admin API (/admin/v1/authz, reached through the gateway's admin API); empty disables it")

	sc, err := sctx.NewServiceContext(
		sctx.WithName(config.ServiceName),
		// first: activated before the components it instruments, stopped last to flush
		sctx.WithComponent(otelc.NewOtel(config.ComponentOtel)),
		sctx.WithComponent(gormc.NewGormDB(config.ComponentDB, "", gormc.WithMigrations(migrations.FS))),
		sctx.WithComponent(authzc.NewSettings(config.ComponentAuthz)),
		// every RPC must carry the gateway's internal token (INTERNAL_AUTH=enforce)
		sctx.WithComponent(internalauthc.New(config.ComponentInternalAuth)),
		// registered after db so it is stopped first: drain HTTP, then close db
		sctx.WithComponent(ginc.NewGin(config.ComponentGin)),
	)

	if err != nil {
		return err
	}

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

	// embedded ReBAC engine: its tables in schema authz of this service's
	// database (migrated with the service's migrations), model from authz/*.yml
	engineDSN := ""
	if dbc.MigrateMode() != gormc.MigrateOff {
		engineDSN = dbc.DSN()
	}
	engine, err := rebac.New(context.Background(), db, rebac.Config{Modules: modelauthz.FS, DSN: engineDSN, Logger: sc.Logger("rebac")})
	if err != nil {
		return err
	}
	model, err := rebac.LoadModel(modelauthz.FS)
	if err != nil {
		return err
	}
	authzSettings := authzSetup{
		enforce: sctx.MustGetAs[authzc.SettingsComponent](sc, config.ComponentAuthz).Enforce(),
		model:   model,
		engine:  engine,
	}
	logger.Infof("authorization: enforce=%s (relationships recorded by the embedded engine)", authzSettings.enforce)

	internalAuth := sctx.MustGetAs[internalauthc.Component](sc, config.ComponentInternalAuth).Auth()
	c, err := buildContainer(db, sc.Logger("app"), authzSettings, internalAuth)
	if err != nil {
		return err
	}

	server := sctx.MustGetAs[ginc.GinComponent](sc, config.ComponentGin)
	router := server.GetRouter()

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// gRPC (+ gRPC-Web, Connect/JSON) on the same port, HTTP/2 via h2c.
	// Public REST lives in the gateway; this service is internal only.
	di.MustGet[*grpctransport.NoteHandler](c).Mount(router)
	// the engine's admin API (model, tuples, checks), for the admin dashboard
	rebac.NewAdminHandler(engine, *adminKey).RegisterRoutes(router)
	// transactional outbox admin API (messages, stats, retry), for the admin dashboard
	outbox.NewAdminHandler(db, *adminKey).RegisterRoutes(router)

	if err := server.Run(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	return nil
}
