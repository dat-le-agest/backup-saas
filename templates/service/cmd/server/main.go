package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"core/components/ginc"
	"core/components/gormc"
	"core/components/otelc"
	"core/di"
	"core/sctx"
	"template/config"
	grpctransport "template/internal/transport/rpc"
	"template/migrations"

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
		// registered after db so it is stopped first: drain HTTP, then close db
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

	c, err := buildContainer(db, sc.Logger("app"))
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

	if err := server.Run(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	return nil
}
