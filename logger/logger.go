package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

var (
	infoLogger  zerolog.Logger
	errorLogger zerolog.Logger
)

func Init() {
	zerolog.TimeFieldFormat = time.RFC3339

	if os.Getenv("NODE_ENV") == "production" || os.Getenv("PM2_HOME") != "" {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	infoLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}).
		With().Timestamp().Logger()

	errorLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
		With().Timestamp().Logger()
}

func Debug() *zerolog.Event {
	return infoLogger.Debug()
}

func Info() *zerolog.Event {
	return infoLogger.Info()
}

func Warn() *zerolog.Event {
	return errorLogger.Warn()
}

func Error() *zerolog.Event {
	return errorLogger.Error()
}

func Fatal() *zerolog.Event {
	return errorLogger.Fatal()
}
