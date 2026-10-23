package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"authz/config"
	"authz/internal/service"
	grpctransport "authz/internal/transport/rpc"
	"authz/migrations"
	"core/components/ginc"
	"core/components/gormc"
	"core/components/otelc"
	"core/di"
	"core/migrate"
	"core/sctx"

	"github.com/gin-gonic/gin"
)

var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run returns errors instead of exiting so deferred sc.Stop() always executes.
func run() error {
	// defined before the service context parses flags (and their env)
	apps := flag.String("platform-apps", "",
		"comma-separated ids of the apps served by the platform; each gets its own store (schema). Required")
	platformModel := flag.String("authz-platform-model", "",
		"directory of the platform module (*.fga), platform/authz/model in the repository. Required")
	appsDir := flag.String("authz-apps-dir", "",
		"directory holding <app>/authz/*.fga (the apps/ folder of the repository). Required")

	sc := sctx.NewServiceContext(
		sctx.WithName(config.ServiceName),
		// first: activated before the components it instruments, stopped last to flush
		sctx.WithComponent(otelc.NewOtel(config.ComponentOtel)),
		// no shared tables: every app's store is migrated into its schema below
		sctx.WithComponent(gormc.NewGormDB(config.ComponentDB, "")),
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

	appIDs, err := parseApps(*apps)
	if err != nil {
		return err
	}
	if *platformModel == "" || *appsDir == "" {
		return errors.New("authz-platform-model and authz-apps-dir are required (AUTHZ_PLATFORM_MODEL, AUTHZ_APPS_DIR)")
	}

	dbc := sctx.MustGetAs[gormc.GormComponent](sc, config.ComponentDB)
	if dbc.MigrateMode() != gormc.MigrateOff {
		for _, app := range appIDs {
			if err := migrate.UpInSchema(context.Background(), dbc.DSN(), migrations.AppFS, app, sc.Logger(config.ComponentDB)); err != nil {
				return err
			}
		}
	}
	if dbc.MigrateOnly() {
		logger.Info("migrations applied (--db-migrate=only), exiting")
		return nil
	}
	db := dbc.GetDB()

	c, err := buildContainer(db, sc.Logger("app"), service.Apps(appIDs))
	if err != nil {
		return err
	}

	// every app's model: platform module + the app's module, activated if changed
	svc := di.MustGet[*service.AuthzService](c)
	for _, app := range appIDs {
		files, err := service.ReadModules(*platformModel, *appsDir, app)
		if err != nil {
			return err
		}
		id, changed, err := svc.LoadModules(context.Background(), app, files)
		if err != nil {
			return fmt.Errorf("load model of %s: %w", app, err)
		}
		if changed {
			logger.Infof("app %s: new authorization model %s activated (%d files)", app, id, len(files))
		} else {
			logger.Infof("app %s: authorization model %s unchanged", app, id)
		}
	}

	server := sctx.MustGetAs[ginc.GinComponent](sc, config.ComponentGin)
	router := server.GetRouter()

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// gRPC (+ gRPC-Web, Connect/JSON) on the same port, HTTP/2 via h2c.
	// Public REST lives in the gateway; this service is internal only.
	di.MustGet[*grpctransport.AuthzHandler](c).Mount(router)

	if err := server.Run(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	return nil
}

func parseApps(list string) ([]string, error) {
	ids := strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' })
	if len(ids) == 0 {
		return nil, errors.New("no apps configured (--platform-apps / PLATFORM_APPS, e.g. notes)")
	}
	for _, id := range ids {
		if !appIDPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid app id %q (lowercase letters, digits and hyphens)", id)
		}
	}
	return ids, nil
}
