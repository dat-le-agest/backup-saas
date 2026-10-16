package sctx

import (
	"core/logger"
	zaplogger "core/logger/zap"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

const (
	DevEnv = "dev"
	StgEnv = "stg"
	PrdEnv = "prd"
)

type Component interface {
	ID() string
	InitFlags()
	Activate(ServiceContext) error
	Stop() error
}

type ServiceContext interface {
	Load() error
	Stop() error

	Logger(prefix string) logger.Logger
	LogLevel() string

	EnvName() string
	GetName() string
	Get(id string) (interface{}, bool)
	MustGet(id string) interface{}

	OutEnv()
}

type serviceCtx struct {
	name       string
	env        string
	components []Component
	store      map[string]Component
	logger     logger.Logger
}

var defaultLogger = zaplogger.NewZapLogger()

type Option func(*serviceCtx)

func WithName(name string) Option {
	return func(s *serviceCtx) {
		s.name = name
	}
}

func WithComponent(c Component) Option {
	return func(s *serviceCtx) {
		if _, ok := s.store[c.ID()]; !ok {
			s.components = append(s.components, c)
			s.store[c.ID()] = c
		}
	}
}

func NewServiceContext(opts ...Option) ServiceContext {
	s := &serviceCtx{
		store: make(map[string]Component),
	}

	for _, opt := range opts {
		opt(s)
	}

	s.initFlags()
	s.parseFlags()

	return s
}

func (s *serviceCtx) initFlags() {
	flag.StringVar(&s.env, "app-env", DevEnv, "Env for service: dev | stg | prd")
	defaultLogger.InitFlags()
	for _, c := range s.components {
		c.InitFlags()
	}
}

func (s *serviceCtx) Load() error {
	if defaultLogger.GetLevel() == "" {
		switch s.env {
		case DevEnv:
			_ = defaultLogger.SetLevel("debug")
		case StgEnv:
			_ = defaultLogger.SetLevel("info")
		case PrdEnv:
			_ = defaultLogger.SetLevel("warn")
		default:
			_ = defaultLogger.SetLevel("info")
		}
	}

	if err := defaultLogger.Activate(); err != nil {
		return err
	}

	s.logger = s.Logger("service-context")
	s.logger.Infof(
		"service starting env=%s log_level=%s",
		s.env,
		defaultLogger.GetLevel(),
	)

	for i, c := range s.components {
		s.logger.Infof("activating component: %s", c.ID())
		if err := c.Activate(s); err != nil {
			activateErr := fmt.Errorf("activate %s: %w", c.ID(), err)
			// rollback components that were already activated
			return errors.Join(activateErr, s.stopComponents(s.components[:i]))
		}
	}
	return nil
}

func (s *serviceCtx) Stop() error {
	s.logger.Info("stopping service context")
	err := s.stopComponents(s.components)
	_ = defaultLogger.Stop()
	return err
}

// stopComponents stops in reverse activation order and keeps going on error.
func (s *serviceCtx) stopComponents(components []Component) error {
	var errs []error
	for i := len(components) - 1; i >= 0; i-- {
		c := components[i]
		s.logger.Infof("stopping component: %s", c.ID())
		if err := c.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", c.ID(), err))
		}
	}
	return errors.Join(errs...)
}

func (s *serviceCtx) Logger(prefix string) logger.Logger {
	return defaultLogger.GetLogger(prefix)
}

func (s *serviceCtx) Get(id string) (interface{}, bool) {
	c, ok := s.store[id]
	return c, ok
}

func (s *serviceCtx) MustGet(id string) interface{} {
	c, ok := s.Get(id)
	if !ok {
		panic(fmt.Sprintf("cannot get component %s", id))
	}
	return c
}

// GetAs returns component id cast to T (usually an interface exported by the component package).
func GetAs[T any](s ServiceContext, id string) (T, bool) {
	var zero T
	c, ok := s.Get(id)
	if !ok {
		return zero, false
	}
	v, ok := c.(T)
	return v, ok
}

func MustGetAs[T any](s ServiceContext, id string) T {
	v, ok := GetAs[T](s, id)
	if !ok {
		panic(fmt.Sprintf("component %s not found or has wrong type", id))
	}
	return v
}

func (s *serviceCtx) LogLevel() string {
	return defaultLogger.GetLevel()
}

func (s *serviceCtx) EnvName() string { return s.env }
func (s *serviceCtx) GetName() string { return s.name }

func (s *serviceCtx) parseFlags() {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
	}

	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			log.Fatalf("load env file %s: %v", envFile, err)
		}
	}

	// parse flag to env format
	flag.VisitAll(func(f *flag.Flag) {
		envKey := strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v := os.Getenv(envKey); v != "" {
			flag.Set(f.Name, v)
		}
	})

	flag.Parse()
}

func (s *serviceCtx) OutEnv() {
	fmt.Println("Resolved environment variables:")

	flag.VisitAll(func(f *flag.Flag) {
		envKey := strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))

		value := f.Value.String()
		source := "default"
		if value != f.DefValue {
			source = "override"
		}

		fmt.Printf(
			"%-30s = %-20s (%s)\n    ↳ %s\n\n",
			envKey,
			value,
			source,
			f.Usage,
		)
	})
}
