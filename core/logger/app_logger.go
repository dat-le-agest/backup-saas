package logger

type AppLogger interface {
	InitFlags()
	Activate() error
	Stop() error

	GetLogger(prefix string) Logger
	GetLevel() string
	SetLevel(string) error
}
