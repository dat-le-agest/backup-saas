package sctx

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"core/logger"
	zaplogger "core/logger/zap"

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
		if _, exists := s.store[c.ID()]; exists {
			return
		}

		s.components = append(s.components, c)
		s.store[c.ID()] = c
	}
}

// NewServiceContext creates a service context and parses configuration flags.
func NewServiceContext(opts ...Option) (ServiceContext, error) {
	s := &serviceCtx{
		store: make(map[string]Component),
	}

	for _, opt := range opts {
		opt(s)
	}

	s.initFlags()
	if err := s.parseFlags(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *serviceCtx) initFlags() {
	flag.StringVar(
		&s.env,
		"app-env",
		DevEnv,
		"Env for service: dev | stg | prd",
	)

	defaultLogger.InitFlags()

	for _, c := range s.components {
		c.InitFlags()
	}
}

func (s *serviceCtx) Load() error {
	if defaultLogger.GetLevel() == "" {
		level := "info"

		switch s.env {
		case DevEnv:
			level = "debug"
		case StgEnv:
			level = "info"
		case PrdEnv:
			level = "warn"
		}

		if err := defaultLogger.SetLevel(level); err != nil {
			return fmt.Errorf("set log level: %w", err)
		}
	}

	if err := defaultLogger.Activate(); err != nil {
		return fmt.Errorf("activate logger: %w", err)
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

			rollbackErr := s.stopComponents(s.components[:i])

			_ = defaultLogger.Stop()
			s.logger = nil

			return errors.Join(activateErr, rollbackErr)
		}
	}

	return nil
}

func (s *serviceCtx) Stop() error {
	if s.logger != nil {
		s.logger.Info("stopping service context")
	}

	err := s.stopComponents(s.components)

	if stopErr := defaultLogger.Stop(); stopErr != nil {
		err = errors.Join(err, stopErr)
	}

	s.logger = nil

	return err
}

func (s *serviceCtx) stopComponents(components []Component) error {
	var errs []error

	for i := len(components) - 1; i >= 0; i-- {
		c := components[i]

		if s.logger != nil {
			s.logger.Infof("stopping component: %s", c.ID())
		}

		if err := c.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", c.ID(), err))
		}
	}

	return errors.Join(errs...)
}

func (s *serviceCtx) Logger(prefix string) logger.Logger {
	return defaultLogger.GetLogger(prefix)
}

func (s *serviceCtx) LogLevel() string {
	return defaultLogger.GetLevel()
}

func (s *serviceCtx) Get(id string) (interface{}, bool) {
	c, ok := s.store[id]
	return c, ok
}

func GetAs[T any](s ServiceContext, id string) (T, bool) {
	var zero T

	c, ok := s.Get(id)
	if !ok {
		return zero, false
	}

	v, ok := c.(T)
	return v, ok
}

// warning: this function will cause panic if component doesn't exists
func MustGetAs[T any](s ServiceContext, id string) T {
	v, ok := GetAs[T](s, id)
	if !ok {
		panic(fmt.Sprintf("component %s not found or has wrong type", id))
	}

	return v
}

func (s *serviceCtx) EnvName() string {
	return s.env
}

func (s *serviceCtx) GetName() string {
	return s.name
}

func (s *serviceCtx) parseFlags() error {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
	}

	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			return fmt.Errorf("load env file %s: %w", envFile, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check env file %s: %w", envFile, err)
	}

	var parseErr error

	flag.VisitAll(func(f *flag.Flag) {
		if parseErr != nil {
			return
		}

		envKey := strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))

		value, exists := os.LookupEnv(envKey)
		if !exists {
			return
		}

		if err := flag.Set(f.Name, value); err != nil {
			parseErr = fmt.Errorf(
				"set flag %s from environment variable %s: %w",
				f.Name,
				envKey,
				err,
			)
		}
	})

	if parseErr != nil {
		return parseErr
	}

	flag.Parse()

	return nil
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
