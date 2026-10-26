package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"core/authn"
	authnmigrations "core/authn/migrations"
	"core/authz/rebac"
	"core/components/ginc"
	"core/components/gormc"
	"core/components/jwtc"
	"core/components/otelc"
	"core/components/redisc"
	"core/components/rpcc"
	"core/infra"
	"core/outbox"
	"core/sctx"
	"notes-gateway/config"
	"notes-gateway/internal/client"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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
		"key the admin service sends to the admin API (internal admin port); empty disables the admin API")

	sc, err := sctx.NewServiceContext(
		sctx.WithName(config.ServiceName),
		// first: activated before the components it instruments, stopped last to flush
		sctx.WithComponent(otelc.NewOtel(config.ComponentOtel)),
		// the app's own users, tenants, memberships and tokens (core/authn)
		sctx.WithComponent(gormc.NewGormDB(config.ComponentDB, "", gormc.WithMigrations(authnmigrations.FS))),
		sctx.WithComponent(jwtc.NewSigner(config.ComponentJWT)),
		sctx.WithComponent(redisc.NewRedis(config.ComponentRedis, "")),
		sctx.WithComponent(rpcc.NewClient(config.ComponentNoteClient, config.UpstreamNote)),
		// registered last so they are stopped first: stop taking requests, then close the rest
		sctx.WithComponent(ginc.NewGin(config.ComponentAdminGin, ginc.WithFlagPrefix(config.AdminFlagPrefix))),
		sctx.WithComponent(ginc.NewGin(config.ComponentGin)),
	)

	if err != nil {
		return err
	}

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

	auth, err := authn.New(authn.Config{
		App:      config.App,
		DB:       dbc.GetDB(),
		Signer:   sctx.MustGetAs[jwtc.SignerComponent](sc, config.ComponentJWT).Signer(),
		Redis:    sctx.MustGetAs[redisc.RedisComponent](sc, config.ComponentRedis).Client(),
		DevLogin: sc.EnvName() == sctx.DevEnv,
		AdminKey: *adminKey,
		Logger:   sc.Logger("authn"),
	})
	if err != nil {
		return err
	}
	if *adminKey == "" {
		logger.Warn("no admin-api-key: the admin API refuses every request")
	}

	noteClient := sctx.MustGetAs[rpcc.ClientComponent](sc, config.ComponentNoteClient)
	notes := client.NewNote(noteClient)
	c, err := buildContainer(notes)
	if err != nil {
		return err
	}

	server := sctx.MustGetAs[ginc.GinComponent](sc, config.ComponentGin)
	registerRoutes(server.GetRouter(), c, routeOptions{
		auth:              auth,
		authRatePerMinute: defaultAuthRatePerMinute,
		authRateBurst:     defaultAuthRateBurst,
		devLogin:          sc.EnvName() == sctx.DevEnv,
		logger:            sc.Logger("http"),
	})
	admin := sctx.MustGetAs[ginc.GinComponent](sc, config.ComponentAdminGin)
	// the services' ReBAC engines, through this admin API: /admin/v1/authz/<service>/*
	authzAdmin, err := rebac.NewAdminProxy(*adminKey, map[string]string{"note": noteClient.BaseURL()},
		otelhttp.NewTransport(http.DefaultTransport), sc.Logger("authz-admin"))
	if err != nil {
		return err
	}
	// the services' outbox engines, through this admin API: /admin/v1/outbox/<service>/*
	outboxAdmin, err := outbox.NewAdminProxy(*adminKey, map[string]string{"note": noteClient.BaseURL()},
		otelhttp.NewTransport(http.DefaultTransport), sc.Logger("outbox-admin"))
	if err != nil {
		return err
	}
	rdb := sctx.MustGetAs[redisc.RedisComponent](sc, config.ComponentRedis).Client()
	infraProbe := infra.NewProbe(dbc.GetDB(), rdb, *adminKey)
	registerAdminRoutes(admin.GetRouter(), auth, authzAdmin, outboxAdmin, infraProbe, *adminKey, sc.Logger("admin-http"))

	if err := admin.Run(); err != nil {
		return err
	}
	if err := server.Run(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	return nil
}
