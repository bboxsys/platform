package letsencrypt

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog"
)

// LegoLogger adapts zerolog to lego's StdLogger interface
type LegoLogger struct {
	logger zerolog.Logger
}

// NewLegoLogger creates a new lego-compatible logger wrapping zerolog
func NewLegoLogger(logger zerolog.Logger) *LegoLogger {
	return &LegoLogger{logger: logger}
}

// Fatal logs a fatal message and exits
func (l *LegoLogger) Fatal(args ...interface{}) {
	l.logger.Fatal().Msg(fmt.Sprint(args...))
}

// Fatalln logs a fatal message with newline and exits
func (l *LegoLogger) Fatalln(args ...interface{}) {
	l.logger.Fatal().Msg(strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
}

// Fatalf logs a formatted fatal message and exits
func (l *LegoLogger) Fatalf(format string, args ...interface{}) {
	l.logger.Fatal().Msg(fmt.Sprintf(format, args...))
}

// Print logs an info message
func (l *LegoLogger) Print(args ...interface{}) {
	l.logger.Info().Msg(fmt.Sprint(args...))
}

// Println logs an info message with newline
func (l *LegoLogger) Println(args ...interface{}) {
	l.logger.Info().Msg(strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
}

// Printf logs a formatted info message
func (l *LegoLogger) Printf(format string, args ...interface{}) {
	l.logger.Info().Msg(fmt.Sprintf(format, args...))
}
