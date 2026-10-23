package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"admin/config"
	"admin/internal/handler"
	"admin/internal/service"
	"admin/internal/store"
	"admin/migrations"
	"admin/web"
	"core/apperr"
	"core/authn/password"
	"core/components/ginc"
	"core/components/gormc"
	"core/components/jwtc"
	"core/components/otelc"
	"core/httpx"
	"core/response"
	"core/sctx"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run returns errors instead of exiting so deferred sc.Stop() always executes.
func run() error {
	// defined before the service context parses flags (and their env)
	appsJSON := flag.String("admin-apps", "[]",
		`managed apps, JSON: [{"id":"notes","name":"Notes","admin_url":"http://notes-gateway:9081","public_url":"http://localhost:8081","api_key":"..."}]`)
	bootstrapEmail := flag.String("admin-bootstrap-email", "", "first admin account, created when there is none")
	bootstrapPassword := flag.String("admin-bootstrap-password", "", "password of the first admin account (12+ characters)")
	secureCookie := flag.Bool("admin-secure-cookie", false, "set Secure on the session cookie (serve over HTTPS)")
	grafanaURL := flag.String("grafana-url", "", "Grafana base URL for per-app dashboard links, e.g. http://localhost:3030")

	sc := sctx.NewServiceContext(
		sctx.WithName(config.ServiceName),
		// first: activated before the components it instruments, stopped last to flush
		sctx.WithComponent(otelc.NewOtel(config.ComponentOtel)),
		sctx.WithComponent(gormc.NewGormDB(config.ComponentDB, "", gormc.WithMigrations(migrations.FS))),
		// signs admin session tokens (audience "admin")
		sctx.WithComponent(jwtc.NewSigner(config.ComponentJWT)),
		// registered last so it is stopped first: stop taking requests, then close the rest
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

	dbc := sctx.MustGetAs[gormc.GormComponent](sc, config.ComponentDB)
	if dbc.MigrateOnly() {
		logger.Info("migrations applied (--db-migrate=only), exiting")
		return nil
	}

	appList, err := service.ParseApps(*appsJSON)
	if err != nil {
		return err
	}
	apps, err := service.NewApps(appList, &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)})
	if err != nil {
		return err
	}
	admins := service.NewAdmins(store.New(dbc.GetDB()), password.NewHasher(password.DefaultParams, 0),
		sctx.MustGetAs[jwtc.SignerComponent](sc, config.ComponentJWT).Signer(), config.SessionAudience)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	created, err := admins.Bootstrap(ctx, *bootstrapEmail, *bootstrapPassword)
	cancel()
	if err != nil {
		return err
	}
	if created {
		logger.Infof("first admin account created: %s", *bootstrapEmail)
	}
	logger.Infof("managing %d apps", len(appList))

	server := sctx.MustGetAs[ginc.GinComponent](sc, config.ComponentGin)
	r := server.GetRouter()
	r.Use(
		otelgin.Middleware(config.ServiceName, otelgin.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz"
		})),
		httpx.RequestID(),
		httpx.AccessLog(sc.Logger("http")),
	)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	handler.New(admins, apps, sc.Logger("admin"), handler.Options{
		SecureCookie: *secureCookie, GrafanaURL: *grafanaURL,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}).Register(r, 10, 5)

	ui, built := web.Handler()
	if !built {
		logger.Warn("dashboard not built (platform/admin/web: npm run build): serving the API only")
	}
	r.NoRoute(func(c *gin.Context) {
		if built && c.Request.Method == http.MethodGet && !isAPI(c.Request.URL.Path) {
			ui.ServeHTTP(c.Writer, c.Request)
			return
		}
		response.Fail(c, fmt.Errorf("%w: route %s %s", apperr.ErrNotFound, c.Request.Method, c.Request.URL.Path))
	})

	if err := server.Run(); err != nil {
		return err
	}

	sig, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sig.Done()
	logger.Info("shutdown signal received")
	return nil
}

func isAPI(path string) bool { return path == "/api" || len(path) > 4 && path[:5] == "/api/" }
