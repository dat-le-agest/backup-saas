package ginc

import (
	"context"
	"core/logger"
	"core/sctx"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultPort            = 3000
	defaultReadTimeout     = 10
	defaultWriteTimeout    = 10
	defaultIdleTimeout     = 60
	defaultShutdownTimeout = 10
)

// GinComponent is what other packages retrieve via sctx.MustGetAs[ginc.GinComponent].
type GinComponent interface {
	GetRouter() *gin.Engine
	GetPort() int
	Run() error
}

// Mount registers an http.Handler under a path prefix, e.g. a generated Connect handler:
//
//	path, h := notev1connect.NewNoteServiceHandler(impl, rpc.HandlerOptions(l))
//	ginc.Mount(router, path, h)
func Mount(r gin.IRouter, path string, h http.Handler) {
	r.Any(strings.TrimSuffix(path, "/")+"/*rpc", gin.WrapH(h))
}

type Config struct {
	port            int
	ginMode         string
	trustedProxies  string
	readTimeout     int
	writeTimeout    int
	idleTimeout     int
	shutdownTimeout int
}

type ginEngine struct {
	*Config
	id         string
	logger     logger.Logger
	router     *gin.Engine
	httpServer *http.Server
}

func NewGin(id string) *ginEngine {
	return &ginEngine{
		Config: new(Config),
		id:     id,
	}
}

func (g *ginEngine) ID() string {
	return g.id
}

func (g *ginEngine) InitFlags() {
	flag.IntVar(&g.port, "gin-port", defaultPort, "gin server port - Default 3000")

	flag.StringVar(
		&g.ginMode,
		"gin-mode",
		"",
		"gin mode (debug | release) - Default derived from app-env (dev → debug, otherwise release)",
	)

	flag.StringVar(
		&g.trustedProxies,
		"gin-trusted-proxies",
		"",
		"comma-separated trusted proxy IPs/CIDRs for ClientIP() - Default none",
	)

	flag.IntVar(
		&g.readTimeout,
		"gin-read-timeout",
		defaultReadTimeout,
		"max duration in seconds for reading the entire request - Default 10",
	)

	flag.IntVar(
		&g.writeTimeout,
		"gin-write-timeout",
		defaultWriteTimeout,
		"max duration in seconds before timing out writes of the response, 0 = no timeout (SSE/streaming) - Default 10",
	)

	flag.IntVar(
		&g.idleTimeout,
		"gin-idle-timeout",
		defaultIdleTimeout,
		"max duration in seconds to wait for the next request on keep-alive connections - Default 60",
	)

	flag.IntVar(
		&g.shutdownTimeout,
		"gin-shutdown-timeout",
		defaultShutdownTimeout,
		"max duration in seconds to wait for in-flight requests on shutdown - Default 10",
	)
}

func (g *ginEngine) Activate(serviceContext sctx.ServiceContext) error {
	g.logger = serviceContext.Logger(g.id)

	// gin-mode > app-env
	mode := gin.ReleaseMode
	if serviceContext.EnvName() == sctx.DevEnv {
		mode = gin.DebugMode
	}

	if g.ginMode != "" {
		switch g.ginMode {
		case gin.DebugMode, gin.ReleaseMode:
			mode = g.ginMode
		default:
			return fmt.Errorf("invalid gin mode: %s (allowed: debug | release)", g.ginMode)
		}
	}

	gin.SetMode(mode)

	g.logger.Infof("init engine (mode=%s)...", mode)
	g.router = gin.New()

	if err := g.router.SetTrustedProxies(parseProxies(g.trustedProxies)); err != nil {
		return fmt.Errorf("invalid gin-trusted-proxies: %w", err)
	}

	g.router.Use(g.recovery())

	return nil
}

// Run binds the port synchronously so errors like "address already in use"
// are returned to the caller, then serves in the background.
func (g *ginEngine) Run() error {
	if g.router == nil {
		return errors.New("gin engine not activated, call ServiceContext.Load() first")
	}

	addr := fmt.Sprintf(":%d", g.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// HTTP/1.1 for REST + h2c (HTTP/2 without TLS) so gRPC clients can call
	// Connect handlers mounted on the same port.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	g.httpServer = &http.Server{
		Protocols:      protocols,
		Handler:        g.router,
		ReadTimeout:    time.Duration(g.readTimeout) * time.Second,
		WriteTimeout:   time.Duration(g.writeTimeout) * time.Second,
		IdleTimeout:    time.Duration(g.idleTimeout) * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		g.logger.Infof("Server is running on port %d...", g.port)
		if err := g.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			g.logger.Errorf("serve: %v", err)
		}
	}()

	return nil
}

func (g *ginEngine) Stop() error {
	if g.httpServer == nil {
		return nil
	}

	g.logger.Info("Shutting down Gin HTTP Server...")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(g.shutdownTimeout)*time.Second,
	)
	defer cancel()

	if err := g.httpServer.Shutdown(ctx); err != nil {
		g.logger.Errorf("Gin Server forced to shutdown: %v", err)
		return err
	}

	g.logger.Info("Gin Server exited properly")
	return nil
}

func (g *ginEngine) GetPort() int {
	return g.port
}

func (g *ginEngine) GetRouter() *gin.Engine {
	return g.router
}

// recovery logs panics through the app logger and responds 500 instead of dropping the connection.
func (g *ginEngine) recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		g.logger.WithFields(logger.Fields{
			"method": c.Request.Method,
			"path":   c.Request.URL.Path,
			"stack":  string(debug.Stack()),
		}).Errorf("panic recovered: %v", err)

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

func parseProxies(raw string) []string {
	var proxies []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			proxies = append(proxies, p)
		}
	}
	return proxies
}
