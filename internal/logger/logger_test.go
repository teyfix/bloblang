package logger

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/teyfix/bloblang/internal/config"
)

func TestNewLogger(t *testing.T) {
	cfg := &config.Config{
		LogLevel: "debug",
	}
	logger := NewLogger(cfg)
	assert.NotNil(t, logger)

	// Since we set log level to debug, it should be enabled for debug.
	assert.True(t, logger.Enabled(nil, slog.LevelDebug))

	cfg2 := &config.Config{
		LogLevel: "warn",
	}
	logger2 := NewLogger(cfg2)
	assert.NotNil(t, logger2)
	assert.False(t, logger2.Enabled(nil, slog.LevelInfo))
	assert.True(t, logger2.Enabled(nil, slog.LevelWarn))
}
