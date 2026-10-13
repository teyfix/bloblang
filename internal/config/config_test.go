package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfigDefaults(t *testing.T) {
	cfg, err := Load()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	assert.Equal(t, "https://docs.redpanda.com/redpanda-connect/guides/bloblang", cfg.BloblangDocsURL)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, 200*time.Millisecond, cfg.DiagnosticsDebounce)
	assert.Equal(t, 150000, cfg.MaxInlineDocumentBytes)
	assert.Equal(t, 100, cfg.MaxInlineResultBytes)
	assert.Equal(t, 1000, cfg.PartialExecCacheSize)
	assert.Equal(t, 5*time.Minute, cfg.PartialExecCacheTTL)
}

func TestConfigEnvironmentOverride(t *testing.T) {
	os.Setenv("BLOBLANG_LSP_LOG_LEVEL", "debug")
	os.Setenv("BLOBLANG_LSP_DIAGNOSTICS_DEBOUNCE", "500ms")
	defer func() {
		os.Unsetenv("BLOBLANG_LSP_LOG_LEVEL")
		os.Unsetenv("BLOBLANG_LSP_DIAGNOSTICS_DEBOUNCE")
	}()

	cfg, err := Load()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 500*time.Millisecond, cfg.DiagnosticsDebounce)
}

func TestInvalidEditorConfigDoesNotPreventStartup(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, ".bloblangrc.json"), []byte(`{"preview":`), 0600))
	cfg, err := Load()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 100, cfg.MaxInlineResultBytes)
}
