package logger

import (
	"log/slog"
	"os"
	"strings"

	"github.com/teyfix/bloblang/internal/config"
)

func NewLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	// We MUST write logs to os.Stderr, as os.Stdout is reserved for the LSP JSON-RPC transport stream.
	handler := slog.NewTextHandler(os.Stderr, opts)
	return slog.New(handler)
}
