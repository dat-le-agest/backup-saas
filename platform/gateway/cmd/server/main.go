package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"core/components/ginc"
	"core/components/otelc"
	"core/components/redisc"
	"core/components/rpcc"
	"core/sctx"
	"gateway/config"
	"sdk/authn"
	"sdk/clients"
	"sdk/components/jwtc"
)

// membership versions cached in Redis must outlive the access tokens they protect
const versionCacheTTL = time.Hour

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
		sctx.WithComponent(jwtc.NewVerifier(config.ComponentJWT)),
		sctx.WithComponent(redisc.NewRedis(config.ComponentRedis, "")),
		sctx.WithComponent(rpcc.NewClient(config.ComponentIamClient, config.UpstreamIam)),
		// registered last so it is stopped first: stop taking requests, then close clients
		sctx.WithComponent(ginc.NewGin(config.ComponentGin)),
	)

	if err := sc.Load(); err != nil {
		return err
	}

	logger := sc.Logger("main")
	defer func() {
		if err := sc.Stop(); err != nil {
			logger.Errorf("stop service context: %v", err)
		}
	}()

	iam := clients.NewIam(sctx.MustGetAs[rpcc.ClientComponent](sc, config.ComponentIamClient))

	auth := authn.NewJWTAuthenticator(
		sctx.MustGetAs[jwtc.VerifierComponent](sc, config.ComponentJWT).Verifier(),
		authn.NewVersionLookup(
			sctx.MustGetAs[redisc.RedisComponent](sc, config.ComponentRedis).Client(),
			iam, versionCacheTTL, sc.Logger("authn"),
		),
	)

	c, err := buildContainer(iam)
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

	if err := server.Run(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	return nil
}
