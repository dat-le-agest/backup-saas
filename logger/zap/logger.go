package zaplogger

import (
	"core/logger"

	"go.uber.org/zap"
)

type zapLogger struct {
	sugar *zap.SugaredLogger
}

func (l *zapLogger) Debug(args ...interface{}) {
	l.sugar.Debug(args...)
}
func (l *zapLogger) Info(args ...interface{}) {
	l.sugar.Info(args...)
}
func (l *zapLogger) Warn(args ...interface{}) {
	l.sugar.Warn(args...)
}
func (l *zapLogger) Error(args ...interface{}) {
	l.sugar.Error(args...)
}

func (l *zapLogger) Debugf(format string, args ...interface{}) {
	l.sugar.Debugf(format, args...)
}
func (l *zapLogger) Infof(format string, args ...interface{}) {
	l.sugar.Infof(format, args...)
}
func (l *zapLogger) Warnf(format string, args ...interface{}) {
	l.sugar.Warnf(format, args...)
}
func (l *zapLogger) Errorf(format string, args ...interface{}) {
	l.sugar.Errorf(format, args...)
}

func (l *zapLogger) With(key string, value interface{}) logger.Logger {
	return &zapLogger{
		sugar: l.sugar.With(key, value),
	}
}

func (l *zapLogger) WithFields(fields logger.Fields) logger.Logger {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return &zapLogger{
		sugar: l.sugar.With(args...),
	}
}
