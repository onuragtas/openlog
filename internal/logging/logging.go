// Package logging configures structured JSON logging via log/slog.
package logging

import (
	"log/slog"
	"os"
)

// New returns a JSON logger writing to stdout at the given level and installs
// it as the slog default. service is added to every record.
func New(level, service string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	l := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})).With("service", service)
	slog.SetDefault(l)
	return l
}
