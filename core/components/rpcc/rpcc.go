// Package rpcc is the sctx component for calling another service over gRPC (Connect).
//
//	sctx.WithComponent(rpcc.NewClient("note-client", "note-service"))
//	// flags: --note-service-url, --note-service-timeout
//	// env:   NOTE_SERVICE_URL,   NOTE_SERVICE_TIMEOUT
//
//	cc := sctx.MustGetAs[rpcc.ClientComponent](sc, "note-client")
//	notes := rpcc.New(cc, notev1connect.NewNoteServiceClient)
package rpcc

import (
	"core/logger"
	"core/rpc"
	"core/sctx"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
)

const defaultTimeout = 10

type ClientComponent interface {
	// Enabled is false for an Optional client whose URL is not set.
	Enabled() bool
	HTTPClient() connect.HTTPClient
	BaseURL() string
	Options() []connect.ClientOption
}

// New builds a generated Connect client (e.g. notev1connect.NewNoteServiceClient)
// on top of the component's connection, URL and interceptors.
func New[T any](
	c ClientComponent,
	ctor func(connect.HTTPClient, string, ...connect.ClientOption) T,
	opts ...connect.ClientOption,
) T {
	return ctor(c.HTTPClient(), c.BaseURL(), append(c.Options(), opts...)...)
}

type rpcClient struct {
	id       string
	prefix   string
	optional bool
	rawURL   string
	timeout  int

	logger     logger.Logger
	telemetry  connect.Interceptor
	baseURL    string
	transport  *http.Transport
	httpClient *http.Client
}

type Option func(*rpcClient)

// Optional makes the URL optional: without it the client is disabled
// (Enabled() is false) instead of failing at startup. For upstreams a service
// can work without, e.g. the central authz service (RBAC fallback).
func Optional() Option { return func(c *rpcClient) { c.optional = true } }

// NewClient: id is the sctx component ID, prefix names the flags (<prefix>-url, <prefix>-timeout).
func NewClient(id, prefix string, opts ...Option) *rpcClient {
	c := &rpcClient{id: id, prefix: prefix}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *rpcClient) Enabled() bool { return c.httpClient != nil }

func (c *rpcClient) ID() string {
	return c.id
}

func (c *rpcClient) InitFlags() {
	flag.StringVar(
		&c.rawURL,
		fmt.Sprintf("%s-url", c.prefix),
		"",
		fmt.Sprintf("base URL of %s, e.g. http://note:3000 (http:// uses h2c)", c.prefix),
	)

	flag.IntVar(
		&c.timeout,
		fmt.Sprintf("%s-timeout", c.prefix),
		defaultTimeout,
		"default deadline in seconds for unary calls without one, 0 = none - Default 10",
	)
}

func (c *rpcClient) Activate(serviceCtx sctx.ServiceContext) error {
	c.logger = serviceCtx.Logger(c.id)

	if c.rawURL == "" {
		if c.optional {
			c.logger.Infof("rpc client disabled (no %s-url)", c.prefix)
			return nil
		}
		return fmt.Errorf("%s-url is required", c.prefix)
	}

	u, err := url.Parse(c.rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid %s-url %q: expected http(s)://host:port", c.prefix, c.rawURL)
	}
	c.baseURL = u.String()

	// gRPC needs HTTP/2: h2c for http://, regular HTTP/2 over TLS for https://
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	c.transport = &http.Transport{
		Protocols:           protocols,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	c.httpClient = &http.Client{Transport: c.transport}

	if c.telemetry, err = rpc.ClientTelemetryInterceptor(); err != nil {
		return fmt.Errorf("rpc client telemetry: %w", err)
	}

	c.logger.Infof("rpc client ready (url=%s, timeout=%ds)", c.baseURL, c.timeout)
	return nil
}

func (c *rpcClient) Stop() error {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}

func (c *rpcClient) HTTPClient() connect.HTTPClient {
	if c.httpClient == nil {
		panic(errors.New("rpc client " + c.id + " not activated"))
	}
	return c.httpClient
}

func (c *rpcClient) BaseURL() string {
	return c.baseURL
}

func (c *rpcClient) Options() []connect.ClientOption {
	return []connect.ClientOption{
		connect.WithGRPC(),
		connect.WithInterceptors(
			c.telemetry,
			rpc.ClientMetadataInterceptor(),
			rpc.ClientTimeoutInterceptor(time.Duration(c.timeout)*time.Second),
			rpc.ClientErrorInterceptor(),
		),
	}
}
