package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	BloblangDocsURL        string        `mapstructure:"bloblang_docs_url"`
	LogLevel               string        `mapstructure:"log_level"`
	DiagnosticsDebounce    time.Duration `mapstructure:"diagnostics_debounce"`
	MaxInlineDocumentBytes int           `mapstructure:"max_inline_document_bytes"`
	MaxInlineResultBytes   int           `mapstructure:"max_inline_result_bytes"`
	PartialExecCacheSize   int           `mapstructure:"partial_exec_cache_size"`
	PartialExecCacheTTL    time.Duration `mapstructure:"partial_exec_cache_ttl"`
}

func Load() (*Config, error) {
	v := viper.New()

	v.SetConfigName(".bloblangrc")

	v.AddConfigPath(".")
	if home, err := os.UserHomeDir(); err == nil {
		v.AddConfigPath(home)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		v.AddConfigPath(filepath.Join(xdg, "bloblang-lsp"))
	} else if home, err := os.UserHomeDir(); err == nil {
		v.AddConfigPath(filepath.Join(home, ".config", "bloblang-lsp"))
	}

	v.SetDefault("bloblang_docs_url", "https://docs.redpanda.com/redpanda-connect/guides/bloblang")
	v.SetDefault("log_level", "info")
	v.SetDefault("diagnostics_debounce", 200*time.Millisecond)
	v.SetDefault("max_inline_document_bytes", 150000)
	v.SetDefault("max_inline_result_bytes", 100)
	v.SetDefault("partial_exec_cache_size", 1000)
	v.SetDefault("partial_exec_cache_ttl", 5*time.Minute)

	keys := []string{
		"bloblang_docs_url",
		"log_level",
		"diagnostics_debounce",
		"max_inline_document_bytes",
		"max_inline_result_bytes",
		"partial_exec_cache_size",
		"partial_exec_cache_ttl",
	}

	for _, key := range keys {
		envVar := "BLOBLANG_LSP_" + strings.ToUpper(key)
		if err := v.BindEnv(key, envVar); err != nil {
			return nil, err
		}
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok && filepath.Base(v.ConfigFileUsed()) != ".bloblangrc.json" {
			return nil, err
		}
		// Workspace editor JSON is validated independently; invalid editor settings
		// must not prevent the language server from starting.
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
