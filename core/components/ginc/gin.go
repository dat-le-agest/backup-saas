package ginc

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"core/logger"
	"core/response"
	"core/sctx"

	"github.com/gin-gonic/gin"
)

const (
	defaultPort            = 3000
	defaultReadTimeout     = 10
	defaultWriteTimeout    = 10
	defaultIdleTimeout     = 60
	defaultShutdownTimeout = 10
)

type GinComponent interface {
	GetRouter() *gin.Engine
	GetPort() int
	Run() error
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
	prefix     string
	logger     logger.Logger
	router     *gin.Engine
	httpServer *http.Server
}

type Option func(*ginEngine)

func WithFlagPrefix(prefix string) Option {
	return func(g *ginEngine) {
		g.prefix = prefix
	}
}

func NewGin(id string, opts ...Option) *ginEngine {
	g := &ginEngine{
		Config: new(Config),
		id:     id,
	}

	for _, opt := range opts {
		opt(g)
	}

	return g
}

func Mount(r gin.IRouter, path string, h http.Handler) {
	r.Any(strings.TrimSuffix(path, "/")+"/*rpc", gin.WrapH(h))
}

func (g *ginEngine) flagName(name string) string {
	if g.prefix == "" {
		return name
	}

	return g.prefix + "-" + name
}

func (g *ginEngine) ID() string {
	return g.id
}

func (g *ginEngine) InitFlags() {
	flag.IntVar(
		&g.port,
		g.flagName("gin-port"),
		defaultPort,
		"gin server port, default 3000",
	)

	flag.StringVar(
		&g.ginMode,
		g.flagName("gin-mode"),
		"",
		"gin mode: debug or release, defaults to debug in dev and release otherwise",
	)

	flag.StringVar(
		&g.trustedProxies,
		g.flagName("gin-trusted-proxies"),
		"",
		"comma-separated trusted proxy IPs or CIDRs for ClientIP, defaults to none",
	)

	flag.IntVar(
		&g.readTimeout,
		g.flagName("gin-read-timeout"),
		defaultReadTimeout,
		"maximum duration in seconds for reading the entire request, default 10",
	)

	flag.IntVar(
		&g.writeTimeout,
		g.flagName("gin-write-timeout"),
		defaultWriteTimeout,
		"maximum duration in seconds before response writes time out, 0 disables the timeout for streaming, default 10",
	)

	flag.IntVar(
		&g.idleTimeout,
		g.flagName("gin-idle-timeout"),
		defaultIdleTimeout,
		"maximum duration in seconds to wait for the next request on a keep-alive connection, default 60",
	)

	flag.IntVar(
		&g.shutdownTimeout,
		g.flagName("gin-shutdown-timeout"),
		defaultShutdownTimeout,
		"maximum duration in seconds to wait for in-flight requests during shutdown, default 10",
	)
}

func (g *ginEngine) Activate(serviceContext sctx.ServiceContext) error {
	g.logger = serviceContext.Logger(g.id)

	mode := gin.ReleaseMode
	if serviceContext.EnvName() == sctx.DevEnv {
		mode = gin.DebugMode
	}

	if g.ginMode != "" {
		switch g.ginMode {
		case gin.DebugMode, gin.ReleaseMode:
			mode = g.ginMode
		default:
			return fmt.Errorf(
				"invalid gin mode %q: allowed values are debug and release",
				g.ginMode,
			)
		}
	}

	gin.SetMode(mode)

	g.logger.Infof("initializing engine with mode %s", mode)
	g.router = gin.New()

	if err := g.router.SetTrustedProxies(parseProxies(g.trustedProxies)); err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}

	g.router.Use(g.recovery())

	return nil
}

func (g *ginEngine) Run() error {
	if g.router == nil {
		return errors.New("gin engine is not activated; call service context load first")
	}

	addr := fmt.Sprintf(":%d", g.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

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
		g.logger.Infof("server is running on port %d", g.port)

		if err := g.httpServer.Serve(ln); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			g.logger.Errorf("serve http server: %v", err)
		}
	}()

	return nil
}

func (g *ginEngine) Stop() error {
	if g.httpServer == nil {
		return nil
	}

	g.logger.Info("shutting down gin http server")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(g.shutdownTimeout)*time.Second,
	)
	defer cancel()

	if err := g.httpServer.Shutdown(ctx); err != nil {
		g.logger.Errorf("graceful shutdown failed: %v", err)
		return err
	}

	g.logger.Info("gin http server stopped")

	return nil
}

func (g *ginEngine) GetPort() int {
	return g.port
}

func (g *ginEngine) GetRouter() *gin.Engine {
	return g.router
}

func (g *ginEngine) recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		logger.WithContext(c.Request.Context(), g.logger).
			WithFields(logger.Fields{
				"method": c.Request.Method,
				"path":   c.Request.URL.Path,
				"stack":  string(debug.Stack()),
			}).
			Errorf("panic recovered: %v", err)

		response.Fail(c, fmt.Errorf("panic: %v", err))
	})
}

func parseProxies(raw string) []string {
	var proxies []string

	for _, proxy := range strings.Split(raw, ",") {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}

		proxies = append(proxies, proxy)
	}

	return proxies
}
