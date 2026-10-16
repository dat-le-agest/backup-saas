package zaplogger

import (
	"core/logger"
	"errors"
	"flag"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type appLogger struct {
	level       string
	atomicLevel zap.AtomicLevel
	logger      *zap.Logger
}

func NewZapLogger() logger.AppLogger {
	zl, _ := zap.NewProduction()

	return &appLogger{
		level:       "",
		atomicLevel: zap.NewAtomicLevel(),
		logger:      zl,
	}
}

func (a *appLogger) InitFlags() {
	flag.StringVar(
		&a.level,
		"log-level",
		"",
		"Log level: debug | info | warn | error",
	)
}

func (a *appLogger) Activate() error {
	if a.level == "" {
		return errors.New("log level cannot be empty")
	}

	lv, err := zapcore.ParseLevel(a.level)
	if err != nil {
		return err
	}

	a.atomicLevel.SetLevel(lv)

	var cfg zap.Config
	if lv == zap.DebugLevel {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}

	cfg.Level = a.atomicLevel

	logger, err := cfg.Build(
		zap.AddCaller(),
		zap.AddCallerSkip(1),
		// structured fields carry the context; stacks are only useful for real
		// panics, which recovery handlers already log with "stack"
		zap.AddStacktrace(zapcore.DPanicLevel),
	)
	if err != nil {
		return err
	}

	a.logger = logger
	return nil
}

func (a *appLogger) GetLogger(prefix string) logger.Logger {
	return &zapLogger{
		sugar: a.logger.
			With(zap.String("prefix", prefix)).
			Sugar(),
	}
}

func (a *appLogger) GetLevel() string {
	return a.level
}

func (a *appLogger) SetLevel(level string) error {
	lv, err := zapcore.ParseLevel(level)
	if err != nil {
		return err
	}
	a.level = level
	a.atomicLevel.SetLevel(lv)
	return nil
}

func (a *appLogger) Stop() error {
	if a.logger != nil {
		_ = a.logger.Sync()
	}
	return nil
}
