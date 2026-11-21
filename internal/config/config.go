// Package config reads Bragi's runtime settings from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config holds process-level settings. Everything users change at runtime
// (indexers, download clients, libraries) lives in the database instead.
type Config struct {
	Port      int
	ConfigDir string
	LogLevel  slog.Level
}

// FromEnv reads BRAGI_PORT, BRAGI_CONFIG_DIR and BRAGI_LOG_LEVEL.
func FromEnv() (Config, error) {
	cfg := Config{Port: 8787, ConfigDir: "./config", LogLevel: slog.LevelInfo}

	if v := os.Getenv("BRAGI_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return cfg, fmt.Errorf("BRAGI_PORT must be a port number, got %q", v)
		}
		cfg.Port = p
	}
	if v := os.Getenv("BRAGI_CONFIG_DIR"); v != "" {
		cfg.ConfigDir = v
	}
	if v := os.Getenv("BRAGI_LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			cfg.LogLevel = slog.LevelDebug
		case "info":
			cfg.LogLevel = slog.LevelInfo
		case "warn", "warning":
			cfg.LogLevel = slog.LevelWarn
		case "error":
			cfg.LogLevel = slog.LevelError
		default:
			return cfg, fmt.Errorf("BRAGI_LOG_LEVEL must be debug, info, warn or error, got %q", v)
		}
	}
	return cfg, nil
}
