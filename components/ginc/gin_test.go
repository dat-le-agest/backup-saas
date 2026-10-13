package ginc

import (
	"core/logger"
	"core/sctx"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type nopLogger struct{}

func (nopLogger) Debug(...interface{})                     {}
func (nopLogger) Info(...interface{})                      {}
func (nopLogger) Warn(...interface{})                      {}
func (nopLogger) Error(...interface{})                     {}
func (nopLogger) Debugf(string, ...interface{})            {}
func (nopLogger) Infof(string, ...interface{})             {}
func (nopLogger) Warnf(string, ...interface{})             {}
func (nopLogger) Errorf(string, ...interface{})            {}
func (l nopLogger) With(string, interface{}) logger.Logger { return l }
func (l nopLogger) WithFields(logger.Fields) logger.Logger { return l }

type fakeSC struct {
	sctx.ServiceContext
	env string
}

func (f fakeSC) EnvName() string             { return f.env }
func (f fakeSC) Logger(string) logger.Logger { return nopLogger{} }

func newEngine(cfg Config) *ginEngine {
	g := NewGin("gin")
	*g.Config = cfg
	return g
}

func TestModeDerivedFromEnv(t *testing.T) {
	cases := []struct{ env, flag, want string }{
		{sctx.DevEnv, "", gin.DebugMode},
		{sctx.PrdEnv, "", gin.ReleaseMode},
		{sctx.StgEnv, "", gin.ReleaseMode},
		{sctx.PrdEnv, gin.DebugMode, gin.DebugMode},
	}
	for _, tc := range cases {
		g := newEngine(Config{ginMode: tc.flag})
		if err := g.Activate(fakeSC{env: tc.env}); err != nil {
			t.Fatal(err)
		}
		if gin.Mode() != tc.want {
			t.Errorf("env=%s flag=%q: got %s want %s", tc.env, tc.flag, gin.Mode(), tc.want)
		}
	}
	if err := newEngine(Config{ginMode: "test"}).Activate(fakeSC{env: sctx.DevEnv}); err == nil {
		t.Error("expected invalid mode error")
	}
}

func TestRunReturnsBindError(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	g := newEngine(Config{port: ln.Addr().(*net.TCPAddr).Port})
	_ = g.Activate(fakeSC{env: sctx.PrdEnv})
	if err := g.Run(); err == nil {
		t.Fatal("expected address in use error")
	}
}

func TestRunBeforeActivate(t *testing.T) {
	if err := NewGin("gin").Run(); err == nil {
		t.Fatal("expected not activated error")
	}
}

func TestRecoveryReturns500(t *testing.T) {
	g := newEngine(Config{})
	_ = g.Activate(fakeSC{env: sctx.PrdEnv})
	g.GetRouter().GET("/panic", func(*gin.Context) { panic("boom") })

	w := httptest.NewRecorder()
	g.GetRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d", w.Code)
	}
}

func TestClientIPNotSpoofableByDefault(t *testing.T) {
	g := newEngine(Config{})
	_ = g.Activate(fakeSC{env: sctx.PrdEnv})
	g.GetRouter().GET("/ip", func(c *gin.Context) { c.String(200, c.ClientIP()) })

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	w := httptest.NewRecorder()
	g.GetRouter().ServeHTTP(w, req)
	if w.Body.String() != "10.0.0.5" {
		t.Fatalf("got %q", w.Body.String())
	}

	g = newEngine(Config{trustedProxies: "10.0.0.0/8, 192.168.1.1"})
	_ = g.Activate(fakeSC{env: sctx.PrdEnv})
	g.GetRouter().GET("/ip", func(c *gin.Context) { c.String(200, c.ClientIP()) })
	w = httptest.NewRecorder()
	g.GetRouter().ServeHTTP(w, req)
	if w.Body.String() != "6.6.6.6" {
		t.Fatalf("trusted proxy: got %q", w.Body.String())
	}

	if err := newEngine(Config{trustedProxies: "not-an-ip"}).Activate(fakeSC{env: sctx.PrdEnv}); err == nil {
		t.Fatal("expected invalid proxy error")
	}
}

func TestServeAndGracefulStop(t *testing.T) {
	ln, _ := net.Listen("tcp", ":0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	g := newEngine(Config{port: port, readTimeout: 5, writeTimeout: 5, shutdownTimeout: 5})
	_ = g.Activate(fakeSC{env: sctx.PrdEnv})
	g.GetRouter().GET("/slow", func(c *gin.Context) {
		time.Sleep(300 * time.Millisecond)
		c.String(200, "done")
	})
	if err := g.Run(); err != nil {
		t.Fatal(err)
	}

	res := make(chan string, 1)
	go func() {
		r, err := http.Get("http://127.0.0.1:" + itoa(port) + "/slow")
		if err != nil {
			res <- err.Error()
			return
		}
		b, _ := io.ReadAll(r.Body)
		res <- string(b)
	}()

	time.Sleep(100 * time.Millisecond)
	if err := g.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := <-res; got != "done" {
		t.Fatalf("in-flight request not completed: %s", got)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
